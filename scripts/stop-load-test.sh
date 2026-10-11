#!/usr/bin/env bash
# Hard-stops generator traffic for the active experiment (infrastructure keeps running).
source "$(dirname "$0")/lib.sh"
here="$(dirname "$0")"
id="$("$here/control.sh" GET /api/state | python3 -c 'import json,sys; e=json.load(sys.stdin)["experiment"]; print(e["id"] if e and e["state"]=="running" else "")')"
[ -n "$id" ] || die "No running experiment"
"$here/control.sh" POST "/api/experiments/${id}/stop" >/dev/null
log "Stop sent for ${id}"
