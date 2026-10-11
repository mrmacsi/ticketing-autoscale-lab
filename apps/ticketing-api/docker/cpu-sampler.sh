#!/bin/sh
# CPU utilisation of this application container every 2 s, written for /instance-info and /lab/cpu (the
# waiting room's admission signal). Measured from the container's own cgroup (cgroup v2 cpu.stat) so
# that other workloads on the same host do not count: locally every container shares one Docker VM, and
# the load generator's thousands of connections used to trip the 90 % CPU cut. Falls back to the whole
# host from /proc/stat without cgroup v2. The autoscaler uses Cloud Monitoring's VM metric instead.
OUT="${LAB_GUEST_CPU_FILE:-/tmp/guest-cpu.json}"
CG=/sys/fs/cgroup

if [ -r "$CG/cpu.stat" ]; then
    SOURCE=container
    set -- $(cat "$CG/cpu.max" 2>/dev/null)
    if [ -n "${1:-}" ] && [ "$1" != max ] && [ -n "${2:-}" ]; then
        CPUS=$(awk -v q="$1" -v p="$2" 'BEGIN { printf "%.3f", q / p }')
    else
        CPUS=$(nproc)
    fi
    # Prints "<cpu microseconds used> <microseconds of capacity elapsed>".
    read_stat() { awk -v cpus="$CPUS" 'NR == FNR { if ($1 == "usage_usec") used = $2; next } { printf "%.0f %.0f\n", used, $1 * 1000000 * cpus }' "$CG/cpu.stat" /proc/uptime; }
else
    SOURCE=host
    # Prints "<busy jiffies> <total jiffies>".
    read_stat() { awk '/^cpu / { idle = $5 + $6; total = 0; for (i = 2; i <= NF; i++) total += $i; print total - idle, total }' /proc/stat; }
fi

set -- $(read_stat)
prev_used=$1; prev_total=$2
while true; do
    sleep 2
    set -- $(read_stat)
    used=$1; total=$2
    dt=$((total - prev_total)); du=$((used - prev_used))
    if [ "$dt" -gt 0 ]; then
        pct=$(awk -v dt="$dt" -v du="$du" 'BEGIN { p = du / dt * 100; if (p > 100) p = 100; if (p < 0) p = 0; printf "%.1f", p }')
        printf '{"percent":%s,"sampled_at":"%s","source":"%s"}' "$pct" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$SOURCE" > "${OUT}.tmp" && mv "${OUT}.tmp" "$OUT"
    fi
    prev_used=$used; prev_total=$total
done
