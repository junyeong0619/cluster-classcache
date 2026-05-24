/*
 * kube.c — kubectl + cJSON wrappers.
 *
 * Why kubectl instead of libcurl talking to the API server?
 *   - kubectl already handles kubeconfig, current-context, TLS, OIDC tokens.
 *   - We'd have to reimplement all of that in C otherwise.
 *   - Cost is one fork+exec per call; this CLI is not on a hot path.
 *
 * Each function:
 *   1. popen("kubectl ... -o json")
 *   2. slurp stdout
 *   3. cJSON_Parse
 *   4. walk items[] into the small structs declared in classcache.h
 */

#include "classcache.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>

#include <cjson/cJSON.h>

/* Read all output of a popen'd command into a malloc'd buffer.
 * Returns the buffer (caller frees) and exit status via *exit_status. */
static char *slurp_cmd(const char *cmd, int *exit_status)
{
    FILE *fp = popen(cmd, "r");
    if (!fp) return NULL;

    size_t cap = 8192, len = 0;
    char *buf = malloc(cap);
    if (!buf) { pclose(fp); return NULL; }

    for (;;) {
        if (len + 4096 > cap) {
            cap *= 2;
            char *nb = realloc(buf, cap);
            if (!nb) { free(buf); pclose(fp); return NULL; }
            buf = nb;
        }
        size_t n = fread(buf + len, 1, 4096, fp);
        if (n == 0) break;
        len += n;
    }
    buf[len] = '\0';

    int rc = pclose(fp);
    if (exit_status) *exit_status = WIFEXITED(rc) ? WEXITSTATUS(rc) : -1;
    return buf;
}

static char *dup_or_empty(cJSON *node)
{
    if (cJSON_IsString(node) && node->valuestring)
        return strdup(node->valuestring);
    return strdup("");
}

int kube_list_classcaches(struct cc_classcache **out, size_t *n_out)
{
    *out = NULL;
    *n_out = 0;

    int ec = 0;
    char *raw = slurp_cmd(
        "kubectl get classcaches.classcache.dev -A -o json 2>/dev/null", &ec);
    if (!raw) return -1;
    if (ec != 0) {
        free(raw);
        return -1;
    }

    cJSON *root = cJSON_Parse(raw);
    free(raw);
    if (!root) return -1;

    cJSON *items = cJSON_GetObjectItem(root, "items");
    if (!cJSON_IsArray(items)) { cJSON_Delete(root); return 0; }

    size_t n = cJSON_GetArraySize(items);
    struct cc_classcache *arr = calloc(n, sizeof(*arr));
    if (!arr) { cJSON_Delete(root); return -1; }

    size_t kept = 0;
    cJSON *it = NULL;
    cJSON_ArrayForEach(it, items) {
        cJSON *meta   = cJSON_GetObjectItem(it, "metadata");
        cJSON *spec   = cJSON_GetObjectItem(it, "spec");
        cJSON *status = cJSON_GetObjectItem(it, "status");
        cJSON *wlref  = spec ? cJSON_GetObjectItem(spec, "workloadRef") : NULL;

        arr[kept].name          = dup_or_empty(meta   ? cJSON_GetObjectItem(meta,   "name")        : NULL);
        arr[kept].ns            = dup_or_empty(meta   ? cJSON_GetObjectItem(meta,   "namespace")   : NULL);
        arr[kept].workload_name = dup_or_empty(wlref  ? cJSON_GetObjectItem(wlref,  "name")        : NULL);
        arr[kept].archive_key   = dup_or_empty(status ? cJSON_GetObjectItem(status, "archiveKey")  : NULL);
        arr[kept].phase         = dup_or_empty(status ? cJSON_GetObjectItem(status, "phase")       : NULL);
        kept++;
    }
    cJSON_Delete(root);

    *out = arr;
    *n_out = kept;
    return 0;
}

void kube_classcaches_free(struct cc_classcache *v, size_t n)
{
    if (!v) return;
    for (size_t i = 0; i < n; i++) {
        free(v[i].name);
        free(v[i].ns);
        free(v[i].workload_name);
        free(v[i].archive_key);
        free(v[i].phase);
    }
    free(v);
}

int kube_list_workload_pods(const char *ns, const char *workload_name,
                            struct cc_pod **out, size_t *n_out)
{
    *out = NULL;
    *n_out = 0;
    if (!ns || !workload_name) return -1;

    char cmd[512];
    snprintf(cmd, sizeof(cmd),
             "kubectl -n %s get pod -l app=%s -o json 2>/dev/null",
             ns, workload_name);

    int ec = 0;
    char *raw = slurp_cmd(cmd, &ec);
    if (!raw) return -1;
    if (ec != 0) { free(raw); return -1; }

    cJSON *root = cJSON_Parse(raw);
    free(raw);
    if (!root) return -1;

    cJSON *items = cJSON_GetObjectItem(root, "items");
    if (!cJSON_IsArray(items)) { cJSON_Delete(root); return 0; }

    size_t n = cJSON_GetArraySize(items);
    struct cc_pod *arr = calloc(n, sizeof(*arr));
    if (!arr) { cJSON_Delete(root); return -1; }

    size_t kept = 0;
    cJSON *it = NULL;
    cJSON_ArrayForEach(it, items) {
        cJSON *meta   = cJSON_GetObjectItem(it, "metadata");
        cJSON *spec   = cJSON_GetObjectItem(it, "spec");
        cJSON *status = cJSON_GetObjectItem(it, "status");

        /* Skip pods that aren't Running — they don't have JVMs yet. */
        const char *phase = NULL;
        cJSON *ph = status ? cJSON_GetObjectItem(status, "phase") : NULL;
        if (cJSON_IsString(ph)) phase = ph->valuestring;
        if (!phase || strcmp(phase, "Running") != 0) continue;

        arr[kept].name = dup_or_empty(meta ? cJSON_GetObjectItem(meta, "name")      : NULL);
        arr[kept].ns   = dup_or_empty(meta ? cJSON_GetObjectItem(meta, "namespace") : NULL);
        arr[kept].node = dup_or_empty(spec ? cJSON_GetObjectItem(spec, "nodeName")  : NULL);
        kept++;
    }
    cJSON_Delete(root);

    *out = arr;
    *n_out = kept;
    return 0;
}

void kube_pods_free(struct cc_pod *v, size_t n)
{
    if (!v) return;
    for (size_t i = 0; i < n; i++) {
        free(v[i].name);
        free(v[i].ns);
        free(v[i].node);
    }
    free(v);
}

int kube_find_java_pids(const char *kind_node, int **out, size_t *n_out)
{
    *out = NULL;
    *n_out = 0;
    if (!kind_node || !*kind_node) return -1;

    /* All workload JVMs run via /work/extracted/app.jar — see the v0.9 primer
     * + workload patch. pgrep -f against that string yields exactly the JVMs
     * we want to sample. */
    char cmd[256];
    snprintf(cmd, sizeof(cmd),
             "docker exec %s pgrep -f '/work/extracted/app.jar' 2>/dev/null",
             kind_node);

    int ec = 0;
    char *raw = slurp_cmd(cmd, &ec);
    if (!raw) return -1;
    /* pgrep returns 1 when nothing matches — that's not an error for us. */

    size_t cap = 16, n = 0;
    int *arr = calloc(cap, sizeof(int));
    if (!arr) { free(raw); return -1; }

    char *save = NULL;
    for (char *tok = strtok_r(raw, "\n", &save); tok; tok = strtok_r(NULL, "\n", &save)) {
        int pid = atoi(tok);
        if (pid <= 0) continue;
        if (n == cap) {
            cap *= 2;
            int *nb = realloc(arr, cap * sizeof(int));
            if (!nb) { free(arr); free(raw); return -1; }
            arr = nb;
        }
        arr[n++] = pid;
    }
    free(raw);

    *out = arr;
    *n_out = n;
    return 0;
}
