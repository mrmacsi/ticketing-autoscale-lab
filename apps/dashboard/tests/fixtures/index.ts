// Test-only fixtures. Production code never imports from tests/.
import type {
  Experiment,
  ExperimentConfig,
  Freshness,
  InstanceView,
  LiveSnapshot,
  Meta,
  Series,
  TimelineEvent,
  TrafficPoint,
  WaitingRoomPoint,
  WaitingRoomServer,
  WaitingRoomView,
} from '@/types/contracts';

export const SERVER_TIME = '2026-10-09T15:40:00.000Z';
export const T0 = Date.parse('2026-10-09T15:30:00.000Z');

export const live = (lastAt = SERVER_TIME): Freshness => ({ status: 'live', lastAt, ageSeconds: 1 });

export function makeConfig(overrides: Partial<ExperimentConfig> = {}): ExperimentConfig {
  return {
    name: 'Scenario B – increasing traffic',
    preset: null,
    servers: { initial: 1, min: 1, max: 5, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 240 },
    tickets: {
      eventName: 'Lab Arena Night',
      totalInventory: 100000,
      categoryCount: 3,
      pricesCents: [4500, 8000, 15000],
      reservationTtlSeconds: 120,
      paymentDelayMinMs: 100,
      paymentDelayMaxMs: 400,
      paymentFailureProbability: 0.05,
    },
    traffic: {
      seed: 42,
      durationSeconds: 600,
      maxVUs: 500,
      totalArrivals: 0,
      requestTimeoutMs: 10000,
      pattern: { type: 'ramp_up', startRate: 5, endRate: 100 },
      mix: { browse: 40, availability: 20, reserve: 25, purchase: 15 },
      behaviour: {
        maxQuantity: 4,
        thinkTimeMinMs: 200,
        thinkTimeMaxMs: 1500,
        abandonCheckoutProbability: 0.1,
        duplicateSubmitProbability: 0.05,
        retryPaymentAfterDeclineProbability: 0.5,
        syntheticCpuMs: 0,
        syntheticCpuProbability: 0,
      },
    },
    syntheticCpuMaxMs: 0,
    cooldownObserveSeconds: 900,
    waitingRoom: { enabled: false, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200 },
    confirmExpensive: false,
    ...overrides,
  };
}

export function makeMeta(): Meta {
  const defaults = makeConfig();
  return {
    approvedMaxInstances: 8,
    limits: {
      maxRate: 500,
      maxVUs: 2000,
      maxDurationSeconds: 1800,
      maxTotalInventory: 1_000_000,
      maxSyntheticCpuMs: 200,
      expensive: { rateAbove: 200, durationSecondsAbove: 1200, vusAbove: 1000, instancesAbove: 5 },
    },
    baseline: { minReplicas: 1, maxReplicas: 3, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 300 },
    defaults,
    presets: (['A', 'B', 'C', 'D', 'E', 'F'] as const).map((id) => ({
      id,
      title: `Preset ${id}`,
      description: `Description ${id}`,
      demonstrates: `Demonstrates ${id}`,
      config: makeConfig({
        name: `Preset ${id} run`,
        preset: id,
        waitingRoom: { enabled: id === 'F', activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: id === 'F' ? 'adaptive' : 'fixed', maxUsersPerServer: 200 },
      }),
    })),
    waitingRoom: { deployed: true },
    target: { baseUrl: 'http://203.0.113.10', migName: 'ticketing-scale-lab-app', zone: 'europe-west2-a', autoscalerName: 'as', region: 'europe-west2', projectId: 'p' },
    costModel: {
      currency: 'USD',
      rows: [
        { resource: 'App VM (e2-small)', unit: 'instance-hour', unitCostPerHour: 0.02, quantity: 1, perHour: 0.02, billedWhileIdle: true, note: '' },
        { resource: 'Cloud SQL', unit: 'hour', unitCostPerHour: 0.03, quantity: 1, perHour: 0.03, billedWhileIdle: true, note: '' },
        { resource: 'Load generator VM', unit: 'hour', unitCostPerHour: 0.04, quantity: 1, perHour: 0.04, billedWhileIdle: false, note: '' },
      ],
      idlePerHour: 0.05,
      peakPerHour: 0.23,
      notes: [],
    },
    links: {},
  };
}

export function makeExperiment(overrides: Partial<Experiment> = {}): Experiment {
  return {
    id: 'exp_20261009_1530_ab12',
    name: 'Scenario B – increasing traffic',
    preset: 'B',
    state: 'running',
    config: makeConfig(),
    createdAt: '2026-10-09T15:28:00.000Z',
    readyAt: '2026-10-09T15:29:00.000Z',
    trafficStartedAt: '2026-10-09T15:30:00.000Z',
    trafficStoppedAt: null,
    ...overrides,
  };
}

export function makeInstance(name: string, overrides: Partial<InstanceView> = {}): InstanceView {
  return {
    name,
    id: `id-${name}`,
    zone: 'europe-west2-a',
    privateIp: '10.0.0.2',
    createdAt: '2026-10-09T15:00:00.000Z',
    vmStatus: 'RUNNING',
    currentAction: 'NONE',
    migHealth: 'HEALTHY',
    lbHealth: 'HEALTHY',
    phase: 'healthy',
    cpu: { value: 0.42, sampleTime: '2026-10-09T15:37:50.000Z', source: 'cloud_monitoring' },
    guestCpu: { value: 0.55, sampleTime: '2026-10-09T15:39:59.000Z', source: 'instance_info' },
    traffic: { rps: 30, avgLatencyMs: 42.5, errorRate: 0.01, windowSeconds: 10 },
    appVersion: '1.0.0',
    ready: true,
    ...overrides,
  };
}

export function makeSnapshot(overrides: Partial<LiveSnapshot> = {}): LiveSnapshot {
  const instances = overrides.instances ?? [makeInstance('ticketing-scale-lab-app-x7k2'), makeInstance('ticketing-scale-lab-app-q9w1')];
  return {
    serverTime: SERVER_TIME,
    experiment: makeExperiment(),
    mig: {
      observedAt: SERVER_TIME,
      name: 'ticketing-scale-lab-app',
      zone: 'europe-west2-a',
      targetSize: 3,
      actualCount: 2,
      runningCount: 2,
      healthyCount: 2,
      isStable: false,
      autoscaler: { minReplicas: 1, maxReplicas: 5, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 240, recommendedSize: 3, status: 'ACTIVE', messages: [] },
      baseline: { minReplicas: 1, maxReplicas: 3, cpuTarget: 0.6, initPeriodSeconds: 60, scaleInWindowSeconds: 300 },
      overridesActive: true,
      approvedMaxInstances: 8,
    },
    instances,
    traffic: {
      second: 600,
      targetRate: 50,
      achievedArrivalRate: 49,
      rps: 180,
      successRps: 170,
      errorRps: 2,
      businessRejectRps: 8,
      p50: 30,
      p95: 120,
      p99: 290,
      activeVUs: 60,
      dropped: 0,
      queueWaiting: null,
      queueJoinedRate: null,
      queueAdmittedRate: null,
      queueWaitP95: null,
      generator: { state: 'running', heartbeatAgeSeconds: 1, message: null },
    },
    inventory: {
      observedAt: SERVER_TIME,
      eventId: 'evt-1',
      capacity: 100000,
      available: 98765,
      held: 35,
      sold: 1200,
      activeReservations: 12,
      paymentProcessing: 2,
      expiredReservations: 17,
      ticketsIssued: 1200,
      invariantViolations: 0,
      categories: [],
    },
    waitingRoom: null,
    cpu: { average: 0.42, sampleTime: '2026-10-09T15:37:50.000Z', target: 0.6 },
    freshness: {
      traffic: live(),
      mig: live(),
      cpu: { status: 'delayed', lastAt: '2026-10-09T15:37:50.000Z', ageSeconds: 130 },
      inventory: live(),
      lb: live(),
      waitingRoom: { status: 'missing', lastAt: null, ageSeconds: null, note: 'Waiting room not deployed (WAITING_ROOM_URL is not set)' },
    },
    ...overrides,
  };
}

export function makeTrafficPoint(second: number, overrides: Partial<TrafficPoint> = {}): TrafficPoint {
  return {
    t: T0 + second * 1000,
    second,
    targetRate: 10,
    scheduled: 10,
    achieved: 10,
    rps: 40,
    ok: 35,
    errors: 1,
    business: 4,
    p50: 30,
    p95: 100,
    p99: 200,
    dropped: 0,
    activeVUs: 12,
    reservationsAttempted: 3,
    reservationsSucceeded: 2,
    purchasesCompleted: 1,
    purchasesFailed: 0,
    rejectedSoldOut: 1,
    httpErrors: 1,
    queueJoined: null,
    queueAdmitted: null,
    queueSoldOut: null,
    queueWaiting: null,
    queueWaitP95: null,
    ...overrides,
  };
}

export function makeEvent(id: string, overrides: Partial<TimelineEvent> = {}): TimelineEvent {
  return {
    id,
    t: '2026-10-09T15:31:05.000Z',
    kind: 'scaling',
    source: 'compute_api',
    attribution: 'autoscaler',
    message: `Event ${id}`,
    ...overrides,
  };
}

export function emptySeries(): Series {
  return { traffic: [], scaling: [], cpu: [], inventory: [], waitingRoom: [], events: [] };
}

export function makeWaitingRoom(overrides: Partial<WaitingRoomView> = {}): WaitingRoomView {
  return {
    enabled: true,
    eventId: 'evt-1',
    soldOut: false,
    waiting: 3110,
    active: 75,
    capacity: 75,
    healthyServers: 3,
    servers: [{ name: 'ticketing-scale-lab-app-ab12', ip: '10.60.0.9' }],
    activeUsersPerServer: 25,
    joinedTotal: 4200,
    admittedTotal: 1090,
    releasedTotal: 1015,
    expiredTotal: 0,
    soldOutTotal: 0,
    admissionRatePerSecond: 24.5,
    waitMs: { p50: 8000, p95: 41000, max: 52000, count: 1090 },
    oldestWaitingSeconds: 48.2,
    inventory: { available: 812, soldOut: false, observedAt: SERVER_TIME },
    capacitySource: 'gce',
    capacityError: null,
    updatedAt: SERVER_TIME,
    observedAt: SERVER_TIME,
    freshness: live(),
    ...overrides,
  };
}

/** Adaptive-mode server row as the queue reports it, with fresh signals 1 s before SERVER_TIME. */
export function makeAdaptiveServer(name: string, overrides: Partial<WaitingRoomServer> = {}): WaitingRoomServer {
  return {
    name,
    ip: '10.60.0.9',
    limit: 31,
    phase: 'steady',
    decision: 'increase',
    reason: 'headroom',
    decisionAt: new Date(Date.parse(SERVER_TIME) - 1000).toISOString(),
    decreases: 2,
    signals: { busy: 0.81, activeWorkers: 26, totalWorkers: 32, listenQueue: 0, cpu: 0.74, observedAt: new Date(Date.parse(SERVER_TIME) - 1000).toISOString() },
    ...overrides,
  };
}

export function makeWaitingRoomPoint(second: number, overrides: Partial<WaitingRoomPoint> = {}): WaitingRoomPoint {
  return {
    t: T0 + second * 1000,
    waiting: 1000,
    active: 25,
    capacity: 25,
    healthyServers: 1,
    admissionRate: 20,
    joinedTotal: 1200,
    admittedTotal: 200,
    soldOutTotal: 0,
    soldOut: false,
    waitP50Ms: 4000,
    waitP95Ms: 9000,
    waitMaxMs: 9500,
    ...overrides,
  };
}
