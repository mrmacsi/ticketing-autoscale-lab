#!/bin/sh
# Whole-VM CPU utilisation from /proc/stat every 2 s (containers see the host's counters), written
# for /instance-info. This is the guest view; the autoscaler uses Cloud Monitoring's metric.
OUT="${LAB_GUEST_CPU_FILE:-/tmp/guest-cpu.json}"
read_stat() { awk '/^cpu / {idle=$5+$6; total=0; for (i=2; i<=NF; i++) total+=$i; print total, idle}' /proc/stat; }
set -- $(read_stat)
prev_total=$1; prev_idle=$2
while true; do
    sleep 2
    set -- $(read_stat)
    total=$1; idle=$2
    dt=$((total - prev_total)); di=$((idle - prev_idle))
    if [ "$dt" -gt 0 ]; then
        pct=$(awk -v dt="$dt" -v di="$di" 'BEGIN { printf "%.1f", (1 - di / dt) * 100 }')
        printf '{"percent":%s,"sampled_at":"%s"}' "$pct" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "${OUT}.tmp" && mv "${OUT}.tmp" "$OUT"
    fi
    prev_total=$total; prev_idle=$idle
done
