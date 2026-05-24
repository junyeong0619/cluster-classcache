#!/bin/bash
# Runs at image build time.
# 1) Extract the Spring Boot fat jar (Spring Boot's recommended CDS approach).
# 2) Build the archive on top via agent + ArchiveClassesAtExit.
set -euo pipefail

APP_JAR=/work/app.jar
AGENT_JAR=/work/agent.jar
EXTRACT_DIR=/work/extracted
ARCHIVE=/work/app.jsa

mkdir -p "$EXTRACT_DIR"
cd "$EXTRACT_DIR"
java -Djarmode=tools -jar "$APP_JAR" extract --destination .
ls -la "$EXTRACT_DIR"
ls -la "$EXTRACT_DIR/lib" | head -10

# Run Spring Boot, warm up, then graceful shutdown.
echo ""
echo "==> Starting Spring Boot with archive recording..."
java \
    -XX:+UnlockDiagnosticVMOptions \
    -XX:+AllowArchivingWithJavaAgent \
    -XX:ArchiveClassesAtExit="$ARCHIVE" \
    -javaagent:"$AGENT_JAR" \
    -jar "$EXTRACT_DIR/app.jar" &
SB_PID=$!

# Wait for Spring Boot to start (up to 60s).
echo "==> Waiting for Spring Boot startup..."
for i in $(seq 1 60); do
    if curl -fsS http://localhost:8080/hello >/dev/null 2>&1; then
        echo "==> Spring Boot ready after ${i}s"
        break
    fi
    sleep 1
done

if ! kill -0 $SB_PID 2>/dev/null; then
    echo "Spring Boot died"
    exit 1
fi

# Warmup requests.
echo "==> Warmup requests..."
curl -s http://localhost:8080/hello | head -c 80; echo
curl -s http://localhost:8080/work/100 | head -c 80; echo
curl -s http://localhost:8080/work/1000 | head -c 80; echo

# Graceful shutdown (triggers archive dump).
echo ""
echo "==> Graceful shutdown..."
kill -TERM $SB_PID
wait $SB_PID 2>/dev/null || true

if [[ ! -f "$ARCHIVE" ]]; then
    echo "Archive not produced"
    exit 1
fi

echo ""
echo "==> Archive built:"
ls -la "$ARCHIVE"
du -h "$ARCHIVE"
