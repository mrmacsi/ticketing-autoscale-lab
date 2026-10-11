# Experiment guide

## Vocabulary — three different numbers

| Term | Meaning |
|---|---|
| **Customer arrival** | One synthetic customer starting a journey (browse, check availability, reserve, or purchase). The traffic pattern sets arrivals per second. |
| **HTTP request** | One call to the API. A purchase journey is ~4–5 requests (availability → reservation → order → payment, plus duplicates/retries), so req/s > arrivals/s. |
| **Virtual user (VU)** | A journey in flight. `maxVUs` caps concurrency; an arrival that finds no free VU is **dropped** and counted — that is generator saturation, not application capacity. |

The generator is **open-loop**: arrivals are scheduled from the pattern (seeded Poisson process) regardless of how slowly the API answers, so a struggling backend shows up as rising latency and in-flight VUs, not as silently reduced load.

## Running an experiment

1. **Configure** (section A) or pick a preset A–F. Validation mirrors the server: `min ≤ initial ≤ max ≤ approved max`, mix sums to 100, pattern parameters present. Expensive settings ask for confirmation.
2. **Prepare Experiment**: the control plane applies autoscaler settings (raising `min` to the initial count if needed — shown as a *baseline* change), creates a fresh event with the requested inventory and runtime settings (TTL, payment delay/failure, synthetic CPU), and waits until the initial number of VMs is healthy behind the load balancer → `ready`.
3. **Start Traffic**: restores `min` (capacity is now the autoscaler's job) and commands the generator. State `running`.
4. Watch: server cards appear as the MIG creates VMs (starting → healthy → serving), charts update every second, the timeline records each observed change.
5. **Stop Traffic** (hard stop) at any time, or let the duration/total-arrivals end the run → `cooling_down`. The lab completes on its own as soon as no extra server is left (instance group back at the minimum, after at least the reservation TTL + 5 s); `cooldownObserveSeconds` is only the upper bound. Presets run the autoscaler with a 60 s scale-in window, so extra servers go about a minute after demand drops (E uses 240 s to show a slower scale-in; the baseline outside experiments is also 60 s).
6. **Complete observation** (or wait) → `completed`: the report is built from persisted measurements and the autoscaler baseline is restored.
7. **Export Results** (JSON, CSV time series, CSV events) and open the **Report** page. Past runs are under **History**.

`Reset Ticket Inventory` is refused while traffic is running. `Reset Experiment` aborts the current run (marked `failed: reset by …`), stops traffic and restores the baseline.

From a terminal: `./scripts/start-load-test.sh B`, `./scripts/stop-load-test.sh`, `./scripts/monitor-scaling.sh`.

## Presets

| | Configuration | What to look for |
|---|---|---|
| **A Baseline** | 1 server (max 1), 10,000 tickets, constant 5 arrivals/s, 5 min | Flat CPU well below target, stable p95, steady checkout; no scaling events. |
| **B Increasing traffic** | 1 → max 5, CPU target 60 %, 100,000 tickets, ramp 5 → 100 arrivals/s over 10 min, 15 min cool-down | CPU (delayed samples) crossing 60 %, *Autoscaler increased MIG target size*, new cards going starting → healthy → serving, latency recovering as capacity arrives. |
| **C Flash sale** | 100 tickets, 1,000 purchase attempts in a 25 s burst at 40 arrivals/s (≈160 req/s, about what one n1-standard-1 drains), quantity 1, 10 % duplicate submits | Exactly 100 sold, ~900 `409 sold_out`, `invariantViolations = 0`, idempotent replays counted. Too short for scaling — it proves correctness. |
| **D Reservation expiry** | 100 tickets, 30 s TTL, 70 % reserve-and-abandon, 3 arrivals/s for 3 min | *Held* fills up, sold stays 0, expired reservations climb and *available* recovers every ~30 s. |
| **E Recovery and scale-in** | Constant 90 arrivals/s for 6 min, then 25 min observation | Scale-out under load, request distribution across VMs, then — after the stabilization period — *Autoscaler decreased MIG target size* and cards going terminating → removed. |
| **F 5,000 customers / 2,000 tickets, fast scale-out** | 5,000 arrivals at 40/s (about 2 min), 2,000 tickets, one ticket per purchase journey, 30 s holds, 10 % checkout abandonment, no payment failures or duplicate submits; starts on 1 VM, max 4, CPU target 30 %; max 2,000 concurrent journeys | Real scale-out inside a short sale. 40 customers/s is about 160 req/s before sell-out (4 requests per purchase) and about 80 req/s after (availability + `409`), against roughly 120 req/s per VM, so the first VM saturates; with a 30 % target one saturated VM becomes a 3–4 VM recommendation in a single autoscaler decision. Inventory is never oversold. |

Preset **F** (10,000 customers, 4,000 tickets, adaptive waiting room, 1 → max 3 servers) stops scheduling at the 10,000th arrival (about 119 s at 84/s); its 300-second duration is a safety timeout that leaves time for the tickets to sell and the last holds to resolve so everyone still waiting is told "sold out". The experiment completes 30 s after traffic ends; the 60 s scale-in window then removes the extra servers about a minute after demand dropped. Every waiting customer occupies a generator slot, so it needs `maxVUs` 10,000. To run from the terminal: `./scripts/start-load-test.sh F --confirm-expensive`.

Recommended demo: **B** (does the infrastructure scale?) followed by **C** (does the application stay correct under contention?).

## If CPU never reaches the target

Laravel requests on an `n1-standard-1` usually push CPU past 60 % somewhere between 60–100 arrivals/s. If not, enable the **demo-only synthetic CPU work**: set `syntheticCpuMaxMs` (e.g. 30) and in behaviour `syntheticCpuMs` (e.g. 20) with a probability. Each affected journey adds `GET /api/lab/cpu-work?ms=N`, which burns bounded CPU and never touches inventory. It is disabled by default, labelled in the UI, logged as a timeline event, and counted separately (`byEndpoint.cpu_work`, `synthetic.*`).

## Reading the numbers honestly

* **Measured** values come from the generator (HTTP), PostgreSQL (inventory), Compute Engine (instances) or Cloud Monitoring (CPU). **Configured** targets are shown as dashed lines. **Estimates** (cost) are labelled.
* CPU from Cloud Monitoring is 1–4 minutes old; each point is plotted at its sample time and the card shows its age. Guest CPU from `/proc/stat` is real-time but is *not* what the autoscaler uses.
* `409`, `410`, `402` are business outcomes (sold out / expired / declined), not errors. Errors are 5xx, timeouts and network failures.
* If a source is missing or stale the UI says so; it never fills gaps with zeros.

## Measured capacity (first deployment)

One `n1-standard-1` VM (1 vCPU, 32 PHP-FPM workers) peaked at about **120 HTTP requests/s** for checkout-heavy traffic before latency climbed into seconds. Bursts above that queue on the VM until the autoscaler adds capacity, which takes minutes, not seconds.

## Timing you should expect (not guaranteed)

* Scale-out decision: 1–3 min after load rises (60 s CPU samples + reporting delay).
* New VM serving: another 1–2 min (boot COS, pull image, migrations check, health checks, initialisation period).
* Scale-in: not before the stabilization period (60 s; 240 s in preset E) has passed with low load.
