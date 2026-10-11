import type { AppConfig } from '../config.js';
import { estimateExperimentCost } from '../costs.js';
import { peakRate } from '../experiments/pattern.js';
import { businessRejections, httpErrors, mergeHistograms, percentileFromHistogram, toTrafficPoint } from '../telemetry/traffic.js';
import {
  waitingRoomOf, type CpuPoint, type Experiment, type InventoryView, type Report, type Series, type TimelineEvent, type TrafficBucket,
  type WaitingRoomAdaptiveReport, type WaitingRoomPoint, type WaitingRoomReport,
} from '../types.js';

const pct = (part: number, whole: number) => (whole > 0 ? Math.round((part / whole) * 10000) / 100 : null);
const sum = (buckets: TrafficBucket[], pick: (b: TrafficBucket) => number) => buckets.reduce((s, b) => s + pick(b), 0);

export interface ReportInput {
  experiment: Experiment;
  buckets: TrafficBucket[];
  series: Omit<Series, 'traffic' | 'events'>;
  events: TimelineEvent[];
  finalInventory: InventoryView | null;
}

/** Builds the end-of-experiment report from persisted measurements only. */
export function buildReport(input: ReportInput, config: AppConfig): Report {
  const { experiment: e, buckets, series, events, finalInventory } = input;
  const c = e.config;
  // With a total-arrivals cap the run ends once the cap is reached; that cap is what was requested.
  const patternRequested = sum(buckets, (b) => b.targetRate);
  const requested = c.traffic.totalArrivals > 0 ? Math.min(patternRequested, c.traffic.totalArrivals) : patternRequested;
  const started = sum(buckets, (b) => b.startedArrivals);
  const total = sum(buckets, (b) => b.requests.total);
  const errors = sum(buckets, httpErrors);
  const business = sum(buckets, businessRejections);
  const hist = mergeHistograms(buckets);
  const instanceCpu = series.cpu.filter((p) => p.instance !== '__avg__');
  const avgCpu = series.cpu.filter((p) => p.instance === '__avg__');
  const peak = (points: CpuPoint[]) => points.reduce<CpuPoint | null>((best, p) => (!best || p.value > best.value ? p : best), null);
  const peakInstance = peak(instanceCpu);
  const peakAvg = peak(avgCpu);
  const running = series.scaling.map((p) => p.running);
  const requestsPerInstance: Record<string, number> = {};
  for (const b of buckets) {
    for (const [name, s] of Object.entries(b.instances ?? {})) requestsPerInstance[name] = (requestsPerInstance[name] ?? 0) + s.requests;
  }
  const lastInventory = finalInventory ?? null;
  const maxObserved = running.length ? Math.max(...running) : null;

  const capacityIssues: string[] = [];
  const dropped = sum(buckets, (b) => b.droppedArrivals);
  if (dropped > 0) capacityIssues.push(`${dropped} scheduled arrivals were dropped because every virtual user was busy (raise max VUs; this is generator capacity, not application capacity).`);
  const worstLag = buckets.reduce((m, b) => Math.max(m, b.schedulerLagMsMax ?? 0), 0);
  if (worstLag > 100) capacityIssues.push(`Generator scheduling lag peaked at ${Math.round(worstLag)} ms; arrival timing was less precise during that period.`);
  const achievedPct = pct(started, requested);
  if (achievedPct !== null && achievedPct < 95 && !(c.traffic.totalArrivals > 0 && started >= c.traffic.totalArrivals)) {
    capacityIssues.push(`Only ${achievedPct}% of requested arrivals were started.`);
  }

  const failures = events.filter((ev) => ev.kind === 'error').map((ev) => `${ev.t} ${ev.message}`);
  if (e.failedReason) failures.unshift(e.failedReason);
  if (e.generator?.finishReason === 'error') failures.push(`Generator: ${e.generator.message ?? 'run ended with an error'}`);

  const durationSeconds = Math.max(0, (Date.parse(e.completedAt ?? new Date().toISOString()) - Date.parse(e.createdAt)) / 1000);
  const sumBusiness = (k: keyof TrafficBucket['business']) => sum(buckets, (b) => b.business[k]);

  return {
    experimentId: e.id,
    name: e.name,
    generatedAt: new Date().toISOString(),
    state: e.state,
    config: c,
    timestamps: { createdAt: e.createdAt, readyAt: e.readyAt, trafficStartedAt: e.trafficStartedAt, trafficStoppedAt: e.trafficStoppedAt, completedAt: e.completedAt },
    measured: {
      arrivalsRequested: Math.round(requested),
      arrivalsScheduled: sum(buckets, (b) => b.scheduledArrivals),
      arrivalsStarted: started,
      arrivalsDropped: dropped,
      achievedVsRequestedPct: achievedPct,
      httpRequests: total,
      peakRps: buckets.reduce((m, b) => Math.max(m, b.requests.total), 0),
      latencyMs: {
        p50: percentileFromHistogram(hist, 0.5),
        p95: percentileFromHistogram(hist, 0.95),
        p99: percentileFromHistogram(hist, 0.99),
        max: buckets.length ? Math.max(...buckets.map((b) => b.latency.maxMs ?? 0)) : null,
      },
      httpErrorPct: pct(errors, total),
      businessRejectionPct: pct(business, total),
      peakCpu: peakInstance ? { value: peakInstance.value, sampleTime: new Date(peakInstance.t).toISOString(), instance: peakInstance.instance } : null,
      peakAverageCpu: peakAvg ? { value: peakAvg.value, sampleTime: new Date(peakAvg.t).toISOString() } : null,
      initialServerCount: e.initialServerCount ?? series.scaling[0]?.running ?? null,
      maxObservedServerCount: maxObserved,
      finalServerCount: series.scaling.at(-1)?.running ?? null,
      ticketsSold: lastInventory?.sold ?? null,
      ticketsRemaining: lastInventory ? lastInventory.available : null,
      expiredReservations: lastInventory?.expiredReservations ?? null,
      invariantViolations: lastInventory?.invariantViolations ?? null,
      reservations: { attempted: sumBusiness('reservationsAttempted'), succeeded: sumBusiness('reservationsSucceeded'), rejected: sumBusiness('reservationsRejected') },
      purchases: { completed: sumBusiness('purchasesCompleted'), declined: sumBusiness('paymentsDeclined'), failed: sumBusiness('purchasesFailed'),
        expiredDuringCheckout: sumBusiness('expiredDuringCheckout') },
      requestsPerInstance,
      waitingRoom: waitingRoomReport(e, buckets, series.waitingRoom ?? []),
    },
    configured: {
      cpuTarget: c.servers.cpuTarget, minReplicas: c.servers.min, maxReplicas: c.servers.max, initialServers: c.servers.initial,
      pattern: `${c.traffic.pattern.type} (peak ${peakRate(c.traffic.pattern, c.traffic.durationSeconds)} arrivals/s)`,
      durationSeconds: c.traffic.durationSeconds,
    },
    estimated: {
      infrastructureCostUsd: estimateExperimentCost(config, maxObserved ?? c.servers.max, durationSeconds),
      note: 'Upper-bound list-price estimate for the whole lab during this experiment window (all resources, max observed servers).',
    },
    scalingTimeline: events.filter((ev) => ['scaling', 'instance', 'health', 'cpu', 'waiting_room'].includes(ev.kind)),
    generatorCapacityIssues: capacityIssues,
    failures,
    notes: [
      'CPU values are Cloud Monitoring samples (60 s resolution, reported 1-4 minutes late) and carry their own sample times.',
      'The Compute Engine autoscaler bases scale-in on the peak recommendation of the last 10 minutes, so scale-in is never immediate.',
      'HTTP 409/410/402 are expected business outcomes (sold out, expired, declined) and are not counted as errors.',
    ],
  };
}

/**
 * Waiting-room results: the queue server's own counters (last observation) when it was observed,
 * otherwise the generator's per-second sums; wait percentiles from the generator's join → admitted
 * histogram when present. null when the experiment ran without the waiting room.
 */
export function waitingRoomReport(e: Experiment, buckets: TrafficBucket[], points: WaitingRoomPoint[]): WaitingRoomReport | null {
  const settings = waitingRoomOf(e.config);
  if (!settings.enabled) return null;
  const queued = buckets.filter((b) => b.queue);
  const observed = [...points].sort((a, b) => a.t - b.t);
  const last = observed.at(-1);
  const generatorSum = (pick: (q: NonNullable<TrafficBucket['queue']>) => number) => (queued.length ? sum(queued, (b) => pick(b.queue!)) : null);
  const peak = (values: number[]) => (values.length ? Math.max(...values) : null);
  const hist = mergeHistograms(queued, (b) => b.queue?.waitMs?.hist);
  const histCount = Object.values(hist).reduce((a, b) => a + b, 0);
  return {
    enabled: true,
    joined: last ? last.joinedTotal : generatorSum((q) => q.joined),
    admitted: last ? last.admittedTotal : generatorSum((q) => q.admitted),
    soldOutInQueue: last ? last.soldOutTotal : generatorSum((q) => q.soldOut),
    peakWaiting: peak([...observed.map((p) => p.waiting), ...queued.map((b) => b.queue!.waiting)]),
    waitMs: histCount > 0
      ? { p50: percentileFromHistogram(hist, 0.5), p95: percentileFromHistogram(hist, 0.95), max: Math.max(...queued.map((b) => b.queue!.waitMs?.maxMs ?? 0)) }
      : { p50: last?.waitP50Ms ?? null, p95: last?.waitP95Ms ?? null, max: last?.waitMaxMs ?? null },
    peakCapacity: peak(observed.map((p) => p.capacity)),
    peakHealthyServers: peak(observed.map((p) => p.healthyServers)),
    adaptive: settings.mode === 'adaptive' ? adaptiveReport(observed) : null,
  };
}

/**
 * Learned limits from the queue's observations (time-ordered). Cuts are summed from increments of each
 * server's counter between successive observations, so a server that left and re-joined (counter
 * restarted) is still counted correctly. A server's first observation contributes its counter as is (the
 * queue clears counters when it is configured for the experiment's event).
 */
export function adaptiveReport(observed: WaitingRoomPoint[]): WaitingRoomAdaptiveReport {
  const servers = new Map<string, { finalLimit: number; peakLimit: number; decreases: number; lastCounter: number | null }>();
  for (const p of observed) {
    for (const [name, limit] of Object.entries(p.limits ?? {})) {
      const counter = p.decreases?.[name] ?? null;
      const s = servers.get(name);
      if (!s) {
        servers.set(name, { finalLimit: limit, peakLimit: limit, decreases: counter ?? 0, lastCounter: counter });
        continue;
      }
      s.finalLimit = limit;
      s.peakLimit = Math.max(s.peakLimit, limit);
      if (counter !== null) {
        s.decreases += s.lastCounter === null ? 0 : counter >= s.lastCounter ? counter - s.lastCounter : counter;
        s.lastCounter = counter;
      }
    }
  }
  const withLimits = observed.filter((p) => p.limitAvg !== null && p.limitAvg !== undefined);
  const peakCapacity = withLimits.reduce<WaitingRoomPoint | null>((best, p) => (!best || p.capacity > best.capacity ? p : best), null);
  const perServer = [...servers.entries()].map(([name, s]) => ({ name, finalLimit: s.finalLimit, peakLimit: s.peakLimit, decreases: s.decreases }));
  return {
    perServer,
    limitAvgAtPeakCapacity: peakCapacity?.limitAvg ?? null,
    decreasesTotal: perServer.reduce((a, s) => a + s.decreases, 0),
  };
}

const CSV_COLUMNS = [
  'second', 'timestamp', 'target_arrivals_per_s', 'scheduled_arrivals', 'started_arrivals', 'dropped_arrivals', 'http_requests',
  'http_2xx', 'http_errors', 'business_rejections', 'latency_p50_ms', 'latency_p95_ms', 'latency_p99_ms', 'active_vus',
  'reservations_attempted', 'reservations_succeeded', 'reservations_rejected_sold_out', 'purchases_completed', 'purchases_failed',
  'servers_running', 'servers_target', 'avg_cpu_percent', 'avg_cpu_sample_time', 'tickets_sold', 'tickets_held', 'tickets_available',
  // Waiting room: queue-server observation at that time, then the generator's own counts for the second.
  'queue_waiting', 'queue_active', 'queue_capacity', 'queue_admission_rate_per_s', 'queue_healthy_servers',
  'queue_joined', 'queue_admitted', 'queue_sold_out', 'queue_wait_p95_ms',
  // Adaptive admission: learned per-server limits across healthy servers (empty in fixed mode).
  'queue_limit_avg', 'queue_limit_min', 'queue_limit_max',
];

/** Per-second time series joined with the latest infrastructure/inventory observation at that time. */
export function timeseriesCsv(buckets: TrafficBucket[], series: Omit<Series, 'traffic' | 'events'>): string {
  const scaling = [...series.scaling].sort((a, b) => a.t - b.t);
  const avgCpu = series.cpu.filter((p) => p.instance === '__avg__').sort((a, b) => a.t - b.t);
  const inventory = [...series.inventory].sort((a, b) => a.t - b.t);
  const room = [...(series.waitingRoom ?? [])].sort((a, b) => a.t - b.t);
  const latestAt = <T extends { t: number }>(points: T[], t: number) => {
    let found: T | undefined;
    for (const p of points) { if (p.t <= t) found = p; else break; }
    return found;
  };
  const rows = buckets.map((b) => {
    const p = toTrafficPoint(b);
    const s = latestAt(scaling, p.t);
    const cpu = latestAt(avgCpu, p.t);
    const inv = latestAt(inventory, p.t);
    const q = latestAt(room, p.t);
    return [
      p.second, b.ts, p.targetRate, p.scheduled, p.achieved, p.dropped, p.rps, p.ok, p.errors, p.business, p.p50, p.p95, p.p99,
      p.activeVUs, p.reservationsAttempted, p.reservationsSucceeded, p.rejectedSoldOut, p.purchasesCompleted, p.purchasesFailed,
      s?.running, s?.target, cpu ? Math.round(cpu.value * 1000) / 10 : undefined, cpu ? new Date(cpu.t).toISOString() : undefined, inv?.sold, inv?.held, inv?.available,
      q?.waiting, q?.active, q?.capacity, q?.admissionRate, q?.healthyServers,
      p.queueJoined, p.queueAdmitted, p.queueSoldOut, p.queueWaitP95,
      q?.limitAvg, q?.limitMin, q?.limitMax,
    ].map(csvCell).join(',');
  });
  return [CSV_COLUMNS.join(','), ...rows].join('\n') + '\n';
}

export function eventsCsv(events: TimelineEvent[]): string {
  const rows = events.map((e) => [e.t, e.kind, e.source, e.attribution ?? '', e.message].map(csvCell).join(','));
  return ['time,kind,source,attribution,message', ...rows].join('\n') + '\n';
}

function csvCell(value: unknown): string {
  if (value === undefined || value === null) return '';
  const s = String(value);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}
