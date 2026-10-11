#!/usr/bin/env bash
# fmt + validate + plan into a saved plan file, then summarise what would change.
# Usage: terraform-plan.sh [--foundation]   (--foundation: APIs, network, registry, secrets, DB only)
source "$(dirname "$0")/lib.sh"

stage_args=()
if [ "${1:-}" = "--foundation" ]; then stage_args=(-var enable_workloads=false); fi
images="${TF_DIR}/images.auto.tfvars.json"
if [ "${1:-}" != "--foundation" ] && [ ! -f "$images" ]; then
    die "No images.auto.tfvars.json; run ./scripts/build-and-push.sh first (or plan with --foundation)"
fi

terraform fmt -check -recursive "${REPO_ROOT}/terraform" >/dev/null || die "terraform fmt found unformatted files"
tf init -input=false >/dev/null
tf validate
tf plan -input=false -lock-timeout=60s ${stage_args[@]+"${stage_args[@]}"} -out=tfplan
# terraform-apply.sh re-plans in the same mode if a transient failure leaves this plan stale.
printf '%s\n' ${stage_args[@]+"${stage_args[@]}"} > "${TF_DIR}/tfplan.args"

terraform -chdir="$TF_DIR" show -json tfplan | python3 -c '
import json, sys
plan = json.load(sys.stdin)
changes = {}
foreign = []
for rc in plan.get("resource_changes", []):
    actions = [a for a in rc["change"]["actions"] if a != "no-op"]
    if not actions:
        continue
    key = "/".join(actions)
    changes.setdefault(key, []).append(rc["address"])
    after = rc["change"].get("after") or rc["change"].get("before") or {}
    name = str(after.get("name") or after.get("account_id") or after.get("secret_id") or after.get("repository_id") or "")
    if name and "ticketing-scale-lab" not in name and not name.startswith("tsl-") and name not in ("ticketing", "ticketing_app", "lab_observer") and "iam_member" not in rc["type"] and rc["type"] not in ("google_project_service", "random_password", "random_bytes"):
        foreign.append("%s (%s)" % (rc["address"], name))
for action, addresses in sorted(changes.items()):
    print("\n%s (%d):" % (action.upper(), len(addresses)))
    for a in addresses:
        print("  " + a)
if foreign:
    print("\nWARNING: resources without the lab prefix:", *foreign, sep="\n  ")
if any("delete" in k for k in changes):
    print("\nNOTE: the plan deletes or replaces resources; review before applying.")
'
log "Saved plan: ${TF_DIR}/tfplan"
