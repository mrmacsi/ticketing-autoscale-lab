export const NA = 'N/A';
export const DASH = '—';

type Num = number | null | undefined;

const isNum = (v: Num): v is number => typeof v === 'number' && Number.isFinite(v);

export function fmtNumber(v: Num, digits = 0, fallback = NA): string {
  if (!isNum(v)) return fallback;
  return v.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

/** Adaptive precision for rates: 0.42, 4.2, 42 */
export function fmtRate(v: Num, fallback = NA): string {
  if (!isNum(v)) return fallback;
  const abs = Math.abs(v);
  return fmtNumber(v, abs >= 100 ? 0 : abs >= 10 ? 1 : 2, fallback);
}

/** Fraction (0..1) → "61.2 %". */
export function fmtFractionPct(v: Num, digits = 1, fallback = NA): string {
  if (!isNum(v)) return fallback;
  return `${(v * 100).toFixed(digits)} %`;
}

/** Already-percent value → "61.2 %". */
export function fmtPct(v: Num, digits = 1, fallback = NA): string {
  if (!isNum(v)) return fallback;
  return `${v.toFixed(digits)} %`;
}

export function fmtMs(v: Num, fallback = NA): string {
  if (!isNum(v)) return fallback;
  if (v >= 1000) return `${(v / 1000).toFixed(2)} s`;
  return `${v.toFixed(v >= 100 ? 0 : 1)} ms`;
}

export function fmtUsd(v: Num, digits = 2, fallback = NA): string {
  if (!isNum(v)) return fallback;
  return `$${v.toFixed(digits)}`;
}

export function fmtCents(cents: Num, fallback = NA): string {
  if (!isNum(cents)) return fallback;
  return `$${(cents / 100).toFixed(2)}`;
}

/** 130 → "2m10s"; 3725 → "1h02m"; 9 → "9s". */
export function fmtAge(seconds: Num, fallback = NA): string {
  if (!isNum(seconds)) return fallback;
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`;
  return `${Math.floor(s / 3600)}h${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
}

/** 125 → "02:05"; -5 → "-00:05"; 3725 → "1:02:05". */
export function fmtClock(seconds: number): string {
  const neg = seconds < 0;
  const s = Math.floor(Math.abs(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const body = h > 0 ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}` : `${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`;
  return neg ? `-${body}` : body;
}

export function fmtDuration(seconds: Num, fallback = NA): string {
  if (!isNum(seconds)) return fallback;
  return fmtClock(seconds);
}

export function parseTime(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
}

/** Absolute UTC wall clock "14:03:27". */
export function fmtTimeOfDay(iso: string | number | null | undefined, fallback = NA): string {
  const t = typeof iso === 'number' ? iso : parseTime(iso);
  if (t == null) return fallback;
  return new Date(t).toISOString().slice(11, 19);
}

/** "2026-10-09 14:03:27 UTC" */
export function fmtDateTime(iso: string | number | null | undefined, fallback = NA): string {
  const t = typeof iso === 'number' ? iso : parseTime(iso);
  if (t == null) return fallback;
  return `${new Date(t).toISOString().slice(0, 19).replace('T', ' ')} UTC`;
}

export function ageSeconds(iso: string | null | undefined, nowMs: number): number | null {
  const t = parseTime(iso);
  if (t == null) return null;
  return Math.max(0, (nowMs - t) / 1000);
}

/**
 * Time of an event relative to traffic start (or experiment creation before traffic), e.g. "+01:23".
 * Returns null when there is no reference time.
 */
export function eventOffset(eventIso: string, trafficStartedAt?: string | null, createdAt?: string | null): { text: string; label: string } | null {
  const t = parseTime(eventIso);
  const ref = eventReference(trafficStartedAt, createdAt);
  if (t == null || ref == null) return null;
  return { text: fmtClock((t - ref.t) / 1000), label: ref.label };
}

/** Reference time for relative event times: traffic start, else experiment creation. */
export function eventReference(trafficStartedAt?: string | null, createdAt?: string | null): { t: number; label: string } | null {
  const traffic = parseTime(trafficStartedAt);
  if (traffic != null) return { t: traffic, label: 'since traffic start' };
  const created = parseTime(createdAt);
  return created != null ? { t: created, label: 'since experiment creation' } : null;
}
