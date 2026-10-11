#!/usr/bin/env bash
# Applies the saved plan from terraform-plan.sh. Refuses without a recorded spending approval, and
# while an experiment is active: apply resets the autoscaler to its Terraform baseline, which would
# override the running experiment's settings. ALLOW_DURING_EXPERIMENT=1 overrides the second check.
source "$(dirname "$0")/lib.sh"
require_approval
if [ "${ALLOW_DURING_EXPERIMENT:-0}" != 1 ]; then
    active="$("$(dirname "$0")/control.sh" GET /api/state 2>/dev/null | python3 -c '
import json, sys
e = json.load(sys.stdin).get("experiment") or {}
print(e.get("id", "") if e.get("state") in ("preparing", "ready", "running", "stopping", "cooling_down") else "")' 2>/dev/null || true)"
    [ -z "$active" ] || die "Experiment ${active} is active; apply would reset its autoscaler settings. Wait for it to complete (or set ALLOW_DURING_EXPERIMENT=1)."
fi
[ -f "${TF_DIR}/tfplan" ] || die "No saved plan; run ./scripts/terraform-plan.sh first"
tf apply -input=false -lock-timeout=60s tfplan
rm -f "${TF_DIR}/tfplan"
