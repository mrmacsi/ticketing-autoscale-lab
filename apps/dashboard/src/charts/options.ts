import type { CpuPoint, InventoryPoint, ScalingPoint, TimelineEvent, TrafficPoint, WaitingRoomPoint } from '@/types/contracts';
import { parseTime } from '@/lib/format';
import { baseOption, C, dashed, INSTANCE_PALETTE, line, type ChartOption, type LineSeries } from './theme';

type XY = Array<[number, number | null]>;

const xy = <T extends { t: number }>(points: readonly T[], pick: (p: T) => number | null): XY => points.map((p) => [p.t, pick(p)]);

// 1. Incoming traffic ----------------------------------------------------------------
export function trafficOption(points: readonly TrafficPoint[]): ChartOption {
  return {
    ...baseOption('per second'),
    series: [
      dashed('Requested arrivals/s (target)', C.target, xy(points, (p) => p.targetRate)),
      line('Achieved arrivals/s', C.achieved, xy(points, (p) => p.achieved)),
      line('HTTP req/s', C.http, xy(points, (p) => p.rps)),
      line('Successful req/s', C.ok, xy(points, (p) => p.ok)),
      line('Failed req/s (errors)', C.error, xy(points, (p) => p.errors)),
      line('Dropped iterations/s', C.dropped, xy(points, (p) => p.dropped)),
    ],
  };
}

// 2. Server scaling --------------------------------------------------------------------
export interface ScalingOptionInput {
  points: readonly ScalingPoint[];
  events: readonly TimelineEvent[];
  approvedMax?: number | null;
}

export function scalingMarkers(events: readonly TimelineEvent[]) {
  return events
    .filter((e) => e.kind === 'scaling' || e.kind === 'instance')
    .map((e) => {
      const t = parseTime(e.t);
      if (t == null) return null;
      const who = e.attribution ?? 'unattributed';
      const color = e.attribution ? C[e.attribution] : C.unknown;
      return {
        xAxis: t,
        name: e.message,
        label: { formatter: e.kind === 'scaling' ? who : '', color, fontSize: 10, position: 'insideEndTop' as const },
        lineStyle: { color, type: e.kind === 'scaling' ? ('solid' as const) : ('dotted' as const), width: 1 },
      };
    })
    .filter((m): m is NonNullable<typeof m> => m !== null);
}

export function scalingOption({ points, events, approvedMax }: ScalingOptionInput): ChartOption {
  const markLineData: Array<Record<string, unknown>> = scalingMarkers(events);
  if (approvedMax != null) {
    markLineData.push({
      yAxis: approvedMax,
      name: 'Approved max',
      label: { formatter: `approved max ${approvedMax}`, color: C.approved, fontSize: 10, position: 'insideEndTop' },
      lineStyle: { color: C.approved, type: 'dashed', width: 1 },
    });
  }
  const step = { step: 'end' as const };
  const base = baseOption('instances', { yFormatter: (v) => String(Math.round(v)) });
  const [y0] = base.yAxis as Array<Record<string, unknown>>;
  return {
    ...base,
    // Keep the approved-max line in view and use whole-instance ticks.
    yAxis: { ...y0, minInterval: 1, max: (ext: { max: number }) => Math.max(ext.max, approvedMax ?? 0) + 1 } as never,
    series: [
      line('Actual instances', C.healthy, xy(points, (p) => p.actual), {
        ...step,
        lineStyle: { width: 2.4, color: C.healthy },
        markLine: { symbol: 'none', silent: false, animation: false, data: markLineData as never },
      }),
      line('Running', C.achieved, xy(points, (p) => p.running), step),
      line('Healthy', '#2dd4bf', xy(points, (p) => p.healthy), step),
      dashed('Desired / target size', C.target, xy(points, (p) => p.target), step),
      dashed('Min (configured)', C.limit, xy(points, (p) => p.min), step),
      dashed('Max (configured)', C.draining, xy(points, (p) => p.max), step),
    ],
  };
}

// 3. CPU ------------------------------------------------------------------------------------
export const AVG_KEY = '__avg__';

export function cpuOption(points: readonly CpuPoint[], targetFraction: number | null): ChartOption {
  const byInstance = new Map<string, XY>();
  for (const p of points) {
    let arr = byInstance.get(p.instance);
    if (!arr) byInstance.set(p.instance, (arr = []));
    arr.push([p.t, p.value * 100]);
  }
  const avg = byInstance.get(AVG_KEY) ?? [];
  byInstance.delete(AVG_KEY);
  const series: LineSeries[] = [
    line('Average CPU', C.target, avg, {
      lineStyle: { width: 2.6, color: C.target },
      markLine:
        targetFraction != null
          ? {
              symbol: 'none',
              data: [
                {
                  yAxis: targetFraction * 100,
                  name: 'CPU target',
                  label: { formatter: `target ${(targetFraction * 100).toFixed(0)} %`, color: C.dropped, fontSize: 10, position: 'insideEndTop' },
                  lineStyle: { color: C.dropped, type: 'dashed', width: 1.2 },
                },
              ],
            }
          : undefined,
    }),
  ];
  [...byInstance.keys()].sort().forEach((name, i) => {
    const color = INSTANCE_PALETTE[i % INSTANCE_PALETTE.length]!;
    series.push(line(shortInstance(name), color, byInstance.get(name)!, { lineStyle: { width: 1, color, opacity: 0.85 } }));
  });
  return { ...baseOption('CPU %', { yMax: 100, yFormatter: (v) => `${v}` }), series };
}

export function shortInstance(name: string): string {
  const parts = name.split('-');
  return parts.length > 2 ? `…-${parts.slice(-1)[0]}` : name;
}

// 4. Latency ----------------------------------------------------------------------------------
export function latencyOption(points: readonly TrafficPoint[]): ChartOption {
  return {
    ...baseOption('ms'),
    series: [
      line('p50', C.p50, xy(points, (p) => p.p50)),
      line('p95', C.p95, xy(points, (p) => p.p95)),
      line('p99', C.p99, xy(points, (p) => p.p99)),
    ],
  };
}

// 5. Inventory -------------------------------------------------------------------------------
export function inventoryOption(points: readonly InventoryPoint[]): ChartOption {
  return {
    ...baseOption('tickets', { rightAxis: 'reservations / rejections' }),
    series: [
      dashed('Capacity', C.target, xy(points, (p) => p.capacity)),
      line('Available', C.available, xy(points, (p) => p.available)),
      line('Held', C.held, xy(points, (p) => p.held)),
      line('Sold', C.sold, xy(points, (p) => p.sold), { lineStyle: { width: 2.2, color: C.sold } }),
      line('Expired reservations', C.expired, xy(points, (p) => p.expired), { yAxisIndex: 1 }),
      line('Rejected (sold out)', C.rejected, xy(points, (p) => p.rejected), { yAxisIndex: 1 }),
    ],
  };
}

// 6. Outcomes -------------------------------------------------------------------------------
export function outcomesOption(points: readonly TrafficPoint[]): ChartOption {
  return {
    ...baseOption('per second'),
    series: [
      line('Reservations attempted', C.target, xy(points, (p) => p.reservationsAttempted)),
      line('Reservations successful', C.achieved, xy(points, (p) => p.reservationsSucceeded)),
      line('Purchases completed', C.ok, xy(points, (p) => p.purchasesCompleted), { lineStyle: { width: 2.2, color: C.ok } }),
      line('Purchases failed', C.draining, xy(points, (p) => p.purchasesFailed)),
      line('HTTP errors', C.error, xy(points, (p) => p.httpErrors)),
      dashed('Business rejections 409/410/402 (not errors)', C.business, xy(points, (p) => p.business)),
      line('Dropped generator iterations', C.dropped, xy(points, (p) => p.dropped), { lineStyle: { width: 1, color: C.dropped, type: 'dotted' } }),
    ],
  };
}

// 7. Waiting room ------------------------------------------------------------------------------
/** Queue-server observations: who waits, who is in checkout, and how many the healthy servers may take. */
export function waitingRoomOption(points: readonly WaitingRoomPoint[]): ChartOption {
  const step = { step: 'end' as const };
  return {
    ...baseOption('customers', { rightAxis: 'admitted/s' }),
    series: [
      line('Waiting', C.held, xy(points, (p) => p.waiting), { lineStyle: { width: 2.4, color: C.held } }),
      line('Active (in checkout)', C.achieved, xy(points, (p) => p.active)),
      dashed('Capacity (healthy servers × per-server)', C.target, xy(points, (p) => p.capacity), step),
      line('Admitted/s (10 s average)', C.ok, xy(points, (p) => p.admissionRate), { yAxisIndex: 1, lineStyle: { width: 1.2, color: C.ok } }),
    ],
  };
}

// 8. Adaptive limits -----------------------------------------------------------------------------
/** Points that carry learned per-server limits (adaptive mode only). */
export const hasAdaptiveLimits = (points: readonly WaitingRoomPoint[]) => points.some((p) => p.limits && Object.keys(p.limits).length > 0);

/**
 * One step line per server's learned limit (left axis; a gap while the server is not healthy) and the
 * room capacity = sum of the healthy servers' limits (right axis).
 */
export function adaptiveLimitsOption(points: readonly WaitingRoomPoint[]): ChartOption {
  const step = { step: 'end' as const };
  const names = [...new Set(points.flatMap((p) => Object.keys(p.limits ?? {})))].sort();
  const series: LineSeries[] = names.map((name, i) => {
    const color = INSTANCE_PALETTE[i % INSTANCE_PALETTE.length]!;
    return line(`Limit ${shortInstance(name)}`, color, xy(points, (p) => p.limits?.[name] ?? null), step);
  });
  series.push(
    dashed('Capacity (sum of limits)', C.target, xy(points, (p) => (p.limits ? p.capacity : null)), { ...step, yAxisIndex: 1 }),
  );
  return { ...baseOption('limit per server', { rightAxis: 'room capacity' }), series };
}

// Totals ----------------------------------------------------------------------------------------
export interface TrafficTotals {
  buckets: number;
  requested: number;
  arrivals: number;
  httpRequests: number;
  successful: number;
  errors: number;
  business: number;
  dropped: number;
  reservationsAttempted: number;
  reservationsSucceeded: number;
  purchasesCompleted: number;
  purchasesFailed: number;
  rejectedSoldOut: number;
}

/** Each TrafficPoint is a 1-second bucket, so per-second counts sum to totals. */
export function trafficTotals(points: readonly TrafficPoint[]): TrafficTotals {
  const t: TrafficTotals = {
    buckets: points.length,
    requested: 0,
    arrivals: 0,
    httpRequests: 0,
    successful: 0,
    errors: 0,
    business: 0,
    dropped: 0,
    reservationsAttempted: 0,
    reservationsSucceeded: 0,
    purchasesCompleted: 0,
    purchasesFailed: 0,
    rejectedSoldOut: 0,
  };
  for (const p of points) {
    t.requested += p.targetRate;
    t.arrivals += p.achieved;
    t.httpRequests += p.rps;
    t.successful += p.ok;
    t.errors += p.errors;
    t.business += p.business;
    t.dropped += p.dropped;
    t.reservationsAttempted += p.reservationsAttempted;
    t.reservationsSucceeded += p.reservationsSucceeded;
    t.purchasesCompleted += p.purchasesCompleted;
    t.purchasesFailed += p.purchasesFailed;
    t.rejectedSoldOut += p.rejectedSoldOut;
  }
  return t;
}
