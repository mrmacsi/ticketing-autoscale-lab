import { describe, expect, it, vi } from 'vitest';
import { ApiError, normaliseDetails } from '@/api/client';
import { makeApi, mockFetch } from './helpers';

describe('api client', () => {
  it('attaches the bearer token when one is provided (google_signin mode)', async () => {
    const fetch = mockFetch({ 'GET /api/me': { body: { email: 'a@b.c', authMode: 'google_signin' } } });
    const api = makeApi(fetch as unknown as typeof globalThis.fetch, 'id-token-xyz');
    await api.getMe();
    const init = fetch.mock.calls[0]![1] as RequestInit;
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer id-token-xyz');
  });

  it('sends no Authorization header without a token (iam_proxy / dev)', async () => {
    const fetch = mockFetch({ 'GET /api/me': { body: { email: 'a@b.c', authMode: 'iam_proxy' } } });
    const api = makeApi(fetch as unknown as typeof globalThis.fetch, null);
    await api.getMe();
    const init = fetch.mock.calls[0]![1] as RequestInit;
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
  });

  it('maps error bodies to ApiError and calls the auth hook on 401/403', async () => {
    const onAuthFailure = vi.fn();
    const fetch = mockFetch({
      'GET /api/state': { status: 403, body: { error: 'forbidden', message: 'not on allowlist' } },
      'POST /api/experiments': { status: 409, body: { error: 'conflict', message: 'another experiment is active' } },
    });
    const api = makeApi(fetch as unknown as typeof globalThis.fetch, null, onAuthFailure);
    await expect(api.getState()).rejects.toMatchObject({ status: 403, code: 'forbidden', message: 'not on allowlist' });
    expect(onAuthFailure).toHaveBeenCalledWith(403, expect.any(ApiError));
    await expect(api.startExperiment('x')).rejects.toMatchObject({ status: 404 });
    await expect(api.createExperiment({} as never)).rejects.toMatchObject({ status: 409, message: 'another experiment is active' });
    expect(onAuthFailure).toHaveBeenCalledTimes(1);
  });

  it('normalises 422 details from maps and arrays', () => {
    expect(normaliseDetails({ 'servers.max': 'too big' })).toEqual([{ path: 'servers.max', message: 'too big' }]);
    expect(normaliseDetails([{ path: ['traffic', 'mix'], message: 'sum' }, { instancePath: '/servers/min', message: 'low' }, 'plain'])).toEqual([
      { path: 'traffic.mix', message: 'sum' },
      { path: 'servers.min', message: 'low' },
      { path: null, message: 'plain' },
    ]);
  });
});
