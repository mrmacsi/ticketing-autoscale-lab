#!/usr/bin/env bash
# Removes EVERYTHING the lab created in Google Cloud and returns the project to its pre-lab state.
# Usage: teardown.sh --yes        (also triggered by the dashboard's "Destroy lab" button)
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/cleanup-lib.sh"
[ "${1:-}" = "--yes" ] || die "Refusing to run without --yes"

# APIs that were NOT enabled in the project before the lab (recorded by the first preflight).
# Core APIs that already existed (compute, logging, monitoring, datastore, …) are left alone, and so
# are iam / cloudresourcemanager, which gcloud itself needs to manage the project.
LAB_APIS="sqladmin.googleapis.com run.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com firestore.googleapis.com iap.googleapis.com billingbudgets.googleapis.com redis.googleapis.com"

# Memorystore instances of the waiting room. With the Redis API off none can exist (and listing would
# fail), so report none; a failed API call is still a failed verification.
redis_instances() {
    local enabled
    enabled="$(gcloud_safe services list --enabled --filter="config.name=redis.googleapis.com" --format='value(config.name)')" || return 1
    [ -n "$enabled" ] || return 0
    gcloud_safe redis instances list --region="$GCP_REGION" --filter="name~/instances/${PREFIX}" --format='value(name)'
}

step() { printf '\n\033[1;35m### %s\033[0m\n' "$*"; }

step "1/6 Stopping any running traffic"
if gcloud_safe run services describe "${PREFIX}-control" --region="$GCP_REGION" --format='value(status.url)' >/dev/null 2>&1; then
    "$(dirname "$0")/control.sh" POST /api/reset >/dev/null 2>&1 && echo "experiment reset, generator stopped" || echo "control plane did not answer; continuing"
else
    echo "control plane already gone"
fi

step "2/6 terraform destroy (every resource in terraform/environments/autoscaling-lab state)"
tf init -input=false >/dev/null
destroy_lab_infrastructure

step "3/6 Removing the temporary roles/datastore.owner grant made for the deployment"
gcloud_safe projects remove-iam-policy-binding "$GCP_PROJECT" --member="user:${GCP_ACCOUNT}" \
    --role=roles/datastore.owner --condition=None --format='value(etag)' >/dev/null 2>&1 \
    && echo "removed" || echo "not present"

step "4/6 Verifying that nothing named ${PREFIX} remains"
leftovers=""
check() { check_leftovers "$@"; }
f="name~^${PREFIX}"
check "VM instances"          gcloud_safe compute instances list --filter="$f" --format='value(name)'
check "instance groups"       gcloud_safe compute instance-groups list --filter="$f" --format='value(name)'
check "instance templates"    gcloud_safe compute instance-templates list --filter="$f" --format='value(name)'
check "health checks"         gcloud_safe compute health-checks list --filter="$f" --format='value(name)'
check "backend services"      gcloud_safe compute backend-services list --filter="$f" --format='value(name)'
check "forwarding rules"      gcloud_safe compute forwarding-rules list --filter="$f" --format='value(name)'
check "IP addresses"          gcloud_safe compute addresses list --filter="$f" --format='value(name)'
check "firewall rules"        gcloud_safe compute firewall-rules list --filter="$f" --format='value(name)'
check "VPC networks"          gcloud_safe compute networks list --filter="$f" --format='value(name)'
check "subnets"               gcloud_safe compute networks subnets list --filter="$f" --format='value(name)'
check "Cloud Run internal IPs" gcloud_safe compute addresses list --filter="subnetwork~/${PREFIX}-" --format='value(name)'
check "disks"                 gcloud_safe compute disks list --filter="$f" --format='value(name)'
check "Cloud SQL"             gcloud_safe sql instances list --filter="$f" --format='value(name)'
check "Memorystore Redis"     redis_instances
check "Cloud Run"             gcloud_safe run services list --region="$GCP_REGION" --filter="metadata.name~^${PREFIX}" --format='value(metadata.name)'
check "Artifact Registry"     gcloud_safe artifacts repositories list --location="$GCP_REGION" --filter="name~${PREFIX}" --format='value(name)'
check "secrets"               gcloud_safe secrets list --filter="name~${PREFIX}" --format='value(name)'
check "Firestore databases"   gcloud_safe firestore databases list --format='value(name)' --filter="name~${PREFIX}"
check "service accounts"      gcloud_safe iam service-accounts list --filter='email~^tsl-' --format='value(email)'
check "IAM bindings"          gcloud_safe projects get-iam-policy "$GCP_PROJECT" --flatten='bindings[].members' \
                                  --filter="bindings.members~tsl- OR bindings.condition.title~^${PREFIX}" --format='value(bindings.role)'
[ -z "$leftovers" ] || die "Lab resources remain or verification failed (see above); not disabling APIs"

step "5/6 Disabling the APIs the lab enabled"
for api in $LAB_APIS; do
    if gcloud_safe services list --enabled --filter="config.name=${api}" --format='value(config.name)' | grep -q .; then
        gcloud_safe services disable "$api" >/dev/null 2>&1 && echo "   disabled  $api" || echo "   KEPT      $api (another enabled service depends on it)"
    else
        echo "   already off  $api"
    fi
done

step "6/6 Local cleanup"
rm -f "${TF_DIR}/images.auto.tfvars.json" "${TF_DIR}/tfplan" "${TF_DIR}/destroy.tfplan" "${TF_DIR}/workloads-destroy.tfplan" "${APPROVAL_FILE}"
echo "removed image pins, saved plans and the spending approval (a new deploy needs a new approval)"

printf '\n\033[1;32mTEARDOWN COMPLETE: no ticketing-scale-lab resources remain in %s.\033[0m\n' "$GCP_PROJECT"
echo "Not removable by design: Cloud Logging entries already written (kept 30 days by Google, no charge)."
