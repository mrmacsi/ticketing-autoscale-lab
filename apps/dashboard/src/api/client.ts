import type {
  ApiErrorBody,
  AuthConfig,
  Experiment,
  ExperimentConfig,
  ExperimentSummary,
  LiveSnapshot,
  Me,
  Meta,
  Report,
  Series,
} from '@/types/contracts';

export interface FieldIssue {
  path: string | null;
  message: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }

  get isAuthFailure(): boolean {
    return this.status === 401 || this.status === 403;
  }

  /** Normalises the optional 422 `details` (object map or array) into a flat issue list. */
  get fieldIssues(): FieldIssue[] {
    return normaliseDetails(this.details);
  }
}

export function normaliseDetails(details: unknown): FieldIssue[] {
  if (details == null) return [];
  if (Array.isArray(details)) {
    return details.map((d) => {
      if (typeof d === 'string') return { path: null, message: d };
      if (d && typeof d === 'object') {
        const o = d as Record<string, unknown>;
        const rawPath = o.path ?? o.field ?? o.instancePath ?? null;
        const path = Array.isArray(rawPath) ? rawPath.join('.') : rawPath == null ? null : String(rawPath).replace(/^\//, '').replace(/\//g, '.');
        return { path, message: String(o.message ?? o.msg ?? JSON.stringify(o)) };
      }
      return { path: null, message: String(d) };
    });
  }
  if (typeof details === 'object') {
    return Object.entries(details as Record<string, unknown>).map(([path, v]) => ({
      path,
      message: Array.isArray(v) ? v.join('; ') : String(v),
    }));
  }
  return [{ path: null, message: String(details) }];
}

export interface ClientHooks {
  /** Bearer token to attach (google_signin mode) or null. */
  getToken: () => string | null;
  /** Invoked for any 401/403 from an authenticated endpoint. */
  onAuthFailure: (status: number, error: ApiError) => void;
  fetchImpl?: typeof fetch;
  baseUrl?: string;
}

export type ExportFormat = 'json' | 'csv';

export function createApiClient(hooks: ClientHooks) {
  const fetchImpl = hooks.fetchImpl ?? fetch.bind(globalThis);
  const base = hooks.baseUrl ?? '';

  function authHeaders(): Record<string, string> {
    const token = hooks.getToken();
    return token ? { Authorization: `Bearer ${token}` } : {};
  }

  async function raw(method: string, path: string, body?: unknown): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json', ...authHeaders() };
    const init: RequestInit = { method, headers, cache: 'no-store' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    let res: Response;
    try {
      res = await fetchImpl(base + path, init);
    } catch (err) {
      throw new ApiError(0, 'network_error', `Network error contacting the control API: ${err instanceof Error ? err.message : String(err)}`);
    }
    if (!res.ok) {
      const error = await toApiError(res);
      if (error.isAuthFailure) hooks.onAuthFailure(res.status, error);
      throw error;
    }
    return res;
  }

  async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await raw(method, path, body);
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  const enc = encodeURIComponent;

  return {
    fetchImpl,
    authHeaders,
    getMe: () => request<Me>('GET', '/api/me'),
    getMeta: () => request<Meta>('GET', '/api/meta'),
    getState: () => request<LiveSnapshot>('GET', '/api/state'),
    createExperiment: (config: ExperimentConfig) => request<Experiment>('POST', '/api/experiments', config),
    startExperiment: (id: string) => request<Experiment>('POST', `/api/experiments/${enc(id)}/start`),
    stopExperiment: (id: string) => request<Experiment>('POST', `/api/experiments/${enc(id)}/stop`),
    completeExperiment: (id: string) => request<Experiment>('POST', `/api/experiments/${enc(id)}/complete`),
    resetInventory: (id: string) => request<unknown>('POST', `/api/experiments/${enc(id)}/reset-inventory`),
    resetLab: () => request<unknown>('POST', '/api/reset'),
    async listExperiments(): Promise<ExperimentSummary[]> {
      const body = await request<unknown>('GET', '/api/experiments');
      if (Array.isArray(body)) return body as ExperimentSummary[];
      const o = (body ?? {}) as Record<string, unknown>;
      const list = o.experiments ?? o.data ?? o.items;
      return Array.isArray(list) ? (list as ExperimentSummary[]) : [];
    },
    getExperiment: (id: string) => request<Experiment>('GET', `/api/experiments/${enc(id)}`),
    getSeries: (id: string) => request<Series>('GET', `/api/experiments/${enc(id)}/series`),
    getReport: (id: string) => request<Report>('GET', `/api/experiments/${enc(id)}/report`),
    exportPath: (id: string, format: ExportFormat) => `${base}/api/experiments/${enc(id)}/export.${format}`,
    /** Fetches an export with auth headers (plain links cannot carry a bearer token). */
    async downloadExport(id: string, format: ExportFormat): Promise<{ blob: Blob; filename: string }> {
      const res = await raw('GET', `/api/experiments/${enc(id)}/export.${format}`);
      const disposition = res.headers.get('Content-Disposition') ?? '';
      const match = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(disposition);
      const filename = match?.[1] ? decodeURIComponent(match[1]) : `${id}.${format}`;
      return { blob: await res.blob(), filename };
    },
  };
}

export type ApiClient = ReturnType<typeof createApiClient>;

/** `/auth/config` is public and must be fetched before any token exists. */
export async function fetchAuthConfig(fetchImpl: typeof fetch = fetch.bind(globalThis)): Promise<AuthConfig> {
  const res = await fetchImpl('/auth/config', { headers: { Accept: 'application/json' }, cache: 'no-store' });
  if (!res.ok) throw await toApiError(res);
  return (await res.json()) as AuthConfig;
}

async function toApiError(res: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> = {};
  try {
    const text = await res.text();
    body = text ? (JSON.parse(text) as Partial<ApiErrorBody>) : {};
  } catch {
    body = {};
  }
  const code = typeof body.error === 'string' ? body.error : `http_${res.status}`;
  const message = typeof body.message === 'string' && body.message ? body.message : `${res.status} ${res.statusText || 'request failed'}`;
  return new ApiError(res.status, code, message, body.details);
}
