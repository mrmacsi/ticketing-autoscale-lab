import { loadConfig, type AppConfig } from '../src/config.js';
import type { AutoscalerPatch, CpuSample, InfraProvider, MigReading, RawManagedInstance } from '../src/infra/types.js';
import { WaitingRoomClient, type WaitingRoomConfig } from '../src/ticketing/waitingRoom.js';
import type { TrafficBucket, WaitingRoomServer, WaitingRoomStats } from '../src/types.js';

export function testConfig(env: Record<string, string> = {}): AppConfig {
  return loadConfig({ AUTH_MODE: 'dev', APPROVED_MAX_INSTANCES: '5', ...env } as NodeJS.ProcessEnv);
}

/** Test double standing in for Compute Engine; only used by tests. */
export class FakeInfra implements InfraProvider {
  readonly available = true;
  targetSize = 1;
  instances: RawManagedInstance[] = [{ name: 'app-1', id: '1', instanceStatus: 'RUNNING', currentAction: 'NONE', migHealth: 'HEALTHY' }];
  autoscaler = { minReplicas: 1, maxReplicas: 5, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 240 as number | null,
    recommendedSize: 1 as number | null, status: 'ACTIVE', messages: [] as string[] };
  patches: AutoscalerPatch[] = [];
  cpu: CpuSample[] = [];
  lbHealth = new Map([['app-1', 'HEALTHY']]);

  async readMig(): Promise<MigReading> {
    return {
      targetSize: this.targetSize, isStable: true, instanceGroupUrl: 'ig',
      instances: structuredClone(this.instances), autoscaler: { ...this.autoscaler },
      details: new Map(this.instances.map((i, n) => [i.name, { name: i.name, privateIp: `10.0.0.${n + 2}`, createdAt: null, status: i.instanceStatus }])),
    };
  }
  async readBackendHealth() { return new Map(this.lbHealth); }
  async readCpu() { return [...this.cpu]; }
  async patchAutoscaler(patch: AutoscalerPatch) {
    this.patches.push(patch);
    if (patch.minReplicas !== undefined) this.autoscaler.minReplicas = patch.minReplicas;
    if (patch.maxReplicas !== undefined) this.autoscaler.maxReplicas = patch.maxReplicas;
    if (patch.cpuTarget !== undefined) this.autoscaler.cpuTarget = patch.cpuTarget;
  }
}

export function bucket(second: number, overrides: Partial<TrafficBucket> = {}): TrafficBucket {
  return {
    second, ts: new Date(Date.UTC(2026, 9, 9, 12, 0, second)).toISOString(),
    targetRate: 10, scheduledArrivals: 10, startedArrivals: 10, droppedArrivals: 0, completedJourneys: 9,
    activeVUs: 5, maxActiveVUs: 6, schedulerLagMsMax: 1,
    requests: { total: 30, ok2xx: 26, conflict409: 2, gone410: 1, payment402: 0, client4xx: 0, server5xx: 1, networkErrors: 0, timeouts: 0 },
    byEndpoint: {},
    latency: { count: 30, sumMs: 900, maxMs: 120, p50: 25, p95: 90, p99: 110, hist: { '34': 15, '45': 14, '50': 1 } },
    instances: { 'app-1': { requests: 20, errors: 1, latencySumMs: 600 }, 'app-2': { requests: 10, errors: 0, latencySumMs: 300 } },
    business: { reservationsAttempted: 5, reservationsSucceeded: 3, reservationsRejected: 2, ordersCreated: 2, paymentsAttempted: 2,
      purchasesCompleted: 2, paymentsDeclined: 0, purchasesFailed: 0, expiredDuringCheckout: 1, abandoned: 1,
      duplicateSubmissions: 0, idempotentReplays: 0, ticketsPurchased: 3 },
    synthetic: { requests: 0, cpuMsRequested: 0 },
    ...overrides,
  };
}

export function roomStats(overrides: Partial<WaitingRoomStats> = {}): WaitingRoomStats {
  return {
    enabled: true, eventId: 'evt-1', soldOut: false,
    waiting: 0, active: 0, capacity: 25, healthyServers: 1, servers: [roomServer('app-1')],
    activeUsersPerServer: 25,
    admissionMode: null, adaptive: null,
    joinedTotal: 0, admittedTotal: 0, releasedTotal: 0, expiredTotal: 0, soldOutTotal: 0, abandonedTotal: 0, store: 'memory', leaderInstance: null,
    admissionRatePerSecond: 0,
    waitMs: { p50: null, p95: null, max: null, count: 0 },
    oldestWaitingSeconds: null,
    inventory: { available: 2000, soldOut: false, observedAt: '2026-10-09T12:00:00.000Z' },
    capacitySource: 'gce', capacityError: null,
    updatedAt: '2026-10-09T12:00:00.000Z',
    ...overrides,
  };
}

/** A healthy server as the queue reports it; fixed-mode/older-queue shape unless overridden. */
export function roomServer(name: string, overrides: Partial<WaitingRoomServer> = {}): WaitingRoomServer {
  return {
    name, ip: '10.0.0.2', limit: null, phase: null, decision: null, reason: null, decisionAt: null, decreases: null, signals: null,
    ...overrides,
  };
}

/** Adaptive-mode server with fresh signals. */
export function adaptiveServer(name: string, limit: number, overrides: Partial<WaitingRoomServer> = {}): WaitingRoomServer {
  return roomServer(name, {
    limit, phase: 'slow_start', decision: 'increase', reason: 'headroom', decisionAt: '2026-10-10T17:22:01.000Z', decreases: 0,
    signals: { busy: 0.5, activeWorkers: 16, totalWorkers: 32, listenQueue: 0, cpu: 0.6, observedAt: '2026-10-10T17:22:01.000Z' },
    ...overrides,
  });
}

/** Test double for the queue server's private API. */
export class FakeWaitingRoom extends WaitingRoomClient {
  configs: WaitingRoomConfig[] = [];
  resets = 0;
  current: WaitingRoomStats = roomStats();
  failConfigure = false;
  constructor() { super('http://queue.test', 'token'); }
  override async configure(config: WaitingRoomConfig) {
    if (this.failConfigure) throw new Error('queue unreachable');
    this.configs.push(config);
    return { ...config };
  }
  override async reset() { this.resets += 1; return {}; }
  override async stats() { return structuredClone(this.current); }
}
