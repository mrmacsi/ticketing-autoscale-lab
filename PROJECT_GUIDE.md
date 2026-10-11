# Ticketing Autoscale Lab — Project Guide

One document that explains the whole project: what it is, how every part works, how to run it, and what was proven with it. For deeper detail, each section links to the focused documents in [`docs/`](docs/).

---

## 1. What this project is

A **real** Google Cloud autoscaling laboratory. A small but genuine ticketing application runs on Compute Engine virtual machines behind Google's load balancer. A separate machine sends thousands of synthetic customers at it. A web dashboard shows, live, what Google Cloud actually does:

* how many servers exist, and when the autoscaler adds or removes them;
* their CPU, health and share of the traffic;
* request rate, latency and errors;
* tickets held, sold and expired, and whether inventory ever goes wrong.

Nothing on the dashboard is simulated. Servers come from the Compute Engine API, CPU from Cloud Monitoring (with its real, delayed timestamp), traffic from real HTTP responses, and tickets from the PostgreSQL database.

It answers two questions:

1. **Does the infrastructure scale?** Start with 1 server, raise traffic, and watch the autoscaler add servers when CPU passes 60%, then remove them after traffic stops.
2. **Does the application stay correct under pressure?** Send 1,000 buyers at 100 tickets and prove that no more than 100 can ever be sold.

---

## 2. The big picture

```text
 YOUR MAC                                  GOOGLE CLOUD (project contentpulse-vertex-ai, europe-west2-b)
 ────────                                  ─────────────────────────────────────────────────────────────
 browser ── http://localhost:8090 ──┐
                                    │  scripts/dashboard-proxy.mjs signs requests as thebeezindex@gmail.com
                                    └──────────────▶  Cloud Run: control API + Vue dashboard (1 instance)
 Terraform state + scripts/                              │ reads/controls
 (deploy, verify, teardown)                              ├── Compute Engine API  (servers, autoscaler, LB health)
                                                         ├── Cloud Monitoring    (CPU, delayed 1–4 min)
                                                         ├── Cloud SQL           (inventory, read-only user)
                                                         └── Firestore           (commands, metrics, history)
                                                                   ▲  │
                                                       metrics     │  │ start / stop commands
                                                                   │  ▼
                                             Load-generator VM (Go agent, synthetic customers)
                                                         │ real HTTP
                                                         ▼
                                             External Application Load Balancer (http://<ip>)
                                                         │ URL map
                              /queue/* ┌─────────────────┴──────────────────┐ everything else
                                       ▼                                    │
                          Waiting-room VM (Go, FIFO queue)                  │  (only admitted customers reach
                          admits while active < healthy × 25 ──────────┐     │   checkout: signed X-Queue-Token)
                          reads LB backend health + availability       │     │
                                                                       ▼     ▼
                                       ┌─────────────────┬─────────────────┐
                                       ▼                 ▼                 ▼
                                   app VM 1          app VM 2   …      app VM 5     ← Managed Instance Group
                                   nginx + PHP-FPM + Laravel              + CPU autoscaler (1–5, target 60%)
                                       └─────────────────┼─────────────────┘
                                                         ▼
                                             Cloud SQL PostgreSQL 16 (private, TLS)
```

The three "planes":

| Plane | Parts | Job |
|---|---|---|
| **Control** | Cloud Run control API + dashboard, Firestore | Configure, start and stop experiments; collect telemetry; build reports |
| **Load** | Load-generator VM | Produce synthetic customers with real HTTP requests |
| **Application** | Load balancer, waiting room, instance group of app VMs, Cloud SQL | The system under test |

---

## 3. Repository map

```text
ticketing-autoscale-lab/
├── apps/
│   ├── ticketing-api/     Laravel 13 (PHP 8.4) ticketing REST API + Docker image (nginx, PHP-FPM, sweeper)
│   ├── waiting-room/      Go queue server: FIFO waiting room, admission tokens, LB-health-based capacity
│   ├── control-api/       Node/TypeScript control API: experiments, telemetry, SSE, reports
│   └── dashboard/         Vue 3 + TypeScript + Tailwind + ECharts web UI (bundled into control-api image)
├── load-generator/        Go traffic generator agent
├── terraform/
│   ├── modules/           networking, database, application-mig, load-balancer, load-generator, waiting-room, control-plane
│   └── environments/autoscaling-lab/   root module, tfvars, and the lab's own Terraform state
├── scripts/               preflight, plan/apply, build/push, deploy, verify, load tests, dashboard, teardown
├── docs/                  architecture, deployment, experiment guide, cost & cleanup, failure scenarios, contracts
├── local/ + docker-compose.yml   cost-free local copy of the whole lab
├── README.md              quick start
└── PROJECT_GUIDE.md       this file
```

---

## 4. The ticketing application (`apps/ticketing-api`)

### What it does

A real, small ticketing backend:

| Endpoint | Purpose |
|---|---|
| `GET /api/events`, `GET /api/events/{id}` | Browse events and ticket categories |
| `GET /api/events/{id}/availability` | Approximate remaining tickets |
| `POST /api/reservations` | Hold tickets for a few minutes |
| `POST /api/orders` | Create an order for a held reservation |
| `POST /api/orders/{id}/simulate-payment` | Simulated card payment (configurable delay and failure rate; no real money) |
| `GET /api/orders/{id}`, `GET /api/tickets/{id}` | Issued orders and tickets |
| `GET /api/sales/summary` | Sales totals |
| `GET /health`, `/health/live`, `/instance-info` | Readiness, liveness, safe VM identity and CPU |
| `/api/admin/*` | Private: create events, reset inventory, change settings (VPC + token only) |

Every response carries `X-Served-By: <vm-name>`, so the generator can measure how the load balancer spreads traffic.

### How it keeps tickets correct

Each ticket category stores `capacity`, `held` and `sold`, and PostgreSQL enforces:

```text
held ≥ 0,  sold ≥ 0,  held + sold ≤ capacity
```

* **Taking a ticket is one conditional SQL update:** `held = held + n WHERE capacity − held − sold ≥ n`. PostgreSQL re-checks that condition after waiting for any competing transaction, so two buyers can never both take the last ticket.
* **Reservation states:** `active → payment_processing → consumed`, or `→ expired`. Held tickets become sold, or are released, in the same transaction as the state change.
* **Idempotency:** reservations, orders and payments each have a unique `Idempotency-Key`. Retrying the same request returns the original result (`Idempotent-Replayed: true`); it never sells twice.
* **Payment** runs in two short transactions around the simulated provider's delay, so no locks are held while "the bank" responds. If the second step fails, a compensating step returns the reservation to `active`.
* **Expiry sweeper:** every VM runs `reservations:expire` every 2 seconds. A PostgreSQL advisory lock ensures only one sweep runs at a time across all servers. Expired holds go back on sale.
* **Invariant check:** `held` must equal the active reservations, and `sold` must equal both the consumed reservations and the tickets issued. The control plane runs this check every 2 seconds during experiments. Expected violations: always 0.

Details and test evidence: [`docs/failure-scenarios.md`](docs/failure-scenarios.md).

### How it runs on a VM

Each app VM uses Container-Optimized OS. At boot, cloud-init starts a systemd unit that:

1. pulls the versioned image from Artifact Registry with the VM's own service account;
2. opens the app port in the OS firewall (COS blocks inbound traffic by default);
3. starts the container on the host network.

The container entrypoint reads the DB password, admin token and app key from **Secret Manager** (never baked into the image or VM metadata), caches the config, then supervisord runs:

* nginx (16,384 connections per worker);
* PHP-FPM (32 workers, each with one persistent DB connection);
* the migration task (runs once across all VMs under a PostgreSQL advisory lock);
* the reservation sweeper;
* a guest CPU sampler.

---

## 4a. The waiting room (`apps/waiting-room`)

**Question it answers:** when every server is full, do customers fail, or do they wait their turn?

Without it, a burst piles onto the servers that exist; when one is overloaded its readiness probe times
out and the load balancer has no backend left, so customers get `503`. (Measured: the first fast-scale
run of preset F produced 3,469 × `503 failed_to_pick_backend` in 17 s before the second VM could serve.)

With it:

1. A customer who wants to buy calls `POST /queue/join` (routed by the load balancer's `/queue/*` path rule
   to the waiting-room VM) and gets a place in a **FIFO** line.
2. The waiting room admits people only while `active < healthy servers × active users per server`
   (default 25). "Healthy" is what the **load balancer** reports, read from the Compute API every 2 s, so
   capacity follows the autoscaler: each new server that turns healthy adds 25 places.
3. An admitted customer gets a short-lived **signed token** (HMAC of the lab admin token) and goes through
   the load balancer as usual; **the load balancer picks the server**. The app refuses
   `POST /api/reservations` without a valid token while the room is in force, so nobody skips the line.
4. When the customer finishes, they release their place; abandoned places expire after 60 s.
5. When tickets are gone (availability 0 for longer than the hold time), everyone still waiting is told
   **sold out** without touching an application server.

**How many customers per server?** Two admission modes:

| Mode | Capacity | When to use |
|---|---|---|
| Fixed | healthy servers × a number you set (e.g. 25, measured by load testing) | What Cloudflare Waiting Room ("total active users") and Queue-it ("outflow rate") do; predictable, but only right for one machine type and workload |
| **Adaptive** (preset F) | healthy servers × **a limit per server learned every second** from the busiest server | No magic number: the limit grows while every server has headroom and is cut when any server shows overload |

```text
 every 1 s, per healthy app server                         queue VM
 ┌───────────── app VM ─────────────┐    GET /lab/load    ┌──────────────────────────────────────────┐
 │ nginx stub_status: requests in   │ ──────────────────▶ │ worst signal across servers overloaded?  │
 │ flight, PHP worker count         │    GET /lab/cpu     │ (waiting for a worker > 0, workers busy  │
 │ cpu sampler: VM CPU every 2 s    │ ──────────────────▶ │  ≥ 90 %, or VM CPU ≥ 90 %)               │
 └──────────────────────────────────┘   (nginx only, no   │   yes → limit × 0.75 (at most every 3 s) │
                                         PHP, VPC only)   │ headroom everywhere and room ≥ 80 % full?│
                                                          │   slow start → limit × 1.25, then +1     │
                                                          │ capacity = healthy servers × limit       │
                                                          └──────────────────────────────────────────┘
```

The signals come from nginx, not PHP, so they answer in milliseconds even when every PHP worker is busy,
and they react in 1–2 s instead of the 1–4 minutes Cloud Monitoring CPU needs. "Requests in flight"
rises for any bottleneck (CPU, database, locks), which CPU alone does not. The room only holds servers
back *above* the autoscaler's CPU target, so busy servers still make the autoscaler add servers.
Details, state diagram and measurements: [`docs/waiting-room.md` §2a](docs/waiting-room.md).

Waiting customers long-poll (`GET /queue/status/{id}?waitSeconds=15`), so 5,000 waiting people cost a few
hundred cheap requests per second on one small VM. Full design, contracts and diagrams:
[`docs/waiting-room.md`](docs/waiting-room.md).

---

## 5. The load generator (`load-generator`)

A Go agent on its own VM. Chosen instead of k6 because one binary handles open-loop arrivals, saturation accounting, per-server attribution and per-second metrics to Firestore.

* **Open loop:** arrivals are scheduled from the traffic pattern (seeded Poisson process) whether or not the API is slow. A struggling backend shows up as rising latency, not as silently reduced load.
* **Three different numbers:**
  * a **customer arrival** is one synthetic customer;
  * an **HTTP request** is one API call (a purchase is about 4–5 of them);
  * a **virtual user (VU)** is a journey in flight. If no VU is free, the arrival is **dropped** and counted. That is generator saturation, not app capacity.
* **Customer journeys:**
  * *browse* (list events → event page);
  * *availability* (one check);
  * *reserve* (check → reserve → abandon);
  * *purchase* (check → reserve → order → pay, with retries on decline or timeout).

  Random ticket quantities, think times, duplicate submissions and abandoned checkouts are all seeded, so runs are reproducible.
* **Traffic patterns:** constant, ramp up, spike, ramp down, random bursts, custom schedule.
* **Control:** listens to the Firestore document `generator/command`. It claims an experiment with a renewable lease, sends a heartbeat every 2 s, and stops immediately on a hard stop. An independent max-duration timer (30 min) backs that up. After a restart it never silently resumes traffic.
* **Safety:** it can only target the load balancer IP that Terraform gives it (`ALLOWED_TARGET_HOSTS`). Commands cannot change the target.
* **Metrics:** one Firestore document per second, holding:
  * requested and achieved arrivals, and dropped arrivals;
  * status classes (2xx, 409, 410, 402, 4xx, 5xx, timeouts);
  * a latency histogram (p50/p95/p99);
  * requests per server, from `X-Served-By`;
  * business counters (reservations, purchases, rejections).

---

## 6. The control API and dashboard (`apps/control-api`, `apps/dashboard`)

### Control API (Cloud Run, Node/TypeScript)

**Experiment lifecycle:**

```text
idle → preparing → ready → running → stopping → cooling_down → completed      (any → failed)
```

* **Prepare:**
  1. Validate the config.
  2. Patch the autoscaler (min, max, CPU target, initialisation period, stabilization period; never above the approved max).
  3. Create a fresh event with the requested tickets through the private admin API.
  4. Wait until the initial number of servers is healthy.
* **Start:** check the generator is online, restore the minimum (from now on capacity is the autoscaler's job), and send the start command.
* **Stop:** hard stop through Firestore.
* **Cooling down:** after traffic ends, complete as soon as the extra servers are removed (instance group back at the minimum, after at least the reservation TTL + 5 s so last holds resolve); `cooldownObserveSeconds` is only the upper bound. Presets use a 60 s scale-in window so the autoscaler removes servers about a minute after demand drops (preset E uses 240 s to show a slower scale-in; the Terraform baseline is also 60 s, so servers left after any run go quickly).
* **Complete:** build the report, then restore the autoscaler to its Terraform baseline.

A Firestore lock ensures only one experiment runs at a time. If the service restarts, it resumes the active experiment from Firestore.

**Telemetry collectors**, each with its own interval and freshness rule:

| Source | Every | Freshness |
|---|---|---|
| Generator buckets (Firestore listener) | 1 s | live ≤ 3 s |
| PostgreSQL inventory and invariants (read-only `lab_observer` user) | 2 s | live ≤ 6 s |
| Compute Engine instance group, instances, autoscaler | 5 s | live ≤ 15 s |
| `/instance-info` on VM private IPs (guest CPU, not used by the autoscaler) | 5 s | per sample |
| Load-balancer backend health | 10 s | live ≤ 30 s |
| Cloud Monitoring CPU (what the autoscaler uses) | 20 s | never "live": *delayed* ≤ 5 min, then *stale* |

A missing or stale source shows **N/A** with a badge, never a fake 0.

**Timeline events** are produced only by comparing successive real observations. Examples:

* "Autoscaler increased target size 1 → 2";
* "VM is RUNNING", "passed health check", "HEALTHY in load balancer", "served its first requests";
* "Average CPU exceeded 60% (sample at 23:08)";
* "Inventory sold out", "termination started", "removed from the group".

Each capacity change is labelled:

* **autoscaler** — the autoscaler's own CPU-based decision;
* **baseline** — the lab raised the minimum to honour "initial servers";
* **operator** — someone lowered the maximum.

**Reports** keep three kinds of value apart:

* **measured** — arrivals, requests, latency percentiles, error % vs business-rejection %, peak CPU with sample time, servers initial/max/final, tickets sold/remaining/expired, invariant violations, requests per server;
* **configured** — the targets that were set;
* **estimated** — cost.

Export as JSON, CSV time series or CSV events.

### Dashboard (Vue 3 + TypeScript + Tailwind + ECharts)

| Section | Contents |
|---|---|
| Header | Experiment state, signed-in identity, stream status, estimated cost per hour |
| Telemetry health | Live / delayed / stale / missing for traffic, servers, CPU, inventory, LB |
| **B. Experiment controls** | Prepare, Start Traffic, Stop Traffic, Complete observation, Reset Ticket Inventory, Reset Experiment, exports, history |
| **A. Configuration** | Servers, tickets, traffic, mix, behaviour, seed, demo-only synthetic CPU; presets A–F; validation; confirmation for expensive settings |
| **C. Live servers** | One card per real VM: phase, VM/MIG/LB health, Cloud Monitoring CPU with sample age, guest CPU, req/s, latency, errors, share of traffic |
| **D. Real-time charts** | Incoming traffic, server scaling, CPU vs target, latency p50/p95/p99, ticket inventory, outcomes |
| **E. Event timeline** | Observed events with time since start, source and attribution |
| History / Report pages | Past experiments and full reports |
| **Destroy lab** | Deletes everything in Google Cloud (see §10) |

Live updates arrive over Server-Sent Events, read with `fetch` so they can carry an auth header.

---

## 7. Experiment presets

| Preset | Configuration | Shows |
|---|---|---|
| **A Baseline** | 1 server (max 1), 10,000 tickets, 5 customers/s, 5 min | Stable single-server behaviour, no scaling |
| **B Increasing traffic** | 1 → max 5, CPU target 60%, 100,000 tickets, ramp 5 → 100 customers/s over 10 min | Real autoscaling: CPU crosses target, VMs added, new VMs join and serve |
| **C Flash sale** | 100 tickets, 1,000 purchase attempts in a 25 s burst at 40/s | Correctness: exactly 100 sold, the rest get `409 sold out` |
| **D Reservation expiry** | 100 tickets, 30 s holds, customers reserve and abandon | Holds filling up, expiring and capacity returning |
| **E Recovery and scale-in** | 90 customers/s for 6 min, then 25 min observation | Scale-out, traffic spread across VMs, delayed scale-in |
| **F 10,000 customers, adaptive waiting room** | 10,000 customers at 84/s (about 2 min), 4,000 tickets, 30 s holds, 10% abandonment; 1 → max 3 servers, CPU target 30%, 60 s scale-in window, completes 30 s after traffic ends; adaptive waiting room (limit per server learned from the busiest server, starting at 25) | More customers than 3 servers can take: the overflow waits in a FIFO waiting room instead of failing, capacity grows as the autoscaler adds servers, and the people still waiting at the end are told "sold out" |

Run them from the dashboard, or with `./scripts/start-load-test.sh B`. Guide: [`docs/experiment-guide.md`](docs/experiment-guide.md).

---

## 8. Infrastructure (Terraform)

The root module is `terraform/environments/autoscaling-lab/` and the state lives there (local, gitignored). It creates:

| Area | Resources |
|---|---|
| APIs | Compute, Cloud SQL Admin, Cloud Run, Artifact Registry, Secret Manager, Firestore, Monitoring, Logging, IAM, IAP, Resource Manager, Billing Budgets (never disabled by Terraform) |
| Network | Dedicated VPC; workloads subnet with Private Google Access; control subnet for Cloud Run; firewall rules only for the LB/health-check ranges, the control subnet and IAP SSH |
| Database | Cloud SQL PostgreSQL 16 (`db-custom-1-3840`): no public IP, TLS required, reached through a Private Service Connect endpoint; app user and read-only observer user |
| Secrets | DB passwords, admin token, Laravel app key (generated by Terraform, stored in Secret Manager) |
| Images | Artifact Registry Docker repository |
| App tier | Instance template (COS, `n1-standard-1`, no external IP); zonal instance group starting with 1 VM; autohealing on liveness; rolling updates (surge 1, unavailable 0) |
| Autoscaler | CPU target 60%, min 1, max 5, initialisation 60 s, stabilization 60 s (preset E uses 240 s to show a slow scale-in) |
| Load balancer | Global external Application Load Balancer (Envoy), HTTP on a static IP, readiness check `/health`, 5% request logging |
| Generator | `e2-small` COS VM with an ephemeral external IP (no inbound ports except IAP SSH) |
| Waiting room | `e2-small` COS VM, no external IP, fixed private IP; unmanaged instance group + backend service; LB URL map sends `/queue/*` to it; firewall only from LB ranges and the control subnet; reads app VMs on their private IPs |
| Control plane | Cloud Run (1 vCPU, always allocated, exactly 1 instance), Direct VPC egress |
| History | Firestore database `ticketing-scale-lab` |
| Observability | Cloud Monitoring dashboard; budget alert (notification only) |
| IAM | Four service accounts (`tsl-app`, `tsl-loadgen`, `tsl-queue`, `tsl-control`) with least privilege; resource-specific grants use IAM conditions; dashboard access only for the allowlisted account |

**Runtime overrides:** during an experiment the dashboard may change the autoscaler's min, max, CPU target and periods, never above `approved_max_instances`. Overrides are logged, shown with a badge, and reverted when the experiment ends or is reset. `terraform apply` also reverts them, so `scripts/terraform-apply.sh` refuses while an experiment is active (override: `ALLOW_DURING_EXPERIMENT=1`).

Deep dive: [`docs/architecture.md`](docs/architecture.md).

---

## 9. Security and cost controls

**Security**

* **Access:** dashboard access goes through Cloud Run IAM (only the allowlisted Google account may invoke it), reached through `scripts/dashboard.sh`.
* **Credentials:** no service-account key files and no passwords in code or VM metadata. Secrets come from Secret Manager at runtime.
* **Database:** no public IP and TLS required.
* **App VMs:** no external IPs.
* **Admin API:** reachable only from the control subnet (nginx refuses it through the load balancer with 403) and also needs a bearer token.
* **Generator:** locked to the lab's load balancer IP.
* **Scripts:** every gcloud call passes `--account=thebeezindex@gmail.com --project=contentpulse-vertex-ai` explicitly, and the global gcloud configuration is never changed.
* **Terraform credentials:** a fresh OAuth token is exported in the same shell before each plan, apply or destroy. It is never printed or stored.

**Cost** (list-price estimates, europe-west2)

| State | ≈ per hour | ≈ per day |
|---|---|---|
| Idle (1 app VM + SQL + LB + generator + Cloud Run) | $0.29 | $7 |
| Peak (5 app VMs) | $0.54 | — |

* **Approval gate:** `approve.sh` records the approved maximum (5 VMs) and budget (50); apply refuses without it.
* **Expensive settings:** configs above 150 customers/s, 15 min or 1,000 VUs need explicit confirmation.
* **Budget alert:** sends emails only; it is not a spending cap.
* **What keeps billing:** stopping traffic does not stop billing. SQL, the LB, the generator, Cloud Run and the minimum VM keep running until teardown.

Details: [`docs/cost-and-cleanup.md`](docs/cost-and-cleanup.md).

---

## 10. How to use it

### Run everything locally (free)

```bash
docker compose up -d --build        # Postgres, Firestore emulator, API, control plane, generator
open http://localhost:18081         # local dashboard (Google Cloud panels show N/A by design)
./scripts/concurrency-test.sh       # 1,000 purchase attempts vs 100 tickets over real HTTP
docker compose down -v
```

### Deploy to Google Cloud

```bash
./scripts/gcp-preflight.sh          # read-only: account, project number, billing, permissions, quotas, name conflicts
./scripts/approve.sh 5 "50"         # record the approved max VMs and budget
./scripts/deploy.sh                 # foundation → build & push images → workloads → verify
./scripts/dashboard.sh              # dashboard at http://localhost:8090
```

The Firestore database needs `roles/datastore.owner` on the deploying account (granted for the deployment, removed by teardown).

### Day-to-day

| Command | What it does |
|---|---|
| `./scripts/start-load-test.sh B` | Prepare and start a preset |
| `./scripts/stop-load-test.sh` | Hard stop traffic |
| `./scripts/monitor-scaling.sh` | Raw MIG / autoscaler / LB state from gcloud |
| `./scripts/verify.sh` | Post-deploy checks: health, routing, admin isolation, autoscaler, auth, generator |
| `./scripts/smoke-test.sh` | One purchase through the load balancer |
| `./scripts/build-and-push.sh control-api` | Rebuild only the named image(s); others keep their tag (no needless VM replacement) |
| `./scripts/terraform-plan.sh` / `terraform-apply.sh` | Plan and apply changes (apply needs the approval file) |

### Remove everything

Press **Destroy everything in Google Cloud** on the dashboard and type `destroy ticketing-scale-lab`, or run:

```bash
./scripts/teardown.sh --yes
```

It runs these steps, can be resumed if interrupted, and APIs are only disabled after a successful verification:

1. Stop traffic.
2. Delete the workloads and Cloud SQL.
3. Wait for Google to release Cloud Run's internal IP (can take 1–2 hours).
4. Delete the network and everything else.
5. Remove the temporary `datastore.owner` role.
6. Verify that nothing named `ticketing-scale-lab` / `tsl-*` remains.
7. Disable the APIs the lab enabled.

Only Cloud Logging entries remain; Google keeps them for 30 days at no charge.

---

## 11. Tests

| Suite | Command | Covers |
|---|---|---|
| Ticketing API (real PostgreSQL) | `cd apps/ticketing-api && php artisan test` | Reservations, sold out, last ticket, a 60-process concurrency race, idempotency, expiry, delayed/declined payment, ticket issue, rollback, admin auth |
| Control API | `cd apps/control-api && npm test` | Lifecycle, overlap and confirmation guards, event detection and attribution, freshness, aggregation, reports, auth |
| Dashboard | `cd apps/dashboard && npm test` | Live instance count, delayed/missing telemetry, counts, auth, no invented events, SSE parser, validation, destroy panel |
| Waiting room | `cd apps/waiting-room && go test -race ./...` | FIFO, capacity from healthy servers, release/expiry, long poll, sold-out grace, tokens (shared test vector with PHP), admin auth, 5,000-waiter drain |
| Load generator | `cd load-generator && go test -race ./...` | Requested vs achieved rate, open loop, saturation, seed reproducibility, duration, cancellation, recovery, aggregation |
| Terraform | `terraform -chdir=terraform/environments/autoscaling-lab test` (+ modules) | Autoscaler bounds and CPU, MIG, health checks, private VMs, no public DB, firewall, approved max |
| Cleanup scripts | `python3 -m unittest discover -s scripts/tests` | Staged destroy, IP-release wait, safety checks |
| Everything | `./scripts/build.sh` | All suites + local image builds |

---

## 12. What was proven in Google Cloud

All measured on the live deployment:

| Test | Result |
|---|---|
| **Increasing traffic (B)** | 31,662 customers, 67,219 requests, **0% HTTP errors**, p95 252 ms. Autoscaler scaled **1 → 2 → 3 → 4** on its own as CPU passed 60%; new VMs served within about a minute of creation. After traffic stopped: **4 → 1** within about 5 minutes. **0 inventory violations.** |
| **1,000-ticket proof run** | 31,086 customers, 58,061 requests, 0 HTTP errors, p95 60 ms. Scaled 1 → 3. 997 sold + 3 released by hold expiry after traffic ended = 1,000. Load balancer counts (Cloud Monitoring): 42,933 × 2xx, 14,716 × 409 sold out, 16 × 402 declined, **no 5xx**. |
| **Preset F without a waiting room** (5,000 customers at 40/s, 2,000 tickets, 1 → max 4, CPU target 30%) | Autoscaler scaled **1 → 2 → 3 → 4 at 23 s, 43 s and 69 s**. But the first VM was overwhelmed before the second could serve (t+74 s): its readiness probe timed out and the LB returned **3,469 × `503 failed_to_pick_backend`** in 17 s. **21% HTTP errors**, 1,358 failed purchases, 1,988 sold (12 holds lapsed after traffic ended), 0 violations. |
| **Preset F with the waiting room** (5,000 customers at 42/s, 2,000 tickets, 1 → max 3, 25 per healthy server) | **0% HTTP errors, 0 dropped, no 5xx in the LB logs.** Peak 2,743 waiting. Capacity followed the LB: 25 → 50 at t+83 s → 75 at t+110 s as the autoscaler's VMs turned healthy. **Exactly 2,000 sold**, 0 failed purchases, 0 violations; 2,267 admitted to checkout and **2,733 told "sold out" in the queue** at t+149 s, without touching an app server. Wait p50 20 s, p95 39 s. App p95 653 ms (was 7.8 s). Scale-in 3 → 1 after the stabilisation window. |
| **Preset F, adaptive waiting room** (shared limit learned from workers busy, request queue, VM CPU) | 0 % errors, exactly 2,000 sold, 2,746 told "sold out" in the queue. The limit settled at **14 per 1-vCPU server** (CPU-bound), every server the same; app p95 **405 ms** vs 653 ms with a fixed 25, at ~25 % less throughput. Details: [`docs/waiting-room.md`](docs/waiting-room.md). |
| **Flash sale (C)** | Exactly **100 of 100** tickets sold, 0 violations. One 1-vCPU VM cannot absorb the burst, so some requests time out; that limit is documented, and correctness held. |

Measured capacity: one `n1-standard-1` VM handles roughly **120 req/s** of checkout-heavy traffic before latency rises into seconds.

---

## 13. Problems found during deployment (and their fixes)

| Symptom | Cause | Fix |
|---|---|---|
| First apply failed: "API not used before" | Newly enabled APIs had not propagated | Explicit dependencies; re-run |
| VMs could not pull images | Artifact Registry ignores the resource-name IAM condition | Read-only registry access without condition |
| `no password supplied` | Secret Manager URL missing `/versions/latest`, error swallowed | Versioned URL; entrypoint fails loudly |
| Health checks timed out, VMs recreated in a loop | COS host firewall blocks inbound; migrations blocked nginx | `iptables` rule for the app port; migrations as a supervised task |
| Sweeper could not connect to the DB | libpq looked for certs under `/root` | `HOME=/var/www` for www-data processes |
| LB `503 backend_connection_closed…` under bursts | nginx ran out of worker connections | 16,384 connections per worker, 32 PHP workers |
| **Database CPU at 100%** at about 80 req/s | A new TLS + SCRAM connection per request | Persistent DB connections per PHP worker; readiness check cached for 60 s |
| `gcloud run services proxy` got 403 | It uses the globally active gcloud account | Own proxy (`dashboard-proxy.mjs`) signed as the allowlisted account |
| Destroy failed on SQL database and users | Active connections and dependent objects | `deletion_policy = ABANDON`; the instance deletion removes them |
| Destroy failed on the control subnet | Cloud Run keeps an internal IP reserved for 1–2 h | Staged teardown that waits for the release |
| Teardown stopped by a network drop | Transient "No route to host" | Teardown is resumable; transport errors are retried |
| Short sale (preset F) produced `503 failed_to_pick_backend` for 17 s | One saturated VM's readiness probe timed out before the 2nd VM could serve, so the LB had no backend | Waiting room: admit only healthy servers × 25 customers; the rest wait instead of failing |
| After a deploy the generator still ran the old agent (customers skipped the queue and got `403 queue_token_required`) | Changing the image only rewrites cloud-init metadata; the single VMs were not rebooted | Generator and queue VMs are replaced when their image changes (`terraform_data` + `replace_triggered_by`) |
| `terraform apply` failed mid-create ("http2: client connection lost") and left the queue backend outside state | Local network drop while polling the operation | `terraform import` of the created backend, then re-plan |

---

## 14. Known limits (Google Cloud behaviour, not bugs)

* **CPU data lag:** the autoscaler reacts to 60-second CPU samples that arrive 1–4 minutes late, so scale-out takes minutes, not seconds.
* **Boot time:** a new VM needs about 1 minute to boot, pull the image and pass health checks.
* **Scale-in:** waits for the stabilization period (240 s here) and happens gradually.
* **Uneven traffic split:** the load balancer spreads requests per request, but newer servers receive less until they have been healthy for a while.
* **Short bursts:** bursts shorter than the autoscaler's reaction time (presets C and F) are absorbed by the servers that already exist. With the waiting room on (F), the overflow waits instead of failing.
* **Waiting room is one VM:** if it restarts, people in line lose their place (issued tokens stay valid until they expire). Lab scope; a production design would keep the queue in a replicated store.
* **Teardown wait:** Cloud Run's internal IP can delay the final network deletion by 1–2 hours.

---

## 15. Glossary

| Term | Meaning |
|---|---|
| **MIG** | Managed Instance Group: Compute Engine keeps N identical VMs from a template and replaces unhealthy ones |
| **Autoscaler** | Changes the MIG's target size based on average CPU vs the target (60%) |
| **Initialisation period** | How long the autoscaler ignores a new VM's CPU while it boots (60 s) |
| **Stabilization period** | How long the autoscaler waits before removing VMs after load drops (240 s) |
| **Readiness / liveness** | `/health` (DB reachable; decides LB traffic) vs `/health/live` (process up; decides VM recreation) |
| **Open-loop load** | Arrivals follow the schedule regardless of how fast the server answers |
| **Business rejection** | Expected answers that are not errors: `409` sold out, `410` reservation expired, `402` payment declined |
| **Invariant violation** | Any mismatch between ticket counters and the rows they summarise; must always be 0 |
| **PSC** | Private Service Connect: Cloud SQL reached through an internal IP in the lab's own subnet |
| **Direct VPC egress** | Cloud Run sending traffic straight into the VPC (to reach VMs and Cloud SQL privately) |

---

## 16. Further reading

* [README.md](README.md) — quick start
* [docs/architecture.md](docs/architecture.md) — components, decisions, telemetry, attribution
* [docs/deployment.md](docs/deployment.md) — deployment steps, authentication, troubleshooting
* [docs/experiment-guide.md](docs/experiment-guide.md) — running experiments and reading the numbers
* [docs/cost-and-cleanup.md](docs/cost-and-cleanup.md) — costs, guard rails, teardown
* [docs/failure-scenarios.md](docs/failure-scenarios.md) — concurrency and failure handling with test evidence
* [docs/contracts.md](docs/contracts.md) — exact interfaces between components
* [docs/implementation-checklist.md](docs/implementation-checklist.md) — build checklist and decisions log
