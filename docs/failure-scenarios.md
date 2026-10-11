# Failure scenarios

Each scenario is covered by an automated test against a real PostgreSQL 16 server (`apps/ticketing-api/tests/Feature`), and the HTTP-level ones also by the Go generator against the running stack.

| Scenario | Mechanism | Evidence |
|---|---|---|
| **Two users buy the last ticket** | Conditional `UPDATE … WHERE capacity - held - sold >= q`; PostgreSQL re-checks the predicate after the row lock is released. | `ConcurrencyTest::two_processes_racing_for_the_last_ticket` — two OS processes released from a time barrier: exactly one success, one `sold_out`. |
| **Many concurrent purchases vs limited stock** | Same, plus the `CHECK (held + sold <= capacity)` backstop. | `ConcurrencyTest::many_concurrent_purchases_never_oversell` (60 processes, 10 tickets → 10 sold); `scripts/concurrency-test.sh` (1,000 HTTP purchase attempts, 100 tickets → 100 sold, 100 tickets issued); preset **C** in the cloud. |
| **Duplicate order submissions** | Unique `orders.idempotency_key` and `orders.reservation_id`; concurrent duplicates hit the unique constraint and return the winner. | `CheckoutTest::duplicate_order_submissions_return_the_same_order`; generator `duplicateSubmitProbability`. |
| **Duplicate reservation / reused key** | Unique `reservations.idempotency_key` + request fingerprint; different body → `422 idempotency_key_reused`. | `ReservationTest` (replay, key reuse). |
| **Reservation expiry during checkout** | Payment and order creation lock the reservation and check `expires_at <= now()` (database clock); an expired hold is released in the same transaction and the order cancelled → `410`. | `ExpiryTest::reservation_expiring_during_checkout`, `ordering_an_expired_reservation_releases_it`; preset **D**. |
| **Delayed payment** | Provider delay happens **between** two short transactions, so no locks are held while waiting. Delay range and failure probability are runtime settings. | `CheckoutTest::delayed_payment_waits_for_the_simulated_provider`, `configured_failure_probability_drives_unforced_outcomes`. |
| **Declined payment** | Reservation returns to `active` (or expires if its time ran out); order `payment_failed`; retry with a new key allowed. | `CheckoutTest::declined_payment_keeps_the_hold_and_can_be_retried`. |
| **Application failure before commit** | `X-Lab-Fault` injection (enabled only with `LAB_FAULT_INJECTION_ENABLED=true`, off in GCP) throws inside the transaction. The reservation case rolls back fully; the payment case rolls back the sale and a compensating transaction returns the reservation to `active` and frees the idempotency key. | `RollbackTest` (both), `fault_header_is_ignored_when_fault_injection_is_disabled`. |
| **Process crash mid-payment** | A crash after step 1 leaves `payment_processing`; the sweeper expires it after `expires_at + 60 s` grace and releases the hold; replays of that key then return `410`. | `ExpiryTest::payment_in_progress_is_only_expired_after_the_grace_period`. |
| **Retry after an unknown response** | Same `Idempotency-Key` returns the stored result (`Idempotent-Replayed: true`) or `409 payment_in_progress` while still running; never a second charge or second set of tickets. | `CheckoutTest::retry_after_unknown_response_does_not_charge_or_sell_twice`; the generator retries timeouts with the same key. |
| **Background expiry** | `reservations:expire --loop` on every VM, serialised by `pg_try_advisory_xact_lock`, batches with `FOR UPDATE SKIP LOCKED`, one statement releases `held` per category. Idempotent. | `ExpiryTest::background_sweep_releases_expired_holds_exactly_once`. |

Invariant check after every test and every 2 s during experiments:

```text
held + sold <= capacity, held >= 0, sold >= 0
held = Σ quantity of active + payment_processing reservations
sold = Σ quantity of consumed reservations = number of tickets issued
```

## Infrastructure failures

| Failure | Behaviour |
|---|---|
| Database unreachable | `/health` → 503, the LB stops routing to that VM; `/health/live` stays 200 so autohealing does not recreate VMs in a loop. |
| VM process dies | systemd restarts the container; if the VM stops answering `/health/live` for ~60 s the MIG recreates it (initial delay 300 s). |
| Load balancer not yet programmed | `verify.sh --wait` polls for up to 10 minutes. |
| Generator VM restarts mid-run | The new agent marks the experiment's run `finished/error: generator restarted during run; traffic was not resumed` and goes idle — it never silently resumes. |
| Control plane restarts | State is in Firestore; on boot it resumes the active experiment and its watchers. Generator buckets written meanwhile are not lost. |
| Generator saturated | Arrivals without a free VU are counted as `droppedArrivals`; the report lists them under *load-generator capacity issues*. |
| Cloud Monitoring delayed or failing | CPU shows *delayed*/*stale*/*missing* with the last sample time; nothing is interpolated. |
