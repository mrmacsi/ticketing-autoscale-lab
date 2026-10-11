# Implementation checklist

Status as of 2026-10-09. ✅ done and verified · ⏳ waiting for approval · ⬜ not started

## Phase 1 – Repository and application
- ✅ Laravel 13 ticketing API: events, categories, inventory, availability, reservations, orders, simulated payment, tickets, sales totals
- ✅ PostgreSQL schema with CHECK constraints, unique idempotency keys, partial unique index (one approved payment per order)
- ✅ Reservation lifecycle `active → payment_processing → consumed | expired`, background sweeper, payment grace period
- ✅ Fault injection (test/demo only), synthetic CPU endpoint (disabled by default)
- ✅ Docker image: nginx + PHP-FPM (static pool) + sweeper + guest CPU sampler under supervisord; secrets from Secret Manager at start
- ✅ 29 PHPUnit tests on real PostgreSQL 16 incl. a 60-process concurrency race

## Phase 2 – Terraform
- ✅ Modules: networking, database (PSC), application-mig, load-balancer, load-generator, control-plane; root in `terraform/environments/autoscaling-lab`
- ✅ `terraform validate`; `terraform test` with mocked providers (root + 3 modules)
- ✅ Real plan against the project: 69 to create, 0 to change, 0 to destroy
- ✅ Applied to `contentpulse-vertex-ai` (approved: max 5 VMs, budget alert 50)

## Phase 3 – Traffic generator
- ✅ Go agent: open-loop seeded Poisson arrivals, 6 patterns, 4 journey types, duplicates, abandonment, retries with same key
- ✅ Firestore command channel with leases, heartbeat, hard stop, max-duration timer, restart recovery
- ✅ `go test -race` suite (rate accuracy, open loop under slow responses, saturation, cancellation, aggregation, flash sale)

## Phase 4 – Dashboard
- ✅ Vue 3 + TS + Tailwind + ECharts: configuration + presets, controls, server cards, 6 charts, freshness panel, timeline, history, report
- ✅ fetch-based SSE (works with bearer tokens), auth modes, 69 Vitest tests, typecheck, build

## Phase 5 – Integration
- ✅ Control API: experiment state machine, Firestore store, Compute/Monitoring collectors, inventory reader, event detector with attribution, reports, CSV/JSON export (30 Vitest tests)
- ✅ Local end-to-end (docker compose with Firestore emulator): preset C through the control API → 1,000 arrivals, 100 sold, 900 × 409, 0 errors, 0 invariant violations
- ✅ Local real-HTTP concurrency script: 1,000 purchase attempts → 100 sold / 100 tickets
- ✅ GCP integration: MIG, autoscaler, backend health, Cloud Monitoring CPU, Cloud SQL inventory, Firestore generator channel, Cloud Run IAM

## Phase 6 – Validation (2026-10-09, europe-west2-b)
- ✅ `verify.sh`: healthy VM, LB backend healthy, routing + `X-Served-By`, admin API 403 via LB, autoscaler ON (1–5, 60 %, init 60 s, stabilization 240 s), Cloud Run ready, anonymous access 403, generator heartbeat
- ✅ Preset B: 31,662 arrivals / 67,219 requests, 0 % HTTP errors, p95 252 ms; autoscaler 1→2 (23:07), 2→3 (23:10), 3→4 (23:13); scale-in 4→1 (23:18–23:19), VMs removed by 23:21; 0 invariant violations
- ✅ Preset C: exactly 100 of 100 tickets sold, 0 invariant violations (one 1-vCPU VM times out part of a 40/s burst — documented capacity limit)
- ⬜ Cleanup test (`terraform-destroy.sh`) — run when you are finished with the lab

## Decisions log
See `architecture.md#key-decisions`. Notable deviations from the brief:
- Go generator instead of k6 (allowed by the brief).
- Control API in Node/TypeScript rather than Laravel (SSE + pollers).
- Cloud SQL via Private Service Connect instead of VPC peering (missing `servicenetworking` permission; also avoids the peering range).
- Project-level IAM bindings with resource-name conditions (no resource-level setIamPolicy permission).
- Terraform root module lives in `terraform/environments/autoscaling-lab/` (dedicated state location requested), modules in `terraform/modules/`.
- Dashboard access through `gcloud run services proxy` (Cloud Run IAM) by default; Google Sign-In mode is available but needs a manually created OAuth client.
