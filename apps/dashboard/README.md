# Dashboard

Vue 3 + TypeScript + Vite + Tailwind CSS + Apache ECharts dashboard for the ticketing autoscale lab.
The control API (`apps/control-api`) serves the built `dist/` at `/` on Cloud Run and answers
`/api/*` and `/auth/*`; the binding interface is `docs/contracts.md` §3.

## Scripts

| Command | What it does |
|---|---|
| `npm install` | Installs pinned dependencies (`.npmrc` sets `legacy-peer-deps` to work around an npm 10 peer-resolution crash with Vitest 5's optional peers). |
| `npm run dev` | Vite dev server on :5173, proxying `/api` and `/auth` to `http://localhost:8081` (override with `CONTROL_API_URL=...`). |
| `npm run build` | `vue-tsc` type check, then production build into `dist/`. |
| `npm run typecheck` | `vue-tsc --noEmit`. |
| `npm test` | Vitest (jsdom) — `tests/**/*.spec.ts`. |
| `npm run preview` | Serve `dist/` locally. |

## Structure

```text
src/
  types/contracts.ts     Types copied from docs/contracts.md §3 (+ Experiment / ExperimentSummary, see below)
  api/client.ts          fetch wrapper, ApiError {status, code, message, details}, typed endpoints, export download
  api/auth.ts            auth session (/auth/config → token → /api/me) and Google Identity Services loader
  api/sse.ts             SSE parser + fetch/ReadableStream stream client with exponential backoff
  store/lab.ts           live store: meta, snapshot, experiment, bounded series, timeline, actions
  store/draft.ts         editable ExperimentConfig, validation, 428 confirmation flow
  lib/                   formatting, rate-profile math, validation, state rules, cost estimate, tones
  charts/                ECharts theme + pure option builders (one per chart)
  components/
    layout/              header bar, state pill, stream status, cost indicator
    auth/                AuthGate, sign-in, not-authorised
    config/              A. experiment configuration form sections + preview + confirm dialog
    controls/            B. experiment controls
    live/                C. server summary, instance cards, telemetry freshness, totals
    charts/              D. ECharts wrapper, chart cards with no-data / stale overlays
    timeline/            E. event timeline
    report/              report value groups
  views/                 Dashboard (/), History (/history), Report (/experiments/:id/report)
tests/                   specs, fixtures (test-only), helpers
```

## Auth modes

`GET /auth/config` (public) decides the mode:

* **`iam_proxy`** — Cloud Run IAM authenticates the caller (`gcloud run services proxy`, see
  `scripts/dashboard.sh`). The browser sends no token.
* **`google_signin`** — Google Identity Services (`https://accounts.google.com/gsi/client`, loaded only
  in this mode) issues an ID token for `googleClientId`. The token is kept in memory and sent as
  `Authorization: Bearer <id token>` on every request, including the SSE stream (hence fetch-based SSE,
  not `EventSource`). A 401 drops the token and shows the sign-in screen again.
* **`dev`** — local development; no token.

`/api/me` is called first. 401/403 render a "Not authorised" screen (or the sign-in screen for a
401 in `google_signin` mode); the live store is torn down so no data stays on screen.

## Data flow

1. On sign-in: `GET /api/meta` and `GET /api/state`, then `GET /api/stream`.
2. `tick` events replace the snapshot and append `append.*` points to bounded series (3600 points,
   de-duplicated by `t`, CPU by `t + instance`).
3. When `snapshot.experiment.id` changes, series and timeline are reset and bootstrapped from
   `GET /api/experiments/:id/series`.
4. `timeline` events are appended by id. The dashboard never creates timeline events itself.
5. `experiment` events replace the current experiment document.

Missing values render `N/A` / `—`. Stale or missing telemetry is flagged (amber / red) rather than
shown as `0`. The instance count always comes from the MIG (`mig.actualCount`) and the
`instances` list, never from CPU or load.

## Assumptions the control API must satisfy (beyond the contract)

* **`Experiment`** (referenced by `LiveSnapshot.experiment` but not defined): the UI relies on `id`, `name`,
  `state`, `config`, `createdAt`, and uses `trafficStartedAt`, `trafficStoppedAt`, `message`, `error`
  when they are present.
* **`GET /api/experiments`** returns an array (or `{experiments: [...]}`) of
  `{id, name, preset, state, createdAt, trafficStartedAt?, durationSeconds?, peakRps?, maxServers?, ticketsSold?, invariantViolations?}`.
* **Action endpoints** (`POST .../start|stop|complete`, `POST /api/experiments`) should return the updated
  `Experiment`. Any other 2xx body is accepted, and the UI refreshes `/api/state` after every action.
* **CPU values are fractions** (0–1), as `cpuTarget` is: `InstanceView.cpu.value`, `InstanceView.guestCpu.value`
  (normalise `/instance-info` `guest_cpu.percent` ÷ 100), `CpuPoint.value`, `snapshot.cpu.*`, `Report.measured.peakCpu`.
  `InstanceView.traffic.errorRate` is also a fraction.
* **422 `details`**: either a map `{"servers.max": "message"}` or an array of `{path|field, message}` (paths are
  dot-separated config paths such as `traffic.mix`). Matching paths are shown inline next to the field.
* **Cost model**: rows whose `unit` contains `instance` (for example `instance-hour`) are treated as
  per-app-server cost and multiplied by `mig.actualCount`. Other rows add their `perHour`, and rows with
  `billedWhileIdle: false` count only while an experiment is active.
* **Exports**: `export.json` / `export.csv` are fetched with the auth header and saved as blobs. A
  `Content-Disposition` filename is used when present.
* **SSE through proxies**: the stream must not be buffered (`Cache-Control: no-cache`,
  `X-Accel-Buffering: no`); the client reconnects if nothing (including `: ping`) arrives for 45 s.
* SPA routing: unknown non-`/api` paths serve `index.html` (history-mode routes `/history`, `/experiments/:id/report`).
