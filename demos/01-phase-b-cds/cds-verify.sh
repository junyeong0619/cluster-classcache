#!/usr/bin/env bash
#
# Phase B verification script.
# Goal: confirm that ByteBuddy-transformed classes get baked into the dynamic
#       CDS archive and are then loaded from the archive on the next run.
#
# If this passes, the v0.3 design's core hypothesis holds.
# If it fails, v0.3 needs a redesign.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$HERE/src"
LIB="$HERE/lib"
WORK="$HERE/work"

BB_VERSION="${BB_VERSION:-1.14.19}"
BB_JAR="$LIB/byte-buddy-$BB_VERSION.jar"
BB_URL="https://repo1.maven.org/maven2/net/bytebuddy/byte-buddy/$BB_VERSION/byte-buddy-$BB_VERSION.jar"

mkdir -p "$LIB" "$WORK"

step() {
    echo ""
    echo "============================================================"
    echo "  $*"
    echo "============================================================"
}

# --- 1. Fetch ByteBuddy --------------------------------------------
if [[ ! -f "$BB_JAR" ]]; then
    step "Downloading ByteBuddy $BB_VERSION"
    curl -fsSL "$BB_URL" -o "$BB_JAR"
fi
echo "ByteBuddy: $BB_JAR ($(du -h "$BB_JAR" | awk '{print $1}'))"

# --- 2. Compile ----------------------------------------------------
step "Compile app + agent"
rm -rf "$WORK/classes"
mkdir -p "$WORK/classes/app" "$WORK/classes/agent"

javac -d "$WORK/classes/app" \
    "$SRC/app/com/example/app/App.java"

javac -cp "$BB_JAR" -d "$WORK/classes/agent" \
    "$SRC/agent/com/example/agent/TraceAdvice.java" \
    "$SRC/agent/com/example/agent/TraceAgent.java"

# --- 3. app.jar ----------------------------------------------------
step "Build app.jar"
( cd "$WORK/classes/app" && jar cf "$WORK/app.jar" . )
echo "$WORK/app.jar ($(du -h "$WORK/app.jar" | awk '{print $1}'))"

# --- 4. agent.jar (ByteBuddy shaded) -------------------------------
step "Build agent.jar (ByteBuddy shaded)"
rm -rf "$WORK/agent-build"
mkdir -p "$WORK/agent-build/META-INF"
( cd "$WORK/agent-build" && unzip -qo "$BB_JAR" )
cp -r "$WORK/classes/agent/"* "$WORK/agent-build/"
cat > "$WORK/agent-build/META-INF/MANIFEST.MF" <<'EOF'
Manifest-Version: 1.0
Premain-Class: com.example.agent.TraceAgent
Can-Retransform-Classes: false
Can-Redefine-Classes: false
EOF
( cd "$WORK/agent-build" && jar cfm "$WORK/agent.jar" META-INF/MANIFEST.MF . )
echo "$WORK/agent.jar ($(du -h "$WORK/agent.jar" | awk '{print $1}'))"

# --- 5. Phase 1: build archive (agent ON) --------------------------
step "PHASE 1 — Build dynamic CDS archive (agent ON)"
rm -f "$WORK/app.jsa" "$WORK/phase1.log"

PHASE1_OUT=$(
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit="$WORK/app.jsa" \
    -javaagent:"$WORK/agent.jar" \
    "-Xlog:cds=info,cds+dynamic=info,cds+class=debug:file=$WORK/phase1.log::filesize=0" \
    -cp "$WORK/app.jar" \
    com.example.app.App 2>&1
)

echo "$PHASE1_OUT"

if [[ ! -f "$WORK/app.jsa" ]]; then
    echo ""
    echo "FAIL: archive file was not created."
    echo "  Phase 1 log: $WORK/phase1.log"
    exit 1
fi

ARCHIVE_SIZE=$(du -h "$WORK/app.jsa" | awk '{print $1}')
echo ""
echo "OK: archive created: $WORK/app.jsa ($ARCHIVE_SIZE)"

# --- 6. Phase 2a: use archive + agent OFF (v0.3 runtime mode) ------
# The real v0.3 runtime scenario: mmap the archive, don't run the agent.
# This must work if the archive contains baked-in transformed bytecode.
step "PHASE 2a — Use CDS archive (agent OFF, -Xshare:on) [v0.3 runtime mode]"
rm -f "$WORK/phase2a.log"

PHASE2A_OUT=$(
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile="$WORK/app.jsa" \
    -Xshare:on \
    "-Xlog:cds=info,class+load=info:file=$WORK/phase2a.log::filesize=0" \
    -cp "$WORK/app.jar" \
    com.example.app.App 2>&1
) || {
    echo "FAIL: Phase 2a JVM execution itself failed."
    echo "$PHASE2A_OUT"
    exit 1
}
echo "$PHASE2A_OUT"

# --- 7. Phase 2b: archive + agent ON (reference; archive usually bypassed) -
step "PHASE 2b — Use CDS archive (agent ON) [informational]"
rm -f "$WORK/phase2b.log"

PHASE2B_OUT=$(
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:SharedArchiveFile="$WORK/app.jsa" \
    -Xshare:auto \
    -javaagent:"$WORK/agent.jar" \
    "-Xlog:class+load=info:file=$WORK/phase2b.log::filesize=0" \
    -cp "$WORK/app.jar" \
    com.example.app.App 2>&1
) || true
echo "(re-running the agent may bypass the archive — informational only)"

# --- 8. Checks -----------------------------------------------------
step "Verification results"

PASS=0
FAIL=0

# Check 1: Phase 1 built the archive and the App is in it.
if grep -qE "Written dynamic archive" "$WORK/phase1.log"; then
    APP_COUNT=$(grep -E "(app|unregistered) +=" "$WORK/phase1.log" | head -2 | awk '{s+=$NF} END {print s}')
    echo "[Check 1] PASS: Phase 1 archive built (contains $APP_COUNT app+unregistered classes)"
    PASS=$((PASS+1))
else
    echo "[Check 1] FAIL: no Phase 1 archive-written log"
    FAIL=$((FAIL+1))
fi

# Check 2 (KEY): in Phase 2a, App loads from the archive (agent OFF).
APP_FROM_ARCHIVE=$(grep "com.example.app.App " "$WORK/phase2a.log" 2>/dev/null | grep -c "shared objects file" || true)
if [[ "$APP_FROM_ARCHIVE" -gt 0 ]]; then
    echo "[Check 2] PASS (KEY): Phase 2a loaded App from the archive — without an agent"
    grep "com.example.app.App " "$WORK/phase2a.log" | grep "shared objects file" | head -1 | sed 's/^/   /'
    PASS=$((PASS+1))
else
    echo "[Check 2] FAIL (KEY): Phase 2a did not load App from the archive"
    grep "com.example.app.App " "$WORK/phase2a.log" 2>/dev/null | head -3 | sed 's/^/   /' || echo "   (none)"
    FAIL=$((FAIL+1))
fi

# Check 3 (KEY): Phase 2a actually executed the transformed code (TRACE-ENTER without agent).
if echo "$PHASE2A_OUT" | grep -q "TRACE-ENTER"; then
    HITS=$(echo "$PHASE2A_OUT" | grep -c "TRACE-ENTER")
    echo "[Check 3] PASS (KEY): Phase 2a executed transformed code — TRACE-ENTER printed $HITS time(s)"
    echo "   → ByteBuddy transforms are baked into the archive"
    PASS=$((PASS+1))
else
    echo "[Check 3] FAIL (KEY): Phase 2a did not execute the transformed code"
    echo "   → archive either holds the pre-transform bytecode, or the archive isn't used"
    FAIL=$((FAIL+1))
fi

# Check 4 (reference): how does Phase 2b (agent ON) load App?
P2B_FROM_ARCHIVE=$(grep "com.example.app.App " "$WORK/phase2b.log" 2>/dev/null | grep -c "shared objects file" || true)
P2B_FROM_JAR=$(grep "com.example.app.App " "$WORK/phase2b.log" 2>/dev/null | grep -cE "\.jar" || true)
if [[ "$P2B_FROM_ARCHIVE" -gt 0 ]]; then
    echo "[Check 4] INFO: Phase 2b (agent ON) also loaded App from archive — bonus"
elif [[ "$P2B_FROM_JAR" -gt 0 ]]; then
    echo "[Check 4] INFO: Phase 2b (agent ON) loaded App from jar — expected"
    echo "   → runtime pod should NOT run the agent (matches the v0.3 design)"
fi

# --- 9. Conclusion -------------------------------------------------
echo ""
echo "============================================================"
echo "  Summary"
echo "============================================================"
echo "Archive size:    $ARCHIVE_SIZE"
echo "Phase 1 log:     $WORK/phase1.log"
echo "Phase 2a log:    $WORK/phase2a.log  (agent OFF — v0.3 runtime)"
echo "Phase 2b log:    $WORK/phase2b.log  (agent ON  — informational)"
echo "Archive file:    $WORK/app.jsa"
echo ""

if [[ "$FAIL" -eq 0 ]] && [[ "$PASS" -ge 3 ]]; then
    echo "Phase B verification PASS  (pass=$PASS, fail=$FAIL)"
    echo ""
    echo "→ v0.3 core hypothesis holds:"
    echo "   (1) ByteBuddy transforms are included in the dynamic CDS archive"
    echo "   (2) Runtime JVM can run the transformed code with just the archive mmap, no agent"
    echo "   (3) → physical basis for cross-pod metaspace sharing established"
    echo ""
    echo "→ Next actions:"
    echo "   - Run multiple JVMs on one node and measure page sharing via pmap/smaps"
    echo "   - Repeat with a real Spring Boot + in-house APM agent combo"
    exit 0
else
    echo "Phase B verification FAIL  (pass=$PASS, fail=$FAIL)"
    echo ""
    echo "→ v0.3's archive sharing strategy is unusable. Look into:"
    echo "   (a) Adjust ByteBuddy transform options (a different visitor than Advice)"
    echo "   (b) A different transform library (ASM directly, JDK Class-File API)"
    echo "   (c) Consider OpenJ9 SCC instead of AppCDS"
    echo ""
    echo "Debugging:"
    echo "  grep -i 'com.example' $WORK/phase2a.log"
    echo "  grep -iE 'cds|archive' $WORK/phase1.log | tail -50"
    exit 1
fi
