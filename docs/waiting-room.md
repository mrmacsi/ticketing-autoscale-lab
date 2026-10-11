# Waiting room (queue server)

What happens when every server is full? Without a waiting room the answer is: requests pile up on the
servers that exist, latency climbs past the load balancer's readiness probe, the load balancer runs out
of healthy backends and customers get `503 failed_to_pick_backend` (measured in the first fast-scale
run of preset F: 3,469 × 503 in 17 seconds, before the second VM could serve).

With the waiting room, customers who arrive while the servers are full **wait in a FIFO queue** and are
let in only as fast as the servers that the load balancer can actually route to can take them. When the
autoscaler adds a server, capacity grows and the queue drains faster. When tickets are gone, the people
still waiting are told "sold out" without ever touching an application server.

* The **waiting room decides _when_** a customer may start checkout (admission control).
* The **load balancer decides _where_** that customer's requests go (any healthy server).
* The **application enforces** it: `POST /api/reservations` refuses a request without a valid signed
  admission token while the waiting room is in force, so nobody can skip the queue.

---

## 1. Architecture

```text
                                   External Application Load Balancer (http://<ip>)
 load generator VM                 URL map
 (synthetic customers)  ──HTTP──▶   /queue/*  ───────────────▶  waiting-room backend ──▶  queue VMs (Go, 2, shared Redis)
                                    everything else ─────────▶  app backend ──┬──▶ app VM 1           │
                                                                             ├──▶ app VM 2           │ every 2 s: LB backend health
                                                                             └──▶ app VM 3 (max)     │   (Compute API getHealth)
                                                                                    ▲                │ every 1 s: availability from a
                                                                                    └────────────────┘   healthy app VM (private IP)
 control API (Cloud Run) ──private IP──▶ queue VM /admin/*  (configure, reset, stats; admin token)
                         ──private IP──▶ app VM /api/admin/settings  (waiting_room_required on/off)
```

```mermaid
sequenceDiagram
    autonumber
    participant C as Customer (generator)
    participant LB as Load balancer
    participant Q as Waiting room (queue VM)
    participant A as App server (chosen by LB)
    C->>LB: POST /queue/join
    LB->>Q: route /queue/*
    Q-->>C: waiting, position 1,240
    loop long poll (up to 15 s each)
        C->>Q: GET /queue/status/{id}?waitSeconds=15
        Q-->>C: waiting, position 610 ... / admitted + token
    end
    Note over Q: admits while active < healthy servers × active users per server
    C->>LB: GET availability, POST /api/reservations (X-Queue-Token)
    LB->>A: any healthy server
    A-->>C: 201 reservation (token verified with HMAC, no DB lookup)
    C->>A: order, payment (via LB)
    C->>Q: POST /queue/release (frees the slot for the next person)
```

### Why a separate VM behind the same load balancer

| Option | Verdict |
|---|---|
| Queue inside the Laravel app, state in PostgreSQL | Rejected: thousands of waiting customers polling would hit the 1-vCPU database and the very servers that are full. |
| Cloud Run service | Rejected: needs a serverless NEG and public invoker IAM the deploying account cannot grant; FIFO state must be single-instance anyway. |
| **Dedicated small VMs, FIFO in Memorystore for Redis, `/queue/*` path rule on the existing LB** | **Chosen.** Redis is the single source of truth for order; long polling keeps thousands of waiters cheap; same host for the generator (its target allowlist is unchanged); reachable privately by the control plane. |

The first version was one VM with the line in memory: a restart, deploy or crash lost everybody's
place. The queue now runs as several stateless instances sharing one Redis (section 5); the memory
store remains as a fallback (`STORE=memory`, one instance). Tokens already issued stay valid across
any of this because verification is stateless.

---

## 2. Admission rules

* `capacity = healthyServers × activeUsersPerServer` where `healthyServers` is the number of app VMs the
  **load balancer** reports `HEALTHY` (not merely running). Zero healthy servers ⇒ capacity 0 ⇒ nobody
  is admitted (people wait instead of getting 503).
* `active` = admitted customers who have neither released their slot nor passed `admissionTtlSeconds`
  since admission (abandoned browsers cannot hold a slot forever).
* Every 100 ms, while `active < capacity` and the room is not sold out, the head of the queue is admitted.
  Strict FIFO by join order.
* **Inventory:** every second the queue reads `GET /api/events/{id}/availability` (tickets `available`
  and `held` in other customers' checkouts) from a healthy app VM.
  * It never admits more customers per observation than there are `available` tickets. Near the end of a
    sale an expired hold returns one ticket at a time; without this budget the room would let a full
    batch of 25–50 customers in to fight over one ticket (measured in the first local run: 4,362 admitted
    for 2,000 tickets).
  * While `available == 0` it stops admitting but keeps people waiting: held tickets may come back.
  * `available == 0 and held == 0` ⇒ **sold out at once**: every waiting customer gets `sold_out` and new
    joins are told immediately. (If an app does not report `held`, the room falls back to waiting
    `soldOutGraceSeconds` — reservation TTL + 5 s — of continuous zero availability.)
  * Sold out is final until the room is reset.
* **People who leave are dropped.** Anyone in line who has not checked in (joined or polled) for
  `waitingTimeoutSeconds` (default 45 s, longer than the 25 s maximum long poll) is removed once a
  second, counted in `abandonedTotal`, and never admitted; if they come back their id is unknown and they
  re-join at the back. Without this, the first adaptive run in Google Cloud kept 2,700 people who had
  already gone (the generator stopped) and trickled them into checkout one per returned ticket.
* A customer who joins when the room is disabled gets status `open` (no queue in force; proceed without a
  token).

`activeUsersPerServer` (default 25) is sized from measurement: one `n1-standard-1` serves about 100–120
req/s; an uncontended purchase journey is 4 requests in about 1 s, so ~25 journeys in flight per server
keeps a VM busy (CPU high enough for the autoscaler) without starving its readiness probe.

---

## 2a. Adaptive admission (how many customers can a server take?)

A fixed "25 customers per server" is only right for one machine type and one workload. CPU alone is a
poor gate: Cloud Monitoring CPU arrives 1–4 minutes late, it misses non-CPU bottlenecks (database,
locks, sleeping workers), and if the room admitted only below the autoscaler's CPU target the autoscaler
would never scale. In **adaptive mode** the room learns each server's limit from fast signals the server
reports about itself, the way TCP finds a network's capacity (slow start, then
additive-increase / multiplicative-decrease) and the way Netflix concurrency-limits and Envoy's adaptive
concurrency protect services.

### Signals (per healthy server, every second, never through the busy worker pool)

| Signal | Where it comes from | Why it matters |
|---|---|---|
| **Workers busy** = requests in flight / PHP workers (capped at 100 %) | `GET /lab/load` → nginx `stub_status` ("Writing" = requests being processed) and the worker count in the `X-Php-Workers` header | Little's law: requests in flight = throughput × time per request, so this rises for *any* bottleneck (CPU, database, locks, payment delays) |
| **Listen queue** = requests in flight beyond the worker count (waiting for a free worker) | same | Anything > 0 means the server is already overloaded: requests are queuing, latency is climbing |
| **VM CPU** (sampled every 2 s inside the VM) | `GET /lab/cpu` → the sampler's file, served by nginx | CPU-bound protection; seconds old, not minutes |

Both endpoints are served by nginx without PHP and only to VPC addresses (`LOAD_ALLOW_CIDRS`); through
the load balancer they return 403.

Measured on the local stack (16 PHP workers, each request burning 200 ms of CPU):

| Concurrent requests | In flight reported | Busy | Waiting for a worker | Signal response time |
|---:|---:|---:|---:|---:|
| 4 | 4 | 25 % | 0 | 2–4 ms |
| 8 | 7–8 | 44–50 % | 0 | 2–3 ms |
| 12 | 9–12 | 56–75 % | 0 | 1–3 ms |
| 64 | 62–63 | 100 % | 46–47 | 1–2 ms (app requests meanwhile: 0.74 s) |

PHP-FPM's own status page was tried first and rejected: served from a separate status listener
(`pm.status_listen`, so it answers when the pool is full) it reported impossible worker counts
(e.g. 24 busy of 24 with 4 requests on a 16-worker pool, 85 of 85 under saturation).

### Controller (every second): one shared limit, set by the busiest server

The load balancer gives every healthy server the same share of requests, whatever "limit" each
server has, so the limit that matters is **customers in checkout per healthy server**, and it must
suit the busiest server. capacity = healthy servers × limit.

The first adaptive version kept a separate limit per server. Measured in Google Cloud, identical VMs
drifted apart (7 and 39): server 1 also carried load from customers admitted on server 2's limit and
was cut for it. A new server slow-started from 25 and overshot on a cold VM (p95 2 s). One shared limit
fixes both: decisions use the worst signal of any server, and a new server joins at the learned value.

```mermaid
flowchart TD
    S[Every 1 s] --> W[Worst signals across healthy servers<br/>max queue, max busy, max CPU]
    W --> F{Any fresh signals?}
    F -- no --> HOLD1[hold: no signal]
    F -- yes --> OV{Overloaded anywhere?<br/>queue > 0 or busy ≥ 90% or CPU ≥ 90%}
    OV -- yes --> CD{Last cut ≥ 3 s ago?}
    CD -- yes --> DEC[limit = max min, limit × 0.75<br/>phase = steady<br/>reason names the server]
    CD -- no --> HOLD2[hold: let the last cut take effect]
    OV -- no --> HR{Every server fresh with headroom?<br/>queue = 0, busy < 70%, CPU < 80%<br/>and room ≥ 80% full}
    HR -- no --> HOLD3[hold]
    HR -- yes --> PH{Phase}
    PH -- slow start --> INC1[limit × 1.25]
    PH -- steady --> INC2[limit + 1]
    DEC --> SUM
    INC1 --> SUM
    INC2 --> SUM
    HOLD1 --> SUM
    HOLD2 --> SUM
    HOLD3 --> SUM
    SUM[capacity = healthy servers × limit<br/>admission loop every 100 ms: admit while active < capacity]
```

```mermaid
stateDiagram-v2
    [*] --> SlowStart: room opens (limit = starting value 25)
    SlowStart --> SlowStart: headroom everywhere → ×1.25
    SlowStart --> Steady: overload on any server → ×0.75
    Steady --> Steady: headroom everywhere → +1 / overload anywhere → ×0.75
    note right of Steady: a server joining or leaving changes capacity\n(healthy × limit), not the learned limit
```

The result is a sawtooth just under what the busiest server can take. A bigger machine type (or more
PHP workers) gets a higher limit without anyone changing a number; a database slowdown lowers it.
Heterogeneous pools behind a round-robin balancer are limited by their weakest member — which is
exactly what that balancer forces anyway.

### Why the two control loops do not fight

| Loop | Signal | Acts on | Time scale | Threshold |
|---|---|---|---|---|
| Autoscaler | Cloud Monitoring CPU (60 s samples, 1–4 min late) | number of servers | minutes | CPU target (30 % in preset F) |
| Waiting room | workers busy, listen queue, VM CPU (1–2 s) | customers admitted | seconds | busy 90 %, CPU 90 % |

The room only holds servers back *above* the autoscaler's target, so a busy server still pushes the
autoscaler to add servers, and each new server adds its own limit to the room's capacity.

### Guards

* Increase only when the room is at least 80 % full (active ≥ 0.8 × capacity). "People waiting" is not
  enough: near a sell-out they wait for *tickets*, not capacity. The first local run did exactly that
  (an idle server's limit climbed 14 → 130 while 2,000 people waited for returned holds) before this
  rule was tightened.
* After a cut, no further cut for 3 s: customers already inside need time to finish.
* Limits stay within `[minPerServer, maxPerServer]` (default 5–200).
* No fresh signals for a server ⇒ its limit is held (not increased); a server that leaves the LB's
  healthy set leaves the capacity at once.
* Fixed mode (`admissionMode: "fixed"`) keeps the previous behaviour: healthy servers × `activeUsersPerServer`.

## 3. Contracts

### 3.1 Queue server public API (through the load balancer, prefix `/queue/`)

Every response has `X-Served-By: waiting-room` and `Cache-Control: no-store`. Error body shape is the same
as the ticketing API: `{"error": "<code>", "message": "<text>"}`.

**Status object** (`data` of join and status responses):

```json
{
  "queueId": "q_01J9...",            // opaque, unguessable (128-bit random, base32/hex)
  "eventId": "<uuid>",
  "status": "waiting | admitted | sold_out | open",
  "position": 1240,                    // 1-based place in line while waiting, 0 otherwise
  "waiting": 3110,                     // total currently waiting
  "estimatedWaitSeconds": 42,          // position / recent admission rate (null when unknown)
  "token": "v1.<payload>.<sig>",       // only when admitted
  "tokenExpiresAt": "2026-10-10T15:00:00.000Z",  // only when admitted
  "pollAfterMs": 0                     // hint for clients that do not long-poll
}
```

| Method & path | Body / query | Response |
|---|---|---|
| `POST /queue/join` | `{"eventId": "<uuid>", "customerRef": "<string ≤ 100>"}`. Same `customerRef` + event while still waiting/admitted returns the same entry (safe retry). | 200 `{data: Status}`; 400 `invalid_request`; 404 `unknown_event` when the room is enabled for a different event |
| `GET /queue/status/{queueId}?waitSeconds=N` | `N` 0–25 (default 0). Long poll: returns as soon as the status is no longer `waiting`, or after N seconds with the current position. | 200 `{data: Status}`; 404 `unknown_queue_id` |
| `POST /queue/release` | `{"queueId": "...", "outcome": "purchased | sold_out | abandoned | failed"}` | 200 `{data: {"released": true}}` (idempotent; unknown id also 200 with `released: false`) |
| `GET /queue/health` | — | 200 `{"status":"ok"}` (LB health check) |

### 3.2 Admission token

```text
token   = "v1." + b64url(payloadJSON) + "." + b64url(HMAC_SHA256(signingKey, "v1." + b64url(payloadJSON)))
payload = {"q": "<queueId>", "e": "<eventId>", "exp": <unix seconds>}
signingKey = HMAC_SHA256(key = LAB_ADMIN_TOKEN, message = "waiting-room-token-v1")   // raw 32 bytes
```

b64url is base64 URL alphabet **without padding**. Both sides derive the signing key from the lab admin
token (already in Secret Manager for both), so no new secret is introduced. `exp = admittedAt +
admissionTtlSeconds`.

### 3.3 Ticketing API enforcement

* New lab setting `waiting_room_required` (boolean, default `false`) through the existing
  `PUT /api/admin/settings`.
* When true, `POST /api/reservations` requires header `X-Queue-Token`. Checked **before** idempotency
  replay and before any database work:
  * missing → `403 {"error":"queue_token_required"}`
  * bad format/signature → `403 {"error":"queue_token_invalid"}`
  * `exp` in the past → `403 {"error":"queue_token_expired"}`
  * `e` ≠ the event of the requested `category_id` → `403 {"error":"queue_token_wrong_event"}` (this
    check may be done in the controller where the category is loaded).
* Orders and payments are not gated: they can only continue a reservation that was already admitted.

### 3.4 Queue server private API (VPC only — the LB never routes `/admin/*` to it)

All require `Authorization: Bearer <LAB_ADMIN_TOKEN>`; 401 otherwise.

| Method & path | Body | Response |
|---|---|---|
| `PUT /admin/config` | `{"enabled": bool, "eventId": "<uuid>", "activeUsersPerServer": 1..1000, "admissionTtlSeconds": 10..3600, "soldOutGraceSeconds": 0..3600, "admissionMode": "fixed" \| "adaptive", "waitingTimeoutSeconds": 0 \| 30..3600, "adaptive": {"minPerServer": 1..1000, "maxPerServer": 1..1000, "busyHigh": 0..1, "busyLow": 0..1, "cpuHigh": 0..1, "cpuLow": 0..1, "decreaseFactor": 0.1..0.99, "slowStartFactor": 1.01..4, "cooldownSeconds": 0..60}}` (partial bodies keep omitted values; `adaptive` merges field by field). In adaptive mode `activeUsersPerServer` is the starting limit of each new server. A different `eventId` (or `enabled:false`) clears the queue, counters and learned limits. | 200 `{data: Config}` |
| `POST /admin/reset` | — | 200; clears queue, active set, counters; keeps config |
| `GET /admin/stats` | — | 200 `{data: Stats}` |

Every instance answers every endpoint (public and admin) with the same data. If the shared store
(Redis) is unreachable, join, status, release and the admin endpoints answer
`503 {"error":"store_unavailable"}` (clients retry).

```json
Stats {
  "enabled": true, "eventId": "<uuid>", "soldOut": false,
  "waiting": 3110, "active": 75, "capacity": 75,
  "healthyServers": 3, "servers": [{"name": "ticketing-scale-lab-app-ab12", "ip": "10.60.0.9"}],
  "activeUsersPerServer": 25,
  "joinedTotal": 4200, "admittedTotal": 1090, "releasedTotal": 1015, "expiredTotal": 0, "soldOutTotal": 0,
  "admissionRatePerSecond": 24.5,            // admissions over the last 10 s
  "waitMs": {"p50": 8000, "p95": 41000, "max": 52000, "count": 1090},   // join → admitted, admitted customers
  "oldestWaitingSeconds": 48.2,
  "inventory": {"available": 812, "held": 40, "soldOut": false, "observedAt": "..."} | null,
  "capacitySource": "gce | static", "capacityError": null | "<last error>",
  "admissionMode": "fixed | adaptive",
  "abandonedTotal": 12, "waitingTimeoutSeconds": 45,
  "adaptive": {"minPerServer": 5, "maxPerServer": 200, "busyHigh": 0.9, "busyLow": 0.7, "cpuHigh": 0.9, "cpuLow": 0.8,
               "decreaseFactor": 0.75, "slowStartFactor": 1.25, "cooldownSeconds": 3},
  // servers[] gains, per server:
  //   "limit": 31, "phase": "slow_start | steady | fixed",
  //   "decision": "increase | decrease | hold", "reason": "listen queue 4" | "headroom" | "no signal" | ...,
  //   "decisionAt": "...", "decreases": 3,
  //   "signals": {"busy": 0.81, "activeWorkers": 26, "totalWorkers": 32, "listenQueue": 0,
  //               "cpu": 0.74, "cpuSampledAt": "...", "observedAt": "..."} | null
  //   (activeWorkers = min(in flight, workers); listenQueue = in flight beyond the worker count)
  "updatedAt": "...",
  // high availability (section 5):
  "store": "memory | redis", "instance": "<hostname that answered>",
  "leader": true,                            // this instance runs admission now
  "leaderInstance": "<hostname of the leader>" | null   // null for a few seconds after a crash
}
```

### 3.5 Queue server configuration (environment)

| Variable | Meaning |
|---|---|
| `PORT` | listen port (default 8080) |
| `LAB_ADMIN_TOKEN` or `LAB_ADMIN_TOKEN_SECRET` | admin token, or Secret Manager resource name read at start via the metadata-server token |
| `CAPACITY_SOURCE` | `gce` (LB backend health) or `static` |
| `GCP_PROJECT_ID`, `BACKEND_SERVICE_NAME`, `INSTANCE_GROUP_URL` | for `gce`: `POST compute/v1/projects/{p}/global/backendServices/{bs}/getHealth` with `{"group": INSTANCE_GROUP_URL}` |
| `STATIC_BACKENDS` | for `static`: comma-separated base URLs (e.g. `http://ticketing-api:8080`); a backend counts as healthy while `GET /health` answers 200 |
| `APP_PORT` | app port on VM private IPs (default 8080) |
| `CAPACITY_POLL_MS` / `INVENTORY_POLL_MS` | defaults 2000 / 1000 |
| `SIGNAL_POLL_MS` / `CONTROL_INTERVAL_MS` | defaults 1000 / 1000 (adaptive mode: per-server load signals and controller step) |
| `STORE` | `memory` (default; one instance) or `redis` (any number of instances, section 5) |
| `REDIS_ADDR`, `REDIS_PASSWORD` | for `redis`: `host:port`; password optional (Memorystore without AUTH in the lab) |

### 3.5a Ticketing API load endpoints (VPC only, served by nginx without PHP)

| Method & path | Response |
|---|---|
| `GET /lab/load` | nginx `stub_status` text (`Active connections: 40 … Reading: 0 Writing: 37 Waiting: 3`) with header `X-Php-Workers: 32`. In flight = Writing − 1 (the status request itself). |
| `GET /lab/cpu` | `{"percent": 74.2, "sampled_at": "2026-10-10T17:22:01Z"}` (VM CPU, sampled every 2 s) |

Allowed only from `LOAD_ALLOW_CIDRS` (the workloads and control subnets); 403 otherwise, including
everything arriving through the load balancer.

### 3.6 Generator

* `TrafficConfig.waitingRoom = {"enabled": bool}` (absent ⇒ false).
* Env `QUEUE_BASE_URL` (default `TARGET_BASE_URL`); its host must also be in `ALLOWED_TARGET_HOSTS`.
* When enabled, **reserve and purchase journeys** become: join → long-poll status (`waitSeconds=15`)
  until not waiting → if `sold_out`: end (outcome `sold_out_in_queue`) → if `admitted`/`open`: run the
  normal checkout, sending `X-Queue-Token` on `POST /api/reservations` → always `POST /queue/release` with
  the outcome. Browse/availability journeys never queue.
* A waiting customer occupies a VU (it is a customer in flight). Queue HTTP failures are retried with
  backoff (1 s, 2 s, 4 s … max 5 s) until the journey's overall deadline (`durationSeconds` + 30 s grace).
* Queue calls are **not** counted in the bucket's `requests`, latency histogram, `byEndpoint` or
  `byInstance` (those describe the app servers). They go in a new `queue` object on each 1 s bucket:

```json
"queue": {
  "joined": 40, "admitted": 25, "soldOut": 0, "open": 0, "failed": 0,
  "waiting": 1830,                        // gauge: customers waiting at the end of this second
  "waitMs": <histogram, same format as latency>,  // join → admitted, recorded at admission
  "requests": {"total": 130, "ok": 130, "errors": 0}
}
```

### 3.7 Control plane

* `ExperimentConfig.waitingRoom = {"enabled": bool, "activeUsersPerServer": int, "admissionTtlSeconds": int,
  "mode": "fixed" | "adaptive", "maxUsersPerServer": int}` (`mode` default `fixed`; `maxUsersPerServer`
  default 200, only used in adaptive mode; sent to the queue as `admissionMode` and `adaptive.maxPerServer`)
  (default disabled; presets A–E disabled; **F enabled, adaptive**, starting limit 25, max 200, 60 s TTL).
* Prepare: `PUT /admin/config` on the queue (eventId of the new event, `soldOutGraceSeconds =
  reservationTtlSeconds + 5`) and `waiting_room_required` on the app; disabled ⇒ both off. Complete,
  reset and failure turn both off.
* Telemetry: poll `GET /admin/stats` every 1 s (freshness live ≤ 3 s) → `LiveSnapshot.waitingRoom`;
  generator `queue` buckets aggregated into the traffic view and series.
* Timeline events (from successive observations only): "Waiting room opened: N customers waiting",
  "Waiting room capacity 25 → 50 (2 healthy servers)", "Waiting room empty", "Waiting room declared sold
  out: N customers told".
* Report `measured.waitingRoom`: joined, admitted, soldOutInQueue, peakWaiting, wait p50/p95/max,
  peak capacity.
* Dashboard: a **Waiting room** panel (waiting, active / capacity, healthy servers, admission rate,
  wait p50/p95, sold out) and a chart of waiting vs admitted vs capacity over time; a toggle and
  "active users per server" field in the configuration form.

---

## 4. Preset F with the waiting room — measured in Google Cloud (2026-10-10)

Configuration: 1 → max **3** servers, CPU target 30 %, 2,000 tickets, 30 s holds, 10 % checkout
abandonment, 5,000 customers at 42/s (the 5,000th arrives at about 119 s), waiting room on with 25
active customers per healthy server. Experiment `exp_20261010_172055_a367`; raw data in
`outputs/waiting-room-2026-10-10/`.

| t (s) | What happened |
|---:|---|
| 0 | Waiting room opens; server 1 at 25/25 customers, the rest queue |
| 34 | Autoscaler 1 → 2 |
| 59 | Autoscaler 2 → 3 |
| 83 | Server 2 healthy in the LB → room capacity 25 → 50 |
| 110 | Server 3 healthy → capacity 50 → 75 (all 5,000 have joined; 2,303 waiting) |
| 85–147 | Tickets all sold or held: admissions paused, then trickled one customer per returned hold |
| 149 | `available = 0, held = 0` → **sold out declared: 2,733 waiting customers told** |
| 393–399 | Scale-in 3 → 2 → 1 |

| Measure | Without waiting room (same preset, earlier run) | With waiting room |
|---|---:|---:|
| HTTP errors | 21 % (3,469 × 503) | **0 %** |
| Failed purchases | 1,358 | **0** |
| Tickets sold | 1,988 | **2,000** |
| App latency p95 | 7.8 s | **0.65 s** |
| Customers told "sold out" without hitting the app | 0 | **2,733** |
| Invariant violations | 0 | 0 |
| Peak waiting | — | 2,743 |
| Time in queue p50 / p95 / max | — | 20 s / 39 s / 94 s |

**Reading it honestly:** the waiting room is what removed the errors; the extra servers arrived when the
sale was nearly over (server 1 served 96 % of app requests). At 25 customers per server, one
`n1-standard-1` sells ~22 tickets/s, so 2,000 tickets take ~90 s, about the same as the autoscaler's
reaction plus VM boot time. A larger sale (more tickets, or a longer arrival window) is where servers 2 and
3 would carry real traffic; the room's capacity already follows them automatically.

### Fixed vs adaptive, measured in Google Cloud (same preset F, 1 → max 3 `n1-standard-1`)

| | Fixed 25 | Adaptive, per-server limits | **Adaptive, shared limit** (current) |
|---|---:|---:|---:|
| HTTP errors | 0 % | 0 % | **0 %** |
| App latency p50 / p95 / p99 | 117 / 653 / 1,051 ms | 73 / 790 / 1,693 ms | **66 / 405 / 869 ms** |
| Peak VM CPU (Cloud Monitoring, 60 s) | 77 % | 77 % | 66 % |
| Learned limit per server | 25 (set) | 7, 39, 25 (drifted apart) | **14 on every server** |
| Tickets sold / failed purchases / violations | 2,000 / 0 / 0 | 2,000 / 0 / 0 | 2,000 / 0 / 0 |
| Told "sold out" in the queue | 2,733 | 2,741 | 2,746 |
| Time in queue p50 / p95 | 20 s / 39 s | 32 s / 48 s | 30 s / 48 s |
| Sold out declared at | t+149 s | t+174 s | t+192 s |

Experiments: `exp_20261010_172055_a367` (fixed), `exp_20261010_200934_1b48` (per-server),
`exp_20261010_202657_d839` (shared); exports in `outputs/waiting-room-2026-10-10/` and
`outputs/adaptive-2026-10-10/`.

**What the numbers say.** On a 1-vCPU VM the controller settles at ~14 customers in checkout per
server, not 25: VM CPU is the binding signal (it cuts at 90 %). That halves p95 latency (653 → 405 ms)
and p99 (1,051 → 869 ms) at the cost of ~25 % less throughput, so the sale takes ~40 s longer and people
wait ~10 s more. The thresholds (busy 90 %, CPU 90 %) are the real policy knob — how hot servers may
run — not a customer count. The shared limit beat per-server limits on every latency figure: no cold-VM
slow-start overshoot, no limits drifting apart under an even load balancer.

### How to run it

```bash
./scripts/start-load-test.sh F --confirm-expensive
```

or press **F** in the dashboard presets. The **Waiting room** panel and chart show waiting, active /
capacity and healthy servers live; the report has a `waitingRoom` section.

---

## 5. High availability

**Goal:** a restart, deploy or crash of one queue server loses nobody's place in line. The queue
servers are stateless; everything that matters lives in one Redis, and one elected instance (the
leader) runs the parts that must happen exactly once.

```mermaid
flowchart LR
    C[Customers] -->|/queue/* round robin| LB[External LB<br/>locally: nginx]
    CP[Control plane] -->|/admin/* fixed private IP| ILB[Internal passthrough LB<br/>locally: same nginx]
    LB --> Q1[Queue server 1<br/>leader]
    LB --> Q2[Queue server 2]
    LB --> QN[Queue server N]
    ILB --> Q1
    ILB --> Q2
    ILB --> QN
    Q1 <-->|Lua scripts| R[(Redis<br/>line, entries, counters,<br/>config, leader lease)]
    Q2 <--> R
    QN <--> R
    R -. pub/sub wr:events<br/>admitted ids / sold out / reset .-> Q1
    R -.-> Q2
    R -.-> QN
    Q1 -->|leader only: capacity, inventory,<br/>signals pollers| APP[App servers]
```

### Who does what

| Every instance | The leader only |
|---|---|
| `POST /queue/join`, `GET /queue/status` (long poll), `POST /queue/release` | 100 ms admission tick (one atomic Lua script: expire TTLs, drop people who stopped checking in, sold-out decision, admit from the head while active < capacity, prune) |
| `/admin/config`, `/admin/reset`, `/admin/stats` (same numbers from any instance) | adaptive controller step (1 s) |
| renders an admitted customer's token (HMAC over `q`, `e`, `exp`, so nothing is stored) | capacity (LB backend health), inventory and load-signal pollers |

Every customer operation is one Lua script, so concurrent instances can never double-admit, admit out
of order or lose a counter.

**Every script is O(batch), never O(line).** Redis runs one script at a time and blocks every other
client meanwhile. The first version walked the whole line in one script when it declared sold out or
dropped people who had left, and read every wait sample for each stats call; in a local
10,000-customer run with ~6,600 waiting that produced 100+ ms scripts, lease renewals timing out,
leadership flapping and stats failing for minutes. Now:

| Work | Bound per call |
|---|---|
| join, status, release, stats, controller | O(1) or O(log n) commands (`ZRANK`, `ZCARD`, `ZRANGE line 0 0`); whole-key reads only of bounded keys |
| admission | pops at most min(free capacity, budget, 500) with one `ZPOPMIN` |
| expiring admissions, dropping people who stopped checking in, pruning finished entries | `ZRANGEBYSCORE … LIMIT 0 500` per tick; a full batch continues on the next tick (admission waits until the abandonment backlog is gone, so nobody who left is admitted) |
| sold out | declaring is O(1): a flag makes everyone still in line `sold_out` at once (status, join, release and stats honour it), `soldOutTotal` grows by `ZCARD line`, one `*` message wakes every long poll; the line is then drained 500 per tick |
| reset / new event | `UNLINK` (big keys are freed in the background) |

Each batch uses a fixed number of multi-member commands (`HMGET`/`HSET`/`ZREM`/`ZADD` with up to 500
members), so a tick is at most ~25 Redis commands whatever the line length.
`queue/redis_scale_test.go` pins this with 12,000 people in line on miniredis (command count per
operation, and no unbounded read of a key that grows with the line); against the real Redis with
20,000 in line the slowest script was 2.3 ms server-side (`SLOWLOG`, threshold 1 ms). Positions are `ZRANK` in the line, so they are identical everywhere.

### Leader election and failover

* Lease `wr:leader` = `SET NX PX 5000` with value `<hostname>/<random>`, renewed every 1 s by a script
  that only extends the lease if it still holds it, on its own small connection pool (renewals never
  queue behind customer traffic). One slow or failed renewal does not end leadership: the leader keeps
  going while the lease it last renewed is certainly valid (4 s) and steps down only when Redis says
  the lease is someone else's, or after 4 s without a successful renewal.
* Every leader-side script is **fenced**: it first checks that `wr:leader` still holds this instance's
  value. A leader that paused (GC, network) and lost its lease cannot act on stale belief; it steps down.
* **Graceful stop** (deploy, `docker compose restart`, MIG replacement, SIGTERM): the instance answers its
  open long polls, releases the lease, and another instance takes over within one renewal interval
  (≤ 1 s).
* **Crash** (kill -9, VM lost): the lease expires; another instance takes over **within ~5 s**.
  Meanwhile joins, status and releases keep working on the other instances; only admissions pause.
* Long polls wait on a local channel per queue id. The leader publishes `w <ids…>` (admitted, removed),
  `*` (sold out, reset, new event) on `wr:events`; each instance wakes its local waiters. A missed
  message (pub/sub reconnect) only delays a waiter: every waiting long poll re-checks Redis once a
  second.

### Key layout (prefix `wr:`)

| Key | Type | Contents |
|---|---|---|
| `config` | hash | `json` (full Config), plus `enabled`, `event`, `per`, `ttl`, `grace`, `mode`, `wto`, `start` for the scripts, `gen` (bumped on every clear). Written by `PUT /admin/config` on any instance, read by all — so there is no config cache to go stale. |
| `seq` | string | ticket counter (`INCR` on join) |
| `line` | zset | waiting queue ids scored by ticket number (FIFO) |
| `seen` | zset | waiting queue id → last check-in (ms), for the abandonment sweep |
| `entries` | hash | queue id → `state,joinedMs,expMs,customerRef` (state `w`/`a`/`r`/`x`/`s`) |
| `customers` | hash | customerRef → queue id while waiting or admitted (idempotent join) |
| `active` | zset | admitted queue id → expiry (ms) |
| `finished` | zset | released/expired/sold-out id → finished at; forgotten after an hour |
| `counters` | hash | joined, admitted, released, expired, soldOut, leftWaiting, abandoned, `rel:<outcome>`, waitCount, waitMax |
| `state` | hash | sold-out flag, inventory observation and budget, zero-since, controller (limit, phase, decision, reason, decreases, last cut), sweep/prune clocks |
| `rate` | hash | admissions per 100 ms slot (last 10 s, at most 100 fields); the leader stores the sum in `state` every tick |
| `waits` | list | join → admitted samples: every sample up to 2,000, then a uniform reservoir of 2,000 (p50/p95 are exact up to 2,000 admissions, sampled beyond; `max` and `count` are always exact). The memory store keeps 200,000. |
| `signals` | hash | server name → latest load signals |
| `capacity`, `capacity:names` | hash, set | latest healthy servers (written by the leader; kept across reset) |
| `leader` | string | the lease |

A new event, `enabled:false` or `POST /admin/reset` unlinks every key above except `config`,
`capacity*` and `leader`, atomically, and wakes every waiter (they get `unknown_queue_id`, as before).

The HTTP API, token format, admission rules, adaptive controller and stats are unchanged; the room's
behavioural tests run against both stores (`queue/store_test.go`).

### Measured locally (docker compose: Redis 7, two replicas behind nginx, 2026-10-11)

| Scenario | Result |
|---|---|
| 8 customers joined through nginx (alternating replicas); stats read from each replica directly | identical `waiting 8`, `joinedTotal 8`, `leaderInstance waiting-room-1` on both |
| `docker compose restart waiting-room-1` (the leader) with a long poll in flight | the poll was answered at shutdown (client re-polls); lease released and re-acquired in 0.4 s; all 8 positions unchanged |
| `docker kill -s KILL` the leader | `waiting-room-2` became leader **5.3 s** later; line, positions and counters intact |
| Both replicas recreated at the same time | line intact (it lives in Redis); the new leader admitted the head of the line |
| Kill the leader mid-sale (capacity 2, customers long-poll and release through nginx) | admission order 1, 2, 3, *kill*, 4, 5, 6, 7, 8 — FIFO kept; customers 6–8 admitted by the new leader ~4.9 s after the kill; final `admitted 8, released 8, waiting 0` |
| 5,000 customers, 2,000 concurrent long polls, two servers on one Redis (`WR_REDIS_ADDR=… go test ./server -run RedisLoad`) | joins 14,500/s; drained in 3.1 s (memory store, one server: 2.6 s); wait p50 1.6 s / p95 2.5 s; every customer admitted exactly once |

**Preset F locally** (10,000 customers at 84/s, 4,000 tickets, adaptive room, `STORE=redis`, two
replicas behind nginx; experiment `exp_20261011_125635_4fc0`), leader killed with `kill -9` at t+46 s
when 2,037 people were waiting, restarted 20 s later:

| | Result |
|---|---|
| Failover | `waiting-room-2` became leader 5 s after the kill; admissions resumed (admitted 1,703 at t+49 s → 1,773 at t+52 s); the restarted instance rejoined as a follower; no further leadership changes |
| Errors | no `context deadline exceeded`, no lost leadership, no `store error` in either replica's log during the run |
| Sale | 4,000 sold, 0 held at the end; peak waiting 6,113; 4,477 admitted; sold out declared at t+176 s (when the last holds resolved): **5,523 told** without touching the app (4,477 + 5,523 = 10,000) |
| Control plane | `/api/state` waiting-room view `live` in every 3 s sample for the whole run |
| Redis `SLOWLOG` (> 10 ms) | 25 entries, max 138 ms — all of them the O(log n) status script, or single commands inside it (one `HGET` at 13.7 ms, one `ZADD` at 111 ms), while the 4-vCPU Docker VM was saturated by the app server, the generator and nginx. Script latency over the whole Redis uptime: p50 40 µs, p99 0.68 ms, p99.9 4.4 ms; `HGET` p99.9 32 µs. These outliers are Redis being descheduled, not script cost; on GCP Memorystore has its own vCPU. |

Before the O(batch) rework, the same preset made scripts take 100+ ms, lease renewals time out,
leadership flap every few seconds and the control plane's waiting-room view freeze for ~6 minutes.

Not measured: anything in Google Cloud (below).

### Google Cloud

* Queue VMs: a zonal **managed instance group** of `queue_replicas` (default 2) `e2-small` COS VMs from
  an instance template; the image is part of the template, so a new image rolls out one VM at a time
  (`max_surge 1`, `max_unavailable 0`) and nobody loses their place. Autohealing recreates a VM whose
  `/queue/health` stops answering (initial delay 180 s).
* Customers: the existing external load balancer's `/queue/*` backend service points at the MIG
  (balancing mode `RATE`, because the same instance group also backs the internal load balancer and
  `UTILIZATION` cannot be shared with another mode).
* Control plane: an **internal passthrough load balancer** on the same fixed private IP as before
  (`${prefix}-queue-ip`), so `WAITING_ROOM_URL` / `module.waiting_room[0].private_url` is unchanged.
* Redis: **Memorystore for Redis** `BASIC`, 1 GB, Redis 7.2, `maxmemory-policy noeviction`,
  `authorized_network` = lab VPC, `connect_mode = DIRECT_PEERING`, reserved range `10.60.32.0/29`.
  Private Service Access is not an option: the deploying account lacks
  `servicenetworking.services.addPeering`. Direct peering is created by Memorystore itself and is
  **assumed** to work with this account — unverified until the next deploy. No firewall rule is
  needed (VPC egress is open; the Redis side is Google-managed); the existing rules already admit the
  load balancers' health checks to the queue VMs by tag.
* BASIC has no replica: a Memorystore failure or maintenance loses the line (as a queue VM restart did
  before). `redis_tier = "STANDARD_HA"` adds a replica with automatic failover, at about twice the price.
* Fallback: `waiting_room_store = "memory"` deploys one VM, no Memorystore, no Redis API.
* Cost (list, europe-west2): Memorystore BASIC 1 GB ≈ **$0.06/h**, one more `e2-small` ≈ **$0.02/h**;
  the internal forwarding rule adds forwarding-rule charges (small; not measured).
* Teardown: `redis.googleapis.com` is in the APIs the lab disables, and `teardown.sh` checks that no
  `ticketing-scale-lab*` Memorystore instance is left.
* **Untested in Google Cloud** until the next deploy: Memorystore direct peering, the internal
  passthrough load balancer sharing the MIG with the external one, and MIG rolling updates of the queue.
