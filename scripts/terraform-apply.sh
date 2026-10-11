#!/usr/bin/env bash
# Applies the saved plan from terraform-plan.sh (retrying transient Google API failures). Refuses without a
# recorded spending approval, and
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

# A dropped connection mid-apply (seen: EOF, "http2: client connection lost", flaky TLS) can leave a
# resource created in Google Cloud but missing from the state, and makes the saved plan stale. Retry up to
# 3 times: import what was created, re-plan in the same mode, apply again. Permanent errors stop at once.
helper="${REPO_ROOT}/scripts/transient-terraform-error.py"
stage_args=()
[ -f "${TF_DIR}/tfplan.args" ] && while IFS= read -r a; do [ -n "$a" ] && stage_args+=("$a"); done < "${TF_DIR}/tfplan.args"
output="$(mktemp)"
trap 'rm -f "$output"' EXIT
for attempt in 1 2 3; do
    if tf apply -input=false -lock-timeout=60s tfplan 2>&1 | tee "$output"; [ "${PIPESTATUS[0]}" -eq 0 ]; then
        rm -f "${TF_DIR}/tfplan" "${TF_DIR}/tfplan.args"
        exit 0
    fi
    imports="$(python3 "$helper" --already-exists "$output")"
    if [ "$attempt" -eq 3 ] || { [ -z "$imports" ] && ! python3 "$helper" "$output"; }; then
        rm -f "${TF_DIR}/tfplan"
        die "terraform apply failed (see above); not retrying"
    fi
    while read -r address resource_id; do
        [ -n "$address" ] || continue
        warn "Importing ${address}: it was created before the connection dropped"
        tf import -input=false "$address" "$resource_id"
    done <<< "$imports"
    warn "Temporary failure during apply; re-planning and retrying (attempt $((attempt + 1))/3)"
    sleep $((attempt * 10))
    tf plan -input=false -lock-timeout=60s ${stage_args[@]+"${stage_args[@]}"} -out=tfplan >/dev/null
done
