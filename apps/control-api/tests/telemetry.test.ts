import { describe, expect, it } from 'vitest';
import { adaptiveLimits } from '../src/telemetry/collector.js';
import { EventDetector, shortServerName } from '../src/telemetry/detector.js';
import { cpuFreshness, freshness } from '../src/telemetry/freshness.js';
import { instancePhase } from '../src/telemetry/phase.js';
import { instanceTraffic, mergeHistograms, percentileFromHistogram, toTrafficPoint } from '../src/telemetry/traffic.js';
import { normalizeStats } from '../src/ticketing/waitingRoom.js';
import type { InstanceView, MigView } from '../src/types.js';
import { adaptiveServer, bucket, roomServer, roomStats, testConfig } from './fakes.js';

const mig = (targetSize: number, min = 1, max = 5): MigView => ({
  observedAt: '', name: 'mig', zone: 'z', targetSize, actualCount: targetSize, runningCount: targetSize, healthyCount: targetSize, isStable: true,
  autoscaler: { minReplicas: min, maxReplicas: max, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 240, recommendedSize: targetSize, status: 'ACTIVE', messages: [] },
  baseline: { minReplicas: 1, maxReplicas: 5, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 240 },
  overridesActive: false, approvedMaxInstances: 5,
});
const inst = (name: string, o: Partial<InstanceView> = {}): InstanceView => ({
  name, id: name, zone: 'z', privateIp: null, createdAt: null, vmStatus: 'RUNNING', currentAction: 'NONE', migHealth: 'HEALTHY',
  lbHealth: 'HEALTHY', phase: 'healthy', cpu: { value: null, sampleTime: null, source: 'cloud_monitoring' }, guestCpu: null,
  traffic: { rps: 0, avgLatencyMs: null, errorRate: null, windowSeconds: 5 }, appVersion: null, ready: null, ...o,
});

describe('EventDetector', () => {
  it('records the starting state once and invents nothing when nothing changes', () => {
    const d = new EventDetector();
    const first = d.observeMig(mig(1), [inst('a')]);
    expect(first).toHaveLength(1);
    expect(first[0].message).toContain('Observed 1 instance');
    expect(d.observeMig(mig(1), [inst('a')])).toEqual([]);
    expect(d.observeMig(mig(1), [inst('a')])).toEqual([]);
  });

  it('attributes target growth to the autoscaler unless the control plane raised the minimum', () => {
    const d = new EventDetector();
    d.observeMig(mig(1), [inst('a')]);
    const scaled = d.observeMig(mig(2), [inst('a'), inst('b', { vmStatus: 'PROVISIONING', currentAction: 'CREATING', migHealth: null, lbHealth: null, phase: 'starting' })]);
    expect(scaled.find((e) => e.kind === 'scaling' && e.message.includes('1 → 2'))?.attribution).toBe('autoscaler');
    expect(scaled.find((e) => e.message.includes('b created'))?.attribution).toBe('autoscaler');

    const d2 = new EventDetector();
    d2.observeMig(mig(1), [inst('a')]);
    d2.noteOperatorChange({ minReplicas: 3, reason: 'Baseline' });
    const baseline = d2.observeMig(mig(3, 3), [inst('a')]);
    expect(baseline.find((e) => e.kind === 'scaling')?.attribution).toBe('baseline');
  });

  it('reports each lifecycle milestone exactly once, in order of observation', () => {
    const d = new EventDetector();
    d.observeMig(mig(1), [inst('a')]);
    d.observeMig(mig(2), [inst('a'), inst('b', { vmStatus: 'STAGING', currentAction: 'CREATING', migHealth: null, lbHealth: null })]);
    const running = d.observeMig(mig(2), [inst('a'), inst('b', { currentAction: 'VERIFYING', migHealth: null, lbHealth: null })]);
    expect(running.map((e) => e.message)).toEqual(['b VM is RUNNING (booted)']);
    const healthy = d.observeMig(mig(2), [inst('a'), inst('b', { traffic: { rps: 4, avgLatencyMs: 20, errorRate: 0, windowSeconds: 5 } })]);
    expect(healthy.map((e) => e.message)).toEqual([
      'b passed the MIG health check', 'b became HEALTHY in the load-balancer backend', 'b served its first synthetic requests',
    ]);
    expect(d.observeMig(mig(2), [inst('a'), inst('b')])).toEqual([]);
    const gone = d.observeMig(mig(1), [inst('a')]);
    expect(gone.map((e) => e.message)).toContain('b removed from the MIG (terminated)');
  });

  it('reports CPU target crossings with the Cloud Monitoring sample time, never re-reporting old samples', () => {
    const d = new EventDetector();
    const t0 = Date.UTC(2026, 9, 9, 12, 0, 0);
    expect(d.observeCpu(0.3, t0, 0.6)).toEqual([]);
    const crossed = d.observeCpu(0.75, t0 + 60_000, 0.6);
    expect(crossed[0].message).toBe('Average CPU 75% exceeded the 60% target (sample at 12:01:00 UTC)');
    expect(d.observeCpu(0.2, t0 + 60_000, 0.6)).toEqual([]);
  });

  it('flags sold-out inventory and invariant violations from database observations', () => {
    const d = new EventDetector();
    const view = { observedAt: '', eventId: 'e', capacity: 100, available: 0, held: 0, sold: 100, activeReservations: 0,
      paymentProcessing: 0, expiredReservations: 0, ticketsIssued: 100, invariantViolations: 0, categories: [] };
    expect(d.observeInventory(view).map((e) => e.message)).toEqual(['Inventory sold out (100/100 sold)']);
    expect(d.observeInventory(view)).toEqual([]);
    expect(d.observeInventory({ ...view, invariantViolations: 2 })[0].kind).toBe('error');
  });
});

describe('waiting-room events', () => {
  it('reports only transitions between successive observations', () => {
    const d = new EventDetector();
    // First observation is a starting state, even with people already waiting (e.g. after a restart).
    expect(d.observeWaitingRoom(roomStats({ waiting: 40 }))).toEqual([]);
    expect(d.observeWaitingRoom(roomStats({ waiting: 60 }))).toEqual([]);

    const d2 = new EventDetector();
    d2.observeWaitingRoom(roomStats());
    const opened = d2.observeWaitingRoom(roomStats({ waiting: 1240, active: 25 }));
    expect(opened.map((e) => e.message)).toEqual(['Waiting room opened: 1,240 customers waiting']);
    expect(opened[0]).toMatchObject({ kind: 'waiting_room', source: 'waiting_room' });
    expect(d2.observeWaitingRoom(roomStats({ waiting: 1500 }))).toEqual([]);

    const grown = d2.observeWaitingRoom(roomStats({ waiting: 1400, capacity: 50, healthyServers: 2 }));
    expect(grown.map((e) => e.message)).toEqual(['Waiting room capacity 25 → 50 (2 healthy servers)']);
    expect(grown[0].data).toMatchObject({ from: 25, to: 50, healthyServers: 2 });
    expect(d2.observeWaitingRoom(roomStats({ waiting: 1300, capacity: 25, healthyServers: 1 }))[0].message)
      .toBe('Waiting room capacity 50 → 25 (1 healthy server)');
  });

  it('declares empty only after several observations with nobody waiting', () => {
    const d = new EventDetector();
    d.observeWaitingRoom(roomStats());
    d.observeWaitingRoom(roomStats({ waiting: 3 }));
    expect(d.observeWaitingRoom(roomStats({ waiting: 0 }))).toEqual([]);
    expect(d.observeWaitingRoom(roomStats({ waiting: 1 }))).toEqual([]);   // flap: still open, no new "opened"
    expect(d.observeWaitingRoom(roomStats({ waiting: 0 }))).toEqual([]);
    expect(d.observeWaitingRoom(roomStats({ waiting: 0 }))).toEqual([]);
    const empty = d.observeWaitingRoom(roomStats({ waiting: 0 }));
    expect(empty.map((e) => e.message)).toEqual(['Waiting room empty']);
    expect(d.observeWaitingRoom(roomStats({ waiting: 0 }))).toEqual([]);
    expect(d.observeWaitingRoom(roomStats({ waiting: 2 })).map((e) => e.message)).toEqual(['Waiting room opened: 2 customers waiting']);
  });

  it('reports the sold-out declaration once with the number of customers told', () => {
    const d = new EventDetector();
    d.observeWaitingRoom(roomStats());
    d.observeWaitingRoom(roomStats({ waiting: 2900 }));
    const sold = d.observeWaitingRoom(roomStats({ waiting: 0, soldOut: true, soldOutTotal: 2950 }));
    expect(sold.map((e) => e.message)).toEqual(['Waiting room declared sold out: 2,950 customers told']);
    expect(d.observeWaitingRoom(roomStats({ waiting: 0, soldOut: true, soldOutTotal: 2990 }))).toEqual([]);

    const d2 = new EventDetector();
    d2.observeWaitingRoom(roomStats({ waiting: 7 }));
    expect(d2.observeWaitingRoom(roomStats({ soldOut: true }))[0].message).toBe('Waiting room declared sold out: 7 customers told');
  });
});

describe('adaptive waiting-room events', () => {
  const A = 'ticketing-scale-lab-app-ab12';
  const B = 'ticketing-scale-lab-app-cd34';
  const clock = () => {
    let t = Date.UTC(2026, 9, 10, 17, 0, 0);
    return { now: () => t, advance: (ms: number) => { t += ms; } };
  };
  const adaptive = (servers: ReturnType<typeof adaptiveServer>[], o: Parameters<typeof roomStats>[0] = {}) => roomStats({
    admissionMode: 'adaptive', waiting: 500, servers, healthyServers: servers.length,
    capacity: servers.reduce((sum, s) => sum + (s.limit ?? 0), 0), ...o,
  });

  it('does not report every learned capacity change, only changes in the number of healthy servers', () => {
    const c = clock();
    const d = new EventDetector(c.now);
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 25)]));
    for (const limit of [31, 39, 48, 60]) {
      c.advance(1000);
      expect(d.observeWaitingRoom(adaptive([adaptiveServer(A, limit)]))).toEqual([]);
    }
    c.advance(1000);
    const grown = d.observeWaitingRoom(adaptive([adaptiveServer(A, 61), adaptiveServer(B, 25)]));
    expect(grown.map((e) => e.message)).toEqual(['Waiting room capacity 60 → 86 (2 healthy servers)']);
    expect(grown[0]).toMatchObject({ kind: 'waiting_room', source: 'waiting_room', data: { admissionMode: 'adaptive', healthyServers: 2 } });
    c.advance(1000);
    expect(d.observeWaitingRoom(adaptive([adaptiveServer(A, 62), adaptiveServer(B, 31)]))).toEqual([]);
    c.advance(1000);
    expect(d.observeWaitingRoom(adaptive([adaptiveServer(A, 63)])).map((e) => e.message))
      .toEqual(['Waiting room capacity 93 → 63 (1 healthy server)']);
  });

  it('reports a cut of the shared limit once (not once per server), at most once per 10 s', () => {
    const c = clock();
    const d = new EventDetector(c.now);
    const both = (limit: number, decreases: number, reason = 'headroom') => adaptive([A, B].map((name) =>
      adaptiveServer(name, limit, { phase: 'steady', decreases, decision: reason === 'headroom' ? 'increase' : 'decrease', reason })));
    d.observeWaitingRoom(both(80, 0));
    c.advance(1000);
    const cut = d.observeWaitingRoom(both(60, 1, 'listen queue 4 on ab12'));
    expect(cut.map((e) => e.message)).toEqual(['Adaptive limit cut 80 → 60 per server (listen queue 4 on ab12)']);
    expect(cut[0]).toMatchObject({ kind: 'waiting_room', source: 'waiting_room', data: { from: 80, to: 60, reason: 'listen queue 4 on ab12' } });
    // Another cut 4 s later is real but not reported again yet.
    c.advance(4000);
    expect(d.observeWaitingRoom(both(45, 2, 'workers busy 95% on cd34'))).toEqual([]);
    c.advance(6000); // 10 s after the first reported cut
    const again = d.observeWaitingRoom(both(34, 3, 'CPU 93% on ab12'));
    expect(again.map((e) => e.message)).toEqual(['Adaptive limit cut 45 → 34 per server (CPU 93% on ab12)']);
    expect(again[0].data).toMatchObject({ cutsNotReportedBefore: 1 });
    // Holds and increases are never events.
    c.advance(1000);
    expect(d.observeWaitingRoom(both(35, 3))).toEqual([]);
  });

  it('falls back to a falling limit with a decrease decision when the queue reports no cut counter', () => {
    const d = new EventDetector();
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 40, { phase: 'steady', decreases: null })]));
    expect(d.observeWaitingRoom(adaptive([adaptiveServer(A, 30, { phase: 'steady', decreases: null, decision: 'decrease', reason: 'busy 92%' })]))
      .map((e) => e.message)).toEqual(['Adaptive limit cut 40 → 30 per server (busy 92%)']);
  });

  it('reports leaving slow start once, at the limit slow start had reached', () => {
    const c = clock();
    const d = new EventDetector(c.now);
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 25)]));
    c.advance(1000);
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 49)]));
    c.advance(1000);
    const left = d.observeWaitingRoom(adaptive([adaptiveServer(A, 36, { phase: 'steady', decision: 'decrease', decreases: 1, reason: 'listen queue 3' })]));
    expect(left.map((e) => e.message)).toEqual(['Adaptive limit cut 49 → 36 per server (listen queue 3)', 'Adaptive limit left slow start at 49 per server']);
    c.advance(20_000);
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 40, { decreases: 1 })]));
    c.advance(1000);
    expect(d.observeWaitingRoom(adaptive([adaptiveServer(A, 41, { phase: 'steady', decreases: 1 })]))).toEqual([]);
  });

  it('treats a server seen for the first time as a starting state, and fixed mode as before', () => {
    const d = new EventDetector();
    d.observeWaitingRoom(adaptive([adaptiveServer(A, 40)]));
    // B appears already cut and steady: nothing to report about B, only the new healthy server.
    const appeared = d.observeWaitingRoom(adaptive([adaptiveServer(A, 40), adaptiveServer(B, 20, { phase: 'steady', decreases: 3 })]));
    expect(appeared.map((e) => e.message)).toEqual(['Waiting room capacity 40 → 60 (2 healthy servers)']);

    const fixed = new EventDetector();
    fixed.observeWaitingRoom(roomStats({ admissionMode: 'fixed', servers: [roomServer(A, { limit: 25, phase: 'fixed' })] }));
    expect(fixed.observeWaitingRoom(roomStats({ admissionMode: 'fixed', capacity: 50, healthyServers: 2 }))[0].message)
      .toBe('Waiting room capacity 25 → 50 (2 healthy servers)');
  });

  it('shortens MIG instance names for event messages', () => {
    expect(shortServerName('ticketing-scale-lab-app-ab12')).toBe('app-ab12');
    expect(shortServerName('app-1')).toBe('app-1');
  });
});

describe('adaptive series points', () => {
  it('records per-server limits with average, min and max only in adaptive mode', () => {
    const stats = roomStats({ admissionMode: 'adaptive', servers: [adaptiveServer('a', 30, { decreases: 2 }), adaptiveServer('b', 45), roomServer('c')] });
    expect(adaptiveLimits(stats)).toEqual({ limits: { a: 30, b: 45 }, decreases: { a: 2, b: 0 }, limitAvg: 37.5, limitMin: 30, limitMax: 45 });
    const none = { limits: null, decreases: null, limitAvg: null, limitMin: null, limitMax: null };
    expect(adaptiveLimits(roomStats({ admissionMode: 'fixed', servers: [roomServer('a', { limit: 25, phase: 'fixed' })] }))).toEqual(none);
    expect(adaptiveLimits(roomStats())).toEqual(none);
    expect(adaptiveLimits(roomStats({ admissionMode: 'adaptive', servers: [roomServer('a')] }))).toEqual(none);
  });
});

describe('waiting-room stats', () => {
  it('normalises the queue server response and keeps unknown gauges null', () => {
    const s = normalizeStats({ enabled: true, eventId: 'e', waiting: 3110, active: 75, capacity: 75, healthyServers: 3,
      joinedTotal: 4200, admittedTotal: 1090, admissionRatePerSecond: 24.5, waitMs: { p50: 8000, p95: 41000, max: 52000, count: 1090 },
      oldestWaitingSeconds: 48.2, inventory: null, capacitySource: 'gce', capacityError: null, updatedAt: '2026-10-10T15:00:00.000Z' });
    expect(s).toMatchObject({ enabled: true, waiting: 3110, active: 75, capacity: 75, healthyServers: 3, soldOut: false, servers: [],
      waitMs: { p50: 8000, p95: 41000, max: 52000, count: 1090 }, oldestWaitingSeconds: 48.2, inventory: null });
    expect(normalizeStats({ enabled: false, waitMs: {} })).toMatchObject({ waitMs: { p50: null, p95: null, max: null }, oldestWaitingSeconds: null });
    expect(() => normalizeStats(undefined)).toThrow(/no data/);
  });

  it('normalises adaptive fields and leaves them null for an older queue (never 0)', () => {
    const s = normalizeStats({ enabled: true, admissionMode: 'adaptive',
      adaptive: { minPerServer: 5, maxPerServer: 200, busyHigh: 0.9, busyLow: 0.7, cpuHigh: 0.9, cpuLow: 0.8, decreaseFactor: 0.75, slowStartFactor: 1.25, cooldownSeconds: 3 },
      servers: [
        { name: 'app-1', ip: '10.0.0.2', limit: 31, phase: 'steady', decision: 'decrease', reason: 'listen queue 4', decisionAt: '2026-10-10T17:22:01Z', decreases: 3,
          signals: { busy: 0.81, activeWorkers: 26, totalWorkers: 32, listenQueue: 4, cpu: 0.74, observedAt: '2026-10-10T17:22:01Z' } },
        { name: 'app-2', ip: '10.0.0.3', limit: 25, phase: 'slow_start', decision: 'hold', reason: 'no signal', signals: null },
      ] });
    expect(s.admissionMode).toBe('adaptive');
    expect(s.adaptive).toMatchObject({ minPerServer: 5, maxPerServer: 200, cooldownSeconds: 3 });
    expect(s.servers[0]).toEqual({ name: 'app-1', ip: '10.0.0.2', limit: 31, phase: 'steady', decision: 'decrease', reason: 'listen queue 4',
      decisionAt: '2026-10-10T17:22:01Z', decreases: 3,
      signals: { busy: 0.81, activeWorkers: 26, totalWorkers: 32, listenQueue: 4, cpu: 0.74, observedAt: '2026-10-10T17:22:01Z' } });
    expect(s.servers[1]).toMatchObject({ decreases: null, signals: null, decisionAt: null });
    const partial = normalizeStats({ servers: [{ name: 'x', ip: 'y', signals: { busy: 0.5 } }] });
    expect(partial.servers[0].signals).toEqual({ busy: 0.5, activeWorkers: null, totalWorkers: null, listenQueue: null, cpu: null, observedAt: null });
    const old = normalizeStats({ enabled: true, servers: [{ name: 'app-1', ip: '10.0.0.2' }] });
    expect(old).toMatchObject({ admissionMode: null, adaptive: null });
    expect(old.servers[0]).toEqual({ name: 'app-1', ip: '10.0.0.2', limit: null, phase: null, decision: null, reason: null, decisionAt: null, decreases: null, signals: null });
    expect(normalizeStats({ admissionMode: 'turbo', servers: [{ name: 'a', phase: 'warp', decision: 'maybe' }] }))
      .toMatchObject({ admissionMode: null, servers: [{ phase: null, decision: null }] });
  });

  it('reads WAITING_ROOM_URL without a trailing slash and treats empty as not deployed', () => {
    expect(testConfig({ WAITING_ROOM_URL: 'http://10.60.0.20:8080/' }).waitingRoom.baseUrl).toBe('http://10.60.0.20:8080');
    expect(testConfig({ WAITING_ROOM_URL: '' }).waitingRoom.baseUrl).toBeNull();
    expect(testConfig().intervals.waitingRoomMs).toBe(1000);
  });
});

describe('traffic aggregation', () => {
  it('aggregates the generator queue object and leaves it null for older generators', () => {
    expect(toTrafficPoint(bucket(1))).toMatchObject({ queueJoined: null, queueAdmitted: null, queueSoldOut: null, queueWaiting: null, queueWaitP95: null });
    const withQueue = bucket(2, { queue: { joined: 40, admitted: 25, soldOut: 0, open: 0, failed: 0, waiting: 1830,
      waitMs: { count: 25, maxMs: 9000, p50: null, p95: null, p99: null, hist: { '90': 20, '95': 5 } }, requests: { total: 130, ok: 130, errors: 0 } } });
    const p = toTrafficPoint(withQueue);
    expect(p).toMatchObject({ queueJoined: 40, queueAdmitted: 25, queueSoldOut: 0, queueWaiting: 1830 });
    expect(p.queueWaitP95).toBeCloseTo(Math.pow(1.1, 95), 0);
    // Queue calls are not application requests.
    expect(p.rps).toBe(30);
  });

  it('separates business rejections from HTTP errors', () => {
    const p = toTrafficPoint(bucket(3));
    expect(p).toMatchObject({ second: 3, rps: 30, ok: 26, business: 3, errors: 1, rejectedSoldOut: 2, achieved: 10 });
  });

  it('computes percentiles from merged log histograms', () => {
    const merged = mergeHistograms([bucket(0), bucket(1)]);
    expect(merged).toEqual({ '34': 30, '45': 28, '50': 2 });
    expect(percentileFromHistogram(merged, 0.5)).toBeCloseTo(Math.pow(1.1, 34), 1);
    expect(percentileFromHistogram(merged, 0.99)).toBeCloseTo(Math.pow(1.1, 50), 1);
    expect(percentileFromHistogram({}, 0.5)).toBeNull();
  });

  it('derives per-instance request distribution from X-Served-By counts', () => {
    const stats = instanceTraffic([bucket(0), bucket(1)], 5);
    expect(stats.get('app-1')).toEqual({ rps: 20, avgLatencyMs: 30, errorRate: 0.05, windowSeconds: 2 });
    expect(stats.get('app-2')?.rps).toBe(10);
  });
});

describe('instance phase and freshness', () => {
  it('maps Compute Engine states to card phases', () => {
    expect(instancePhase('PROVISIONING', 'CREATING', null, null)).toBe('starting');
    expect(instancePhase('RUNNING', 'VERIFYING', null, null)).toBe('starting');
    expect(instancePhase('RUNNING', 'NONE', 'HEALTHY', 'HEALTHY')).toBe('healthy');
    expect(instancePhase('RUNNING', 'NONE', 'UNHEALTHY', 'UNHEALTHY')).toBe('unhealthy');
    expect(instancePhase('RUNNING', 'DELETING', 'HEALTHY', 'HEALTHY')).toBe('terminating');
    expect(instancePhase('RUNNING', 'NONE', 'HEALTHY', 'DRAINING')).toBe('draining');
  });

  it('never labels Cloud Monitoring CPU as live', () => {
    const now = Date.now();
    expect(cpuFreshness(now - 90_000, now).status).toBe('delayed');
    expect(cpuFreshness(now - 400_000, now).status).toBe('stale');
    expect(cpuFreshness(null, now).status).toBe('missing');
    expect(freshness(now - 1000, now, 3, 15).status).toBe('live');
    expect(freshness(now - 20_000, now, 3, 15).status).toBe('stale');
  });
});
