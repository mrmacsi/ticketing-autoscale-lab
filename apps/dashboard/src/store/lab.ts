import { inject, reactive, shallowReactive, shallowRef, type InjectionKey, type ShallowRef } from 'vue';
import { ApiError, type ApiClient, type FieldIssue } from '@/api/client';
import { connectStream, type SseMessage, type StreamHandle, type StreamState } from '@/api/sse';

/** Limits and presets are reloaded this often (and on every stream reconnect). */
const META_REFRESH_MS = 60_000;
import { byT, byTAndInstance, MAX_POINTS, mergeBounded } from '@/lib/series';
import { parseTime } from '@/lib/format';
import type {
  CpuPoint,
  Experiment,
  ExperimentConfig,
  InventoryPoint,
  LiveSnapshot,
  Meta,
  ScalingPoint,
  TickEvent,
  TimelineEvent,
  TrafficPoint,
  WaitingRoomPoint,
} from '@/types/contracts';

export const MAX_TIMELINE_EVENTS = 2000;

export type ActionName = 'prepare' | 'start' | 'stop' | 'complete' | 'resetInventory' | 'reset';

export interface ActionFailure {
  action: ActionName;
  status: number;
  code: string;
  message: string;
  issues: FieldIssue[];
}

export interface LiveSeries {
  traffic: TrafficPoint[];
  scaling: ScalingPoint[];
  cpu: CpuPoint[];
  inventory: InventoryPoint[];
  waitingRoom: WaitingRoomPoint[];
}

export interface LabStoreOptions {
  maxPoints?: number;
  fetchImpl?: typeof fetch;
  streamUrl?: string;
}

export function createLabStore(api: ApiClient, opts: LabStoreOptions = {}) {
  const maxPoints = opts.maxPoints ?? MAX_POINTS;

  const meta = shallowRef<Meta | null>(null);
  const snapshot = shallowRef<LiveSnapshot | null>(null);
  const experiment = shallowRef<Experiment | null>(null);
  const timeline: ShallowRef<TimelineEvent[]> = shallowRef([]);
  const series = shallowReactive<LiveSeries>({ traffic: [], scaling: [], cpu: [], inventory: [], waitingRoom: [] });

  const status = reactive({
    metaError: null as string | null,
    stateError: null as string | null,
    seriesExperimentId: null as string | null,
    seriesLoading: false,
    seriesError: null as string | null,
    busy: null as ActionName | null,
    lastFailure: null as ActionFailure | null,
  });

  const stream = reactive<StreamState>({ status: 'closed', attempt: 0, nextRetryAt: null, lastError: null, lastMessageAt: null });
  /** Wall clock re-based on the control API's serverTime so ages do not depend on the browser clock. */
  const clock = reactive({ nowMs: Date.now(), offsetMs: 0 });

  let handle: StreamHandle | null = null;
  let clockTimer: ReturnType<typeof setInterval> | null = null;
  let metaTimer: ReturnType<typeof setInterval> | null = null;

  function serverNow(): number {
    return clock.nowMs + clock.offsetMs;
  }

  // ---------------------------------------------------------------- state application

  function resetSeries(): void {
    series.traffic = [];
    series.scaling = [];
    series.cpu = [];
    series.inventory = [];
    series.waitingRoom = [];
    timeline.value = [];
  }

  function appendSeries(append: Partial<LiveSeries>): void {
    if (append.traffic?.length) series.traffic = mergeBounded(series.traffic, append.traffic, byT, maxPoints);
    if (append.scaling?.length) series.scaling = mergeBounded(series.scaling, append.scaling, byT, maxPoints);
    // CPU holds one point per instance per sample, so allow proportionally more.
    if (append.cpu?.length) series.cpu = mergeBounded(series.cpu, append.cpu, byTAndInstance, maxPoints * 8);
    if (append.inventory?.length) series.inventory = mergeBounded(series.inventory, append.inventory, byT, maxPoints);
    if (append.waitingRoom?.length) series.waitingRoom = mergeBounded(series.waitingRoom, append.waitingRoom, byT, maxPoints);
  }

  /** Only events received from the server are ever stored; nothing is synthesised here. */
  function addTimelineEvents(events: readonly TimelineEvent[]): void {
    if (!events.length) return;
    const byId = new Map(timeline.value.map((e) => [e.id, e]));
    for (const e of events) byId.set(e.id, e);
    const sorted = [...byId.values()].sort((a, b) => (parseTime(a.t) ?? 0) - (parseTime(b.t) ?? 0));
    timeline.value = sorted.length > MAX_TIMELINE_EVENTS ? sorted.slice(sorted.length - MAX_TIMELINE_EVENTS) : sorted;
  }

  async function bootstrapSeries(id: string): Promise<void> {
    status.seriesLoading = true;
    status.seriesError = null;
    try {
      const s = await api.getSeries(id);
      if (status.seriesExperimentId !== id) return;
      appendSeries(s);
      addTimelineEvents(s.events ?? []);
    } catch (err) {
      if (status.seriesExperimentId === id) status.seriesError = err instanceof Error ? err.message : String(err);
    } finally {
      if (status.seriesExperimentId === id) status.seriesLoading = false;
    }
  }

  function ensureSeriesFor(id: string | null): void {
    if (id === status.seriesExperimentId) return;
    status.seriesExperimentId = id;
    resetSeries();
    if (id) void bootstrapSeries(id);
    else status.seriesLoading = false;
  }

  function applyExperiment(exp: Experiment | null): void {
    experiment.value = exp;
    ensureSeriesFor(exp?.id ?? null);
  }

  function applySnapshot(s: LiveSnapshot): void {
    const server = parseTime(s.serverTime);
    clock.nowMs = Date.now();
    if (server != null) clock.offsetMs = server - clock.nowMs;
    snapshot.value = s;
    applyExperiment(s.experiment);
  }

  function applyTick(tick: TickEvent): void {
    applySnapshot(tick.snapshot);
    if (tick.append) appendSeries(tick.append);
  }

  function handleMessage(msg: SseMessage): void {
    let payload: unknown;
    try {
      payload = JSON.parse(msg.data);
    } catch {
      return; // malformed frame: ignore rather than render something invented
    }
    switch (msg.event) {
      case 'tick':
        applyTick(payload as TickEvent);
        break;
      case 'timeline':
        addTimelineEvents([payload as TimelineEvent]);
        break;
      case 'experiment':
        applyExperiment(payload as Experiment | null);
        break;
      default:
        break;
    }
  }

  // ---------------------------------------------------------------- lifecycle

  async function loadMeta(): Promise<void> {
    try {
      meta.value = await api.getMeta();
      status.metaError = null;
    } catch (err) {
      status.metaError = err instanceof Error ? err.message : String(err);
    }
  }

  async function refreshState(): Promise<void> {
    try {
      applySnapshot(await api.getState());
      status.stateError = null;
    } catch (err) {
      status.stateError = err instanceof Error ? err.message : String(err);
    }
  }

  function connect(): void {
    handle?.close();
    handle = connectStream({
      url: opts.streamUrl ?? '/api/stream',
      getHeaders: api.authHeaders,
      fetchImpl: opts.fetchImpl ?? api.fetchImpl,
      onMessage: handleMessage,
      onState: (s) => {
        // A (re)connect usually follows a control-plane deploy: reload limits and presets so the form never
        // validates against values the server no longer uses.
        if (s.status === 'connected' && stream.status !== 'connected') void loadMeta();
        Object.assign(stream, s);
      },
      onAuthFailure: () => {
        /* the REST client's auth hook drives the auth screen; a follow-up /api/me reveals it */
        void api.getMe().catch(() => undefined);
      },
    });
  }

  async function startLive(): Promise<void> {
    if (!clockTimer) clockTimer = setInterval(() => (clock.nowMs = Date.now()), 1000);
    if (!metaTimer) metaTimer = setInterval(() => void loadMeta(), META_REFRESH_MS);
    await Promise.all([loadMeta(), refreshState()]);
    connect();
  }

  function stopLive(): void {
    handle?.close();
    handle = null;
    if (clockTimer) clearInterval(clockTimer);
    clockTimer = null;
    if (metaTimer) clearInterval(metaTimer);
    metaTimer = null;
  }

  /** Drop everything (e.g. after an auth failure) so no stale data stays on screen. */
  function clear(): void {
    stopLive();
    meta.value = null;
    snapshot.value = null;
    experiment.value = null;
    status.seriesExperimentId = null;
    resetSeries();
  }

  // ---------------------------------------------------------------- actions

  async function runAction<T>(action: ActionName, fn: () => Promise<T>): Promise<{ ok: true; value: T } | { ok: false; failure: ActionFailure }> {
    if (status.busy) {
      return { ok: false, failure: { action, status: 0, code: 'busy', message: `Another action (${status.busy}) is in progress.`, issues: [] } };
    }
    status.busy = action;
    status.lastFailure = null;
    try {
      const value = await fn();
      if (isExperiment(value)) applyExperiment(value);
      void refreshState();
      return { ok: true, value };
    } catch (err) {
      const failure: ActionFailure =
        err instanceof ApiError
          ? { action, status: err.status, code: err.code, message: err.message, issues: err.fieldIssues }
          : { action, status: 0, code: 'error', message: err instanceof Error ? err.message : String(err), issues: [] };
      status.lastFailure = failure;
      return { ok: false, failure };
    } finally {
      status.busy = null;
    }
  }

  const currentId = (): string => {
    const id = experiment.value?.id;
    if (!id) throw new Error('No current experiment');
    return id;
  };

  const actions = {
    prepare: (config: ExperimentConfig) => runAction('prepare', () => api.createExperiment(config)),
    start: () => runAction('start', () => api.startExperiment(currentId())),
    stop: () => runAction('stop', () => api.stopExperiment(currentId())),
    complete: () => runAction('complete', () => api.completeExperiment(currentId())),
    resetInventory: () => runAction('resetInventory', () => api.resetInventory(currentId())),
    reset: () => runAction('reset', () => api.resetLab()),
  };

  return {
    api,
    meta,
    snapshot,
    experiment,
    timeline,
    series,
    status,
    stream,
    clock,
    serverNow,
    startLive,
    stopLive,
    clear,
    loadMeta,
    refreshState,
    retryStream: () => handle?.retryNow(),
    // exposed for the SSE pipeline and tests
    handleMessage,
    applyTick,
    applySnapshot,
    applyExperiment,
    addTimelineEvents,
    ...actions,
  };
}

function isExperiment(v: unknown): v is Experiment {
  return !!v && typeof v === 'object' && typeof (v as Experiment).id === 'string' && typeof (v as Experiment).state === 'string';
}

export type LabStore = ReturnType<typeof createLabStore>;

export const LAB_STORE: InjectionKey<LabStore> = Symbol('lab-store');

export function useLab(): LabStore {
  const store = inject(LAB_STORE);
  if (!store) throw new Error('Lab store not provided');
  return store;
}
