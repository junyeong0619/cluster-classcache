/*
 * format.c — tiny output helpers (colors honoring NO_COLOR + KiB → MB string).
 */

#include "classcache.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int fmt_color_enabled(void)
{
    if (getenv("NO_COLOR") != NULL) return 0;
    return isatty(fileno(stdout));
}

void fmt_header(const char *title)
{
    int color = fmt_color_enabled();
    if (color) printf("\x1b[1m");          /* bold */
    printf("%s\n", title);
    if (color) printf("\x1b[0m");
    /* Underline of dashes the same width as the title. */
    size_t n = strlen(title);
    for (size_t i = 0; i < n && i < 79; i++) putchar('-');
    putchar('\n');
}

void fmt_clear_screen(void)
{
    /* ESC[H = cursor home, ESC[2J = clear entire screen. */
    fputs("\x1b[H\x1b[2J", stdout);
    fflush(stdout);
}

void fmt_kib(uint64_t kib, char *buf, size_t bufsz)
{
    if (kib < 1024)
        snprintf(buf, bufsz, "%llu KB", (unsigned long long)kib);
    else if (kib < 1024ull * 1024)
        snprintf(buf, bufsz, "%.1f MB", (double)kib / 1024.0);
    else
        snprintf(buf, bufsz, "%.2f GB", (double)kib / (1024.0 * 1024.0));
}
