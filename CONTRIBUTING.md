# Contributing to cluster-classcache

Bug reports, fixes, and new agent profiles are welcome. This guide is short on
purpose — keep the bar low for first-time contributors and don't ask for things
the code itself doesn't need.

## Quick orientation

| Where | What |
|---|---|
| `modules/primer/` | Go — per-node DaemonSet that builds / pulls the CDS archive |
| `modules/operator/` | Go — `ClassCache` CRD controller + webhook |
| `modules/agent-catalog/` | Container images for APM agents that don't ship one upstream (e.g. Scouter) |
| `modules/agent-profiles/` | YAML profiles + JSON Schema |
| `modules/cli/` | C — `classcache` CLI for runtime introspection |
| `demos/01..08/` | Hypothesis-by-hypothesis validation scripts |
| `docs/` | DESIGN.md, REPORT.md, OVERVIEW.md |
| `deploy/` | Raw K8s manifests + Helm chart |

Start by reading [`docs/OVERVIEW.md`](docs/OVERVIEW.md) before touching code.

## Development setup

```bash
git clone https://github.com/junyeong0619/cluster-classcache.git
cd cluster-classcache

# End-to-end verification on a fresh kind cluster.
./scripts/quickstart.sh
```

If `quickstart.sh` doesn't reach `Ready` on your machine, that's the most
useful possible bug report.

### Building individual modules

```bash
# Go modules
cd modules/primer && go test ./... && go build ./...
cd modules/operator && go test ./... && go build ./...

# C CLI
cd modules/cli && make
```

### Adding a new APM agent

The pattern depends on whether the vendor ships a Docker image:

- **Official image exists** (OTel, Datadog, New Relic, Elastic):
  Add a profile YAML entry under `deploy/manifests/05-profile-catalog.yaml`
  and a row in `modules/agent-catalog/README.md`. No new image required.

- **Tarball only** (Scouter, Pinpoint):
  Add `modules/agent-catalog/<vendor>/` with a `Dockerfile` + `setup.sh` that
  downloads the upstream release and produces `classcache-agent-<vendor>:<tag>`.
  Then add the profile to the catalog ConfigMap.

## Commits

Conventional but lightweight. One commit = one intent. Examples:

```
agent-catalog: add Pinpoint setup.sh + profile
operator: clean up stale build_lock when holder pod disappears
docs: clarify distroless workaround
```

No Co-Authored-By trailers. AI assistance during authorship is fine and
common; don't add tags that confuse provenance.

## Pull request checklist

- [ ] `go test ./...` passes in any module you touched
- [ ] `make` builds in `modules/cli` (if you touched it)
- [ ] `./scripts/quickstart.sh` still reaches `Ready` if you touched the
      operator or primer
- [ ] New behavior described in either `docs/REPORT.md` or the PR body
- [ ] `docs/OVERVIEW.md` updated if you changed measured numbers or
      architecture

## Known good first issues

These are real, concrete, and explicitly called out as unfinished in the
docs — perfect for a first PR:

1. **Stale `archive:<key>:build_lock` cleanup** (REPORT §18.6, OVERVIEW §5)
2. **Stale peer set entries** — `classcache stats` already shows the problem
   live (peers count > running primers)
3. **Multi-host validation** — currently single-host kind only; even a k3d
   3-node measurement is genuinely new data
4. **Datadog / New Relic profile entries** — see `modules/agent-catalog/README.md`

## License

By contributing, you agree your contribution is licensed under Apache 2.0
(see `LICENSE`).
