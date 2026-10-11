#!/usr/bin/env bash
# Read-only checks before provisioning. Makes no changes.
source "$(dirname "$0")/lib.sh"

fail=0
check() { if "$@"; then :; else fail=1; fi; }

log "1. Authorised account and project"
token="$(gcloud_safe auth print-access-token 2>/dev/null || true)"
[ -n "$token" ] || die "No valid credentials for ${GCP_ACCOUNT}; run: $GCLOUD auth login ${GCP_ACCOUNT}"
number="$(gcloud_safe projects describe "$GCP_PROJECT" --format='value(projectNumber)')"
log "2. Project number: ${number}"
[ "$number" = "$GCP_PROJECT_NUMBER" ] || die "Project number mismatch: expected ${GCP_PROJECT_NUMBER}, got ${number}"

log "3. Billing"
billing="$(gcloud_safe billing projects describe "$GCP_PROJECT" --format='value(billingEnabled,billingAccountName)')"
echo "   ${billing}"
[[ "$billing" == True* ]] || die "Billing is not enabled"

log "4. Enabled APIs (Terraform enables the missing ones, never disables any)"
gcloud_safe services list --enabled --format='value(config.name)' | sort | tr '\n' ' ' | fold -w 160; echo

log "4b. Caller permissions"
perms='["iam.serviceAccounts.create","iam.serviceAccounts.actAs","resourcemanager.projects.setIamPolicy","compute.instances.create","compute.instanceGroupManagers.create","compute.autoscalers.create","compute.globalForwardingRules.create","compute.forwardingRules.create","cloudsql.instances.create","run.services.create","secretmanager.secrets.create","artifactregistry.repositories.create","serviceusage.services.enable","datastore.databases.create","monitoring.dashboards.create"]'
granted="$(curl -fsS -X POST -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
    "https://cloudresourcemanager.googleapis.com/v1/projects/${GCP_PROJECT}:testIamPermissions" \
    -d "{\"permissions\": ${perms}}" | python3 -c 'import json,sys; print(" ".join(json.load(sys.stdin).get("permissions", [])))')"
for p in $(echo "$perms" | tr -d '[]"' | tr ',' ' '); do
    if [[ " $granted " == *" $p "* ]]; then echo "   ok      $p"; else echo "   MISSING $p"; fail=1; fi
done
unset token

log "5. Quotas in ${GCP_REGION}"
gcloud_safe compute regions describe "$GCP_REGION" --format=json | python3 -c '
import json, sys
need = {"CPUS": 6, "N1_CPUS": 5, "E2_CPUS": 2, "INSTANCES": 7, "IN_USE_ADDRESSES": 2, "SSD_TOTAL_GB": 20, "DISKS_TOTAL_GB": 80}
for q in json.load(sys.stdin)["quotas"]:
    if q["metric"] in need:
        m, used, limit = q["metric"], q["usage"], q["limit"]
        verdict = "ok" if limit - used >= need[m] else "INSUFFICIENT"
        print("   %-18s usage %5.0f / %6.0f   need %3d   %s" % (m, used, limit, need[m], verdict))'

log "6. n1-standard-1 in ${GCP_ZONE}"
gcloud_safe compute machine-types describe n1-standard-1 --zone="$GCP_ZONE" --format='value(name,guestCpus,memoryMb)'

log "7. Name conflicts with prefix ${PREFIX}"
conflicts="$(gcloud_safe compute instances list --filter="name~^${PREFIX}" --format='value(name)' 2>/dev/null; \
             gcloud_safe compute networks list --filter="name~^${PREFIX}" --format='value(name)' 2>/dev/null)"
if [ -n "$conflicts" ]; then
    if [ -f "${TF_DIR}/terraform.tfstate" ]; then echo "   existing lab resources (managed by this state):"; else warn "Unmanaged resources with the lab prefix exist:"; fail=1; fi
    echo "$conflicts" | sed 's/^/     /'
else
    echo "   none"
fi

log "8. Cost estimate: see docs/cost-and-cleanup.md (idle ≈ \$0.29/h, peak with 5 VMs ≈ \$0.54/h)"
[ "$fail" -eq 0 ] && log "Preflight passed" || die "Preflight found blockers (see above)"
