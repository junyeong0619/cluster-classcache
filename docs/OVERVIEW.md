# cluster-classcache — One-Page Overview

한국어 버전: [`OVERVIEW.ko.md`](./OVERVIEW.ko.md)

## 1. What it is (one sentence)

A Kubernetes operator that bakes APM-agent–transformed bytecode into a JVM CDS archive, distributes that archive across nodes via a Valkey directory + P2P pull, and lets workload pods boot from it without running the agent at runtime.

## 2. Problem it tries to solve

Every Java pod doing:
- ~5–10 s startup (cold JIT + class loading)
- APM agent `premain` re-running every boot (Scouter / OTel / Datadog)
- Same JAR loaded into N separate JVMs on the same node = N × RSS

In a 1000-pod Spring Boot fleet this is wasteful. The CDS archive can be built **once**, distributed via P2P, and **mmap-shared** at the OS level. APM transforms — usually a runtime cost — can be baked into the archive at build time and reused for free.

## 3. Three mechanisms that make it work

| # | Mechanism | Where to point in code |
|---|---|---|
| 1 | **CDS archive bake-in** — `-XX:ArchiveClassesAtExit` at build time with the agent on; `-XX:SharedArchiveFile` at runtime without it. Transformed bytecode is in the archive, so the agent doesn't have to re-run. | `demos/01-phase-b-cds/cds-verify.sh` (the minimal proof) |
| 2 | **Deterministic sha256 key** — same `(app.jar, agent.jar, JVM, arch, profile)` ⇒ same archive. Used as the cluster-wide identifier. | `modules/primer/archive.go` (~70 lines, `ComputeKey`) |
| 3 | **mmap page sharing** — `-XX:ArchiveRelocationMode=0` makes every JVM mmap at the same address → kernel shares pages (`Shared_Clean` in smaps). | Measured in `demos/02-mmap-share/scripts/measure-in-container.sh` |

If you can explain these three out loud, you can answer 80% of the interview questions about this repo.

## 4. What was built (v0.5 → v0.12-A)

| Version | Headline | Key code path |
|---|---|---|
| v0.5 | PoC: hypothesis verification (Phase B → mmap → Spring Boot scale → primer → APM v0.1 → K8s → Scouter → OTel) | `demos/01..08/` |
| v0.6 | Modularization. Python primer → Go (6 modules). Redis → Valkey (BSD). AgentProfile JSON Schema. | `modules/primer/`, `modules/agent-profiles/` |
| v0.7 | Kubernetes operator (controller-runtime). `ClassCache` CRD. Owned / Webhook patch modes. Helm chart. | `modules/operator/`, `deploy/helm/classcache/` |
| v0.8-1 | Closed the loop: primer PATCHes `status.archiveKey` (in-cluster SA token, no client-go); operator patches the workload only after the real key arrives. | `modules/primer/status_publisher.go` + `controllers/classcache_controller.go` |
| v0.9 | Zero-build UX: no user Dockerfile. Primer Pod's initContainers extract jars from the user's app image + a catalog agent image. Universal primer image. | `controllers/primer.go` (initContainer wiring) + `modules/agent-catalog/` |
| v0.10 | Distroless support (`spec.app.extractorImage`), Pinpoint catalog (multi-file agent tree), k3d 4-node verification, classcache C CLI, Apache 2.0 + CONTRIBUTING. | `modules/cli/`, `modules/agent-catalog/pinpoint/`, `demos/09-k3d-multinode/`, `LICENSE` |
| v0.11 | Stale build_lock + peer cleanup (TTL+heartbeat). SHA256 integrity verification on pull. Zone-aware peer selection (protocol). kubectl-exec smaps fallback for k3d. | `primer/directory.go`, `primer/peer.go`, `cli/src/smaps.c` |
| v0.12-A | Valkey directory survives Pod restart — StatefulSet + PVC 256Mi + AOF `everysec`. | `operator/controllers/valkey.go` |

End-user surface today:
```yaml
apiVersion: classcache.dev/v1
kind: ClassCache
metadata: { name: my-app }
spec:
  workloadRef: { kind: Deployment, name: my-app }
  app:
    image:          my-app:1.0                       # what your Deployment uses
    extractorImage: my-app-extractor:1.0             # optional, for distroless workloads
    jarPath:        /app.jar
  agent:
    image:   ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest
    jarPath: /javaagent.jar                          # OTel official image
  profile: otel                                      # or scouter / pinpoint / apm-v01
```
That's it. Plus a normal Deployment.

## 5. What's actually measured vs. what's still a claim

### Measured (kind / k3d on macOS arm64, real numbers in the logs)

| Claim | Source |
|---|---|
| Spring Boot boots from archive in **579 ms** | `demos/04-cluster-primer/run-demo.sh` Bonus step |
| First node builds in **3.0 s** (Go primer), 2.6 s (Python) | `demos/06-k8s-end-to-end/run-k8s-demo.sh` step 4 |
| Subsequent nodes pull in **80 ms** | Same |
| `Pss/Rss = 63.5%` for 2 same-node JVMs (hybrid mode) | Same, step 9 |
| Same sha256 key `99cdff82d2f81455` across kind + k3d with same inputs | `demos/09-k3d-multinode/README.md` |
| Operator drives a CR to `Ready` in **11–15 s** (kind), **~34 s** (k3d 4-node) | `scripts/quickstart.sh` + `demos/09/run.sh` |
| **k3d 4-node** (1 server + 3 agents) — primer DaemonSet 4/4, peer set 4 entries, P2P over real Docker bridge | `demos/09-k3d-multinode/README.md` |

### Not yet verified

- **Real multi-host cluster** (EKS/GKE/bare-metal). k3d is the strongest laptop signal but still single-host.
- **x86_64**. Only arm64 (Apple Silicon) tested.
- **Production load.** No measurement under sustained traffic, only warmup-then-measure.
- **Archive signing / supply chain.** `AllowArchivingWithJavaAgent` is a diagnostic flag, not production-safe.
- **OTel hybrid limitations.** §8.7 + §8.10 of `REPORT.md` — works partially (~1328 classes from archive vs ~4861 for Scouter), but OTel SDK is still bound to isolated AgentClassLoader. Real fix is a v0.11+ task.
- **smaps over k3d.** k3d nodes don't share host PID namespace, so `classcache stats` shows 0 KB for memory sharing there. Workaround documented; portable fix (kubectl-exec fallback) tracked as v0.11.

## 6. Code map (where things live)

```
modules/
├── primer/                       (~900 lines Go, 13 tests, miniredis-backed)
│   ├── archive.go                sha256(jars||jvm||arch||profile) → 16-char key
│   ├── directory.go              Valkey client: peers, build lock, events
│   ├── peer.go                   /archive/{key} HTTP serve + PullFromPeer
│   ├── builder.go                Profile loader + JVM subprocess for build
│   ├── orchestrator.go           local-hit → pull → build-lock → build → register
│   ├── main.go                   env → Config wiring
│   └── status_publisher.go       PATCH CR status with no client-go (~80 lines)
├── operator/                     (~1000 lines Go, 12 tests, fake client)
│   ├── api/v1/                   CRD types + hand-written DeepCopy
│   ├── controllers/
│   │   ├── classcache_controller.go     reconcile entrypoint
│   │   ├── valkey.go                    Valkey Deployment + Service
│   │   ├── primer.go                    Primer DaemonSet with two initContainers
│   │   ├── primer_rbac.go               per-CC SA + Role(status:patch) + RB
│   │   ├── workload_patch.go            inject initContainer + JVM opts + volume
│   │   └── profile_cm.go                profile-name → catalog ConfigMap → per-CC CM
│   └── webhook/pod_mutator.go    Pod CREATE admission patch (Webhook mode)
├── agent-catalog/
│   ├── scouter/                  Wraps Scouter tarball (single-jar agent)
│   └── pinpoint/                 Wraps Pinpoint tarball (multi-file agent tree, NAVER-origin)
├── agent-profiles/               JSON Schema for AgentProfile + reference YAMLs
└── cli/                          C — `classcache` runtime CLI (~1.3k LOC, hiredis+cJSON+libcurl)

deploy/
├── manifests/                    Raw K8s YAMLs (CRD, RBAC, operator, webhook, profile catalog)
└── helm/classcache/              Helm chart (same content, parameterized)

demos/01..09/                     Hypothesis-by-hypothesis trail (demos/09 is k3d multi-node)
```

## 7. Likely interview questions + where the answer lives

| Question | File to point at |
|---|---|
| "Show me how you generate the archive key." | `modules/primer/archive.go` — `ComputeKey` is 5 lines |
| "How does the workload pod know which `.jsa` to wait for?" | `modules/operator/controllers/workload_patch.go` — `waitScript` + `addEnv` |
| "Why does `ArchiveRelocationMode=0` matter?" | `REPORT.md` §3.2 (and the measured 83% vs 12.6% delta) |
| "How does the primer publish to the CR without client-go?" | `modules/primer/status_publisher.go` — reads SA token + ca.crt, does `PATCH`. ~80 lines |
| "What happens if two primers start at the same time?" | `modules/primer/orchestrator.go` — `acquireRemoteOrBuild` + `waitForPeer` (Valkey SETNX lock + polling) |
| "Why Scouter has a catalog image but OTel doesn't?" | `modules/agent-catalog/README.md` |
| "How do I use this when my workload image is distroless?" | `spec.app.extractorImage` (v0.10) — `controllers/primer.go` falls back when empty, separate test cases in `classcache_controller_test.go` |
| "Pinpoint is multi-file; how does the extractor handle that?" | `controllers/primer.go:extractAgentScript` — `if [ -d ]` branch + `cp -a` |
| "Does this work across real nodes, not just kind?" | `demos/09-k3d-multinode/README.md` — k3d 4-node, real Docker-bridge P2P, same sha256 key as kind |
| "What stops a bad archive from being trusted?" | Honest answer: nothing yet. Archive signing remains a v0.11+ task. |
| "Why ~1.3k lines of C in this repo?" | `modules/cli/README.md` — keeps tooling consistent with the systems side (uftrace, JFS, valkey); hiredis+cJSON+libcurl is the canonical trio for this glue. |

## 8. Honest limits

### Process-level

1. **Built fast with heavy AI assistance.** Architecture decisions and measurements were both done in collaboration with an AI assistant. The numbers in this doc are real (logged outputs from the demos), but the depth of "did the author personally debug every code path" is not the same as e.g. uftrace #1925 or the JFS patches.
2. **No external user yet.** Quickstart works end-to-end on kind and k3d, but no one outside this repo has tried it.

### Technical — design-inherent (won't disappear with more engineering)

3. **CDS archive is JVM-version-locked.** An archive built on JDK 21 cannot be used on JDK 17 (or even a different JDK 21 patch level in some cases). If a cluster mixes JDK versions, the sha256 input splits, archives multiply, P2P sharing degrades. Mitigation = standardize JDK across the cluster; that's an org problem, not a code problem.
4. **AppCDS captures static class loading only.** Dynamic proxies, lambdas, reflection-generated classes are partial-coverage at best. Spring's `@Configuration` CGLIB proxies usually land in the archive because they're created during the warmup HTTP hit, but it's case-by-case — any class created after warmup but before SIGTERM is included; anything created post-archive isn't.
5. **classlist determinism is fragile.** Two "identical" builds can produce different archives if the warmup happens to load classes in a different order — for example, a library that branches on `/proc/cpuinfo` flags. Docker pins most of this, but not all. A reproducibility audit (`diffoscope` on two builds) is on the v0.11 wishlist.
6. **hostPath dependency (workload archives).** Archives on workers live on hostPath, not a PVC. If a node dies, its archive dies with it. New node = new pull. That's fine in steady state (pulls are 80 ms), but there's no archive durability guarantee — treat it as cache, not state. (Note: as of v0.12-A the *directory* itself is on a PVC, so directory metadata survives Pod restarts even though workload archives don't.)

### Technical — current implementation gaps (could be fixed with more work)

7. **Multi-host still single-physical-host.** v0.10 added k3d 4-node verification — real separate node containers joined by a Docker bridge, not kind's shared-container hack — but it's still one machine. EKS/GKE / bare-metal measurements remain.
8. **First-build hot spot.** Cluster-wide image rollout = 1 node builds, N−1 nodes pull from it simultaneously. At N=1000 that single primer is a 33 MB × 999 fan-out point. Mitigation = tiered distribution (zone-local peer preference, or CDN-style intermediate caches). Designed for, not implemented.
9. **`AllowArchivingWithJavaAgent` is diagnostic.** Not blessed for production by the JDK team. The whole approach hinges on a flag that's officially "for testing purposes only".
10. **OTel SDK isolated classloader.** Hybrid mode works but with smaller archive coverage (~1328 classes vs ~4861 for Scouter). Full OTel parity requires a forked agent or an SDK-only bootstrap mode. v0.11+.
11. **`classcache stats` smaps fallback.** Works on kind (shared host PID namespace) but reads 0 KB on k3d. A `kubectl exec` fallback is the obvious fix; v0.11.

(License, CONTRIBUTING, Pinpoint, and the distroless gap from earlier feedback are resolved as of v0.10.)

## 9. If you want to make this truly yours

- Read three files line by line: `archive.go`, `workload_patch.go`, `status_publisher.go`. Total ~400 lines.
- Read two CLI files: `modules/cli/src/smaps.c`, `modules/cli/src/stats.c`. ~365 lines. They're the parts that the systems side of the resume connects to.
- Run `./scripts/quickstart.sh` yourself, watch the numbers come out, look at the smaps inside the kind worker (`docker exec cc-worker bash -c 'cat /proc/<pid>/smaps | grep -A 10 jsa'`), then run `classcache stats` and confirm the numbers match.
- Run `./demos/09-k3d-multinode/run.sh` too — proves the same archive key shows up on a different K8s runtime (kind vs k3d).
- The three sentences you should be able to say from memory: how CDS bake-in works, why sha256 makes it deterministic, why `ArchiveRelocationMode=0` enables Shared_Clean.

Once those click, this stops being an AI-built repo and starts being a JVM-internals project you happen to have prototyped with AI help. The difference shows in interviews.

---

## TL;DR

| Question | Answer |
|---|---|
| What is it? | A K8s operator + JVM CDS archive distribution system. APM agent overhead 0, cross-pod memory sharing, fast boot. |
| Core mechanisms? | (1) CDS bake-in, (2) sha256 determinism, (3) mmap page sharing |
| Was it really measured? | Yes — 6+ numbers are real. Measured by an AI assistant with the author present, not by the author independently → spend a weekend running it yourself before claiming it. |
| How far did it get? | v0.10: ClassCache CR + a normal Deployment is enough; kind reaches `Ready` in 11–15 s, k3d 4-node in ~34 s. Distroless workloads supported. Apache 2.0. |
| What's still missing? | Real multi-host (EKS/GKE), x86_64, prod load, archive signing, OTel SDK split-bootstrap, Valkey HA (multi-replica + failover) — all v0.12-B+. |
