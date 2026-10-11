import { describe, expect, it } from 'vitest';
import { defaultExperiment } from '../src/experiments/presets.js';
import { buildReport, timeseriesCsv } from '../src/report/report.js';
import type { TrafficBucket, WaitingRoomPoint } from '../src/types.js';
import type { Experiment } from '../src/types.js';
import { bucket, testConfig } from './fakes.js';

const config = testConfig();
const experiment: Experiment = {
  id: 'exp_1', name: 'Test', state: 'completed', config: defaultExperiment(config), createdBy: 'me', createdAt: '2026-10-09T12:00:00.000Z',
  readyAt: null, trafficStartedAt: '2026-10-09T12:00:00.000Z', trafficStoppedAt: null, completedAt: '2026-10-09T12:10:00.000Z',
  failedReason: null, eventId: 'evt', categories: [], generator: null, initialServerCount: 1, report: null,
};

describe('report', () => {
  const series = {
    scaling: [{ t: Date.UTC(2026, 9, 9, 12, 0, 0), actual: 1, running: 1, healthy: 1, target: 1, min: 1, max: 5 },
      { t: Date.UTC(2026, 9, 9, 12, 0, 1), actual: 3, running: 3, healthy: 2, target: 3, min: 1, max: 5 }],
    cpu: [{ t: Date.UTC(2026, 9, 9, 12, 0, 0), instance: 'app-1', value: 0.82 }, { t: Date.UTC(2026, 9, 9, 12, 0, 0), instance: '__avg__', value: 0.7 }],
    inventory: [],
    waitingRoom: [] as WaitingRoomPoint[],
  };
  const inventory = { observedAt: '', eventId: 'evt', capacity: 100, available: 10, held: 5, sold: 85, activeReservations: 3,
    paymentProcessing: 0, expiredReservations: 7, ticketsIssued: 85, invariantViolations: 0, categories: [] };

  it('aggregates measurements and keeps targets and estimates separate', () => {
    const r = buildReport({ experiment, buckets: [bucket(0), bucket(1, { droppedArrivals: 3 })], series, events: [], finalInventory: inventory }, config);
    expect(r.measured).toMatchObject({
      arrivalsRequested: 20, arrivalsStarted: 20, arrivalsDropped: 3, httpRequests: 60, peakRps: 30, httpErrorPct: 3.33,
      businessRejectionPct: 10, initialServerCount: 1, maxObservedServerCount: 3, finalServerCount: 3, ticketsSold: 85,
      ticketsRemaining: 10, invariantViolations: 0, requestsPerInstance: { 'app-1': 40, 'app-2': 20 },
    });
    expect(r.measured.peakCpu).toMatchObject({ value: 0.82, instance: 'app-1' });
    expect(r.configured.cpuTarget).toBe(0.6);
    expect(r.estimated.infrastructureCostUsd).toBeGreaterThan(0);
    expect(r.generatorCapacityIssues[0]).toMatch(/3 scheduled arrivals were dropped/);
    expect(r.measured.waitingRoom).toBeNull();   // disabled for this experiment
  });

  const queue = (o: Partial<NonNullable<TrafficBucket['queue']>> = {}): NonNullable<TrafficBucket['queue']> => ({
    joined: 40, admitted: 25, soldOut: 0, open: 0, failed: 0, waiting: 500,
    waitMs: { count: 25, maxMs: 12_000, p50: null, p95: null, p99: null, hist: { '80': 20, '95': 5 } },
    requests: { total: 100, ok: 100, errors: 0 }, ...o,
  });
  const point = (s: number, o: Partial<WaitingRoomPoint> = {}): WaitingRoomPoint => ({
    t: Date.UTC(2026, 9, 9, 12, 0, s), waiting: 400, active: 25, capacity: 25, healthyServers: 1, admissionRate: 20,
    joinedTotal: 100, admittedTotal: 50, soldOutTotal: 0, soldOut: false, waitP50Ms: 4000, waitP95Ms: 9000, waitMaxMs: 9500, ...o,
  });
  const withRoom: Experiment = { ...experiment, config: { ...experiment.config,
    waitingRoom: { enabled: true, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200 } } };
  const adaptiveRoom: Experiment = { ...withRoom, config: { ...withRoom.config, waitingRoom: { ...withRoom.config.waitingRoom, mode: 'adaptive' } } };

  it('reports the waiting room from queue-server counters and the generator wait histogram', () => {
    const r = buildReport({ experiment: withRoom, buckets: [bucket(0, { queue: queue() }), bucket(1, { queue: queue({ waiting: 1830, waitMs: { count: 1, maxMs: 41_000, p50: null, p95: null, p99: null, hist: { '110': 1 } } }) })],
      series: { ...series, waitingRoom: [point(0), point(1, { waiting: 1200, capacity: 75, healthyServers: 3, joinedTotal: 5000, admittedTotal: 2300, soldOutTotal: 2700 })] },
      events: [], finalInventory: null }, config);
    expect(r.measured.waitingRoom).toMatchObject({ enabled: true, joined: 5000, admitted: 2300, soldOutInQueue: 2700, peakWaiting: 1830,
      peakCapacity: 75, peakHealthyServers: 3 });
    expect(r.measured.waitingRoom?.waitMs.p50).toBeCloseTo(Math.pow(1.1, 80), 0);
    expect(r.measured.waitingRoom?.waitMs.max).toBe(41_000);
    expect(r.measured.waitingRoom?.adaptive).toBeNull();   // fixed mode
  });

  it('reports learned limits per server in adaptive mode', () => {
    const points = [
      point(0, { capacity: 25, healthyServers: 1, limits: { a: 25 }, decreases: { a: 0 }, limitAvg: 25, limitMin: 25, limitMax: 25 }),
      point(1, { capacity: 60, healthyServers: 1, limits: { a: 60 }, decreases: { a: 0 }, limitAvg: 60, limitMin: 60, limitMax: 60 }),
      point(2, { capacity: 70, healthyServers: 2, limits: { a: 45, b: 25 }, decreases: { a: 1, b: 0 }, limitAvg: 35, limitMin: 25, limitMax: 45 }),
      // b left and came back: its counter restarted at 0 and then counted one more cut.
      point(3, { capacity: 46, healthyServers: 1, limits: { a: 46 }, decreases: { a: 1 }, limitAvg: 46, limitMin: 46, limitMax: 46 }),
      point(4, { capacity: 65, healthyServers: 2, limits: { a: 47, b: 18 }, decreases: { a: 1, b: 1 }, limitAvg: 32.5, limitMin: 18, limitMax: 47 }),
      point(5, { capacity: 48, healthyServers: 2, limits: { a: 34, b: 14 }, decreases: { a: 2, b: 2 }, limitAvg: 24, limitMin: 14, limitMax: 34 }),
    ];
    const r = buildReport({ experiment: adaptiveRoom, buckets: [bucket(0)], series: { ...series, waitingRoom: points }, events: [], finalInventory: null }, config);
    expect(r.measured.waitingRoom?.adaptive).toEqual({
      perServer: [{ name: 'a', finalLimit: 34, peakLimit: 60, decreases: 2 }, { name: 'b', finalLimit: 14, peakLimit: 25, decreases: 2 }],
      limitAvgAtPeakCapacity: 35,
      decreasesTotal: 4,
    });
    const nothing = buildReport({ experiment: adaptiveRoom, buckets: [bucket(0)], series, events: [], finalInventory: null }, config);
    expect(nothing.measured.waitingRoom?.adaptive).toEqual({ perServer: [], limitAvgAtPeakCapacity: null, decreasesTotal: 0 });
  });

  it('falls back to generator sums and stats percentiles, and stays null when nothing was measured', () => {
    const generatorOnly = buildReport({ experiment: withRoom, buckets: [bucket(0, { queue: queue({ soldOut: 3 }) }), bucket(1, { queue: queue() })],
      series, events: [], finalInventory: null }, config);
    expect(generatorOnly.measured.waitingRoom).toMatchObject({ joined: 80, admitted: 50, soldOutInQueue: 3, peakWaiting: 500, peakCapacity: null });
    const statsOnly = buildReport({ experiment: withRoom, buckets: [bucket(0)], series: { ...series, waitingRoom: [point(0)] }, events: [], finalInventory: null }, config);
    expect(statsOnly.measured.waitingRoom?.waitMs).toEqual({ p50: 4000, p95: 9000, max: 9500 });
    const nothing = buildReport({ experiment: withRoom, buckets: [bucket(0)], series, events: [], finalInventory: null }, config);
    expect(nothing.measured.waitingRoom).toMatchObject({ joined: null, admitted: null, peakWaiting: null, waitMs: { p50: null, p95: null, max: null } });
  });

  it('exports one CSV row per second joined with infrastructure state', () => {
    const csv = timeseriesCsv([bucket(0), bucket(1)], series).trim().split('\n');
    expect(csv).toHaveLength(3);
    expect(csv[0].split(',')[0]).toBe('second');
    expect(csv[2].split(',')[19]).toBe('3');
  });

  it('adds waiting-room columns, empty when the room was not observed', () => {
    const header = timeseriesCsv([], series).trim().split(',');
    for (const c of ['queue_waiting', 'queue_active', 'queue_capacity', 'queue_admission_rate_per_s', 'queue_healthy_servers', 'queue_joined', 'queue_wait_p95_ms']) {
      expect(header).toContain(c);
    }
    const rows = timeseriesCsv([bucket(0), bucket(1, { queue: queue() })], { ...series, waitingRoom: [point(1, { capacity: 50, healthyServers: 2 })] }).trim().split('\n');
    const col = (row: string, name: string) => row.split(',')[header.indexOf(name)];
    expect(col(rows[1], 'queue_waiting')).toBe('');
    expect(col(rows[1], 'queue_joined')).toBe('');
    expect(col(rows[2], 'queue_waiting')).toBe('400');
    expect(col(rows[2], 'queue_capacity')).toBe('50');
    expect(col(rows[2], 'queue_healthy_servers')).toBe('2');
    expect(col(rows[2], 'queue_admission_rate_per_s')).toBe('20');
    expect(col(rows[2], 'queue_joined')).toBe('40');
    expect(col(rows[2], 'queue_limit_avg')).toBe('');   // fixed mode / not observed
  });

  it('exports the average, min and max learned limit in adaptive mode', () => {
    const header = timeseriesCsv([], series).trim().split(',');
    expect(header.slice(-3)).toEqual(['queue_limit_avg', 'queue_limit_min', 'queue_limit_max']);
    const rows = timeseriesCsv([bucket(1)], { ...series, waitingRoom: [point(1, { limits: { a: 30, b: 45 }, limitAvg: 37.5, limitMin: 30, limitMax: 45 })] })
      .trim().split('\n');
    const col = (name: string) => rows[1].split(',')[header.indexOf(name)];
    expect([col('queue_limit_avg'), col('queue_limit_min'), col('queue_limit_max')]).toEqual(['37.5', '30', '45']);
  });
});
