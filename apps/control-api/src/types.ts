// Mirrors docs/contracts.md (section 3). Keep in sync with apps/dashboard/src/types/contracts.ts.

export type ExperimentState =
  | 'idle' | 'preparing' | 'ready' | 'running' | 'stopping' | 'cooling_down' | 'completed' | 'failed';

export const ACTIVE_STATES: ExperimentState[] = ['preparing', 'ready', 'running', 'stopping', 'cooling_down'];

export type PatternType = 'constant' | 'ramp_up' | 'spike' | 'ramp_down' | 'random_bursts' | 'custom';

export interface TrafficPattern {
  type: PatternType;
  rate?: number;
  startRate?: number;
  endRate?: number;
  baseRate?: number;
  peakRate?: number;
  spikeAtSeconds?: number;
  spikeDurationSeconds?: number;
  burstCount?: number;
  burstDurationSeconds?: number;
  points?: Array<{ atSeconds: number; rate: number }>;
}

export interface TrafficSettings {
  seed: number;
  durationSeconds: number;
  maxVUs: number;
  totalArrivals: number;
  requestTimeoutMs: number;
  pattern: TrafficPattern;
  mix: { browse: number; availability: number; reserve: number; purchase: number };
  behaviour: {
    maxQuantity: number;
    thinkTimeMinMs: number;
    thinkTimeMaxMs: number;
    abandonCheckoutProbability: number;
    duplicateSubmitProbability: number;
    retryPaymentAfterDeclineProbability: number;
    syntheticCpuMs: number;
    syntheticCpuProbability: number;
  };
}

export interface TrafficConfig extends TrafficSettings {
  experimentId: string;
  eventId: string;
  /** docs/waiting-room.md 3.6: absent ⇒ disabled. */
  waitingRoom: { enabled: boolean };
}

export type AdmissionMode = 'fixed' | 'adaptive';

/**
 * docs/waiting-room.md 3.7. Optional in stored documents written before the waiting room existed;
 * `mode` and `maxUsersPerServer` are absent from documents written before adaptive admission.
 */
export interface WaitingRoomSettings {
  enabled: boolean;
  /** Fixed mode: customers in checkout per healthy server. Adaptive mode: the starting limit of each new server. */
  activeUsersPerServer: number;
  admissionTtlSeconds: number;
  /** 'fixed': capacity = healthy servers × activeUsersPerServer; 'adaptive': each server's limit is learned (docs 2a). */
  mode: AdmissionMode;
  /** Adaptive mode only: upper bound of a server's learned limit (sent as adaptive.maxPerServer). */
  maxUsersPerServer: number;
}

export const DEFAULT_WAITING_ROOM: WaitingRoomSettings = {
  enabled: false, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200,
};

/** Waiting-room settings of a (possibly old, stored) config; missing ⇒ disabled. */
export const waitingRoomOf = (c: Pick<ExperimentConfig, 'waitingRoom'> | null | undefined): WaitingRoomSettings =>
  ({ ...DEFAULT_WAITING_ROOM, ...(c?.waitingRoom ?? {}) });

export interface ExperimentConfig {
  name: string;
  preset: 'A' | 'B' | 'C' | 'D' | 'E' | 'F' | null;
  servers: { initial: number; min: number; max: number; cpuTarget: number; initPeriodSeconds: number; scaleInWindowSeconds: number };
  tickets: {
    eventName: string; totalInventory: number; categoryCount: number; pricesCents: number[];
    reservationTtlSeconds: number; paymentDelayMinMs: number; paymentDelayMaxMs: number; paymentFailureProbability: number;
  };
  traffic: TrafficSettings;
  syntheticCpuMaxMs: number;
  cooldownObserveSeconds: number;
  waitingRoom: WaitingRoomSettings;
  confirmExpensive?: boolean;
}

export interface Experiment {
  id: string;
  name: string;
  state: ExperimentState;
  config: ExperimentConfig;
  createdBy: string;
  createdAt: string;
  readyAt: string | null;
  trafficStartedAt: string | null;
  trafficStoppedAt: string | null;
  completedAt: string | null;
  failedReason: string | null;
  eventId: string | null;
  categories: Array<{ id: string; name: string; price_cents: number; capacity: number }>;
  generator: {
    owner?: string; state?: string; startedAt?: string | null; finishedAt?: string | null;
    finishReason?: string | null; message?: string | null;
  } | null;
  initialServerCount: number | null;
  report: Report | null;
}

export type FreshnessStatus = 'live' | 'delayed' | 'stale' | 'missing';
export interface Freshness { status: FreshnessStatus; lastAt: string | null; ageSeconds: number | null; note?: string }

export interface AutoscalerView {
  minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number;
  scaleInWindowSeconds: number | null; recommendedSize: number | null; status: string; messages: string[];
}

export interface Baseline { minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number; scaleInWindowSeconds: number }

export interface MigView {
  observedAt: string; name: string; zone: string;
  targetSize: number; actualCount: number; runningCount: number; healthyCount: number; isStable: boolean;
  autoscaler: AutoscalerView;
  baseline: Baseline;
  overridesActive: boolean;
  approvedMaxInstances: number;
}

export type InstancePhase = 'starting' | 'healthy' | 'unhealthy' | 'draining' | 'terminating' | 'unknown';

export interface InstanceView {
  name: string; id: string; zone: string; privateIp: string | null; createdAt: string | null;
  vmStatus: string; currentAction: string; migHealth: string | null; lbHealth: string | null;
  phase: InstancePhase;
  cpu: { value: number | null; sampleTime: string | null; source: 'cloud_monitoring' };
  guestCpu: { value: number | null; sampleTime: string | null; source: 'instance_info' } | null;
  traffic: { rps: number; avgLatencyMs: number | null; errorRate: number | null; windowSeconds: number };
  appVersion: string | null; ready: boolean | null;
}

export interface TrafficView {
  second: number | null; targetRate: number | null; achievedArrivalRate: number | null;
  rps: number | null; successRps: number | null; errorRps: number | null; businessRejectRps: number | null;
  p50: number | null; p95: number | null; p99: number | null; activeVUs: number | null; dropped: number | null;
  /** Generator-side waiting room (bucket `queue`); null when the bucket has none (old generator / no traffic). */
  queueWaiting: number | null; queueJoinedRate: number | null; queueAdmittedRate: number | null; queueWaitP95: number | null;
  generator: { state: string; heartbeatAgeSeconds: number | null; message: string | null };
}

export interface InventoryView {
  observedAt: string; eventId: string; capacity: number; available: number; held: number; sold: number;
  activeReservations: number; paymentProcessing: number; expiredReservations: number; ticketsIssued: number;
  invariantViolations: number;
  categories: Array<{ id: string; name: string; capacity: number; held: number; sold: number }>;
}

/** Per-server load signals the queue reads in adaptive mode (docs/waiting-room.md 2a); every gauge may be unknown. */
export interface WaitingRoomServerSignals {
  /** Busy PHP-FPM workers as a fraction 0..1. */
  busy: number | null; activeWorkers: number | null; totalWorkers: number | null;
  /** Requests waiting for a free PHP-FPM worker. */
  listenQueue: number | null;
  /** VM CPU as a fraction 0..1 (sampled inside the VM every 2 s). */
  cpu: number | null;
  observedAt: string | null;
}

export type WaitingRoomServerPhase = 'slow_start' | 'steady' | 'fixed';
export type WaitingRoomDecision = 'increase' | 'decrease' | 'hold';

/** One healthy server as the queue sees it. Everything but name/ip is absent from queues older than adaptive admission. */
export interface WaitingRoomServer {
  name: string; ip: string;
  limit: number | null; phase: WaitingRoomServerPhase | null;
  decision: WaitingRoomDecision | null; reason: string | null; decisionAt: string | null;
  decreases: number | null;
  signals: WaitingRoomServerSignals | null;
}

/** Adaptive controller settings reported by the queue (docs/waiting-room.md 3.4). */
export interface WaitingRoomAdaptiveSettings {
  minPerServer: number | null; maxPerServer: number | null;
  busyHigh: number | null; busyLow: number | null; cpuHigh: number | null; cpuLow: number | null;
  decreaseFactor: number | null; slowStartFactor: number | null; cooldownSeconds: number | null;
}

/** `GET /admin/stats` of the queue server (docs/waiting-room.md 3.4). */
export interface WaitingRoomStats {
  enabled: boolean; eventId: string | null; soldOut: boolean;
  waiting: number; active: number; capacity: number;
  healthyServers: number; servers: WaitingRoomServer[];
  activeUsersPerServer: number;
  /** null from a queue older than adaptive admission (it always behaves as 'fixed'). */
  admissionMode: AdmissionMode | null;
  adaptive: WaitingRoomAdaptiveSettings | null;
  joinedTotal: number; admittedTotal: number; releasedTotal: number; expiredTotal: number; soldOutTotal: number;
  /** Dropped from the line for not checking in (closed the page); null from an older queue. */
  abandonedTotal: number | null;
  admissionRatePerSecond: number;
  waitMs: { p50: number | null; p95: number | null; max: number | null; count: number };
  oldestWaitingSeconds: number | null;
  inventory: { available: number; soldOut: boolean; observedAt: string } | null;
  capacitySource: string; capacityError: string | null;
  updatedAt: string | null;
}

/** Latest queue-server observation; `observedAt` is when the control plane received it. */
export interface WaitingRoomView extends WaitingRoomStats { observedAt: string; freshness: Freshness }

export interface LiveSnapshot {
  serverTime: string;
  experiment: Experiment | null;
  mig: MigView | null;
  instances: InstanceView[];
  traffic: TrafficView;
  inventory: InventoryView | null;
  /** null until the queue server has been observed (or when it is not deployed). */
  waitingRoom: WaitingRoomView | null;
  cpu: { average: number | null; sampleTime: string | null; target: number | null };
  freshness: { traffic: Freshness; mig: Freshness; cpu: Freshness; inventory: Freshness; lb: Freshness; waitingRoom: Freshness };
}

export interface TrafficPoint {
  t: number; second: number; targetRate: number; scheduled: number; achieved: number; rps: number;
  ok: number; errors: number; business: number; p50: number | null; p95: number | null; p99: number | null;
  dropped: number; activeVUs: number;
  reservationsAttempted: number; reservationsSucceeded: number; purchasesCompleted: number;
  purchasesFailed: number; rejectedSoldOut: number; httpErrors: number;
  /** Generator-side waiting room counts for this second; null when the bucket has no `queue` object. */
  queueJoined: number | null; queueAdmitted: number | null; queueSoldOut: number | null;
  queueWaiting: number | null; queueWaitP95: number | null;
}
export interface ScalingPoint { t: number; actual: number; running: number; healthy: number; target: number; min: number; max: number }
/** value is a utilisation fraction 0..1; t is the Cloud Monitoring sample end time. */
export interface CpuPoint { t: number; instance: string; value: number }
export interface InventoryPoint { t: number; capacity: number; available: number; held: number; sold: number; expired: number; rejected: number }
/** One queue-server stats observation (t = control-plane receive time). */
export interface WaitingRoomPoint {
  t: number; waiting: number; active: number; capacity: number; healthyServers: number; admissionRate: number;
  joinedTotal: number; admittedTotal: number; soldOutTotal: number; soldOut: boolean;
  waitP50Ms: number | null; waitP95Ms: number | null; waitMaxMs: number | null;
  /**
   * Adaptive mode only (absent/null in fixed mode and in points written before adaptive admission):
   * each healthy server's learned limit and its cumulative cut counter as reported by the queue,
   * keyed by server name, plus the average / min / max limit across those servers.
   */
  limits?: Record<string, number> | null;
  decreases?: Record<string, number> | null;
  limitAvg?: number | null; limitMin?: number | null; limitMax?: number | null;
}

export type TimelineKind = 'experiment' | 'traffic' | 'scaling' | 'instance' | 'health' | 'cpu' | 'inventory' | 'config' | 'error' | 'waiting_room';
export type TimelineSource = 'control_plane' | 'generator' | 'compute_api' | 'cloud_monitoring' | 'database' | 'load_balancer' | 'waiting_room';
export type Attribution = 'autoscaler' | 'baseline' | 'operator' | null;

export interface TimelineEvent {
  id: string; t: string; kind: TimelineKind; source: TimelineSource;
  attribution?: Attribution; message: string; data?: Record<string, unknown>;
}

export interface Series {
  traffic: TrafficPoint[]; scaling: ScalingPoint[]; cpu: CpuPoint[]; inventory: InventoryPoint[]; waitingRoom: WaitingRoomPoint[];
  events: TimelineEvent[];
}

export interface WaitingRoomReport {
  enabled: boolean;
  /** Queue-server counters when observed, otherwise generator sums; null when neither was measured. */
  joined: number | null; admitted: number | null; soldOutInQueue: number | null;
  peakWaiting: number | null;
  /** join → admitted; generator histogram when available, otherwise the queue server's last observation. */
  waitMs: { p50: number | null; p95: number | null; max: number | null };
  peakCapacity: number | null; peakHealthyServers: number | null;
  /** null in fixed mode (and for experiments recorded before adaptive admission). */
  adaptive: WaitingRoomAdaptiveReport | null;
}

export interface WaitingRoomAdaptiveReport {
  /** Every server that had a learned limit while observed: last and highest limit, cuts while observed. */
  perServer: Array<{ name: string; finalLimit: number; peakLimit: number; decreases: number }>;
  /** Average per-server limit at the first observation with the peak capacity; null when never observed. */
  limitAvgAtPeakCapacity: number | null;
  decreasesTotal: number;
}

export interface Report {
  experimentId: string; name: string; generatedAt: string; state: string;
  config: ExperimentConfig;
  timestamps: { createdAt: string; readyAt: string | null; trafficStartedAt: string | null; trafficStoppedAt: string | null; completedAt: string | null };
  measured: {
    arrivalsRequested: number; arrivalsScheduled: number; arrivalsStarted: number; arrivalsDropped: number;
    achievedVsRequestedPct: number | null;
    httpRequests: number; peakRps: number;
    latencyMs: { p50: number | null; p95: number | null; p99: number | null; max: number | null };
    httpErrorPct: number | null; businessRejectionPct: number | null;
    peakCpu: { value: number; sampleTime: string; instance: string } | null;
    peakAverageCpu: { value: number; sampleTime: string } | null;
    initialServerCount: number | null; maxObservedServerCount: number | null; finalServerCount: number | null;
    ticketsSold: number | null; ticketsRemaining: number | null; expiredReservations: number | null;
    invariantViolations: number | null;
    reservations: { attempted: number; succeeded: number; rejected: number };
    purchases: { completed: number; declined: number; failed: number; expiredDuringCheckout: number };
    requestsPerInstance: Record<string, number>;
    /** null when the waiting room was disabled for this experiment. */
    waitingRoom: WaitingRoomReport | null;
  };
  configured: { cpuTarget: number; minReplicas: number; maxReplicas: number; initialServers: number; pattern: string; durationSeconds: number };
  estimated: { infrastructureCostUsd: number; note: string };
  scalingTimeline: TimelineEvent[];
  generatorCapacityIssues: string[];
  failures: string[];
  notes: string[];
}

/** One second of generator measurements, exactly as written to Firestore by the load generator. */
export interface TrafficBucket {
  second: number;
  ts: string;
  targetRate: number; scheduledArrivals: number; startedArrivals: number; droppedArrivals: number;
  completedJourneys: number; activeVUs: number; maxActiveVUs: number; schedulerLagMsMax: number;
  requests: { total: number; ok2xx: number; conflict409: number; gone410: number; payment402: number;
              client4xx: number; server5xx: number; networkErrors: number; timeouts: number };
  byEndpoint: Record<string, { count: number; errors: number }>;
  latency: { count: number; sumMs: number; maxMs: number; p50: number | null; p95: number | null; p99: number | null; hist: Record<string, number> };
  instances: Record<string, { requests: number; errors: number; latencySumMs: number }>;
  business: {
    reservationsAttempted: number; reservationsSucceeded: number; reservationsRejected: number;
    ordersCreated: number; paymentsAttempted: number; purchasesCompleted: number; paymentsDeclined: number;
    purchasesFailed: number; expiredDuringCheckout: number; abandoned: number;
    duplicateSubmissions: number; idempotentReplays: number; ticketsPurchased: number;
  };
  synthetic: { requests: number; cpuMsRequested: number };
  /** Waiting-room activity (docs/waiting-room.md 3.6); absent from older generators. */
  queue?: TrafficBucketQueue;
}

export interface TrafficBucketQueue {
  joined: number; admitted: number; soldOut: number; open: number; failed: number;
  waiting: number;
  waitMs: { count: number; sumMs?: number; maxMs: number; p50: number | null; p95: number | null; p99: number | null; hist: Record<string, number> };
  requests: { total: number; ok: number; errors: number };
}

export interface GeneratorStatus {
  agentId: string; state: 'idle' | 'running' | 'stopping' | 'error'; experimentId: string | null;
  lastCommandSeq: number; heartbeatAt: string | null; runStartedAt: string | null; message: string | null;
  version: string | null; limits: { maxRate: number; maxVUs: number; maxDurationSeconds: number } | null;
  targetBaseUrl: string | null;
}
