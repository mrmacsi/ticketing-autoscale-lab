import { describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import WaitingRoomPanel from '@/components/live/WaitingRoomPanel.vue';
import ChartGrid from '@/components/charts/ChartGrid.vue';
import FreshnessPanel from '@/components/live/FreshnessPanel.vue';
import DashboardView from '@/views/DashboardView.vue';
import ReportView from '@/views/ReportView.vue';
import { adaptiveLimitsOption, waitingRoomOption } from '@/charts/options';
import { validateConfig } from '@/lib/validation';
import { cloneConfig } from '@/store/draft';
import {
  emptySeries, live, makeAdaptiveServer, makeConfig, makeExperiment, makeMeta, makeSnapshot, makeWaitingRoom, makeWaitingRoomPoint, SERVER_TIME,
} from './fixtures';
import { makeStore, mountWithStore } from './helpers';
import type { ExperimentConfig, Freshness, Report } from '@/types/contracts';

const missing: Freshness = { status: 'missing', lastAt: null, ageSeconds: null, note: 'Waiting room not deployed (WAITING_ROOM_URL is not set)' };
const stale: Freshness = { status: 'stale', lastAt: '2026-10-09T15:35:00.000Z', ageSeconds: 300 };
const enabledRoom = { enabled: true, activeUsersPerServer: 25, admissionTtlSeconds: 60 };
const adaptiveRoom = { ...enabledRoom, mode: 'adaptive' as const, maxUsersPerServer: 200 };
const NOW = Date.parse(SERVER_TIME);
const A = 'ticketing-scale-lab-app-ab12';
const B = 'ticketing-scale-lab-app-cd34';

describe('waiting room panel', () => {
  it('shows the queue server measurements', () => {
    const w = mount(WaitingRoomPanel, { props: { room: makeWaitingRoom(), freshness: live(), eventId: 'evt-1', configured: enabledRoom } });
    expect(w.find('[data-testid="wr-waiting"]').text()).toBe('3,110');
    expect(w.find('[data-testid="wr-active"]').text()).toBe('75 / 75');
    expect(w.find('[data-testid="wr-healthy"]').text()).toBe('3');
    expect(w.find('[data-testid="wr-rate"]').text()).toBe('24.5/s');
    expect(w.find('[data-testid="wr-wait-p50"]').text()).toBe('8.00 s');
    expect(w.find('[data-testid="wr-wait-p95"]').text()).toBe('41.00 s');
    expect(w.text()).toContain('max 52.00 s');
    expect(w.text()).toContain('oldest 48s');
    expect(w.find('[data-testid="wr-totals"]').text()).toBe('4,200 / 1,090');
    expect(w.find('[data-testid="wr-state"]').text()).toBe('Waiting room on');
    expect(w.find('[data-testid="wr-sold-out"]').exists()).toBe(false);
    expect(w.find('[data-testid="wr-other-event"]').exists()).toBe(false);
    expect(w.find('[data-testid="wr-configured"]').text()).toContain('on, 25 per server, TTL 60s');
  });

  it('shows a sold-out badge and flags a room serving another event', () => {
    const w = mount(WaitingRoomPanel, {
      props: { room: makeWaitingRoom({ soldOut: true, waiting: 0, soldOutTotal: 2950 }), freshness: live(), eventId: 'evt-other', configured: null },
    });
    expect(w.find('[data-testid="wr-sold-out"]').text()).toContain('Sold out');
    expect(w.find('[data-testid="wr-sold-out-total"]').text()).toBe('2,950');
    expect(w.find('[data-testid="wr-other-event"]').exists()).toBe(true);
  });

  it('renders N/A with the freshness note when the queue was never observed — never 0', () => {
    const w = mount(WaitingRoomPanel, { props: { room: null, freshness: missing, eventId: null, configured: null } });
    for (const id of ['wr-waiting', 'wr-active', 'wr-healthy', 'wr-rate', 'wr-wait-p50', 'wr-wait-p95', 'wr-totals', 'wr-sold-out-total']) {
      expect(w.find(`[data-testid="${id}"]`).text(), id).toBe('N/A');
    }
    expect(w.find('[data-freshness]').attributes('data-freshness')).toBe('missing');
    expect(w.find('[data-testid="wr-note"]').text()).toContain('not deployed');
    expect(w.text()).toContain('Waiting room telemetry unavailable');
  });

  it('keeps the last observation visible but dimmed when stale', () => {
    const w = mount(WaitingRoomPanel, { props: { room: makeWaitingRoom({ freshness: stale }), freshness: stale, eventId: 'evt-1', configured: null } });
    expect(w.find('[data-freshness]').attributes('data-freshness')).toBe('stale');
    expect(w.find('[data-testid="wr-waiting"]').text()).toBe('3,110');
    expect(w.find('.opacity-70').exists()).toBe(true);
  });

  it('labels fixed admission and shows no per-server table (also for an older queue without a mode)', () => {
    for (const admissionMode of ['fixed', undefined, null] as const) {
      const w = mount(WaitingRoomPanel, { props: { room: makeWaitingRoom({ admissionMode }), freshness: live(), eventId: 'evt-1', configured: enabledRoom } });
      expect(w.find('[data-testid="wr-mode"]').text()).toBe('Fixed admission');
      expect(w.find('[data-testid="wr-servers"]').exists()).toBe(false);
      expect(w.text()).toContain('25 per healthy server');
    }
  });

  it('shows adaptive admission with learned limits and live signals per server', () => {
    const room = makeWaitingRoom({
      admissionMode: 'adaptive',
      capacity: 56,
      healthyServers: 2,
      servers: [
        makeAdaptiveServer(A, { limit: 31, phase: 'steady', decision: 'decrease', reason: 'listen queue 4', decreases: 3,
          signals: { busy: 0.94, activeWorkers: 30, totalWorkers: 32, listenQueue: 4, cpu: 0.91, observedAt: new Date(NOW - 2000).toISOString() } }),
        makeAdaptiveServer(B, { limit: 25, phase: 'slow_start', decision: 'increase', reason: 'headroom', decreases: 0 }),
      ],
    });
    const w = mount(WaitingRoomPanel, { props: { room, freshness: live(), eventId: 'evt-1', configured: adaptiveRoom, nowMs: NOW } });
    expect(w.find('[data-testid="wr-mode"]').text()).toBe('Adaptive admission');
    expect(w.find('[data-testid="wr-active"]').text()).toBe('75 / 56');
    expect(w.text()).toContain('sum of learned server limits');
    expect(w.find('[data-testid="wr-configured"]').text()).toContain('on, adaptive, start 25 per server, max 200, TTL 60s');

    const a = w.find(`[data-testid="wr-server-${A}"]`);
    expect(a.text()).toContain('…-ab12');
    expect(a.find('[data-testid="wr-server-limit"]').text()).toBe('31');
    expect(a.find('[data-testid="wr-server-phase"]').text()).toBe('steady');
    expect(a.find('[data-testid="wr-server-workers"]').text()).toBe('30/32 (94 %)');
    expect(a.find('[data-testid="wr-server-workers"]').classes()).toContain('text-amber-300');
    expect(a.find('[data-testid="wr-server-queue"]').text()).toBe('4');
    expect(a.find('[data-testid="wr-server-cpu"]').text()).toBe('91 %');
    const decision = a.find('[data-testid="wr-server-decision"]');
    expect(decision.attributes('data-decision')).toBe('decrease');
    expect(decision.text()).toContain('decrease · listen queue 4');
    expect(decision.text()).toContain('(3 cuts)');
    expect(decision.find('span').classes()).toContain('text-red-300');
    expect(a.find('[data-testid="wr-server-age"]').text()).toBe('2s');

    const b = w.find(`[data-testid="wr-server-${B}"]`);
    expect(b.find('[data-testid="wr-server-phase"]').text()).toBe('slow start');
    expect(b.find('[data-testid="wr-server-decision"] span').classes()).toContain('text-emerald-300');
    expect(b.find('[data-testid="wr-server-age"]').text()).toBe('1s');
  });

  it('colours a hold neutrally and renders N/A (never 0) when a server has no signals', () => {
    const room = makeWaitingRoom({
      admissionMode: 'adaptive',
      servers: [
        makeAdaptiveServer(A, { decision: 'hold', reason: 'no signal', signals: null, decreases: 0 }),
        makeAdaptiveServer(B, { limit: null, phase: null, decision: null, reason: null, decreases: null,
          signals: { busy: null, activeWorkers: null, totalWorkers: null, listenQueue: null, cpu: null, observedAt: null } }),
      ],
    });
    const w = mount(WaitingRoomPanel, { props: { room, freshness: live(), eventId: 'evt-1', configured: adaptiveRoom, nowMs: NOW } });
    const a = w.find(`[data-testid="wr-server-${A}"]`);
    for (const id of ['wr-server-workers', 'wr-server-queue', 'wr-server-cpu', 'wr-server-age']) {
      expect(a.find(`[data-testid="${id}"]`).text(), id).toBe('N/A');
    }
    expect(a.find('[data-testid="wr-server-limit"]').text()).toBe('31');
    expect(a.find('[data-testid="wr-server-decision"] span').classes()).toContain('text-slate-300');
    expect(a.find('[data-testid="wr-server-decision"]').text()).toContain('hold · no signal');
    const b = w.find(`[data-testid="wr-server-${B}"]`);
    for (const id of ['wr-server-limit', 'wr-server-phase', 'wr-server-workers', 'wr-server-queue', 'wr-server-cpu', 'wr-server-age']) {
      expect(b.find(`[data-testid="${id}"]`).text(), id).toBe('N/A');
    }
    expect(b.find('[data-testid="wr-server-decision"]').attributes('data-decision')).toBe('unknown');
    expect(b.find('[data-testid="wr-server-decision"]').text()).toBe('N/A');
  });

  it('flags old signals and says so when the adaptive room reports no servers', () => {
    const old = makeWaitingRoom({ admissionMode: 'adaptive', servers: [makeAdaptiveServer(A, { signals: { busy: 0.5, activeWorkers: 16, totalWorkers: 32, listenQueue: 0, cpu: 0.4, observedAt: new Date(NOW - 12_000).toISOString() } })] });
    const w = mount(WaitingRoomPanel, { props: { room: old, freshness: live(), eventId: 'evt-1', configured: adaptiveRoom, nowMs: NOW } });
    const age = w.find('[data-testid="wr-server-age"]');
    expect(age.text()).toBe('12s');
    expect(age.classes()).toContain('text-amber-300');
    const empty = mount(WaitingRoomPanel, { props: { room: makeWaitingRoom({ admissionMode: 'adaptive', servers: [] }), freshness: live(), eventId: 'evt-1', configured: adaptiveRoom } });
    expect(empty.find('[data-testid="wr-servers-empty"]').exists()).toBe(true);
  });

  it('lists the waiting room in the telemetry health panel', () => {
    const snap = makeSnapshot();
    const w = mount(FreshnessPanel, { props: { freshness: snap.freshness, nowMs: Date.parse(SERVER_TIME) } });
    expect(w.find('[data-testid="freshness-waitingRoom"]').text()).toContain('missing');
    expect(w.find('[data-testid="freshness-waitingRoom"]').text()).toContain('not deployed');
  });
});

describe('waiting room chart', () => {
  it('appears only when the experiment uses the room or there are observations', () => {
    const snap = makeSnapshot();
    const base = { traffic: [], scaling: [], cpu: [], inventory: [], events: [], cpuTarget: 0.6, freshness: snap.freshness };
    expect(mount(ChartGrid, { props: base }).find('[data-testid="chart-Waiting room"]').exists()).toBe(false);

    const shown = mount(ChartGrid, { props: { ...base, showWaitingRoom: true } });
    expect(shown.findAll('[data-testid="chart-no-data"]')).toHaveLength(7);
    expect(shown.find('[data-testid="chart-Waiting room"] [data-testid="chart-no-data"]').exists()).toBe(true);

    const withData = mount(ChartGrid, { props: { ...base, freshness: { ...snap.freshness, waitingRoom: live() }, waitingRoom: [makeWaitingRoomPoint(0), makeWaitingRoomPoint(1)] } });
    expect(withData.find('[data-testid="chart-Waiting room"]').exists()).toBe(true);
    expect(withData.find('[data-testid="chart-Waiting room"] [data-testid="chart-no-data"]').exists()).toBe(false);
  });

  it('shows the adaptive limits chart only for adaptive experiments or recorded limits', () => {
    const snap = makeSnapshot();
    const base = { traffic: [], scaling: [], cpu: [], inventory: [], events: [], cpuTarget: 0.6, freshness: snap.freshness, showWaitingRoom: true };
    const fixedPoints = [makeWaitingRoomPoint(0), makeWaitingRoomPoint(1)];
    expect(mount(ChartGrid, { props: { ...base, waitingRoom: fixedPoints } }).find('[data-testid="chart-Adaptive limits"]').exists()).toBe(false);

    const empty = mount(ChartGrid, { props: { ...base, showAdaptiveLimits: true } });
    expect(empty.find('[data-testid="chart-Adaptive limits"] [data-testid="chart-no-data"]').exists()).toBe(true);

    const points = [makeWaitingRoomPoint(0, { limits: { [A]: 25 }, limitAvg: 25 }), makeWaitingRoomPoint(1, { limits: { [A]: 31 }, limitAvg: 31 })];
    const withData = mount(ChartGrid, { props: { ...base, waitingRoom: points } });
    expect(withData.find('[data-testid="chart-Adaptive limits"]').exists()).toBe(true);
    expect(withData.find('[data-testid="chart-Adaptive limits"] [data-testid="chart-no-data"]').exists()).toBe(false);
  });

  it('plots one step line per server limit with gaps while a server is absent, plus capacity on the right axis', () => {
    const option = adaptiveLimitsOption([
      makeWaitingRoomPoint(0, { capacity: 25, limits: { [A]: 25 } }),
      makeWaitingRoomPoint(1, { capacity: 56, limits: { [A]: 31, [B]: 25 } }),
      makeWaitingRoomPoint(2, { capacity: 40 }),
    ]);
    const series = option.series as Array<{ name: string; data: Array<[number, number | null]>; yAxisIndex?: number; step?: string }>;
    expect(series.map((x) => x.name)).toEqual(['Limit …-ab12', 'Limit …-cd34', 'Capacity (sum of limits)']);
    expect(series[0]!.data.map((d) => d[1])).toEqual([25, 31, null]);
    expect(series[1]!.data.map((d) => d[1])).toEqual([null, 25, null]);
    expect(series[2]!.data.map((d) => d[1])).toEqual([25, 56, null]);
    expect(series.every((x) => x.step === 'end')).toBe(true);
    expect(series[2]!.yAxisIndex).toBe(1);
  });

  it('plots waiting, active and capacity on the left axis and admissions on the right', () => {
    const option = waitingRoomOption([makeWaitingRoomPoint(0), makeWaitingRoomPoint(1, { waiting: 800, capacity: 50 })]);
    const series = option.series as Array<{ name: string; data: Array<[number, number]>; yAxisIndex?: number }>;
    expect(series.map((s) => s.name)).toEqual(['Waiting', 'Active (in checkout)', 'Capacity (healthy servers × per-server)', 'Admitted/s (10 s average)']);
    expect(series[0]!.data[1]![1]).toBe(800);
    expect(series[2]!.data[1]![1]).toBe(50);
    expect(series[3]!.yAxisIndex).toBe(1);
  });
});

describe('waiting room configuration', () => {
  async function setup(deployed = true) {
    const { store, fetch } = makeStore({
      'GET /api/state': { body: makeSnapshot({ experiment: null }) },
      'POST /api/experiments': { status: 201, body: makeExperiment({ state: 'preparing', trafficStartedAt: null }) },
    });
    const meta = makeMeta();
    meta.waitingRoom = { deployed };
    store.meta.value = meta;
    store.applySnapshot(makeSnapshot({ experiment: null }));
    const wrapper = await mountWithStore(DashboardView, store);
    await flushPromises();
    return { store, fetch, wrapper };
  }
  const posted = (fetch: Awaited<ReturnType<typeof setup>>['fetch']) =>
    fetch.mock.calls.filter((c) => String(c[0]) === '/api/experiments').map((c) => JSON.parse(String((c[1] as RequestInit).body)) as ExperimentConfig);

  it('preset F turns the waiting room on and sends its settings', async () => {
    const { wrapper, fetch } = await setup();
    const toggle = () => wrapper.find('[data-testid="waiting-room-enabled"]').element as HTMLInputElement;
    expect(toggle().checked).toBe(false);
    expect(wrapper.find('[data-testid="waiting-room-per-server"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="preset-F-waiting-room"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="preset-C-waiting-room"]').exists()).toBe(false);

    await wrapper.find('[data-testid="preset-F"]').trigger('click');
    expect(toggle().checked).toBe(true);
    expect((wrapper.find('[data-testid="waiting-room-per-server"]').element as HTMLInputElement).value).toBe('25');
    expect((wrapper.find('[data-testid="waiting-room-ttl"]').element as HTMLInputElement).value).toBe('60');
    expect(wrapper.find('[data-testid="waiting-room-section"]').text()).toContain('first-come, first-served');

    await wrapper.find('[data-testid="waiting-room-per-server"]').setValue('40');
    await wrapper.find('[data-testid="btn-prepare"]').trigger('click');
    await flushPromises();
    expect(posted(fetch)[0]!.waitingRoom).toEqual({ enabled: true, activeUsersPerServer: 40, admissionTtlSeconds: 60, mode: 'adaptive', maxUsersPerServer: 200 });
    wrapper.unmount();
  });

  it('switches between fixed and adaptive admission with the matching fields and help', async () => {
    const { wrapper, fetch } = await setup();
    await wrapper.find('[data-testid="waiting-room-enabled"]').setValue(true);
    const mode = () => wrapper.find('[data-testid="waiting-room-mode"]').element as HTMLSelectElement;
    const section = () => wrapper.find('[data-testid="waiting-room-section"]');
    expect(mode().value).toBe('fixed');
    expect(wrapper.find('[data-testid="waiting-room-max-per-server"]').exists()).toBe(false);
    expect(section().text()).toContain('Active users per server');

    await wrapper.find('[data-testid="waiting-room-mode"]').setValue('adaptive');
    expect(mode().value).toBe('adaptive');
    expect(section().text()).toContain(
      "Adaptive: each server's limit is learned from PHP workers busy, request queue and VM CPU; the number below is the starting limit",
    );
    expect(section().text()).toContain('Starting limit per server');
    const max = () => wrapper.find('[data-testid="waiting-room-max-per-server"]');
    expect((max().element as HTMLInputElement).value).toBe('200');

    // The maximum must not be below the starting limit.
    await max().setValue('20');
    await flushPromises();
    expect(section().text()).toContain('Maximum per server must be at least the starting limit');
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeDefined();
    await max().setValue('120');
    await flushPromises();
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeUndefined();
    await wrapper.find('[data-testid="btn-prepare"]').trigger('click');
    await flushPromises();
    expect(posted(fetch)[0]!.waitingRoom).toEqual({ enabled: true, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'adaptive', maxUsersPerServer: 120 });
    wrapper.unmount();
  });

  it('hides the maximum in fixed mode and never leaves an invalid hidden value behind', async () => {
    const { wrapper, fetch } = await setup();
    await wrapper.find('[data-testid="preset-F"]').trigger('click');
    expect((wrapper.find('[data-testid="waiting-room-mode"]').element as HTMLSelectElement).value).toBe('adaptive');
    await wrapper.find('[data-testid="waiting-room-max-per-server"]').setValue('');
    await wrapper.find('[data-testid="waiting-room-mode"]').setValue('fixed');
    await flushPromises();
    expect(wrapper.find('[data-testid="waiting-room-max-per-server"]').exists()).toBe(false);
    await wrapper.find('[data-testid="btn-prepare"]').trigger('click');
    await flushPromises();
    expect(posted(fetch)[0]!.waitingRoom).toMatchObject({ mode: 'fixed', maxUsersPerServer: 200 });
    wrapper.unmount();
  });

  it('blocks Prepare when the waiting room is on but not deployed', async () => {
    const { wrapper } = await setup(false);
    expect(wrapper.find('[data-testid="waiting-room-not-deployed"]').exists()).toBe(true);
    await wrapper.find('[data-testid="preset-F"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-testid="waiting-room-error"]').text()).toContain('not deployed');
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeDefined();
    await wrapper.find('[data-testid="waiting-room-enabled"]').setValue(false);
    await flushPromises();
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeUndefined();
    wrapper.unmount();
  });

  it('validates the ranges and fills in defaults for configs without waiting-room settings', () => {
    const meta = makeMeta();
    const bad = validateConfig(makeConfig({ waitingRoom: { enabled: true, activeUsersPerServer: 0, admissionTtlSeconds: 3601 } }), meta);
    expect(bad.errors['waitingRoom.activeUsersPerServer']).toBe('Active users per server must be at least 1');
    expect(bad.errors['waitingRoom.admissionTtlSeconds']).toBe('Admission TTL must be at most 3600');
    const { waitingRoom: _omit, ...old } = makeConfig();
    expect(cloneConfig(old as ExperimentConfig).waitingRoom).toEqual({ enabled: false, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200 });
    // Stored before adaptive admission: mode and maximum are filled in.
    const beforeAdaptive = makeConfig({ waitingRoom: { enabled: true, activeUsersPerServer: 30, admissionTtlSeconds: 90 } });
    expect(cloneConfig(beforeAdaptive).waitingRoom).toEqual({ enabled: true, activeUsersPerServer: 30, admissionTtlSeconds: 90, mode: 'fixed', maxUsersPerServer: 200 });
    const adaptiveBad = validateConfig(makeConfig({ waitingRoom: { ...adaptiveRoom, activeUsersPerServer: 50, maxUsersPerServer: 40 } }), meta);
    expect(adaptiveBad.errors['waitingRoom.maxUsersPerServer']).toBe('Maximum per server must be at least the starting limit');
    expect(validateConfig(makeConfig({ waitingRoom: { ...adaptiveRoom, maxUsersPerServer: 1001 } }), meta).errors['waitingRoom.maxUsersPerServer'])
      .toBe('Maximum per server must be at most 1000');
    expect(validateConfig(makeConfig({ waitingRoom: { ...enabledRoom, mode: 'fixed', activeUsersPerServer: 50, maxUsersPerServer: 40 } }), meta).valid).toBe(true);
  });
});

describe('waiting room live data', () => {
  it('appends waiting-room series from ticks and shows the panel on the dashboard', async () => {
    const experiment = makeExperiment({ config: makeConfig({ waitingRoom: enabledRoom }), eventId: 'evt-1' });
    const snapshot = makeSnapshot({ experiment, waitingRoom: makeWaitingRoom() });
    snapshot.freshness.waitingRoom = live();
    const { store } = makeStore({
      'GET /api/state': { body: snapshot },
      'GET /api/experiments/exp_20261009_1530_ab12/series': { body: { ...emptySeries(), waitingRoom: [makeWaitingRoomPoint(0)] } },
    });
    store.meta.value = makeMeta();
    store.applyTick({ snapshot, append: { traffic: [], scaling: [], cpu: [], inventory: [], waitingRoom: [makeWaitingRoomPoint(1), makeWaitingRoomPoint(2)] } });
    await flushPromises();
    expect(store.series.waitingRoom.map((p) => p.t)).toEqual([makeWaitingRoomPoint(0).t, makeWaitingRoomPoint(1).t, makeWaitingRoomPoint(2).t]);

    const wrapper = await mountWithStore(DashboardView, store);
    expect(wrapper.find('[data-testid="waiting-room-panel"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="wr-waiting"]').text()).toBe('3,110');
    expect(wrapper.find('[data-testid="chart-Waiting room"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('hides the panel when the room is not deployed, not observed and not used', async () => {
    const { store } = makeStore({ 'GET /api/state': { body: makeSnapshot({ experiment: null }) } });
    const meta = makeMeta();
    meta.waitingRoom = { deployed: false };
    store.meta.value = meta;
    store.applySnapshot(makeSnapshot({ experiment: null }));
    const wrapper = await mountWithStore(DashboardView, store);
    expect(wrapper.find('[data-testid="waiting-room-panel"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

describe('waiting room report', () => {
  it('shows the measured waiting-room results', async () => {
    const config = makeConfig({ waitingRoom: enabledRoom });
    const report = {
      experimentId: 'exp_1', name: 'Scenario F', generatedAt: SERVER_TIME, state: 'completed', config,
      timestamps: { createdAt: SERVER_TIME, readyAt: null, trafficStartedAt: SERVER_TIME, trafficStoppedAt: null, completedAt: null },
      measured: {
        arrivalsRequested: 5000, arrivalsScheduled: 5000, arrivalsStarted: 5000, arrivalsDropped: 0, achievedVsRequestedPct: 100,
        httpRequests: 9000, peakRps: 120, latencyMs: { p50: 30, p95: 90, p99: 200, max: 900 }, httpErrorPct: 0, businessRejectionPct: 0,
        peakCpu: null, peakAverageCpu: null, initialServerCount: 1, maxObservedServerCount: 3, finalServerCount: 3,
        ticketsSold: 2000, ticketsRemaining: 0, expiredReservations: 10, invariantViolations: 0,
        reservations: { attempted: 2100, succeeded: 2050, rejected: 50 }, purchases: { completed: 2000, declined: 0, failed: 0, expiredDuringCheckout: 0 },
        requestsPerInstance: {},
        waitingRoom: { enabled: true, joined: 5000, admitted: 2300, soldOutInQueue: 2700, peakWaiting: 3110,
          waitMs: { p50: 8000, p95: 41000, max: 52000 }, peakCapacity: 75, peakHealthyServers: 3 },
      },
      configured: { cpuTarget: 0.3, minReplicas: 1, maxReplicas: 3, initialServers: 1, pattern: 'constant', durationSeconds: 150 },
      estimated: { infrastructureCostUsd: 0.1, note: '' },
      scalingTimeline: [], generatorCapacityIssues: [], failures: [], notes: [],
    } satisfies Report;
    const { store } = makeStore({
      'GET /api/experiments/exp_1/report': { body: report },
      'GET /api/experiments/exp_1/series': { body: { ...emptySeries(), waitingRoom: [makeWaitingRoomPoint(0)] } },
    });
    const wrapper = await mountWithStore(ReportView, store, { props: { id: 'exp_1' } });
    await flushPromises();
    const group = wrapper.find('[data-testid="report-waiting-room"]');
    expect(group.text()).toContain('Told sold out in queue');
    expect(group.text()).toContain('2,700');
    expect(group.text()).toContain('41.00 s');
    expect(wrapper.find('[data-testid="chart-Waiting room"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="chart-Adaptive limits"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('shows learned limits for an adaptive experiment', async () => {
    const config = makeConfig({ waitingRoom: adaptiveRoom });
    const report = {
      experimentId: 'exp_2', name: 'Scenario F', generatedAt: SERVER_TIME, state: 'completed', config,
      timestamps: { createdAt: SERVER_TIME, readyAt: null, trafficStartedAt: SERVER_TIME, trafficStoppedAt: null, completedAt: null },
      measured: {
        arrivalsRequested: 5000, arrivalsScheduled: 5000, arrivalsStarted: 5000, arrivalsDropped: 0, achievedVsRequestedPct: 100,
        httpRequests: 9000, peakRps: 120, latencyMs: { p50: 30, p95: 90, p99: 200, max: 900 }, httpErrorPct: 0, businessRejectionPct: 0,
        peakCpu: null, peakAverageCpu: null, initialServerCount: 1, maxObservedServerCount: 2, finalServerCount: 2,
        ticketsSold: 2000, ticketsRemaining: 0, expiredReservations: 10, invariantViolations: 0,
        reservations: { attempted: 2100, succeeded: 2050, rejected: 50 }, purchases: { completed: 2000, declined: 0, failed: 0, expiredDuringCheckout: 0 },
        requestsPerInstance: {},
        waitingRoom: { enabled: true, joined: 5000, admitted: 2300, soldOutInQueue: 2700, peakWaiting: 3110,
          waitMs: { p50: 8000, p95: 41000, max: 52000 }, peakCapacity: 96, peakHealthyServers: 2,
          adaptive: { perServer: [{ name: A, finalLimit: 41, peakLimit: 60, decreases: 3 }, { name: B, finalLimit: 36, peakLimit: 39, decreases: 1 }],
            limitAvgAtPeakCapacity: 48, decreasesTotal: 4 } },
      },
      configured: { cpuTarget: 0.3, minReplicas: 1, maxReplicas: 3, initialServers: 1, pattern: 'constant', durationSeconds: 150 },
      estimated: { infrastructureCostUsd: 0.1, note: '' },
      scalingTimeline: [], generatorCapacityIssues: [], failures: [], notes: [],
    } satisfies Report;
    const { store } = makeStore({
      'GET /api/experiments/exp_2/report': { body: report },
      'GET /api/experiments/exp_2/series': { body: { ...emptySeries(), waitingRoom: [] } },
    });
    const wrapper = await mountWithStore(ReportView, store, { props: { id: 'exp_2' } });
    await flushPromises();
    const group = wrapper.find('[data-testid="report-waiting-room"]');
    expect(group.text()).toContain('Admission mode');
    expect(group.text()).toContain('Avg limit at peak capacity');
    expect(group.text()).toContain('48.0');
    expect(group.text()).toContain('41 (peak 60)');
    expect(group.text()).toContain('3 cuts');
    expect(group.text()).toContain('sum of learned server limits');
    // Adaptive experiment: the chart is shown (empty) even without recorded limits.
    expect(wrapper.find('[data-testid="chart-Adaptive limits"] [data-testid="chart-no-data"]').exists()).toBe(true);
    wrapper.unmount();
  });
});
