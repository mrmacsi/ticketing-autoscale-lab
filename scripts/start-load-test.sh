#!/usr/bin/env bash
# Prepares and starts a preset experiment through the control API (the same path as the UI).
# Usage: start-load-test.sh <A|B|C|D|E|F> [--confirm-expensive]
source "$(dirname "$0")/lib.sh"
preset="${1:?usage: $0 <A|B|C|D|E|F> [--confirm-expensive]}"
confirm=False; [ "${2:-}" = "--confirm-expensive" ] && confirm=True
here="$(dirname "$0")"

config="$("$here/control.sh" GET /api/meta | python3 -c "
import json, sys
meta = json.load(sys.stdin)
c = next(p['config'] for p in meta['presets'] if p['id'] == '${preset}')
c['confirmExpensive'] = ${confirm}
print(json.dumps(c))")"
id="$("$here/control.sh" POST /api/experiments "$config" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
log "Experiment ${id} preparing (waiting for ready)"
for _ in $(seq 1 300); do
    state="$("$here/control.sh" GET /api/state | python3 -c 'import json,sys; e=json.load(sys.stdin)["experiment"]; print(e["state"] if e else "none")')"
    [ "$state" = ready ] && break
    [ "$state" = failed ] && die "Preparation failed; see the dashboard timeline"
    sleep 3
done
[ "$state" = ready ] || die "Experiment not ready after 15 minutes"
"$here/control.sh" POST "/api/experiments/${id}/start" >/dev/null
log "Traffic started for ${id}. Watch: ./scripts/dashboard.sh or ./scripts/monitor-scaling.sh"
echo "$id"
