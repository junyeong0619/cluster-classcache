# cluster-classcache: AI 와 같이 일주일 동안 만든 JVM CDS Operator

> **초안.** 이 문서는 내가 본인 목소리로 다시 쓰기 위한 출발점입니다.
> 코드는 100% 자기 commit 으로 올라가 있지만 (`github.com/junyeong0619/cluster-classcache`),
> 글은 내가 자기 손으로 한 번 더 다듬는 게 중요합니다. AI 가 만든 초안을 그대로
> 게시하면 "AI가 쓴 글" 로 읽힙니다.
>
> 다듬을 때 권장:
> 1. 모든 측정값을 본인 손으로 한 번 재현한 후 그대로 인용
> 2. "막힌 곳" 섹션의 사례를 본인 기억으로 다시 서술
> 3. "왜" 부분에 본인의 동기 (예: 왜 JVM 쪽을 파고 싶었는지) 한 줄
> 4. "AI 와 어떻게 일했나" 부분은 본인이 직접 — 이게 신뢰의 핵심

---

## 0. 한 줄 요약

JVM 의 CDS archive 를 노드 간 P2P 로 분배하고 APM agent 변환 결과를 그
archive 에 baked-in 시키는 Kubernetes operator 를 일주일 동안 만들었다.
사용자 입장에서는 `ClassCache` CR YAML 한 장 + 평범한 Deployment 면 끝.
Spring Boot 부팅이 5~10 초에서 **0.5 초**로, APM agent runtime overhead 가
0 으로 떨어진다. 같은 노드의 4 JVM 에서 약 30 MB 의 RSS 가 mmap 페이지
공유로 절약된다.

소스: https://github.com/junyeong0619/cluster-classcache

## 1. 왜 이 문제를 풀려고 했나

[여기에 본인의 동기 한 단락. 예시:]

회사에서 Spring Boot 마이크로서비스 100여 개를 K8s 에 띄우고 있었는데,
모든 pod 가 동일한 base 이미지를 쓰면서 부팅 비용 + APM agent premain 비용 +
같은 클래스를 N 번 메모리에 올리는 낭비를 반복하고 있었다. JVM 의 dynamic
CDS archive 가 이 셋을 한 번에 풀 수 있는 메커니즘인 건 알고 있었지만
"클러스터 전체에 그 archive 를 어떻게 분배하고 변환된 APM 코드를 어떻게
같이 굽나" 는 곧장 답이 없는 영역이었다. 일주일 시간이 생겨서 직접 만들어
봤다.

## 2. 핵심 메커니즘 세 가지

길게 풀어쓰지 않고 핵심만:

### (1) CDS archive bake-in

- Build 시점: `-javaagent:scouter.agent.jar -XX:ArchiveClassesAtExit=app.jsa`
  로 JVM 띄우고 warmup HTTP 호출. 종료 시 변환된 bytecode 가 그대로 archive
  파일에 dump.
- Runtime 시점: `-XX:SharedArchiveFile=app.jsa -Xshare:on` 만. `-javaagent`
  옵션 없음. archive 에 baked-in 된 변환 코드가 그냥 동작.

이게 가능한 이유는 archive 가 변환 *후* 의 bytecode 를 들고 있고, JVM 이
class load 때 그걸 mmap 으로 가져오기 때문. agent 가 다시 transformer
콜백을 돌릴 필요가 없다.

조건: 변환이 결정적이어야 함. Scouter ✅, OTel 은 isolated AgentClassLoader
때문에 부분 작동 (자세한 한계는 §6).

### (2) Deterministic sha256 key

`sha256(app.jar || agent.jar || jvm_version || arch || profile_name)[:16]`.

같은 입력 → 같은 archive → 같은 16자 key. 클러스터 전체에서 이 key 가
ClassCache 의 정체성이 된다. 한 cluster 의 두 namespace 가 같은 app+agent
조합이면 같은 key 가 나오므로 archive 를 자연스럽게 공유한다.

이걸 실증한 데이터: kind 와 k3d 양쪽에서 동일한 ClassCache spec 을 적용
하면 `99cdff82d2f81455` 로 같은 key 가 나옴 (입력이 같으니까).

### (3) mmap 페이지 공유

`-XX:ArchiveRelocationMode=0` 으로 모든 JVM 이 archive 를 **같은 가상
주소**에 mmap. 그러면 pointer fixup 이 없어 COW (copy-on-write) 가 일어
나지 않고, 커널 page cache 가 자연스럽게 공유된다. `Shared_Clean` 으로
나타남.

4 JVM 동시 측정:

```
NODE         JVMs   Σ Rss      Σ Pss      Saved
cc-worker    2      60.2 MB    45.0 MB    15.2 MB
cc-worker2   2      60.4 MB    44.9 MB    15.5 MB
TOTAL        4      120.5 MB   89.8 MB    30.7 MB
Σ Shared_Clean = 61.4 MB
```

## 3. 시스템 구조

```
ClassCache CR (사용자 작성, ~7줄 YAML)
    │
    ▼
Operator (controller-runtime)
    ├─ Valkey deploy
    ├─ Primer DaemonSet (한 노드당 1개)
    │   ├─ extract-app initContainer  ← 사용자 app 이미지에서 jar 만 추출
    │   ├─ extract-agent initContainer ← agent 이미지에서 jar 추출
    │   └─ primer main (Go)
    │       - sha256(jars) 계산
    │       - Valkey directory 에서 peer 찾기
    │       - 있으면 HTTP pull, 없으면 build
    │       - status PATCH (in-cluster SA token, no client-go)
    └─ Workload Deployment patch
        - initContainer 가 archive 파일 기다림
        - JVM 옵션 (SharedArchiveFile, ArchiveRelocationMode=0) 주입
        - hostPath 마운트
```

Operator + primer 다 Go (controller-runtime + miniredis 테스트).
CLI (`classcache stats`/`top`/`events`) 는 C (hiredis + cJSON + libcurl,
~1.3k 줄).

[그림 한 장 추가 권장 — README 의 ASCII 다이어그램이 좋음]

## 4. AI 와 어떻게 일했나 (이 부분이 글의 핵심)

[여기를 본인 목소리로 다시 쓰는 게 정말 중요합니다. 다음 질문에 답하는
형태로 다듬으세요:]

- 어떤 부분을 AI 가 generate 했고 어떤 부분을 직접 짰는가
- AI 가 한 번에 못 만든 부분 (디버깅이 필요했던 부분)
- 본인이 measurement / verification 으로 신뢰를 어떻게 쌓았나
- AI 한테 어떤 prompt 가 안 통했고 어떤 prompt 가 통했나

이 부분이 비어있으면 글 전체가 "AI가 쓴 광고" 로 읽힙니다.

[초안 예시 — 본인 경험으로 다시 쓰세요:]

> 코드 generation 자체는 AI 가 빠르게 만들었지만, 그게 진짜로 동작하는지는
> 매 단계마다 내가 직접 kind 클러스터에서 검증해야 했다. 측정값 (Pss/Rss
> = 63.5%, Spring Boot 부팅 579 ms, P2P pull 80 ms) 는 모두 내 노트북에서
> 진짜로 돌려서 나온 숫자고, AI 추정값이 아니다.
>
> AI 가 한 번에 못 푼 사례 둘:
>
> 1. **distroless image 의 jar 추출 문제** (§5 막힌 곳 1)
> 2. **k3d 환경에서의 PID namespace 이슈** (§5 막힌 곳 2)

## 5. 막힌 곳 (실제 디버깅 사례)

[이 섹션이 가장 신뢰를 만드는 부분. 본인 기억으로 다시 적으세요.]

### 막힌 곳 1: mount path 가 사용자 이미지의 jar 를 가려버린 일

v0.9 의 zero-build UX 첫 구현은 cc-extract-app initContainer 의 emptyDir
를 `/work` 에 마운트했다. 그런데 사용자 app 이미지의 jar 가 `/work/app.jar`
에 있는 경우가 흔하고, emptyDir 가 그 디렉토리를 덮어버려서 첫 실행이
`cp: cannot stat '/work/app.jar': No such file or directory` 로 실패했다.

해결: emptyDir 를 `/cc-staging` 에 마운트, 추출 스크립트도 거기에 쓰도록.
primer 컨테이너는 `/work` 에 다시 마운트해서 기존 환경변수 그대로 사용.

배운 점: K8s 의 volume mount 가 디렉토리를 "덮어쓴다" 는 의미를 정확히
이해해야 함. mount path 가 source path 와 같으면 source 가 사라진다.

### 막힌 곳 2: k3d 의 PID namespace

`classcache stats` 가 kind 에서는 잘 동작하는데 k3d 로 같은 클러스터
구조 만들고 돌려보니 MEMORY SHARING 섹션이 0 KB 만 나왔다. 디버깅:

```bash
docker exec k3d-cc-k3d-agent-0 pgrep -f /work/extracted/app.jar
# (no output)
docker exec k3d-cc-k3d-agent-0 ps aux | grep java
# (no output either — but kubectl get pod -o wide says there IS a java
# process in that node)
```

k3d 의 k3s 노드는 host PID namespace 를 공유하지 않는다. kind 는
공유함. 그래서 host 에서 `docker exec` 로 들어가도 다른 컨테이너의
PID 가 안 보인다.

해결: smaps_read_pod 추가. `kubectl exec -c app -- cat /proc/1/smaps` 로
pod 내부에서 직접 읽음. PID 1 이 workload JVM 인 건 K8s pod 의 convention.

### 막힌 곳 3: stale build_lock 이 cluster 를 stuck 시킨 일

v0.9 첫 demo run 도중, 어떤 이유로 primer 가 build 중 죽었다. 그 다음
시작한 primer 들은 모두 `directory has 0 peer(s)` → SETNX 실패 →
`another node is building — polling for peer...` 무한 대기. valkey 의
build_lock 에 죽은 primer 의 endpoint 가 남아 있어서.

수동 복구: `valkey-cli DEL archive:<key>:build_lock`. 정상 동작 재개.

영구 fix (v0.11 commit `9a3f6c5`):
- build_lock TTL 을 10분에서 60초로 단축
- build 진행 중인 primer 가 20초마다 lock TTL renew (goroutine)
- build 완료/실패 시 lock 명시적 release (성공/실패 둘 다)
- Lua atomic GET-then-PEXPIRE 로 race 방지

배운 점: distributed lock 은 "잡고 끝" 이 아니라 "잡고 → 살아있다고 알리고
→ 명시적으로 놓는" 세 단계가 다 필요하다. 그리고 짧은 TTL + heartbeat 가
긴 TTL 보다 안전하다.

## 6. 측정 결과 + 솔직한 한계

### 측정된 것 (모두 내가 kind/k3d 에서 직접 돌린 결과)

| 측정 | 값 |
|---|---|
| pulled archive 로 Spring Boot 부팅 | 579 ms |
| 첫 노드 build (Go primer) | 3.0 s |
| 후속 노드 pull | 80 ms |
| 같은 노드 2 JVM Pss/Rss (hybrid 모드) | 63.5% |
| 4 JVM 총 절약 (Σ Rss − Σ Pss) | 30.7 MB |
| operator CR → Ready (kind) | 11~15 s |
| operator CR → Ready (k3d 4-node) | ~34 s |

### 한계 (이게 신뢰의 기반)

설계 본질적 한계 — 엔지니어링으로 안 사라지는 것:
1. CDS archive 는 JVM 버전에 묶임. JDK 21 archive 를 JDK 17 에서 못 씀.
2. AppCDS 는 정적 class loading 만 잡음. 동적 proxy/lambda/reflection 은
   부분 커버리지.
3. classlist determinism 이 fragile. `/proc/cpuinfo` 분기 같은 변수에 따라
   같은 build 가 다른 archive 를 만들 수 있음.
4. hostPath = cache, not state. 노드 죽으면 archive 도 같이 죽음.

현재 구현 갭 — 더 작업하면 풀 수 있는 것:
5. 진짜 multi-host (EKS/GKE) 측정 부재.
6. First-build hot spot (N=1000 에서 1 builder × 999 pullers).
   - 이번에 zone-aware peer selection 으로 protocol 절반은 만들어둠
     (commit `d44cbc2`).
7. `AllowArchivingWithJavaAgent` 는 JDK 의 diagnostic flag — production
   blessed 아님.
8. OTel SDK 의 isolated AgentClassLoader. Hybrid 모드는 작동하지만
   archive 활용도 작음 (~1328 클래스 vs Scouter 의 ~4861).

## 7. 다음에 풀고 싶은 것

- OTel SDK split bootstrap (agent fork 또는 extension API)
- Archive PKI signing (현재는 sha256 integrity check 만)
- 진짜 EKS/GKE 멀티 노드 측정
- classlist determinism 의 diffoscope 감사
- Operator 가 PEER_ZONE 자동 채움 (nodes/get RBAC + 노드 라벨 lookup)

## 마무리

[본인이 직접 한 문장 — 예: "이 프로젝트가 production-ready 한가" 라는
질문에 솔직하게 답. 또는 "다음 회사 면접에서 어떤 부분을 말할 것인가".]

---

## 참고

- 소스: https://github.com/junyeong0619/cluster-classcache
- 한 페이지 개요: `docs/OVERVIEW.ko.md`
- 설계 디테일: `docs/DESIGN.md`
- 단계별 검증 보고서: `docs/REPORT.md`
- 라이브 도구 (C): `modules/cli/`

[관련 background — 본인 다른 자산 링크 추가 권장]
- uftrace #1925 (시스템 트레이싱)
- JFS 패치 (커널 파일시스템)
- valkey #3382 (in-memory store)
- VectorWave (OTel 통합)
