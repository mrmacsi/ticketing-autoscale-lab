import { describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import EventTimeline from '@/components/timeline/EventTimeline.vue';
import { scalingMarkers } from '@/charts/options';
import { emptySeries, makeEvent, makeInstance, makeSnapshot } from './fixtures';
import { makeStore } from './helpers';

const SERIES = 'GET /api/experiments/exp_20261009_1530_ab12/series';

describe('event timeline', () => {
  it('renders exactly the events it is given, newest first, with relative and absolute time', () => {
    const events = [
      makeEvent('e1', { t: '2026-10-09T15:29:30.000Z', kind: 'experiment', source: 'control_plane', attribution: null, message: 'Experiment ready' }),
      makeEvent('e2', { t: '2026-10-09T15:31:05.000Z', kind: 'scaling', attribution: 'autoscaler', message: 'Target size 1 → 2' }),
      makeEvent('e3', { t: '2026-10-09T15:32:00.000Z', kind: 'instance', attribution: 'autoscaler', message: 'app-x7k2 created' }),
    ];
    const w = mount(EventTimeline, { props: { events, trafficStartedAt: '2026-10-09T15:30:00.000Z' } });
    const items = w.findAll('[data-testid="timeline-event"]');
    expect(items.map((i) => i.attributes('data-event-id'))).toEqual(['e3', 'e2', 'e1']);
    expect(items[1]!.find('[data-testid="event-relative"]').text()).toBe('01:05');
    expect(items[2]!.find('[data-testid="event-relative"]').text()).toBe('-00:30');
    expect(items[1]!.text()).toContain('15:31:05 UTC');
    expect(items[1]!.find('[data-testid="event-attribution"]').text()).toBe('autoscaler');
    expect(items[2]!.find('[data-testid="event-attribution"]').exists()).toBe(false);
  });

  it('shows an empty state rather than placeholder events', () => {
    const w = mount(EventTimeline, { props: { events: [] } });
    expect(w.findAll('[data-testid="timeline-event"]')).toHaveLength(0);
    expect(w.find('[data-testid="timeline-empty"]').exists()).toBe(true);
  });

  it('the store never invents events from snapshot changes', async () => {
    const { store } = makeStore({ [SERIES]: { body: emptySeries() } });
    const a = makeSnapshot();
    store.applyTick({ snapshot: a, append: { traffic: [], scaling: [{ t: 1, actual: 2, running: 2, healthy: 2, target: 2, min: 1, max: 5 }], cpu: [], inventory: [] } });
    await flushPromises();

    const b = makeSnapshot({ instances: [makeInstance('a'), makeInstance('b'), makeInstance('c', { phase: 'starting' })] });
    b.mig!.actualCount = 3;
    b.mig!.targetSize = 4;
    b.mig!.overridesActive = false;
    store.applyTick({ snapshot: b, append: { traffic: [], scaling: [{ t: 2, actual: 3, running: 3, healthy: 2, target: 4, min: 1, max: 5 }], cpu: [], inventory: [] } });
    store.handleMessage({ event: 'experiment', data: JSON.stringify({ ...b.experiment, state: 'stopping' }), id: null });
    await flushPromises();

    expect(store.timeline.value).toEqual([]);
    expect(scalingMarkers(store.timeline.value)).toEqual([]);
  });

  it('adds server timeline events once each, merged with bootstrapped series events', async () => {
    const boot = makeEvent('boot-1', { t: '2026-10-09T15:30:00.000Z', kind: 'traffic', source: 'generator', attribution: null });
    const { store } = makeStore({ [SERIES]: { body: { ...emptySeries(), events: [boot] } } });
    store.applySnapshot(makeSnapshot());
    await flushPromises();
    const ev = makeEvent('s-1');
    store.handleMessage({ event: 'timeline', data: JSON.stringify(ev), id: null });
    store.handleMessage({ event: 'timeline', data: JSON.stringify(ev), id: null });
    store.handleMessage({ event: 'timeline', data: '{not json', id: null });
    expect(store.timeline.value.map((e) => e.id)).toEqual(['boot-1', 's-1']);
    expect(scalingMarkers(store.timeline.value)).toHaveLength(1);
  });
});
