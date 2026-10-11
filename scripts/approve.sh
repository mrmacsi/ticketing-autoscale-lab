#!/usr/bin/env bash
# Records the user's explicit approval of the instance ceiling and budget. Only run after the
# owner of the Google Cloud account has approved these numbers.
source "$(dirname "$0")/lib.sh"
[ $# -eq 2 ] || die "usage: $0 <approved_max_instances> <monthly_budget>"
cat > "$APPROVAL_FILE" <<APPROVAL
APPROVED_MAX_INSTANCES=$1
APPROVED_BUDGET="$2"
APPROVED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
APPROVAL
log "Recorded approval: max $1 application VMs, budget $2"
