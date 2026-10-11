#!/usr/bin/env bash
# Prints the real MIG / autoscaler / load-balancer state every N seconds straight from Compute Engine.
source "$(dirname "$0")/lib.sh"
INTERVAL="${1:-10}"
while true; do
    clear
    date -u +"%Y-%m-%dT%H:%M:%SZ"
    gcloud_safe compute instance-groups managed describe "${PREFIX}-app-mig" --zone="$GCP_ZONE" \
        --format='table(targetSize,status.isStable,status.autoscaler)'
    autoscaler_json | python3 -c '
import json, sys
a = json.load(sys.stdin); p = a["autoscalingPolicy"]
print("autoscaler min=%s max=%s cpu_target=%s recommended=%s status=%s %s" % (p["minNumReplicas"], p["maxNumReplicas"],
      p["cpuUtilization"]["utilizationTarget"], a.get("recommendedSize"), a.get("status"), [d.get("message") for d in a.get("statusDetails", [])]))' || true
    gcloud_safe compute instance-groups managed list-instances "${PREFIX}-app-mig" --zone="$GCP_ZONE" \
        --format='table(instance.basename():label=INSTANCE,status,currentAction,instanceHealth[0].detailedHealthState:label=HEALTH)'
    gcloud_safe compute backend-services get-health "${PREFIX}-app-backend" --global \
        --format='table(status.healthStatus[].instance.basename():label=BACKEND,status.healthStatus[].healthState:label=LB_HEALTH)' 2>/dev/null || true
    sleep "$INTERVAL"
done
