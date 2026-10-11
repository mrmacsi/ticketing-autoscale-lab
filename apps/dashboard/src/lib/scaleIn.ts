import type { ScaleInReport } from '@/types/contracts';

/** How long the control API keeps watching the instance group after completion (mirrors the server). */
export const SCALE_IN_WATCH_MINUTES = 15;

const servers = (n: number) => `${n} server${n === 1 ? '' : 's'}`;

/** "64 s", "4 min 5 s". */
export function fmtSecondsShort(seconds: number): string {
  if (seconds < 120) return `${seconds} s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s ? `${m} min ${s} s` : `${m} min`;
}

export type ScaleInTone = 'good' | 'bad' | null;

/**
 * One-line scale-in summary for the report and history, e.g. "Back to 1 server 64 s after traffic stopped".
 * `minServers` is the experiment's servers.min (used when the result does not carry it). Without a result:
 * a completed experiment within the watch window is still being observed; anything else was not recorded.
 */
export function scaleInSummary(
  r: ScaleInReport | null | undefined,
  minServers: number | null | undefined,
  context: { state?: string; completedAt?: string | null; nowMs?: number } = {},
): { text: string; tone: ScaleInTone } {
  const min = r?.targetServers ?? minServers ?? null;
  const target = min !== null ? servers(min) : 'the minimum';
  if (!r) {
    const completed = context.completedAt ? Date.parse(context.completedAt) : Number.NaN;
    const now = context.nowMs ?? Date.now();
    if (context.state === 'completed' && Number.isFinite(completed) && now - completed < SCALE_IN_WATCH_MINUTES * 60_000) {
      return { text: `Watching scale-in (back to ${target}, up to ${SCALE_IN_WATCH_MINUTES} min after completion)…`, tone: null };
    }
    return { text: 'Not recorded', tone: null };
  }
  switch (r.status) {
    case 'complete': {
      const when = r.secondsAfterTrafficStopped != null ? ` ${fmtSecondsShort(r.secondsAfterTrafficStopped)} after traffic stopped` : '';
      return { text: `Back to ${target}${when}`, tone: 'good' };
    }
    case 'timed_out':
      return { text: `Still above ${target} ${SCALE_IN_WATCH_MINUTES} min after completion`, tone: 'bad' };
    case 'not_needed':
      return { text: `Not needed (never above ${target})`, tone: null };
    case 'superseded':
      return { text: `Observation stopped early${r.note ? ` (${r.note})` : ''}`, tone: null };
    case 'unavailable':
      return { text: `Not observed${r.note ? ` (${r.note})` : ''}`, tone: null };
    default:
      return { text: String((r as { status: unknown }).status), tone: null };
  }
}
