/*
 * valkey.c — thin hiredis wrapper for the directory queries we need.
 *
 * Valkey is wire-compatible with Redis, so hiredis works as-is.
 *
 * What we expose:
 *   - vk_connect / vk_close
 *   - vk_list_archives           → archive:<16-hex>            (excludes :peers, :build_lock)
 *   - vk_list_peers              → SMEMBERS archive:<key>:peers
 *   - vk_archive_meta            → HGETALL archive:<key>
 *
 * Everything returns malloc'd memory; callers free with vk_strv_free /
 * vk_archive_meta_free.
 */

#include "classcache.h"

#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <hiredis/hiredis.h>

struct vk {
    redisContext *ctx;
};

struct vk *vk_connect(const char *host, int port)
{
    redisContext *c = redisConnect(host, port);
    if (!c || c->err) {
        fprintf(stderr, "vk_connect: %s\n",
                c ? c->errstr : "no context");
        if (c) redisFree(c);
        return NULL;
    }
    struct vk *v = calloc(1, sizeof(*v));
    if (!v) {
        redisFree(c);
        return NULL;
    }
    v->ctx = c;
    return v;
}

void vk_close(struct vk *v)
{
    if (!v) return;
    if (v->ctx) redisFree(v->ctx);
    free(v);
}

void vk_strv_free(char **v, size_t n)
{
    if (!v) return;
    for (size_t i = 0; i < n; i++) free(v[i]);
    free(v);
}

/* Strict pattern check: "archive:<16-hex-chars>", no suffix.
 * (We don't want :peers or :build_lock to count as archive keys.)
 */
static int is_plain_archive_key(const char *key)
{
    static const char prefix[] = "archive:";
    if (strncmp(key, prefix, sizeof(prefix) - 1) != 0)
        return 0;
    const char *p = key + sizeof(prefix) - 1;
    int n = 0;
    while (p[n] && isxdigit((unsigned char)p[n])) n++;
    return n == 16 && p[n] == '\0';
}

char **vk_list_archives(struct vk *v, size_t *count_out)
{
    *count_out = 0;
    redisReply *r = redisCommand(v->ctx, "KEYS archive:*");
    if (!r) return NULL;
    if (r->type != REDIS_REPLY_ARRAY) { freeReplyObject(r); return NULL; }

    char **out = calloc(r->elements + 1, sizeof(char *));
    size_t kept = 0;
    for (size_t i = 0; i < r->elements; i++) {
        const char *key = r->element[i]->str;
        if (key && is_plain_archive_key(key))
            out[kept++] = strdup(key + strlen("archive:"));
    }
    freeReplyObject(r);
    *count_out = kept;
    return out;
}

char **vk_list_peers(struct vk *v, const char *archive_key, size_t *count_out)
{
    *count_out = 0;
    redisReply *r = redisCommand(v->ctx, "SMEMBERS archive:%s:peers", archive_key);
    if (!r) return NULL;
    if (r->type != REDIS_REPLY_ARRAY) { freeReplyObject(r); return NULL; }

    char **out = calloc(r->elements + 1, sizeof(char *));
    for (size_t i = 0; i < r->elements; i++)
        out[i] = strdup(r->element[i]->str ? r->element[i]->str : "");
    *count_out = r->elements;
    freeReplyObject(r);
    return out;
}

int vk_archive_meta(struct vk *v, const char *key, struct vk_archive_meta *out)
{
    memset(out, 0, sizeof(*out));
    redisReply *r = redisCommand(v->ctx, "HGETALL archive:%s", key);
    if (!r) return -1;
    if (r->type != REDIS_REPLY_ARRAY) { freeReplyObject(r); return -1; }

    /* HGETALL returns flat field/value pairs. */
    for (size_t i = 0; i + 1 < r->elements; i += 2) {
        const char *field = r->element[i]->str;
        const char *value = r->element[i + 1]->str;
        if (!field || !value) continue;

        if      (strcmp(field, "size") == 0)          out->size_bytes    = strtoull(value, NULL, 10);
        else if (strcmp(field, "registered_at") == 0) out->registered_at = strtoull(value, NULL, 10);
        else if (strcmp(field, "jvm")  == 0)          out->jvm  = strdup(value);
        else if (strcmp(field, "arch") == 0)          out->arch = strdup(value);
    }
    freeReplyObject(r);
    return 0;
}

void vk_archive_meta_free(struct vk_archive_meta *m)
{
    if (!m) return;
    free(m->jvm);  m->jvm  = NULL;
    free(m->arch); m->arch = NULL;
}

char *vk_archive_build_lock(struct vk *v, const char *key)
{
    redisReply *r = redisCommand(v->ctx, "GET archive:%s:build_lock", key);
    if (!r) return NULL;
    char *holder = NULL;
    if (r->type == REDIS_REPLY_STRING && r->str)
        holder = strdup(r->str);
    freeReplyObject(r);
    return holder;
}

int vk_subscribe_events(struct vk *v, vk_event_cb cb, void *userdata)
{
    redisReply *r = redisCommand(v->ctx, "SUBSCRIBE primer-events");
    if (!r) return -1;
    /* The first reply is the subscribe ACK ("subscribe", channel, count). */
    freeReplyObject(r);

    while (redisGetReply(v->ctx, (void **)&r) == REDIS_OK) {
        if (!r) break;
        /* Each message: ["message", "primer-events", "<payload>"]. */
        if (r->type == REDIS_REPLY_ARRAY &&
            r->elements == 3 &&
            r->element[0]->str &&
            strcmp(r->element[0]->str, "message") == 0 &&
            r->element[2]->str)
        {
            if (cb(r->element[2]->str, userdata) != 0) {
                freeReplyObject(r);
                return 0;
            }
        }
        freeReplyObject(r);
    }
    return v->ctx->err ? -1 : 0;
}
