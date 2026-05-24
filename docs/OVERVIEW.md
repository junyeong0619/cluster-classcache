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

## 4. What was built (v0.5 → v0.9)

| Version | Headline | Key code path |
|---|---|---|
| v0.5 | PoC: hypothesis verification (Phase B → mmap → Spring Boot scale → primer → APM v0.1 → K8s → Scouter → OTel) | `demos/01..08/` |
| v0.6 | Modularization. Python primer → Go (6 modules). Redis → Valkey (BSD). AgentProfile JSON Schema. | `modules/primer/`, `modules/agent-profiles/` |
| v0.7 | Kubernetes operator (controller-runtime). `ClassCache` CRD. Owned / Webhook patch modes. Helm chart. | `modules/operator/`, `deploy/helm/classcache/` |
| v0.8-1 | Closed the loop: primer PATCHes `status.archiveKey` (in-cluster SA token, no client-go); operator patches the workload only after the real key arrives. | `modules/primer/status_publisher.go` + `controllers/classcache_controller.go` |
| v0.9 | Zero-build UX: no user Dockerfile. Primer Pod's initContainers extract jars from the user's app image + a catalog agent image. Universal primer image. | `controllers/primer.go` (initContainer wiring) + `modules/agent-catalog/` |

End-user surface today:
```yaml
apiVersion: classcache.dev/v1
kind: ClassCache
metadata: { name: my-app }
spec:
  workloadRef: { kind: Deployment, name: my-app }
  app:   { image: my-app:1.0, jarPath: /app.jar }
  agent: { image: ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest, jarPath: /javaagent.jar }
  profile: otel
```
That's it. Plus a normal Deployment.

## 5. What's actually measured vs. what's still a claim

### Measured (kind on macOS arm64, real numbers in the logs)

| Claim | Source |
|---|---|
| Spring Boot boots from archive in **579 ms** | `demos/04-cluster-primer/run-demo.sh` Bonus step |
| First node builds in **3.0 s** (Go primer), 2.6 s (Python) | `demos/06-k8s-end-to-end/run-k8s-demo.sh` step 4 |
| Subsequent nodes pull in **80 ms** | Same |
| `Pss/Rss = 63.5%` for 2 same-node JVMs (hybrid mode) | Same, step 9 |
| Same sha256 key `99cdff82d2f81455` across both demos with same inputs | Determinism evidence |
| Operator drives a CR to `Ready` in **11–15 s** | `scripts/quickstart.sh` timeline |

### Not yet verified

- **Real multi-host cluster** (EKS/GKE/bare-metal). All measurements are on kind, which is K8s-inside-Docker — single host.
- **x86_64**. Only arm64 (Apple Silicon) tested.
- **Production load.** No measurement under sustained traffic, only warmup-then-measure.
- **Archive signing / supply chain.** `AllowArchivingWithJavaAgent` is a diagnostic flag, not production-safe.
- **OTel hybrid limitations.** §8.7 + §8.10 of `REPORT.md` — works partially (~1328 classes from archive vs ~4861 for Scouter), but OTel SDK is still bound to isolated AgentClassLoader. Real fix is a v0.10+ task.

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
├── agent-catalog/scouter/        Wraps Scouter tarball (only vendor w/o official image)
└── agent-profiles/               JSON Schema for AgentProfile + reference YAMLs

deploy/
├── manifests/                    Raw K8s YAMLs (CRD, RBAC, operator, webhook, profile catalog)
└── helm/classcache/              Helm chart (same content, parameterized)

demos/01..08/                     The hypothesis-by-hypothesis validation trail
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
| "What stops a bad archive from being trusted?" | Honest answer: nothing yet. v0.10 archive signing. |

## 8. Honest limits

1. **Built fast with heavy AI assistance.** Architecture decisions and measurements were both done in collaboration with an AI assistant. The numbers in this doc are real (logged outputs from the demos), but the depth of "did the author personally debug every code path" is not the same as e.g. uftrace #1925 or the JFS patches.
2. **Single-host validation.** Real cross-host networking effects (peer pull over actual NICs, ARP, MTU, mTLS) are unmeasured.
3. **`AllowArchivingWithJavaAgent` is diagnostic.** Not blessed for production by the JDK team. The whole approach hinges on a flag that's officially "for testing purposes only".
4. **OTel SDK isolated classloader.** Hybrid mode works but with smaller archive coverage. Full OTel parity is a v0.10+ task.
5. **No external user yet.** Quickstart works end-to-end on kind, but no one outside this repo has tried it.

## 9. If you want to make this truly yours

- Read three files line by line: `archive.go`, `workload_patch.go`, `status_publisher.go`. Total ~400 lines.
- Run `./scripts/quickstart.sh` yourself, watch the numbers come out, look at the smaps inside the kind worker (`docker exec cc-worker bash -c 'cat /proc/<pid>/smaps | grep -A 10 jsa'`).
- The three sentences you should be able to say from memory: how CDS bake-in works, why sha256 makes it deterministic, why `ArchiveRelocationMode=0` enables Shared_Clean.

Once those click, this stops being an AI-built repo and starts being a JVM-internals project you happen to have prototyped with AI help. The difference shows in interviews.
