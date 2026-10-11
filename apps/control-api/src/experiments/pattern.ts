import type { TrafficPattern } from '../types.js';

/**
 * Requested arrival rate at second t — the same definitions as the Go generator
 * (load-generator/scenarios). random_bursts positions depend on the generator's seeded RNG, so
 * here only its envelope (base and peak) is used for validation and cost estimates.
 */
export function rateAt(pattern: TrafficPattern, t: number, duration: number): number {
  switch (pattern.type) {
    case 'constant':
      return pattern.rate ?? 0;
    case 'ramp_up':
    case 'ramp_down': {
      const start = pattern.startRate ?? 0;
      const end = pattern.endRate ?? 0;
      return duration <= 0 ? start : start + ((end - start) * Math.min(t, duration)) / duration;
    }
    case 'spike': {
      const at = pattern.spikeAtSeconds ?? 0;
      const inSpike = t >= at && t < at + (pattern.spikeDurationSeconds ?? 0);
      return inSpike ? pattern.peakRate ?? 0 : pattern.baseRate ?? 0;
    }
    case 'random_bursts':
      return pattern.baseRate ?? 0;
    case 'custom': {
      const points = [...(pattern.points ?? [])].sort((a, b) => a.atSeconds - b.atSeconds);
      if (!points.length) return 0;
      if (t <= points[0].atSeconds) return points[0].rate;
      for (let i = 1; i < points.length; i++) {
        const a = points[i - 1];
        const b = points[i];
        if (t <= b.atSeconds) return a.rate + ((b.rate - a.rate) * (t - a.atSeconds)) / Math.max(1e-9, b.atSeconds - a.atSeconds);
      }
      return points[points.length - 1].rate;
    }
  }
}

export function peakRate(pattern: TrafficPattern, duration: number): number {
  if (pattern.type === 'random_bursts') return Math.max(pattern.baseRate ?? 0, pattern.peakRate ?? 0);
  let peak = 0;
  for (let t = 0; t < duration; t++) peak = Math.max(peak, rateAt(pattern, t, duration));
  return peak;
}

export function expectedArrivals(pattern: TrafficPattern, duration: number, totalCap: number): number {
  let total = 0;
  for (let t = 0; t < duration; t++) total += rateAt(pattern, t + 0.5, duration);
  if (pattern.type === 'random_bursts') {
    const extra = (pattern.burstCount ?? 0) * (pattern.burstDurationSeconds ?? 0) * Math.max(0, (pattern.peakRate ?? 0) - (pattern.baseRate ?? 0));
    total += extra;
  }
  return Math.round(totalCap > 0 ? Math.min(total, totalCap) : total);
}
