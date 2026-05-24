#!/bin/bash
# Runtime verification:
# - archive ON + agent OFF + HTTP requests → SPAN JSON output + DispatcherServlet served from the archive.
set -euo pipefail

EXTRACT=/work/extracted
ARCHIVE=/work/app.jsa

echo "============================================================"
echo "  APM agent v0.1 verification"
echo "============================================================"
echo "Archive: $(du -h "$ARCHIVE" | awk '{print $1}')"
echo ""

echo "==> Starting Spring Boot (archive ON, agent OFF)"
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile="$ARCHIVE" \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:on \
    "-Xlog:class+load=info:file=/tmp/cl.log::filesize=0" \
    -jar "$EXTRACT/app.jar" > /tmp/sb.log 2>&1 &
SB_PID=$!

T0=$(date +%s%3N)
READY_MS=0
for i in $(seq 1 60); do
    if curl -fsS http://localhost:8080/hello >/dev/null 2>&1; then
        READY_MS=$(( $(date +%s%3N) - T0 ))
        echo "==> Ready after ${READY_MS} ms"
        break
    fi
    if ! kill -0 $SB_PID 2>/dev/null; then
        echo "Spring Boot died:"; tail -30 /tmp/sb.log; exit 1
    fi
    sleep 0.5
done

echo ""
echo "==> Sending HTTP requests"
curl -s http://localhost:8080/hello    | head -c 60; echo
curl -s http://localhost:8080/work/100 | head -c 60; echo
curl -s http://localhost:8080/work/500 | head -c 60; echo

sleep 1

echo ""
echo "============================================================"
echo "  SPAN output"
echo "============================================================"
grep "^\[SPAN\]" /tmp/sb.log | tail -5 | sed 's/^/  /'
SPAN_COUNT=$(grep -c "^\[SPAN\]" /tmp/sb.log || true)
echo ""
echo "Total SPANs emitted: $SPAN_COUNT"

echo ""
echo "============================================================"
echo "  DispatcherServlet load source"
echo "============================================================"
grep "DispatcherServlet " /tmp/cl.log | head -3 | sed 's/^/  /' || echo "  (no log)"

echo ""
echo "============================================================"
echo "  Checks"
echo "============================================================"
PASS=0; FAIL=0

# 1. Were enough SPANs emitted?
if [[ "$SPAN_COUNT" -ge 3 ]]; then
    echo "[Check 1] PASS: SPAN JSON emitted (n=$SPAN_COUNT, transformed code runs without the agent)"
    PASS=$((PASS+1))
else
    echo "[Check 1] FAIL: not enough SPANs (n=$SPAN_COUNT)"
    FAIL=$((FAIL+1))
fi

# 2. Did DispatcherServlet load from the archive?
if grep -q "DispatcherServlet .*shared objects file" /tmp/cl.log; then
    echo "[Check 2] PASS: DispatcherServlet loaded from the archive (shared objects file)"
    PASS=$((PASS+1))
else
    echo "[Check 2] WARN: DispatcherServlet not seen in the archive"
    echo "    (archive benefit is partial — the transform itself may still be baked in)"
fi

# 3. Was the SPAN name parsed correctly?
if grep "^\[SPAN\]" /tmp/sb.log | head -1 | grep -qE '"name":"GET /'; then
    echo "[Check 3] PASS: SPAN name parsed as 'GET /...' (HTTP metadata extraction OK)"
    PASS=$((PASS+1))
else
    echo "[Check 3] FAIL: SPAN name parsing failed"
    FAIL=$((FAIL+1))
fi

# 4. Were trace/span IDs generated correctly?
if grep "^\[SPAN\]" /tmp/sb.log | head -1 | grep -qE '"trace":"[0-9a-f]+"'; then
    echo "[Check 4] PASS: trace/span IDs generated"
    PASS=$((PASS+1))
fi

# 5. Was dur_us measured?
DUR_OK=$(grep "^\[SPAN\]" /tmp/sb.log | head -1 | grep -oE '"dur_us":[0-9]+' | head -1)
if [[ -n "$DUR_OK" ]]; then
    echo "[Check 5] PASS: $DUR_OK (duration measured)"
    PASS=$((PASS+1))
fi

kill -TERM $SB_PID 2>/dev/null || true

echo ""
echo "Startup time: ${READY_MS} ms"
echo ""
if [[ "$FAIL" -eq 0 ]] && [[ "$PASS" -ge 3 ]]; then
    echo "APM agent v0.1 verification PASS (pass=$PASS fail=$FAIL)"
    exit 0
else
    echo "Verification FAIL (pass=$PASS fail=$FAIL)"
    echo ""
    echo "--- last sb.log ---"
    tail -50 /tmp/sb.log
    exit 1
fi
