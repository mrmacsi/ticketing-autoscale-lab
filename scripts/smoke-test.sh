#!/usr/bin/env bash
# Exercises the public ticketing API end to end: browse → availability → reserve → order → pay.
# Usage: smoke-test.sh [base_url]   (default: the deployed load balancer, or http://localhost:8080)
source "$(dirname "$0")/lib.sh"
BASE="${1:-$(tf_output application_url)}"; BASE="${BASE:-http://localhost:8080}"
j() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
key() { echo "smoke-$(date +%s)-$RANDOM-$1"; }

curl -fsS "$BASE/health" >/dev/null && log "health ok"
event="$(curl -fsS "$BASE/api/events" | j 'd["data"][0]["id"] if d["data"] else ""')"
[ -n "$event" ] || die "No event yet: prepare an experiment first (dashboard or start-load-test.sh)"
cat_id="$(curl -fsS "$BASE/api/events/$event/availability" | j 'next((c["id"] for c in d["data"]["categories"] if c["available"] > 0), "")')"
[ -n "$cat_id" ] || die "Event is sold out"
res="$(curl -fsS -X POST "$BASE/api/reservations" -H "Idempotency-Key: $(key r)" -H 'Content-Type: application/json' \
    -d "{\"category_id\":\"$cat_id\",\"quantity\":1,\"customer_ref\":\"smoke-test\"}" | j 'd["data"]["id"]')"
log "reserved $res"
order="$(curl -fsS -X POST "$BASE/api/orders" -H "Idempotency-Key: $(key o)" -H 'Content-Type: application/json' \
    -d "{\"reservation_id\":\"$res\",\"customer_ref\":\"smoke-test\"}" | j 'd["data"]["id"]')"
log "order $order"
status="$(curl -s -o /tmp/smoke-pay.json -w '%{http_code}' -X POST "$BASE/api/orders/$order/simulate-payment" -H "Idempotency-Key: $(key p)")"
log "payment HTTP $status: $(python3 -c 'import json; d=json.load(open("/tmp/smoke-pay.json")); print(d.get("data",{}).get("status"), len(d.get("data",{}).get("tickets",[])), "ticket(s)")')"
rm -f /tmp/smoke-pay.json
[ "$status" = 200 ] || [ "$status" = 402 ] || die "unexpected payment status $status"
log "Smoke test passed (402 is a simulated decline, an expected outcome)"
