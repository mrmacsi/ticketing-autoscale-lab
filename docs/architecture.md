# Architecture

```text
                       CONTROL PLANE (Cloud Run, 1 instance)
   browser ──gcloud run services proxy (IAM)──▶ control-api + Vue dashboard
                                                │  │  │  │
             Compute Engine API (MIG, autoscaler, backend health) ◀──┘  │  │  │
             Cloud Monitoring API (VM CPU, delayed samples) ◀───────────┘  │  │
             Cloud SQL via PSC endpoint, read-only `lab_observer` ◀───────┘  │
             VM private IPs: /instance-info, /api/admin/* (Direct VPC egress) ◀┘
                                                │
                                   Firestore (named DB `ticketing-scale-lab`)
                                   generator/command, generator/status,
                                   experiments/{id}/traffic|events|series
                                                │
                       LOAD PLANE               ▼
   load-generator VM (Go agent, e2-small) ──real HTTP──▶ external Application Load Balancer
                                                              │ (EXTERNAL_MANAGED, :80)
                                       ┌──────────────────────┼──────────────────────┐
                                       ▼                      ▼                      ▼
                           app VM (COS, n1-standard-1)   app VM ...             app VM ...
                           nginx + PHP-FPM + Laravel     ← Managed Instance Group + CPU autoscaler
                                       └──────────────────────┼──────────────────────┘
                                                              ▼
                                        Cloud SQL PostgreSQL 16 (private, PSC, TLS)
```

## Components

| Component | Where | Responsibility |
|---|---|---|
| `apps/ticketing-api` | MIG VMs (Container-Optimized OS, Docker, host network) | Laravel 13 REST API, inventory correctness, simulated payments, reservation expiry sweeper, `/instance-info`, `X-Served-By` header |
| `load-generator` | Dedicated VM | Go agent: open-loop arrivals, synthetic customer journeys, 1 s metric buckets to Firestore, hard stop, max duration |
| `apps/control-api` | Cloud Run | Experiment state machine, telemetry collectors, SSE, reports, autoscaler overrides, serves the dashboard |
| `apps/dashboard` | Bundled into the control-api image | Vue 3 + TS + Tailwind + ECharts UI |
| `terraform/` | Local state in `terraform/environments/autoscaling-lab/` | All infrastructure |

The binding interfaces between them are in [`contracts.md`](contracts.md).

## Key decisions

| Decision | Why |
|---|---|
| **Go generator instead of k6** | Explicitly allowed by the brief. One binary gives open-loop Poisson arrivals, VU-capacity accounting (dropped arrivals), per-instance attribution from `X-Served-By`, and direct 1 s Firestore buckets with no k6 output parsing. |
| **Control API in Node/TypeScript** (not Laravel) | Long-lived SSE connections and concurrent pollers fit an event loop; PHP-FPM would pin a worker per SSE client. Same language as the dashboard; shared contract types. |
| **Firestore as command bus + history** | Durable, no public generator endpoint: the generator has no inbound ports and only listens to `generator/command`. Ownership is a Firestore transaction with a renewable lease. |
| **Cloud Run with min = max = 1, CPU always allocated** | One owner of the telemetry loop and SSE fan-out; telemetry keeps flowing with no browser open. On restart it resumes the active experiment from Firestore. |
| **Cloud SQL through Private Service Connect** | No public IP. The deploying account lacks `servicenetworking.services.addPeering`, so PSC (an internal forwarding rule) replaces VPC peering. TLS is required (`ENCRYPTED_ONLY`). |
| **COS + cloud-init + systemd** (not the deprecated container declaration) | VM pulls the immutable image tag from Artifact Registry with its own service account and starts it; no SSH. |
| **Secrets fetched at container start** | The entrypoint reads Secret Manager with the VM's metadata token. Instance metadata contains secret *names* only. |
| **Project-level IAM with conditions** | The deploying account can set project IAM but not resource IAM, so per-resource access (secrets, repository, Firestore DB, autoscaler/MIG, Cloud Run invoker) uses IAM conditions on resource names. |
| **Readiness ≠ liveness** | LB uses `/health` (DB reachable, schema migrated); autohealing uses `/health/live` so a database blip never triggers a VM recreation loop. |
| **Migrations at boot under a PostgreSQL advisory lock** | Every VM can boot independently; exactly one applies pending migrations. |
| **HTTP (no TLS) on the load balancer** | No domain for a managed certificate; traffic is synthetic. The admin API is never reachable through the LB (nginx denies the GFE ranges) and also needs a bearer token. |

## Inventory correctness

* `ticket_categories(capacity, held, sold)` with `CHECK (held >= 0 AND sold >= 0 AND held + sold <= capacity)`.
* Holding tickets is one conditional statement: `UPDATE … SET held = held + q WHERE id = ? AND capacity - held - sold >= q`. PostgreSQL re-evaluates the predicate after waiting for a concurrent writer's row lock, so the last ticket can be taken once.
* Reservation states `active → payment_processing → consumed`, or `→ expired`; held inventory moves to `sold` or back to available in the same transaction as the state change.
* Idempotency keys are unique columns on reservations, orders and payment attempts; one order per reservation; at most one approved payment per order (partial unique index).
* Payment uses two short transactions around the simulated provider delay (no row locks held while waiting). A failure in the second transaction is compensated; a crashed process leaves `payment_processing`, which the sweeper releases after a grace period.
* Lock order everywhere: order → reservation → attempt → category. The expiry sweeper is serialised across instances with `pg_try_advisory_xact_lock` and uses `FOR UPDATE SKIP LOCKED`.
* Invariant check (`held = Σ active/processing quantities`, `sold = tickets issued = Σ consumed quantities`, bounds) runs in one snapshot every 2 s from the control plane; violations are shown and reported (expected: 0).

## Telemetry

| Source | Interval | What | Freshness rule |
|---|---|---|---|
| Generator buckets (Firestore listener) | 1 s | requested vs achieved arrivals, HTTP status classes, latency histogram, per-instance counts | live ≤ 3 s, stale > 15 s |
| PostgreSQL (read-only role) | 2 s | capacity / held / sold / expired / invariants | live ≤ 6 s |
| Compute API (MIG, managed instances, autoscaler) | 5 s | real instances, actions, health, target/recommended size | live ≤ 15 s |
| `/instance-info` on private IPs | 5 s | guest CPU from `/proc/stat` (real-time, *not* what the autoscaler uses), app version, readiness | per sample time |
| Backend service `getHealth` | 10 s | LB view of each backend | live ≤ 30 s |
| Cloud Monitoring `instance/cpu/utilization` | 20 s poll | the autoscaler's metric; 60 s samples visible 1–4 min late | never "live": *delayed* ≤ 5 min, then *stale* |

Every value carries its own timestamp; missing sources render as N/A with a missing/stale badge, never as 0.
Only traffic is written per second; infrastructure and inventory series are flushed in 10 s chunks. HTTP requests are never written individually.

## Timeline and attribution

Events are produced only by diffing successive real observations (`src/telemetry/detector.ts`): MIG target-size changes, recommended-size changes, autoscaler status messages, instance created / RUNNING / passed health check / HEALTHY in LB / first request served / termination started / removed, CPU target crossings (stamped with the Cloud Monitoring sample time), traffic milestones, sold out, first expiries, invariant violations.

Capacity changes are attributed:

* **baseline** — the control plane raised `min` (to honour "initial servers") and the MIG grew to that minimum;
* **operator** — the control plane lowered `max` below the current size;
* **autoscaler** — everything else, i.e. the autoscaler's own CPU-driven decision.

## Runtime overrides vs Terraform

Terraform owns the autoscaler baseline. During an experiment the control plane may patch `min`, `max` (≤ `approved_max_instances`), CPU target, initialisation period and stabilization period. Overrides are:

* recorded as `config` timeline events and shown as an "overrides active" badge;
* reverted automatically when an experiment completes, fails or is reset;
* reverted by `terraform apply` as well (Terraform does **not** ignore these fields, so drift shows in `terraform plan`).

The MIG's `target_size` *is* ignored by Terraform after creation because the autoscaler owns it.

## What GCP does not let us control precisely

* Scale-out timing: CPU samples are 60 s aggregates, the autoscaler waits for the initialisation period of new VMs, VM boot + image pull + health checks take ~1–3 min.
* Scale-in: the autoscaler uses the stabilization period (configured, default 10 min when unset) and acts on its own schedule; the dashboard observes, it never forces it.
* Cloud Monitoring latency varies (typically 1–4 min).
* The load balancer distributes per request but not perfectly evenly, especially right after a backend becomes healthy.
