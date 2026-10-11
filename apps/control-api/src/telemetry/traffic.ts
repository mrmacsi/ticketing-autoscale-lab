import type { TrafficBucket, TrafficPoint } from '../types.js';

export const businessRejections = (b: TrafficBucket) => b.requests.conflict409 + b.requests.gone410 + b.requests.payment402;
export const httpErrors = (b: TrafficBucket) =>
  b.requests.server5xx + b.requests.networkErrors + b.requests.timeouts + b.requests.client4xx;

export function toTrafficPoint(b: TrafficBucket): TrafficPoint {
  return {
    t: Date.parse(b.ts),
    second: b.second,
    targetRate: b.targetRate,
    scheduled: b.scheduledArrivals,
    achieved: b.startedArrivals,
    rps: b.requests.total,
    ok: b.requests.ok2xx,
    errors: httpErrors(b),
    business: businessRejections(b),
    p50: b.latency.p50,
    p95: b.latency.p95,
    p99: b.latency.p99,
    dropped: b.droppedArrivals,
    activeVUs: b.activeVUs,
    reservationsAttempted: b.business.reservationsAttempted,
    reservationsSucceeded: b.business.reservationsSucceeded,
    purchasesCompleted: b.business.purchasesCompleted,
    purchasesFailed: b.business.purchasesFailed,
    rejectedSoldOut: b.business.reservationsRejected,
    httpErrors: httpErrors(b),
    // Older generators write no `queue` object: unknown, not zero.
    queueJoined: b.queue ? b.queue.joined : null,
    queueAdmitted: b.queue ? b.queue.admitted : null,
    queueSoldOut: b.queue ? b.queue.soldOut : null,
    queueWaiting: b.queue ? b.queue.waiting : null,
    queueWaitP95: b.queue ? (b.queue.waitMs?.p95 ?? percentileFromHistogram(b.queue.waitMs?.hist ?? {}, 0.95)) : null,
  };
}

export interface InstanceTraffic { rps: number; avgLatencyMs: number | null; errorRate: number | null; windowSeconds: number }

/** Per-instance request distribution over the most recent buckets, from the X-Served-By header. */
export function instanceTraffic(recent: TrafficBucket[], windowSeconds: number): Map<string, InstanceTraffic> {
  const totals = new Map<string, { requests: number; errors: number; latency: number }>();
  for (const bucket of recent) {
    for (const [name, s] of Object.entries(bucket.instances ?? {})) {
      const t = totals.get(name) ?? { requests: 0, errors: 0, latency: 0 };
      t.requests += s.requests; t.errors += s.errors; t.latency += s.latencySumMs;
      totals.set(name, t);
    }
  }
  const window = Math.max(1, Math.min(windowSeconds, recent.length));
  const result = new Map<string, InstanceTraffic>();
  for (const [name, t] of totals) {
    result.set(name, {
      rps: t.requests / window,
      avgLatencyMs: t.requests ? t.latency / t.requests : null,
      errorRate: t.requests ? t.errors / t.requests : null,
      windowSeconds: window,
    });
  }
  return result;
}

/** Histogram bucket i represents latencies up to 1.1^i ms (see docs/contracts.md). */
export function percentileFromHistogram(hist: Record<string, number>, p: number): number | null {
  const entries = Object.entries(hist).map(([k, v]) => [Number(k), v] as const).filter(([, v]) => v > 0).sort((a, b) => a[0] - b[0]);
  const total = entries.reduce((s, [, v]) => s + v, 0);
  if (!total) return null;
  const rank = Math.ceil(p * total);
  let cumulative = 0;
  for (const [index, count] of entries) {
    cumulative += count;
    if (cumulative >= rank) return Math.round(Math.pow(1.1, index) * 100) / 100;
  }
  return null;
}

export function mergeHistograms(buckets: TrafficBucket[], pick: (b: TrafficBucket) => Record<string, number> | undefined = (b) => b.latency.hist): Record<string, number> {
  const merged: Record<string, number> = {};
  for (const b of buckets) {
    for (const [k, v] of Object.entries(pick(b) ?? {})) merged[k] = (merged[k] ?? 0) + v;
  }
  return merged;
}
