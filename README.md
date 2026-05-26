# cluster-classcache

> **Fast JVM startup anywhere in the cluster + zero APM agent runtime overhead + cross-pod memory sharing on the same node** — driven by a single `ClassCache` YAML.

A Kubernetes operator that distributes JVM CDS (Class Data Sharing) archives across nodes over P2P, with APM-agent–transformed bytecode baked in.

---

## What it gives you

Measured on kind (2 workers), Spring Boot 3 + Scouter v2.21:

| | Baseline | cluster-classcache |
|---|---|---|
| **Spring Boot startup** | 5–10 s (cold JIT) | **0.5 s** (archive mmap) |
| **APM agent runtime overhead** | premain on every boot + retransform | **0** (transformed classes baked into archive) |
| **First-node archive build** | — | 3 s (Go primer) |
| **Subsequent nodes** | 5–10 s rebuild each | **80 ms** (P2P pull) |
| **Per-JVM memory on the same node** | N × RSS | **Pss/Rss ≈ 63%** (smaps Shared_Clean) |
| **Dockerfiles you have to write** | — | **0** (v0.9) |

For a 1,000-pod Spring Boot fleet: one build, 999 pulls.

---

## Three key ideas

1. **JVM CDS archive** — `-XX:ArchiveClassesAtExit` dumps loaded classes to a file, `-XX:SharedArchiveFile` mmaps it next boot → 10× faster startup.
2. **APM bytecode transforms get baked into the archive** — run the agent at build time with `ArchiveClassesAtExit` and the transformed bytecode ends up in the archive itself. **At runtime the agent is off**, yet the transformed code is still loaded via mmap and instrumentation just works (zero premain cost).
3. **Archives are P2P-distributable** — the same `(image, agent, JVM, arch)` tuple yields the same sha256 archive. The first node builds; the rest HTTP-pull. Valkey acts as the directory.

---

## Architecture

```
                       ┌────────────────────────────────────────────┐
                       │              KUBERNETES CLUSTER             │
                       │                                            │
   ┌────────────┐      │   ┌────────────┐      ┌─────────────┐      │
   │    User    │      │   │  Operator  │      │   Valkey    │      │
   │            │──────┼──►│ (Reconcile)│──────│ (Directory) │      │
   │ ClassCache │      │   └─────┬──────┘      └──────┬──────┘      │
   │   one CR   │      │         │ owns                │ key→peers  │
   └────────────┘      │         ▼                     │            │
                       │   ┌────────────────────────┐  │            │
                       │   │  Per-node Primer (DS)  │◄─┘            │
                       │   │  initC: extract app    │               │
                       │   │  initC: extract agent  │               │
                       │   │  main:  build/pull     │               │
                       │   │         + status PATCH │               │
                       │   └────────────┬───────────┘               │
                       │                │ writes                    │
                       │                ▼                           │
                       │   [hostPath /var/lib/classcache/foo.jsa]   │
                       │                │ mmap                      │
                       │                ▼                           │
                       │   ┌────────────────────────┐               │
                       │   │   Workload Pods        │               │
                       │   │  (your Spring Boot)    │               │
                       │   │  agent OFF + archive   │               │
                       │   └────────────────────────┘               │
                       └────────────────────────────────────────────┘
```

See [`docs/DESIGN.md`](./docs/DESIGN.md) for a deeper walkthrough.

### Components at a glance

| Component | Role | Module |
|---|---|---|
| **Operator** | Watches the `ClassCache` CR, materializes Valkey / Primer DaemonSet / RBAC / Workload patch | `modules/operator/` |
| **Primer** | One per node. Builds the archive or P2P-pulls it, then PATCHes status | `modules/primer/` |
| **Valkey** | "Which node holds which archive key" directory (Redis-compatible) | `valkey/valkey:7.2-alpine` |
| **Agent catalog** | Pre-packaged agent jar images (Scouter, OTel, …) | `modules/agent-catalog/` |
| **Profile catalog** | Declarative "how this agent goes in at build/runtime" YAML | `modules/agent-profiles/` + ConfigMap |

---

## Quick Start

> A single script takes you all the way through. **Prereqs**: `docker`, `kubectl`, `kind`.

### 5-minute quickstart (drive the demo app once)

```bash
git clone https://github.com/junyeong0619/cluster-classcache.git
cd cluster-classcache
./scripts/quickstart.sh
```

The script does everything for you:
1. Creates a `kind` cluster called `cc-quickstart` (control-plane + 2 workers)
2. Installs cert-manager (for the webhook's TLS)
3. Builds the operator + universal primer images, and runs
   `modules/agent-catalog/scouter/setup.sh` which downloads the Scouter
   tarball from GitHub and wraps it in a small image. Demo app gets built too.
4. Loads everything into kind
5. Installs CRD + RBAC + profile catalog + operator + webhook
6. Applies `examples/quickstart.yaml`
7. Waits for the ClassCache to reach `Ready` and prints the result

> The Scouter step is the only "first-time setup" you have to do —
> Scouter has no official Docker image. OpenTelemetry, Datadog, New Relic,
> and Elastic all ship official agent images, so for those you just point
> `spec.agent.image` at the vendor's image (see
> [`modules/agent-catalog/README.md`](./modules/agent-catalog/README.md)).

### What you'll see

```
═══════════════════════════════════════════════════════
  Result
═══════════════════════════════════════════════════════
NAME         WORKLOAD     PROFILE   PHASE   KEY
quickstart   quickstart   scouter   Ready   99cdff82d2f81455

NAME                                    READY   STATUS    AGE
cc-quickstart-primer-xxxxx              1/1     Running   15s
cc-quickstart-primer-yyyyy              1/1     Running   15s
cc-quickstart-valkey-zzzzz              1/1     Running   15s
quickstart-aaaaa                        1/1     Running   3s
quickstart-bbbbb                        1/1     Running   3s
quickstart-ccccc                        1/1     Running   3s
```

End-to-end ~15 s. Each Workload Pod boots in 0.5 s.

### Tear-down

```bash
kind delete cluster --name cc-quickstart
```

---

## Applying it to your own app

Copy [`examples/my-app-template.yaml`](./examples/my-app-template.yaml) and fill in the four `<REPLACE_ME_…>` slots:

```bash
cp examples/my-app-template.yaml my-app.yaml
$EDITOR my-app.yaml      # fill in the four <REPLACE_ME_*> placeholders
kubectl apply -f my-app.yaml
```

The four slots (the template's header comment has the details):

| Slot | Meaning | Example |
|---|---|---|
| `<REPLACE_ME_NAMESPACE>` | Namespace your app lives in | `prod`, `default` |
| `<REPLACE_ME_NAME>` | ClassCache + Deployment name | `my-app` |
| `<REPLACE_ME_APP_IMAGE>` | **Your docker image** (whatever your CI/CD ships) | `ghcr.io/acme/my-app:1.4.0` |
| `<REPLACE_ME_APP_JAR_PATH>` | Path of the Spring Boot fat jar inside that image | `/app.jar`, `/work/app.jar` |

> **If you don't know where the jar lives in your image:**
> `docker run --rm --entrypoint sh <your-image> -c 'find / -name "*.jar" 2>/dev/null | head'`

Requirements:
- Your app image must contain **`sh`**, `cp`, and `java` (the initContainer copies the jar and runs `jarmode=tools extract`). Standard alpine/debian-based JDK images work; fully distroless images do not — see the workaround below.
- The fat jar must be **Spring Boot `jarmode=tools`** compatible (default since Spring Boot 3.2).

### If your app image is distroless

The `cc-extract-app` initContainer needs a shell to copy the jar and a JDK
to run `jarmode=tools extract`. Distroless images have neither. Three ways
out, in order of preference:

1. **Two-stage Dockerfile (recommended)** — keep your runtime image
   distroless, but base the *initContainer* on something that can `cp`.
   The cleanest pattern is to publish a small "extractor companion" image
   alongside your normal one:
   ```dockerfile
   # Dockerfile.extractor — runs only as initContainer, never serves traffic
   FROM eclipse-temurin:22-jdk-alpine
   COPY my-app.jar /app.jar
   ```
   Point `spec.app.image` at `my-app-extractor:1.0`; point your normal
   Deployment's container image at the distroless `my-app:1.0`. The
   initContainer extracts the jar from the companion image; the workload
   container boots from the archive using the distroless runtime.

2. **Use `spec.app.image` from a non-distroless build target** — many
   companies already produce a JDK image for CI/test purposes. If that
   image contains the same jar, point `spec.app.image` at it. The
   workload Deployment still uses your distroless image; only the
   extractor reads from the JDK image.

3. **Drop distroless for the primer step only** — if your CI doesn't have
   a JDK image, build one inside this repo. See
   [`CONTRIBUTING.md`](./CONTRIBUTING.md) for how to add a one-off extractor
   image to `modules/agent-catalog/`-style structure.

Long term: a future operator field (`spec.app.extractorImage`) will make
option 1 a first-class spec field instead of just a convention. Tracked as
a v0.10 task.

### Picking an agent image

| Vendor | Use the official image (no setup needed) |
|---|---|
| OpenTelemetry | `ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest` (`jarPath: /javaagent.jar`) |
| Datadog | `gcr.io/datadoghq/dd-lib-java-init:latest` (`jarPath: /datadog-java-agent.jar`) |
| New Relic | `newrelic/newrelic-java-init:latest` (`jarPath: /newrelic-agent.jar`) |
| Elastic APM | `docker.elastic.co/observability/apm-agent-java:latest` (`jarPath: /usr/agent/elastic-apm-agent.jar`) |
| **Scouter** | No official image. Run `modules/agent-catalog/scouter/setup.sh` once — it downloads the upstream tarball and builds `classcache-agent-scouter:v0.9`. |
| Internal / forked agent | Build your own tiny image (`FROM alpine:3.20` + `COPY my-agent.jar /agent.jar`) and push it to your registry. See [`modules/agent-catalog/README.md`](./modules/agent-catalog/README.md). |

### Targeting your own cluster (EKS, GKE, …, not kind)

Follow the same steps but push to your registry instead of `kind load`:

```bash
BUILDER=docker IMAGE=myreg.io/classcache-operator:v0.9.1     modules/operator/build.sh
docker push myreg.io/classcache-operator:v0.9.1
# same for primer + agent

helm install classcache deploy/helm/classcache \
  --namespace classcache-system --create-namespace \
  --set image.repository=myreg.io/classcache-operator \
  --set image.tag=v0.9.1
```

Then point `spec.primerImage` / `spec.agent.image` in your ClassCache CR at your registry paths.

---

## Two patch modes

| Mode | Deployment template | When to use |
|---|---|---|
| **Owned** (default) | The operator patches it directly (initContainer / volume / env) | General use |
| **Webhook** | Template stays clean; the admission webhook patches at Pod-creation time | ArgoCD / GitOps — avoids sync drift caused by the operator constantly rewriting the template |

Webhook mode requires the pod label `classcache.dev/inject: <cc-name>` and a working cert-manager.

---

## Demos

If you'd rather poke at the building blocks directly, eight demos isolate one hypothesis each:

```bash
demos/01-phase-b-cds/         # Verify CDS archives work
demos/02-mmap-share/          # Measure mmap sharing across N JVMs on a node
demos/03-springboot-scale/    # Scale test (Spring Boot 33 MB archive)
demos/04-cluster-primer/      # docker-compose 3-node P2P distribution
demos/05-apm-v01/             # Reference in-house APM agent
demos/06-k8s-end-to-end/      # kind multi-node integration (pre-v0.7 path)
demos/07-scouter-ingestion/   # Scouter ingestion compatibility
demos/08-otel-ingestion/      # OTel hybrid mode
demos/09-k3d-multinode/       # k3d 4-node (real bridge between node containers)
```

Every directory has a `run-*.sh` you can launch in one shot.

---

## Documentation

- [`docs/DESIGN.md`](./docs/DESIGN.md) — Design (why, how, what's inside)
- [`docs/REPORT.md`](./docs/REPORT.md) — Step-by-step verification report (Phase B → v0.9)

---

## License

Apache 2.0 — see [`LICENSE`](./LICENSE). Third-party attribution in [`NOTICE`](./NOTICE).

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for development setup, the
new-agent guide, and a list of known-good first issues.
