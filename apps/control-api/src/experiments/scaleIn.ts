import type { InstanceView, MigView, ScaleInReport, ScalingPoint } from '../types.js';

/** How long the instance group is still watched after completion for the scale-in to finish. */
export const SCALE_IN_WATCH_LIMIT_MS = 15 * 60_000;

/** Back at the minimum: MIG target and VM count at or below it, and no VM still being deleted. */
export function atMinimum(mig: MigView, instances: InstanceView[], min: number): boolean {
  return mig.targetSize <= min && mig.actualCount <= min && !instances.some((i) => i.phase === 'terminating');
}

/**
 * Start (ms) of the trailing run of recorded scaling observations at or below `min`, not earlier than `since`;
 * null when the last observation is above the minimum (or there is none).
 */
export function backAtMinimumSince(points: ScalingPoint[], min: number, since: number): number | null {
  const sorted = [...points].sort((a, b) => a.t - b.t);
  let start: number | null = null;
  for (let i = sorted.length - 1; i >= 0; i--) {
    const p = sorted[i];
    if (p.t < since || p.actual > min || p.target > min) break;
    start = p.t;
  }
  return start;
}

const servers = (n: number) => `${n} server${n === 1 ? '' : 's'}`;

/** "64 s", "4 min 5 s". */
export function fmtSeconds(seconds: number): string {
  if (seconds < 120) return `${seconds} s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s ? `${m} min ${s} s` : `${m} min`;
}

/** One-line summary, e.g. "Back to 1 server 64 s after traffic stopped (2 removed)". */
export function scaleInSummary(r: ScaleInReport): string {
  const note = r.note ? ` (${r.note})` : '';
  switch (r.status) {
    case 'complete': {
      const when = r.secondsAfterTrafficStopped !== null ? ` ${fmtSeconds(r.secondsAfterTrafficStopped)} after traffic stopped` : '';
      const removed = r.serversRemoved ? ` (${r.serversRemoved} removed)` : '';
      return `Back to ${servers(r.targetServers)}${when}${removed}`;
    }
    case 'timed_out':
      return `Still above ${servers(r.targetServers)} when the observation ended${note}`;
    case 'not_needed':
      return `Not needed: never above ${servers(r.targetServers)}`;
    case 'superseded':
      return `Observation stopped before the group was back at ${servers(r.targetServers)}${note}`;
    case 'unavailable':
      return `Not observed${note}`;
  }
}
