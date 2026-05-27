# ClassCache desktop

A Wails (Go + native WebView) demo client that talks to a running
`cluster-classcache` deployment. Built so professors, students, and other
non-developers can poke around the system without touching kubectl.

What you see at a glance:

- **Sidebar** — every `ClassCache` CR in every namespace, with its phase
  badge (Pending / PrimerReady / WorkloadPatched / Ready / Failed).
- **Detail card** — archive key, profile, workload, and the peer set
  (`SMEMBERS archive:<key>:peers` in Valkey).
- **Live savings panel** — Σ Rss, Σ Pss, derived savings, `Shared_Clean`
  across every JVM running an archive-mmap, refreshed every 2 s, with a
  sparkline for the last 2 minutes.

## How it talks to the cluster

The Go backend shells out to `kubectl` and `valkey-cli` (or `redis-cli`,
as a fallback). That keeps the binary self-contained — no kubeconfig
parser, no client-go, no TLS / OIDC / exec-credential machinery. The
same pattern the C CLI in `modules/cli/` uses.

For the live smaps sample on a kind cluster, the backend also runs
`docker exec <node> cat /proc/<pid>/smaps` — same idea as
`modules/cli/src/stats.c`. A future patch will fall back to `kubectl
exec -c app -- cat /proc/1/smaps` for k3d / hosted clusters where the
host PID namespace is not visible from outside the node container.

## Run it (dev)

```
cd modules/desktop
wails dev
```

## Build a redistributable

```
wails build
open build/bin/desktop.app
```

The resulting `.app` is a standard macOS bundle. Drop it into
`/Applications` or `.dmg` it. No external dependencies are bundled —
`kubectl` and `valkey-cli`/`redis-cli` must be on the user's `$PATH`.
