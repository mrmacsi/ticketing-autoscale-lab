import type { SchedulePoint, TrafficPattern } from '@/types/contracts';

// Mirrors the TrafficConfig pattern semantics in docs/contracts.md so the operator can preview
// what will be *requested* of the load generator. It is a plan, not telemetry.

const num = (v: number | undefined, fallback = 0): number => (typeof v === 'number' && Number.isFinite(v) ? v : fallback);

export function sortedPoints(points: SchedulePoint[] | undefined): SchedulePoint[] {
  return [...(points ?? [])].filter((p) => Number.isFinite(p.atSeconds) && Number.isFinite(p.rate)).sort((a, b) => a.atSeconds - b.atSeconds);
}

/**
 * Illustrative burst windows for random_bursts. The generator derives the real positions from the
 * seed with its own PRNG, so the preview spaces bursts evenly and the UI says so.
 */
export function illustrativeBurstWindows(pattern: TrafficPattern, durationSeconds: number): Array<[number, number]> {
  const count = Math.max(0, Math.floor(num(pattern.burstCount)));
  const len = Math.max(0, num(pattern.burstDurationSeconds));
  if (count === 0 || len === 0 || durationSeconds <= 0) return [];
  const slot = durationSeconds / count;
  const windows: Array<[number, number]> = [];
  for (let k = 0; k < count; k++) {
    const centre = slot * (k + 0.5);
    const start = Math.max(0, Math.min(durationSeconds - len, centre - len / 2));
    windows.push([start, Math.min(durationSeconds, start + len)]);
  }
  return windows;
}

/** Requested arrivals/s at offset `t` seconds from traffic start. */
export function rateAt(pattern: TrafficPattern, t: number, durationSeconds: number): number {
  const d = Math.max(0, durationSeconds);
  let r: number;
  switch (pattern.type) {
    case 'constant':
      r = num(pattern.rate);
      break;
    case 'ramp_up':
    case 'ramp_down': {
      const start = num(pattern.startRate);
      const end = num(pattern.endRate);
      const f = d > 0 ? Math.min(1, Math.max(0, t / d)) : 1;
      r = start + (end - start) * f;
      break;
    }
    case 'spike': {
      const at = num(pattern.spikeAtSeconds);
      const len = num(pattern.spikeDurationSeconds);
      r = t >= at && t < at + len ? num(pattern.peakRate) : num(pattern.baseRate);
      break;
    }
    case 'random_bursts': {
      const inBurst = illustrativeBurstWindows(pattern, d).some(([s, e]) => t >= s && t < e);
      r = inBurst ? num(pattern.peakRate) : num(pattern.baseRate);
      break;
    }
    case 'custom':
      r = interpolate(sortedPoints(pattern.points), t);
      break;
    default:
      r = 0;
  }
  return Math.max(0, r);
}

/** Piecewise-linear interpolation; flat before the first and after the last point. */
export function interpolate(points: SchedulePoint[], t: number): number {
  if (points.length === 0) return 0;
  const first = points[0]!;
  const last = points[points.length - 1]!;
  if (t <= first.atSeconds) return first.rate;
  if (t >= last.atSeconds) return last.rate;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1]!;
    const b = points[i]!;
    if (t <= b.atSeconds) {
      const span = b.atSeconds - a.atSeconds;
      if (span <= 0) return b.rate;
      return a.rate + ((b.rate - a.rate) * (t - a.atSeconds)) / span;
    }
  }
  return last.rate;
}

export interface RateProfile {
  /** [secondsFromStart, arrivalsPerSecond] at 1-second resolution (start of each second). */
  points: Array<[number, number]>;
  /** Σ per-second requested rate — the same definition as Report.measured.arrivalsRequested. */
  requestedArrivals: number;
  /** Arrivals expected to be scheduled once totalArrivals (if > 0) caps the run. */
  expectedArrivals: number;
  /** Second at which totalArrivals is reached, or null if never. */
  capReachedAtSeconds: number | null;
  peakRate: number;
  averageRate: number;
}

export function buildRateProfile(pattern: TrafficPattern, durationSeconds: number, totalArrivals = 0): RateProfile {
  const d = Math.max(0, Math.floor(durationSeconds));
  const points: Array<[number, number]> = [];
  let requested = 0;
  let peak = 0;
  let capReachedAt: number | null = null;
  for (let s = 0; s < d; s++) {
    const r = rateAt(pattern, s, d);
    points.push([s, r]);
    requested += r;
    peak = Math.max(peak, r);
    if (totalArrivals > 0 && capReachedAt === null && requested >= totalArrivals) capReachedAt = s + 1;
  }
  if (d > 0) points.push([d, rateAt(pattern, d, d)]);
  const expected = totalArrivals > 0 ? Math.min(requested, totalArrivals) : requested;
  return {
    points,
    requestedArrivals: requested,
    expectedArrivals: expected,
    capReachedAtSeconds: capReachedAt,
    peakRate: peak,
    averageRate: d > 0 ? requested / d : 0,
  };
}

export function peakRate(pattern: TrafficPattern, durationSeconds: number): number {
  switch (pattern.type) {
    case 'constant':
      return Math.max(0, num(pattern.rate));
    case 'ramp_up':
    case 'ramp_down':
      return Math.max(0, num(pattern.startRate), num(pattern.endRate));
    case 'spike':
    case 'random_bursts':
      return Math.max(0, num(pattern.baseRate), num(pattern.peakRate));
    case 'custom':
      return Math.max(0, ...sortedPoints(pattern.points).map((p) => p.rate));
    default:
      return buildRateProfile(pattern, durationSeconds).peakRate;
  }
}
