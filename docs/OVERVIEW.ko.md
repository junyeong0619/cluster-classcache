# cluster-classcache — 전체 정리 (한국어)

영문 버전: [`OVERVIEW.md`](./OVERVIEW.md)

---

## 1. 한 문장으로 뭐냐

> APM agent 가 변환한 bytecode 를 JVM CDS archive 에 baked-in 시키고, 그 archive 를 Valkey directory + P2P pull 로 노드 간 분배해서, workload pod 들이 **agent 를 안 띄우고** 그 archive 만 mmap 으로 부팅하게 만드는 Kubernetes operator.

---

## 2. 왜 만들었나 (풀려는 문제)

모든 Java pod 가 매번 하는 짓:
- **부팅 5~10초** (cold JIT + class loading)
- APM agent `premain` 매 부팅마다 실행 (Scouter / OTel / Datadog)
- 같은 JAR 를 같은 노드의 N개 JVM 이 각자 들고 있음 → N × RSS

1000개 pod 의 Spring Boot 클러스터에서는 명백한 낭비. CDS archive 는 **한 번만** 빌드하고, P2P 로 분배하고, **OS mmap 으로 페이지 공유** 가능. 그리고 APM 변환 — 보통 runtime 비용 — 을 build 시점에 archive 에 박아두면 공짜로 재사용.

---

## 3. 동작하게 만드는 핵심 메커니즘 3개 (이게 면접의 80%)

| # | 메커니즘 | 가리킬 코드 |
|---|---|---|
| 1 | **CDS archive bake-in** — build 시점에 agent ON 으로 `-XX:ArchiveClassesAtExit`, runtime 에는 agent 없이 `-XX:SharedArchiveFile`. 변환된 bytecode 가 archive 안에 있어서 agent 가 다시 안 돌아도 됨. | `demos/01-phase-b-cds/cds-verify.sh` (최소 증명) |
| 2 | **결정적 sha256 key** — 같은 `(app.jar, agent.jar, JVM, arch, profile)` ⇒ 같은 archive. 이게 클러스터 전체에서 식별자 역할. | `modules/primer/archive.go` (~70줄, `ComputeKey`) |
| 3 | **mmap 페이지 공유** — `-XX:ArchiveRelocationMode=0` 이면 모든 JVM 이 같은 주소에 mmap → 커널이 페이지 공유 (smaps 의 `Shared_Clean`). | `demos/02-mmap-share/scripts/measure-in-container.sh` |

이 셋을 입으로 설명할 수 있으면, 면접에서 받을 수 있는 질문의 80% 는 커버됩니다.

---

## 4. 무엇을 구현했나 (v0.5 → v0.9)

| 버전 | 한 줄 요약 | 핵심 코드 |
|---|---|---|
| v0.5 | PoC. 가설 검증 (Phase B → mmap → Spring Boot 규모 → primer → APM v0.1 → K8s → Scouter → OTel) | `demos/01..08/` |
| v0.6 | 모듈화. Python primer → Go (6 모듈). Redis → Valkey (BSD). AgentProfile JSON Schema. | `modules/primer/`, `modules/agent-profiles/` |
| v0.7 | Kubernetes operator (controller-runtime). `ClassCache` CRD. Owned / Webhook patch mode. Helm chart. | `modules/operator/`, `deploy/helm/classcache/` |
| v0.8-1 | 루프 닫기: primer 가 `status.archiveKey` 를 직접 PATCH (in-cluster SA token, client-go 없이); operator 는 진짜 키가 들어온 후에만 workload patch. | `modules/primer/status_publisher.go` + `controllers/classcache_controller.go` |
| v0.9 | Zero-build UX: 사용자 Dockerfile 0개. Primer Pod 의 initContainer 들이 사용자 app 이미지 + catalog agent 이미지에서 jar 만 추출. Universal primer 이미지 1개. | `controllers/primer.go` (initContainer wiring) + `modules/agent-catalog/` |

**사용자가 결국 작성하는 것**:
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
이게 다. 그리고 평범한 Deployment 한 개.

---

## 5. 실제 측정된 것 vs 아직 검증 안 된 것 (정직 파트)

### 진짜 측정된 것 (kind / macOS arm64, 로그에 실수치 박혀있음)

| 주장 | 출처 |
|---|---|
| pulled archive 로 Spring Boot **579ms** 부팅 | `demos/04-cluster-primer/run-demo.sh` Bonus |
| 첫 노드 build **3.0초** (Go primer), 2.6초 (Python) | `demos/06-k8s-end-to-end/run-k8s-demo.sh` step 4 |
| 후속 노드 pull **80ms** | 같은 곳 |
| 같은 노드 2 JVM `Pss/Rss = 63.5%` (hybrid 모드) | 같은 곳, step 9 |
| 두 데모에서 같은 입력 → 같은 sha256 key `99cdff82d2f81455` | determinism 증거 |
| Operator 가 CR → `Ready` 까지 **11~15초** | `scripts/quickstart.sh` 타임라인 |

### 아직 검증 안 된 것

- **진짜 multi-host 클러스터** (EKS/GKE/bare-metal). 모든 측정은 kind = "Docker 안의 K8s" = 단일 호스트.
- **x86_64**. arm64 (Apple Silicon) 만 검증.
- **운영 부하**. warmup 후 일회성 측정만. 지속 트래픽 측정 없음.
- **Archive signing / supply chain 보안**. `AllowArchivingWithJavaAgent` 는 diagnostic flag — 운영 안전성 없음.
- **OTel hybrid 의 한계**. `REPORT.md` §8.7 + §8.10 — 부분 작동 (archive 활용 ~1328 클래스 vs Scouter 의 ~4861). OTel SDK 가 isolated AgentClassLoader 에 묶여있는 게 근본 이슈. v0.10+ 일감.

---

## 6. 코드 어디에 뭐가 있나

```
modules/
├── primer/                       (~900줄 Go, 13 tests, miniredis-backed)
│   ├── archive.go                sha256(jars||jvm||arch||profile) → 16자 key
│   ├── directory.go              Valkey 클라이언트: peers, build lock, events
│   ├── peer.go                   /archive/{key} HTTP serve + PullFromPeer
│   ├── builder.go                Profile loader + JVM 서브프로세스 (build)
│   ├── orchestrator.go           local-hit → pull → build-lock → build → register
│   ├── main.go                   env → Config 와이어링
│   └── status_publisher.go       CR status PATCH — client-go 없이 ~80줄
├── operator/                     (~1000줄 Go, 12 tests, fake client)
│   ├── api/v1/                   CRD types + 수동 DeepCopy
│   ├── controllers/
│   │   ├── classcache_controller.go     reconcile 진입점
│   │   ├── valkey.go                    Valkey Deployment + Service
│   │   ├── primer.go                    Primer DaemonSet + initContainer 2개
│   │   ├── primer_rbac.go               per-CC SA + Role(status:patch) + RB
│   │   ├── workload_patch.go            initContainer + JVM opts + volume 주입
│   │   └── profile_cm.go                profile name → catalog ConfigMap → per-CC CM
│   └── webhook/pod_mutator.go    Pod CREATE admission patch (Webhook 모드)
├── agent-catalog/scouter/        Scouter 만 — 공식 Docker image 없는 유일한 vendor
└── agent-profiles/               AgentProfile JSON Schema + 레퍼런스 YAML

deploy/
├── manifests/                    K8s 매니페스트 raw YAML (CRD, RBAC, operator, webhook, profile catalog)
└── helm/classcache/              Helm chart (같은 내용 parameterize)

demos/01..08/                     가설별 검증 흐름
```

---

## 7. 면접에서 받을 만한 질문 + 답이 있는 파일

| 질문 | 가리킬 파일 |
|---|---|
| "archive key 어떻게 만드는지 보여줘봐" | `modules/primer/archive.go` — `ComputeKey` 5줄 |
| "workload pod 가 어느 `.jsa` 기다리는지 어떻게 알아?" | `modules/operator/controllers/workload_patch.go` — `waitScript` + `addEnv` |
| "`ArchiveRelocationMode=0` 이 왜 중요해?" | `REPORT.md` §3.2 (그리고 측정된 83% vs 12.6% 차이) |
| "primer 가 client-go 없이 CR status 어떻게 PATCH 해?" | `modules/primer/status_publisher.go` — SA token + ca.crt 직접 읽어서 `PATCH`. ~80줄 |
| "두 primer 가 동시에 시작하면?" | `modules/primer/orchestrator.go` — `acquireRemoteOrBuild` + `waitForPeer` (Valkey SETNX lock + polling) |
| "Scouter 는 catalog 만들고 OTel 은 왜 안 만들어?" | `modules/agent-catalog/README.md` |
| "나쁜 archive 가 들어오는 거 어떻게 막아?" | 솔직한 답: 아직 안 막음. v0.10 archive signing 일감. |

---

## 8. 솔직한 한계 (이게 신뢰의 기반)

1. **AI 협업으로 빠르게 빌드함**. 아키텍처 결정도, 측정도 AI 와 같이 한 것. 이 문서의 숫자는 진짜 (데모 로그에서 나온 실측) 지만, "코드 한 줄 한 줄 직접 디버깅한 깊이" 는 uftrace #1925 / JFS 패치 / valkey #3382 와 같지 않음.
2. **단일 호스트 검증만**. 진짜 cross-host 네트워킹 효과 (peer pull 실 NIC, ARP, MTU, mTLS) 는 측정 안 됨.
3. **`AllowArchivingWithJavaAgent` 는 diagnostic**. JDK 팀이 production 권장 안하는 옵션. 전체 접근법이 "for testing purposes only" 라고 명시된 flag 에 의존.
4. **OTel SDK isolated classloader**. Hybrid 모드는 작동하지만 archive 활용도 작음. 완전 OTel parity 는 v0.10+ 과제.
5. **외부 사용자 0명**. quickstart 가 kind 에서 end-to-end 돌긴 하지만, 이 repo 밖의 사람이 시도해본 적 없음.

---

## 9. 진짜 본인 것으로 만들려면

- 세 파일 라인 단위 정독: `archive.go`, `workload_patch.go`, `status_publisher.go`. 합쳐서 ~400줄.
- 본인 손으로 `./scripts/quickstart.sh` 돌려보고, 숫자 나오는 거 직접 확인, kind worker 안에서 smaps 직접 까보기:
  ```bash
  docker exec cc-worker bash -c 'cat /proc/<pid>/smaps | grep -A 10 jsa'
  ```
- 외워서 말할 수 있어야 하는 3문장:
  1. **CDS bake-in 이 어떻게 작동하는지** (build 때 agent ON, runtime 때 agent OFF — 변환 결과는 archive 에 있음)
  2. **왜 sha256 가 determinism 을 만드는지** (입력 동일 → 출력 동일 → 클러스터 전체에서 같은 key)
  3. **왜 `ArchiveRelocationMode=0` 이 Shared_Clean 을 가능하게 하는지** (모든 JVM 이 같은 가상주소에 mmap → pointer fixup 없음 → COW 안 일어남 → 페이지 공유)

이 셋이 클릭하는 순간, 이건 "AI 가 짜준 repo" 가 아니라 **"AI 도움으로 프로토타이핑한 JVM internals 프로젝트"** 가 됩니다. 면접에서 그 차이가 드러납니다.

---

## TL;DR

| 질문 | 답 |
|---|---|
| 이게 뭐야? | K8s operator + JVM CDS archive 분배 시스템. APM agent overhead 0 + 노드 메모리 공유 + 빠른 부팅. |
| 핵심 메커니즘은? | (1) CDS bake-in, (2) sha256 determinism, (3) mmap 페이지 공유 |
| 진짜 측정됐어? | 6+개 숫자 실측. 단 측정자는 Claude (본인 옆에서) — 본인이 독립적으로 한 적 없음 → 주말 한 번 들여서 직접 돌려보고 정독하는 게 안전. |
| 어디까지 갔어? | v0.10 까지: ClassCache CR + 평범한 Deployment 면 끝. kind 에서 11~15초, k3d 4-node 에서 ~34초에 Ready. distroless 워크로드 지원. Apache 2.0. |
| 안 된 건? | 진짜 multi-host (EKS/GKE), x86_64, prod 부하, archive signing, OTel SDK 분리 부트스트랩, k3d smaps fallback — 다 v0.11+ 과제. |
