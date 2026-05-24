/*
 * classcache — CLI to introspect cluster-classcache runtime state.
 *
 * Subcommands (more to come):
 *   classcache archives            list archive keys + sizes + peer counts
 *   classcache peers <archive-key> list peers holding a given archive
 *
 * Connection:
 *   VALKEY_HOST / VALKEY_PORT envs (default: 127.0.0.1:6379).
 *   For kind, set up a port-forward first:
 *     kubectl -n cc-v7 port-forward svc/cc-realkey-valkey 6379:6379 &
 */

#include "classcache.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const char *env_or(const char *key, const char *def)
{
    const char *v = getenv(key);
    return (v && *v) ? v : def;
}

static int env_int(const char *key, int def)
{
    const char *v = getenv(key);
    return (v && *v) ? atoi(v) : def;
}

static void usage(void)
{
    fprintf(stderr,
        "classcache — inspect cluster-classcache runtime state\n"
        "\n"
        "Usage:\n"
        "  classcache stats                 one-shot full report\n"
        "  classcache top [interval-sec]    same as stats, refreshes every N s (default 2)\n"
        "  classcache archives              archive keys + sizes + peer counts\n"
        "  classcache peers <archive-key>   peers holding a given archive\n"
        "  classcache events                live tail of the primer-events channel\n"
        "\n"
        "Env:\n"
        "  VALKEY_HOST   default 127.0.0.1\n"
        "  VALKEY_PORT   default 6379\n"
        "  NO_COLOR      disable colored output\n"
        "\n"
        "Requirements:\n"
        "  * kubectl on PATH, configured to talk to your cluster\n"
        "  * docker on PATH if your cluster is kind (for smaps via docker exec)\n"
        "  * Valkey reachable on VALKEY_HOST:VALKEY_PORT — for kind, port-forward\n"
        "    e.g. `kubectl -n cc-v7 port-forward svc/<valkey-svc> 6379:6379`.\n"
        );
}

static int cmd_archives(struct vk *v)
{
    size_t n;
    char **keys = vk_list_archives(v, &n);
    if (!keys) return 1;

    fmt_header("ARCHIVES");
    if (n == 0) {
        printf("(none — has the primer registered any archive yet?)\n");
        vk_strv_free(keys, 0);
        return 0;
    }
    printf("%-18s  %10s  %5s  %s\n", "KEY", "SIZE", "PEERS", "JVM");
    for (size_t i = 0; i < n; i++) {
        struct vk_archive_meta m;
        if (vk_archive_meta(v, keys[i], &m) != 0) continue;

        size_t peer_n;
        char **peers = vk_list_peers(v, keys[i], &peer_n);
        vk_strv_free(peers, peer_n);

        char sizebuf[32];
        fmt_kib(m.size_bytes / 1024, sizebuf, sizeof(sizebuf));

        printf("%-18s  %10s  %5zu  %s\n",
               keys[i], sizebuf, peer_n,
               m.jvm ? m.jvm : "?");
        vk_archive_meta_free(&m);
    }
    vk_strv_free(keys, n);
    return 0;
}

static int cmd_peers(struct vk *v, const char *key)
{
    size_t n;
    char **peers = vk_list_peers(v, key, &n);
    if (!peers) return 1;

    char title[64];
    snprintf(title, sizeof(title), "PEERS for archive %s", key);
    fmt_header(title);
    if (n == 0) {
        printf("(no peers registered for this key)\n");
    } else {
        for (size_t i = 0; i < n; i++)
            printf("  %s\n", peers[i]);
    }
    vk_strv_free(peers, n);
    return 0;
}

int main(int argc, char **argv)
{
    if (argc < 2) { usage(); return 1; }

    const char *host = env_or("VALKEY_HOST", "127.0.0.1");
    int         port = env_int("VALKEY_PORT", 6379);

    struct vk *v = vk_connect(host, port);
    if (!v) {
        fprintf(stderr,
                "Could not connect to Valkey at %s:%d.\n"
                "If you're on kind, port-forward first, e.g.:\n"
                "  kubectl -n cc-v7 port-forward svc/cc-realkey-valkey 6379:6379 &\n",
                host, port);
        return 2;
    }

    int rc = 0;
    if (strcmp(argv[1], "stats") == 0) {
        rc = cmd_stats(v);
    } else if (strcmp(argv[1], "top") == 0) {
        int interval = (argc >= 3) ? atoi(argv[2]) : 2;
        rc = cmd_top(v, interval);
    } else if (strcmp(argv[1], "archives") == 0) {
        rc = cmd_archives(v);
    } else if (strcmp(argv[1], "peers") == 0) {
        if (argc < 3) { usage(); rc = 1; }
        else rc = cmd_peers(v, argv[2]);
    } else if (strcmp(argv[1], "events") == 0) {
        rc = cmd_events(v);
    } else {
        usage();
        rc = 1;
    }

    vk_close(v);
    return rc;
}
