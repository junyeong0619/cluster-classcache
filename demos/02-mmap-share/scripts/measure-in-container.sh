#!/bin/bash
# Runs at container runtime.
# Launches N JVMs against the same archive and measures page sharing via smaps.
set -euo pipefail

N="${N:-4}"
ARCHIVE=/work/build/app.jsa
APP_JAR=/work/build/app.jar
SETTLE_SEC="${SETTLE_SEC:-5}"

echo "============================================================"
echo "  Multi-JVM mmap sharing measurement"
echo "============================================================"
echo "N (workload JVMs):     $N"
echo "Archive:               $ARCHIVE ($(du -h "$ARCHIVE" | awk '{print $1}'))"
echo "Settle wait:           ${SETTLE_SEC}s"
echo ""

# Run N JVMs in the background (agent OFF, archive ON).
PIDS=()
for i in $(seq 1 $N); do
    java \
        -XX:+UnlockDiagnosticVMOptions \
        -XX:+AllowArchivingWithJavaAgent \
        -XX:SharedArchiveFile="$ARCHIVE" \
        -XX:ArchiveRelocationMode=0 \
        -Xshare:on \
        -cp "$APP_JAR" \
        com.example.app.App --loop > "/tmp/jvm-$i.log" 2>&1 &
    PIDS+=($!)
done

echo "JVM PIDs: ${PIDS[@]}"
sleep "$SETTLE_SEC"

# Sanity check: all JVMs still alive.
for PID in "${PIDS[@]}"; do
    if ! kill -0 "$PID" 2>/dev/null; then
        echo "FAIL: JVM PID $PID died. Logs:"
        cat /tmp/jvm-*.log
        exit 1
    fi
done
echo "OK: all JVMs alive"
echo ""

# ---- A. Baseline: a single JVM without the archive ----
echo "============================================================"
echo "  Baseline: archive OFF, single JVM"
echo "============================================================"
java -cp "$APP_JAR" com.example.app.App --loop > /tmp/jvm-baseline.log 2>&1 &
BASELINE_PID=$!
sleep "$SETTLE_SEC"

if kill -0 "$BASELINE_PID" 2>/dev/null; then
    BASELINE_RSS=$(awk '/Rss:/ {s+=$2} END {print s}' /proc/$BASELINE_PID/smaps)
    echo "Baseline PID:  $BASELINE_PID"
    echo "Baseline RSS:  ${BASELINE_RSS} KB"
else
    echo "WARN: baseline JVM died (no reference value)"
    BASELINE_RSS=0
fi
echo ""

# ---- B. Per-JVM analysis of the archive-backed VMA ----
echo "============================================================"
echo "  Per-JVM archive mmap region (smaps)"
echo "============================================================"

analyze_archive_vma() {
    local pid="$1"
    awk -v archive="app.jsa" '
        $0 ~ archive { in_block=1; print "[", $1, $NF, "]"; next }
        in_block && /^Size:/             { size=$2 }
        in_block && /^Rss:/              { rss=$2 }
        in_block && /^Pss:/              { pss=$2 }
        in_block && /^Shared_Clean:/     { sc=$2 }
        in_block && /^Shared_Dirty:/     { sd=$2 }
        in_block && /^Private_Clean:/    { pc=$2 }
        in_block && /^Private_Dirty:/    { pd=$2 }
        in_block && /^VmFlags:/ {
            printf "  Size=%d Rss=%d Pss=%d Shared_Clean=%d Shared_Dirty=%d Private_Clean=%d Private_Dirty=%d\n", size, rss, pss, sc, sd, pc, pd
            total_size += size
            total_rss += rss
            total_pss += pss
            total_sc += sc
            total_sd += sd
            total_pc += pc
            total_pd += pd
            in_block=0
        }
        END {
            printf "%d %d %d %d %d %d %d\n", total_size, total_rss, total_pss, total_sc, total_sd, total_pc, total_pd > "/tmp/vma-stats"
        }
    ' "/proc/$pid/smaps"
}

declare -a STATS_LINES
for PID in "${PIDS[@]}"; do
    echo ""
    echo "--- PID $PID ---"
    analyze_archive_vma "$PID"
    STATS_LINES+=("$(cat /tmp/vma-stats)")
done

# ---- C. Aggregate ----
echo ""
echo "============================================================"
echo "  Aggregate"
echo "============================================================"

T_SIZE=0; T_RSS=0; T_PSS=0; T_SC=0; T_SD=0; T_PC=0; T_PD=0
for line in "${STATS_LINES[@]}"; do
    read s r p sc sd pc pd <<< "$line"
    T_SIZE=$((T_SIZE + s))
    T_RSS=$((T_RSS + r))
    T_PSS=$((T_PSS + p))
    T_SC=$((T_SC + sc))
    T_SD=$((T_SD + sd))
    T_PC=$((T_PC + pc))
    T_PD=$((T_PD + pd))
done

printf "%-25s %12s\n" "N JVMs"             "$N"
printf "%-25s %12d KB\n" "Σ Size (virtual)"      "$T_SIZE"
printf "%-25s %12d KB\n" "Σ Rss (resident)"   "$T_RSS"
printf "%-25s %12d KB\n" "Σ Pss (proportional)" "$T_PSS"
printf "%-25s %12d KB\n" "Σ Shared_Clean"     "$T_SC"
printf "%-25s %12d KB\n" "Σ Shared_Dirty"     "$T_SD"
printf "%-25s %12d KB\n" "Σ Private_Clean"    "$T_PC"
printf "%-25s %12d KB\n" "Σ Private_Dirty"    "$T_PD"

if [[ "$T_RSS" -gt 0 && "$T_PSS" -gt 0 ]]; then
    SAVED=$((T_RSS - T_PSS))
    RATIO=$(awk "BEGIN {printf \"%.1f\", $T_PSS / $T_RSS * 100}")
    echo ""
    echo "Physical memory saved:  $SAVED KB  (Σ Rss - Σ Pss)"
    echo "Pss / Rss ratio:        ${RATIO}%"
    if [[ "$N" -gt 1 ]]; then
        IDEAL_RATIO=$(awk "BEGIN {printf \"%.1f\", 100.0 / $N}")
        echo "Ideal ratio ($N JVM):   ${IDEAL_RATIO}%  (perfect sharing)"
    fi
fi

echo ""
echo "============================================================"
echo "  Verdict"
echo "============================================================"

if [[ "$T_SC" -gt 0 ]] && [[ "$T_PSS" -lt "$T_RSS" ]]; then
    echo "Page sharing confirmed: $T_SC KB of the archive is Shared_Clean"
    echo "   → the $N JVMs on this node share archive pages at the OS level"
    echo "   → RAM saved equals the archived region of metaspace"
else
    echo "No page sharing (Shared_Clean=0 or Pss == Rss)"
    echo "   → archive may be mapped private, or pages haven't been faulted in yet"
fi

# Cleanup
echo ""
echo "Cleaning up JVMs..."
kill "$BASELINE_PID" 2>/dev/null || true
for PID in "${PIDS[@]}"; do kill "$PID" 2>/dev/null || true; done
sleep 1
echo "Done."
