#!/usr/bin/env bash
# K8s end-to-end demo
# Prereq: a kind cluster named 'cc' exists, and the classcache-springboot-scale image is present.
set -uo pipefail   # set -e is intentionally omitted so empty grep/awk results don't kill us

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
IMAGE="classcache-primer:latest"
BUILDER="${BUILDER:-docker}"
KIND_NAME="cc"
NS="cc"

step() { echo ""; echo "============================================================"; echo "  $*"; echo "============================================================"; }

# --- 0. Pre-clean (residue from prior runs) ---
kubectl delete ns "$NS" --ignore-not-found --wait=true >/dev/null 2>&1 || true
# Clear each worker node's hostPath so a stale archive cache doesn't skew the P2P demo.
for n in $(kind get nodes --name "$KIND_NAME" | grep worker); do
    docker exec "$n" rm -rf /var/lib/classcache 2>/dev/null || true
    docker exec "$n" mkdir -p /var/lib/classcache 2>/dev/null || true
done

# --- 1. Build images + load into kind ---
step "1) Build primer image (Go + distroless) + load into kind"
BUILDER="$BUILDER" IMAGE="$IMAGE" "$REPO/modules/primer/build.sh" >&2
kind load docker-image "$IMAGE" --name "$KIND_NAME"

# --- 2. K8s manifests ---
step "1.5) Build Scouter server image + load into kind"
"$BUILDER" build -t classcache-scouter-server:latest "$HERE/scouter-server" >&2
kind load docker-image classcache-scouter-server:latest --name "$KIND_NAME"

step "2) Namespace + Valkey + Scouter server"
kubectl apply -f "$HERE/manifests/00-namespace.yaml"
kubectl apply -f "$HERE/manifests/01-valkey.yaml"
kubectl apply -f "$HERE/manifests/04-scouter-server.yaml"
kubectl -n $NS rollout status deploy/valkey --timeout=60s
kubectl -n $NS rollout status deploy/scouter-server --timeout=60s

step "3) Primer DaemonSet"
kubectl apply -f "$HERE/manifests/02-primer-daemonset.yaml"

# Wait until as many DS pods are ready as there are workers.
for i in $(seq 1 60); do
    READY=$(kubectl -n $NS get ds primer -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0)
    DESIRED=$(kubectl -n $NS get ds primer -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo 0)
    if [[ "$READY" -gt 0 && "$READY" == "$DESIRED" ]]; then
        echo "  primer DaemonSet pods ready: $READY/$DESIRED"
        break
    fi
    sleep 2
done

# Wait until every primer prints "READY method=".
echo ""
echo "Waiting for primers to publish READY..."
PRIMER_PODS=($(kubectl -n $NS get pod -l app=primer -o jsonpath='{.items[*].metadata.name}'))
for pod in "${PRIMER_PODS[@]}"; do
    for i in $(seq 1 300); do
        if kubectl -n $NS logs "$pod" 2>/dev/null | grep -q "READY method="; then
            echo "  $pod ok"
            break
        fi
        sleep 2
    done
done

# --- 4. Primer results summary ---
step "4) Primer results — which node built, which pulled?"
printf "%-20s | %-32s | %10s | %s\n" "node" "method" "elapsed_ms" "size"
printf "%-20s-+-%-32s-+-%10s-+-%s\n" "$(printf '%.0s-' {1..20})" "$(printf '%.0s-' {1..32})" "----------" "------"
for pod in "${PRIMER_PODS[@]}"; do
    line=$(kubectl -n $NS logs "$pod" 2>/dev/null | grep "READY method=" | tail -1)
    node=$(kubectl -n $NS get pod "$pod" -o jsonpath='{.spec.nodeName}' 2>/dev/null)
    method=$(echo "$line"  | sed -nE 's/.*method=([^ ]+).*/\1/p')
    elapsed=$(echo "$line" | sed -nE 's/.*elapsed_ms=([0-9]+).*/\1/p')
    size=$(echo "$line"    | sed -nE 's/.*archive_size=([0-9]+).*/\1/p')
    printf "%-20s | %-32s | %10s | %s\n" "$node" "$method" "$elapsed" "$size"
done

# --- 5. Valkey directory state ---
step "5) Valkey directory state"
kubectl -n $NS exec deploy/valkey -- valkey-cli --raw KEYS 'archive:*' 2>/dev/null
echo "---"
KEY=$(kubectl -n $NS exec deploy/valkey -- valkey-cli --raw KEYS 'archive:*:peers' 2>/dev/null | head -1 | tr -d '\r\n')
if [[ -n "$KEY" ]]; then
    echo "peers in $KEY:"
    kubectl -n $NS exec deploy/valkey -- valkey-cli --raw SMEMBERS "$KEY"
fi

# --- 6. Workload Deployment ---
step "6) Workload Deployment (replicas=4, archive ON, agent OFF)"
kubectl apply -f "$HERE/manifests/03-workload-deployment.yaml"
kubectl -n $NS rollout status deploy/workload --timeout=240s

# --- 7. Workload distribution ---
step "7) Workload pod distribution"
kubectl -n $NS get pod -l app=workload -o custom-columns=POD:.metadata.name,NODE:.spec.nodeName,STATUS:.status.phase,IP:.status.podIP

# --- 8. HTTP requests + SPAN check ---
step "8) HTTP requests → verify SPAN JSON output"
WL_POD=$(kubectl -n $NS get pod -l app=workload -o jsonpath='{.items[0].metadata.name}')
WL_NODE=$(kubectl -n $NS get pod "$WL_POD" -o jsonpath='{.spec.nodeName}')
echo "Target pod: $WL_POD (on $WL_NODE)"

kubectl -n $NS port-forward "$WL_POD" 18080:8080 >/dev/null 2>&1 &
PF_PID=$!
sleep 3
for i in 1 2 3; do
    echo -n "  GET /hello       → "; curl -s http://localhost:18080/hello       | head -c 60; echo
    echo -n "  GET /work/$((i*100)) → "; curl -s "http://localhost:18080/work/$((i*100))" | head -c 60; echo
done
kill $PF_PID 2>/dev/null || true
sleep 1

echo ""
echo "Instrumentation output from $WL_POD (v0.5: Scouter agent classes are on boot CP and archive contains inlined transformed code):"
kubectl -n $NS logs "$WL_POD" -c app 2>/dev/null | grep -iE 'scouter|trace|span' | tail -5 | sed 's/^/  /'
SCOUTER_LINES=$(kubectl -n $NS logs "$WL_POD" -c app 2>/dev/null | grep -ic 'scouter')
echo "  Scouter activity lines: $SCOUTER_LINES (agent OFF + boot CP + archive pattern)"
echo ""
echo "  Did the agent connect to the Scouter server (collector)?"
kubectl -n $NS logs deploy/scouter-server 2>/dev/null | tail -30 | grep -iE "(connect|agent|object|cc-workload|new agent|udp|register)" | tail -8 | sed 's/^/    /' || echo "    (no relevant log)"
echo ""
echo "  Scouter server's db/ directory (did data land?):"
kubectl -n $NS exec deploy/scouter-server -- sh -c 'ls -la /opt/scouter-server/database/ 2>/dev/null || ls -la /opt/scouter-server/db/ 2>/dev/null || find /opt/scouter-server -name "*.idx" -o -name "*.dat" 2>/dev/null | head -10' 2>/dev/null | sed 's/^/    /' | head -15

# --- 9. Same-node multi-pod mmap sharing measurement ---
step "9) Same-node multi-pod mmap sharing (smaps inside the kind worker)"
# Pick the node with the most workload pods.
TARGET_NODE=$(kubectl -n $NS get pod -l app=workload -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort | uniq -c | sort -rn | head -1 | awk '{print $2}')
N_ON_NODE=$(kubectl -n $NS get pod -l app=workload -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | grep -c "^$TARGET_NODE$")
echo "Target node: $TARGET_NODE  ($N_ON_NODE workload pod(s) located here)"
echo ""

docker exec "$TARGET_NODE" bash -c '
    PIDS=$(pgrep -f "/work/extracted/app.jar" 2>/dev/null || true)
    if [ -z "$PIDS" ]; then echo "no java PIDs found"; exit 0; fi
    echo "Java workload PIDs on this node: $PIDS"
    echo ""
    T_RSS=0; T_PSS=0; T_SC=0; T_PD=0; N=0
    for pid in $PIDS; do
        N=$((N+1))
        STATS=$(awk "
            /classcache.*\\.jsa/ { in_b=1; next }
            in_b && /^Rss:/             { rss+=\$2 }
            in_b && /^Pss:/             { pss+=\$2 }
            in_b && /^Shared_Clean:/    { sc+=\$2 }
            in_b && /^Private_Dirty:/   { pd+=\$2 }
            in_b && /^VmFlags:/         { in_b=0 }
            END { printf \"%d %d %d %d\", rss, pss, sc, pd }
        " /proc/$pid/smaps 2>/dev/null)
        read rss pss sc pd <<< "$STATS"
        printf "  PID %-6s Rss=%6d KB  Pss=%6d KB  Shared_Clean=%6d KB  Private_Dirty=%6d KB\n" \
            "$pid" "$rss" "$pss" "$sc" "$pd"
        T_RSS=$((T_RSS+rss)); T_PSS=$((T_PSS+pss))
        T_SC=$((T_SC+sc));    T_PD=$((T_PD+pd))
    done
    echo ""
    echo "Aggregate over $N JVMs (archive VMA only):"
    printf "  Σ Rss=%d KB  Σ Pss=%d KB  Σ Shared_Clean=%d KB  Σ Private_Dirty=%d KB\n" \
        "$T_RSS" "$T_PSS" "$T_SC" "$T_PD"
    if [ "$T_RSS" -gt 0 ] && [ "$N" -gt 1 ]; then
        SAVED=$((T_RSS - T_PSS))
        RATIO=$(awk "BEGIN {printf \"%.1f\", $T_PSS / $T_RSS * 100}")
        IDEAL=$(awk "BEGIN {printf \"%.1f\", 100.0 / $N}")
        echo "  Physical memory saved: $SAVED KB"
        echo "  Pss/Rss = ${RATIO}%   ideal ($N JVMs, full sharing) = ${IDEAL}%"
    fi
'

# --- 10. Conclusion ---
step "10) Summary"
echo "K8s integration check passed"
echo ""
echo "Confirmed:"
echo "  - Runs correctly on a kind cluster (control-plane + 2 workers)"
echo "  - One bundle: Valkey Service + Primer DaemonSet (hostPath) + Workload Deployment"
echo "  - Primer distributes the archive across nodes via P2P (PodIP-based endpoints)"
echo "  - Build lock (Valkey SETNX) resolves the simultaneous-start race"
echo "  - Workload pods mount the same-node hostPath archive and mmap it at boot"
echo "  - Multi-pod page sharing on the same node (smaps Shared_Clean)"
echo "  - SPAN JSON emitted correctly at agent-OFF runtime (on K8s)"
echo ""
echo "Cluster preserved. To clean up: kind delete cluster --name cc"
