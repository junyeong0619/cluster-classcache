# classcache — CLI

A tiny C program (~1k LOC, ~50 KB binary) for inspecting a running
cluster-classcache install. Reads three sources and stitches them
into one screen:

| Source | What we read | How |
|---|---|---|
| Kubernetes API | ClassCache CRs, workload Pods, node names | `kubectl get ... -o json` + cJSON |
| Valkey directory | archive metadata, peer set, build_lock | hiredis |
| `/proc/<pid>/smaps` | Pss / Rss / Shared_Clean for each workload JVM | direct read, or via `docker exec` for kind |

## Subcommands

```
classcache stats                 # one-shot full report
classcache top [interval-sec]    # same, but refreshes every N seconds (default 2)
classcache archives              # archive keys + sizes + peer count
classcache peers <archive-key>   # who holds a given archive
classcache events                # tail of the primer-events channel
```

## Building

```bash
# macOS
brew install hiredis cjson
make

# Debian/Ubuntu
sudo apt-get install -y libhiredis-dev libcjson-dev libcurl4-openssl-dev
make
```

Output: `./build/classcache`. Install with `make install` (also drops a
`kubectl-classcache` symlink so `kubectl classcache stats` works).

## Pointing it at a running cluster

The Valkey service inside the cluster usually isn't reachable from the host;
port-forward first:

```bash
kubectl -n cc-v7 port-forward svc/cc-zerobuild-valkey 6379:6379 &
./build/classcache stats
```

Or set a different host:

```bash
VALKEY_HOST=valkey.example.com VALKEY_PORT=6379 ./build/classcache stats
```

## What `stats` prints

```
CLASSCACHES
-----------
  NAME              NS          ARCHIVE KEY         PHASE               WORKLOAD
  zerobuild         cc-v7       99cdff82d2f81455    WorkloadPatched     zerobuild
  quickstart        cc-demo     99cdff82d2f81455    WorkloadPatched     quickstart

ARCHIVE DISTRIBUTION
--------------------
  KEY                       SIZE  COUNT  PEERS
  99cdff82d2f81455       33.4 MB      2  10.244.1.55:8088, 10.244.2.58:8088

MEMORY SHARING (live smaps, archive VMA only)
---------------------------------------------
  NODE                     JVMs      Σ Rss      Σ Pss       Saved  Pss/Rss
  cc-worker                   2     60.0 MB     44.9 MB     15.1 MB    74.8%
  cc-worker2                  2     60.2 MB     43.5 MB     16.7 MB    72.3%
  TOTAL                       4    120.2 MB     88.4 MB     31.8 MB    73.6%

  Σ Shared_Clean (mmap)    61.0 MB
  Saved (Σ Rss − Σ Pss)    31.8 MB
  Pss/Rss explainer           73.6% (lower is better; ideal for 4 JVMs = 25.0%)
```

The bottom block is the real win: `Σ Shared_Clean` is memory that's
physically present once but charged to multiple JVMs because they all
mmap the same archive file.

## Source layout

```
src/
├── main.c       subcommand dispatch
├── stats.c      composes K8s + Valkey + smaps into one report
├── top.c        ANSI-clear + cmd_stats + sleep loop
├── events.c     SUBSCRIBE primer-events + JSON pretty-print
├── kube.c       kubectl wrappers (cJSON-parsed)
├── valkey.c     hiredis wrappers (archives / peers / meta / build_lock / subscribe)
├── smaps.c      /proc/<pid>/smaps parser + docker-exec variant
└── format.c     color, KiB→MB, clear-screen
include/
└── classcache.h shared structs + prototypes
```

## Why kubectl shell-out instead of libcurl

`kubectl` already handles kubeconfig contexts, TLS, OIDC tokens. Re-implementing
that in C just to avoid a fork/exec gives nothing back. This CLI is invoked by
a human looking at a terminal — not a hot path.

## Why C (not Go)

To keep tooling honest with the rest of the systems-programming side of the
repo (uftrace, JFS work, valkey). hiredis + cJSON + libcurl is the canonical
C trio for this kind of glue.
