import { describe, expect, it } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import DashboardView from '@/views/DashboardView.vue';
import { trafficOption, trafficTotals, inventoryOption } from '@/charts/options';
import { mergeBounded, byT } from '@/lib/series';
import { emptySeries, makeExperiment, makeMeta, makeSnapshot, makeTrafficPoint } from './fixtures';
import { makeStore, mountWithStore } from './helpers';
import type { InventoryPoint, ScalingPoint, TickEvent } from '@/types/contracts';

const SERIES = 'GET /api/experiments/exp_20261009_1530_ab12/series';

const tick = (points: ReturnType<typeof makeTrafficPoint>[], inv: InventoryPoint[] = [], scaling: ScalingPoint[] = []): TickEvent => ({
  snapshot: makeSnapshot(),
  append: { traffic: points, scaling, cpu: [], inventory: inv },
});

describe('request and ticket counts', () => {
  it('sums per-second traffic buckets exactly', () => {
    const pts = [
      makeTrafficPoint(0, { rps: 120, ok: 110, errors: 2, business: 8, achieved: 38, targetRate: 37.5, dropped: 0 }),
      makeTrafficPoint(1, { rps: 130, ok: 121, errors: 1, business: 8, achieved: 40, targetRate: 40, dropped: 2 }),
    ];
    const t = trafficTotals(pts);
    expect(t).toMatchObject({ httpRequests: 250, successful: 231, errors: 3, business: 16, arrivals: 78, requested: 77.5, dropped: 2 });
  });

  it('renders totals from bootstrapped + streamed series and ticket counts from the snapshot', async () => {
    const { store } = makeStore({
      [SERIES]: { body: { ...emptySeries(), traffic: [makeTrafficPoint(0, { rps: 100, ok: 90, errors: 3, business: 7 })] } },
    });
    store.meta.value = makeMeta();
    const wrapper = await mountWithStore(DashboardView, store);

    store.applyTick(tick([]));
    await flushPromises(); // bootstrap /series
    store.applyTick(tick([makeTrafficPoint(1, { rps: 50, ok: 45, errors: 1, business: 4 })]));
    store.applyTick(tick([makeTrafficPoint(2, { rps: 25, ok: 25, errors: 0, business: 0 })]));
    // duplicate delivery of an already-held bucket must not double count
    store.applyTick(tick([makeTrafficPoint(2, { rps: 25, ok: 25, errors: 0, business: 0 })]));
    await flushPromises();

    expect(store.series.traffic).toHaveLength(3);
    expect(wrapper.find('[data-testid="total-requests"]').text()).toBe('175');
    expect(wrapper.find('[data-testid="total-ok"]').text()).toBe('160');
    expect(wrapper.find('[data-testid="total-errors"]').text()).toBe('4');
    expect(wrapper.find('[data-testid="total-business"]').text()).toBe('11');
    expect(wrapper.find('[data-testid="tickets-sold"]').text()).toBe('1,200');
    expect(wrapper.find('[data-testid="tickets-available"]').text()).toBe('98,765');
    expect(wrapper.find('[data-testid="tickets-held"]').text()).toBe('35');
    expect(wrapper.find('[data-testid="tickets-expired"]').text()).toBe('17');
    expect(wrapper.find('[data-testid="invariant-violations"]').text()).toBe('0');
    expect(wrapper.find('[data-testid="live-rps"]').text()).toBe('180');
    wrapper.unmount();
  });

  it('chart options carry the exact series values', () => {
    const pts = [makeTrafficPoint(0, { rps: 120, targetRate: 37.5 }), makeTrafficPoint(1, { rps: 130, targetRate: 40 })];
    const opt = trafficOption(pts);
    const series = opt.series as Array<{ name: string; data: Array<[number, number]> }>;
    expect(series.find((s) => s.name === 'HTTP req/s')!.data.map((d) => d[1])).toEqual([120, 130]);
    expect(series.find((s) => s.name.startsWith('Requested'))!.data.map((d) => d[1])).toEqual([37.5, 40]);

    const inv = inventoryOption([{ t: 1, capacity: 100, available: 60, held: 10, sold: 30, expired: 2, rejected: 5 }]);
    const invSeries = inv.series as Array<{ name: string; data: Array<[number, number]> }>;
    expect(invSeries.find((s) => s.name === 'Sold')!.data[0]![1]).toBe(30);
    expect(invSeries.find((s) => s.name === 'Rejected (sold out)')!.data[0]![1]).toBe(5);
  });

  it('bounds series length and resets when the experiment id changes', async () => {
    const { store } = makeStore({
      [SERIES]: { body: emptySeries() },
      'GET /api/experiments/exp_other/series': { body: emptySeries() },
    });
    store.applyTick(tick([]));
    await flushPromises();
    store.applyTick(tick(Array.from({ length: 10 }, (_, i) => makeTrafficPoint(i))));
    expect(store.series.traffic).toHaveLength(10);
    expect(mergeBounded(store.series.traffic, [makeTrafficPoint(10)], byT, 5).map((p) => p.second)).toEqual([6, 7, 8, 9, 10]);

    const other = makeSnapshot({ experiment: makeExperiment({ id: 'exp_other' }) });
    store.applySnapshot(other);
    expect(store.series.traffic).toHaveLength(0);
    expect(store.status.seriesExperimentId).toBe('exp_other');
  });
});
