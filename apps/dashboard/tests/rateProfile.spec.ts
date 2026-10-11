import { describe, expect, it } from 'vitest';
import { buildRateProfile, illustrativeBurstWindows, interpolate, peakRate, rateAt } from '@/lib/rateProfile';

describe('rate profile preview', () => {
  it('constant: flat rate, total = rate × duration', () => {
    const p = buildRateProfile({ type: 'constant', rate: 20 }, 60);
    expect(p.points).toHaveLength(61);
    expect(p.points.every(([, r]) => r === 20)).toBe(true);
    expect(p.requestedArrivals).toBe(1200);
    expect(p.expectedArrivals).toBe(1200);
    expect(p.peakRate).toBe(20);
    expect(p.averageRate).toBe(20);
  });

  it('ramp_up: linear from start to end over the duration', () => {
    const pattern = { type: 'ramp_up' as const, startRate: 0, endRate: 100 };
    expect(rateAt(pattern, 0, 100)).toBe(0);
    expect(rateAt(pattern, 50, 100)).toBe(50);
    expect(rateAt(pattern, 100, 100)).toBe(100);
    // Σ_{s=0}^{99} s = 4950
    expect(buildRateProfile(pattern, 100).requestedArrivals).toBe(4950);
    expect(peakRate(pattern, 100)).toBe(100);
  });

  it('ramp_down: linear decrease', () => {
    const pattern = { type: 'ramp_down' as const, startRate: 100, endRate: 0 };
    expect(rateAt(pattern, 25, 100)).toBe(75);
    expect(buildRateProfile(pattern, 100).requestedArrivals).toBe(5050);
  });

  it('spike: base rate with a peak window [spikeAt, spikeAt + duration)', () => {
    const pattern = { type: 'spike' as const, baseRate: 5, peakRate: 200, spikeAtSeconds: 30, spikeDurationSeconds: 20 };
    expect(rateAt(pattern, 29, 120)).toBe(5);
    expect(rateAt(pattern, 30, 120)).toBe(200);
    expect(rateAt(pattern, 49, 120)).toBe(200);
    expect(rateAt(pattern, 50, 120)).toBe(5);
    expect(buildRateProfile(pattern, 120).requestedArrivals).toBe(100 * 5 + 20 * 200);
  });

  it('random_bursts: total depends only on count × length, not positions', () => {
    const pattern = { type: 'random_bursts' as const, baseRate: 5, peakRate: 50, burstCount: 3, burstDurationSeconds: 10 };
    const windows = illustrativeBurstWindows(pattern, 300);
    expect(windows).toHaveLength(3);
    windows.forEach(([s, e]) => expect(e - s).toBe(10));
    expect(buildRateProfile(pattern, 300).requestedArrivals).toBe(270 * 5 + 30 * 50);
  });

  it('custom: piecewise linear, flat before first and after last point, unsorted input', () => {
    const pattern = {
      type: 'custom' as const,
      points: [
        { atSeconds: 120, rate: 80 },
        { atSeconds: 20, rate: 0 },
      ],
    };
    expect(rateAt(pattern, 0, 200)).toBe(0);
    expect(rateAt(pattern, 70, 200)).toBe(40);
    expect(rateAt(pattern, 150, 200)).toBe(80);
    expect(interpolate([], 10)).toBe(0);
    expect(peakRate(pattern, 200)).toBe(80);
  });

  it('caps expected arrivals by totalArrivals and reports when the cap is reached', () => {
    const p = buildRateProfile({ type: 'constant', rate: 10 }, 600, 1000);
    expect(p.requestedArrivals).toBe(6000);
    expect(p.expectedArrivals).toBe(1000);
    expect(p.capReachedAtSeconds).toBe(100);
    expect(buildRateProfile({ type: 'constant', rate: 10 }, 600, 0).capReachedAtSeconds).toBeNull();
  });
});
