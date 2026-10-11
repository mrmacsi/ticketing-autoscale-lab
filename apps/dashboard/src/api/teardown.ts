// Client for the local dashboard proxy's /local/* endpoints (scripts/dashboard-proxy.mjs). They only
// exist when the dashboard is opened through that proxy on the operator's machine.

export interface TeardownStatus {
  state: 'idle' | 'running' | 'succeeded' | 'failed';
  startedAt: string | null;
  finishedAt: string | null;
  exitCode: number | null;
  lines: string[];
}

export interface LocalCapabilities {
  teardown: boolean;
  confirmPhrase: string;
  project: string;
  account: string;
}

export async function localCapabilities(fetchImpl: typeof fetch = fetch): Promise<LocalCapabilities | null> {
  try {
    const res = await fetchImpl('/local/status', { cache: 'no-store' });
    if (!res.ok || !res.headers.get('content-type')?.includes('application/json')) return null;
    const body = (await res.json()) as Partial<LocalCapabilities>;
    return body.teardown ? (body as LocalCapabilities) : null;
  } catch {
    return null;
  }
}

export async function teardownStatus(fetchImpl: typeof fetch = fetch): Promise<TeardownStatus> {
  const res = await fetchImpl('/local/teardown', { cache: 'no-store' });
  if (!res.ok) throw new Error(`Teardown status unavailable (HTTP ${res.status})`);
  return (await res.json()) as TeardownStatus;
}

export async function startTeardown(confirmPhrase: string, fetchImpl: typeof fetch = fetch): Promise<TeardownStatus> {
  const res = await fetchImpl('/local/teardown', { method: 'POST', headers: { 'X-Lab-Confirm': confirmPhrase } });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ? `Teardown refused: ${body.error}` : `Teardown refused (HTTP ${res.status})`);
  return body as TeardownStatus;
}
