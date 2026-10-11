/** Default bound for live series (1 h of 1 s buckets). */
export const MAX_POINTS = 3600;

/**
 * Appends points keeping ascending `t` order, dropping duplicates (same key) and trimming to `max`.
 * Fast path when incoming points are newer than everything held, which is the normal SSE case.
 */
export function mergeBounded<T extends { t: number }>(target: T[], incoming: readonly T[], keyOf: (p: T) => string, max = MAX_POINTS): T[] {
  if (incoming.length === 0) return target;
  const last = target.length ? target[target.length - 1]!.t : -Infinity;
  const sortedIn = [...incoming].sort((a, b) => a.t - b.t);
  let result: T[];
  if (sortedIn[0]!.t > last) {
    result = target.concat(dedupe(sortedIn, keyOf));
  } else {
    const seen = new Map<string, T>();
    for (const p of target) seen.set(keyOf(p), p);
    for (const p of sortedIn) seen.set(keyOf(p), p);
    result = [...seen.values()].sort((a, b) => a.t - b.t);
  }
  return result.length > max ? result.slice(result.length - max) : result;
}

function dedupe<T>(items: T[], keyOf: (p: T) => string): T[] {
  const seen = new Set<string>();
  const out: T[] = [];
  for (const p of items) {
    const k = keyOf(p);
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(p);
  }
  return out;
}

export const byT = (p: { t: number }): string => String(p.t);
export const byTAndInstance = (p: { t: number; instance: string }): string => `${p.t}|${p.instance}`;
