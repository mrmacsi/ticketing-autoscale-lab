#!/usr/bin/env bash
# Shared helpers. Every Google Cloud call goes through gcloud_safe with the authorised account
# and project passed explicitly, so the global gcloud configuration is never read or changed.
set -euo pipefail

GCLOUD="${GCLOUD:-/opt/homebrew/share/google-cloud-sdk/bin/gcloud}"
GCP_ACCOUNT="${GCP_ACCOUNT:-thebeezindex@gmail.com}"
GCP_PROJECT="${GCP_PROJECT:-contentpulse-vertex-ai}"
GCP_PROJECT_NUMBER="${GCP_PROJECT_NUMBER:-210564622269}"
GCP_REGION="${GCP_REGION:-europe-west2}"
GCP_ZONE="${GCP_ZONE:-europe-west2-b}"
PREFIX="ticketing-scale-lab"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
TF_DIR="${REPO_ROOT}/terraform/environments/autoscaling-lab"
REGISTRY="${GCP_REGION}-docker.pkg.dev/${GCP_PROJECT}/${PREFIX}"

gcloud_safe() {
    "$GCLOUD" --account="$GCP_ACCOUNT" --project="$GCP_PROJECT" --quiet "$@"
}

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

# Fresh, account-specific OAuth token for Terraform in THIS shell process. Never printed or saved.
export_terraform_token() {
    set +x
    GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud_safe auth print-access-token 2>/dev/null || true)"
    if [ -z "$GOOGLE_OAUTH_ACCESS_TOKEN" ]; then
        die "Google Cloud authentication unavailable for ${GCP_ACCOUNT}. Run: $GCLOUD auth login ${GCP_ACCOUNT}"
    fi
    export GOOGLE_OAUTH_ACCESS_TOKEN
    unset GOOGLE_APPLICATION_CREDENTIALS GOOGLE_CREDENTIALS GOOGLE_IMPERSONATE_SERVICE_ACCOUNT
}

tf() {
    export_terraform_token
    terraform -chdir="$TF_DIR" "$@"
}

tf_output() {
    terraform -chdir="$TF_DIR" output -raw "$1" 2>/dev/null || true
}

# Approval gate: billable changes require an approvals file written after the user approved.
APPROVAL_FILE="${TF_DIR}/.approved-budget"
require_approval() {
    [ -f "$APPROVAL_FILE" ] || die "No recorded spending approval. After approval run: ./scripts/approve.sh <max_instances> <monthly_budget>"
    # shellcheck disable=SC1090
    . "$APPROVAL_FILE"
    local requested_max
    requested_max="$(awk -F= '/^max_instances/ {sub(/#.*/,"",$2); gsub(/[ "]/,"",$2); print $2}' "${TF_DIR}/terraform.tfvars")"
    local approved_cap
    approved_cap="$(awk -F= '/^approved_max_instances/ {sub(/#.*/,"",$2); gsub(/[ "]/,"",$2); print $2}' "${TF_DIR}/terraform.tfvars")"
    [ "${requested_max:-0}" -le "${APPROVED_MAX_INSTANCES}" ] || die "max_instances ${requested_max} exceeds the approved ${APPROVED_MAX_INSTANCES}"
    [ "${approved_cap:-0}" -le "${APPROVED_MAX_INSTANCES}" ] || die "approved_max_instances ${approved_cap} exceeds the recorded approval ${APPROVED_MAX_INSTANCES}"
    log "Spending approval on record: max ${APPROVED_MAX_INSTANCES} app VMs, budget ${APPROVED_BUDGET} (approved ${APPROVED_AT})"
}

# The autoscaler resource straight from the Compute API (gcloud has no autoscalers command group).
autoscaler_json() {
    curl -fsS -H "Authorization: Bearer $(gcloud_safe auth print-access-token)" \
        "https://compute.googleapis.com/compute/v1/projects/${GCP_PROJECT}/zones/${GCP_ZONE}/autoscalers/${PREFIX}-app-autoscaler"
}
