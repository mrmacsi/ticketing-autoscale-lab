#!/usr/bin/env bash
# Local stack only: creates a demo event through the private admin API.
# Usage: seed.sh [capacity]
source "$(dirname "$0")/lib.sh"
capacity="${1:-500}"
curl -fsS -X POST http://localhost:8080/api/admin/experiments/prepare \
    -H 'Authorization: Bearer local-admin-token-change-me-0123456789' -H 'Content-Type: application/json' \
    -d "{\"event_name\":\"Local Demo Night\",\"categories\":[{\"name\":\"Standing\",\"price_cents\":4500,\"capacity\":${capacity}}]}"
echo
