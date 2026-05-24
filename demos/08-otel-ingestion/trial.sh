#!/bin/bash
# OTel Java agent ingestion test (extending v0.4's generality claim).
# Difference from Scouter: OTel uses an isolated agent classloader + dynamic
# helper injection — riskier. Core hypothesis: is it archive-compatible?
set -uo pipefail

EXTRACT=/work/extracted/app.jar
ARCHIVE=/work/otel.jsa
AGENT=/opt/otel/opentelemetry-javaagent.jar

# OTel runtime env — emit traces to stdout (drops the collector dependency).
export OTEL_TRACES_EXPORTER=logging
export OTEL_METRICS_EXPORTER=none
export OTEL_LOGS_EXPORTER=none
export OTEL_SERVICE_NAME=cc-otel-trial
export OTEL_INSTRUMENTATION_RUNTIME_TELEMETRY_ENABLED=false

step() { echo ""; echo "============================================================"; echo "  $*"; echo "============================================================"; }

wait_hello() {
    local pid=$1 label=$2
    for i in $(seq 1 60); do
        if curl -fsS http://localhost:8080/hello >/dev/null 2>&1; then
            echo "  [$label] Spring Boot ready after ${i}s"; return 0
        fi
        if ! kill -0 $pid 2>/dev/null; then
            echo "  FAIL [$label]: JVM died"; return 1
        fi
        sleep 1
    done
    echo "  FAIL [$label]: 60s timeout"; return 1
}

stop_sb() { kill -TERM $1 2>/dev/null || true; wait $1 2>/dev/null || true; }

# ===================================================================
# O.1 — OTel agent + Spring Boot sanity check
# ===================================================================
step "O.1 — OTel agent ON, no archive (sanity check)"
rm -f /tmp/o1.log
java \
    -javaagent:$AGENT \
    -jar $EXTRACT > /tmp/o1.log 2>&1 &
PID=$!
if wait_hello $PID "O.1"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    sleep 2
fi
stop_sb $PID

echo ""
echo "--- O.1 results ---"
grep -E "(OpenTelemetry|otel)" /tmp/o1.log | head -3 | sed 's/^/  /'
echo "  trace output (LoggingSpanExporter):"
grep -E "(trace_id=|span_id=|ScopeSpans|GET /hello)" /tmp/o1.log | head -3 | sed 's/^/  /' || echo "  (none)"
SPAN_HITS=$(grep -cE "trace_id=|GET /hello" /tmp/o1.log || true)
echo "  trace lines: $SPAN_HITS"

# ===================================================================
# O.2 — OTel agent ON + archive build
# ===================================================================
step "O.2 — OTel agent ON + -XX:ArchiveClassesAtExit"
rm -f $ARCHIVE /tmp/o2.log /tmp/o2-cds.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit=$ARCHIVE \
    "-Xlog:cds=info,cds+dynamic=info:file=/tmp/o2-cds.log::filesize=0" \
    -javaagent:$AGENT \
    -jar $EXTRACT > /tmp/o2.log 2>&1 &
PID=$!
if wait_hello $PID "O.2"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    curl -s http://localhost:8080/work/50  | head -c 60; echo
    sleep 1
fi
stop_sb $PID

echo ""
echo "--- O.2 results ---"
if [[ -f "$ARCHIVE" ]]; then
    echo "  Archive built: $(du -h $ARCHIVE | awk '{print $1}')"
else
    echo "  FAIL: archive not produced"
fi
grep -E "Number of classes|instance classes|app +=|unregistered|hidden|Skipping.*opentelemetry" /tmp/o2-cds.log | head -10 | sed 's/^/  /'

# ===================================================================
# O.3a — archive ON + agent OFF + no bootclasspath (naive)
# ===================================================================
step "O.3a — archive ON, agent OFF, no bootclasspath"
rm -f /tmp/o3a.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    "-Xlog:class+load=info:file=/tmp/o3a-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/o3a.log 2>&1 &
PID=$!
O3A_OK=0
if wait_hello $PID "O.3a"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    sleep 1
    O3A_OK=1
fi
stop_sb $PID

echo ""
echo "--- O.3a results ---"
if [[ $O3A_OK -eq 1 ]]; then
    echo "  JVM booted normally"
    REQ_ERR=$(grep -cE "ClassNotFoundException|NoClassDefFoundError" /tmp/o3a.log || true)
    echo "  ClassNotFoundException occurrences: $REQ_ERR"
    grep -E "ClassNotFoundException|NoClassDef" /tmp/o3a.log | head -3 | sed 's/^/    /'
else
    echo "  FAIL: JVM died"
    grep -iE "error|exception|NoClassDef" /tmp/o3a.log | head -5 | sed 's/^/    /'
fi
grep "DispatcherServlet " /tmp/o3a-cl.log | head -1 | sed 's/^/  /'

# ===================================================================
# O.3b — archive ON + agent OFF + bootclasspath ON
# ===================================================================
step "O.3b — archive ON, agent OFF, -Xbootclasspath/a:opentelemetry-javaagent.jar"
rm -f /tmp/o3b.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    -Xbootclasspath/a:$AGENT \
    "-Xlog:class+load=info:file=/tmp/o3b-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/o3b.log 2>&1 &
PID=$!
O3B_OK=0
if wait_hello $PID "O.3b"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    sleep 1
    O3B_OK=1
fi
stop_sb $PID

echo ""
echo "--- O.3b results ---"
if [[ $O3B_OK -eq 1 ]]; then
    SHARED=$(grep -c "shared objects file" /tmp/o3b-cl.log || true)
    REQ_ERR=$(grep -cE "ClassNotFoundException|NoClassDefFoundError" /tmp/o3b.log || true)
    echo "  JVM booted, 'shared objects file' class loads = $SHARED"
    echo "  ClassNotFoundException/NoClassDefFoundError = $REQ_ERR"
    if [[ $REQ_ERR -gt 0 ]]; then
        grep -E "ClassNotFoundException|NoClassDef" /tmp/o3b.log | head -3 | sed 's/^/    /'
    fi
    echo "  trace output:"
    grep -cE "trace_id=|GET /hello" /tmp/o3b.log | awk '{print "    OTel trace lines: "$1}'
    grep -E "trace_id=|name=GET" /tmp/o3b.log | head -2 | sed 's/^/    /' || true
else
    echo "  FAIL: JVM died"
    grep -iE "error|exception|NoClassDef" /tmp/o3b.log | head -5 | sed 's/^/    /'
fi

# ===================================================================
# O.3c (bonus) — archive ON + agent ON (full)
# ===================================================================
step "O.3c (bonus) — archive ON + agent ON (mirrors Phase 2b)"
rm -f /tmp/o3c.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    -javaagent:$AGENT \
    "-Xlog:class+load=info:file=/tmp/o3c-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/o3c.log 2>&1 &
PID=$!
O3C_OK=0
if wait_hello $PID "O.3c"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    sleep 1
    O3C_OK=1
fi
stop_sb $PID

echo ""
echo "--- O.3c results ---"
if [[ $O3C_OK -eq 1 ]]; then
    SHARED=$(grep -c "shared objects file" /tmp/o3c-cl.log || true)
    echo "  JVM booted (agent ON), 'shared objects file' class loads = $SHARED"
    grep "DispatcherServlet " /tmp/o3c-cl.log | head -1 | sed 's/^/  /'
fi

# ===================================================================
# O.3d (Option B candidate) — agent ON to init SDK + disable all instrumentation.
# Hypothesis: premain initializes the SDK (exporter, processor) while every
#             instrumentation is disabled → transformer callbacks don't run
#             → the archived transformed code is used as-is + the SDK is alive
#             → traces still export.
# ===================================================================
step "O.3d (Option B) — agent ON + all instrumentation disabled + archive"
rm -f /tmp/o3d.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    -javaagent:$AGENT \
    -Dotel.instrumentation.common.default-enabled=false \
    "-Xlog:class+load=info:file=/tmp/o3d-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/o3d.log 2>&1 &
PID=$!
O3D_OK=0
if wait_hello $PID "O.3d"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    sleep 1
    O3D_OK=1
fi
stop_sb $PID

echo ""
echo "--- O.3d results ---"
if [[ $O3D_OK -eq 1 ]]; then
    SHARED=$(grep -c "shared objects file" /tmp/o3d-cl.log || true)
    DISP=$(grep "DispatcherServlet " /tmp/o3d-cl.log | head -1)
    DISP_FROM_ARCHIVE=$(echo "$DISP" | grep -c "shared objects" || true)
    TRACES=$(grep -cE "trace_id=|GET /hello" /tmp/o3d.log || true)
    echo "  JVM booted, 'shared objects file' class loads = $SHARED"
    echo "  DispatcherServlet used archive = $([ $DISP_FROM_ARCHIVE -gt 0 ] && echo YES || echo NO)"
    echo "  $DISP" | sed 's/^/    /'
    echo "  OTel trace lines emitted: $TRACES"
    grep -E "trace_id=|GET /hello" /tmp/o3d.log | head -2 | sed 's/^/    /' || echo "    (none)"
else
    grep -iE "error|exception" /tmp/o3d.log | head -3 | sed 's/^/    /'
fi

# ===================================================================
# O.3e — Same hybrid pattern Scouter used: agent ON + boot CP + archive.
#         (User insight: if Scouter hybrid worked in §8.9, try it for OTel too.)
# ===================================================================
step "O.3e (hybrid) — -javaagent + -Xbootclasspath/a: + archive — full features + partial archive"
rm -f /tmp/o3e.log /tmp/o3e-cl.log
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    -javaagent:$AGENT \
    -Xbootclasspath/a:$AGENT \
    "-Xlog:class+load=info:file=/tmp/o3e-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/o3e.log 2>&1 &
PID=$!
O3E_OK=0
if wait_hello $PID "O.3e"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    curl -s http://localhost:8080/work/200 | head -c 60; echo
    sleep 1
    O3E_OK=1
fi
stop_sb $PID

echo ""
echo "--- O.3e results ---"
if [[ $O3E_OK -eq 1 ]]; then
    SHARED=$(grep -c "shared objects file" /tmp/o3e-cl.log || true)
    TRACES=$(grep -cE "trace_id=|GET /hello" /tmp/o3e.log || true)
    DISP=$(grep "DispatcherServlet " /tmp/o3e-cl.log | head -1)
    DISP_ARCHIVE=$(echo "$DISP" | grep -c "shared objects" || true)
    echo "  JVM booted normally"
    echo "  'shared objects file' class loads = $SHARED  (should be in Scouter B's ~4861 ballpark)"
    echo "  DispatcherServlet used archive = $([ $DISP_ARCHIVE -gt 0 ] && echo YES || echo NO)"
    echo "  $DISP" | sed 's/^/    /'
    echo "  OTel trace (SPAN) lines emitted: $TRACES"
    grep -E "trace_id=|GET /hello" /tmp/o3e.log | head -2 | sed 's/^/    /' || true
fi

# ===================================================================
# Wrap-up
# ===================================================================
step "Wrap-up"
O1_OK=$([[ "$SPAN_HITS" -gt 0 ]] && echo "PASS" || echo "FAIL")
O2_OK=$([[ -f $ARCHIVE ]] && echo "PASS" || echo "FAIL")
O3A_STATUS=$([[ $O3A_OK -eq 1 ]] && echo "boots-but-trace?" || echo "fails (expected)")
O3B_STATUS=$([[ $O3B_OK -eq 1 ]] && echo "PASS" || echo "FAIL")
O3C_STATUS=$([[ $O3C_OK -eq 1 ]] && echo "PASS" || echo "FAIL")

echo "O.1  (OTel + Spring Boot sanity)                     : $O1_OK"
echo "O.2  (OTel agent ON, archive built)                  : $O2_OK"
echo "O.3a (archive ON, agent OFF, no boot cp)             : $O3A_STATUS"
echo "O.3b (archive ON, agent OFF, with boot cp)           : $O3B_STATUS"
echo "O.3c (archive ON, agent ON)                          : $O3C_STATUS"
O3D_STATUS=$([[ $O3D_OK -eq 1 ]] && echo "PASS" || echo "FAIL")
echo "O.3d (agent ON + instrumentation disabled + archive) : $O3D_STATUS"

echo ""
echo "=== Conclusion ==="
if [[ $O3D_OK -eq 1 ]] && [[ "${TRACES:-0}" -gt 0 ]] && [[ "${DISP_FROM_ARCHIVE:-0}" -gt 0 ]]; then
    echo "(Option B) OTel SDK split-bootstrap succeeded!"
    echo "   - agent ON to initialize the SDK"
    echo "   - instrumentation disabled so the transformer doesn't fire → archive isn't bypassed"
    echo "   - archived transformed code + live SDK = traces export correctly"
    echo "   → OTel is viable as the v0.5 default"
elif [[ "$O3B_STATUS" == "PASS" ]]; then
    echo "O.3b PASS but no traces — transformed code is archive-compatible but the SDK never initialized"
fi
