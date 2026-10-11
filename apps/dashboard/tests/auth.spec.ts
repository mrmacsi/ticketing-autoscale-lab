import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createMemoryHistory } from 'vue-router';
import App from '@/App.vue';
import { createAuthSession } from '@/api/auth';
import { createAppRouter } from '@/router';
import { makeMeta, makeSnapshot, emptySeries } from './fixtures';
import { mockFetch, type RouteHandler } from './helpers';

const openStream = () => new Response(new ReadableStream({ start() {} }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } });

async function mountApp(fetch: ReturnType<typeof mockFetch>) {
  const session = createAuthSession(fetch as unknown as typeof globalThis.fetch);
  const router = createAppRouter(createMemoryHistory());
  router.push('/');
  await router.isReady();
  const wrapper = mount(App, { props: { session }, global: { plugins: [router] }, attachTo: document.body });
  await flushPromises();
  await vi.dynamicImportSettled();
  await flushPromises();
  return { wrapper, session };
}

afterEach(() => {
  delete window.google;
  document.body.innerHTML = '';
});

describe('auth enforcement', () => {
  it('shows the not-authorised screen and loads no data when /api/me returns 401 (iam_proxy)', async () => {
    const fetch = mockFetch({
      'GET /auth/config': { body: { authMode: 'iam_proxy', googleClientId: null } },
      'GET /api/me': { status: 401, body: { error: 'unauthenticated', message: 'missing identity' } },
      'GET /api/state': { body: makeSnapshot() },
      'GET /api/meta': { body: makeMeta() },
    });
    const { wrapper } = await mountApp(fetch);
    expect(wrapper.find('[data-testid="not-authorised-screen"]').exists()).toBe(true);
    expect(wrapper.text()).toContain('missing identity');
    expect(wrapper.find('[data-testid="instance-card"]').exists()).toBe(false);
    const paths = fetch.mock.calls.map((c) => String(c[0]));
    expect(paths).not.toContain('/api/state');
    expect(paths).not.toContain('/api/meta');
    wrapper.unmount();
  });

  it('shows not-authorised on 403 (not on the allowlist)', async () => {
    const fetch = mockFetch({
      'GET /auth/config': { body: { authMode: 'dev', googleClientId: null } },
      'GET /api/me': { status: 403, body: { error: 'forbidden', message: 'user@example.com is not allowed' } },
    });
    const { wrapper } = await mountApp(fetch);
    expect(wrapper.find('[data-testid="not-authorised-screen"]').text()).toContain('HTTP 403');
    wrapper.unmount();
  });

  it('google_signin: shows sign-in, then sends the bearer token on every request incl. the SSE stream', async () => {
    let gisCallback: ((r: { credential: string }) => void) | null = null;
    window.google = {
      accounts: {
        id: {
          initialize: (o) => {
            gisCallback = o.callback;
          },
          renderButton: vi.fn(),
          prompt: vi.fn(),
          disableAutoSelect: vi.fn(),
        },
      },
    };
    const authed: RouteHandler = ({ headers }) =>
      headers.Authorization === 'Bearer google-id-token' ? { body: null } : { status: 401, body: { error: 'unauthenticated', message: 'no token' } };
    let stateCalls = 0;
    const fetch = mockFetch({
      'GET /auth/config': { body: { authMode: 'google_signin', googleClientId: 'client-123.apps.googleusercontent.com' } },
      'GET /api/me': (req) => {
        const r = authed(req);
        return 'status' in r && r.status === 401 ? r : { body: { email: 'operator@example.com', authMode: 'google_signin' } };
      },
      'GET /api/meta': (req) => ('status' in authed(req) ? authed(req) : { body: makeMeta() }),
      'GET /api/state': (req) => {
        stateCalls++;
        if (stateCalls > 1) return { status: 401, body: { error: 'unauthenticated', message: 'token expired' } };
        return 'status' in authed(req) ? authed(req) : { body: makeSnapshot() };
      },
      'GET /api/experiments/exp_20261009_1530_ab12/series': { body: emptySeries() },
      'GET /api/stream': (req) => ('status' in authed(req) ? authed(req) : openStream()),
    });

    const { wrapper, session } = await mountApp(fetch);
    expect(wrapper.find('[data-testid="sign-in-screen"]').exists()).toBe(true);
    expect(fetch.mock.calls.map((c) => String(c[0]))).not.toContain('/api/me');
    expect(gisCallback).not.toBeNull();

    gisCallback!({ credential: 'google-id-token' });
    await flushPromises();
    await vi.dynamicImportSettled();
    await flushPromises();

    expect(session.state.status).toBe('signed_in');
    await vi.waitFor(() => expect(fetch.mock.calls.some((c) => String(c[0]) === '/api/stream')).toBe(true));
    expect(wrapper.find('[data-testid="me-email"]').text()).toBe('operator@example.com');
    for (const path of ['/api/me', '/api/meta', '/api/state', '/api/stream']) {
      const call = fetch.mock.calls.find((c) => String(c[0]) === path);
      expect(call, path).toBeDefined();
      expect(((call![1] as RequestInit).headers as Record<string, string>).Authorization).toBe('Bearer google-id-token');
    }
    expect(wrapper.findAll('[data-testid="instance-card"]')).toHaveLength(2);

    // A later 401 (expired token) re-prompts for sign-in and clears the data from the screen.
    await session.api.getState().catch(() => undefined);
    await flushPromises();
    expect(wrapper.find('[data-testid="sign-in-screen"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="instance-card"]').exists()).toBe(false);
    expect(session.state.token).toBeNull();
    wrapper.unmount();
  });
});
