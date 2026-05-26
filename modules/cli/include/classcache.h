#ifndef CLASSCACHE_H
#define CLASSCACHE_H

#include <stddef.h>
#include <stdint.h>

/* ---- smaps.c ----
 * Sum smaps entries whose pathname matches a substring (we use ".jsa").
 * `pid` may be a host PID or a "<container>:<pid>" pair (see smaps.c for the
 * container variant). All sizes are in kilobytes, matching /proc directly.
 */
struct smaps_totals {
    uint64_t rss;
    uint64_t pss;
    uint64_t shared_clean;
    uint64_t shared_dirty;
    uint64_t private_clean;
    uint64_t private_dirty;
};

/* Read /proc/<pid>/smaps directly. Returns 0 on success. */
int smaps_read_local(int pid, const char *needle, struct smaps_totals *out);

/* Read smaps from inside a kind worker via `docker exec`. Returns 0 on success.
 * Useful when classcache runs on the host but the workload pods live inside
 * kindest/node containers. kind shares the host PID namespace so docker
 * exec into a node container can `cat /proc/<pid>/smaps` directly.
 */
int smaps_read_kind(const char *kind_node, int pid, const char *needle,
                    struct smaps_totals *out);

/* Read smaps from inside a Pod via `kubectl exec`. Required when the cluster
 * runtime does NOT share the host PID namespace with node containers (k3d,
 * managed K8s, bare-metal kubelet). PID 1 inside the pod is the workload
 * JVM, so we read /proc/1/smaps from there.
 */
int smaps_read_pod(const char *ns, const char *pod, const char *needle,
                   struct smaps_totals *out);

/* ---- valkey.c ----
 * Light wrapper over hiredis for the queries we actually need.
 */
struct vk;
struct vk *vk_connect(const char *host, int port);
void vk_close(struct vk *v);

/* Return a NULL-terminated array of archive keys (caller frees with vk_strv_free).
 * Looks up keys matching "archive:<16-hex>" — i.e. excludes :peers, :build_lock.
 */
char **vk_list_archives(struct vk *v, size_t *count_out);

/* Return a NULL-terminated array of "<host>:<port>" endpoints holding the key. */
char **vk_list_peers(struct vk *v, const char *archive_key, size_t *count_out);

/* Archive metadata (size, jvm, arch, registered_at). NULL fields if not set. */
struct vk_archive_meta {
    uint64_t size_bytes;
    char    *jvm;       /* malloc'd, may be NULL */
    char    *arch;      /* malloc'd, may be NULL */
    uint64_t registered_at;
};
int  vk_archive_meta(struct vk *v, const char *key, struct vk_archive_meta *out);
void vk_archive_meta_free(struct vk_archive_meta *m);

void vk_strv_free(char **v, size_t n);

/* GET archive:<key>:build_lock — returns the holder ("<host>:<port>") if any.
 * Caller frees. Returns NULL when no lock exists. */
char *vk_archive_build_lock(struct vk *v, const char *key);

/* Subscribe to the "primer-events" channel and invoke `cb` for each message.
 * Blocks until the connection drops or cb returns non-zero.
 * The JSON string passed to cb is owned by hiredis — copy if you need it. */
typedef int (*vk_event_cb)(const char *json_payload, void *userdata);
int vk_subscribe_events(struct vk *v, vk_event_cb cb, void *userdata);

/* ---- format.c ---- */
void fmt_header(const char *title);
void fmt_kib(uint64_t kib, char *buf, size_t bufsz);   /* "37.1 MB" style */
int  fmt_color_enabled(void);                          /* honors NO_COLOR */

/* ---- kube.c ----
 * Light wrappers around kubectl. We deliberately shell out to kubectl
 * instead of speaking the K8s REST API directly: kubectl is already
 * authenticated through the user's kubeconfig, and parsing kubeconfig +
 * minting tokens + handling TLS in C just to duplicate that is wasted code.
 * If you need to run without kubectl on PATH, swap in libcurl here.
 */
struct cc_classcache {
    char *name;
    char *ns;
    char *workload_name;
    char *archive_key;     /* may be empty string if status not yet populated */
    char *phase;
};

struct cc_pod {
    char *name;
    char *ns;
    char *node;            /* spec.nodeName — for kind, this is the kindest/node container name */
};

int  kube_list_classcaches(struct cc_classcache **out, size_t *n_out);
void kube_classcaches_free(struct cc_classcache *v, size_t n);

int  kube_list_workload_pods(const char *ns, const char *workload_name,
                             struct cc_pod **out, size_t *n_out);
void kube_pods_free(struct cc_pod *v, size_t n);

/* Find java PIDs of workload JVMs inside a kind worker container.
 * Looks for processes whose cmdline contains /work/extracted/app.jar.
 */
int  kube_find_java_pids(const char *kind_node, int **out, size_t *n_out);

/* ---- stats.c / events.c / top.c ---- */
int cmd_stats(struct vk *v);
int cmd_events(struct vk *v);
int cmd_top(struct vk *v, int interval_sec);

/* Clear screen + move cursor home (ANSI). */
void fmt_clear_screen(void);

#endif /* CLASSCACHE_H */
