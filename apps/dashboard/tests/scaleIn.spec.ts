import { describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import HistoryView from '@/views/HistoryView.vue';
import ReportView from '@/views/ReportView.vue';
import TicketSection from '@/components/config/TicketSection.vue';
import { estimateCost } from '@/lib/cost';
import { scaleInSummary } from '@/lib/scaleIn';
import { validateConfig } from '@/lib/validation';
import { cloneConfig, createExperimentDraft, DRAFT } from '@/store/draft';
import { LAB_STORE } from '@/store/lab';
import { emptySeries, makeConfig, makeMeta, SERVER_TIME } from './fixtures';
import { makeStore, mountWithStore } from './helpers';
import type { ExperimentConfig, ExperimentSummary, Report, ScaleInReport } from '@/types/contracts';

const done: ScaleInReport = {
  status: 'complete', completedAt: '2026-10-09T15:41:04.000Z', secondsAfterTrafficStopped: 64, serversRemoved: 2,
  observedUntil: '2026-10-09T15:41:05.000Z', targetServers: 1,
};

function makeReport(scaleIn: ScaleInReport | null | undefined, completedAt: string | null = SERVER_TIME): Report {
  return {
    experimentId: 'exp_s', name: 'Scenario F', generatedAt: SERVER_TIME, state: 'completed', config: makeConfig(),
    timestamps: { createdAt: SERVER_TIME, readyAt: null, trafficStartedAt: SERVER_TIME, trafficStoppedAt: SERVER_TIME, completedAt },
    measured: {
      arrivalsRequested: 10, arrivalsScheduled: 10, arrivalsStarted: 10, arrivalsDropped: 0, achievedVsRequestedPct: 100,
      httpRequests: 40, peakRps: 4, latencyMs: { p50: 30, p95: 90, p99: 200, max: 900 }, httpErrorPct: 0, businessRejectionPct: 0,
      peakCpu: null, peakAverageCpu: null, initialServerCount: 1, maxObservedServerCount: 3, finalServerCount: 3,
      ticketsSold: 5, ticketsRemaining: 0, expiredReservations: 0, invariantViolations: 0,
      reservations: { attempted: 5, succeeded: 5, rejected: 0 }, purchases: { completed: 5, declined: 0, failed: 0, expiredDuringCheckout: 0 },
      requestsPerInstance: {}, waitingRoom: null, ...(scaleIn === undefined ? {} : { scaleIn }),
    },
    configured: { cpuTarget: 0.3, minReplicas: 1, maxReplicas: 3, initialServers: 1, pattern: 'constant', durationSeconds: 300 },
    estimated: { infrastructureCostUsd: 0.1, note: '' },
    scalingTimeline: [], generatorCapacityIssues: [], failures: [], notes: [],
  };
}

describe('scale-in summary', () => {
  const now = Date.parse(SERVER_TIME);
  it('describes every outcome', () => {
    expect(scaleInSummary(done, 1)).toEqual({ text: 'Back to 1 server 64 s after traffic stopped', tone: 'good' });
    expect(scaleInSummary({ ...done, secondsAfterTrafficStopped: 245, targetServers: 2 }, 1).text).toBe('Back to 2 servers 4 min 5 s after traffic stopped');
    expect(scaleInSummary({ ...done, status: 'timed_out', completedAt: null, secondsAfterTrafficStopped: null }, 1))
      .toEqual({ text: 'Still above 1 server 15 min after completion', tone: 'bad' });
    expect(scaleInSummary({ ...done, status: 'not_needed', serversRemoved: 0 }, 1).text).toBe('Not needed (never above 1 server)');
    expect(scaleInSummary({ ...done, status: 'superseded', note: 'new experiment "B" created' }, 1).text)
      .toBe('Observation stopped early (new experiment "B" created)');
    expect(scaleInSummary({ ...done, status: 'unavailable', note: 'Compute API not available (local mode)' }, 1).text)
      .toBe('Not observed (Compute API not available (local mode))');
    // targetServers absent: the experiment's minimum is used.
    const { targetServers: _t, ...noTarget } = done;
    expect(scaleInSummary(noTarget, 3).text).toBe('Back to 3 servers 64 s after traffic stopped');
  });

  it('is still being watched shortly after completion, and not recorded otherwise', () => {
    const recent = new Date(now - 60_000).toISOString();
    expect(scaleInSummary(null, 1, { state: 'completed', completedAt: recent, nowMs: now }).text).toMatch(/^Watching scale-in \(back to 1 server/);
    const old = new Date(now - 16 * 60_000).toISOString();
    expect(scaleInSummary(undefined, 1, { state: 'completed', completedAt: old, nowMs: now }).text).toBe('Not recorded');
    expect(scaleInSummary(null, 1, { state: 'failed', completedAt: recent, nowMs: now }).text).toBe('Not recorded');
  });
});

describe('scale-in in the report and history', () => {
  it('shows the measured scale-in on the report', async () => {
    const { store } = makeStore({
      'GET /api/experiments/exp_s/report': { body: makeReport(done) },
      'GET /api/experiments/exp_s/series': { body: emptySeries() },
    });
    const wrapper = await mountWithStore(ReportView, store, { props: { id: 'exp_s' } });
    await flushPromises();
    const section = wrapper.find('[data-testid="report-scale-in"]');
    expect(section.attributes('data-status')).toBe('complete');
    expect(section.find('[data-testid="scale-in-summary"]').text()).toBe('Back to 1 server 64 s after traffic stopped');
    expect(section.text()).toContain('Servers removed');
    expect(section.text()).toContain('64 s');
    wrapper.unmount();
  });

  it('says when a report predates scale-in tracking', async () => {
    const { store } = makeStore({
      'GET /api/experiments/exp_s/report': { body: makeReport(undefined, '2026-10-01T10:00:00.000Z') },
      'GET /api/experiments/exp_s/series': { body: emptySeries() },
    });
    const wrapper = await mountWithStore(ReportView, store, { props: { id: 'exp_s' } });
    await flushPromises();
    expect(wrapper.find('[data-testid="scale-in-summary"]').text()).toBe('Not recorded');
    expect(wrapper.find('[data-testid="report-scale-in"]').attributes('data-status')).toBe('pending');
    wrapper.unmount();
  });

  it('lists the scale-in per completed experiment', async () => {
    const rows: ExperimentSummary[] = [
      { id: 'exp_1', name: 'F', preset: 'F', state: 'completed', createdAt: SERVER_TIME, completedAt: SERVER_TIME, minServers: 1, scaleIn: done },
      { id: 'exp_2', name: 'A', preset: 'A', state: 'completed', createdAt: SERVER_TIME, completedAt: SERVER_TIME, minServers: 1,
        scaleIn: { ...done, status: 'not_needed', completedAt: null, secondsAfterTrafficStopped: null, serversRemoved: 0 } },
      { id: 'exp_3', name: 'B', preset: 'B', state: 'failed', createdAt: SERVER_TIME, minServers: 1, scaleIn: null },
    ];
    const { store } = makeStore({ 'GET /api/experiments': { body: rows } });
    const wrapper = await mountWithStore(HistoryView, store);
    await flushPromises();
    const cells = wrapper.findAll('[data-testid="history-scale-in"]').map((c) => c.text());
    expect(cells).toEqual(['Back to 1 server 64 s after traffic stopped', 'Not needed (never above 1 server)', '—']);
    wrapper.unmount();
  });
});

describe('near-sell-out holds', () => {
  it('validates 0 or 5–3600 s, not longer than the reservation TTL, and a 0–1,000,000 threshold', () => {
    const meta = makeMeta();
    const withTickets = (o: Partial<ExperimentConfig['tickets']>) => {
      const c = makeConfig();
      c.tickets = { ...c.tickets, ...o };
      return validateConfig(c, meta).errors;
    };
    expect(withTickets({ nearSelloutTtlSeconds: 10, nearSelloutThreshold: 200 })).toEqual({});
    expect(withTickets({ nearSelloutTtlSeconds: 4 })['tickets.nearSelloutTtlSeconds']).toMatch(/0 \(off\) or 5–3600/);
    expect(withTickets({ nearSelloutTtlSeconds: 121 })['tickets.nearSelloutTtlSeconds']).toMatch(/not be longer than the reservation TTL/);
    expect(withTickets({ nearSelloutThreshold: 1_000_001 })['tickets.nearSelloutThreshold']).toMatch(/at most 1000000/);
  });

  it('defaults to off for configs from an older control API', () => {
    const c = makeConfig();
    const { nearSelloutTtlSeconds: _a, nearSelloutThreshold: _b, ...old } = c.tickets;
    expect(cloneConfig({ ...c, tickets: old as ExperimentConfig['tickets'] }).tickets).toMatchObject({ nearSelloutTtlSeconds: 0, nearSelloutThreshold: 0 });
  });

  it('explains the fields in the ticket section', async () => {
    const { store } = makeStore();
    store.meta.value = makeMeta();
    const draft = createExperimentDraft(store);
    draft.config.value!.tickets.nearSelloutTtlSeconds = 10;
    draft.config.value!.tickets.nearSelloutThreshold = 200;
    const w = mount(TicketSection, { props: { maxInventory: 1000 }, global: { provide: { [LAB_STORE as symbol]: store, [DRAFT as symbol]: draft } } });
    await nextTick();
    const section = w.find('[data-testid="near-sellout"]');
    expect(section.text()).toContain('Near-sell-out hold');
    expect(section.text()).toContain('Shorter holds for the last N tickets');
    expect(section.text()).toContain('so abandoned checkouts near the end return them faster');
    expect(section.text()).toContain('last 200 tickets held 10 s');
    w.unmount();
  });
});

describe('header cost estimate', () => {
  it('scales only the application VMs with the live count and always includes the waiting-room VMs and Redis', () => {
    const model = makeMeta().costModel;
    const one = estimateCost(model, 1, true);
    const three = estimateCost(model, 3, true);
    expect(one.perInstancePerHour).toBeCloseTo(0.1152, 6);
    expect(three.currentPerHour! - one.currentPerHour!).toBeCloseTo(2 * 0.1152, 6);
    // App VM + Cloud SQL + generator + 2 queue VMs + Redis.
    expect(one.currentPerHour).toBeCloseTo(0.1152 + 0.084 + 0.083 + 0.039 + 0.058, 6);
  });
});
