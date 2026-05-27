# cluster-classcache — Architecture Deep Dive (v0.12)

> 시스템의 모든 컴포넌트, 데이터 흐름, 스펙 필드, 상태 머신, 호출 그래프를
> 한 문서에 정리합니다. 한 페이지 요약은 [`OVERVIEW.md`](./OVERVIEW.md) / [`OVERVIEW.ko.md`](./OVERVIEW.ko.md),
> 가설별 검증 결과는 [`REPORT.md`](./REPORT.md) 를 참고하세요.

---

## 목차

1. [전체 그림](#1-전체-그림)
2. [컴포넌트 카탈로그](#2-컴포넌트-카탈로그)
3. [`ClassCache` CRD 스펙](#3-classcache-crd-스펙)
4. [Valkey directory 데이터 모델](#4-valkey-directory-데이터-모델)
5. [Primer 라이프사이클](#5-primer-라이프사이클)
6. [Operator reconcile 로직](#6-operator-reconcile-로직)
7. [Workload 패치 + Webhook 흐름](#7-workload-패치--webhook-흐름)
8. [P2P 전송 프로토콜](#8-p2p-전송-프로토콜)
9. [CLI 의 데이터 출처](#9-cli-의-데이터-출처)
10. [Agent / Profile 카탈로그](#10-agent--profile-카탈로그)
11. [이미지 / 빌드 / 배포](#11-이미지--빌드--배포)
12. [관측 / 안전성 / 한계](#12-관측--안전성--한계)
13. [전체 코드 맵](#13-전체-코드-맵)
14. [버전별 변경 누적](#14-버전별-변경-누적)

---

## 1. 전체 그림

```
                   ┌───────────────────────────────────────────────────────────────┐
                   │                       KUBERNETES CLUSTER                       │
                   │                                                                │
  ┌────────────┐   │   ┌─────────────────┐         ┌──────────────────────┐         │
  │   User     │   │   │  Operator       │         │   Valkey (per-CC)    │         │
  │            │───┼──►│ controller-     │◄────────│   directory only,    │         │
  │ ClassCache │   │   │ runtime         │ HSET/   │   no archive blobs   │         │
  │   CR YAML  │   │   │ Reconcile()     │ SADD    └──────────┬───────────┘         │
  └────────────┘   │   └────┬────────────┘                    │                     │
                   │        │ owns                            │ pub/sub             │
                   │        │ (OwnerRef)                      │ "primer-events"     │
                   │        ▼                                 │                     │
                   │  ┌─────────────────────────────────────┐ │                     │
                   │  │  Primer DaemonSet                   │ │                     │
                   │  │  one Pod per worker node            │ │                     │
                   │  │                                     │◄┘                     │
                   │  │  initContainers:                    │                       │
                   │  │   cc-extract-app    (user's image)  │                       │
                   │  │   cc-extract-agent  (catalog image) │                       │
                   │  │                                     │                       │
                   │  │  main: primer (Go)                  │                       │
                   │  │   - sha256(jars) → key              │                       │
                   │  │   - check local hostPath            │                       │
                   │  │   - else ListPeers (zone-aware)     │                       │
                   │  │   - pull (verify sha256)            │                       │
                   │  │   - or build (lock + heartbeat)     │                       │
                   │  │   - SAdd peer + status PATCH        │                       │
                   │  │                                     │                       │
                   │  │  HTTP 8088: /archive/{key}          │                       │
                   │  └────────────┬────────────────────────┘                       │
                   │               │ hostPath /var/lib/classcache/<key>.jsa         │
                   │               ▼                                                │
                   │  ┌─────────────────────────────────────┐                       │
                   │  │  Workload Deployment (user app)     │                       │
                   │  │                                     │                       │
                   │  │  initContainer cc-wait-archive      │                       │
                   │  │   (busybox: sh loop until .jsa)     │                       │
                   │  │                                     │                       │
                   │  │  container app                      │                       │
                   │  │   $ java                            │                       │
                   │  │       -XX:SharedArchiveFile=…       │                       │
                   │  │       -XX:ArchiveRelocationMode=0   │                       │
                   │  │       -Xshare:on                    │                       │
                   │  │       -Xbootclasspath/a:agent       │                       │
                   │  │       -jar app.jar                  │                       │
                   │  │                                     │                       │
                   │  │  4 JVMs same node → mmap共有 →      │                       │
                   │  │  Shared_Clean ≈ 61 MB / 4 JVMs      │                       │
                   │  └─────────────────────────────────────┘                       │
                   │                                                                │
                   │  ┌─────────────────────────────────────────────────────────┐   │
                   │  │  Mutating Admission Webhook (operator binary, alt mode) │   │
                   │  │   triggered by label classcache.dev/inject=<cc-name>     │   │
                   │  │   patches Pod CREATE (mounts, env, initContainer)        │   │
                   │  │   used when spec.patchMode=Webhook                       │   │
                   │  └─────────────────────────────────────────────────────────┘   │
                   └───────────────────────────────────────────────────────────────┘
                                  ▲
                                  │ kubectl exec / docker exec
                                  │
                            ┌─────┴─────┐
                            │ classcache│   ← C CLI on the operator's laptop
                            │  stats /  │     (~1.3k LOC, hiredis+cJSON+libcurl)
                            │  top /    │
                            │  events   │
                            └───────────┘
```

세 가지 데이터 흐름이 동시에 돈다:

- **컨트롤 플레인**: ClassCache CR → operator → derived resources (Deployments,
  DaemonSet, Service, SA, Role, RoleBinding, ConfigMap).
- **분배 플레인**: primer → Valkey (메타데이터) + primer-to-primer HTTP (binary).
- **사용 플레인**: workload Pod 가 같은 노드의 hostPath archive 를 mmap.

각 플레인은 독립적이라 하나가 죽어도 다른 게 망하지 않는다 (예: Valkey 가
죽어도 노드의 로컬 archive 는 동작 — 새 노드만 build 부담 받음).

---

## 2. 컴포넌트 카탈로그

| 컴포넌트 | 언어 | 라인 수 | 핵심 책임 | 코드 위치 |
|---|---|---|---|---|
| Operator | Go (controller-runtime) | ~1000 | ClassCache 의 reconcile 진입점 + Webhook server | `modules/operator/` |
| Primer | Go (go-redis + 표준 라이브러리) | ~900 | 노드별 archive 빌드/풀/서빙 + status PATCH | `modules/primer/` |
| Valkey | (외부) Linux Foundation BSD-3 | n/a | 메타데이터 directory (peer set, build_lock, sha256, zone) | `deploy/manifests/` 가 image 만 참조 |
| Agent catalog | Dockerfile + setup.sh | ~150 | Scouter / Pinpoint 처럼 공식 image 없는 agent 의 wrapper | `modules/agent-catalog/` |
| Profile catalog | YAML | ~80 | "이 agent 는 build/runtime 에 이렇게" 의 선언 | `deploy/manifests/05-profile-catalog.yaml` |
| AgentProfile schema | JSON Schema | ~100 | profile YAML 의 유효성 검증 (loader 가 적용) | `modules/agent-profiles/schema/v1.json` |
| CLI | C (hiredis + cJSON + libcurl) | ~1300 | 런타임 introspection (stats / top / archives / peers / events) | `modules/cli/` |
| In-house APM agent v0.1 | Java (ByteBuddy) | (small) | 회귀 baseline — 검증용 | `modules/apm-agent-v0.1/` |
| Helm chart | Helm template | ~250 | `helm install classcache` 한 줄 배포 | `deploy/helm/classcache/` |
| Demos 01-09 | Bash / Java / Dockerfile | 1000+ | 가설별 검증 (Phase B → k3d multi-node) | `demos/` |

각 컴포넌트의 책임 한 단락:

### 2.1 Operator

`modules/operator/cmd/main.go` 가 진입점. controller-runtime manager 를 띄우고
두 가지를 wire-up:

- **`ClassCacheReconciler`** (`controllers/classcache_controller.go`)
  - watch: `ClassCache` (For), Deployment / DaemonSet / Service / SA /
    ConfigMap / Role / RoleBinding (Owns)
  - 각 reconcile 마다 다음 순서로 derived resource 를 ensure:
    1. `reconcileValkey` — `spec.valkey.create=true` 면 Deployment+Service 만듦
    2. `reconcilePrimerRBAC` — per-CC SA + Role(`classcaches/status:patch`) + RB
    3. `reconcileProfileConfigMap` — `spec.profile` 또는 `spec.profileYAML` →
       per-CC ConfigMap (mount 됨)
    4. `reconcilePrimer` — Primer DaemonSet (initContainer 2개 + main + env)
    5. `reconcileWorkload` (Owned 모드만) — 사용자 Deployment 의 PodTemplate
       에 initContainer/volume/env 주입. `cc.Status.ArchiveKey` 가 비어있으면
       보류.
    6. status 갱신 (Phase / ReadyPeers / Conditions)
- **`PodMutator`** (`webhook/pod_mutator.go`)
  - `/mutate-pod` 에 webhook 등록 (`cmd/main.go` 의 `mgr.GetWebhookServer().Register`)
  - Pod CREATE 가 들어오면 라벨 `classcache.dev/inject=<cc-name>` 확인
  - 매칭되면 같은 namespace 의 `ClassCache` lookup → `spec.patchMode==Webhook`
    + `status.archiveKey!=""` 인 경우에만 patch 수행
  - controllers 의 `ApplyPodPatch` 를 그대로 호출 (동일 로직 보장)

이미지: `gcr.io/distroless/static:nonroot` → **11.5 MiB**. binary 만 들어있고
shell 도 libc 도 없음. 안정성/공격 표면 minimize.

### 2.2 Primer

`modules/primer/main.go` 가 진입점. 6 모듈로 책임 분리:

| 파일 | 함수 한 단락 |
|---|---|
| `main.go` | env → Config 매핑, 시그널 핸들러, orchestrator wiring |
| `orchestrator.go` | 전체 라이프사이클 state machine (§5) |
| `directory.go` | Valkey wrapper — Register / ListPeers / build_lock / heartbeat / sha256 / zone |
| `archive.go` | sha256(file), ComputeKey, ArchivePath helpers |
| `peer.go` | HTTP server (`/archive/{key}`) + `PullFromPeer` with sha256 verify |
| `builder.go` | Profile loader (+ schema 무관 validation), BuildArgs, JVM 서브프로세스 |
| `status_publisher.go` | client-go 없이 SA token + ca.crt 로 CR status PATCH |

추가 모듈:
- `archive_test.go`, `directory_test.go`, `peer_test.go`, `builder_test.go` —
  16개 테스트 함수 (miniredis 기반, 외부 의존 없음)

이미지: `eclipse-temurin:22-jre` → **93.8 MiB** (JVM 포함, archive build 에 필요).
원래 `distroless/java22-debian12:nonroot` 로 ~80 MB 였으나 그 태그가 GCR 에서
제거된 후 `eclipse-temurin:22-jre` 로 폴백 (REPORT §17.4).

### 2.3 Valkey

`valkey/valkey:7.2-alpine` **StatefulSet** (v0.12-A 부터). per-ClassCache 로
띄우거나 (`spec.valkey.create=true`) 외부에 띄워둔 인스턴스를 공유
(`addr` 명시).

v0.12-A 변경: Deployment + emptyDir → StatefulSet + PVC (256Mi RWO) +
AOF persistence (`--appendonly yes --appendfsync everysec`). Pod 가 단순
재시작 (eviction, OOM, drain, kubelet restart) 해도 directory 데이터는
PVC 에 살아남아 AOF replay 로 1초 안에 복구. PVC 가 노드 간 follow 하느냐
는 사용자 StorageClass 에 종속:

| StorageClass | 노드 drain + 다른 노드 reschedule |
|---|---|
| EBS gp3 / GCE PD / Azure Disk | ✅ PVC 따라감, 데이터 그대로 |
| local-path-provisioner | ❌ PV 가 노드 종속, Pod 가 영원히 Pending |
| NFS / Longhorn | ✅ |

데이터는 메타데이터만. archive 바이너리는 절대 안 저장 (binary 는 노드 간
HTTP). 그래서 노드 200 + archive 50종 가정 시에도 ~5-10 MB.

진짜 HA (multi-replica + 자동 failover) 는 v0.12-B 일감, 멀티 호스트 실측
환경 확보 후.

### 2.4 Agent catalog

`modules/agent-catalog/` 아래에 vendor 별 디렉토리:
- `scouter/` — Scouter v2.20+ tarball wrapper (단일 jar agent)
- `pinpoint/` — Pinpoint v3.1.0 tarball wrapper (multi-file agent tree, NAVER 출신)

각 디렉토리는 `Dockerfile` + `setup.sh` 가 있고, `setup.sh` 가:
1. upstream GitHub Release 에서 tarball 다운로드
2. 필요한 파일 추출
3. `docker build` (or BUILDER) 로 `classcache-agent-<name>:<tag>` 생성
4. optional `kind load`

OTel / Datadog / New Relic / Elastic 은 **공식 image** 가 있으므로 catalog
없이 직접 가리키면 됨 (`modules/agent-catalog/README.md` 의 표 참조).

### 2.5 CLI (`classcache`)

`modules/cli/` C 작업. 단일 binary `classcache`, 5개 subcommand:

| Subcommand | 출처 데이터 |
|---|---|
| `stats` | K8s API + Valkey + smaps (docker exec 또는 kubectl exec) |
| `top` | `stats` 를 N초마다 refresh (ANSI clear) |
| `archives` | Valkey only — peer count + size + JVM |
| `peers <key>` | Valkey only — peer endpoint 목록 |
| `events` | Valkey pub/sub `primer-events` subscribe |

`make install` 시 `/usr/local/bin/classcache` + `kubectl-classcache` symlink →
`kubectl classcache stats` 로도 호출 가능 (kubectl plugin convention).

---

## 3. `ClassCache` CRD 스펙

API group/version: `classcache.dev/v1`. CRD 정의는 `deploy/manifests/00-classcache-crd.yaml`,
타입 정의는 `modules/operator/api/v1/classcache_types.go`.

### 3.1 Spec

```yaml
spec:
  workloadRef:
    apiVersion: apps/v1            # default
    kind: Deployment               # default; only Deployment supported (v0.11)
    name: <required>
  app:
    image:          <user's app image — what your Deployment uses>
    jarPath:        /app.jar       # default; path to the fat jar inside Image
    extractorImage: <optional>     # used when Image is distroless (v0.10+)
  agent:
    image:      <catalog image or vendor's official image>
    jarPath:    /agent.jar         # default; "/agent" if directory mode
    configPath: <optional>         # e.g. /agent.conf for Scouter
  profile:     <name>              # looked up in classcache-profiles ConfigMap
  profileYAML: <inline YAML>       # alternative to profile; wins if both set
  primerImage: classcache-primer:v0.9-universal   # default
  archiveDir:  /var/lib/classcache                # default; hostPath on workers
  patchMode:   Owned                              # default; or Webhook
  valkey:
    create: true                   # default
    addr:   <host:port>            # required when create=false
    image:  valkey/valkey:7.2-alpine               # default
```

각 필드의 의미:

- **`workloadRef`** — 어떤 Deployment 에 archive 부팅을 입힐지. operator 가
  Owned 모드에선 직접 patch, Webhook 모드에선 Pod 생성 시점에 admission.
- **`app.image`** — 사용자의 평소 image. CI/CD 가 만들어내는 그것.
- **`app.extractorImage`** — `app.image` 가 distroless 면 cc-extract-app
  initContainer 가 거기에 sh/cp/java 가 없어서 jar 추출 못 함. companion
  image (e.g. `eclipse-temurin:22-jdk-alpine` + 같은 jar) 를 가리킴.
- **`agent.image`** — 잘 알려진 agent 면 공식 image (OTel/DD/NR/Elastic) 또는
  carlog image (`classcache-agent-scouter` / `classcache-agent-pinpoint`).
- **`agent.jarPath`** — 파일 경로 또는 디렉토리 경로 (Pinpoint). extractor 가
  `[ -d ]` 분기로 자동 감지.
- **`profile`** — `classcache-system/classcache-profiles` ConfigMap 에서
  `<name>.yaml` key 로 lookup.
- **`profileYAML`** — 같은 schema 의 inline 형태. catalog 없이 self-contained
  한 CR 작성 가능.
- **`patchMode`** — Owned 모드는 operator 가 Deployment 직접 mutate. Webhook
  모드는 Pod 생성 시점에 admission patch (GitOps drift 방지).
- **`valkey.create=false`** + `addr` — 외부 / 공유 Valkey 사용. 여러 CC 가 같은
  Valkey 공유해서 cross-namespace archive 재사용 가능.

### 3.2 Status

```yaml
status:
  archiveKey:  "99cdff82d2f81455"   # set by primer (PATCH); empty until primer registers
  phase:       Ready                 # Pending → PrimerReady → WorkloadPatched → Ready / Failed
  readyPeers:  2
  lastError:   ""
  conditions:  []                    # metav1.Condition list
```

Phase 진행:
1. **Pending** — operator 가 아직 Valkey + Primer DS 까지 안 만든 상태
2. **PrimerReady** — Primer DS 의 numberReady > 0 이지만 status.archiveKey 가 아직
3. **WorkloadPatched** — operator 가 Deployment 에 initContainer + JVM opts 주입 완료
4. **Ready** — Workload Pod 가 ReadyReplicas == Replicas
5. **Failed** — reconcile 중 에러 (LastError 에 메시지)

---

## 4. Valkey directory 데이터 모델

모든 key 가 `archive:<sha256-16chars>` prefix 로 묶임.

| Key 패턴 | 타입 | 내용 |
|---|---|---|
| `archive:<key>` | Hash | size, registered_at, jvm, arch, **sha256** (v0.11+) |
| `archive:<key>:peers` | Set | `{<PodIP>:8088, ...}` — primer 가 SAdd 로 등록, graceful shutdown 시 SRem |
| `archive:<key>:peer-zone` | Hash | endpoint → zone (v0.11+, optional) |
| `archive:<key>:build_lock` | String | holder endpoint, **TTL 60 s** (v0.11), heartbeat 로 renew |
| (Pub/Sub) `primer-events` | Channel | JSON `{node, key, method, elapsed_ms, archive_size}` 매 acquire 시 publish |

이전 (v0.10 까지) 의 build_lock TTL 은 10분이었음. v0.11 에서 60초 + heartbeat
패턴으로 짧혀짐. 이유는 §5.2 의 stale lock 문제.

### 4.1 데이터 모델 예시 (실제 cluster 데이터)

```
$ valkey-cli KEYS 'archive:*'
1) "archive:99cdff82d2f81455"
2) "archive:99cdff82d2f81455:peers"
3) "archive:99cdff82d2f81455:peer-zone"
4) "archive:99cdff82d2f81455:build_lock"

$ valkey-cli HGETALL archive:99cdff82d2f81455
 1) "size"           2) "35061760"
 3) "registered_at"  4) "1735127893"
 5) "jvm"            6) "openjdk version \"22.0.2\" 2024-07-16"
 7) "arch"           8) "arm64"
 9) "sha256"        10) "f3a8…<64 hex>"

$ valkey-cli SMEMBERS archive:99cdff82d2f81455:peers
1) "10.244.1.55:8088"
2) "10.244.2.58:8088"

$ valkey-cli HGETALL archive:99cdff82d2f81455:peer-zone
1) "10.244.1.55:8088"  2) "us-east-1a"
3) "10.244.2.58:8088"  4) "us-east-1b"

$ valkey-cli GET archive:99cdff82d2f81455:build_lock
"10.244.2.58:8088"   # holder; key disappears 60s after last heartbeat
```

이 데이터 모델은 매우 작음 — archive 50종 + peer 200개 + zone 정보까지 다
포함해도 ~5-10 MB. Valkey 단일 인스턴스로 충분.

---

## 5. Primer 라이프사이클

`orchestrator.go` 의 `Run()` 메서드 한 줄씩.

### 5.1 State machine

```
                          ┌─────────────────┐
                          │   primer start  │
                          └────────┬────────┘
                                   │
                                   ▼
                         WaitReady(valkey, 30s)
                                   │
                                   ▼
                          key = ComputeKey()
                                   │
                                   ▼
                  ┌────────────────┴────────────────┐
                  │ LocalArchiveExists(key) ?       │
                  └────────┬───────────────┬────────┘
                       yes │           no  │
                           │               │
                           │               ▼
                           │      ListPeersZoneAware()
                           │               │
                           │       any peer? ─┐
                           │           │      │ no
                           │       yes │      │
                           │           ▼      ▼
                           │     PullFromPeer (verify sha256)
                           │           │
                           │  success? ┴─┐
                           │       yes │ │ no
                           │           │ │
                           │           │ ▼
                           │           │ TryAcquireBuildLock(TTL=60s)
                           │           │   │
                           │           │   │ won? ─────┐
                           │           │   │           │ no
                           │           │   │           │
                           │           │   │ yes       ▼
                           │           │   │   waitForPeer (poll)
                           │           │   │           │
                           │           │   ▼           │
                           │           │  start heartbeat goroutine
                           │           │   │ (renew every TTL/3)
                           │           │   │
                           │           │   ▼
                           │           │  BuildLocally(jar, agent, profile)
                           │           │   │
                           │           │   ▼
                           │           │  ReleaseBuildLock (success or fail)
                           │           │   │
                           │           ▼   ▼
                           ▼       ┌───────┴────────┐
                       method =    │  method =      │
                       "local-hit" │  "pulled-from" │
                                   │  "built-locally"│
                                   │  "pulled-after-wait" │
                                   └────────┬────────┘
                                            │
                                            ▼
                              sha256 = sha256File(archive)
                                            │
                                            ▼
                              dir.Register(key, selfEP, size, jvm,
                                          arch, sha256, zone)
                                            │
                                            ▼
                              o.registeredKey/Endpoint = ... (for graceful)
                                            │
                                            ▼
                              dir.PublishEvent(...)   ← classcache events 가 잡음
                                            │
                                            ▼
                              publisher.PublishArchiveKey(ctx, key, size)
                                            │            ← CR status PATCH
                                            ▼
                              peer.Start()  → HTTP 8088 /archive/{key}
                                            │
                                            ▼
                              < block on ctx.Done() — SIGTERM 까지 대기 >
                                            │
                                            ▼
                              orch.GracefulShutdown(shutdownCtx)
                                            │
                                            ▼
                              dir.Unregister(key, selfEP)   ← SREM
                              peer.Shutdown()
                                            │
                                            ▼
                                     primer exits
```

### 5.2 v0.11 의 cleanup 개선

v0.10 까지의 문제 (REPORT §18.6 에 보고됨):
- primer 가 build 중 죽으면 `build_lock` 이 TTL 10분 동안 남아 다른 primer
  가 다 wait
- primer 가 그냥 죽으면 자기 PodIP 가 `peers` set 에 남아 stale peer 가 됨

v0.11 의 해법 (`commit 9a3f6c5`):

1. **Build lock TTL 단축**: 10분 → 60초.
2. **Heartbeat**: build 진행 중에 매 20초마다 `RenewBuildLock` 호출.
   Lua atomic GET-then-PEXPIRE 로 race-free.
3. **Explicit release**: build 성공/실패 둘 다 `ReleaseBuildLock` 호출.
   Lua atomic GET-then-DEL 로 다른 holder 의 lock 안 yank.
4. **Graceful unregister**: SIGTERM 시 orchestrator 가 `Unregister` 로 SREM.

이론적으로 primer 가 SIGKILL 로 죽으면 unregister 못 함. 그 경우 lazy
cleanup: 다른 primer 가 그 peer 에 pull 시도 → 실패 → 다음 peer. 죽은
peer 가 set 에 남아있는 자체가 functionally 무해 (functional cleanup 은
v0.12 일감).

### 5.3 Build phase 디테일 (`builder.go`)

1. `LoadProfile(/etc/classcache/profile.yaml)` — YAML → struct + 필드 검증
   (`apiVersion = classcache.dev/v1`, `kind = AgentProfile`, name 있음, agent.jar 있음)
2. `BuildArgs(profile, archivePath, extractedAppJar)` — JVM argv 생성:
   - profile.spec.build.bootclasspath=true 면 `-Xbootclasspath/a:<agent.jar>`
   - `-XX:+UnlockDiagnosticVMOptions -XX:+AllowArchivingWithJavaAgent
     -XX:ArchiveClassesAtExit=<path>` 항상
   - profile.spec.build.javaagent=true 면 `-javaagent:<agent.jar>`
   - profile.spec.build.extraJvmOpts 펼침
   - `-jar <extractedAppJar>` 끝
3. `ensureExtracted` — 한 번만 `java -Djarmode=tools -jar app.jar extract`
4. JVM 띄움 → `localhost:8080/hello` 가 200 응답할 때까지 polling
5. `/hello` + `/work/100` 호출로 warmup
6. SIGINT 보냄 → JVM shutdown hook 이 ArchiveClassesAtExit 트리거 → archive
   파일 dump
7. wait + (실패 시) kill -9 fallback

총 build 시간 ~3 초 (Go primer, kind 환경).

---

## 6. Operator reconcile 로직

`controllers/classcache_controller.go` 의 `Reconcile(req)` 한 줄씩.

### 6.1 진입 + 기본 검사

```go
cc := &v1.ClassCache{}
if err := r.Get(ctx, req.NamespacedName, cc); err != nil {
    return ctrl.Result{}, client.IgnoreNotFound(err)
}
applyDefaults(cc)        // PrimerImage, ArchiveDir, PatchMode, WorkloadRef.Kind
if !cc.DeletionTimestamp.IsZero() {
    return ctrl.Result{}, nil    // owner refs handle cascade delete
}
```

`applyDefaults` 가 채우는 것:
- `PrimerImage` → `classcache-primer:v0.9-universal`
- `ArchiveDir` → `/var/lib/classcache`
- `PatchMode` → `Owned`
- `WorkloadRef.APIVersion/Kind` → `apps/v1` / `Deployment`
- `App.JarPath` → `/app.jar`
- `Agent.JarPath` → `/agent.jar`

### 6.2 단계별 sub-reconciler

1. `reconcileValkey(ctx, cc)` → `valkeyAddr` (string)
   - Create=true: Deployment + Service ensure (OwnerRef 설정)
   - Create=false: cc.Spec.Valkey.Addr 검증 + 그대로 사용
2. `reconcilePrimerRBAC(ctx, cc)` — per-CC SA + Role + RoleBinding
   - Role 의 PolicyRule: `classcache.dev / classcaches/status / get,patch,update`
   - SA 가 Pod 의 ServiceAccountName 으로 사용됨 → primer 가 status PATCH 가능
3. `reconcileProfileConfigMap(ctx, cc)` — `cc-<name>-profile` ConfigMap
   - Inline (`spec.profileYAML`) 우선
   - 없으면 `classcache-system/classcache-profiles` 의 `<profile>.yaml` 값
4. `reconcilePrimer(ctx, cc, valkeyAddr)` — Primer DaemonSet
   - `extractorImage` 가 있으면 cc-extract-app initContainer 의 image 로 사용
   - `app.image` 와 `agent.image` 가 둘 다 있으면 v0.9+ extractor 패턴
   - 없으면 v0.7 baked-in primer 이미지 패턴 (deprecation 후보)
5. `reconcileWorkload(ctx, cc)` (Owned 모드 + `cc.Status.ArchiveKey` != "" 일 때)
   - `ApplyPodPatch(template, PatchSpec{key, dir, profile, ccName})`
   - PodTemplate 에 initContainer `cc-wait-archive` + volume `cc-archive` +
     env `CLASSCACHE_JAVA_OPTS` 주입
   - annotation `classcache.dev/archive-key` 박음
6. `observePhase(ctx, cc)` → Phase (string) + ReadyPeers (int32)
7. `r.Status().Update(cc)` — 단, `ArchiveKey` 는 안 set (primer 가 PATCH 함)

### 6.3 Workload patch 의 idempotent 보장

`workload_patch.go` 의 `ApplyPodPatch(template, spec)` 가 세 가지 ensure:

- `ensureVolume` — `cc-archive` 가 이미 있으면 그대로
- `ensureInitContainer` — 이름이 `cc-wait-archive` 인 init 이 있으면 image/command
  비교해서 다르면 갱신, 같으면 노터치
- `ensureWorkloadMountAndEnv` — 각 사용자 container 마다 volumeMount + env 추가

return value `changed bool` 로 callers (operator + webhook) 가 무거운 Update 를
스킵 가능.

### 6.4 Owner reference 와 cascade delete

operator 가 만드는 모든 derived resource 에 `ctrl.SetControllerReference(cc, obj, scheme)`
호출 → CR 가 삭제되면 Kubernetes GC 가 cascading delete 수행. operator 가
finalizer 같은 거 안 써도 자동 정리됨.

### 6.5 `SetupWithManager` 의 owns

```go
For(&v1.ClassCache{}).
Owns(&appsv1.Deployment{}).
Owns(&appsv1.DaemonSet{}).
Owns(&corev1.Service{}).
Owns(&corev1.ServiceAccount{}).
Owns(&corev1.ConfigMap{}).
Owns(&rbacv1.Role{}).
Owns(&rbacv1.RoleBinding{}).
```

각 owned 리소스가 바뀌면 controller-runtime 이 자동으로 reconcile 트리거.
status PATCH 도 같은 controller 가 watch (For=&CC{}) 라 primer 의 PATCH 직후
reconcile 재실행 → workload patch.

---

## 7. Workload 패치 + Webhook 흐름

두 가지 모드.

### 7.1 Owned mode (default)

```
operator reconcile
  → cc.Status.ArchiveKey != ""
  → r.Get(workloadDeployment)
  → ApplyPodPatch(&dep.Spec.Template, PatchSpec{...})
  → if changed: r.Update(dep)
```

장점: operator 한 군데서 모든 일을 함. webhook 인프라 (cert-manager 등) 불필요.
단점: ArgoCD/Flux 같은 GitOps 도구가 Deployment 를 다시 reconcile 하면 patch 가
다시 사라짐. operator 가 또 patch → 끝없는 ping-pong (drift).

### 7.2 Webhook mode

```
사용자 Deployment template 에 라벨 추가:
  labels:
    classcache.dev/inject: <classcache-name>

Pod CREATE → kube-apiserver 가 MutatingWebhookConfiguration 매칭
  → POST https://classcache-webhook.classcache-system.svc/mutate-pod
  → PodMutator.Handle:
      decode Pod
      라벨 읽고 ClassCache 찾기
      patchMode 확인 + Status.ArchiveKey 확인
      ApplyPodPatch (controllers 와 동일 로직)
      JSON patch response
```

`failurePolicy: Ignore` (Helm 기본) — webhook 이 다운이어도 Pod 생성은 막지
않음. archive 없이 부팅됨 (slow path).

cert TLS:
- `cert-manager.io/inject-ca-from` annotation → cert-manager 가 caBundle 자동
- `Issuer (selfsigned) → Certificate → Secret classcache-webhook-tls` →
  operator Pod 의 `/etc/webhook/certs` 마운트

### 7.3 Patch 의 정확한 결과물

```yaml
spec:
  template:
    metadata:
      annotations:
        classcache.dev/archive-key: "99cdff82d2f81455"
    spec:
      initContainers:
      - name: cc-wait-archive
        image: busybox:1.36
        command:
        - sh
        - -c
        - |
          set -e
          for i in $(seq 1 600); do
            [ -s /var/lib/classcache/99cdff82d2f81455.jsa ] && exit 0
            sleep 1
          done
          echo "archive .../99cdff82d2f81455.jsa never appeared" >&2; exit 1
        volumeMounts:
        - name: cc-archive
          mountPath: /var/lib/classcache
          readOnly: true
      containers:
      - name: app
        env:
        - name: CLASSCACHE_JAVA_OPTS
          value: >-
            -XX:+UnlockDiagnosticVMOptions
            -XX:+AllowArchivingWithJavaAgent
            -XX:SharedArchiveFile=/var/lib/classcache/99cdff82d2f81455.jsa
            -XX:ArchiveRelocationMode=0
            -Xshare:on
        volumeMounts:
        - name: cc-archive
          mountPath: /var/lib/classcache
          readOnly: true
      volumes:
      - name: cc-archive
        hostPath:
          path: /var/lib/classcache
          type: DirectoryOrCreate
```

사용자 컨테이너의 entrypoint 가 `CLASSCACHE_JAVA_OPTS` 환경변수를 어떻게 처리
하느냐는 사용자 몫. 보통:
- `java $CLASSCACHE_JAVA_OPTS -jar app.jar`
- 또는 `JAVA_TOOL_OPTIONS=$CLASSCACHE_JAVA_OPTS` 로 자동 적용

---

## 8. P2P 전송 프로토콜

`peer.go` 에서 정의.

### 8.1 서버 측 (`PeerServer`)

- `:8088/archive/{key}` HTTP GET
- 디스크의 `<archiveDir>/<key>.jsa` 를 `io.Copy(w, file)` 로 streaming
- `Content-Type: application/octet-stream`, `Content-Length` 설정
- 인증 없음 (cluster-internal HTTP, v0.11). mTLS 는 v0.12+ 후보.

### 8.2 클라이언트 측 (`PullFromPeer`)

```go
func PullFromPeer(ctx, peer, key, dest, timeout, expectedSHA256) (n int64, err)
```

- `http.Client{Timeout: timeout}` 으로 GET
- 200 OK 가 아니면 에러
- `io.Copy(file, resp.Body)` 로 dest 에 streaming
- file close 후 `sha256File(dest)` 호출
- `expectedSHA256 != ""` && 다르면: dest 삭제 + error
- empty expected: skip verify (v0.10 이전 archive 호환)

### 8.3 Race 처리

여러 primer 가 동시에 시작하면 모두 ListPeers 가 비어있어 모두 build_lock
경합. SETNX (Lua: SET NX EX) 가 single-winner 보장 → 한 명만 build, 나머지는
60초 짧은 TTL 후 ListPeers 재시도. v0.11 의 heartbeat 로 winning primer 는
build 가 60초 넘어도 lock 유지.

### 8.4 Zone 우선 정렬

```go
peers := dir.ListPeersZoneAware(ctx, key, selfEP, selfZone)
// → same-zone first, then others
for _, peer := range peers {
    if pull succeeds: return
}
// fallback to build_lock
```

같은 zone (AZ) intra-bandwidth 가 free 이거나 더 빠른 환경에서 유효. v0.11
은 protocol 만 — operator 가 `PEER_ZONE` 환경변수를 자동 채우는 건 v0.12.

---

## 9. CLI 의 데이터 출처

`modules/cli/src/stats.c` 가 세 소스를 한 화면에 합침.

### 9.1 K8s API (`kubectl + cJSON`)

`kube.c` 가 다음 명령들을 popen 으로 실행:
- `kubectl get classcaches.classcache.dev -A -o json`
- `kubectl -n <ns> get pod -l app=<workload> -o json`
- `docker exec <kind-node> pgrep -f /work/extracted/app.jar`

각 출력을 `cJSON_Parse` 로 파싱 후 작은 struct (`cc_classcache`, `cc_pod`)
로 옮김.

kubectl 셸링 vs libcurl: kubeconfig 의 current-context, OIDC 토큰, TLS 인증서
경로를 다시 구현하느니 이미 인증된 kubectl 을 쓰는 게 합리적. 핫 패스 아님.

### 9.2 Valkey (`hiredis`)

`valkey.c` 의 wrapper 들:
- `vk_list_archives` — `KEYS archive:*` 후 `archive:<16hex>` 패턴 필터
- `vk_list_peers` — `SMEMBERS archive:<key>:peers`
- `vk_archive_meta` — `HGETALL archive:<key>`
- `vk_archive_build_lock` — `GET archive:<key>:build_lock`
- `vk_subscribe_events` — `SUBSCRIBE primer-events` 루프 + callback

### 9.3 smaps (`/proc/<pid>/smaps`)

`smaps.c` 가 라인 단위로 파싱:
- 헤더 (`addr-range perms offset dev inode  pathname`) 가 needle (`.jsa`) 매칭
  → 블록 시작
- `Rss:`, `Pss:`, `Shared_Clean:`, ... 누적
- `VmFlags:` 만나면 블록 종료

두 가지 접근 경로:
- **kind**: `smaps_read_kind` — `docker exec <node> cat /proc/<pid>/smaps`
  (host PID namespace 공유 가설)
- **k3d / managed K8s**: `smaps_read_pod` — `kubectl exec <pod> -c app -- cat /proc/1/smaps`
  (PID 1 = workload JVM convention)

`stats.c` 의 `aggregate_node` 가 docker exec 먼저 시도, 실패하면 kubectl exec
폴백. 결과의 `source:` 라인이 어느 경로가 동작했는지 표시.

### 9.4 `events` subcommand

```
classcache events
  → vk_subscribe_events
  → on each "message" reply:
       cJSON_Parse(payload)
       extract node / key / method / elapsed_ms / archive_size
       print "[hh:mm:ss] <node>  <method>  <ms> ms  <size>  key=<key>"
  → 무한 루프, Ctrl-C 로 종료
```

primer 의 `dir.PublishEvent` 가 매 acquire 마다 publish 하므로, 실시간으로
"누가 build/pull 했는지" 보임. 데모 / 디버깅 친화적.

---

## 10. Agent / Profile 카탈로그

### 10.1 AgentProfile JSON Schema (`modules/agent-profiles/schema/v1.json`)

```json
{
  "apiVersion": "classcache.dev/v1",
  "kind":       "AgentProfile",
  "metadata":   { "name": "scouter", "version": "2.21.3" },
  "spec": {
    "agent": {
      "jar":    "/opt/agent/agent.jar",
      "config": "/opt/agent/agent.conf"
    },
    "build": {
      "javaagent":     true,
      "bootclasspath": true,
      "extraJvmOpts":  ["-Dscouter.config=/opt/agent/agent.conf"]
    },
    "runtime": {
      "javaagent":     false,
      "bootclasspath": true,
      "extraJvmOpts":  ["-Dscouter.config=/opt/agent/agent.conf"]
    }
  }
}
```

- `build` 는 archive 만들 때 (premain 돌게 해서 변환 결과를 baked-in)
- `runtime` 은 사용자 워크로드 부팅 때 (premain 없이도 변환 결과 사용)

3개 reference profile (`deploy/manifests/05-profile-catalog.yaml`):
- `scouter` — v0.5 default. hybrid (build=on, runtime=off)
- `otel` — runtime 도 on (OTel SDK 의 isolated CL 한계)
- `apm-v01` — `modules/apm-agent-v0.1` regression baseline (no-op)
- `pinpoint` (v0.10+) — multi-file agent tree, build & runtime 둘 다 on

### 10.2 catalog image 와 공식 image 비교

| 출처 | image | jarPath | 비고 |
|---|---|---|---|
| Catalog (우리 wrap) | `classcache-agent-scouter:v0.9` | `/agent.jar` | Scouter 공식 image 없음 |
| Catalog (우리 wrap) | `classcache-agent-pinpoint:v0.10` | `/agent` (dir) | Pinpoint 공식 agent image 없음, multi-file |
| 공식 image | `ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest` | `/javaagent.jar` | OTel Operator init image |
| 공식 image | `gcr.io/datadoghq/dd-lib-java-init:latest` | `/datadog-java-agent.jar` | Datadog 공식 |
| 공식 image | `newrelic/newrelic-java-init:latest` | `/newrelic-agent.jar` | New Relic 공식 |
| 공식 image | `docker.elastic.co/observability/apm-agent-java:latest` | `/usr/agent/elastic-apm-agent.jar` | Elastic 공식 |

operator 의 `cc-extract-agent` initContainer 가 어느 image 든 똑같이 처리:
`/agent.jar` 한 파일 또는 `/agent` 한 디렉토리를 `/cc-staging/` 으로 cp.

---

## 11. 이미지 / 빌드 / 배포

### 11.1 빌드 산출물

| Image | Dockerfile | 사이즈 | 빌드 스크립트 |
|---|---|---|---|
| `classcache-operator:<tag>` | `modules/operator/Dockerfile` | 11.5 MiB (distroless/static) | `modules/operator/build.sh` |
| `classcache-primer:<tag>` | `modules/primer/Dockerfile` | (apm-v0.1 베이스, 데모 path) | `modules/primer/build.sh` |
| `classcache-primer:v0.9-universal` | `modules/primer/Dockerfile.universal` | 93.8 MiB | `modules/primer/build-universal.sh` |
| `classcache-agent-scouter:<tag>` | `modules/agent-catalog/scouter/Dockerfile` | 6.1 MiB (Alpine + jar) | `modules/agent-catalog/scouter/setup.sh` |
| `classcache-agent-pinpoint:<tag>` | `modules/agent-catalog/pinpoint/Dockerfile` | ~30 MiB (multi-file tree) | `modules/agent-catalog/pinpoint/setup.sh` |

모든 build.sh 가 `BUILDER` 환경변수로 docker / podman / nerdctl 선택 가능.
`KIND_NAME` 환경변수 주면 빌드 직후 `kind load docker-image` 도 자동.

### 11.2 배포 manifest 들 (`deploy/manifests/`)

```
00-classcache-crd.yaml         CRD definition
01-namespace-rbac.yaml         classcache-system ns + operator SA / ClusterRole / CRB
02-operator-deployment.yaml    operator Deployment + Service (webhook 9443)
03-mutatingwebhook.yaml        MutatingWebhookConfiguration (label objectSelector)
04-cert-manager.yaml           Issuer (selfsigned) + Certificate (TLS for webhook)
05-profile-catalog.yaml        classcache-profiles ConfigMap (scouter/otel/apm-v01/pinpoint)
```

순서대로 apply:
```bash
kubectl apply -f deploy/manifests/00-classcache-crd.yaml
kubectl apply -f deploy/manifests/01-namespace-rbac.yaml
kubectl apply -f deploy/manifests/05-profile-catalog.yaml
kubectl apply -f deploy/manifests/04-cert-manager.yaml   # cert-manager 가 미리 설치돼 있어야
# cert provisioning 대기
kubectl apply -f deploy/manifests/02-operator-deployment.yaml
kubectl apply -f deploy/manifests/03-mutatingwebhook.yaml
```

`scripts/quickstart.sh` 가 이 순서를 자동화 + kind 클러스터 생성까지 묶음.

### 11.3 Helm chart (`deploy/helm/classcache/`)

```
Chart.yaml
values.yaml          기본값
crds/                CRD (Helm 이 별도 install)
templates/
  rbac.yaml
  deployment.yaml
  webhook.yaml
  cert.yaml          cert-manager Issuer + Certificate
  _helpers.tpl
  NOTES.txt
```

핵심 values:
- `image.repository` / `image.tag` (operator image)
- `webhook.enabled` (default true)
- `webhook.failurePolicy` (default Ignore)
- `tls.mode` (certManager / externalSecret / none)
- `tls.certManager.issuerName` (blank → self-signed Issuer 자동 생성)
- `installCRD` (default true)
- `leaderElect` (default false; replicaCount > 1 이면 true 권장)

### 11.4 Quickstart 흐름

```
./scripts/quickstart.sh
  1. kind create cluster --name cc-quickstart (--config kind-config.yaml)
  2. cert-manager 설치 + wait
  3. operator + primer-universal + agent-scouter 이미지 빌드
  4. demo app (classcache-springboot-scale) 빌드 (없으면)
  5. kind load 4개 이미지
  6. apply manifests 00..05
  7. wait classcache-webhook-tls secret
  8. apply 02 + 03 (operator + webhook)
  9. wait operator rollout
  10. apply examples/quickstart.yaml
  11. wait CR phase=Ready
  12. print final status
```

13~15초만에 Ready 도달 (kind 환경).

---

## 12. 관측 / 안전성 / 한계

### 12.1 관측

- **`classcache stats`** — 한 페이지에 모든 상태
- **`classcache top`** — watch mode
- **`classcache events`** — primer pub/sub stream
- **operator metrics** — `:8080/metrics` (controller-runtime 기본 노출). v0.12
  에서 custom `classcache_archive_build_seconds` 등 추가 예정.
- **K8s 표준** — `kubectl get cc -A -o wide`, `kubectl describe cc <name>`,
  `kubectl logs -n classcache-system deploy/classcache-operator`

### 12.2 보안

| 항목 | 상태 |
|---|---|
| Pod-to-pod HTTP 인증 | ❌ cluster-internal, 평문 (v0.12 mTLS 후보) |
| Archive integrity | ✅ v0.11 sha256 verify on pull |
| Archive PKI signing | ❌ v0.12+ |
| Operator RBAC | ✅ 명시적 ClusterRole, 권한 minimum |
| Primer RBAC | ✅ per-CC Role, 자기 CR 의 status 만 patch |
| Webhook TLS | ✅ cert-manager auto-provision |
| `AllowArchivingWithJavaAgent` | ⚠️ JDK diagnostic flag — production-bless 안 됨 |
| hostPath write | ⚠️ primer 만 write, workload 는 readOnly mount |

### 12.3 한계 (3분류)

#### 설계 본질적 (엔지니어링으로 안 풀림)

1. **JVM 버전 lock** — JDK 21 archive 를 JDK 17 에서 못 씀.
2. **AppCDS 정적 로딩만** — 동적 proxy/lambda/reflection 부분 커버리지.
3. **classlist determinism fragile** — `/proc/cpuinfo` 분기 같은 변수가 같은
   build 에서 다른 archive 만들 수 있음.
4. **hostPath = cache, not state** — 노드 죽으면 archive 도 같이.

#### 현재 구현 갭 (작업 더 하면 풀림)

5. **진짜 multi-host 측정 부재** — k3d (단일 호스트, 별개 컨테이너) 가
   최대 한도.
6. **First-build hot spot** — N=1000 시 1 builder × 999 pullers fan-out.
   v0.11 의 zone-aware peer 가 부분 완화. CDN-style 계층 캐시 미구현.
7. **`AllowArchivingWithJavaAgent` 의존** — diagnostic flag 외엔 대안 없음.
8. **OTel SDK isolated classloader** — Hybrid 모드는 동작하지만 archive
   coverage ~1328 클래스 (Scouter 의 ~4861 대비).
9. **Operator 의 PEER_ZONE auto-population 미구현** — 사용자가 환경변수 직접
   설정해야 zone-aware 활성화.

#### 프로세스 차원

10. **AI 협업으로 빠르게 빌드함** — 측정값은 진짜지만 라인 단위 디버깅 depth
    는 uftrace/JFS 와 같지 않음.
11. **외부 사용자 0명** — quickstart 가 동작하긴 함, 다만 이 repo 밖에서
    누군가 시도해본 적 없음.

### 12.4 알려진 운영 패턴

| 시나리오 | 동작 |
|---|---|
| Valkey 단기 다운 | 새 노드는 peer 발견 못 함 → build. 기존 노드는 영향 없음. |
| Valkey Pod 재시작 (OOM/evict) | (v0.12-A) PVC + AOF 로 데이터 유지, 1초 내 복구 |
| Valkey 노드 영구 손실 | StorageClass 종속 — EBS/PD 같은 net storage 면 다른 노드로 follow |
| Primer Pod crash | DaemonSet 가 재시작. heartbeat 멈춰 build_lock 60초 후 자동 해제. |
| Workload Pod crash | restart → 같은 archive 재사용 (hostPath). |
| 노드 자체 사라짐 | 다른 노드들이 stale peer 시도 → fail → 다음 peer. archive 다시 pull. |
| Image rolling update | 새 (app, agent) → 새 sha256 → 새 archive build. 이전 archive 는 hostPath 에 남음 (GC 정책 v0.12 후보). |
| Operator crash | derived resources 그대로. workload 영향 0. operator 재기동 후 reconcile 재실행. |

---

## 13. 전체 코드 맵

```
cluster-classcache/
├── LICENSE                              Apache 2.0
├── NOTICE                               Third-party attribution
├── CONTRIBUTING.md                      Dev setup + agent-add guide + good first issues
├── README.md                            Marketing + quickstart
├── scripts/
│   └── quickstart.sh                    kind end-to-end one-shot
├── examples/
│   ├── quickstart.yaml                  Demo ClassCache (used by quickstart.sh)
│   └── my-app-template.yaml             User-fillable template (4 REPLACE_ME slots)
│
├── modules/
│   ├── primer/                          Go — per-node archive daemon
│   │   ├── main.go                      env → Config wiring, signal handling
│   │   ├── orchestrator.go              Lifecycle state machine (§5)
│   │   ├── directory.go                 Valkey wrapper
│   │   ├── archive.go                   sha256 / ComputeKey / ArchivePath
│   │   ├── peer.go                      HTTP serve + PullFromPeer (with verify)
│   │   ├── builder.go                   Profile loader + BuildArgs + JVM subprocess
│   │   ├── status_publisher.go          SA-token-based CR status PATCH
│   │   ├── *_test.go                    miniredis-backed unit tests
│   │   ├── Dockerfile                   v0.7 baked-in path (legacy demo)
│   │   ├── Dockerfile.universal         v0.9 — JRE + Go binary only
│   │   └── build*.sh                    BUILDER abstraction
│   │
│   ├── operator/                        Go — controller-runtime
│   │   ├── cmd/main.go                  Manager wiring + webhook server
│   │   ├── api/v1/                      CRD types + hand-written DeepCopy
│   │   ├── controllers/
│   │   │   ├── classcache_controller.go Reconcile entrypoint
│   │   │   ├── valkey.go                Valkey Deployment + Service
│   │   │   ├── primer.go                Primer DaemonSet w/ initContainers
│   │   │   ├── primer_rbac.go           per-CC SA + Role + RoleBinding
│   │   │   ├── workload_patch.go        ApplyPodPatch (shared by webhook)
│   │   │   ├── profile_cm.go            Profile catalog lookup → ConfigMap
│   │   │   └── *_test.go                fake-client tests
│   │   ├── webhook/pod_mutator.go       Admission patch (Webhook mode)
│   │   └── Dockerfile                   distroless/static:nonroot (11.5 MiB)
│   │
│   ├── agent-catalog/
│   │   ├── README.md                    Vendor table + per-agent guide
│   │   ├── build.sh                     Batch build (delegates to setup.sh)
│   │   ├── scouter/                     Single-jar agent
│   │   └── pinpoint/                    Multi-file agent (Pinpoint v3.1, NAVER)
│   │
│   ├── agent-profiles/
│   │   ├── schema/v1.json               JSON Schema for AgentProfile
│   │   └── profiles/                    Reference profiles (scouter, otel, apm-v01, …)
│   │
│   ├── apm-agent-v0.1/                  In-house ByteBuddy agent (regression baseline)
│   │
│   └── cli/                             C — `classcache` CLI
│       ├── Makefile                     macOS + Debian/Ubuntu
│       ├── include/classcache.h         Public structs + prototypes
│       └── src/
│           ├── main.c                   subcommand dispatch (stats/top/archives/peers/events)
│           ├── stats.c                  K8s + Valkey + smaps in one screen
│           ├── top.c                    ANSI-clear + cmd_stats + sleep loop
│           ├── events.c                 SUBSCRIBE primer-events + JSON pretty-print
│           ├── kube.c                   kubectl + cJSON wrappers
│           ├── valkey.c                 hiredis wrappers
│           ├── smaps.c                  /proc/<pid>/smaps parser + docker + kubectl variants
│           └── format.c                 ANSI color + KiB→MB + clear_screen
│
├── deploy/
│   ├── manifests/                       Raw K8s YAMLs (00..05)
│   └── helm/classcache/                 Helm chart (same content parameterized)
│
├── demos/
│   ├── 01-phase-b-cds/                  CDS archive bake-in proof
│   ├── 02-mmap-share/                   smaps page-sharing measurement
│   ├── 03-springboot-scale/             Spring Boot 33 MB archive scale test
│   ├── 04-cluster-primer/               docker-compose 3-node P2P
│   ├── 05-apm-v01/                      In-house APM baseline
│   ├── 06-k8s-end-to-end/               kind multi-node integration (pre-v0.7)
│   ├── 07-scouter-ingestion/            Scouter compat verification
│   ├── 08-otel-ingestion/               OTel hybrid mode
│   └── 09-k3d-multinode/                k3d 4-node cross-bridge P2P
│
└── docs/
    ├── DESIGN.md                        v0.5 design + v0.6..0.10 changes
    ├── REPORT.md                        Hypothesis-by-hypothesis verification
    ├── OVERVIEW.md / .ko.md             One-page TL;DR
    ├── ARCHITECTURE.md (this file)      Full architectural deep dive
    └── BLOG_DRAFT.ko.md                 Korean blog post draft (for author to rewrite)
```

---

## 14. 버전별 변경 누적

| 버전 | 핵심 변경 | commit (anchor) |
|---|---|---|
| v0.5 | PoC. Phase B → mmap → SB → primer (Python) → APM → K8s → Scouter → OTel | (initial commit) |
| v0.6 | Modularization. Python → Go primer. Redis → Valkey. AgentProfile JSON Schema. | initial commit |
| v0.7 | Operator + Webhook + Helm. ClassCache CRD. Owned/Webhook patch modes. | initial commit |
| v0.8-1 | Real archive-key pipeline. primer PATCHes status (no client-go). | initial commit |
| v0.9 | Zero-build UX. extractor initContainers. universal primer image. | initial commit |
| v0.10 | License (Apache 2.0), CONTRIBUTING, NOTICE. Pinpoint catalog. distroless support (`spec.app.extractorImage`). k3d 4-node verification. classcache C CLI. | `3cca080`, `f1607c2`, `01cd8b7`, `6593b3a`, `7020bca`, `d1618e4`, `f839efb`, `99c64e1` |
| v0.11 | Stale build_lock + peer cleanup (heartbeat). kubectl-exec smaps fallback for k3d. SHA256 integrity verification on P2P pull. Zone-aware peer selection (protocol). | `9a3f6c5`, `3874c3d`, `45522db`, `d44cbc2`, `d4fc68a`, `2c0bf95` |
| v0.12-A | Valkey directory survives Pod restart (StatefulSet + PVC 256Mi + AOF `everysec`). `spec.valkey.storageSize` and `storageClassName` knobs. | `7855a24` |

v0.12-B+ 후보 (REPORT 의 known issues + OVERVIEW 의 implementation gaps):
- Operator 가 PEER_ZONE 자동 채움 (nodes/get RBAC + node label lookup)
- Archive PKI signing
- Multi-host EKS/GKE 실측
- OTel SDK split bootstrap (agent fork 또는 extension API)
- Hierarchical / CDN-style 분배
- Workload mTLS
- Old archive GC 정책
- classlist determinism diffoscope 감사
- DESIGN.md / REPORT.md v0.10/v0.11 반영 (현재 v0.9 까지)

---

## 마무리

이 문서는 "**v0.11 시점의 정적 스냅샷**" 이다. 코드는 계속 움직이므로 새
commit 마다 본 문서를 자동 갱신하기는 어렵고, 새 기능이 들어오면 해당
섹션만 patch 하는 게 현실적이다.

질문 / 잘못된 부분 / 추가하고 싶은 디테일이 있으면 GitHub Issue 로
부탁드린다.
