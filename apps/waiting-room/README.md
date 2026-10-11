# Waiting room (queue server)

Go service that holds customers in a FIFO line and admits them only as fast as the app servers the load
balancer reports `HEALTHY` can take them. The binding contract, admission rules and API are in
[`docs/waiting-room.md`](../../docs/waiting-room.md). The line lives either in memory (`STORE=memory`,
one instance, the default) or in Redis (`STORE=redis`, any number of stateless instances, one elected
leader admits; see "High availability" in the doc). Dependencies: `go-redis` (and `miniredis` in tests).

| Package | Job |
|---|---|
| `queue` | the room: join, long-poll status, 100 ms admission loop, active set with TTL, release, sold-out grace, stats. `Room` (memory) and `RedisRoom` (shared, leader election, Lua scripts in `redis_lua.go`) both implement `Store` |
| `token` | `v1.<payload>.<sig>` admission tokens (HMAC-SHA256, key derived from `LAB_ADMIN_TOKEN`) |
| `capacity` | healthy servers from Compute `backendServices.getHealth` (`gce`) or `GET /health` probes (`static`); keeps the last count for 30 s on errors |
| `inventory` | polls `GET /api/events/{id}/availability` on a healthy app server every second |
| `gcp` | metadata-server access token, Secret Manager read |
| `server` | `/queue/*` (public, via the LB) and `/admin/*` (bearer admin token) handlers |
| `cmd/waiting-room` | env config, wiring, JSON logs on stdout, graceful shutdown |

## Run

```bash
go test -race ./...                       # unit, both stores (Redis via miniredis), 5,000-customer load test

LAB_ADMIN_TOKEN=dev CAPACITY_SOURCE=static STATIC_BACKENDS=http://localhost:8000 \
  go run ./cmd/waiting-room               # listens on :8080

LAB_ADMIN_TOKEN=dev CAPACITY_SOURCE=static STATIC_BACKENDS=http://localhost:8000 \
  STORE=redis REDIS_ADDR=localhost:6379 PORT=8081 go run ./cmd/waiting-room   # run several of these

docker build --platform linux/amd64 --build-arg APP_VERSION=1.0.0 -t waiting-room .
```

Environment: `PORT` (8080), `LAB_ADMIN_TOKEN` or `LAB_ADMIN_TOKEN_SECRET` (`projects/N/secrets/X`),
`CAPACITY_SOURCE` (`gce` | `static`), `GCP_PROJECT_ID`, `BACKEND_SERVICE_NAME`, `INSTANCE_GROUP_URL`,
`STATIC_BACKENDS`, `APP_PORT` (8080), `CAPACITY_POLL_MS` (2000), `INVENTORY_POLL_MS` (1000),
`STORE` (`memory` | `redis`), `REDIS_ADDR` (`host:port`), `REDIS_PASSWORD` (optional).
`GCE_METADATA_HOST` overrides the metadata server host.

The room starts **disabled** (every join answers `open`) until the control plane sends
`PUT /admin/config`.

```bash
curl -X PUT localhost:8080/admin/config -H 'Authorization: Bearer dev' \
  -d '{"enabled":true,"eventId":"<uuid>","activeUsersPerServer":25,"admissionTtlSeconds":60,"soldOutGraceSeconds":125}'
curl localhost:8080/queue/join -d '{"eventId":"<uuid>","customerRef":"c1"}'
curl 'localhost:8080/queue/status/<queueId>?waitSeconds=15'
curl localhost:8080/admin/stats -H 'Authorization: Bearer dev'
```

## Behaviour notes

* Long polls block on a per-entry channel that is closed once when the entry stops waiting; there are
  no per-customer goroutines or polling loops. Waits are capped at 25 s; a client disconnect ends the
  handler immediately. Shutdown answers every open long poll before closing.
* `PUT /admin/config` accepts partial bodies (omitted fields keep their value). Changing `eventId` or
  setting `enabled:false` clears the line, the active set and the counters; long-pollers then get
  `404 unknown_queue_id`.
* After release or TTL expiry an entry still answers `status: admitted` with its original token (kept
  for an hour), so a late poll never looks like a fresh wait. Once declared sold out, the room stays
  sold out until reset or a new event.
* `testdata/token-vector.txt` holds a fixed token (admin token `test-admin-token`, payload
  `{"q":"q_TEST","e":"00000000-0000-0000-0000-000000000001","exp":2000000000}`) shared with the PHP
  verifier's tests.
