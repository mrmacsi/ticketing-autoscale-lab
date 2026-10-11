import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { defaultExperiment, presets } from '../src/experiments/presets.js';
import { ExperimentService } from '../src/experiments/service.js';
import { MemoryStore } from '../src/store/memory.js';
import { Collector } from '../src/telemetry/collector.js';
import { TicketingAdmin } from '../src/ticketing/admin.js';
import { InventoryReader } from '../src/ticketing/inventory.js';
import { adaptiveServer, bucket, FakeInfra, FakeWaitingRoom, roomStats, testConfig } from './fakes.js';

const quiet = { info: () => undefined, warn: () => undefined, error: () => undefined };

class FakeAdmin extends TicketingAdmin {
  resets: string[] = [];
  prepared: Array<Parameters<TicketingAdmin['prepare']>[0]> = [];
  settings: Array<Record<string, number | boolean>> = [];
  constructor() { super('t', () => 'http://fake'); }
  override async prepare(input: Parameters<TicketingAdmin['prepare']>[0]) {
    this.prepared.push(input);
    return { event: { id: 'evt-1', name: input.event_name, categories: input.categories.map((c, i) => ({ ...c, id: `cat-${i}` })) }, settings: input.settings };
  }
  override async resetInventory(eventId: string) { this.resets.push(eventId); return {}; }
  override async updateSettings(settings: Record<string, number | boolean>) { this.settings.push(settings); return settings; }
}

async function until(check: () => boolean, ms = 3000) {
  const end = Date.now() + ms;
  while (!check()) {
    if (Date.now() > end) throw new Error('condition not met');
    await new Promise((r) => setTimeout(r, 10));
  }
}

describe('ExperimentService lifecycle', () => {
  let store: MemoryStore; let infra: FakeInfra; let collector: Collector; let service: ExperimentService; let admin: FakeAdmin;
  const config = testConfig({ POLL_MIG_MS: '20', POLL_LB_MS: '20', POLL_CPU_MS: '50', TICK_MS: '50' });

  beforeEach(async () => {
    store = new MemoryStore();
    infra = new FakeInfra();
    collector = new Collector(config, infra, store, new InventoryReader(null), quiet);
    admin = new FakeAdmin();
    service = new ExperimentService(config, store, infra, collector, admin, new InventoryReader(null), quiet, () => new Promise((r) => setTimeout(r, 10)));
    collector.start();
    await service.init();
    await until(() => collector.currentMig !== null);
  });
  afterEach(() => { service.stop(); collector.stop(); });

  const heartbeat = (state: 'idle' | 'running' = 'idle') => {
    store.status = { agentId: 'gen', state, experimentId: null, lastCommandSeq: 0, heartbeatAt: new Date().toISOString(), runStartedAt: null,
      message: null, version: '1', limits: null, targetBaseUrl: null };
  };

  it('prepares, runs, cools down and completes with a report, restoring the baseline', async () => {
    const e = await service.create(presets(config)[1].config, 'me@example.com');
    await until(() => service.experiment?.state === 'ready');
    expect(service.experiment?.eventId).toBe('evt-1');
    expect(infra.patches[0]).toMatchObject({ minReplicas: 1, maxReplicas: 5, cpuTarget: 0.6 });

    heartbeat();
    await service.start(e.id, 'me@example.com');
    expect(store.commands.at(-1)).toMatchObject({ action: 'start', experimentId: e.id, config: { eventId: 'evt-1', experimentId: e.id } });
    expect(service.experiment?.state).toBe('running');

    store.pushBucket(e.id, bucket(0));
    store.setGenerator(e.id, { state: 'finished', finishReason: 'completed', finishedAt: new Date(Date.now() - 2_000_000).toISOString() });
    await until(() => service.experiment?.state === 'completed', 6000);
    const done = await store.getExperiment(e.id);
    expect(done?.report?.measured.httpRequests).toBe(30);
    expect(done?.report?.measured.invariantViolations).toBeNull();   // no database configured: unknown, not zero
    expect(await store.activeExperimentId()).toBeNull();
    const events = await store.listEvents(e.id);
    expect(events.some((ev) => ev.message.includes('Traffic stopped (completed)'))).toBe(true);
  });

  it('completes as soon as the extra servers are removed, not after the full cool-down', async () => {
    const f = presets(config).find((p) => p.id === 'F')!.config;
    const cfg = { ...f, waitingRoom: { ...f.waitingRoom, enabled: false }, cooldownObserveSeconds: 3600, confirmExpensive: true };
    const e = await service.create(cfg, 'me');
    await until(() => service.experiment?.state === 'ready');
    expect(infra.patches[0]).toMatchObject({ scaleInWindowSeconds: 60 });
    heartbeat();
    await service.start(e.id, 'me');
    infra.targetSize = 3;
    infra.instances.push({ name: 'app-2', id: '2', instanceStatus: 'RUNNING', currentAction: 'NONE', migHealth: 'HEALTHY' },
      { name: 'app-3', id: '3', instanceStatus: 'RUNNING', currentAction: 'NONE', migHealth: 'HEALTHY' });
    await until(() => collector.currentMig?.actualCount === 3);
    // Traffic ended 40 s ago: past the minimum observation (TTL 30 s + 5 s), far from the 1 h maximum.
    store.setGenerator(e.id, { state: 'finished', finishReason: 'completed', finishedAt: new Date(Date.now() - 40_000).toISOString() });
    await until(() => service.experiment?.state === 'cooling_down');
    await new Promise((r) => setTimeout(r, 300));
    expect(service.experiment?.state).toBe('cooling_down'); // 3 servers still up
    // The autoscaler removes them; one is still deleting: keep waiting.
    infra.targetSize = 1;
    infra.instances[1].currentAction = 'DELETING';
    infra.instances.splice(2, 1);
    await new Promise((r) => setTimeout(r, 300));
    expect(service.experiment?.state).toBe('cooling_down');
    infra.instances.splice(1, 1);
    await until(() => service.experiment?.state === 'completed', 3000);
    const events = await store.listEvents(e.id);
    expect(events.some((ev) => ev.message.includes('No more servers needed'))).toBe(true);
  });

  it('refuses a second experiment while one is active', async () => {
    await service.create(defaultExperiment(config), 'me');
    await expect(service.create(defaultExperiment(config), 'me')).rejects.toThrow(/still active/);
  });

  it('requires confirmation for expensive configurations', async () => {
    const c = defaultExperiment(config);
    c.traffic.pattern = { type: 'constant', rate: 400 };
    await expect(service.create(c, 'me')).rejects.toThrow(/confirmation/);
    await expect(service.create({ ...c, confirmExpensive: true }, 'me')).resolves.toBeTruthy();
  });

  it('will not start without a live generator and blocks inventory reset while running', async () => {
    const e = await service.create(defaultExperiment(config), 'me');
    await until(() => service.experiment?.state === 'ready');
    await expect(service.start(e.id, 'me')).rejects.toThrow(/offline/);
    heartbeat();
    await service.start(e.id, 'me');
    await expect(service.resetInventory(e.id, 'me')).rejects.toThrow(/running/);
    await service.stopTraffic(e.id, 'me');
    expect(store.commands.at(-1)?.action).toBe('stop');
    expect(service.experiment?.state).toBe('stopping');
  });

  it('raises the minimum for a larger initial count and restores it when traffic starts', async () => {
    const c = defaultExperiment(config);
    c.servers = { ...c.servers, initial: 2, min: 1 };
    infra.targetSize = 2;
    infra.instances.push({ name: 'app-2', id: '2', instanceStatus: 'RUNNING', currentAction: 'NONE', migHealth: 'HEALTHY' });
    infra.lbHealth.set('app-2', 'HEALTHY');
    const e = await service.create(c, 'me');
    await until(() => service.experiment?.state === 'ready');
    expect(infra.patches[0].minReplicas).toBe(2);
    heartbeat();
    await service.start(e.id, 'me');
    expect(infra.patches.at(-1)).toEqual({ minReplicas: 1 });
    const events = await store.listEvents(e.id);
    expect(events.filter((ev) => ev.kind === 'config').map((ev) => ev.attribution)).toEqual(['baseline', 'baseline']);
  });

  it('reset aborts the active experiment honestly and frees the lab', async () => {
    const e = await service.create(defaultExperiment(config), 'me');
    await until(() => service.experiment?.state === 'ready');
    await service.reset('me');
    expect((await store.getExperiment(e.id))?.state).toBe('failed');
    expect(service.experiment).toBeNull();
    expect(await store.activeExperimentId()).toBeNull();
  });

  it('keeps the waiting room off for experiments without it, even when it is not deployed', async () => {
    const e = await service.create(defaultExperiment(config), 'me');
    await until(() => service.experiment?.state === 'ready');
    expect(admin.prepared[0].settings.waiting_room_required).toBe(false);
    heartbeat();
    await service.start(e.id, 'me');
    expect(store.commands.at(-1)?.config?.waitingRoom).toEqual({ enabled: false });
    expect(collector.snapshot().waitingRoom).toBeNull();
    expect(collector.snapshot().freshness.waitingRoom).toMatchObject({ status: 'missing', note: expect.stringMatching(/not deployed/) });
    await service.reset('me');
    expect(admin.settings.at(-1)).toEqual({ waiting_room_required: false });
  });
});

describe('ExperimentService with the waiting room', () => {
  let store: MemoryStore; let infra: FakeInfra; let collector: Collector; let service: ExperimentService; let admin: FakeAdmin;
  let room: FakeWaitingRoom;
  const config = testConfig({ POLL_MIG_MS: '20', POLL_LB_MS: '20', POLL_CPU_MS: '50', TICK_MS: '50', POLL_WAITING_ROOM_MS: '20',
    WAITING_ROOM_URL: 'http://queue.test' });
  const sale = () => {
    const c = presets(config).find((p) => p.id === 'F')!.config;
    return { ...c, confirmExpensive: true };
  };

  beforeEach(async () => {
    store = new MemoryStore();
    infra = new FakeInfra();
    room = new FakeWaitingRoom();
    collector = new Collector(config, infra, store, new InventoryReader(null), quiet, room);
    admin = new FakeAdmin();
    service = new ExperimentService(config, store, infra, collector, admin, new InventoryReader(null), quiet,
      () => new Promise((r) => setTimeout(r, 10)), room);
    collector.start();
    await service.init();
    await until(() => collector.currentMig !== null);
  });
  afterEach(() => { service.stop(); collector.stop(); });

  it('configures the queue and the app at prepare, observes it while recording and turns both off on completion', async () => {
    const e = await service.create(sale(), 'me');
    await until(() => service.experiment?.state === 'ready');
    expect(admin.prepared[0].settings.waiting_room_required).toBe(true);
    // Preset F runs adaptive admission: 25 is each new server's starting limit, learned up to 200.
    expect(room.configs[0]).toEqual({ enabled: true, eventId: 'evt-1', activeUsersPerServer: 25, admissionTtlSeconds: 60, soldOutGraceSeconds: 35,
      admissionMode: 'adaptive', adaptive: { maxPerServer: 200 } });

    store.status = { agentId: 'gen', state: 'idle', experimentId: null, lastCommandSeq: 0, heartbeatAt: new Date().toISOString(), runStartedAt: null,
      message: null, version: '1', limits: null, targetBaseUrl: null };
    await service.start(e.id, 'me');
    expect(store.commands.at(-1)?.config?.waitingRoom).toEqual({ enabled: true });

    // Let the detector see the empty room for this event first: only transitions produce events.
    const adaptive = { admissionMode: 'adaptive' as const };
    room.current = roomStats({ ...adaptive, waiting: 0, servers: [adaptiveServer('app-1', 25)] });
    await new Promise((r) => setTimeout(r, 100));
    room.current = roomStats({ ...adaptive, waiting: 900, active: 25, servers: [adaptiveServer('app-1', 25)] });
    await until(() => collector.snapshot().waitingRoom?.waiting === 900);
    const snap = collector.snapshot();
    expect(snap.freshness.waitingRoom.status).toBe('live');
    expect(snap.waitingRoom).toMatchObject({ enabled: true, eventId: 'evt-1', active: 25, capacity: 25, admissionMode: 'adaptive', freshness: { status: 'live' } });
    expect(snap.waitingRoom?.servers[0]).toMatchObject({ name: 'app-1', limit: 25, phase: 'slow_start', signals: { busy: 0.5 } });
    // A learned limit changes capacity without a new server: no capacity event for that.
    room.current = roomStats({ ...adaptive, waiting: 880, active: 31, capacity: 31, servers: [adaptiveServer('app-1', 31)] });
    await until(() => collector.snapshot().waitingRoom?.capacity === 31);
    await new Promise((r) => setTimeout(r, 60));
    room.current = roomStats({ ...adaptive, waiting: 800, active: 50, capacity: 56, healthyServers: 2,
      servers: [adaptiveServer('app-1', 31), adaptiveServer('app-2', 25)] });
    await until(() => collector.snapshot().waitingRoom?.capacity === 56);
    await new Promise((r) => setTimeout(r, 60));

    store.setGenerator(e.id, { state: 'finished', finishReason: 'completed', finishedAt: new Date(Date.now() - 2_000_000).toISOString() });
    await until(() => service.experiment?.state === 'completed', 6000);
    expect(room.configs.at(-1)).toMatchObject({ enabled: false, eventId: 'evt-1' });
    expect(admin.settings.at(-1)).toEqual({ waiting_room_required: false });

    const events = await store.listEvents(e.id);
    const messages = events.map((ev) => ev.message);
    expect(messages).toContain('Waiting room opened: 900 customers waiting');
    expect(messages).toContain('Waiting room capacity 31 → 56 (2 healthy servers)');
    expect(messages.filter((m) => m.startsWith('Waiting room capacity'))).toHaveLength(1);
    expect(messages.some((m) => m.startsWith('Waiting room enabled: adaptive admission'))).toBe(true);
    expect(messages).toContain('Waiting room turned off (experiment completed)');
    const series = await store.readSeries(e.id);
    expect(series.waitingRoom.length).toBeGreaterThan(0);
    expect(series.waitingRoom.at(-1)).toMatchObject({ limits: { 'app-1': 31, 'app-2': 25 }, limitAvg: 28, limitMin: 25, limitMax: 31 });
    const report = (await store.getExperiment(e.id))?.report;
    expect(report?.measured.waitingRoom).toMatchObject({ enabled: true, peakWaiting: 900, peakCapacity: 56, peakHealthyServers: 2 });
    expect(report?.measured.waitingRoom?.adaptive).toEqual({
      perServer: [{ name: 'app-1', finalLimit: 31, peakLimit: 31, decreases: 0 }, { name: 'app-2', finalLimit: 25, peakLimit: 25, decreases: 0 }],
      limitAvgAtPeakCapacity: 28, decreasesTotal: 0,
    });
  });

  it('sends fixed admission for a fixed-mode room and leaves the disabling body unchanged', async () => {
    const fixed = sale();
    fixed.waitingRoom = { ...fixed.waitingRoom, mode: 'fixed' };
    await service.create(fixed, 'me');
    await until(() => service.experiment?.state === 'ready');
    expect(room.configs[0]).toEqual({ enabled: true, eventId: 'evt-1', activeUsersPerServer: 25, admissionTtlSeconds: 60, soldOutGraceSeconds: 35,
      admissionMode: 'fixed', adaptive: { maxPerServer: 200 } });
    await service.reset('me');
    expect(room.configs.at(-1)).toEqual({ enabled: false, eventId: 'evt-1', activeUsersPerServer: 25, admissionTtlSeconds: 60, soldOutGraceSeconds: 35 });
  });

  it('fails preparation when the queue cannot be configured and still turns the app requirement off', async () => {
    room.failConfigure = true;
    const e = await service.create(sale(), 'me');
    await until(() => service.experiment?.state === 'failed');
    expect((await store.getExperiment(e.id))?.failedReason).toMatch(/queue unreachable/);
    await until(() => admin.settings.length > 0);
    expect(admin.settings.at(-1)).toEqual({ waiting_room_required: false });
  });

  it('resets the queue together with the inventory', async () => {
    const e = await service.create(sale(), 'me');
    await until(() => service.experiment?.state === 'ready');
    await service.resetInventory(e.id, 'me');
    expect(room.resets).toBe(1);
  });
});
