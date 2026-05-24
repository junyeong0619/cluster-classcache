#!/bin/bash
# Runs at image build time.
# Spring Boot + ApmAgent + warmup HTTP calls → produces app.jsa archive.
set -euo pipefail

EXTRACT=/work/extracted
ARCHIVE=/work/app.jsa
AGENT=/work/agent.jar

echo "==> Starting Spring Boot with ApmAgent + ArchiveClassesAtExit"
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit="$ARCHIVE" \
    -javaagent:"$AGENT" \
    -jar "$EXTRACT/app.jar" > /tmp/build.log 2>&1 &
SB_PID=$!

for i in $(seq 1 60); do
    if curl -fsS http://localhost:8080/hello >/dev/null 2>&1; then
        echo "==> Spring Boot ready after ${i}s"
        break
    fi
    if ! kill -0 $SB_PID 2>/dev/null; then
        echo "Spring Boot died. Log:"
        tail -30 /tmp/build.log
        exit 1
    fi
    sleep 1
done

echo "==> Warmup (triggers DispatcherServlet.doDispatch)"
curl -s http://localhost:8080/hello   | head -c 60; echo
curl -s http://localhost:8080/work/50 | head -c 60; echo

# SPAN should print at build time too (agent has transformed DispatcherServlet).
echo ""
echo "==> SPAN output (build phase, agent ON):"
grep "^\[SPAN\]" /tmp/build.log | tail -3 || echo "  (none — transform may not have applied)"

echo ""
kill -TERM $SB_PID
wait $SB_PID 2>/dev/null || true

if [[ ! -f "$ARCHIVE" ]]; then
    echo "Archive was not produced"
    tail -30 /tmp/build.log
    exit 1
fi

ls -la "$ARCHIVE"
echo "Archive built ($(du -h "$ARCHIVE" | awk '{print $1}'))"
