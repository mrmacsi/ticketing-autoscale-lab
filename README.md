# Ticketing Autoscale Lab

A real Google Cloud autoscaling laboratory: a Laravel ticketing API on a Compute Engine Managed Instance Group behind an external Application Load Balancer, a separate Go load generator producing open-loop synthetic customers, and a Cloud Run control plane with a Vue dashboard that shows the **actual** VMs, CPU, traffic, latency and ticket inventory as the autoscaler reacts.

Nothing on the dashboard is simulated: instances come from the Compute Engine API, CPU from Cloud Monitoring (with its real sample time), traffic from real HTTP responses, inventory from PostgreSQL.

```text
apps/ticketing-api   Laravel 13 API (PHP 8.4, nginx, PHP-FPM, PostgreSQL)
apps/control-api     Node/TypeScript control API: experiments, telemetry, SSE, reports
apps/dashboard       Vue 3 + TypeScript + Tailwind + ECharts
load-generator       Go traffic generator agent (open-loop, Firestore-controlled)
terraform            modules/ + environments/autoscaling-lab (root module and local state)
scripts              preflight, plan/apply/destroy, build/push, deploy, verify, load tests, monitoring
docs                 architecture, deployment, experiment guide, cost & cleanup, failure scenarios, contracts
```

## Quick start (cloud)

```bash
./scripts/gcp-preflight.sh                     # read-only checks
./scripts/approve.sh 5 "<monthly budget>"      # only after the account owner approved
./scripts/deploy.sh                            # foundation → images → workloads → verify
./scripts/dashboard.sh                         # http://localhost:8090 (authenticated proxy)
```

Run experiments from the dashboard, or:

```bash
./scripts/start-load-test.sh B                 # increasing traffic (autoscaling)
./scripts/start-load-test.sh C                 # flash sale (correctness under contention)
./scripts/stop-load-test.sh                    # hard stop
./scripts/monitor-scaling.sh                   # raw MIG/autoscaler/LB state from gcloud
./scripts/smoke-test.sh                        # one purchase through the load balancer
```

Destroy everything this lab created (and nothing else): the **Destroy everything in Google Cloud** button in the dashboard, or

```bash
./scripts/teardown.sh --yes
```

## Local (no cloud, no cost)

```bash
docker compose up -d --build                   # Postgres, Firestore emulator, API, control plane, generator
open http://localhost:18081                    # dashboard (dev auth; GCP telemetry shown as unavailable)
./scripts/concurrency-test.sh                  # 1,000 purchase attempts vs 100 tickets over real HTTP
docker compose down -v
```

## Tests

```bash
docker run -d --name tsl-pg-test -e POSTGRES_USER=lab -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=ticketing_test -p 55432:5432 postgres:16-alpine
./scripts/build.sh                             # all suites + local image builds
```

| Suite | Command | Covers |
|---|---|---|
| Ticketing API | `cd apps/ticketing-api && php artisan test` | reservations, sold out, last ticket, multi-process concurrency on real PostgreSQL, idempotency, expiry, delayed/declined payment, ticket issuance, rollback, admin auth |
| Control API | `cd apps/control-api && npm test` | lifecycle state machine, overlap/confirmation guards, event detection & attribution, freshness, aggregation, reports, auth |
| Dashboard | `cd apps/dashboard && npm test` | live instance count, delayed/missing telemetry, counts, auth enforcement, no invented events, SSE parser, validation |
| Generator | `cd load-generator && go test -race ./...` | requested vs achieved rate, open loop, saturation, randomness/seed, duration, cancellation, recovery, aggregation |
| Terraform | `terraform -chdir=terraform/environments/autoscaling-lab test` (+ modules) | autoscaler bounds/CPU, MIG, health checks, private VMs, no public DB, firewall, approved max |
| Cloud | `./scripts/verify.sh` | bootstrap, LB health, routing, admin isolation, autoscaler config, Cloud Run auth, generator heartbeat, IAM |

## Documentation

* [Project guide — everything in one document](PROJECT_GUIDE.md)

* [Architecture and decisions](docs/architecture.md)
* [Deployment](docs/deployment.md)
* [Experiment guide](docs/experiment-guide.md)
* [Cost and cleanup](docs/cost-and-cleanup.md)
* [Failure scenarios](docs/failure-scenarios.md)
* [Component contracts](docs/contracts.md)
* [Implementation checklist](docs/implementation-checklist.md)
