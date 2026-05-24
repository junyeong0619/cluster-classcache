/*
 * events.c — `classcache events`.
 *
 * Subscribes to the "primer-events" Valkey channel and prints each message.
 * The primer publishes a small JSON document every time a node finishes
 * acquiring an archive — either by build, pull, or local-hit:
 *
 *   {"node":"node-a","key":"99cd...","method":"built-locally",
 *    "elapsed_ms":3032,"archive_size":35913728}
 *
 * Useful for live demos and debugging "did primer X actually finish?" without
 * tailing pod logs.
 */

#include "classcache.h"

#include <cjson/cJSON.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static void timestamp(char *buf, size_t n)
{
    time_t t = time(NULL);
    struct tm tm;
    localtime_r(&t, &tm);
    strftime(buf, n, "%H:%M:%S", &tm);
}

static int on_event(const char *payload, void *userdata)
{
    (void)userdata;
    char ts[16];
    timestamp(ts, sizeof(ts));

    /* Try to parse and pretty-print; on failure, dump raw. */
    cJSON *root = cJSON_Parse(payload);
    if (!root) {
        printf("[%s] %s\n", ts, payload);
        return 0;
    }

    const char *node = NULL, *key = NULL, *method = NULL;
    long long   elapsed = 0, size = 0;

    cJSON *x;
    if ((x = cJSON_GetObjectItem(root, "node"))   && cJSON_IsString(x)) node   = x->valuestring;
    if ((x = cJSON_GetObjectItem(root, "key"))    && cJSON_IsString(x)) key    = x->valuestring;
    if ((x = cJSON_GetObjectItem(root, "method")) && cJSON_IsString(x)) method = x->valuestring;
    if ((x = cJSON_GetObjectItem(root, "elapsed_ms"))   && cJSON_IsNumber(x)) elapsed = (long long)x->valuedouble;
    if ((x = cJSON_GetObjectItem(root, "archive_size")) && cJSON_IsNumber(x)) size    = (long long)x->valuedouble;

    char szbuf[32];
    fmt_kib((uint64_t)size / 1024, szbuf, sizeof(szbuf));

    printf("[%s] %-12s  %-30s  %5lld ms  %s  key=%s\n",
           ts,
           node   ? node   : "?",
           method ? method : "?",
           elapsed, szbuf,
           key    ? key    : "?");
    fflush(stdout);

    cJSON_Delete(root);
    return 0;
}

int cmd_events(struct vk *v)
{
    /* In case stdout is a pipe (e.g., piped to head/tee), fully-buffered
     * output would swallow our first lines. Use line buffering. */
    setvbuf(stdout, NULL, _IOLBF, 0);

    printf("Listening on 'primer-events' (Ctrl-C to stop)...\n\n");
    fflush(stdout);
    int rc = vk_subscribe_events(v, on_event, NULL);
    if (rc != 0) {
        fprintf(stderr, "subscribe failed (Valkey connection dropped?)\n");
        return 1;
    }
    return 0;
}
