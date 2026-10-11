#!/usr/bin/env bash
# Post-deployment verification against the real infrastructure. --wait polls until healthy (max 15 min).
source "$(dirname "$0")/lib.sh"
WAIT=false; [ "${1:-}" = "--wait" ] && WAIT=true
app_url="$(tf_output application_url)"
[ -n "$app_url" ] || die "No application_url output; is the environment deployed?"
fail=0
ok() { echo "   ok    $*"; }
bad() { echo "   FAIL  $*"; fail=1; }

log "VM application bootstrap + MIG health"
deadline=$(( $(date +%s) + 900 ))
while true; do
    states="$(gcloud_safe compute instance-groups managed list-instances "${PREFIX}-app-mig" --zone="$GCP_ZONE" \
        --format='value(instance.basename(),status,currentAction,instanceHealth[0].detailedHealthState)')"
    healthy="$(echo "$states" | grep -c 'HEALTHY$' || true)"
    if [ "$healthy" -ge 1 ] || ! $WAIT || [ "$(date +%s)" -gt "$deadline" ]; then break; fi
    echo "   waiting for a healthy VM..."; sleep 15
done
echo "$states" | sed 's/^/   /'
[ "${healthy:-0}" -ge 1 ] && ok "${healthy} healthy application VM(s)" || bad "no healthy application VM"

log "Load balancer backend health"
lb="$(gcloud_safe compute backend-services get-health "${PREFIX}-app-backend" --global --format='value(status.healthStatus[].healthState)' | tr ';' '\n' | grep -c HEALTHY || true)"
[ "$lb" -ge 1 ] && ok "${lb} backend(s) HEALTHY" || bad "no HEALTHY backend"

log "Routing through ${app_url} (new load balancers can take a few minutes to program)"
deadline=$(( $(date +%s) + 600 ))
while true; do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${app_url}/health" || true)"
    if [ "$code" = 200 ] || ! $WAIT || [ "$(date +%s)" -gt "$deadline" ]; then break; fi
    sleep 10
done
[ "$code" = 200 ] && ok "/health 200 (Cloud SQL reachable, schema migrated)" || bad "/health returned ${code}"
served="$(curl -s -D - -o /dev/null --max-time 5 "${app_url}/api/events" | awk -F': ' 'tolower($1)=="x-served-by" {print $2}' | tr -d '\r')"
[ -n "$served" ] && ok "/api/events served by ${served}" || bad "no X-Served-By header"
admin="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${app_url}/api/admin/settings" || true)"
[ "$admin" = 403 ] && ok "admin API blocked through the load balancer (403)" || bad "admin API through LB returned ${admin} (expected 403)"

log "Autoscaler configuration"
autoscaler_json | python3 -c '
import json, sys
a = json.load(sys.stdin); p = a["autoscalingPolicy"]
print("   mode=%s min=%s max=%s cpu_target=%s init=%ss stabilization=%s status=%s" % (p.get("mode"), p["minNumReplicas"], p["maxNumReplicas"], p["cpuUtilization"]["utilizationTarget"], p.get("coolDownPeriodSec"), p.get("stabilizationPeriodSec", "default"), a.get("status")))
' && ok "autoscaler readable" || bad "autoscaler not readable"

log "Control plane"
ready="$(gcloud_safe run services describe "${PREFIX}-control" --region="$GCP_REGION" --format='value(status.conditions[0].status)')"
[ "$ready" = True ] && ok "Cloud Run service ready" || bad "Cloud Run service not ready"
anon="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$(tf_output dashboard_url)/api/state" || true)"
[ "$anon" = 403 ] || [ "$anon" = 401 ] && ok "anonymous dashboard access refused (${anon})" || bad "anonymous dashboard access returned ${anon}"
gen="$("$(dirname "$0")/control.sh" GET /api/state | python3 -c 'import json,sys; g=json.load(sys.stdin)["traffic"]["generator"]; print(g["state"], g["heartbeatAgeSeconds"])' || true)"
[[ "$gen" == idle* || "$gen" == running* ]] && ok "load generator heartbeat: ${gen}" || bad "load generator not reporting (${gen})"

log "Service-account roles"
gcloud_safe projects get-iam-policy "$GCP_PROJECT" --flatten='bindings[].members' \
    --filter="bindings.members~serviceAccount:tsl-" --format='table(bindings.members.split(":").slice(1:).flatten():label=MEMBER,bindings.role,bindings.condition.title)'

[ "$fail" -eq 0 ] && log "Verification passed" || die "Verification failed"
