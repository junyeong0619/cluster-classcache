/*
 * smaps.c — parse /proc/<pid>/smaps and sum entries matching a substring.
 *
 * The shape of /proc/<pid>/smaps:
 *
 *   <addr-range> <perms> <offset> <dev> <inode>  <pathname>
 *   Size:                4 kB
 *   Rss:                 4 kB
 *   Pss:                 4 kB
 *   Shared_Clean:        0 kB
 *   ...
 *   VmFlags: rd mr mw me
 *
 * We treat the header line as "block start" if its pathname column matches
 * the needle (substring), then accumulate the numeric fields until VmFlags
 * closes the block.
 *
 * Output values are in KiB, exactly like the file contents (no unit conversion).
 */

#include "classcache.h"

#include <ctype.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <sys/types.h>
#include <sys/wait.h>

static int parse_kib_field(const char *line, const char *prefix, uint64_t *out)
{
    size_t plen = strlen(prefix);
    if (strncmp(line, prefix, plen) != 0)
        return 0;
    /* Skip prefix, then any whitespace, then read the number. */
    const char *p = line + plen;
    while (*p == ' ' || *p == '\t') p++;
    char *end = NULL;
    unsigned long long v = strtoull(p, &end, 10);
    if (end == p)
        return 0;
    *out += (uint64_t)v;
    return 1;
}

static int smaps_consume(FILE *fp, const char *needle, struct smaps_totals *t)
{
    char *line = NULL;
    size_t cap = 0;
    int in_block = 0;

    memset(t, 0, sizeof(*t));

    while (getline(&line, &cap, fp) != -1) {
        /* Header lines look like:
         *   "00400000-00500000 r--p 00000000 fd:00 12345  /path/to/file.jsa"
         * We detect them by presence of '-' before the first space (an
         * address range) AND containing the needle substring.
         */
        char *dash = strchr(line, '-');
        char *first_space = strchr(line, ' ');
        if (dash && first_space && dash < first_space) {
            in_block = (needle && strstr(line, needle)) ? 1 : 0;
            continue;
        }

        if (!in_block)
            continue;

        if (strncmp(line, "VmFlags:", 8) == 0) {
            in_block = 0;
            continue;
        }

        parse_kib_field(line, "Rss:",             &t->rss);
        parse_kib_field(line, "Pss:",             &t->pss);
        parse_kib_field(line, "Shared_Clean:",    &t->shared_clean);
        parse_kib_field(line, "Shared_Dirty:",    &t->shared_dirty);
        parse_kib_field(line, "Private_Clean:",   &t->private_clean);
        parse_kib_field(line, "Private_Dirty:",   &t->private_dirty);
    }

    free(line);
    return ferror(fp) ? -1 : 0;
}

int smaps_read_local(int pid, const char *needle, struct smaps_totals *out)
{
    char path[64];
    snprintf(path, sizeof(path), "/proc/%d/smaps", pid);
    FILE *fp = fopen(path, "r");
    if (!fp) {
        fprintf(stderr, "smaps_read_local: %s: %s\n", path, strerror(errno));
        return -1;
    }
    int rc = smaps_consume(fp, needle, out);
    fclose(fp);
    return rc;
}

/*
 * smaps_read_kind:
 *   docker exec <kind-node> cat /proc/<pid>/smaps  →  pipe → smaps_consume
 *
 * We deliberately use the plain `docker` CLI here rather than libdocker.
 * Keeps the binary dependency-free vs. yet another C library, and matches
 * what every demo script already does.
 */
int smaps_read_kind(const char *kind_node, int pid, const char *needle,
                    struct smaps_totals *out)
{
    char cmd[256];
    int n = snprintf(cmd, sizeof(cmd),
                     "docker exec %s cat /proc/%d/smaps 2>/dev/null",
                     kind_node, pid);
    if (n < 0 || (size_t)n >= sizeof(cmd))
        return -1;

    FILE *fp = popen(cmd, "r");
    if (!fp) {
        fprintf(stderr, "smaps_read_kind: popen failed: %s\n", strerror(errno));
        return -1;
    }

    int rc = smaps_consume(fp, needle, out);
    int wait_status = pclose(fp);
    if (rc == 0 && wait_status != 0) {
        /* docker exec failed (no such container, pid missing, etc.) */
        return -1;
    }
    return rc;
}
