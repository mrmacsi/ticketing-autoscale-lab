import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import InstanceCard from '@/components/live/InstanceCard.vue';
import FreshnessPanel from '@/components/live/FreshnessPanel.vue';
import ChartGrid from '@/components/charts/ChartGrid.vue';
import TotalsStrip from '@/components/live/TotalsStrip.vue';
import ServerSummary from '@/components/live/ServerSummary.vue';
import { live, makeInstance, makeSnapshot, makeTrafficPoint, SERVER_TIME } from './fixtures';
import type { Freshness } from '@/types/contracts';

const NOW = Date.parse(SERVER_TIME);
const stale: Freshness = { status: 'stale', lastAt: '2026-10-09T15:35:00.000Z', ageSeconds: 300 };
const missing: Freshness = { status: 'missing', lastAt: null, ageSeconds: null };

function card(instance = makeInstance('app-1'), trafficFreshness: Freshness | null = live(), cpuFreshness: Freshness | null = live()) {
  return mount(InstanceCard, { props: { instance, totalRps: 60, trafficFreshness, cpuFreshness, nowMs: NOW } });
}

describe('telemetry rendering', () => {
  it('shows the Cloud Monitoring sample time and its age', () => {
    const w = card();
    expect(w.find('[data-testid="cpu-value"]').text()).toBe('42.0 %');
    const sample = w.find('[data-testid="cpu-sample"]').text();
    expect(sample).toContain('15:37:50');
    expect(sample).toContain('sampled 2m10s ago — Cloud Monitoring delay');
    expect(w.find('[data-testid="guest-cpu"]').text()).toContain('guest /proc/stat, real-time, not used by autoscaler');
  });

  it('renders missing values as N/A, never 0', () => {
    const inst = makeInstance('app-1', {
      cpu: { value: null, sampleTime: null, source: 'cloud_monitoring' },
      guestCpu: null,
      traffic: { rps: 0, avgLatencyMs: null, errorRate: null, windowSeconds: 10 },
    });
    const w = card(inst);
    expect(w.find('[data-testid="cpu-value"]').text()).toBe('N/A');
    expect(w.find('[data-testid="cpu-sample"]').text()).toContain('no Cloud Monitoring sample yet');
    expect(w.find('[data-testid="guest-cpu"]').text()).toContain('N/A');
    expect(w.find('[data-testid="latency"]').text()).toBe('N/A');
    expect(w.find('[data-testid="error-rate"]').text()).toBe('N/A');
    for (const id of ['cpu-value', 'latency', 'error-rate']) expect(w.find(`[data-testid="${id}"]`).text()).not.toMatch(/^0/);
  });

  it('hides per-instance load and flags stale/missing sources instead of showing numbers', () => {
    const noCpu = { cpu: { value: null, sampleTime: null, source: 'cloud_monitoring' as const } };
    const w = card(makeInstance('app-1', { ...noCpu, createdAt: '2020-01-01T00:00:00.000Z' }), stale, missing);
    expect(w.find('[data-testid="rps"]').text()).toBe('N/A');
    expect(w.find('[data-testid="share"]').text()).toBe('—');
    expect(w.find('[data-testid="traffic-stale"]').text()).toContain('stale');
    expect(w.find('[data-testid="cpu-stale"]').text()).toContain('missing');
  });

  it('judges CPU telemetry per VM: a brand-new VM without a sample is not flagged', () => {
    const noCpu = { cpu: { value: null, sampleTime: null, source: 'cloud_monitoring' as const } };
    const w = card(makeInstance('app-new', { ...noCpu, createdAt: new Date().toISOString() }), stale, missing);
    expect(w.find('[data-testid="cpu-stale"]').exists()).toBe(false);
    expect(w.find('[data-testid="cpu-sample"]').text()).toContain('no Cloud Monitoring sample yet');
  });

  it('computes request share from per-instance rps', () => {
    const w = card();
    expect(w.find('[data-testid="rps"]').text()).toBe('30.0');
    expect(w.find('[data-testid="share"]').text()).toBe('50 %');
  });

  it('freshness panel shows each source status with last time and age', () => {
    const snap = makeSnapshot();
    snap.freshness.inventory = stale;
    snap.freshness.lb = missing;
    const w = mount(FreshnessPanel, { props: { freshness: snap.freshness, nowMs: NOW } });
    expect(w.find('[data-testid="freshness-traffic"] [data-freshness]').attributes('data-freshness')).toBe('live');
    expect(w.find('[data-testid="freshness-cpu"]').text()).toContain('delayed');
    expect(w.find('[data-testid="freshness-cpu"]').text()).toContain('age 2m10s');
    expect(w.find('[data-testid="freshness-inventory"]').text()).toContain('stale');
    expect(w.find('[data-testid="freshness-inventory"]').classes()).toContain('bg-amber-500/10');
    expect(w.find('[data-testid="freshness-lb"]').text()).toContain('missing');
    expect(w.find('[data-testid="freshness-lb"]').text()).toContain('last N/A');
    expect(w.find('[data-testid="freshness-lb"]').classes()).toContain('bg-red-500/10');
  });

  it('charts show a no-data overlay without points and a stale banner when the source is stale', () => {
    const snap = makeSnapshot();
    const empty = mount(ChartGrid, { props: { traffic: [], scaling: [], cpu: [], inventory: [], events: [], cpuTarget: 0.6, freshness: snap.freshness } });
    expect(empty.findAll('[data-testid="chart-no-data"]')).toHaveLength(6);

    snap.freshness.traffic = stale;
    const withData = mount(ChartGrid, {
      props: { traffic: [makeTrafficPoint(0)], scaling: [], cpu: [], inventory: [], events: [], cpuTarget: 0.6, freshness: snap.freshness },
    });
    expect(withData.find('[data-testid="chart-Incoming traffic"] [data-testid="chart-stale"]').exists()).toBe(true);
    expect(withData.find('[data-testid="cpu-latest-sample"]').text()).toBe('N/A');
  });

  it('live strip shows N/A when traffic telemetry is unusable or inventory is missing', () => {
    const w = mount(TotalsStrip, { props: { points: [], live: makeSnapshot().traffic, inventory: null, trafficUsable: false } });
    expect(w.find('[data-testid="live-rps"]').text()).toBe('N/A');
    expect(w.find('[data-testid="total-requests"]').text()).toBe('N/A');
    expect(w.find('[data-testid="tickets-sold"]').text()).toBe('N/A');
  });

  it('server summary renders N/A for a missing MIG', () => {
    const w = mount(ServerSummary, { props: { mig: null, instanceCards: 0, freshness: missing, approvedMaxFallback: 8 } });
    expect(w.find('[data-testid="stat-actual"]').text()).toBe('N/A');
    expect(w.find('[data-testid="stat-running"]').text()).toBe('N/A');
    expect(w.find('[data-testid="stat-approved"]').text()).toBe('8');
    expect(w.text()).toContain('MIG telemetry unavailable');
  });
});
