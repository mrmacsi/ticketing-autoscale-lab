#!/usr/bin/env bash
# Sourced after lib.sh. All mutations here require the caller's destroy approval.

tf_for_cleanup() {
    # Use HTTPS with HTTP/1.1 for cleanup: long-lived HTTP/2 connections on
    # unstable networks can fail during deletion polling. Scope the setting
    # to this Terraform command, leaving app traffic and gcloud unchanged.
    GODEBUG="${GODEBUG:+${GODEBUG},}http2client=0" tf "$@"
}

destroy_plan_with_retry() {
    local plan="$1" attempt output status delay
    shift
    case "$plan" in
        workloads-destroy.tfplan|destroy.tfplan) ;;
        *) die "Unexpected destroy plan filename" ;;
    esac
    output="$(mktemp)" || return 1
    for attempt in 1 2 3; do
        # Never reuse a saved plan after a failed refresh. tf refreshes the
        # account-specific OAuth token on every attempt; planning cannot delete.
        rm -f "${TF_DIR}/${plan}"
        if tf_for_cleanup plan -destroy -input=false -lock-timeout=60s -parallelism=2 "$@" -out="$plan" 2>&1 | tee "$output"; then
            rm -f "$output"
            return 0
        else
            status=$?
        fi
        if [ "$attempt" -eq 3 ] || ! python3 "${REPO_ROOT}/scripts/transient-terraform-error.py" "$output"; then
            rm -f "$output" "${TF_DIR}/${plan}"
            return "$status"
        fi
        delay=$((attempt * 5))
        warn "Temporary Google API connection failure during destroy planning; retrying in ${delay}s (attempt $((attempt + 1))/3). No deletion was attempted by this plan."
        sleep "$delay"
    done
}

destroy_phase_with_recovery() {
    local plan="$1" attempt output status
    shift
    output="$(mktemp)" || return 1
    for attempt in 1 2 3; do
        if ! destroy_plan_with_retry "$plan" "$@"; then
            rm -f "$output"
            return 1
        fi
        # Both phases must be deletion-only; the workload phase must also
        # preserve the network until Cloud Run has released its reservations.
        if ! terraform -chdir="$TF_DIR" show -json "$plan" | python3 -c '
import json, sys
for change in json.load(sys.stdin).get("resource_changes", []):
    actions = change["change"]["actions"]
    if any(action in actions for action in ("create", "update")) or (
        sys.argv[1] == "workloads-destroy.tfplan" and "delete" in actions
        and change["address"].startswith("module.networking.")
    ):
        phase = "workload" if sys.argv[1] == "workloads-destroy.tfplan" else "final"
        sys.exit("Unsafe " + phase + " destroy plan: " + change["address"])
' "$plan"; then
            rm -f "$output" "${TF_DIR}/${plan}"
            return 1
        fi
        if tf_for_cleanup apply -input=false -lock-timeout=60s -parallelism=2 "$plan" 2>&1 | tee "$output"; then
            rm -f "$output" "${TF_DIR}/${plan}"
            return 0
        else
            status=$?
        fi
        # Never reuse a failed apply's plan: deletion may have succeeded in
        # Google even when polling failed. The next attempt refreshes state,
        # builds a NEW destroy plan, and validates it before applying.
        rm -f "${TF_DIR}/${plan}"
        if [ "$attempt" -eq 3 ] || ! python3 "${REPO_ROOT}/scripts/transient-terraform-error.py" "$output"; then
            rm -f "$output"
            return "$status"
        fi
        warn "Temporary Google API connection failure during deletion; refreshing state and rebuilding the destroy plan (attempt $((attempt + 1))/3)."
        sleep "$((attempt * 5))"
    done
}

prepare_database_destroy() {
    local children address
    local -a addresses=()
    children="$(terraform -chdir="$TF_DIR" state pull | python3 "${REPO_ROOT}/scripts/destroy-state.py" "$GCP_PROJECT" "$PREFIX")" || return 1
    while IFS= read -r address; do
        [ -z "$address" ] || addresses+=("$address")
    done <<< "$children"
    if [ "${#addresses[@]}" -gt 0 ]; then
        log "The whole lab SQL instance will be deleted; skipping separate database/role drops"
        # ABANDON is stored in Terraform state. This also handles deployments made
        # before that policy was configured, without applying/recreating infrastructure.
        # The parent remains tracked until its actual deletion succeeds.
        terraform -chdir="$TF_DIR" state rm -lock-timeout=60s "${addresses[@]}"
    fi
}

wait_for_cloud_run_ip_release() {
    local timeout="${TEARDOWN_VPC_RELEASE_TIMEOUT_SECONDS:-9000}"
    local interval="${TEARDOWN_POLL_INTERVAL_SECONDS:-60}"
    local started=$SECONDS remaining addresses
    [[ "$timeout" =~ ^[0-9]+$ ]] && [ "$timeout" -gt 0 ] || die "Invalid VPC release timeout"
    [[ "$interval" =~ ^[0-9]+$ ]] && [ "$interval" -gt 0 ] || die "Invalid VPC polling interval"
    while true; do
        # A failed API call must never be interpreted as an empty resource list.
        addresses="$(gcloud_safe compute addresses list --filter="purpose=SERVERLESS AND subnetwork~/${PREFIX}-" --format='value(name)')" || return 1
        [ -n "$addresses" ] || return 0
        remaining=$((timeout - (SECONDS - started)))
        [ "$remaining" -gt 0 ] || die "Timed out waiting for Cloud Run IP release. No APIs were disabled; rerun teardown.sh --yes to resume."
        log "Waiting for Cloud Run to release ${addresses//$'\n'/, } (${remaining}s remaining; Google can take 1-2 hours)"
        if [ "$interval" -lt "$remaining" ]; then sleep "$interval"; else sleep "$remaining"; fi
    done
}

destroy_lab_infrastructure() {
    local state address
    local -a targets=()
    prepare_database_destroy
    state="$(terraform -chdir="$TF_DIR" state list)" || return 1
    while IFS= read -r address; do
        case "$address" in
            module.application.*|module.application\[*|module.control_plane.*|module.control_plane\[*|module.load_generator.*|module.load_generator\[*|module.database.*)
                targets+=("-target=$address") ;;
        esac
    done <<< "$state"
    if [ "${#targets[@]}" -gt 0 ]; then
        log "Deleting workloads and Cloud SQL before waiting for the network reservation"
        destroy_phase_with_recovery workloads-destroy.tfplan "${targets[@]}"
    fi
    wait_for_cloud_run_ip_release
    log "Cloud Run IPs released; destroying the remaining lab infrastructure"
    # Refresh credentials after a potentially long wait, and use a new plan so
    # reruns after partial deletion are safe. This is always a destroy operation.
    destroy_phase_with_recovery destroy.tfplan
}

check_leftovers() {
    local label="$1" out
    shift
    if ! out="$("$@")"; then
        leftovers="${leftovers}${label}: verification failed"$'\n'
        warn "Could not verify ${label}; refusing to report successful cleanup"
        return 0
    fi
    if [ -n "$out" ]; then
        leftovers="${leftovers}${label}: ${out//$'\n'/, }"$'\n'
        echo "   LEFT  ${label}: ${out//$'\n'/, }"
    else
        echo "   none  ${label}"
    fi
}
