/*
 * stats.c — `classcache stats`.
 *
 * Composes data from three sources into one screen:
 *   1. K8s API (via kubectl)  — ClassCaches + their workload Pods + node names
 *   2. Valkey directory       — archive metadata + peer set per archive
 *   3. /proc/<pid>/smaps      — read inside each kind worker via docker exec
 *
 * Output is one-shot, plain text. The goal is "screenshot this and it
 * justifies the project" — measured savings, peer distribution, who built
 * vs. who pulled.
 */

#include "classcache.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* ----- helpers ----- */

static void print_kib_row(const char *label, uint64_t kib)
{
    char b[32];
    fmt_kib(kib, b, sizeof(b));
    printf("  %-22s %10s\n", label, b);
}

static int contains(const char *hay, const char *needle)
{
    return hay && needle && strstr(hay, needle) != NULL;
}

/* Aggregate smaps totals for every workload JVM on `kind_node`. */
static int aggregate_node(const char *kind_node, struct smaps_totals *out, int *n_jvms)
{
    int *pids = NULL;
    size_t npids = 0;
    *n_jvms = 0;
    memset(out, 0, sizeof(*out));

    if (kube_find_java_pids(kind_node, &pids, &npids) != 0)
        return -1;

    for (size_t i = 0; i < npids; i++) {
        struct smaps_totals t = {0};
        if (smaps_read_kind(kind_node, pids[i], ".jsa", &t) != 0) continue;
        if (t.rss == 0) continue;   /* no archive VMA on this PID; skip */
        out->rss           += t.rss;
        out->pss           += t.pss;
        out->shared_clean  += t.shared_clean;
        out->shared_dirty  += t.shared_dirty;
        out->private_clean += t.private_clean;
        out->private_dirty += t.private_dirty;
        (*n_jvms)++;
    }
    free(pids);
    return 0;
}

/* ----- node list dedup -----
 * One archive can live on multiple nodes, and we'd query smaps per node
 * once total. So collect a deduped node set across all workload pods.
 */
struct node_set {
    char **names;
    size_t n;
    size_t cap;
};

static int node_set_add(struct node_set *s, const char *name)
{
    if (!name || !*name) return 0;
    for (size_t i = 0; i < s->n; i++)
        if (strcmp(s->names[i], name) == 0) return 0;
    if (s->n == s->cap) {
        size_t nc = s->cap ? s->cap * 2 : 8;
        char **nb = realloc(s->names, nc * sizeof(char *));
        if (!nb) return -1;
        s->names = nb;
        s->cap = nc;
    }
    s->names[s->n++] = strdup(name);
    return 0;
}

static void node_set_free(struct node_set *s)
{
    for (size_t i = 0; i < s->n; i++) free(s->names[i]);
    free(s->names);
}

/* ----- main entry ----- */

int cmd_stats(struct vk *v)
{
    /* === 1) ClassCaches === */
    struct cc_classcache *ccs = NULL;
    size_t n_ccs = 0;
    if (kube_list_classcaches(&ccs, &n_ccs) != 0) {
        fprintf(stderr, "stats: kubectl get classcaches failed\n");
        return 1;
    }

    fmt_header("CLASSCACHES");
    if (n_ccs == 0) {
        printf("(none)\n\n");
    } else {
        printf("  %-16s  %-10s  %-18s  %-18s  %s\n",
               "NAME", "NS", "ARCHIVE KEY", "PHASE", "WORKLOAD");
        for (size_t i = 0; i < n_ccs; i++) {
            const char *k = ccs[i].archive_key && *ccs[i].archive_key
                            ? ccs[i].archive_key : "(none yet)";
            printf("  %-16s  %-10s  %-18s  %-18s  %s\n",
                   ccs[i].name, ccs[i].ns, k,
                   ccs[i].phase && *ccs[i].phase ? ccs[i].phase : "?",
                   ccs[i].workload_name);
        }
        printf("\n");
    }

    /* === 2) Archive distribution (Valkey) === */
    size_t n_keys = 0;
    char **keys = vk_list_archives(v, &n_keys);

    fmt_header("ARCHIVE DISTRIBUTION");
    if (!keys || n_keys == 0) {
        printf("(no archives registered in Valkey)\n\n");
    } else {
        printf("  %-18s  %10s  %5s  PEERS\n", "KEY", "SIZE", "COUNT");
        for (size_t i = 0; i < n_keys; i++) {
            struct vk_archive_meta m;
            vk_archive_meta(v, keys[i], &m);
            char szbuf[32];
            fmt_kib(m.size_bytes / 1024, szbuf, sizeof(szbuf));

            size_t np;
            char **peers = vk_list_peers(v, keys[i], &np);
            printf("  %-18s  %10s  %5zu  ", keys[i], szbuf, np);
            for (size_t j = 0; j < np; j++)
                printf("%s%s", peers[j], j + 1 < np ? ", " : "");
            printf("\n");
            vk_strv_free(peers, np);
            vk_archive_meta_free(&m);
        }
        printf("\n");
    }
    vk_strv_free(keys, n_keys);

    /* === 3) Memory sharing per node ===
     * Collect every workload pod across every ClassCache, dedup nodes,
     * then sample smaps once per node. */
    struct node_set nodes = {0};
    for (size_t i = 0; i < n_ccs; i++) {
        struct cc_pod *pods = NULL;
        size_t np = 0;
        if (kube_list_workload_pods(ccs[i].ns, ccs[i].workload_name, &pods, &np) != 0)
            continue;
        for (size_t j = 0; j < np; j++)
            node_set_add(&nodes, pods[j].node);
        kube_pods_free(pods, np);
    }

    fmt_header("MEMORY SHARING (live smaps, archive VMA only)");
    if (nodes.n == 0) {
        printf("(no running workload pods to sample)\n\n");
    } else {
        printf("  %-22s  %5s  %10s  %10s  %10s  %7s\n",
               "NODE", "JVMs", "Σ Rss", "Σ Pss", "Saved", "Pss/Rss");

        uint64_t tot_rss = 0, tot_pss = 0, tot_sc = 0;
        int      tot_jvms = 0;
        int      node_errors = 0;

        for (size_t i = 0; i < nodes.n; i++) {
            struct smaps_totals t;
            int n_jvms = 0;
            if (aggregate_node(nodes.names[i], &t, &n_jvms) != 0) {
                node_errors++;
                printf("  %-22s  (docker exec failed — is this a kind cluster?)\n",
                       nodes.names[i]);
                continue;
            }
            char rss_b[16], pss_b[16], saved_b[16];
            fmt_kib(t.rss, rss_b, sizeof(rss_b));
            fmt_kib(t.pss, pss_b, sizeof(pss_b));
            uint64_t saved = t.rss > t.pss ? t.rss - t.pss : 0;
            fmt_kib(saved, saved_b, sizeof(saved_b));

            double ratio = t.rss > 0 ? (100.0 * (double)t.pss / (double)t.rss) : 0.0;

            printf("  %-22s  %5d  %10s  %10s  %10s  %6.1f%%\n",
                   nodes.names[i], n_jvms, rss_b, pss_b, saved_b, ratio);

            tot_rss   += t.rss;
            tot_pss   += t.pss;
            tot_sc    += t.shared_clean;
            tot_jvms  += n_jvms;
        }

        if (tot_rss > 0) {
            char rss_b[16], pss_b[16], saved_b[16], sc_b[16];
            fmt_kib(tot_rss, rss_b, sizeof(rss_b));
            fmt_kib(tot_pss, pss_b, sizeof(pss_b));
            uint64_t saved = tot_rss - tot_pss;
            fmt_kib(saved, saved_b, sizeof(saved_b));
            fmt_kib(tot_sc, sc_b, sizeof(sc_b));
            double ratio = 100.0 * (double)tot_pss / (double)tot_rss;
            printf("  %-22s\n", "  ----------------------------------------------------------------");
            printf("  %-22s  %5d  %10s  %10s  %10s  %6.1f%%\n",
                   "TOTAL", tot_jvms, rss_b, pss_b, saved_b, ratio);
            printf("\n");
            print_kib_row("Σ Shared_Clean (mmap)", tot_sc);
            print_kib_row("Saved (Σ Rss − Σ Pss)", saved);

            /* Quick interpretation. */
            if (tot_jvms > 1) {
                double ideal = 100.0 / (double)tot_jvms;
                printf("  %-22s  %9.1f%% (lower is better; ideal for %d JVMs = %.1f%%)\n",
                       "Pss/Rss explainer", ratio, tot_jvms, ideal);
            }
        }
        printf("\n");
    }
    node_set_free(&nodes);

    kube_classcaches_free(ccs, n_ccs);
    (void)contains;  /* reserved for future filters */
    return 0;
}
