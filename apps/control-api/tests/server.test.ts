import { describe, expect, it } from 'vitest';
import { Authenticator } from '../src/auth/auth.js';
import { ExperimentService } from '../src/experiments/service.js';
import { UnavailableInfra } from '../src/infra/types.js';
import { buildServer } from '../src/server.js';
import { MemoryStore } from '../src/store/memory.js';
import { Collector } from '../src/telemetry/collector.js';
import { TicketingAdmin } from '../src/ticketing/admin.js';
import { InventoryReader } from '../src/ticketing/inventory.js';
import { testConfig } from './fakes.js';

const quiet = { info: () => undefined, warn: () => undefined, error: () => undefined };

function server(env: Record<string, string>, onCloudRun = false) {
  const config = testConfig(env);
  const store = new MemoryStore();
  const infra = new UnavailableInfra();
  const collector = new Collector(config, infra, store, new InventoryReader(null), quiet);
  const experiments = new ExperimentService(config, store, infra, collector, new TicketingAdmin('', () => null), new InventoryReader(null), quiet);
  return buildServer({ config, auth: new Authenticator(config.auth, onCloudRun), store, collector, experiments });
}

const jwt = (payload: object) => `e30.${Buffer.from(JSON.stringify(payload)).toString('base64url')}.`;

describe('authentication', () => {
  it('serves auth bootstrap publicly but protects every /api route', async () => {
    const app = server({ AUTH_MODE: 'google_signin', GOOGLE_CLIENT_ID: 'client.apps.googleusercontent.com', ALLOWED_EMAILS: 'me@example.com' });
    expect((await app.inject('/auth/config')).json()).toEqual({ authMode: 'google_signin', googleClientId: 'client.apps.googleusercontent.com' });
    for (const url of ['/api/state', '/api/meta', '/api/experiments', '/api/stream']) {
      expect((await app.inject(url)).statusCode, url).toBe(401);
    }
    const forged = await app.inject({ url: '/api/state', headers: { authorization: `Bearer ${jwt({ email: 'me@example.com', email_verified: true })}` } });
    expect(forged.statusCode).toBe(401);
    expect((await app.inject({ method: 'POST', url: '/api/reset' })).statusCode).toBe(401);
  });

  it('applies the allowlist to the identity Cloud Run IAM forwarded', async () => {
    const app = server({ AUTH_MODE: 'iam_proxy', ALLOWED_EMAILS: 'me@example.com' }, true);
    const allowed = await app.inject({ url: '/api/me', headers: { authorization: `Bearer ${jwt({ email: 'Me@Example.com' })}` } });
    expect(allowed.json()).toMatchObject({ email: 'me@example.com', verifiedBy: 'cloud_run_iam' });
    const denied = await app.inject({ url: '/api/me', headers: { authorization: `Bearer ${jwt({ email: 'other@example.com' })}` } });
    expect(denied.statusCode).toBe(403);
  });

  it('refuses development auth on Cloud Run', () => {
    expect(() => testConfig({ AUTH_MODE: 'dev', K_SERVICE: 'control' })).toThrow(/refused/);
  });
});

describe('state endpoint', () => {
  it('reports missing telemetry as missing instead of zeros', async () => {
    const app = server({ AUTH_MODE: 'dev' });
    const state = (await app.inject('/api/state')).json();
    expect(state.mig).toBeNull();
    expect(state.instances).toEqual([]);
    expect(state.traffic.rps).toBeNull();
    expect(state.cpu.average).toBeNull();
    expect(state.freshness.mig.status).toBe('missing');
    expect(state.freshness.cpu.status).toBe('missing');
    expect(state.waitingRoom).toBeNull();
    expect(state.freshness.waitingRoom).toMatchObject({ status: 'missing', note: expect.stringMatching(/not deployed/) });
    expect(state.traffic.queueWaiting).toBeNull();
  });

  it('tells the dashboard whether the waiting room is deployed', async () => {
    expect((await server({ AUTH_MODE: 'dev' }).inject('/api/meta')).json().waitingRoom).toEqual({ deployed: false });
    const meta = (await server({ AUTH_MODE: 'dev', WAITING_ROOM_URL: 'http://10.60.0.20:8080' }).inject('/api/meta')).json();
    expect(meta.waitingRoom).toEqual({ deployed: true });
    expect(meta.presets.find((p: { id: string }) => p.id === 'F').config.waitingRoom.enabled).toBe(true);
    expect(meta.defaults.waitingRoom.enabled).toBe(false);
  });

  it('returns validation details for bad experiment configs', async () => {
    const app = server({ AUTH_MODE: 'dev' });
    const response = await app.inject({ method: 'POST', url: '/api/experiments', payload: { name: '' } });
    expect(response.statusCode).toBe(422);
    expect(response.json().error).toBe('validation_failed');
  });
});
