import { describe, expect, it, vi } from 'vitest';
import { makeMeta, makeSnapshot } from './fixtures';
import { makeStore } from './helpers';

const openStream = () => new Response(new ReadableStream({ start() {} }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } });

describe('limits stay current without a page reload', () => {
  it('reloads limits when the live stream (re)connects, e.g. after a control-plane deploy', async () => {
    let maxVUs = 10_000;
    const { store, fetch } = makeStore({
      'GET /api/meta': () => ({ body: { ...makeMeta(), limits: { ...makeMeta().limits, maxVUs } } }),
      'GET /api/state': { body: makeSnapshot() },
      'GET /api/stream': () => openStream(),
    });
    const started = store.startLive();
    maxVUs = 1_000_000; // the deploy lands while the page is open
    await started;
    await vi.waitFor(() => expect(store.meta.value?.limits.maxVUs).toBe(1_000_000));
    expect(fetch.mock.calls.filter((c) => String(c[0]) === '/api/meta').length).toBeGreaterThanOrEqual(2);
    store.stopLive();
  });
});
