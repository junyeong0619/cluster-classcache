#!/bin/bash
# Runs at container image build time:
# - compile sources
# - package app.jar / agent.jar
# - run Phase 1 to produce app.jsa archive
set -euo pipefail

BB=/opt/byte-buddy.jar
SRC=/work/src
OUT=/work/build

mkdir -p "$OUT/classes/app" "$OUT/classes/agent" "$OUT/agent-build"

javac -d "$OUT/classes/app" "$SRC/app/com/example/app/App.java"

javac -cp "$BB" -d "$OUT/classes/agent" \
    "$SRC/agent/com/example/agent/TraceAdvice.java" \
    "$SRC/agent/com/example/agent/TraceAgent.java"

( cd "$OUT/classes/app" && jar cf "$OUT/app.jar" . )

# Shaded agent jar
( cd "$OUT/agent-build" && unzip -qo "$BB" )
cp -r "$OUT/classes/agent/"* "$OUT/agent-build/"
mkdir -p "$OUT/agent-build/META-INF"
cat > "$OUT/agent-build/META-INF/MANIFEST.MF" <<'EOF'
Manifest-Version: 1.0
Premain-Class: com.example.agent.TraceAgent
Can-Retransform-Classes: false
Can-Redefine-Classes: false
EOF
( cd "$OUT/agent-build" && jar cfm "$OUT/agent.jar" META-INF/MANIFEST.MF . )

# Build archive (Phase 1 equivalent, one-shot)
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit="$OUT/app.jsa" \
    -javaagent:"$OUT/agent.jar" \
    -cp "$OUT/app.jar" \
    com.example.app.App

ls -la "$OUT/app.jsa"
echo "Build complete."
