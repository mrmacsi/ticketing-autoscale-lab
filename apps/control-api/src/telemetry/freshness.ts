import type { Freshness } from '../types.js';

export function freshness(lastAt: number | null, now: number, liveWithinS: number, staleAfterS: number, note?: string): Freshness {
  if (lastAt === null) return { status: 'missing', lastAt: null, ageSeconds: null, note };
  const age = Math.max(0, (now - lastAt) / 1000);
  const status = age <= liveWithinS ? 'live' : age <= staleAfterS ? 'delayed' : 'stale';
  return { status, lastAt: new Date(lastAt).toISOString(), ageSeconds: Math.round(age), note };
}

/**
 * Cloud Monitoring publishes VM CPU once per minute and points become visible 1-4 minutes later,
 * so a sample is never "live": it is at best "delayed" (up to 5 minutes) and then "stale".
 */
export function cpuFreshness(sampleTime: number | null, now: number): Freshness {
  const note = 'Cloud Monitoring CPU samples arrive 1-4 minutes after measurement';
  if (sampleTime === null) return { status: 'missing', lastAt: null, ageSeconds: null, note };
  const age = Math.max(0, (now - sampleTime) / 1000);
  return { status: age <= 300 ? 'delayed' : 'stale', lastAt: new Date(sampleTime).toISOString(), ageSeconds: Math.round(age), note };
}
