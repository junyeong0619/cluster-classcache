# Agent catalog

Where the agent jars come from, by vendor.

## TL;DR

| Agent | Official image exists? | What to do |
|---|---|---|
| **OpenTelemetry Java** | ✅ | Point `spec.agent.image` at it directly. Skip the catalog. |
| **Datadog Java** | ✅ | Same. |
| **New Relic Java** | ✅ | Same. |
| **Elastic APM Java** | ✅ | Same. |
| **Scouter** | ❌ (tarball only) | `modules/agent-catalog/scouter/setup.sh` builds one. |
| **Pinpoint** | ❌ (tarball only, NAVER-origin) | `modules/agent-catalog/pinpoint/setup.sh` builds one. |

Catalog images live in this directory **only when the upstream agent doesn't ship a Docker image of its own**. The `setup.sh` script for each such agent is a one-shot wrapper: download → extract → docker build → (optional) `kind load`.

---

## Agents with official images (no setup needed)

Just point `spec.agent.image` at the vendor's image. The operator's `cc-extract-agent` initContainer will `cp` the jar out of it; you only need to tell us where the jar lives (`spec.agent.jarPath`).

### OpenTelemetry
```yaml
spec:
  agent:
    image:   ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest
    jarPath: /javaagent.jar
  profile: otel
```

### Datadog
```yaml
spec:
  agent:
    image:   gcr.io/datadoghq/dd-lib-java-init:latest
    jarPath: /datadog-java-agent.jar
  profileYAML: |
    apiVersion: classcache.dev/v1
    kind: AgentProfile
    metadata: { name: datadog }
    spec:
      agent: { jar: /opt/agent/agent.jar }
      build:   { javaagent: true, bootclasspath: true }
      runtime: { javaagent: true, bootclasspath: true }
```

### New Relic
```yaml
spec:
  agent:
    image:   newrelic/newrelic-java-init:latest
    jarPath: /newrelic-agent.jar
```

### Elastic APM
```yaml
spec:
  agent:
    image:   docker.elastic.co/observability/apm-agent-java:latest
    jarPath: /usr/agent/elastic-apm-agent.jar
```

> **Tip**: confirm a vendor's jar path by running
> `docker run --rm --entrypoint sh <image> -c 'ls /'` once.

---

## Agents without official images (use the catalog)

### Pinpoint

Pinpoint is a NAVER-origin APM, widely deployed in Korean enterprises. No
official Docker image exists for the agent (only for collector/web), and
the agent itself is multi-file: a bootstrap jar plus a `lib/`, `plugin/`,
`boot/` tree and config files.

```bash
BUILDER=docker TAG=v0.10 KIND_NAME=cc-quickstart \
  modules/agent-catalog/pinpoint/setup.sh
```

What this does:
1. Downloads `pinpoint-agent-3.1.0.tar.gz` from `github.com/pinpoint-apm/pinpoint`.
2. Extracts the agent tree into `modules/agent-catalog/pinpoint/agent/`.
3. Builds `classcache-agent-pinpoint:v0.10` (Alpine + the tree at `/agent`).
4. `kind load` if `KIND_NAME` is set.

ClassCache usage:
```yaml
spec:
  agent:
    image:      classcache-agent-pinpoint:v0.10
    jarPath:    /agent              # NOTE: directory, not a single jar
    configPath: /agent.conf
  profile: pinpoint
```

The operator's `cc-extract-agent` initContainer detects that `jarPath`
points at a directory and `cp -a` 's the whole tree into `/cc-staging/agent`.
The runtime profile then points `-javaagent` at
`/opt/agent/agent/pinpoint-bootstrap.jar`.

### Scouter

Scouter only releases tarballs on GitHub, so we wrap it in a tiny Alpine image. One command:

```bash
BUILDER=docker TAG=v0.9 KIND_NAME=cc-quickstart \
  modules/agent-catalog/scouter/setup.sh
```

What this does:
1. Downloads `scouter-min-2.20.0.tar.gz` from `github.com/scouter-project/scouter/releases`.
2. Extracts `scouter.agent.jar` + `scouter.conf`.
3. `docker build -t classcache-agent-scouter:v0.9`.
4. `kind load` if `KIND_NAME` is set.

Then in your ClassCache:
```yaml
spec:
  agent:
    image:      classcache-agent-scouter:v0.9
    jarPath:    /agent.jar
    configPath: /agent.conf
  profile: scouter
```

---

## Adding your own agent

If you have an internal/forked agent and want the same plug-and-play UX, build a tiny image yourself — see [`scouter/Dockerfile`](./scouter/Dockerfile):

```dockerfile
FROM alpine:3.20
COPY my-agent.jar  /agent.jar
COPY my-agent.conf /agent.conf   # optional
```

Push it to your registry, then reference it from `spec.agent.image`. No catalog entry is required for it to work — the operator doesn't look at this directory at runtime, it just instantiates whatever image you put in the CR.
