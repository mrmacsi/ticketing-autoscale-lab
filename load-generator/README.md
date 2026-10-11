# load-generator

The traffic generator for the ticketing autoscaling lab: a dedicated Go binary (used instead of k6) that sends synthetic customer journeys to the ticketing API and writes one metrics document per second to Firestore. The interfaces are defined in [`docs/contracts.md`](../docs/contracts.md).

```text
control-api ──generator/command──▶ agent ──HTTP──▶ TARGET_BASE_URL (ticketing API via ALB)
            ◀─generator/status────       
            ◀─experiments/{id}.generator, experiments/{id}/traffic/{second}
```

## Layout

| Path | Purpose |
|---|---|
| `cmd/loadgen` | Binary: `agent` (default), `run` (local one-shot), `version` |
| `config` | `TrafficConfig` contract types, validation, clamping to agent limits |
| `scenarios` | `rate(t)` patterns, seeded Poisson schedule, per-arrival plans, journey executor |
| `engine` | Open-loop scheduler, VU semaphore, drain/stop handling, per-second flushing |
| `metrics` | Log-bucket histogram, per-second aggregator, run summary, JSONL and async (bounded, retrying) sinks |
| `ticketapi` | HTTP client for the public ticketing API (shared tuned transport, fixed target) |
| `queueapi` | HTTP client for the waiting room's public `/queue/*` API (own long-poll transport, own recorder) |
| `controller` | Command handling, lease, heartbeat, hard stops and restart recovery behind a `Store` interface |
| `fsstore` | Thin Firestore adapter (`controller.Store` + traffic bucket writer) |
| `internal/seedrand` | Reproducible RNG streams derived from the seed |
| `internal/fakeapi` | In-memory ticketing API and waiting room (FIFO, long poll, HMAC tokens) used by tests |

## How it works

### Open-loop arrivals

Arrivals come from a **nonhomogeneous Poisson process** with intensity `rate(t)`. Lewis–Shedler thinning draws exponential gaps at the pattern's peak rate and accepts each candidate with probability `rate(t)/peak`. The schedule is a pure function of `(pattern, seed)`.

The scheduler sleeps until each arrival's time and dispatches it without waiting for any response. Each arrival tries to take a VU slot from a semaphore of size `maxVUs`. The acquire is **non-blocking**:

* If a slot is free, the journey starts in its own goroutine (`startedArrivals`).
* If no slot is free, the arrival is counted as `droppedArrivals` and skipped.

Dropped arrivals mean the **generator** is saturated: there are not enough VUs for the latency the app currently shows. They are not a measure of application capacity. A slow application therefore raises `activeVUs` and latency, but it never lowers the arrival rate. A closed-loop tool would quietly send less traffic in that situation. `schedulerLagMsMax` is the worst gap between an arrival's scheduled time and its actual dispatch.

### Ending a run

* **Duration elapses:** the run continues to `durationSeconds`, then gives in-flight journeys up to 30 s to finish, then aborts the rest. The reason is `completed`, or `max_duration` if the requested duration was clamped.
* **`totalArrivals` reached:** scheduling stops at once and the same ≤30 s drain applies. The reason is `total_arrivals_reached`.
* **Stop command:** a hard stop. The run context is cancelled, scheduling stops and in-flight HTTP requests are aborted. The reason is `stopped`. Aborted requests are not recorded.
* **Max-duration timer:** an independent timer in the controller (`MAX_DURATION_SECONDS`) cancels the run even if the engine misbehaves. The reason is `max_duration`.
* **Lease lost or agent shutdown (SIGTERM):** the run is cancelled with reason `error`.

### Journeys

Each arrival is one customer `cust-<seed>-<n>`. All random decisions for an arrival are drawn up front from an RNG seeded by `(seed, arrival index)`. This keeps decisions reproducible no matter how goroutines interleave. The decisions are: journey type (from `mix`), quantity, category pick, think times, abandonment, duplicate submits, payment retries and synthetic CPU.

* **Quantity:** 1..`maxQuantity` (≤ 8), truncated geometric with P(k) ∝ 2^-(k-1), so small orders dominate.
* **Category:** picked among categories that the availability response shows in stock. The quantity is capped at what is left. If availability is unknown or everything is sold out, the pick is among all categories, because availability is approximate and real customers still try.
* **Idempotency keys:** `lg-<fnv32(experimentId)>-s<seed>-a<arrival>-<step>`, with steps `res`, `ord`, `pay0`, `pay1`, …. They are deterministic per experiment and do not collide across experiments.
* **Duplicate submission:** the identical POST, with the same key, is re-sent right after the first response. The server must replay the result, and the replay is counted in `idempotentReplays`.
* **Unknown outcome** (timeout, connection error, 502/503/504): retried once with the **same** key.
* **Payment decline (402):** retried with a **new** key when the plan says so (up to 2 retries).
* **`purchase` with abandonment:** stops after the order is created and before payment (`abandoned`). A successful `reserve` journey also counts as `abandoned`.
* **Synthetic CPU:** when enabled, `GET /api/lab/cpu-work?ms=N` is sent at the start of the journey.

### Waiting room

When the config has `"waitingRoom": {"enabled": true}` ([`docs/waiting-room.md`](../docs/waiting-room.md) §3.6), **reserve and purchase** journeys queue before checkout. Browse and availability journeys never queue.

1. `POST /queue/join` with `{eventId, customerRef}`. The customer ref is the arrival's `cust-<seed>-<n>`, so a retried join returns the same entry.
2. While the status is `waiting`: long-poll `GET /queue/status/{id}?waitSeconds=15`. Each request has a deadline of `waitSeconds + 10 s`. If a poll comes back `waiting` in under 200 ms (a server that doesn't long-poll), the customer pauses for the longer of the rest of those 200 ms and `pollAfterMs` (capped at 5 s).
3. `sold_out`: the journey ends with outcome `sold_out_in_queue`. No application request is sent, and that includes synthetic CPU work.
4. `admitted`: synthetic CPU work (if planned) runs, then the normal checkout. `X-Queue-Token: <token>` goes on every `POST /api/reservations` attempt: the first send, the unknown-outcome retry, and the planned duplicate. Orders and payments carry no token.
5. `open` (no waiting room in force): the normal checkout runs without a token.
6. Afterwards the customer always calls `POST /queue/release` with an outcome mapped from the checkout: `purchased`; `sold_out` (a 409 on the reservation); `failed` (an application error); and `abandoned` for a reserve-only journey, abandonment, a decline or an expired hold. A customer told `sold_out` in the queue also releases (`sold_out`). The release is a single attempt with a 5 s timeout. A failure is only counted, in `queue.requests.errors`.

Queue failures (network error, timeout, 5xx, 429) are retried with backoff of 1 s, 2 s, 4 s, then 5 s, until the journey deadline. A `404 unknown_queue_id` while polling means the queue lost the entry (for example after a restart), so the customer joins again. A re-join counts once in `joined`, and the wait time keeps counting from the first join. Any other 4xx (`400`, `404 unknown_event`) ends the journey with outcome `queue_failed` and counts in `queue.failed`. Nothing is sent to the application in that case.

* **Deadline:** journeys run until `durationSeconds` + the 30 s drain grace. With the waiting room on, this also applies when `totalArrivals` is reached early: the drain lasts until `max(now + grace, start + durationSeconds + grace)`. After that, and on any hard stop, the journey context is cancelled. Long polls and backoff sleeps return at once, and aborted customers do not release (the control plane resets the room when the experiment ends).
* **VUs:** a waiting customer is a customer in flight, so it holds a VU slot. A long queue therefore needs `maxVUs` (and `MAX_VUS`) at least as large as the expected number of simultaneous waiters. Otherwise later arrivals are counted as `droppedArrivals`.
* **Transport:** queue calls use their own `http.Transport`: no per-host connection cap, idle pool sized to `maxVUs`, and no response-header timeout, because each request carries its own deadline. Thousands of parked long polls therefore never compete with checkout connections.
* **Metrics:** queue calls are excluded from `requests`, `latency`, `byEndpoint` and `instances`. They go to the bucket's `queue` object: `joined`, `admitted`, `soldOut`, `open`, `failed`, the `waiting` gauge (sampled when the bucket closes), the `waitMs` histogram (join → admitted, in the latency format) and `requests {total, ok, errors}`. The run summary has `queue` with the same counters, `peakWaiting`, and a merged `waitMs`.

The event and its categories are fetched once at run start with `GET /api/events/{eventId}`. This setup request is not included in the metrics.

### Metrics

There is one bucket per elapsed second, with the exact contract fields. Every second gets a bucket, including empty ones and the drain seconds.

* **Histogram:** `i = ceil(ln(ms)/ln(1.1))`, clamped to [0, 250], with `ms ≤ 1 → 0`. The representative value is `1.1^i`.
* **Percentiles:** p50/p95/p99 use the nearest-rank method on the histogram. They are `null` for an empty bucket.
* **Run-wide percentiles:** computed from the merged histograms (`metrics.Summary`), never by averaging per-second values.
* **`instances`:** keyed by the `X-Served-By` response header, sanitized to `[A-Za-z0-9_-]`, max 63 characters. A missing header becomes `unknown`.
* **Error classification:** application errors are `client4xx + server5xx + networkErrors + timeouts`. The 409, 410 and 402 responses are business outcomes, not errors.

The Firestore sink writes asynchronously from a bounded queue (900 buckets) with retry and backoff. It never blocks the load loop. On overflow the oldest pending bucket is dropped and logged. On run end the agent flushes for up to 20 s before marking the experiment finished.

### Controller (agent)

* **Commands:** the agent listens on `generator/command` and re-subscribes with backoff (1 s → 30 s) on errors. It acts only on `seq > lastCommandSeq`, which is persisted in `generator/status`. Snapshot redeliveries and older commands are ignored.
* **`start` is ignored when:**
  * it is older than 120 s, or has no `issuedAt`;
  * a run is already in progress (only one run at a time);
  * its config is invalid or its `experimentId` doesn't match.

  A rejected start is recorded in the status message. If the experiment's `generator` map is still empty, the rejection is also written there as `state=finished, finishReason=error`.
* **Ownership:** claimed in a Firestore transaction on `experiments/{id}.generator`, only if the map is empty or its lease has expired. The lease (15 s) is renewed every 5 s, and `generator/status` is heartbeated every `HEARTBEAT_SECONDS`.
* **Restart recovery:** if `generator/status` shows that an earlier agent on **this host** (same `<host>` before `/boot-`) was running an experiment that agent still owns, that experiment is marked `finished / error / "generator restarted during run; traffic was not resumed"`. The agent then goes idle and never resumes traffic. A finished experiment can never be claimed again.

### Target safety

* Traffic only goes to `TARGET_BASE_URL` (and the waiting room at `QUEUE_BASE_URL`, which defaults to the target), both from the environment. Commands cannot change them.
* The agent refuses to start unless the target host (or `host:port`) is listed in `ALLOWED_TARGET_HOSTS`. The same check applies to `QUEUE_BASE_URL`.
* Only validated ID path segments are appended to the base URL. Redirects are never followed, and proxy environment variables are ignored.
* `loadgen run` additionally allows loopback targets without an allowlist entry.

## Environment (agent)

| Variable | Default | Meaning |
|---|---|---|
| `TARGET_BASE_URL` | — (required) | The only traffic target, e.g. `http://34.x.x.x` |
| `QUEUE_BASE_URL` | `TARGET_BASE_URL` | Waiting room base URL (`/queue/*` is appended). Its host must also be in `ALLOWED_TARGET_HOSTS`, or the agent refuses to start |
| `ALLOWED_TARGET_HOSTS` | — (required) | Comma list of allowed hosts or `host:port` |
| `GOOGLE_CLOUD_PROJECT` | auto-detect | Firestore project |
| `FIRESTORE_DATABASE` | `ticketing-scale-lab` | Named Firestore database |
| `FIRESTORE_EMULATOR_HOST` | — | If set, the official client uses the emulator |
| `AGENT_ID` | `<GCE instance name or hostname>/boot-<uuid>` | Agent identity |
| `MAX_RATE` | `500` | Max arrivals/s; `rate(t)` is clamped |
| `MAX_VUS` | `1000000` | Max concurrent journeys (effectively no cap; memory of the generator VM is the real limit) |
| `MAX_DURATION_SECONDS` | `1800` | Max run length, also enforced by the hard-stop timer |
| `HEARTBEAT_SECONDS` | `2` | `generator/status` heartbeat interval |

Logs are JSON on stdout.

## Local usage

```sh
make build                     # bin/loadgen
bin/loadgen run --config examples/constant.json --target http://localhost:8080 --summary
bin/loadgen run --config examples/flash-sale.json --target http://localhost:8080 \
    --out buckets.jsonl --summary 2> summary.json   # 1000 purchase arrivals
```

`run` takes `--queue <url>` (default `QUEUE_BASE_URL`, then `--target`) for the waiting room. `run` uses the same engine as the agent. It writes bucket JSON lines to stdout, or to the `--out` file. With `--summary` it prints the final summary to stderr: totals, merged percentiles, and `achievedVsRequestedPct` (started / requested arrivals). `MAX_*` env limits apply. Set the `eventId` in the example configs first.

Agent against the Firestore emulator:

```sh
FIRESTORE_EMULATOR_HOST=localhost:8681 GOOGLE_CLOUD_PROJECT=demo-lab \
TARGET_BASE_URL=http://localhost:8080 ALLOWED_TARGET_HOSTS=localhost:8080 bin/loadgen agent
```

## Tests

```sh
go vet ./...
go test -race ./...            # or: make check
docker build --platform linux/amd64 -t loadgen .   # or: make docker
```

The tests run against `httptest` fakes of the ticketing API that follow the contract's codes (409 with limited stock, 402 declines, 410, replays, alternating `X-Served-By`). They cover:

* requested vs achieved rate, open-loop behaviour with 500 ms responses, and dropped arrivals with tiny `maxVUs`;
* every pattern's `rate(t)`;
* Poisson statistics, seed reproducibility, and how closely the behaviour mix matches its percentages;
* duration and max-duration enforcement, and cancellation within 1 s while requests are in flight;
* duplicate and timeout retries with the same key, and decline retries with a new key;
* the 1000-vs-100-tickets flash sale, where the generator must equal server truth;
* histogram, merge, percentiles, classification, instance attribution and completion-second attribution;
* the async sink's retry, bounds and deadline;
* controller start/stop, stale or duplicate commands, rejection while running, lease handling, restart recovery and listener re-subscribe (with an in-memory `Store`);
* Firestore document mapping;
* the waiting room, against a fake FIFO queue with long polling and HMAC tokens, and a fake app that enforces `X-Queue-Token`:
  * the token is sent on every reservation attempt and the slot is released;
  * customers wait while the room is full and are admitted in FIFO order;
  * `sold_out` in the queue never touches the app;
  * queue calls are excluded from app metrics;
  * the waiting gauge returns to 0;
  * retry and backoff, re-join after a lost entry, and non-retryable errors;
  * hard stop of long-polling customers within 1 s;
  * the `durationSeconds` + grace deadline after an early `totalArrivals`;
  * the `QUEUE_BASE_URL` allowlist check.

## Contract deviations

The wire formats follow the contract. Where the contract is silent, these interpretations apply:

1. **Attribution.** Requests, `completedJourneys` and business counters go to the second in which the outcome was observed (completion). Journeys can span several seconds, while a bucket is written exactly once. `scheduledArrivals`, `startedArrivals` and `droppedArrivals` go to the dispatch second, which is the scheduled second plus `schedulerLagMs`.
2. **Latency** covers only requests that got an HTTP response, so `latency.count` can be lower than `requests.total` when there are timeouts or network errors. Requests aborted by a stop are not counted at all.
3. **`client4xx`** also counts unexpected 1xx/3xx responses. Redirects are never followed.
4. **Order 409 `reservation_not_active`** counts as `expiredDuringCheckout`, the same as 410. `purchasesFailed` counts purchase journeys that ended on an application error.
5. **`totalArrivals`** counts scheduled arrivals, including dropped ones.
6. **Claiming** is refused when `generator.state == "finished"`, even if the lease expired, so traffic for an experiment never runs twice.
7. **Rejected starts.** Stale, invalid or concurrent starts on an experiment whose `generator` map is still empty are recorded there as `finished/error` with the reason.
8. **`generator/status.experimentId`** is `null` while idle.
9. **Waiting room counters.** `queue.joined` counts customers once, even after a re-join, and includes customers who were then told `open` or `sold_out`. `waitMs` runs from sending the successful join to observing `admitted`, so a customer admitted directly at join records the join round trip. `queue.requests.ok` means a 2xx with a readable body. Customers aborted by a stop or by the run deadline are neither `failed` nor released.
