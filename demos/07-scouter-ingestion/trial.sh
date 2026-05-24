#!/bin/bash
# Scouter ingestion test — Phase X.1 / X.2 / X.3a / X.3b.
# Does the v0.4 archive model also work for a third-party APM (Scouter)?
set -uo pipefail

EXTRACT=/work/extracted/app.jar
ARCHIVE=/work/scouter.jsa
AGENT=$SCOUTER_AGENT
CONF=$SCOUTER_CONF

step() { echo ""; echo "============================================================"; echo "  $*"; echo "============================================================"; }

waitForHello() {
    local pid=$1
    local label=$2
    for i in $(seq 1 60); do
        if curl -fsS http://localhost:8080/hello >/dev/null 2>&1; then
            echo "  [$label] Spring Boot ready after ${i}s"
            return 0
        fi
        if ! kill -0 $pid 2>/dev/null; then
            echo "  FAIL [$label]: JVM died"
            return 1
        fi
        sleep 1
    done
    echo "  FAIL [$label]: did not start within 60s"
    return 1
}

stopSpring() {
    local pid=$1
    kill -TERM $pid 2>/dev/null || true
    wait $pid 2>/dev/null || true
}

# ===================================================================
# Phase X.1 — Scouter agent ON, no archive. Verify normal operation + transforms applied.
# ===================================================================
step "Phase X.1 — Scouter agent ON, no archive (sanity check)"
rm -f /tmp/x1.log

java \
    -javaagent:$AGENT \
    -Dscouter.config=$CONF \
    -jar $EXTRACT > /tmp/x1.log 2>&1 &
PID=$!

if waitForHello $PID "X.1"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    sleep 2
fi
stopSpring $PID

echo ""
echo "--- X.1 results ---"
SCOUTER_LINES=$(grep -ciE 'scouter' /tmp/x1.log || true)
echo "  scouter-related stdout lines: $SCOUTER_LINES"
grep -iE 'scouter' /tmp/x1.log | head -3 | sed 's/^/    /'
echo "  Spring Boot 'Started App' log:"
grep "Started App in" /tmp/x1.log | sed 's/^/    /' || echo "    (none)"

# ===================================================================
# Phase X.2 — Scouter agent ON + try to build the archive
# ===================================================================
step "Phase X.2 — Scouter agent ON + -XX:ArchiveClassesAtExit"
rm -f $ARCHIVE /tmp/x2.log

java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit=$ARCHIVE \
    "-Xlog:cds=info,cds+dynamic=info:file=/tmp/x2-cds.log::filesize=0" \
    -javaagent:$AGENT \
    -Dscouter.config=$CONF \
    -jar $EXTRACT > /tmp/x2.log 2>&1 &
PID=$!

if waitForHello $PID "X.2"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    curl -s http://localhost:8080/work/50  | head -c 60; echo
    sleep 1
fi
stopSpring $PID

echo ""
echo "--- X.2 results ---"
if [[ -f "$ARCHIVE" ]]; then
    echo "  Archive built: $(du -h $ARCHIVE | awk '{print $1}')"
else
    echo "  FAIL: archive not produced"
fi
echo "  CDS stats:"
grep -E "Number of classes|instance classes|app +=|unregistered" /tmp/x2-cds.log | head -6 | sed 's/^/    /'
echo "  Were scouter classes skipped from the archive?"
grep -cE "Skipping scouter\." /tmp/x2-cds.log | awk '{print "    Skipping scouter.* lines: "$1}'
echo "  scouter classes that did enter the archive:"
grep -E "scouter/" /tmp/x2-cds.log | grep -v "Skipping" | head -3 | sed 's/^/    /' || echo "    (none)"

# ===================================================================
# Phase X.3a — archive ON + agent OFF + no scouter jar on classpath (naive)
# ===================================================================
step "Phase X.3a — archive ON, agent OFF, scouter jar not on classpath (naive)"
rm -f /tmp/x3a.log

java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    "-Xlog:class+load=info:file=/tmp/x3a-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/x3a.log 2>&1 &
PID=$!

X3A_OK=0
if waitForHello $PID "X.3a"; then
    curl -s http://localhost:8080/hello   | head -c 60; echo
    sleep 1
    X3A_OK=1
fi
stopSpring $PID

echo ""
echo "--- X.3a results ---"
if [[ $X3A_OK -eq 1 ]]; then
    echo "  JVM booted (unexpected — works without scouter classes?)"
else
    echo "  FAIL: JVM died / failed to start (expected)"
    echo "  Last stderr:"
    grep -iE "error|exception|NoClassDef" /tmp/x3a.log | head -5 | sed 's/^/    /'
fi
echo "  Spring DispatcherServlet load source:"
grep "DispatcherServlet " /tmp/x3a-cl.log | head -2 | sed 's/^/    /'

# ===================================================================
# Phase X.3b — archive ON + agent OFF + scouter jar on the boot classpath
# ===================================================================
step "Phase X.3b — archive ON, agent OFF, scouter.agent.jar on -Xbootclasspath/a"
rm -f /tmp/x3b.log

java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile=$ARCHIVE \
    -XX:ArchiveRelocationMode=0 \
    -Xshare:auto \
    -Xbootclasspath/a:$AGENT \
    -Dscouter.config=$CONF \
    "-Xlog:class+load=info:file=/tmp/x3b-cl.log::filesize=0" \
    -jar $EXTRACT > /tmp/x3b.log 2>&1 &
PID=$!

X3B_OK=0
if waitForHello $PID "X.3b"; then
    curl -s http://localhost:8080/hello    | head -c 60; echo
    curl -s http://localhost:8080/work/100 | head -c 60; echo
    sleep 1
    X3B_OK=1
fi
stopSpring $PID

echo ""
echo "--- X.3b results ---"
if [[ $X3B_OK -eq 1 ]]; then
    echo "  JVM booted normally"
    # Were Spring classes loaded from the archive?
    SHARED=$(grep -c "shared objects file" /tmp/x3b-cl.log || true)
    JAR=$(grep -c "BOOT-INF" /tmp/x3b-cl.log || true)
    echo "  class+load 'shared objects file' lines: $SHARED"
    echo "  class+load 'BOOT-INF/...' lines: $JAR"
    if [[ $SHARED -gt 100 ]]; then
        echo "  → many classes loaded from the archive (archive benefit intact)"
    fi
    grep "DispatcherServlet " /tmp/x3b-cl.log | head -2 | sed 's/^/    /'
    echo "  Scouter activity traces (stdout):"
    grep -iE 'scouter' /tmp/x3b.log | head -3 | sed 's/^/    /' || echo "    (Scouter agent is OFF — no output is normal)"
else
    echo "  FAIL: JVM died / failed to start"
    grep -iE "error|exception|NoClassDef" /tmp/x3b.log | head -5 | sed 's/^/    /'
fi

# ===================================================================
# Wrap-up
# ===================================================================
step "Wrap-up"

X1_OK=$([[ -s /tmp/x1.log ]] && grep -q "Started App in" /tmp/x1.log && echo "PASS" || echo "FAIL")
X2_OK=$([[ -f $ARCHIVE ]] && echo "PASS" || echo "FAIL")
X3A_STATUS=$([[ $X3A_OK -eq 1 ]] && echo "PASS" || echo "FAIL (expected)")
X3B_STATUS=$([[ $X3B_OK -eq 1 ]] && echo "PASS" || echo "FAIL")

echo "Phase X.1  (Scouter + Spring Boot sanity)            : $X1_OK"
echo "Phase X.2  (Scouter agent ON, archive built)         : $X2_OK"
echo "Phase X.3a (archive ON, agent OFF, no scouter jar)   : $X3A_STATUS"
echo "Phase X.3b (archive ON, agent OFF, scouter boot cp)  : $X3B_STATUS"
echo ""
echo "=== Conclusion ==="
if [[ "$X3B_STATUS" == "PASS" ]]; then
    echo "Scouter ingestion looks viable. Try integrating via the archive + boot-classpath pattern."
else
    echo "X.3b failed. Scouter's transforms are not archive-compatible."
    echo "→ Next: figure out which transform isn't compatible (Javassist runtime generation? hidden class?)."
fi
echo ""
echo "Archive: $ARCHIVE"
echo "Logs: /tmp/x{1,2,3a,3b}.log, /tmp/x{2-cds,3a-cl,3b-cl}.log"
