#!/bin/bash
# Container-runtime measurement.
# - Launch N Spring Boot JVMs against the same archive.
# - Measure archive page sharing via smaps.
# - Compare startup time (archive ON vs OFF).
set -euo pipefail

N="${N:-4}"
ARCHIVE=/work/app.jsa
EXTRACT_DIR=/work/extracted
SETTLE_SEC="${SETTLE_SEC:-15}"

echo "============================================================"
echo "  Spring Boot multi-JVM mmap sharing measurement (N=$N)"
echo "============================================================"
echo "Archive:  $ARCHIVE ($(du -h "$ARCHIVE" | awk '{print $1}'))"
echo "Settle:   ${SETTLE_SEC}s (full Spring Boot startup)"
echo ""

# ---- Startup-time comparison (archive ON vs OFF) ----
echo "============================================================"
echo "  Startup time comparison"
echo "============================================================"

measure_startup() {
    local label="$1"
    shift
    local start_ms=$(date +%s%3N)
    "$@" --server.port=0 > /tmp/startup-$label.log 2>&1 &
    local pid=$!
    # The "Started" log line indicates startup complete.
    for i in $(seq 1 600); do
        if grep -q "Started App in" /tmp/startup-$label.log 2>/dev/null; then
            local end_ms=$(date +%s%3N)
            local elapsed=$((end_ms - start_ms))
            echo "  $label: ${elapsed} ms"
            kill -TERM $pid 2>/dev/null || true
            wait $pid 2>/dev/null || true
            return
        fi
        if ! kill -0 $pid 2>/dev/null; then
            echo "  $label: startup failed (log):"
            tail -20 /tmp/startup-$label.log
            return
        fi
        sleep 0.05
    done
    echo "  $label: did not start within 30s"
    kill -9 $pid 2>/dev/null || true
}

measure_startup "archive-OFF" \
    java -jar "$EXTRACT_DIR/app.jar"

measure_startup "archive-ON" \
    java -XX:+UnlockDiagnosticVMOptions -XX:+AllowArchivingWithJavaAgent \
         -XX:SharedArchiveFile="$ARCHIVE" -XX:ArchiveRelocationMode=0 \
         -Xshare:on \
         -jar "$EXTRACT_DIR/app.jar"

# ---- N concurrent JVMs (for mmap-sharing measurement) ----
echo ""
echo "============================================================"
echo "  Running N=$N JVMs concurrently (each on a random port)"
echo "============================================================"

PIDS=()
for i in $(seq 1 $N); do
    java \
        -XX:+UnlockDiagnosticVMOptions \
        -XX:+AllowArchivingWithJavaAgent \
        -XX:SharedArchiveFile="$ARCHIVE" \
        -XX:ArchiveRelocationMode=0 \
        -Xshare:on \
        -jar "$EXTRACT_DIR/app.jar" --server.port=0 > "/tmp/jvm-$i.log" 2>&1 &
    PIDS+=($!)
done

echo "JVM PIDs: ${PIDS[@]}"
echo "Settle ${SETTLE_SEC}s ..."
sleep "$SETTLE_SEC"

# Sanity check.
for PID in "${PIDS[@]}"; do
    if ! kill -0 "$PID" 2>/dev/null; then
        echo "FAIL: JVM PID $PID died. Last logs:"
        for i in $(seq 1 $N); do tail -20 "/tmp/jvm-$i.log" 2>/dev/null; done
        exit 1
    fi
done
echo "OK: N=$N JVMs alive"
echo ""

# ---- Archive VMA analysis ----
analyze_pid() {
    local pid="$1"
    awk '
        $0 ~ /app\.jsa/ { in_block=1; print "[", $1, $NF, "]"; next }
        in_block && /^Size:/             { size=$2 }
        in_block && /^Rss:/              { rss=$2 }
        in_block && /^Pss:/              { pss=$2 }
        in_block && /^Shared_Clean:/     { sc=$2 }
        in_block && /^Shared_Dirty:/     { sd=$2 }
        in_block && /^Private_Clean:/    { pc=$2 }
        in_block && /^Private_Dirty:/    { pd=$2 }
        in_block && /^VmFlags:/ {
            printf "  Size=%d Rss=%d Pss=%d Shared_Clean=%d Shared_Dirty=%d Private_Clean=%d Private_Dirty=%d\n", size, rss, pss, sc, sd, pc, pd
            total_size += size; total_rss += rss; total_pss += pss
            total_sc += sc; total_sd += sd; total_pc += pc; total_pd += pd
            in_block=0
        }
        END {
            printf "%d %d %d %d %d %d %d\n", total_size, total_rss, total_pss, total_sc, total_sd, total_pc, total_pd > "/tmp/vma-stats"
        }
    ' "/proc/$pid/smaps"
}

echo "============================================================"
echo "  Per-JVM archive VMA"
echo "============================================================"
declare -a STATS
for PID in "${PIDS[@]}"; do
    echo ""
    echo "--- PID $PID ---"
    analyze_pid "$PID"
    STATS+=("$(cat /tmp/vma-stats)")
done

# ---- Total JVM RSS (archive vs no-archive) ----
echo ""
echo "============================================================"
echo "  Total JVM RSS (archive effect — includes metaspace region)"
echo "============================================================"
T_RSS_ALL=0
for PID in "${PIDS[@]}"; do
    rss=$(awk '/^Rss:/ {s+=$2} END {print s}' /proc/$PID/smaps)
    echo "  PID $PID total RSS: ${rss} KB"
    T_RSS_ALL=$((T_RSS_ALL + rss))
done
AVG_RSS=$((T_RSS_ALL / N))
echo "  Average RSS: ${AVG_RSS} KB"

# Baseline (single JVM, no archive).
echo ""
echo "  -- archive OFF baseline (single JVM) --"
java -jar "$EXTRACT_DIR/app.jar" --server.port=0 > /tmp/baseline.log 2>&1 &
BASE_PID=$!
sleep "$SETTLE_SEC"
if kill -0 $BASE_PID 2>/dev/null; then
    BASE_RSS=$(awk '/^Rss:/ {s+=$2} END {print s}' /proc/$BASE_PID/smaps)
    echo "  Baseline RSS (archive OFF): ${BASE_RSS} KB"
    kill -TERM $BASE_PID 2>/dev/null || true
fi

# ---- Aggregate ----
echo ""
echo "============================================================"
echo "  Archive VMA aggregate"
echo "============================================================"
T_SIZE=0; T_RSS=0; T_PSS=0; T_SC=0; T_SD=0; T_PC=0; T_PD=0
for line in "${STATS[@]}"; do
    read s r p sc sd pc pd <<< "$line"
    T_SIZE=$((T_SIZE + s)); T_RSS=$((T_RSS + r)); T_PSS=$((T_PSS + p))
    T_SC=$((T_SC + sc)); T_SD=$((T_SD + sd))
    T_PC=$((T_PC + pc)); T_PD=$((T_PD + pd))
done

printf "%-25s %12s\n" "N JVMs"               "$N"
printf "%-25s %12d KB\n" "Σ Size"             "$T_SIZE"
printf "%-25s %12d KB\n" "Σ Rss"              "$T_RSS"
printf "%-25s %12d KB\n" "Σ Pss"              "$T_PSS"
printf "%-25s %12d KB\n" "Σ Shared_Clean"     "$T_SC"
printf "%-25s %12d KB\n" "Σ Shared_Dirty"     "$T_SD"
printf "%-25s %12d KB\n" "Σ Private_Clean"    "$T_PC"
printf "%-25s %12d KB\n" "Σ Private_Dirty"    "$T_PD"

if [[ "$T_RSS" -gt 0 ]]; then
    SAVED=$((T_RSS - T_PSS))
    RATIO=$(awk "BEGIN {printf \"%.1f\", $T_PSS / $T_RSS * 100}")
    SAVED_MB=$(awk "BEGIN {printf \"%.1f\", $SAVED / 1024}")
    echo ""
    echo "Physical memory saved:  ${SAVED} KB (${SAVED_MB} MB)"
    echo "Pss / Rss ratio:        ${RATIO}%"
    if [[ "$N" -gt 1 ]]; then
        IDEAL=$(awk "BEGIN {printf \"%.1f\", 100.0 / $N}")
        echo "Ideal ratio ($N JVM):   ${IDEAL}%"
    fi
fi

# Cleanup
for PID in "${PIDS[@]}"; do kill -TERM "$PID" 2>/dev/null || true; done
kill -TERM $BASE_PID 2>/dev/null || true
sleep 2
for PID in "${PIDS[@]}"; do kill -9 "$PID" 2>/dev/null || true; done
echo ""
echo "Done."
