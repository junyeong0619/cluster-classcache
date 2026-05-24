# cluster-classcache — Design Document (v0.5 baseline, updated through v0.9)

**Status**: PoC verification complete → entering production-readiness.
**Prerequisite**: All seven validation phases in v0.1–v0.4 passed (see `docs/REPORT.md`).

---

## 1. One-sentence definition

> Infrastructure that bakes Java-agent–transformed classes into a dynamic CDS archive, distributes that archive across nodes via a Valkey directory + P2P transfer, and lets N JVMs on the same node share the archive pages at the OS level.

Headline numbers (Spring Boot, 33 MB archive, 4 JVMs):
- **Physical memory saved per node**: 61 MB
- **JVM startup time**: 48% reduction (1115 → 577 ms)
- **New-node archive acquisition**: 50× faster (build 2.6 s → P2P pull 50 ms)
- **Runtime overhead**: 0 (workload pods do not run the agent)

---

## 2. System architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          K8s Cluster                                │
│                                                                     │
│   ┌─────────────────────────────────────────────────────────────┐   │
│   │  Valkey Service (directory only — metadata ~1 KB / archive) │   │
│   │     archive:{key}                — size, jvm, arch, built_at │   │
│   │     archive:{key}:peers          — {PodIP:8088, ...}         │   │
│   │     archive:{key}:build_lock     — SETNX lock (TTL 600s)    │   │
│   └─────────────────────────────────────────────────────────────┘   │
│                              ▲                                      │
│                              │ HSET/SADD/SETNX                      │
│           ┌──────────────────┼──────────────────┐                   │
│           ▼                  ▼                  ▼                   │
│      ┌──────────┐       ┌──────────┐      ┌──────────┐              │
│      │ Node A   │       │ Node B   │      │ Node C   │              │
│      │          │       │          │      │          │              │
│      │ Primer   │──P2P─►│ Primer   │──P2P►│ Primer   │              │
│      │ DaemonSet│ HTTP  │ DaemonSet│      │ DaemonSet│              │
│      │ pulls or │       │ pulls    │      │ pulls    │              │
│      │ builds   │       │          │      │          │              │
│      │   │      │       │   │      │      │   │      │              │
│      │   ▼      │       │   ▼      │      │   ▼      │              │
│      │ hostPath:/var/lib/classcache/{key}.jsa   (1 copy per node)   │
│      │   │      │       │   │      │      │   │      │              │
│      │   ▼ mmap (RO)    │   ▼      │      │   ▼      │              │
│      │ ┌──────┐ ┌──────┐│ ┌──────┐ │      │ ┌──────┐ │              │
│      │ │Pod₁  │ │Pod₂  ││ │Pod₁  │ │      │ │Pod₁  │ │              │
│      │ │JVM   │ │JVM   ││ │JVM   │ │      │ │JVM   │ │              │
│      │ │archive shared││ │      │ │      │ │      │ │              │
│      │ └──────┘ └──────┘│ └──────┘ │      │ └──────┘ │              │
│      └──────────┘       └──────────┘      └──────────┘              │
│       pages shared by OS  pages shared    pages shared              │
└─────────────────────────────────────────────────────────────────────┘
```

Core design decisions:
- Valkey holds **metadata only** (no binary blobs). For N archives × 33 MB the Valkey footprint stays well under 1 MB.
- Archive binaries move between nodes via **P2P HTTP** (Pod-to-Pod GET against PodIP:8088).
- Within a node, sharing is done by **OS mmap** (the same hostPath inode is read-only-mapped by every JVM, so pages are shared automatically).
- Workload pods use **only the archive — no agent process** → runtime overhead is zero.

---

## 3. Core components

### 3.1 Scouter v2.21 (default APM in v0.5)

OTel was originally the target default, but ingestion testing (§8.7) revealed a hard limit, so v0.5 ships Scouter instead.

OTel's limitations (full detail in `otel-trial/`):
- Transformed bytecode is archive-compatible (O.3b boots cleanly, zero `ClassNotFoundException`).
- **But the SDK initializes inside an isolated `AgentClassLoader` at premain time** — turning the agent off means no export (zero traces emitted).
- Even with agent on + archive, OTel's helper-class injection is non-deterministic, so transform targets like `DispatcherServlet` bypass the archive (jar fallback).
- True integration is a major v0.6 task (agent fork or extension).

Why Scouter (validated through X.3b and the k8s end-to-end demo):
- ✅ Deterministic transforms (the bytes in the archive match a fresh runtime transform).
- ✅ `Boot-Class-Path: scouter.agent.jar` self-reference manifest → fits the boot-classpath pattern perfectly.
- ✅ Rich auto-instrumentation (HTTP/JDBC/method profiling/Spring/async).
- ✅ Apache 2.0, Korean community.
- ⚠️ The collector (Scouter server) uses UDP transport — no direct Jaeger/Tempo (an adapter would be needed).

Deployment strategy:
| Phase | JVM options |
|-------|---------|
| **Primer (build)** | `-Xbootclasspath/a:scouter.agent.jar -javaagent:scouter.agent.jar -Dscouter.config=<path>` |
| **Workload (runtime)** | `-Xbootclasspath/a:scouter.agent.jar -Dscouter.config=<path>` (agent OFF) |

**Important**: the boot classpath fingerprint at build time must match the runtime fingerprint, otherwise CDS rejects the shared class paths. You have to set `-Xbootclasspath/a:` at build time as well — adding it only at runtime causes a JVM fail.

Pluggable alternatives:
- **In-house `ApmAgent v0.1`** (§6) — learning/debug tool, extremely archive-friendly.
- **OTel javaagent** (§8.7) — only partial integration (agent ON + archive). Full integration planned for v0.6.
- **Datadog Java agent** — determinism not yet validated. Future candidate.
- **Elastic APM** — future candidate.

### 3.2 Primer Pod (DaemonSet)

Language: Python (PoC) → Go (recommended for production, lighter footprint).

Lifecycle:
```
1. Pod start (scheduled to a node by k8s)
2. PEER_HOST = downward API status.podIP
3. key = sha256(app_jar || agent_jar || jvm_version || arch)[:16]
4. Local archive check — if present, register + start peer server, done.
5. Query peers from Valkey
   - If any → HTTP GET from the closest peer (with retries + SHA256 verify)
6. No peers → SETNX build_lock (TTL 600s)
   - Lock acquired → build (Spring Boot + agent + warmup + dump)
   - Lock lost     → poll for peer, pull once one shows up
7. Register self: SADD archive:{key}:peers ${PEER_HOST}:8088
8. Start the peer HTTP server (serves GET /archive/{key})
```

Resources:
- CPU: ~1 core during build (60 s), then idle (peer server, near zero).
- Memory: ~1 GB during build (Spring Boot warmup), then ~50 MB.
- Disk: hostPath `/var/lib/classcache/` — one copy per node (33 MB).
- Network: 33 MB × pull count during archive distribution.

### 3.3 Valkey directory

Since v0.6 we migrated from Redis (SSPL) to Valkey (BSD, Linux Foundation). Same wire protocol.

Data model:
```
HSET archive:<key>
    size           <bytes>
    registered_at  <unix-time>
    jvm            "OpenJDK 22.0.2"
    arch           "linux/aarch64"
    app_sha256     <full-hex>      # for verification
    agent_sha256   <full-hex>

SADD archive:<key>:peers
    <PodIP>:8088
    <PodIP>:8088
    ...

SETEX peer:<PodIP>:heartbeat 60 "alive"   # peers refresh every 30s
SET   archive:<key>:build_lock <PodIP> NX EX 600
```

Capacity estimate: 200 nodes × 50 archive types → roughly 5–10 MB. A single Valkey instance is plenty.

HA: If Valkey is down, **the per-node local archives keep working**. Only new nodes have to build from scratch → graceful degradation.

### 3.4 Workload pod (Spring Boot + user code)

JVM options:
```
-XX:+UnlockDiagnosticVMOptions
-XX:+AllowArchivingWithJavaAgent
-XX:SharedArchiveFile=/var/lib/classcache/<key>.jsa
-XX:ArchiveRelocationMode=0          # critical — decides intra-node share ratio
-Xshare:on                            # fail-fast instead of silently falling back
-Xbootclasspath/a:/opt/agent/opentelemetry-javaagent.jar
# No -javaagent: option here (zero runtime overhead)

# OTel runtime options (some SDK code reads these even without premain)
-DOTEL_EXPORTER_OTLP_ENDPOINT=...
-DOTEL_SERVICE_NAME=...
```

Volumes:
```yaml
volumes:
  - name: classcache
    hostPath:
      path: /var/lib/classcache
      type: Directory
volumeMounts:
  - name: classcache
    mountPath: /var/lib/classcache
    readOnly: true     # workload never modifies the archive
```

InitContainer that waits for the archive:
```yaml
initContainers:
  - name: wait-for-archive
    image: busybox
    command: ["sh","-c","until ls /var/lib/classcache/*.jsa; do sleep 1; done"]
    volumeMounts: [{ name: classcache, mountPath: /var/lib/classcache }]
```

### 3.5 P2P transfer (Pod-to-Pod HTTP)

Protocol:
- HTTP/1.1 `GET /archive/{key}`
- Range request support (resumable downloads)
- Response headers include `Content-Length` and `X-SHA256` (for verification)
- Chunked streaming (memory-efficient)

PoC: Python `http.server.ThreadingTCPServer` (kept it simple).
Production: Go + h2 + concurrent download limits + token-based auth.

Peer selection (v0.5 recommendation):
1. Prefer peers in the same zone (k8s topology labels) — intra-zone bandwidth is free.
2. Prefer peers with a fresh heartbeat.
3. Fall back to a random pick (load distribution).

---

## 4. Standardized JVM options (validated)

### 4.1 Build phase (inside the Primer Pod)
```bash
java \
  -XX:+UnlockDiagnosticVMOptions \      # required to enable AllowArchivingWithJavaAgent
  -XX:+AllowArchivingWithJavaAgent \    # diagnostic flag; lets agent-transformed classes enter the archive
  -XX:ArchiveClassesAtExit=app.jsa \    # dynamic CDS dump on JVM exit
  -javaagent:opentelemetry-javaagent.jar \
  -jar app.jar
# Warmup workload runs, SIGTERM, archive dumps.
```

### 4.2 Runtime phase (inside the Workload Pod)
```bash
java \
  -XX:+UnlockDiagnosticVMOptions \
  -XX:+AllowArchivingWithJavaAgent \    # required because the archive was built with the agent flag
  -XX:SharedArchiveFile=app.jsa \
  -XX:ArchiveRelocationMode=0 \         # all JVMs mmap at the same address → maximum page sharing
  -Xshare:on \                           # do not silently fall back when archive load fails
  -Xbootclasspath/a:opentelemetry-javaagent.jar \  # premain doesn't run; classes are still reachable
  -jar app.jar
```

### 4.3 Details we settled by measurement

| Option | Default | v0.5 recommendation | Reason |
|------|-------|----------|------|
| `ArchiveRelocationMode` | 1 | **0** | With 1, each JVM mmaps at a different address → fixup-induced COW → share ratio 12.6%. With 0, sharing reaches 83% (toy) and 64% (Spring Boot). |
| `Xshare` | auto | **on** | `auto` silently falls back when the archive is broken. In production, fail-fast. |
| `AllowArchivingWithJavaAgent` | (diagnostic) | **enabled** | Required prerequisite for an agent-transformed archive. |
| `ArchiveClassesAtExit` (build only) | (none) | dump path | dynamic CDS dump |
| `SharedArchiveFile` (runtime only) | (none) | archive path | archive usage |

---

## 5. Operational flows

### 5.1 Cold start (fresh cluster deployment)
1. Operator/Helm deploys Valkey + Primer DaemonSet.
2. Primer pods get scheduled (one per worker, N total).
3. **First node**: peers = 0 → SETNX wins → build (60 s).
4. **Other nodes**: peers = 0 → SETNX loses → poll → pull from the first node once it finishes (~1 s).
5. All primers now hold the archive on their hostPath.
6. Workload Deployment rolls out → initContainer confirms archive presence → Spring Boot starts (577 ms).

### 5.2 Warm start (new node joining an existing cluster)
1. Primer pod gets scheduled on the new node.
2. peers = {existing nodes} → pull from the closest peer (50 ms).
3. Workload pods on this node immediately use the archive.

### 5.3 Steady state
- Workload pods read-only mmap the hostPath archive.
- N JVMs on the same node share archive pages at the OS level.
- Primer sits idle; only the peer HTTP server is running.

### 5.4 New image rollout (image rolling update)
1. The new image has a new `(app_jar_hash, agent_jar_hash)` → **new key**.
2. Primer builds/distributes the new archive independent of the old one.
3. The old archive stays on hostPath (cleaned up by a GC policy).
4. Workload pods roll to the new image → mount the new archive.

### 5.5 Failure modes

| Failure | Impact | Response |
|------|------|------|
| Valkey down | New nodes can't discover peers → fall back to direct build (slow) | Graceful degradation. Local archives on existing nodes are unaffected. |
| Peer Pod down | Pull falls back to another peer | Automatic (there are N peers in the set). Stale peers get cleared via heartbeat TTL. |
| Archive file corrupted | JVM fails fast under `-Xshare:on` | Primer's SHA256 verify catches it earlier; on corruption it re-pulls. |
| Build node dies mid-build | After the `build_lock` TTL (600 s) another node retries | Lock reacquired → rebuild. |
| hostPath permission issues | Pod start fails | Standardize SecurityContext in the Helm chart. |

---

## 6. Standard K8s manifests (v0.5)

### 6.1 Namespace isolation
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: classcache-system
```

### 6.2 Valkey (metadata directory, v0.6+)
- Deployment with 1 replica (PoC) / StatefulSet with 3 replicas + Sentinel (production).
- Memory limit 256 MB (metadata only).
- Image: `valkey/valkey:7.2-alpine` (previously `redis:7-alpine` up to v0.5).

### 6.3 Primer DaemonSet
```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: primer
  namespace: classcache-system
spec:
  template:
    spec:
      hostNetwork: false      # PodIP is enough
      containers:
        - name: primer
          image: ghcr.io/<org>/cluster-classcache-primer:<v0.5>
          env:
            - name: NODE_NAME
              valueFrom: { fieldRef: { fieldPath: spec.nodeName } }
            - name: PEER_HOST
              valueFrom: { fieldRef: { fieldPath: status.podIP } }
            - name: VALKEY_HOST
              value: valkey.classcache-system.svc.cluster.local
          ports:
            - containerPort: 8088
              name: peer
          volumeMounts:
            - name: cache
              mountPath: /var/lib/classcache
          resources:
            requests: { cpu: "100m", memory: "128Mi" }
            limits:   { cpu: "2",    memory: "1Gi" }   # bursts during build
      tolerations:
        - operator: Exists      # may schedule on any worker node
      volumes:
        - name: cache
          hostPath: { path: /var/lib/classcache, type: DirectoryOrCreate }
```

### 6.4 Workload (user app)
Patch the user's Deployment:
```yaml
spec:
  template:
    spec:
      initContainers:
        - name: cc-wait-archive
          image: busybox:1.36
          command: ["sh","-c","until ls /var/lib/classcache/*.jsa; do sleep 1; done"]
          volumeMounts: [{ name: cc-cache, mountPath: /var/lib/classcache, readOnly: true }]
      containers:
        - name: app
          env:
            - name: JAVA_TOOL_OPTIONS
              value: >-
                -XX:+UnlockDiagnosticVMOptions
                -XX:+AllowArchivingWithJavaAgent
                -XX:SharedArchiveFile=/var/lib/classcache/{key}.jsa
                -XX:ArchiveRelocationMode=0
                -Xshare:on
                -Xbootclasspath/a:/opt/cc-agent/opentelemetry-javaagent.jar
          volumeMounts:
            - name: cc-cache
              mountPath: /var/lib/classcache
              readOnly: true
      volumes:
        - name: cc-cache
          hostPath: { path: /var/lib/classcache, type: Directory }
```

A Mutating Admission Webhook can automate this patch (the user only adds a single label/annotation).

### 6.5 Operator (follow-up in v0.6)
- CRD: `ClassCache` (defines app, agent, warmup workload).
- The operator manages the Primer Job/DaemonSet, the workload patch, and the Valkey directory.

---

## 7. Operational safety

### 7.1 Archive trustworthiness
- **SHA256 verify**: hash check after pull (etalon stored in Valkey).
- **Archive signing** (v0.6): Primer signs each archive; Workload verifies. Prevents supply-chain attacks.
- **`AllowArchivingWithJavaAgent` risk**: a diagnostic flag tagged "not for production". In v0.5 we accept it under the assumption that only trusted Primers produce archives. v0.6's archive signing mitigates this.

### 7.2 Peer authentication (v0.6)
- Pod-to-Pod HTTP is plaintext → cluster-internal traffic could be sniffed.
- Either TLS + mTLS (a service mesh like istio/linkerd) or token-based (k8s ServiceAccount JWT).

### 7.3 hostPath isolation
- `readOnly: true` workload mount (immutable).
- Only the Primer can write.
- AppArmor/seccomp profiles (v0.6).

### 7.4 Resource limits
- Primer's CPU burst is 1 core × 60 s — isolate with k8s ResourceQuota.
- Cap archive size (e.g., 200 MB) — block abnormal sizes.

### 7.5 Operational monitoring
- Primer pod metrics: build count, pull count, peer transfer bytes, build-lock contention.
- Workload pod metrics: archive HIT/MISS, `ClassNotFoundException` rate.
- Valkey metrics: peer set size, build_lock hold time.

---

## 8. Pluggable APM agent

OTel is the default, but any agent that meets the following criteria works as-is (validated with Scouter in §8):

| Criterion | How to verify |
|------|---------|
| **Deterministic transforms** | Same input → same byte output. Use the four-phase X.3b methodology from v0.4. |
| **Boot-classpath compatible** | Putting the agent jar on `-Xbootclasspath/a:` keeps transformed-code references reachable. Most agents have a `Boot-Class-Path` self-reference manifest, so this works out of the box. |
| **No retransform dependency** | The agent doesn't re-transform classes at runtime. OTel, Scouter, and Datadog all meet this. |
| **No hidden-class creation** | Hidden classes don't enter the archive. Avoid lambda metafactory tricks. |

Compatibility matrix:
| Agent | Deterministic | Boot CP | No retransform | Avoids hidden classes | Integration |
|-------|:---:|:---:|:---:|:---:|:---:|
| In-house ApmAgent v0.1 (ours) | ✅ | ✅ | ✅ | ✅ | **Validated (§6)** |
| Scouter v2.21 | ✅ | ✅ | ✅ | ✅ | **Validated (§8)** |
| OpenTelemetry Java agent | ❓ → to verify | ✅ (expected) | ❓ | ❓ | **v0.5 target** |
| Datadog Java agent | ❓ | ✅ (expected) | ❓ | ❓ | Future |
| New Relic | ❓ | ❓ | ❓ | ❓ | Future |
| Elastic APM | ❓ | ❓ | ❓ | ❓ | Future |

---

## 9. Out-of-scope scenarios

- **Environments with frequent hot redeploys**: code changes → archive key changes → rebuild every time. Diminishing returns.
- **Single huge node (one big machine)**: same-node mmap sharing is already available via plain AppCDS. v0.5's cross-pod distribution is less useful here.
- **Cross-cluster sync**: v0.5 is single-cluster only. Sharing archives across clusters belongs in a different layer.
- **AOT compilation (GraalVM native image)**: native binaries don't co-exist with JVM CDS.
- **Agents with heavy retransform usage (e.g. debuggers)**: conflicts with baked-in archive semantics.

---

## 10. Roadmap (v0.6+)

### v0.6 — Modularization & open-source baseline (done, see §13)
- Directory reorganization (`demos/` + `modules/` + `deploy/` + `docs/`).
- Redis → Valkey migration (BSD license, wire-compatible).
- AgentProfile schema + three reference profiles (`scouter` / `otel` / `apm-v01`).
- Go rewrite of the primer with 6 modules + miniredis-backed tests.
- Distroless image + `BUILDER` abstraction (docker/podman/nerdctl).

### v0.7 — Operator + Webhook + Helm (done, see §14)
- `ClassCache` CRD + controller-runtime reconciler.
- Owned / Webhook patch modes.
- Mutating Admission Webhook (TLS via cert-manager).
- Helm chart (`deploy/helm/classcache/`).

### v0.8-1 — Real archive-key pipeline (done, see §14)
- Primer PATCHes `status.archiveKey` from in-cluster via its ServiceAccount.
- Operator holds the workload patch until status is populated, then patches with the real key.
- Placeholder-key logic removed (eliminates the previous reconcile/publish race).

### v0.9 — Zero-build UX (done, see §15)
- Use the user's app and agent **without building any image**: the primer Pod's initContainers extract the jars from the user app image and a catalog agent image.
- One universal primer image (`classcache-primer:v0.9-universal`) shared by every ClassCache.
- Agent catalog images (`classcache-agent-scouter`, etc.).
- Profile catalog ConfigMap (cluster-wide) + optional inline `spec.profileYAML`.
- Dockerfiles the user has to write: **0**.

### v0.10 (remaining) — Production hardening
- Archive signing + Workload verification.
- Datadog / New Relic / Elastic APM ingestion validation.
- Multi-replica operator + leader election in real ops.
- Server-side apply to remove reconcile conflicts.
- Automatic cleanup of stale `build_lock` (exposed during v0.9 testing).

### v0.11 — Multi-cluster / DR
- Optional cross-cluster archive sync.
- Archive registry (S3/GCS) backing the Valkey directory.

### v1.0 — Native acceleration
- Explore integration with GraalVM native image.
- Per-class archive partitioning (mmap only the classes that haven't changed).

---

## 11. Verification matrix (v0.1 → v0.9)

| Hypothesis | Location | Result | Notes |
|------|---------|------|------|
| ByteBuddy transforms get baked into a dynamic CDS archive | `benchmark/cds-verify/` (Phase B) | ✅ | Transformed code runs at agent-OFF runtime |
| N JVMs on the same node share archive pages | `benchmark/mmap-share/` | ✅ | 4 JVMs, Shared_Clean 83% |
| Scales to bigger apps (Spring Boot) | `benchmark/springboot-scale/` | ✅ | 33 MB archive, 61 MB saved across 4 JVMs |
| Valkey directory + P2P distribution | `benchmark/cluster-primer/` | ✅ | build 2.6 s vs pull 50 ms |
| In-house APM (HTTP entry + SPAN JSON) | `apm/` | ✅ | 5/5 checks, SPAN works at agent-OFF runtime |
| K8s multi-node environment | `k8s/` | ✅ | DaemonSet + hostPath + initContainer + PodIP P2P |
| Third-party APM (Scouter) ingestion | `demos/07-scouter-ingestion/` | ✅ | Integrated via `-Xbootclasspath/a` |
| OTel agent ingestion | `demos/08-otel-ingestion/` | ✅ (hybrid) | agent-on runtime + bootclasspath + archive |
| Go primer + Valkey + AgentProfile | `modules/primer/`, `modules/agent-profiles/` | ✅ v0.6 | See §13 |
| Operator (CRD + Webhook + Helm) | `modules/operator/`, `deploy/helm/` | ✅ v0.7 | See §14 |
| Real archive-key pipeline | primer → CR status PATCH → operator | ✅ v0.8-1 | See §14.4 |
| **Zero-build UX (no Dockerfile)** | `modules/agent-catalog/`, universal primer image | **v0.9 new** | See §15 |

---

## 12. One-liner summary (v0.5 → v0.9)

> **Bake a deterministic Java APM (OTel/Scouter) into a CDS archive, distribute across nodes via a Valkey directory + P2P transfer, share pages on the same node via mmap. From v0.7 the operator automates the infrastructure; from v0.8-1 the archive key is propagated automatically; from v0.9 the user writes zero Dockerfiles — one `ClassCache` CR is all you need.**

---

## 13. v0.6 changes (structure, tooling, licensing)

### 13.1 Directory reorganization

```
demos/        # validation cases (01-phase-b-cds … 08-otel-ingestion)
modules/      # production components
  primer/         # Go implementation (~15 MB binary)
  agent-profiles/ # JSON Schema + YAML profile definitions
  apm-agent-v0.1/ # in-house APM agent
deploy/       # deployment artifacts (helm, manifests)
docs/         # DESIGN, REPORT
```

### 13.2 Redis → Valkey

| Item | v0.5 | v0.6 |
|------|------|------|
| Image | `redis:7-alpine` | `valkey/valkey:7.2-alpine` |
| License | SSPL (Redis Inc) | BSD-3-Clause (Linux Foundation) |
| Env vars | `REDIS_HOST/PORT` | `VALKEY_HOST/PORT` |
| Go client | n/a | `go-redis/v9` (wire-compatible) |

The `docker-compose.yml` service name stays `redis` for compatibility (only `depends_on` references inside that file would have been affected by renaming).

### 13.3 AgentProfile

A JSON Schema (`modules/agent-profiles/schema/v1.json`) declaratively describes how an agent goes in at build and runtime. Profiles are written as YAML; the primer loads one file (`/etc/classcache/profile.yaml`) at boot.

```yaml
apiVersion: classcache.dev/v1
kind: AgentProfile
metadata: { name: scouter, version: "2.21.3" }
spec:
  agent:    { jar: /work/agent.jar, config: /opt/scouter/conf/scouter.conf }
  build:    { javaagent: true,  bootclasspath: true, extraJvmOpts: [...] }
  runtime:  { javaagent: false, bootclasspath: true, extraJvmOpts: [...] }
```

References: `scouter`, `otel`, `apm-v01`.

### 13.4 Go primer (6 modules)

| File | Role |
|------|------|
| `main.go` | env → Config, profile / Valkey / peer wiring, signal handling |
| `orchestrator.go` | local-hit → pull → build-lock → build → register lifecycle |
| `directory.go` | Valkey: Register / ListPeers / TryAcquireBuildLock / PublishEvent |
| `archive.go` | sha256 → 16-char key, ArchivePath, LocalArchiveExists |
| `peer.go` | `/archive/{key}` HTTP serve + PullFromPeer |
| `builder.go` | Profile loader + BuildArgs + JVM subprocess |

Tests: `archive_test.go`, `directory_test.go`, `peer_test.go`, `builder_test.go`. `miniredis` substitutes for Valkey as an in-memory fake.

### 13.5 Distroless image

`modules/primer/Dockerfile` (multi-stage): `golang:1.22` → `classcache-apm-v0.1` → JVM runtime. The entry point `build.sh` uses the `BUILDER` env var to abstract docker/podman/nerdctl.

> **Size note**: the "20 MB" target from the v0.6 plan was unrealistic because the primer forks a JVM at runtime to bake the archive. distroless/java22 alone is ~80 MB. The Go binary itself is ~15 MB and could live in distroless/static, but that would require a JVM-sidecar pattern — a v0.9 candidate.
>
> **v0.8 note**: the `gcr.io/distroless/java22-debian12:nonroot` tag was pulled from the registry, so we fell back to `eclipse-temurin:22-jre` (~200 MB). If the user's app is JDK 21 compatible, `distroless/java21-debian12:nonroot` brings it back to ~80 MB.

---

## 14. v0.7 / v0.8-1 changes (Operator)

v0.7 replaces what the user used to hand-write as manifests with a **single declarative CR**. v0.8-1 closes the primer ↔ operator status pipeline so that the CR actually drives "my app booting from the archive" automatically.

### 14.1 ClassCache CRD

```yaml
apiVersion: classcache.dev/v1
kind: ClassCache
metadata: { name: my-app, namespace: default }
spec:
  workloadRef: { kind: Deployment, name: my-app }
  profile: scouter
  primerImage: classcache-primer:v0.8
  patchMode: Owned          # or Webhook
  valkey: { create: true }  # or { create: false, addr: ... }
status:
  archiveKey: "6ee45a917a084176"   # populated by the primer
  phase: Ready                     # Pending → PrimerReady → WorkloadPatched → Ready
  readyPeers: 2
```

### 14.2 Operator (`modules/operator/`)

| File | Role |
|------|------|
| `api/v1/classcache_types.go` | CRD spec/status, hand-written DeepCopy |
| `controllers/classcache_controller.go` | Reconcile entrypoint (defaults → valkey → primer RBAC → primer DS → workload patch → status) |
| `controllers/valkey.go` | Per-CC Valkey Deployment + Service (or use an external addr) |
| `controllers/primer.go` | Per-CC Primer DaemonSet, injects VALKEY_HOST/PORT + CLASSCACHE_NAME/NAMESPACE env |
| `controllers/primer_rbac.go` | Primer SA + Role(`classcaches/status:patch`) + RoleBinding |
| `controllers/workload_patch.go` | Injects initContainer/volume/JVM opts into the Deployment PodTemplate (deferred until status.archiveKey is set) |
| `webhook/pod_mutator.go` | Patches at Pod-CREATE time (`classcache.dev/inject` label + patchMode=Webhook) |
| `cmd/main.go` | controller-runtime manager + webhook server wiring |

Image: `gcr.io/distroless/static:nonroot` → **~11.5 MiB**. All 12 tests pass with fake client + admission decoder, no external dependencies.

### 14.3 Two patch modes

**Owned** (default): the operator mutates the user's `Deployment.spec.template` directly. Simple UX, but conflicts with GitOps tools like ArgoCD (the operator keeps rewriting the patch).

**Webhook**: the user only adds the label `classcache.dev/inject: <cc-name>` to the Deployment template. The admission webhook intercepts Pod CREATE and patches there. The Deployment itself is untouched → GitOps-friendly. Requires cert-manager (or equivalent).

### 14.4 Archive-key pipeline (the heart of v0.8-1)

Through v0.7, the operator used a placeholder key `sha256(namespace|name|profile|image)`, while the primer used the real key `sha256(app||agent||jvm||arch||profile)`. They didn't match → the initContainer waited forever → workload pods never came up.

The loop v0.8-1 closes:

```
1. operator  reconcile → create Valkey + Primer DS (workload patch deferred)
2. primer    build/pull → compute key
3. primer    PATCH /apis/classcache.dev/v1/.../classcaches/<name>/status
             body: {"status":{"archiveKey":"<real-key>"}}
4. operator  status watch → re-trigger reconcile → patch Workload Deployment
5. initContainer finds <real-key>.jsa → workload Pod Ready
```

**Zero-dependency publisher**: `modules/primer/status_publisher.go` does the merge-patch with only the SA token + ca.crt — no client-go. The primer image size doesn't change.

**Measurement**: on a kind 2-worker cluster, from CR apply to workload Ready takes **~15 seconds** (about 12 s for lock contention + pull + status publish, plus ~3 s for reconcile + initContainer).

### 14.5 Helm chart (`deploy/helm/classcache/`)

```bash
helm install classcache deploy/helm/classcache \
  --namespace classcache-system --create-namespace \
  --set image.repository=classcache-operator \
  --set image.tag=v0.8 \
  --set webhook.enabled=true \
  --set tls.mode=certManager
```

Key knobs in `values.yaml`: `webhook.enabled`, `tls.mode` (certManager/externalSecret/none), `installCRD`, `leaderElect`.

### 14.6 Known limitations

- **Reconcile conflict**: our upsert is a Get→Update pattern, so during status updates we occasionally see `Operation cannot be fulfilled: object has been modified`. controller-runtime automatically re-queues, so it's harmless, but the remaining v0.8 work will clean it up with server-side apply.
- **No JDK 22 distroless tag**: see the §13.5 note.
- **Leader election off**: only enable `leaderElect=true` when `replicaCount=2` (exposed as a Helm value).

---

## 15. v0.9 changes (Zero-build UX)

Through v0.7 + v0.8-1 the user still had to bundle "my app + agent + profile" into a **custom primer image** — Dockerfile authoring + build context wrangling + push. v0.9 removes that step entirely.

### 15.1 Core idea

Don't *build* an image — *mount* an existing one and *extract the jar*.

```
Primer Pod (per node, DaemonSet)
├── initContainer cc-extract-app    image = user's app (my-app:1.0)
│   "cp /app.jar /cc-staging/app.jar; jarmode extract"
├── initContainer cc-extract-agent  image = catalog agent (classcache-agent-scouter:v0.9)
│   "cp /agent.jar /cc-staging/agent.jar"
└── container     primer            image = classcache-primer:v0.9-universal
    mounts:
      /work        ← app emptyDir
      /opt/agent   ← agent emptyDir
      /etc/classcache/profile.yaml ← profile ConfigMap
      /var/lib/classcache (hostPath)
```

The extractors drop jars into emptyDir; the primer uses them as the source for the archive build. The same universal image is **reused across every ClassCache**.

### 15.2 User UX

```yaml
apiVersion: classcache.dev/v1
kind: ClassCache
metadata: { name: my-app }
spec:
  workloadRef: { kind: Deployment, name: my-app }
  app:     { image: my-app:1.0, jarPath: /app.jar }
  agent:   { image: classcache-agent-scouter:v0.9 }
  profile: scouter            # cluster-wide catalog lookup
```

Dockerfiles the user writes: **0**. Build contexts: **0**. Just point `app.image` at whatever your normal CI/CD already produces.

### 15.3 New components

| Location | Purpose |
|------|------|
| `modules/primer/Dockerfile.universal` | Universal primer image — Go binary + JRE only, no app/agent/profile baked in |
| `modules/primer/build-universal.sh` | Universal image build (with `BUILDER` abstraction) |
| `modules/agent-catalog/scouter/` | Alpine image with only the Scouter agent jar + conf (6 MiB) |
| `modules/agent-catalog/build.sh` | Batch-builds every directory in the catalog |
| `deploy/manifests/05-profile-catalog.yaml` | `classcache-system/classcache-profiles` ConfigMap (scouter/otel/apm-v01) |
| `modules/operator/controllers/profile_cm.go` | Resolves profile name → catalog lookup → per-CC ConfigMap |

The operator's `reconcilePrimer` automatically wires the two initContainers whenever both `spec.app.image` and `spec.agent.image` are set. Otherwise it falls back to the v0.7 baked-in mode (everything inside `PrimerImage`) — backward compatible during migration.

### 15.4 Image sizes

| Image | Size |
|--------|--------|
| `classcache-operator:v0.9.1` | 11.5 MiB |
| `classcache-primer:v0.9-universal` | 93.8 MiB (includes JRE 22) |
| `classcache-agent-scouter:v0.9` | 6.1 MiB (Alpine + 2.4 MiB jar) |

Images the user has to build: **0**.

### 15.5 Validation

On a kind 2-worker cluster:

```yaml
spec:
  app: { image: classcache-springboot-scale:latest, jarPath: /work/app.jar }
  agent: { image: classcache-agent-scouter:v0.9 }
  profile: scouter
```

- The initContainers drop jars at the correct paths (`/work/app.jar`, `/opt/agent/agent.jar`, `/opt/agent/agent.conf`).
- `profile.yaml` is mounted via ConfigMap projection.
- The primer builds the archive → status PATCH `archiveKey=99cdff82d2f81455`.
- The operator patches the workload using that key → Pod 2/2 Ready.

### 15.6 Issues v0.9 exposed

- **Stale `archive:<key>:build_lock`**: if the primer dies mid-build, the lock persists and new primers wait forever. Hit during v0.9 testing — needed a manual `valkey-cli DEL`. v0.10 will have the primer periodically renew the TTL + take over when the lock holder Pod disappears.
- **Workload patch fired once with an empty key**: the initial reconcile deferred the patch (no key yet) → the primer published → reconcile re-ran, but by then a ReplicaSet had already been created with the empty patch template, so we needed one `rollout restart`. v0.10 will adjust caching so the status watch is guaranteed to land before any ReplicaSet creation.
