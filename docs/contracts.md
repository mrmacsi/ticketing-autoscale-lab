# Component contracts

This file is the single source of truth for the interfaces between the four runtime components:

```text
dashboard (Vue)  ──HTTP/SSE──▶  control-api (Cloud Run, Node/TS)
control-api      ──Firestore─▶  load-generator (Go, own VM)      [commands]
load-generator   ──Firestore─▶  control-api                      [1 s traffic buckets, status]
load-generator   ──HTTP──────▶  ticketing-api via external ALB   [synthetic customers]
control-api      ──HTTP(private IP)▶ ticketing-api /api/admin/*, /instance-info
control-api      ──SQL (read-only user)▶ Cloud SQL               [authoritative inventory]
control-api      ──Compute / Monitoring APIs▶ MIG, autoscaler, CPU
```

All timestamps are UTC. JSON timestamps are ISO-8601 strings; Firestore timestamps are native `Timestamp` values.

---

## 1. Ticketing API (Laravel, behind the load balancer)

Every response carries:

| Header | Meaning |
|---|---|
| `X-Served-By` | Safe instance name of the VM that handled the request (e.g. `ticketing-scale-lab-app-x7k2`). `local` when not on GCE. |
| `X-App-Version` | Application image version. |
| `Idempotent-Replayed: true` | Present when an idempotent POST returned a stored earlier result. |

Error body shape: `{"error": "<code>", "message": "<human text>"}`.

### Public endpoints

| Method & path | Notes | Success | Business errors |
|---|---|---|---|
| `GET /api/events` | latest 20 events with categories | 200 `{data:[Event]}` | |
| `GET /api/events/{id}` | | 200 `{data:Event}` | 404 |
| `GET /api/events/{id}/availability` | approximate (read committed snapshot) | 200 `{data:{event_id, sold_out, categories:[{id,name,price_cents,capacity,available,held}], as_of}}` (`held` = tickets in other customers' checkouts, which may come back on sale) | 404 |
| `POST /api/reservations` | header `Idempotency-Key` required; body `{category_id, quantity (1..8), customer_ref}`. While the setting `waiting_room_required` is true, header `X-Queue-Token` (waiting-room admission token, [waiting-room.md §3.2](waiting-room.md)) is also required and is checked **before** the Idempotency-Key and its replay (a replayed key still needs a valid token); while false the header is ignored. **One admission pass = one reservation**: while the waiting room is in force the token's queue id `q` is stored on the reservation (unique, in the same transaction as the inventory hold), so a second, different reservation with the same pass is rejected even when both arrive at once; replaying the original Idempotency-Key still returns the original reservation. A rejected attempt (e.g. `sold_out`) does not use the pass up. `expires_at` is `now + reservation_ttl_seconds`, or `now + reservation_ttl_near_sellout_seconds` when that setting is > 0 and the category has at most `near_sellout_threshold` tickets left for sale after this hold | 201 `{data:Reservation}` (200 on replay) | 409 `sold_out` / `insufficient_inventory`; 409 `queue_token_used` (this pass already produced another reservation; no inventory change; join the queue again); 422 `idempotency_key_reused`; 403 `queue_token_required` (header missing) / `queue_token_invalid` (bad format or signature) / `queue_token_expired` (`exp` < now) / `queue_token_wrong_event` (token `e` ≠ event of `category_id`; no inventory change) |
| `GET /api/reservations/{id}` | | 200 | 404 |
| `POST /api/orders` | header `Idempotency-Key`; body `{reservation_id, customer_ref}`; one order per reservation | 201 `{data:Order}` (200 on replay or when the order for the reservation already exists) | 409 `reservation_not_active`; 410 `reservation_expired` |
| `POST /api/orders/{id}/simulate-payment` | header `Idempotency-Key`; body optional `{outcome: "approve"|"decline", delay_ms}` (forced outcome only honoured when lab fault injection is enabled) | 200 `{data:Order}` with status `paid` and tickets | 402 `payment_declined` (`data` = order, retry allowed with a new key); 410 `reservation_expired`; 409 `payment_in_progress` |
| `GET /api/orders/{id}` | | 200 | 404 |
| `GET /api/tickets/{id}` | | 200 | 404 |
| `GET /api/sales/summary?event_id=` | totals per category | 200 | |
| `GET /api/lab/cpu-work?ms=N` | **demo-only synthetic CPU work**, bounded by the runtime setting `synthetic_cpu_max_ms`; never touches inventory | 200 `{data:{requested_ms, performed_ms}}` | 403 `synthetic_cpu_disabled` |
| `GET /health` | readiness (DB reachable, schema migrated) — used by the load balancer | 200 / 503 | |
| `GET /health/live` | liveness (process up) — used by MIG autohealing | 200 | |
| `GET /instance-info` | `{instance, version, zone, ready, started_at, guest_cpu:{percent, sampled_at}, php_fpm:{active, idle, total}?}` | 200 | |

Types:

```text
Event        {id, name, venue, starts_at, created_at, categories:[Category]}
Category     {id, event_id, name, price_cents, capacity}
Reservation  {id, event_id, category_id, quantity, status: active|payment_processing|consumed|expired, customer_ref, expires_at, created_at}
Order        {id, reservation_id, event_id, status: pending|paid|payment_failed|cancelled, amount_cents, payment_attempts, tickets:[Ticket], created_at, paid_at}
Ticket       {id, order_id, event_id, category_id, code, holder_ref, issued_at}
```

IDs are UUID strings.

### Private admin endpoints

Reachable only from inside the VPC (nginx denies the load balancer's proxy ranges) **and** require `Authorization: Bearer <LAB_ADMIN_TOKEN>` (Secret Manager).

| Method & path | Body | Result |
|---|---|---|
| `POST /api/admin/experiments/prepare` | `{event_name, categories:[{name, price_cents, capacity}], settings:{reservation_ttl_seconds, payment_delay_min_ms, payment_delay_max_ms, payment_failure_probability, synthetic_cpu_max_ms, waiting_room_required, reservation_ttl_near_sellout_seconds, near_sellout_threshold}}` (every settings key optional; same validation as `PUT /api/admin/settings`, applied before the event is created) | 201 `{data:{event, settings}}`; 422 `validation_failed` |
| `POST /api/admin/events/{id}/reset-inventory` | – | 200 `{data:{event_id, released_reservations, deleted_orders, deleted_tickets}}` |
| `GET /api/admin/settings` / `PUT /api/admin/settings` | settings object (partial on PUT): `reservation_ttl_seconds`, `payment_delay_min_ms`, `payment_delay_max_ms`, `payment_failure_probability`, `synthetic_cpu_max_ms`, `waiting_room_required` (boolean, default `false`; gates `POST /api/reservations` on `X-Queue-Token`), `reservation_ttl_near_sellout_seconds` (integer, `0` = off (default) or 5..3600; shorter hold for reservations that leave at most `near_sellout_threshold` tickets of their category for sale; never longer than `reservation_ttl_seconds`), `near_sellout_threshold` (integer tickets 0..1,000,000, default `0`, i.e. only the hold that takes the last tickets). Every instance picks up a change within ~2 s (settings cache) | 200 `{data: settings}`; 422 `validation_failed` |
| `GET /api/admin/events/{id}/invariants` | – | 200 `{data:{violations:[...], checked_at}}` |

---

## 2. Firestore (named database `ticketing-scale-lab`, Native mode)

### `generator/command` (written by control-api only)

```json
{
  "seq": 17,                       // monotonically increasing; generator acts on seq > lastCommandSeq
  "action": "start" | "stop",
  "experimentId": "exp_20261009_1530_ab12",
  "config": TrafficConfig,         // present for start
  "issuedAt": Timestamp,
  "issuedBy": "user@example.com"
}
```

### `generator/status` (written by load-generator only, heartbeat every 2 s)

```json
{
  "agentId": "ticketing-scale-lab-loadgen/boot-<uuid>",
  "state": "idle" | "running" | "stopping" | "error",
  "experimentId": "exp_..." | null,
  "lastCommandSeq": 17,
  "heartbeatAt": Timestamp,
  "runStartedAt": Timestamp | null,
  "message": "free text, e.g. last error",
  "version": "1.0.0",
  "limits": {"maxRate": 500, "maxVUs": 2000, "maxDurationSeconds": 1800},
  "targetBaseUrl": "http://34.x.x.x"
}
```

### `experiments/{experimentId}` (control-api owns the document; generator only writes the `generator` map)

```json
"generator": {
  "owner": "<agentId>",
  "leaseExpiresAt": Timestamp,     // renewed every 5 s while running; a generator may claim only when empty or expired
  "state": "running" | "finished",
  "startedAt": Timestamp,
  "finishedAt": Timestamp | null,
  "finishReason": "completed" | "stopped" | "max_duration" | "total_arrivals_reached" | "error",
  "message": "..."
}
```

### `experiments/{experimentId}/traffic/{second}` (load-generator, one doc per elapsed second, doc id zero-padded to 6 digits)

```json
{
  "second": 42,                     // offset from run start
  "ts": Timestamp,                  // wall clock start of this second
  "targetRate": 37.5,               // arrivals/s requested by the traffic pattern at this second
  "scheduledArrivals": 38,          // arrivals the open-loop scheduler emitted in this second
  "startedArrivals": 38,            // journeys that obtained a VU slot and started
  "droppedArrivals": 0,             // scheduled but no VU free (generator saturation, NOT app capacity)
  "completedJourneys": 35,
  "activeVUs": 51, "maxActiveVUs": 60,
  "schedulerLagMsMax": 1.2,         // worst delay between scheduled and actual dispatch time
  "requests": {"total": 120, "ok2xx": 110, "conflict409": 6, "gone410": 1, "payment402": 1,
               "client4xx": 0, "server5xx": 2, "networkErrors": 0, "timeouts": 0},
  "byEndpoint": {"browse": {"count": 50, "errors": 0}, "availability": {...}, "reservation": {...},
                 "order": {...}, "payment": {...}, "cpu_work": {...}},
  "latency": {"count": 120, "sumMs": 5400.2, "maxMs": 310.0, "p50": 31.4, "p95": 120.1, "p99": 290.3,
              "hist": {"36": 10, "37": 22}},   // see histogram definition below
  "instances": {"ticketing-scale-lab-app-x7k2": {"requests": 60, "errors": 1, "latencySumMs": 2500.0}},
  "business": {"reservationsAttempted": 12, "reservationsSucceeded": 10, "reservationsRejected": 2,
               "ordersCreated": 6, "paymentsAttempted": 6, "purchasesCompleted": 5, "paymentsDeclined": 1,
               "purchasesFailed": 0, "expiredDuringCheckout": 0, "abandoned": 4,
               "duplicateSubmissions": 1, "idempotentReplays": 1, "ticketsPurchased": 9},
  "synthetic": {"requests": 0, "cpuMsRequested": 0},
  "queue": {"joined": 40, "admitted": 25, "soldOut": 0, "open": 0, "failed": 0,   // waiting room, see below
            "waiting": 1830,
            "waitMs": {"count": 25, "sumMs": 203515.2, "maxMs": 9012.4, "p50": 8556.7, "p95": 8556.7, "p99": 9412.3,
                       "hist": {"94": 11, "95": 13, "96": 1}},
            "requests": {"total": 130, "ok": 130, "errors": 0}}
}
```

Rules:

* Requests are attributed to the bucket in which they **completed**. Journeys are attributed to the bucket in which they **started**.
* `409` = expected business rejection (sold out / not active), `410` = reservation expired, `402` = simulated payment decline. None of these are application errors. Application errors are `server5xx + networkErrors + timeouts` (+ unexpected `client4xx`).
* Latency histogram: bucket index `i = ceil(ln(ms) / ln(1.1))`, clamped to `[0, 250]`; `ms <= 1` goes to bucket 0. The representative value of bucket `i` is `1.1^i` ms (upper bound, ≤10 % error). Buckets are summed across seconds to compute experiment-wide percentiles; per-bucket `p50/p95/p99` are convenience copies.
* `queue` ([waiting-room.md §3.6](waiting-room.md)) is present in every bucket (all zeros when the waiting room is off).
  Queue HTTP calls are counted **only** in `queue.requests`, never in `requests`, `latency`, `byEndpoint` or `instances`.
  * `joined`: customers whose join succeeded (once per customer, even if they had to re-join after the queue lost
    their entry). `admitted` / `soldOut` / `open`: the queue's decision for a customer, counted when it is observed.
    `failed`: customers the queue rejected with a non-retryable error (4xx such as `unknown_event`); transient
    failures are retried and only show in `requests.errors`.
  * `waiting` is a **gauge**: customers waiting in line when the bucket was closed (not a sum; take the max or the
    last value across seconds).
  * `waitMs` is a histogram of join → admitted time (from sending the successful join to observing `admitted`) in
    exactly the latency format, recorded in the second of admission.
  * `requests`: `ok` = 2xx with a readable body; `errors` = any other status, network error or timeout (including
    failed best-effort releases). Requests aborted by a stop are not counted.

### TrafficConfig (inside `generator/command.config`)

```json
{
  "experimentId": "exp_...",
  "eventId": "<uuid>",
  "seed": 42,
  "durationSeconds": 600,
  "maxVUs": 500,
  "totalArrivals": 0,                       // 0 = unlimited; otherwise stop scheduling once reached
  "requestTimeoutMs": 10000,
  "pattern": {
    "type": "constant" | "ramp_up" | "spike" | "ramp_down" | "random_bursts" | "custom",
    "rate": 20,                              // constant
    "startRate": 5, "endRate": 100,          // ramp_up / ramp_down (linear over the duration)
    "baseRate": 5, "peakRate": 200,          // spike / random_bursts
    "spikeAtSeconds": 30, "spikeDurationSeconds": 20,
    "burstCount": 5, "burstDurationSeconds": 10,   // random_bursts: positions derived from the seed
    "points": [{"atSeconds": 0, "rate": 5}, {"atSeconds": 120, "rate": 80}]   // custom: piecewise linear
  },
  "mix": {"browse": 40, "availability": 20, "reserve": 25, "purchase": 15},   // integer percentages, sum 100
  "behaviour": {
    "maxQuantity": 4,
    "thinkTimeMinMs": 200, "thinkTimeMaxMs": 1500,
    "abandonCheckoutProbability": 0.1,       // purchase journeys that stop before paying
    "duplicateSubmitProbability": 0.05,      // re-send the same POST with the same Idempotency-Key
    "retryPaymentAfterDeclineProbability": 0.5,
    "syntheticCpuMs": 0,                     // >0 adds GET /api/lab/cpu-work?ms=N to a journey
    "syntheticCpuProbability": 0
  },
  "waitingRoom": {"enabled": false}          // optional; absent or null = disabled (waiting-room.md §3.6)
}
```

Journeys (arrival = one synthetic customer with a seeded random identity `cust-<seed>-<n>`):

* **browse**: `GET /api/events` → think → `GET /api/events/{eventId}`.
* **availability**: `GET /api/events/{eventId}/availability`.
* **reserve**: availability → `POST /api/reservations` → abandon (reservation later expires).
* **purchase**: availability → reservation → think → `POST /api/orders` → think → `POST /api/orders/{id}/simulate-payment` (on decline, maybe retry with a new key; on timeout retry once with the **same** key).
* **with `waitingRoom.enabled`**, reserve and purchase journeys first queue: `POST /queue/join` → long-poll
  `GET /queue/status/{id}?waitSeconds=15` while `waiting` → `sold_out`: the journey ends without any application
  request → `admitted`: the journey above with `X-Queue-Token` on every `POST /api/reservations` attempt (retry and
  duplicate included) → `open`: the journey above without a token → `POST /queue/release` with
  `purchased | sold_out | abandoned | failed`. Queue failures (network, timeout, 5xx, 429) are retried with backoff
  1 s, 2 s, 4 s, 5 s … until the journey deadline (`durationSeconds` + 30 s; also when `totalArrivals` is reached
  earlier). A waiting customer holds a VU. Browse and availability journeys never queue. The queue base URL is the
  generator's `QUEUE_BASE_URL` (default `TARGET_BASE_URL`), which must also pass `ALLOWED_TARGET_HOSTS`.

---

## 3. Control API (served with the dashboard on Cloud Run)

Authentication: every `/api/*` route requires a verified Google identity in the allowlist (see `docs/architecture.md`). `GET /api/me` → `{email, authMode}`.

| Method & path | Purpose |
|---|---|
| `GET /api/meta` | limits, approved max instances, defaults, presets, cost model, target URL |
| `GET /api/state` | `LiveSnapshot` (same as SSE `tick.snapshot`) |
| `GET /api/stream` | SSE stream (below) |
| `POST /api/experiments` | create + prepare; body `ExperimentConfig`; 409 if another experiment is active; 428 `confirmation_required` if expensive and `confirmExpensive` is not true |
| `POST /api/experiments/{id}/start` | only from `ready` |
| `POST /api/experiments/{id}/stop` | hard stop traffic (`running` → `stopping` → `cooling_down`) |
| `POST /api/experiments/{id}/complete` | end the cool-down observation → `completed`, builds report (the scale-in that follows is still observed, see below) |
| `POST /api/experiments/{id}/reset-inventory` | rejected (409) while `running`/`stopping` |
| `POST /api/reset` | abort/forget current experiment, restore autoscaler baseline, → `idle` |
| `GET /api/experiments` | history list |
| `GET /api/experiments/{id}` | experiment document |
| `GET /api/experiments/{id}/series` | `Series` |
| `GET /api/experiments/{id}/report` | `Report` JSON |
| `GET /api/experiments/{id}/export.json` / `export.csv` | downloads |

### ExperimentConfig

```json
{
  "name": "Scenario B – increasing traffic",
  "preset": "B" | null,
  "servers": {"initial": 1, "min": 1, "max": 5, "cpuTarget": 0.6, "initPeriodSeconds": 60, "scaleInWindowSeconds": 240},
  "tickets": {"eventName": "Lab Arena Night", "totalInventory": 100000, "categoryCount": 3, "pricesCents": [4500, 8000, 15000],
              "reservationTtlSeconds": 120, "paymentDelayMinMs": 100, "paymentDelayMaxMs": 400, "paymentFailureProbability": 0.05,
              "nearSelloutTtlSeconds": 0, "nearSelloutThreshold": 0},
  "traffic": TrafficConfig without experimentId/eventId/waitingRoom,
  "syntheticCpuMaxMs": 0,
  "cooldownObserveSeconds": 900,
  "waitingRoom": {"enabled": false, "activeUsersPerServer": 25, "admissionTtlSeconds": 60,
                  "mode": "fixed" | "adaptive", "maxUsersPerServer": 200},
  "confirmExpensive": false
}
```

`tickets.nearSelloutTtlSeconds` / `tickets.nearSelloutThreshold` (optional, default 0 = off, so configs stored before
them still parse): shorter holds for the last N tickets, so abandoned checkouts near the end return them faster. Ranges:
TTL 0 or 5–3600 and, when > 0, ≤ `reservationTtlSeconds` (422 `tickets.nearSelloutTtlSeconds` otherwise); threshold
0–1,000,000 tickets. **Prepare** always sends both in the lab settings of `POST /api/admin/experiments/prepare` as
`reservation_ttl_near_sellout_seconds` and `near_sellout_threshold` (0 included, so a previous experiment's value never
carries over). Presets A–E: off; preset F: 10 s for the last 200 tickets.

`waitingRoom` (docs/waiting-room.md 3.7) is optional on input and defaults to the value shown (`mode: "fixed"`), so
experiments stored before it existed still parse; `mode` (default `fixed`) and `maxUsersPerServer` (default 200) are
optional too, for settings stored before adaptive admission. Ranges: `activeUsersPerServer` 1–1000, `admissionTtlSeconds`
10–3600, `maxUsersPerServer` 1–1000. `mode: "fixed"`: capacity = healthy servers × `activeUsersPerServer`.
`mode: "adaptive"` (docs/waiting-room.md 2a): `activeUsersPerServer` is each new server's **starting** limit, the queue
learns each server's limit from PHP-FPM workers busy, listen queue and VM CPU, never above `maxUsersPerServer`; capacity =
sum of the healthy servers' limits. In adaptive mode `maxUsersPerServer < activeUsersPerServer` is a 422
(`waitingRoom.maxUsersPerServer`). Presets A–E are disabled; preset F is enabled in adaptive mode (start 25, max 200
per server, 60 s TTL). With `enabled: true` the API answers 422
`{"waitingRoom.enabled": "The waiting room is not deployed …"}` when the control API has no `WAITING_ROOM_URL`.

Waiting-room lifecycle (best effort where noted; the queue is reached on `WAITING_ROOM_URL` with `LAB_ADMIN_TOKEN`):

* **Prepare** sends `waiting_room_required: <enabled>` with the other lab settings in `POST /api/admin/experiments/prepare`,
  then `PUT {WAITING_ROOM_URL}/admin/config` with `{enabled, eventId: <new event>, activeUsersPerServer, admissionTtlSeconds,
  soldOutGraceSeconds: min(3600, reservationTtlSeconds + 5)}`; when enabled the body also carries
  `admissionMode: <mode>` and `adaptive: {maxPerServer: <maxUsersPerServer>}` (a fixed-mode room sends
  `admissionMode: "fixed"`; the other adaptive tunables keep the queue's defaults). Enabled: a failed queue call fails the
  preparation. Disabled: the queue is switched off too (best effort, skipped when not deployed), with the body unchanged
  (no `admissionMode` / `adaptive`).
* **Start** adds `waitingRoom: {enabled}` to the generator's `TrafficConfig`.
* **Reset inventory** also calls `POST /admin/reset` on the queue when the experiment uses it (best effort).
* **Complete, reset and failure** send `PUT /admin/config` with `enabled: false` (eventId of the experiment when known) and
  `PUT /api/admin/settings {"waiting_room_required": false}`; failures are logged and recorded on the timeline but never
  block the cleanup.

Scale-in after completion (`report.measured.scaleIn`):

* At completion the report is built as before, with `measured.scaleIn` decided right away when it already is:
  `unavailable` (no Compute API / never observed), `not_needed` (the group never had more than `servers.min` VMs), or
  `complete` (already back at `servers.min` — `completedAt` is the first of the trailing recorded scaling points at the
  minimum). Otherwise it is `null` and the control plane keeps watching the instance group (the collector's MIG
  observations, every `POLL_MIG_MS`) for at most 15 min until target size and VM count are ≤ `servers.min` with no VM
  being deleted. Meanwhile scaling points and the detector's `scaling` / `instance` / `health` events are still recorded
  for the completed experiment (events carry `data.afterCompletion: true`, attribution as usual); the autoscaler baseline
  is restored at completion as before, so the scale-in runs with the baseline's stabilization period.
* When the watch ends a `scaling` event `Scale-in after completion: …` is added and the stored report is updated in place:
  `measured.scaleIn` is set and `scalingTimeline` re-read from the timeline (the rest of the report is unchanged).
  Outcomes: `complete`, `timed_out` (15 min), `superseded` (a new experiment was created or the lab was reset first;
  `note` says which). A control-plane restart ends the watch; the latest report is then marked `unavailable` with a note.

### SSE `/api/stream`

* `event: tick` every 1 s: `{snapshot: LiveSnapshot, append: {traffic: TrafficPoint[], scaling: ScalingPoint[], cpu: CpuPoint[], inventory: InventoryPoint[], waitingRoom: WaitingRoomPoint[]}}` — `append` contains only points that are new since the previous tick.
* `event: timeline`: `TimelineEvent` as it is recorded.
* `event: experiment`: experiment document after a state change.
* comment heartbeat `: ping` every 15 s.

### Types

```ts
type Freshness = { status: 'live' | 'delayed' | 'stale' | 'missing'; lastAt: string | null; ageSeconds: number | null; note?: string };

interface LiveSnapshot {
  serverTime: string;
  experiment: Experiment | null;
  mig: {
    observedAt: string; name: string; zone: string;
    targetSize: number; actualCount: number; runningCount: number; healthyCount: number; isStable: boolean;
    autoscaler: { minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number;
                  scaleInWindowSeconds: number | null; recommendedSize: number | null; status: string; messages: string[] };
    baseline: { minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number; scaleInWindowSeconds: number };
    overridesActive: boolean;
    approvedMaxInstances: number;
  } | null;
  instances: InstanceView[];
  traffic: {
    second: number | null; targetRate: number | null; achievedArrivalRate: number | null;
    rps: number | null; successRps: number | null; errorRps: number | null; businessRejectRps: number | null;
    p50: number | null; p95: number | null; p99: number | null; activeVUs: number | null; dropped: number | null;
    // latest generator bucket `queue` object; null when absent (older generator, no traffic, stale)
    queueWaiting: number | null; queueJoinedRate: number | null; queueAdmittedRate: number | null; queueWaitP95: number | null;
    generator: { state: string; heartbeatAgeSeconds: number | null; message: string | null };
  };
  inventory: { observedAt: string; eventId: string; capacity: number; available: number; held: number; sold: number;
               activeReservations: number; paymentProcessing: number; expiredReservations: number; ticketsIssued: number;
               invariantViolations: number; categories: Array<{id: string; name: string; capacity: number; held: number; sold: number}> } | null;
  // Latest queue-server observation (GET /admin/stats polled every POLL_WAITING_ROOM_MS, default 1 s); null until observed.
  waitingRoom: (WaitingRoomStats & { observedAt: string; freshness: Freshness }) | null;
  cpu: { average: number | null; sampleTime: string | null; target: number | null };
  // waitingRoom: live ≤ 3 s, delayed ≤ 15 s, then stale; missing (note "Waiting room not deployed …") without WAITING_ROOM_URL
  freshness: { traffic: Freshness; mig: Freshness; cpu: Freshness; inventory: Freshness; lb: Freshness; waitingRoom: Freshness };
}

// docs/waiting-room.md 3.4 `Stats`; absent gauges (waitMs percentiles, oldestWaitingSeconds, every adaptive field and
// signal) are null, never 0. A queue older than adaptive admission reports admissionMode/adaptive null and servers
// with only name/ip (the rest null).
interface WaitingRoomStats {
  enabled: boolean; eventId: string | null; soldOut: boolean;
  waiting: number; active: number; capacity: number; healthyServers: number; servers: WaitingRoomServer[];
  activeUsersPerServer: number;                       // adaptive mode: starting limit of a new server
  admissionMode: 'fixed' | 'adaptive' | null;
  adaptive: { minPerServer: number | null; maxPerServer: number | null; busyHigh: number | null; busyLow: number | null;
              cpuHigh: number | null; cpuLow: number | null; decreaseFactor: number | null; slowStartFactor: number | null;
              cooldownSeconds: number | null } | null;
  joinedTotal: number; admittedTotal: number; releasedTotal: number; expiredTotal: number; soldOutTotal: number;
  admissionRatePerSecond: number;
  waitMs: { p50: number | null; p95: number | null; max: number | null; count: number };
  oldestWaitingSeconds: number | null;
  inventory: { available: number; soldOut: boolean; observedAt: string } | null;
  capacitySource: string; capacityError: string | null; updatedAt: string | null;
}

interface WaitingRoomServer {
  name: string; ip: string;
  limit: number | null;                               // learned (adaptive) or fixed per-server limit
  phase: 'slow_start' | 'steady' | 'fixed' | null;
  decision: 'increase' | 'decrease' | 'hold' | null; reason: string | null; decisionAt: string | null;
  decreases: number | null;                           // cuts since the queue was configured for this event
  signals: { busy: number | null;                     // PHP-FPM workers busy, fraction 0..1
             activeWorkers: number | null; totalWorkers: number | null;
             listenQueue: number | null;              // requests waiting for a free worker
             cpu: number | null;                      // VM CPU, fraction 0..1
             observedAt: string | null } | null;      // null: no signal from this server
}

interface InstanceView {
  name: string; id: string; zone: string; privateIp: string | null; createdAt: string | null;
  vmStatus: string;            // Compute Engine instanceStatus (PROVISIONING, STAGING, RUNNING, STOPPING, TERMINATED…)
  currentAction: string;       // MIG currentAction (CREATING, VERIFYING, NONE, DELETING, RECREATING…)
  migHealth: string | null;    // MIG health-check state (HEALTHY, UNHEALTHY, TIMEOUT, UNKNOWN, DRAINING)
  lbHealth: string | null;     // backend service health state (HEALTHY, UNHEALTHY, DRAINING) or null if unknown
  phase: 'starting' | 'healthy' | 'unhealthy' | 'draining' | 'terminating' | 'unknown';
  cpu: { value: number | null; sampleTime: string | null; source: 'cloud_monitoring' };
  guestCpu: { value: number | null; sampleTime: string | null; source: 'instance_info' } | null;
  traffic: { rps: number; avgLatencyMs: number | null; errorRate: number | null; windowSeconds: number };
  appVersion: string | null; ready: boolean | null;
}

interface TrafficPoint   { t: number; second: number; targetRate: number; scheduled: number; achieved: number; rps: number;
                           ok: number; errors: number; business: number; p50: number|null; p95: number|null; p99: number|null;
                           dropped: number; activeVUs: number;
                           reservationsAttempted: number; reservationsSucceeded: number; purchasesCompleted: number;
                           purchasesFailed: number; rejectedSoldOut: number; httpErrors: number;
                           // bucket `queue` object (null when the bucket has none); wait p95 in ms
                           queueJoined: number|null; queueAdmitted: number|null; queueSoldOut: number|null;
                           queueWaiting: number|null; queueWaitP95: number|null }
interface ScalingPoint   { t: number; actual: number; running: number; healthy: number; target: number; min: number; max: number }
interface CpuPoint       { t: number; instance: string | '__avg__'; value: number }   // t = Cloud Monitoring sample end time, NOT poll time
interface InventoryPoint { t: number; capacity: number; available: number; held: number; sold: number; expired: number; rejected: number }
// Queue-server observation, recorded only while an experiment is active and the room serves its event; t = receive time.
interface WaitingRoomPoint { t: number; waiting: number; active: number; capacity: number; healthyServers: number;
                             admissionRate: number; joinedTotal: number; admittedTotal: number; soldOutTotal: number; soldOut: boolean;
                             waitP50Ms: number | null; waitP95Ms: number | null; waitMaxMs: number | null;
                             // adaptive mode only (null in fixed mode; absent in points recorded before adaptive admission):
                             // learned limit and cumulative cut counter per server name with a limit, and their avg/min/max
                             limits: Record<string, number> | null; decreases: Record<string, number> | null;
                             limitAvg: number | null; limitMin: number | null; limitMax: number | null }

interface TimelineEvent {
  id: string; t: string;
  kind: 'experiment' | 'traffic' | 'scaling' | 'instance' | 'health' | 'cpu' | 'inventory' | 'config' | 'error' | 'waiting_room';
  source: 'control_plane' | 'generator' | 'compute_api' | 'cloud_monitoring' | 'database' | 'load_balancer' | 'waiting_room';
  attribution?: 'autoscaler' | 'baseline' | 'operator' | null;   // who caused a capacity change
  message: string; data?: Record<string, unknown>;
}

interface Series { traffic: TrafficPoint[]; scaling: ScalingPoint[]; cpu: CpuPoint[]; inventory: InventoryPoint[];
                  waitingRoom: WaitingRoomPoint[]; events: TimelineEvent[] }
```

Waiting-room timeline events (`kind` and `source` `waiting_room`) come only from successive queue observations for the
experiment's event; the first observation is a starting state:

* `Waiting room opened: N customers waiting` — waiting went from 0 to N.
* `Waiting room capacity 25 → 50 (2 healthy servers)` — fixed mode: capacity changed between two observations.
  Adaptive mode: only when the number of healthy servers changed (learned limits move capacity every second).
* `Adaptive limit app-ab12 cut 40 → 30 (listen queue 4)` — adaptive mode: a server's cut counter rose (or, without a
  counter, its limit fell with decision `decrease`); limit before → after and the queue's reason. At most one per server
  per 10 s (`data.cutsNotReportedBefore` counts cuts skipped in between). Server names are shortened to their last two
  `-` segments.
* `Adaptive limit app-ab12 left slow start at 49` — adaptive mode, once per server: phase went `slow_start` → `steady`;
  N is the limit slow start had reached (the previous observation; `data.limitNow` is the limit after that step).
* A server seen for the first time is a starting state for these per-server events.
* `Waiting room empty` — nobody waiting for 3 successive observations (≈ 3 s; `data.emptySince` is the first one).
* `Waiting room declared sold out: N customers told` — `soldOut` became true; N = `soldOutTotal` (or the line just before).

Experiment states: `idle | preparing | ready | running | stopping | cooling_down | completed | failed`.

### Units and shapes settled during integration

* **CPU is always a fraction 0..1** (`InstanceView.cpu.value`, `guestCpu.value`, `cpu.average`, `CpuPoint.value`,
  report `peakCpu.value`), the same unit as the autoscaler's `cpuTarget`. The ticketing API's `/instance-info`
  reports `guest_cpu.percent` (0..100); the control API divides by 100.
* **422 `details`** is a map of dotted config path → message, e.g. `{"servers.max": "Number must be less than or equal to 5"}`.
* **`GET /api/experiments`** returns rows `{id, name, preset, state, createdAt, trafficStartedAt, trafficStoppedAt,
  completedAt, durationSeconds, peakRps, maxServers, ticketsSold, invariantViolations, minServers, scaleIn}` (report values
  are `null` until completed; `scaleIn` stays `null` while the scale-in after completion is still observed; `minServers` =
  `config.servers.min`).
* **`GET /api/experiments/{id}/export.csv?kind=timeseries|events`** — per-second time series (default) or the timeline.
  The time series ends with `queue_waiting, queue_active, queue_capacity, queue_admission_rate_per_s, queue_healthy_servers`
  (latest queue-server observation at that second) and `queue_joined, queue_admitted, queue_sold_out, queue_wait_p95_ms`
  (generator bucket), then `queue_limit_avg, queue_limit_min, queue_limit_max` (learned per-server limits across the
  healthy servers, adaptive mode only); cells are empty when not measured.
* The cost model's application-VM row has `perAppServer: true` (every other row `false`); the dashboard multiplies only
  that row by the live VM count. Older control APIs send no flag; the dashboard then treats the row whose unit starts with
  `instance-hour (VM` as the per-VM row.
* Generator accounting details (attribution seconds, `totalArrivals` counting scheduled arrivals, etc.) are listed in
  `load-generator/README.md`.

### Public auth bootstrap

`GET /auth/config` (no auth) → `{authMode: 'iam_proxy' | 'google_signin' | 'dev', googleClientId: string | null}`.

* `iam_proxy`: Cloud Run IAM authenticates the caller (access through `gcloud run services proxy`, see `scripts/dashboard.sh`); the browser sends no token.
* `google_signin`: the dashboard obtains a Google ID token with Google Identity Services and sends `Authorization: Bearer <id token>` on every request, including the SSE stream (so the dashboard reads SSE with `fetch` + `ReadableStream`, not `EventSource`).
* `dev`: local development only; the API refuses to start in this mode when `K_SERVICE` (Cloud Run) is set.

### `GET /api/meta`

```ts
interface Meta {
  approvedMaxInstances: number;
  limits: { maxRate: number; maxVUs: number; maxDurationSeconds: number; maxTotalInventory: number; maxSyntheticCpuMs: number;
            expensive: { rateAbove: number; durationSecondsAbove: number; vusAbove: number; instancesAbove: number } };
  baseline: { minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number; scaleInWindowSeconds: number };
  defaults: ExperimentConfig;
  presets: Array<{ id: 'A'|'B'|'C'|'D'|'E'|'F'; title: string; description: string; demonstrates: string; config: ExperimentConfig }>;
  target: { baseUrl: string; migName: string; zone: string; autoscalerName: string; region: string; projectId: string };
  costModel: { currency: 'USD'; rows: Array<{ resource: string; unit: string; unitCostPerHour: number; quantity: number;
               perHour: number; billedWhileIdle: boolean; note: string; perAppServer: boolean }>;
               idlePerHour: number; peakPerHour: number; notes: string[] };   // list-price ESTIMATES, see below
  links: Record<string, string>;   // Cloud Console / Monitoring deep links
  waitingRoom: { deployed: boolean };   // WAITING_ROOM_URL is set; otherwise waitingRoom.enabled is refused
}
```

Cost model rows (europe-west2 on-demand list-price estimates, USD/h, each overridable): application VMs
`APP_MACHINE_TYPE` (default `n2-standard-2`, `PRICE_APP_VM_PER_HOUR` 0.113 + `PRICE_APP_DISK_PER_HOUR` 0.0022) × live VMs;
Cloud SQL (`PRICE_CLOUD_SQL_PER_HOUR` 0.084); load balancer (`PRICE_LB_RULE_PER_HOUR` 0.03 + `PRICE_LB_IP_PER_HOUR` 0.005);
load-generator VM `GENERATOR_MACHINE_TYPE` (default `e2-standard-2`, `PRICE_GENERATOR_VM_PER_HOUR` 0.078 +
`PRICE_EXTERNAL_IP_PER_HOUR` 0.005); Cloud Run (`PRICE_CLOUD_RUN_PER_HOUR` 0.068); and, only when the waiting room is
deployed (`WAITING_ROOM_URL` set), `PRICE_QUEUE_VMS` (default 2) waiting-room queue VMs `QUEUE_MACHINE_TYPE` (default
`e2-small`, `PRICE_QUEUE_VM_PER_HOUR` 0.0195 each) and Memorystore Redis basic 1 GB (`PRICE_REDIS_PER_HOUR` 0.058); plus
`PRICE_MISC_PER_HOUR` 0.01. `idlePerHour` counts the baseline minimum of application VMs; the report's
`estimated.infrastructureCostUsd` uses the same rows.

### Report (`GET /api/experiments/{id}/report`)

Every numeric value lives under `measured`, `configured` or `estimated` so the UI can label it.

```ts
interface Report {
  experimentId: string; name: string; generatedAt: string; state: string;
  config: ExperimentConfig;
  timestamps: { createdAt: string; readyAt: string | null; trafficStartedAt: string | null; trafficStoppedAt: string | null; completedAt: string | null };
  measured: {
    arrivalsRequested: number;       // sum of per-second targetRate
    arrivalsScheduled: number; arrivalsStarted: number; arrivalsDropped: number;
    achievedVsRequestedPct: number | null;
    httpRequests: number; peakRps: number;
    latencyMs: { p50: number | null; p95: number | null; p99: number | null; max: number | null };
    httpErrorPct: number | null; businessRejectionPct: number | null;
    peakCpu: { value: number; sampleTime: string; instance: string } | null;
    peakAverageCpu: { value: number; sampleTime: string } | null;
    initialServerCount: number | null; maxObservedServerCount: number | null; finalServerCount: number | null;
    ticketsSold: number | null; ticketsRemaining: number | null; expiredReservations: number | null;
    invariantViolations: number | null;
    reservations: { attempted: number; succeeded: number; rejected: number };
    purchases: { completed: number; declined: number; failed: number; expiredDuringCheckout: number };
    requestsPerInstance: Record<string, number>;
    // null when the experiment ran without the waiting room. Counters: the queue server's last observation, otherwise
    // the generator's bucket sums; waitMs (join → admitted): the generator histogram, otherwise the server's last values.
    waitingRoom: { enabled: boolean; joined: number | null; admitted: number | null; soldOutInQueue: number | null;
                   peakWaiting: number | null; waitMs: { p50: number | null; p95: number | null; max: number | null };
                   peakCapacity: number | null; peakHealthyServers: number | null;
                   // null in fixed mode. perServer: every server that had a learned limit while observed (last and highest
                   // limit, cuts summed from counter increments); limitAvgAtPeakCapacity: limitAvg of the first observation
                   // with the peak capacity
                   adaptive: { perServer: Array<{ name: string; finalLimit: number; peakLimit: number; decreases: number }>;
                               limitAvgAtPeakCapacity: number | null; decreasesTotal: number } | null } | null;
    // Scale-in after traffic stopped (section 3, "Scale-in after completion"); null while still observed, absent in older
    // reports. completedAt / secondsAfterTrafficStopped only for 'complete'; serversRemoved = highest − last observed count.
    scaleIn?: { status: 'complete' | 'timed_out' | 'not_needed' | 'unavailable' | 'superseded';
                completedAt: string | null; secondsAfterTrafficStopped: number | null; serversRemoved: number | null;
                observedUntil: string; targetServers: number; note?: string } | null;
  };
  configured: { cpuTarget: number; minReplicas: number; maxReplicas: number; initialServers: number; pattern: string; durationSeconds: number };
  estimated: { infrastructureCostUsd: number; note: string };
  scalingTimeline: TimelineEvent[];
  generatorCapacityIssues: string[];
  failures: string[];
  notes: string[];
}
```
