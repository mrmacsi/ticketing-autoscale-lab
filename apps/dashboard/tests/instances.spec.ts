import { describe, expect, it } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import DashboardView from '@/views/DashboardView.vue';
import { emptySeries, makeInstance, makeMeta, makeSnapshot } from './fixtures';
import { makeStore, mountWithStore } from './helpers';

const SERIES = 'GET /api/experiments/exp_20261009_1530_ab12/series';

async function setup() {
  const { store } = makeStore({ [SERIES]: { body: emptySeries() } });
  store.meta.value = makeMeta();
  const wrapper = await mountWithStore(DashboardView, store);
  return { store, wrapper };
}

const names = (w: Awaited<ReturnType<typeof setup>>['wrapper']) => w.findAll('[data-testid="instance-card"]').map((c) => c.attributes('data-instance'));

describe('live server visualisation', () => {
  it('renders one card per real instance and the MIG actual count', async () => {
    const { store, wrapper } = await setup();
    store.applySnapshot(makeSnapshot());
    await flushPromises();
    expect(names(wrapper)).toEqual(['ticketing-scale-lab-app-q9w1', 'ticketing-scale-lab-app-x7k2'].sort());
    expect(wrapper.find('[data-testid="stat-actual"]').text()).toBe('2');
    expect(wrapper.find('[data-testid="stat-target"]').text()).toBe('3');
    expect(wrapper.find('[data-testid="instance-card-count"]').text()).toBe('2');
    wrapper.unmount();
  });

  it('adds and removes cards as snapshot instances change', async () => {
    const { store, wrapper } = await setup();
    store.applySnapshot(makeSnapshot());
    await flushPromises();

    const three = makeSnapshot({
      instances: [makeInstance('app-a'), makeInstance('app-b'), makeInstance('app-new', { phase: 'starting', vmStatus: 'STAGING', currentAction: 'CREATING', createdAt: '2026-10-09T15:39:00.000Z' })],
    });
    three.mig!.actualCount = 3;
    store.applySnapshot(three);
    await flushPromises();
    expect(names(wrapper)).toEqual(['app-a', 'app-b', 'app-new']);
    expect(wrapper.find('[data-instance="app-new"] [data-testid="phase"]').text()).toBe('starting');
    expect(wrapper.find('[data-testid="stat-actual"]').text()).toBe('3');

    const one = makeSnapshot({ instances: [makeInstance('app-b')] });
    one.mig!.actualCount = 1;
    store.applySnapshot(one);
    await flushPromises();
    expect(names(wrapper)).toEqual(['app-b']);
    expect(wrapper.find('[data-instance="app-a"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('never infers the instance count from CPU or load', async () => {
    const { store, wrapper } = await setup();
    const snap = makeSnapshot({ mig: null, instances: [], cpu: { average: 0.95, sampleTime: '2026-10-09T15:39:00.000Z', target: 0.6 } });
    snap.traffic.rps = 900;
    store.applySnapshot(snap);
    await flushPromises();
    expect(wrapper.find('[data-testid="stat-actual"]').text()).toBe('N/A');
    expect(wrapper.findAll('[data-testid="instance-card"]')).toHaveLength(0);
    expect(wrapper.find('[data-testid="no-instances"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('shows the MIG count and the listed instances independently when they differ', async () => {
    const { store, wrapper } = await setup();
    const snap = makeSnapshot();
    snap.mig!.actualCount = 3;
    store.applySnapshot(snap);
    await flushPromises();
    expect(wrapper.find('[data-testid="stat-actual"]').text()).toBe('3');
    expect(wrapper.findAll('[data-testid="instance-card"]')).toHaveLength(2);
    wrapper.unmount();
  });
});
