/*
 * top.c — `classcache top`.
 *
 * Clear screen + re-run cmd_stats every `interval_sec`. Plain ANSI; works in
 * any terminal that handles ESC[2J. No ncurses, no double-buffering, no
 * partial diffing — we just reprint. Stats output is small enough that the
 * flicker is barely visible.
 *
 * Stop with Ctrl-C.
 */

#include "classcache.h"

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t g_stop = 0;
static void on_sigint(int sig) { (void)sig; g_stop = 1; }

int cmd_top(struct vk *v, int interval_sec)
{
    if (interval_sec <= 0) interval_sec = 2;

    /* Install Ctrl-C handler so we exit cleanly after the current sleep. */
    struct sigaction sa = {0};
    sa.sa_handler = on_sigint;
    sigaction(SIGINT, &sa, NULL);

    while (!g_stop) {
        fmt_clear_screen();

        time_t now = time(NULL);
        struct tm tm;
        localtime_r(&now, &tm);
        char tbuf[32];
        strftime(tbuf, sizeof(tbuf), "%Y-%m-%d %H:%M:%S", &tm);

        printf("classcache top — refresh every %ds — %s   (Ctrl-C to exit)\n\n",
               interval_sec, tbuf);

        int rc = cmd_stats(v);
        if (rc != 0)
            return rc;

        /* Sleep in 1s chunks so Ctrl-C feels responsive. */
        for (int i = 0; i < interval_sec && !g_stop; i++)
            sleep(1);
    }

    printf("\n[stopped]\n");
    return 0;
}
