import type {
  AdmissionMode, WaitingRoomAdaptiveSettings, WaitingRoomDecision, WaitingRoomServer, WaitingRoomServerPhase, WaitingRoomServerSignals,
  WaitingRoomStats,
} from '../types.js';

/** Body of `PUT /admin/config` (docs/waiting-room.md 3.4). */
export interface WaitingRoomConfig {
  enabled: boolean;
  eventId?: string;
  /** Fixed mode: per healthy server. Adaptive mode: the starting limit of each new server. */
  activeUsersPerServer: number;
  admissionTtlSeconds: number;
  soldOutGraceSeconds: number;
  /** Sent when the room is enabled; omitted from disabling bodies (the queue keeps its previous value). */
  admissionMode?: AdmissionMode;
  /** Partial: the queue merges it field by field and keeps its defaults for the rest. */
  adaptive?: { maxPerServer: number };
}

/**
 * Calls the queue server's private /admin/* API on its VPC address (WAITING_ROOM_URL) with the
 * lab admin token. `available` is false when the waiting room is not deployed.
 */
export class WaitingRoomClient {
  constructor(private readonly baseUrl: string | null, private readonly token: string) {}

  get available() { return this.baseUrl !== null; }

  private async call<T>(method: string, path: string, body?: unknown, timeoutMs = 5000): Promise<T> {
    if (!this.baseUrl) throw new Error('The waiting room is not deployed (WAITING_ROOM_URL is not set)');
    const response = await fetch(`${this.baseUrl}${path}`, {
      method,
      headers: { Authorization: `Bearer ${this.token}`, 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
    const text = await response.text();
    let json: { data?: unknown; message?: string } = {};
    try { json = text ? JSON.parse(text) : {}; } catch { /* non-JSON error body: report the text */ }
    if (!response.ok) throw new Error(`Waiting room ${method} ${path} failed (${response.status}): ${json.message ?? text}`);
    return json.data as T;
  }

  configure(config: WaitingRoomConfig) {
    return this.call<Record<string, unknown>>('PUT', '/admin/config', config);
  }

  reset() {
    return this.call<unknown>('POST', '/admin/reset');
  }

  async stats(): Promise<WaitingRoomStats> {
    return normalizeStats(await this.call<unknown>('GET', '/admin/stats', undefined, 1500));
  }
}

const num = (v: unknown, fallback = 0): number => (typeof v === 'number' && Number.isFinite(v) ? v : fallback);
const numOrNull = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);
const strOrNull = (v: unknown): string | null => (typeof v === 'string' && v !== '' ? v : null);
const oneOf = <T extends string>(v: unknown, allowed: readonly T[]): T | null => (allowed.includes(v as T) ? (v as T) : null);
const record = (v: unknown): Record<string, unknown> | null => (v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : null);

function normalizeSignals(raw: unknown): WaitingRoomServerSignals | null {
  const s = record(raw);
  if (!s) return null;
  return {
    busy: numOrNull(s.busy), activeWorkers: numOrNull(s.activeWorkers), totalWorkers: numOrNull(s.totalWorkers),
    listenQueue: numOrNull(s.listenQueue), cpu: numOrNull(s.cpu), observedAt: strOrNull(s.observedAt),
  };
}

function normalizeServer(raw: unknown): WaitingRoomServer {
  const s = record(raw) ?? {};
  return {
    name: String(s.name ?? ''), ip: String(s.ip ?? ''),
    limit: numOrNull(s.limit),
    phase: oneOf<WaitingRoomServerPhase>(s.phase, ['slow_start', 'steady', 'fixed']),
    decision: oneOf<WaitingRoomDecision>(s.decision, ['increase', 'decrease', 'hold']),
    reason: strOrNull(s.reason), decisionAt: strOrNull(s.decisionAt),
    decreases: numOrNull(s.decreases),
    signals: normalizeSignals(s.signals),
  };
}

function normalizeAdaptive(raw: unknown): WaitingRoomAdaptiveSettings | null {
  const a = record(raw);
  if (!a) return null;
  return {
    minPerServer: numOrNull(a.minPerServer), maxPerServer: numOrNull(a.maxPerServer),
    busyHigh: numOrNull(a.busyHigh), busyLow: numOrNull(a.busyLow), cpuHigh: numOrNull(a.cpuHigh), cpuLow: numOrNull(a.cpuLow),
    decreaseFactor: numOrNull(a.decreaseFactor), slowStartFactor: numOrNull(a.slowStartFactor), cooldownSeconds: numOrNull(a.cooldownSeconds),
  };
}

/** Coerces the queue server's JSON into WaitingRoomStats; unknown/absent gauges stay null rather than becoming 0. */
export function normalizeStats(raw: unknown): WaitingRoomStats {
  if (!raw || typeof raw !== 'object') throw new Error('Waiting room stats response has no data object');
  const r = raw as Record<string, unknown>;
  const wait = (r.waitMs ?? {}) as Record<string, unknown>;
  const inv = r.inventory as Record<string, unknown> | null | undefined;
  return {
    enabled: r.enabled === true,
    eventId: strOrNull(r.eventId),
    soldOut: r.soldOut === true,
    waiting: num(r.waiting),
    active: num(r.active),
    capacity: num(r.capacity),
    healthyServers: num(r.healthyServers),
    servers: Array.isArray(r.servers) ? r.servers.map(normalizeServer) : [],
    activeUsersPerServer: num(r.activeUsersPerServer),
    admissionMode: oneOf<AdmissionMode>(r.admissionMode, ['fixed', 'adaptive']),
    adaptive: normalizeAdaptive(r.adaptive),
    joinedTotal: num(r.joinedTotal),
    admittedTotal: num(r.admittedTotal),
    releasedTotal: num(r.releasedTotal),
    expiredTotal: num(r.expiredTotal),
    soldOutTotal: num(r.soldOutTotal),
    abandonedTotal: numOrNull(r.abandonedTotal),
    store: oneOf<'memory' | 'redis'>(r.store, ['memory', 'redis']),
    leaderInstance: typeof r.leaderInstance === 'string' && r.leaderInstance ? r.leaderInstance : null,
    admissionRatePerSecond: num(r.admissionRatePerSecond),
    waitMs: { p50: numOrNull(wait.p50), p95: numOrNull(wait.p95), max: numOrNull(wait.max), count: num(wait.count) },
    oldestWaitingSeconds: numOrNull(r.oldestWaitingSeconds),
    inventory: inv && typeof inv === 'object'
      ? { available: num(inv.available), soldOut: inv.soldOut === true, observedAt: String(inv.observedAt ?? '') }
      : null,
    capacitySource: typeof r.capacitySource === 'string' ? r.capacitySource : 'unknown',
    capacityError: strOrNull(r.capacityError),
    updatedAt: strOrNull(r.updatedAt),
  };
}
