#!/usr/bin/env bash
# Demo: 3 nodes start one after another.
#   node-a: build (slow)
#   node-b: pull from node-a (fast)
#   node-c: pull from node-a or node-b (fast)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE"

cleanup() {
    docker compose down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

step() { echo ""; echo "============================================================"; echo "  $*"; echo "============================================================"; }

# Clean any prior state.
docker compose down -v --remove-orphans 2>/dev/null || true

step "0) docker build (primer image)"
docker build -t classcache-primer . >&2

step "1) Start Valkey"
docker compose up -d redis
sleep 2

wait_for_ready() {
    local svc="$1"
    local timeout="${2:-120}"
    local t0=$SECONDS
    while (( SECONDS - t0 < timeout )); do
        line=$(docker logs "cc-$svc" 2>&1 | grep -m1 "READY method=" || true)
        if [[ -n "$line" ]]; then
            echo "$svc: $line"
            return 0
        fi
        # Did the container die?
        if ! docker inspect -f '{{.State.Running}}' "cc-$svc" 2>/dev/null | grep -q true; then
            echo "FAIL: $svc container died"
            docker logs "cc-$svc" 2>&1 | tail -30
            return 1
        fi
        sleep 1
    done
    echo "FAIL: $svc did not reach READY within ${timeout}s"
    docker logs "cc-$svc" 2>&1 | tail -30
    return 1
}

step "2) Start Node A (no archive → must build it itself)"
docker compose up -d node-a
wait_for_ready node-a 180

step "3) Start Node B (should pull from Node A)"
docker compose up -d node-b
wait_for_ready node-b 60

step "4) Start Node C (pulls from A or B)"
docker compose up -d node-c
wait_for_ready node-c 60

step "5) Valkey directory state"
docker exec cc-valkey valkey-cli --raw KEYS 'archive:*'
echo "---"
key=$(docker exec cc-valkey valkey-cli --raw KEYS 'archive:*:peers' | head -1)
if [[ -n "$key" ]]; then
    echo "Peers in $key:"
    docker exec cc-valkey valkey-cli --raw SMEMBERS "$key"
fi

step "6) Summary"
echo "node     | method                          | elapsed_ms | archive_size"
echo "---------|---------------------------------|-----------:|-------------:"
for svc in node-a node-b node-c; do
    line=$(docker logs "cc-$svc" 2>&1 | grep "READY method=" | tail -1)
    method=$(echo "$line"  | sed -nE 's/.*method=([^ ]+).*/\1/p')
    elapsed=$(echo "$line" | sed -nE 's/.*elapsed_ms=([0-9]+).*/\1/p')
    size=$(echo "$line"    | sed -nE 's/.*archive_size=([0-9]+).*/\1/p')
    printf "%-8s | %-31s | %10s | %12s\n" "$svc" "$method" "$elapsed" "$size"
done

step "7) Bonus: does the pulled archive actually work in a JVM?"
echo "Measuring Spring Boot startup with the pulled archive on Node B ..."
docker exec cc-node-b bash -c '
    key=$(ls /var/lib/classcache/*.jsa | head -1)
    t0=$(date +%s%3N)
    java -XX:+UnlockDiagnosticVMOptions -XX:+AllowArchivingWithJavaAgent \
         -XX:SharedArchiveFile="$key" -XX:ArchiveRelocationMode=0 -Xshare:on \
         -jar /work/extracted/app.jar --server.port=0 > /tmp/sb.log 2>&1 &
    pid=$!
    for i in $(seq 1 600); do
        if grep -q "Started App in" /tmp/sb.log 2>/dev/null; then
            t1=$(date +%s%3N)
            echo "  Spring Boot ready in $((t1-t0)) ms (pulled archive works)"
            kill -TERM $pid 2>/dev/null
            wait $pid 2>/dev/null
            exit 0
        fi
        if ! kill -0 $pid 2>/dev/null; then
            echo "FAIL: JVM died"
            tail -20 /tmp/sb.log
            exit 1
        fi
        sleep 0.05
    done
    echo "FAIL: did not start within 30s"
    kill -9 $pid 2>/dev/null
    exit 1
'

echo ""
echo "Done."
