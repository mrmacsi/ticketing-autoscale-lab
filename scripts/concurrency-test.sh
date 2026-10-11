#!/usr/bin/env bash
# Real-HTTP oversell test with the Go generator: 1000 purchase attempts against 100 tickets.
# Local: ./scripts/concurrency-test.sh            (docker compose stack)
# Cloud: ./scripts/start-load-test.sh C           (same scenario through the control plane)
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"
event="$("$(dirname "$0")/seed.sh" 100 | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["event"]["id"])')"
cfg="$(mktemp)"
python3 - "$event" > "$cfg" <<'PY'
import json, sys
c = json.load(open("load-generator/examples/flash-sale.json"))
c["eventId"] = sys.argv[1]
print(json.dumps(c))
PY
(cd load-generator && go run ./cmd/loadgen run --config "$cfg" --target http://localhost:8080 --summary --quiet)
rm -f "$cfg"
docker compose exec -T postgres psql -U postgres -d ticketing -c \
    "SELECT name, capacity, held, sold, (SELECT count(*) FROM tickets t WHERE t.category_id = c.id) AS tickets FROM ticket_categories c WHERE event_id = '${event}'"
