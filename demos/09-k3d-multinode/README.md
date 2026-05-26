# demos/09 — k3d 3-node verification

The previous K8s demo (`demos/06`) runs on **kind**, where all "nodes" are
processes inside a single Docker container. P2P pulls in that environment
never traverse a real network boundary.

**k3d** gives you N separate Docker containers, one per node, joined by a
Docker bridge network. It's still not real multi-host (managed K8s would be),
but it's the strongest signal you can get from a laptop:

| | kind | k3d | EKS/GKE |
|---|---|---|---|
| Worker isolation | shared container | separate containers | separate VMs |
| P2P traverses a bridge | ❌ loopback | ✅ Docker bridge | ✅ real network |
| Host PID namespace | ✅ (so `docker exec ... pgrep` sees workload JVMs) | ❌ (k3s uses its own namespace) | ❌ |

## Running

```bash
brew install k3d
./demos/09-k3d-multinode/run.sh
```

The script creates a 1-server + 3-agent k3d cluster (`cc-k3d`), installs
cert-manager, builds + imports the operator/primer/agent/demo-app images,
then applies `examples/quickstart.yaml`.

Cleanup:
```bash
k3d cluster delete cc-k3d
```

## What was actually measured

| Metric | Result |
|---|---|
| **Total time to `Ready`** | t=34s (vs t=11~15s on kind — k3d's k3s init is slower) |
| **Number of nodes** | 4 (1 server + 3 agents) |
| **Same archive key as kind** | ✅ `99cdff82d2f81455` (sha256 determinism holds across cluster runtimes) |
| **Primer DaemonSet pods** | 4/4 ready, one per node |
| **Peer set** | 4 entries, one PodIP per node |
| **Cross-bridge P2P pull** | ✅ implicit (one primer built, three primers pulled across the k3d Docker bridge) |
| **Workload pod spread** | 3 pods across server + 2 agents (topology spread effective) |

Live `classcache stats` output captured during the run:
```
ARCHIVE DISTRIBUTION
  KEY                       SIZE  COUNT  PEERS
  99cdff82d2f81455       33.4 MB      4  10.42.0.5:8088, 10.42.1.5:8088,
                                         10.42.3.4:8088, 10.42.2.5:8088
```

## Known limitation: smaps not available on k3d

`classcache stats` reads `/proc/<pid>/smaps` by `docker exec`-ing into each
node container and running `pgrep -f /work/extracted/app.jar`. That works on
kind because kind nodes share the host PID namespace; k3d's k3s nodes do
not. So the **MEMORY SHARING** section of `stats` shows 0 KB on k3d.

This isn't a regression — it's a property of k3d. Two ways to recover the
measurement:

1. **Read smaps from inside the workload pod** instead of from the node:
   ```bash
   kubectl -n cc-demo exec <workload-pod> -- cat /proc/1/smaps \
       | awk '/\.jsa/{f=1} f && /^Rss/{print}'
   ```
2. **Future cli enhancement**: `classcache stats` could detect "host-PID
   namespace not available" and fall back to `kubectl exec` per pod.
   Tracked as v0.11.

## What this demo adds vs. demos/06

- Same operator + primer + ClassCache machinery, different cluster runtime.
- **Independent evidence of sha256-key determinism**: kind and k3d produce
  the same `99cdff82d2f81455`. The input tuple `(app, agent, JVM, arch,
  profile)` is identical in both, so the key matches — exactly what the
  design promises.
- **First evidence of real network-bridge P2P pulls**: four primer pods on
  four separate node containers, three of which pulled from the one that
  built.
