import { describe, expect, it } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import DashboardView from '@/views/DashboardView.vue';
import { emptySeries, makeExperiment, makeMeta, makeSnapshot } from './fixtures';
import { makeStore, mountWithStore, type RouteHandler } from './helpers';
import type { ExperimentConfig, ExperimentState } from '@/types/contracts';

async function setup(routes: Record<string, RouteHandler | { status?: number; body?: unknown }> = {}, state: ExperimentState | null = null) {
  const { store, fetch } = makeStore({
    'GET /api/state': { body: makeSnapshot({ experiment: state ? makeExperiment({ state }) : null }) },
    'GET /api/experiments/exp_20261009_1530_ab12/series': { body: emptySeries() },
    ...routes,
  });
  store.meta.value = makeMeta();
  store.applySnapshot(makeSnapshot({ experiment: state ? makeExperiment({ state }) : null }));
  const wrapper = await mountWithStore(DashboardView, store);
  await flushPromises();
  return { store, fetch, wrapper };
}

const posted = (fetch: Awaited<ReturnType<typeof setup>>['fetch']) =>
  fetch.mock.calls.filter((c) => String(c[0]) === '/api/experiments' && (c[1] as RequestInit).method === 'POST').map((c) => JSON.parse(String((c[1] as RequestInit).body)) as ExperimentConfig);

async function setNumber(wrapper: Awaited<ReturnType<typeof setup>>['wrapper'], testid: string, value: string) {
  await wrapper.find(`[data-testid="${testid}"]`).setValue(value);
  await flushPromises();
}

describe('experiment configuration', () => {
  it('shows inline validation for mix sum and blocks Prepare', async () => {
    const { wrapper } = await setup();
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeUndefined();
    await setNumber(wrapper, 'mix-purchase', '30');
    expect(wrapper.find('[data-testid="mix-total"]').text()).toContain('currently 115 %');
    expect(wrapper.find('[data-testid="btn-prepare"]').attributes('disabled')).toBeDefined();
    await setNumber(wrapper, 'mix-purchase', '15');
    expect(wrapper.find('[data-testid="mix-total"]').text()).toBe('Mix total 100 %');
    wrapper.unmount();
  });

  it('validates min/initial/max ordering and the approved maximum', async () => {
    const { wrapper } = await setup();
    await setNumber(wrapper, 'servers-max', '9');
    expect(wrapper.text()).toContain('cannot exceed the approved maximum of 8');
    await setNumber(wrapper, 'servers-max', '5');
    await setNumber(wrapper, 'servers-min', '3');
    expect(wrapper.text()).toContain('Initial servers must be ≥ minimum servers');
    wrapper.unmount();
  });

  it('applies a preset from meta and shows its description', async () => {
    const { wrapper, store } = await setup();
    await wrapper.find('[data-testid="preset-C"]').trigger('click');
    expect(wrapper.find('[data-testid="preset-C"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.find('[data-testid="preset-C"]').text()).toContain('Demonstrates C');
    expect((wrapper.find('[data-testid="exp-name"]').element as HTMLInputElement).value).toBe('Preset C run');
    expect(store.meta.value!.presets[2]!.config.name).toBe('Preset C run'); // the preset itself is not mutated
    wrapper.unmount();
  });

  it('loads the new flash-sale preset and keeps the customer cap distinct from concurrency', async () => {
    const { wrapper, store } = await setup();
    const meta = structuredClone(store.meta.value!);
    const sale = meta.presets.find((p) => p.id === 'F')!;
    sale.config = {
      ...sale.config, preset: 'F',
      tickets: { ...sale.config.tickets, totalInventory: 2000, reservationTtlSeconds: 10 },
      traffic: { ...sale.config.traffic, totalArrivals: 5000, maxVUs: 2000,
        durationSeconds: 60, pattern: { type: 'constant', rate: 500 } },
    };
    store.meta.value = meta;
    await flushPromises();
    await wrapper.find('[data-testid="preset-F"]').trigger('click');
    expect(wrapper.find('[data-testid="preset-F"]').attributes('aria-pressed')).toBe('true');
    expect((wrapper.find('[data-testid="total-arrivals"]').element as HTMLInputElement).value).toBe('5000');
    expect((wrapper.find('[data-testid="max-vus"]').element as HTMLInputElement).value).toBe('2000');
    expect((wrapper.find('[data-testid="rate"]').element as HTMLInputElement).value).toBe('500');
    const hold = wrapper.findAll('[data-testid="form-field"]').find((field) => field.find('label').text() === 'Reservation TTL')!;
    expect((hold.find('input').element as HTMLInputElement).value).toBe('10');
    expect(wrapper.find('[data-testid="expensive-warning"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('asks for explicit confirmation on 428 and resends with confirmExpensive: true', async () => {
    const createRoute: RouteHandler = ({ body }) =>
      (body as ExperimentConfig).confirmExpensive
        ? { status: 201, body: makeExperiment({ state: 'preparing', trafficStartedAt: null }) }
        : { status: 428, body: { error: 'confirmation_required', message: 'Max servers 6 exceeds 5: confirm cost.' } };
    const { wrapper, fetch, store } = await setup({ 'POST /api/experiments': createRoute });

    await setNumber(wrapper, 'servers-max', '6');
    expect(wrapper.find('[data-testid="expensive-warning"]').text()).toContain('max servers 6 > 5');

    await wrapper.find('[data-testid="btn-prepare"]').trigger('click');
    await flushPromises();
    expect(posted(fetch)).toHaveLength(1);
    expect(posted(fetch)[0]!.confirmExpensive).toBe(false);
    expect(wrapper.find('[data-testid="confirm-message"]').text()).toBe('Max servers 6 exceeds 5: confirm cost.');

    const ok = wrapper.find('[data-testid="confirm-ok"]');
    expect(ok.attributes('disabled')).toBeDefined();
    await wrapper.find('[data-testid="confirm-ack"]').setValue(true);
    expect(ok.attributes('disabled')).toBeUndefined();
    await ok.trigger('click');
    await flushPromises();

    const bodies = posted(fetch);
    expect(bodies).toHaveLength(2);
    expect(bodies[1]!.confirmExpensive).toBe(true);
    expect(bodies[1]!.servers.max).toBe(6);
    expect(wrapper.find('[data-testid="confirm-message"]').exists()).toBe(false);
    expect(store.status.lastFailure).toBeNull();
    wrapper.unmount();
  });

  it('shows server 422 details inline', async () => {
    const { wrapper } = await setup({
      'POST /api/experiments': { status: 422, body: { error: 'validation_failed', message: 'Invalid config', details: { 'tickets.totalInventory': 'exceeds database headroom' } } },
    });
    await wrapper.find('[data-testid="btn-prepare"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-testid="server-error"]').text()).toContain('Invalid config');
    expect(wrapper.findAll('[data-testid="field-error"]').some((e) => e.text() === 'exceeds database headroom')).toBe(true);
    wrapper.unmount();
  });

  it('prevents double submits while a request is in flight', async () => {
    let release: () => void = () => undefined;
    const { wrapper, fetch } = await setup({
      'POST /api/experiments': () => new Promise((r) => (release = () => r({ status: 201, body: makeExperiment({ state: 'preparing' }) }))),
    });
    const btn = wrapper.find('[data-testid="btn-prepare"]');
    await btn.trigger('click');
    await btn.trigger('click');
    await flushPromises();
    expect(btn.attributes('disabled')).toBeDefined();
    expect(btn.text()).toBe('Preparing…');
    release();
    await flushPromises();
    expect(posted(fetch)).toHaveLength(1);
    wrapper.unmount();
  });
});

describe('experiment controls', () => {
  const enabled = (w: Awaited<ReturnType<typeof setup>>['wrapper'], id: string) => w.find(`[data-testid="${id}"]`).attributes('disabled') === undefined;

  it.each([
    [null, { prepare: true, start: false, stop: false, complete: false, resetInventory: false }],
    ['ready', { prepare: false, start: true, stop: false, complete: false, resetInventory: true }],
    ['running', { prepare: false, start: false, stop: true, complete: false, resetInventory: false }],
    ['stopping', { prepare: false, start: false, stop: false, complete: false, resetInventory: false }],
    ['cooling_down', { prepare: false, start: false, stop: false, complete: true, resetInventory: true }],
    ['completed', { prepare: true, start: false, stop: false, complete: false, resetInventory: true }],
  ] as const)('state %s enables exactly the allowed actions', async (state, expected) => {
    const { wrapper } = await setup({}, state);
    expect({
      prepare: enabled(wrapper, 'btn-prepare'),
      start: enabled(wrapper, 'btn-start'),
      stop: enabled(wrapper, 'btn-stop'),
      complete: enabled(wrapper, 'btn-complete'),
      resetInventory: enabled(wrapper, 'btn-reset-inventory'),
    }).toEqual(expected);
    if (state === 'running') expect(wrapper.find('[data-testid="reset-inventory-reason"]').text()).toContain('cannot be reset while traffic is running');
    wrapper.unmount();
  });

  it('shows the server error message when an action fails', async () => {
    const { wrapper } = await setup(
      { 'POST /api/experiments/exp_20261009_1530_ab12/stop': { status: 409, body: { error: 'conflict', message: 'generator lease lost' } } },
      'running',
    );
    await wrapper.find('[data-testid="btn-stop"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-testid="action-error"]').text()).toContain('generator lease lost');
    expect(wrapper.find('[data-testid="action-error"]').text()).toContain('HTTP 409');
    wrapper.unmount();
  });

  it('resets the experiment only after confirmation', async () => {
    const { wrapper, fetch } = await setup({ 'POST /api/reset': { body: { ok: true } } }, 'completed');
    await wrapper.find('[data-testid="btn-reset"]').trigger('click');
    expect(fetch.mock.calls.some((c) => String(c[0]) === '/api/reset')).toBe(false);
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click');
    await flushPromises();
    expect(fetch.mock.calls.some((c) => String(c[0]) === '/api/reset')).toBe(true);
    wrapper.unmount();
  });
});
