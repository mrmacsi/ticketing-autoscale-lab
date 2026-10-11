import { vi } from 'vitest';
import { createApiClient, type ApiClient } from '@/api/client';
import { createLabStore, type LabStore } from '@/store/lab';

export type RouteResult = { status?: number; body?: unknown; headers?: Record<string, string> } | Response;
export type RouteHandler = (req: { method: string; path: string; headers: Record<string, string>; body: unknown }) => RouteResult | Promise<RouteResult>;

/** fetch mock keyed by "METHOD /path". Unknown routes return 404. */
export function mockFetch(routes: Record<string, RouteResult | RouteHandler>) {
  return vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const path = url.replace(/^https?:\/\/[^/]+/, '');
    const method = (init.method ?? 'GET').toUpperCase();
    const headers = { ...((init.headers as Record<string, string>) ?? {}) };
    const body = typeof init.body === 'string' ? JSON.parse(init.body) : undefined;
    const route = routes[`${method} ${path}`];
    if (!route) return new Response(JSON.stringify({ error: 'not_found', message: `no route ${method} ${path}` }), { status: 404 });
    const result = typeof route === 'function' ? await route({ method, path, headers, body }) : route;
    if (result instanceof Response) return result;
    return new Response(result.body === undefined ? '' : JSON.stringify(result.body), {
      status: result.status ?? 200,
      headers: { 'Content-Type': 'application/json', ...(result.headers ?? {}) },
    });
  });
}

export function makeApi(fetchImpl: typeof fetch, token: string | null = null, onAuthFailure = vi.fn()): ApiClient {
  return createApiClient({ getToken: () => token, onAuthFailure, fetchImpl });
}

export function makeStore(routes: Record<string, RouteResult | RouteHandler> = {}): { store: LabStore; fetch: ReturnType<typeof mockFetch> } {
  const fetch = mockFetch(routes);
  const store = createLabStore(makeApi(fetch as unknown as typeof globalThis.fetch), { fetchImpl: fetch as unknown as typeof globalThis.fetch });
  return { store, fetch };
}

export const flush = () => new Promise((r) => setTimeout(r, 0));

import { mount, flushPromises, type ComponentMountingOptions } from '@vue/test-utils';
import { createMemoryHistory } from 'vue-router';
import { createAppRouter } from '@/router';
import { LAB_STORE } from '@/store/lab';
import type { Component } from 'vue';

export async function mountWithStore<C extends Component>(component: C, store: LabStore, options: ComponentMountingOptions<C> = {} as ComponentMountingOptions<C>) {
  const router = createAppRouter(createMemoryHistory());
  router.push('/');
  await router.isReady();
  const wrapper = mount(component, {
    ...options,
    global: { ...(options.global ?? {}), plugins: [router], provide: { [LAB_STORE as symbol]: store } },
  } as ComponentMountingOptions<C>);
  await flushPromises();
  return wrapper;
}
