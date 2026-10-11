#!/usr/bin/env bash
# Destroys ONLY the resources in this lab's Terraform state, after showing the destroy plan.
# Usage: terraform-destroy.sh [--yes]
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/cleanup-lib.sh"
tf init -input=false >/dev/null
# Preview the recorded scope before confirmation, even after a partial teardown
# left child resources whose parent no longer exists. Actual destroy plans refresh.
tf plan -destroy -refresh=false -input=false -lock-timeout=60s -out=destroy.tfplan
terraform -chdir="$TF_DIR" show -json destroy.tfplan | python3 -c '
import json, sys
plan = json.load(sys.stdin)
gone = [rc["address"] for rc in plan.get("resource_changes", []) if "delete" in rc["change"]["actions"]]
print("\nResources to destroy (%d), all from terraform/environments/autoscaling-lab state:" % len(gone))
for a in gone: print("  " + a)
print("\nNot affected: project APIs stay enabled; nothing outside this state is touched.")'
if [ "${1:-}" != "--yes" ]; then
    read -r -p "Type 'destroy ticketing-scale-lab' to continue: " answer
    [ "$answer" = "destroy ticketing-scale-lab" ] || die "Aborted"
fi
destroy_lab_infrastructure
rm -f "${TF_DIR}/destroy.tfplan"
log "Remaining lab-labelled compute resources (should be empty):"
gcloud_safe compute instances list --filter="labels.lab=${PREFIX}" --format='value(name)'
