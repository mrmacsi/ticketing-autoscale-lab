// Types copied from docs/contracts.md section 3 (Control API). Keep in sync with the contract.
// Types marked "dashboard assumption" are not spelled out in the contract; see README.

export type ExperimentState =
  | 'idle'
  | 'preparing'
  | 'ready'
  | 'running'
  | 'stopping'
  | 'cooling_down'
  | 'completed'
  | 'failed';

export const EXPERIMENT_STATES: readonly ExperimentState[] = [
  'idle',
  'preparing',
  'ready',
  'running',
  'stopping',
  'cooling_down',
  'completed',
  'failed',
];

export type AuthMode = 'iam_proxy' | 'google_signin' | 'dev';

export interface AuthConfig {
  authMode: AuthMode;
  googleClientId: string | null;
}

export interface Me {
  email: string;
  authMode: AuthMode;
}

export interface ApiErrorBody {
  error: string;
  message: string;
  details?: unknown;
}

// ---------------------------------------------------------------------------
// Traffic / experiment configuration
// ---------------------------------------------------------------------------

export type PatternType = 'constant' | 'ramp_up' | 'spike' | 'ramp_down' | 'random_bursts' | 'custom';

export interface SchedulePoint {
  atSeconds: number;
  rate: number;
}

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
  points?: SchedulePoint[];
}

export interface TrafficMix {
  browse: number;
  availability: number;
  reserve: number;
  purchase: number;
}

export interface TrafficBehaviour {
  maxQuantity: number;
  thinkTimeMinMs: number;
  thinkTimeMaxMs: number;
  abandonCheckoutProbability: number;
  duplicateSubmitProbability: number;
  retryPaymentAfterDeclineProbability: number;
  syntheticCpuMs: number;
  syntheticCpuProbability: number;
}

export interface TrafficConfig {
  experimentId: string;
  eventId: string;
  seed: number;
  durationSeconds: number;
  maxVUs: number;
  totalArrivals: number;
  requestTimeoutMs: number;
  pattern: TrafficPattern;
  mix: TrafficMix;
  behaviour: TrafficBehaviour;
  /** Set by the control API from ExperimentConfig.waitingRoom when traffic starts (absent ⇒ disabled). */
  waitingRoom?: { enabled: boolean };
}

export type ExperimentTrafficConfig = Omit<TrafficConfig, 'experimentId' | 'eventId' | 'waitingRoom'>;

export type AdmissionMode = 'fixed' | 'adaptive';

/** docs/waiting-room.md 3.7. */
export interface WaitingRoomSettings {
  enabled: boolean;
  /**
   * Fixed mode: customers admitted to checkout at once per load-balancer-healthy server (1..1000).
   * Adaptive mode: the starting limit of each new server.
   */
  activeUsersPerServer: number;
  /** Seconds an admitted customer may hold a slot without releasing it (10..3600). */
  admissionTtlSeconds: number;
  /** Absent in experiments stored before adaptive admission (⇒ 'fixed'); cloneConfig fills it in. */
  mode?: AdmissionMode;
  /** Adaptive mode: upper bound of a server's learned limit (1..1000, ≥ activeUsersPerServer). Default 200. */
  maxUsersPerServer?: number;
}

export const DEFAULT_WAITING_ROOM: Required<WaitingRoomSettings> = {
  enabled: false,
  activeUsersPerServer: 25,
  admissionTtlSeconds: 60,
  mode: 'fixed',
  maxUsersPerServer: 200,
};

export type PresetId = 'A' | 'B' | 'C' | 'D' | 'E' | 'F';

export interface ServerConfig {
  initial: number;
  min: number;
  max: number;
  /** Fraction 0..1 (0.6 = 60 %). */
  cpuTarget: number;
  initPeriodSeconds: number;
  scaleInWindowSeconds: number;
}

export interface TicketConfig {
  eventName: string;
  totalInventory: number;
  categoryCount: number;
  pricesCents: number[];
  reservationTtlSeconds: number;
  paymentDelayMinMs: number;
  paymentDelayMaxMs: number;
  paymentFailureProbability: number;
}

export interface ExperimentConfig {
  name: string;
  preset: PresetId | null;
  servers: ServerConfig;
  tickets: TicketConfig;
  traffic: ExperimentTrafficConfig;
  syntheticCpuMaxMs: number;
  cooldownObserveSeconds: number;
  /** Optional only in experiments stored before the waiting room existed; the control API always sends it. */
  waitingRoom?: WaitingRoomSettings;
  confirmExpensive: boolean;
}

// ---------------------------------------------------------------------------
// Experiment document (dashboard assumption: the contract references `Experiment`
// without defining it; only id/name/state/config/createdAt are relied upon).
// ---------------------------------------------------------------------------

export interface GeneratorLease {
  owner: string;
  leaseExpiresAt: string | null;
  state: 'running' | 'finished';
  startedAt: string | null;
  finishedAt: string | null;
  finishReason: 'completed' | 'stopped' | 'max_duration' | 'total_arrivals_reached' | 'error' | null;
  message: string | null;
}

export interface Experiment {
  id: string;
  name: string;
  preset: PresetId | null;
  state: ExperimentState;
  config: ExperimentConfig;
  eventId?: string | null;
  createdAt: string;
  createdBy?: string | null;
  readyAt?: string | null;
  trafficStartedAt?: string | null;
  trafficStoppedAt?: string | null;
  cooldownStartedAt?: string | null;
  completedAt?: string | null;
  failedAt?: string | null;
  message?: string | null;
  error?: string | null;
  generator?: GeneratorLease | null;
}

/** Row of `GET /api/experiments` (dashboard assumption). */
export interface ExperimentSummary {
  id: string;
  name: string;
  preset: PresetId | null;
  state: ExperimentState;
  createdAt: string;
  trafficStartedAt?: string | null;
  trafficStoppedAt?: string | null;
  durationSeconds?: number | null;
  peakRps?: number | null;
  maxServers?: number | null;
  ticketsSold?: number | null;
  invariantViolations?: number | null;
}

// ---------------------------------------------------------------------------
// Live telemetry
// ---------------------------------------------------------------------------

export type FreshnessStatus = 'live' | 'delayed' | 'stale' | 'missing';

export type Freshness = { status: FreshnessStatus; lastAt: string | null; ageSeconds: number | null; note?: string };

export type FreshnessSource = 'traffic' | 'mig' | 'cpu' | 'inventory' | 'lb' | 'waitingRoom';

export interface AutoscalerSettings {
  minReplicas: number;
  maxReplicas: number;
  cpuTarget: number;
  initPeriodSeconds: number;
  scaleInWindowSeconds: number | null;
  recommendedSize: number | null;
  status: string;
  messages: string[];
}

export interface BaselineSettings {
  minReplicas: number;
  maxReplicas: number;
  cpuTarget: number;
  initPeriodSeconds: number;
  scaleInWindowSeconds: number;
}

export interface MigView {
  observedAt: string;
  name: string;
  zone: string;
  targetSize: number;
  actualCount: number;
  runningCount: number;
  healthyCount: number;
  isStable: boolean;
  autoscaler: AutoscalerSettings;
  baseline: BaselineSettings;
  overridesActive: boolean;
  approvedMaxInstances: number;
}

export interface LiveTraffic {
  second: number | null;
  targetRate: number | null;
  achievedArrivalRate: number | null;
  rps: number | null;
  successRps: number | null;
  errorRps: number | null;
  businessRejectRps: number | null;
  p50: number | null;
  p95: number | null;
  p99: number | null;
  activeVUs: number | null;
  dropped: number | null;
  /** Generator-side waiting room (bucket `queue`); null when absent. */
  queueWaiting: number | null;
  queueJoinedRate: number | null;
  queueAdmittedRate: number | null;
  queueWaitP95: number | null;
  generator: { state: string; heartbeatAgeSeconds: number | null; message: string | null };
}

export interface InventoryCategoryView {
  id: string;
  name: string;
  capacity: number;
  held: number;
  sold: number;
}

export interface InventoryView {
  observedAt: string;
  eventId: string;
  capacity: number;
  available: number;
  held: number;
  sold: number;
  activeReservations: number;
  paymentProcessing: number;
  expiredReservations: number;
  ticketsIssued: number;
  invariantViolations: number;
  categories: InventoryCategoryView[];
}

/** Per-server load signals the queue reads in adaptive mode (docs/waiting-room.md 2a); any gauge may be unknown. */
export interface WaitingRoomServerSignals {
  /** Busy PHP-FPM workers, fraction 0..1. */
  busy: number | null;
  activeWorkers: number | null;
  totalWorkers: number | null;
  /** Requests waiting for a free PHP-FPM worker. */
  listenQueue: number | null;
  /** VM CPU, fraction 0..1. */
  cpu: number | null;
  observedAt: string | null;
}

export type WaitingRoomServerPhase = 'slow_start' | 'steady' | 'fixed';
export type WaitingRoomDecision = 'increase' | 'decrease' | 'hold';

/** A healthy server as the queue sees it; all but name/ip are absent/null from a queue older than adaptive admission. */
export interface WaitingRoomServer {
  name: string;
  ip: string;
  limit?: number | null;
  phase?: WaitingRoomServerPhase | null;
  decision?: WaitingRoomDecision | null;
  reason?: string | null;
  decisionAt?: string | null;
  decreases?: number | null;
  signals?: WaitingRoomServerSignals | null;
}

export interface WaitingRoomAdaptiveSettings {
  minPerServer: number | null;
  maxPerServer: number | null;
  busyHigh: number | null;
  busyLow: number | null;
  cpuHigh: number | null;
  cpuLow: number | null;
  decreaseFactor: number | null;
  slowStartFactor: number | null;
  cooldownSeconds: number | null;
}

/** Queue server `GET /admin/stats` (docs/waiting-room.md 3.4). */
export interface WaitingRoomStats {
  enabled: boolean;
  eventId: string | null;
  soldOut: boolean;
  waiting: number;
  active: number;
  capacity: number;
  /** App VMs the load balancer reports HEALTHY. */
  healthyServers: number;
  servers: WaitingRoomServer[];
  activeUsersPerServer: number;
  /** null/absent from a queue (or control API) older than adaptive admission: it behaves as 'fixed'. */
  admissionMode?: AdmissionMode | null;
  adaptive?: WaitingRoomAdaptiveSettings | null;
  joinedTotal: number;
  admittedTotal: number;
  releasedTotal: number;
  expiredTotal: number;
  soldOutTotal: number;
  /** Dropped from the line for not checking in (closed the page); null from an older queue. */
  abandonedTotal?: number | null;
  /** Admissions over the last 10 s. */
  admissionRatePerSecond: number;
  /** join → admitted for admitted customers. */
  waitMs: { p50: number | null; p95: number | null; max: number | null; count: number };
  oldestWaitingSeconds: number | null;
  inventory: { available: number; soldOut: boolean; observedAt: string } | null;
  capacitySource: string;
  capacityError: string | null;
  updatedAt: string | null;
}

export interface WaitingRoomView extends WaitingRoomStats {
  observedAt: string;
  freshness: Freshness;
}

export interface LiveSnapshot {
  serverTime: string;
  experiment: Experiment | null;
  mig: MigView | null;
  instances: InstanceView[];
  traffic: LiveTraffic;
  inventory: InventoryView | null;
  /** null until the queue server has been observed (or when it is not deployed). */
  waitingRoom: WaitingRoomView | null;
  cpu: { average: number | null; sampleTime: string | null; target: number | null };
  freshness: { traffic: Freshness; mig: Freshness; cpu: Freshness; inventory: Freshness; lb: Freshness; waitingRoom: Freshness };
}

export type InstancePhase = 'starting' | 'healthy' | 'unhealthy' | 'draining' | 'terminating' | 'unknown';

export interface InstanceView {
  name: string;
  id: string;
  zone: string;
  privateIp: string | null;
  createdAt: string | null;
  vmStatus: string;
  currentAction: string;
  migHealth: string | null;
  lbHealth: string | null;
  phase: InstancePhase;
  cpu: { value: number | null; sampleTime: string | null; source: 'cloud_monitoring' };
  guestCpu: { value: number | null; sampleTime: string | null; source: 'instance_info' } | null;
  traffic: { rps: number; avgLatencyMs: number | null; errorRate: number | null; windowSeconds: number };
  appVersion: string | null;
  ready: boolean | null;
}

export interface TrafficPoint {
  t: number;
  second: number;
  targetRate: number;
  scheduled: number;
  achieved: number;
  rps: number;
  ok: number;
  errors: number;
  business: number;
  p50: number | null;
  p95: number | null;
  p99: number | null;
  dropped: number;
  activeVUs: number;
  reservationsAttempted: number;
  reservationsSucceeded: number;
  purchasesCompleted: number;
  purchasesFailed: number;
  rejectedSoldOut: number;
  httpErrors: number;
  /** Generator-side waiting room for this second; null when the bucket had no `queue` object. */
  queueJoined: number | null;
  queueAdmitted: number | null;
  queueSoldOut: number | null;
  queueWaiting: number | null;
  queueWaitP95: number | null;
}

export interface ScalingPoint {
  t: number;
  actual: number;
  running: number;
  healthy: number;
  target: number;
  min: number;
  max: number;
}

/** t = Cloud Monitoring sample end time, NOT poll time. */
export interface CpuPoint {
  t: number;
  instance: string | '__avg__';
  value: number;
}

export interface InventoryPoint {
  t: number;
  capacity: number;
  available: number;
  held: number;
  sold: number;
  expired: number;
  rejected: number;
}

/** One queue-server stats observation; t = control-plane receive time. */
export interface WaitingRoomPoint {
  t: number;
  waiting: number;
  active: number;
  capacity: number;
  healthyServers: number;
  admissionRate: number;
  joinedTotal: number;
  admittedTotal: number;
  soldOutTotal: number;
  soldOut: boolean;
  waitP50Ms: number | null;
  waitP95Ms: number | null;
  waitMaxMs: number | null;
  /** Adaptive mode only: learned limit per server name (null/absent in fixed mode and older points). */
  limits?: Record<string, number> | null;
  /** Adaptive mode only: each server's cumulative cut counter. */
  decreases?: Record<string, number> | null;
  limitAvg?: number | null;
  limitMin?: number | null;
  limitMax?: number | null;
}

export type TimelineKind =
  | 'experiment'
  | 'traffic'
  | 'scaling'
  | 'instance'
  | 'health'
  | 'cpu'
  | 'inventory'
  | 'config'
  | 'error'
  | 'waiting_room';

export type TimelineSource =
  | 'control_plane'
  | 'generator'
  | 'compute_api'
  | 'cloud_monitoring'
  | 'database'
  | 'load_balancer'
  | 'waiting_room';

export type Attribution = 'autoscaler' | 'baseline' | 'operator';

export interface TimelineEvent {
  id: string;
  t: string;
  kind: TimelineKind;
  source: TimelineSource;
  attribution?: Attribution | null;
  message: string;
  data?: Record<string, unknown>;
}

export interface Series {
  traffic: TrafficPoint[];
  scaling: ScalingPoint[];
  cpu: CpuPoint[];
  inventory: InventoryPoint[];
  /** Absent from control APIs older than the waiting room. */
  waitingRoom?: WaitingRoomPoint[];
  events: TimelineEvent[];
}

export interface TickAppend {
  traffic: TrafficPoint[];
  scaling: ScalingPoint[];
  cpu: CpuPoint[];
  inventory: InventoryPoint[];
  waitingRoom?: WaitingRoomPoint[];
}

export interface TickEvent {
  snapshot: LiveSnapshot;
  append: TickAppend;
}

// ---------------------------------------------------------------------------
// Meta
// ---------------------------------------------------------------------------

export interface MetaLimits {
  maxRate: number;
  maxVUs: number;
  maxDurationSeconds: number;
  maxTotalInventory: number;
  maxSyntheticCpuMs: number;
  expensive: { rateAbove: number; durationSecondsAbove: number; vusAbove: number; instancesAbove: number };
}

export interface Preset {
  id: PresetId;
  title: string;
  description: string;
  demonstrates: string;
  config: ExperimentConfig;
}

export interface CostRow {
  resource: string;
  unit: string;
  unitCostPerHour: number;
  quantity: number;
  perHour: number;
  billedWhileIdle: boolean;
  note: string;
}

export interface CostModel {
  currency: 'USD';
  rows: CostRow[];
  idlePerHour: number;
  peakPerHour: number;
  notes: string[];
}

export interface Meta {
  approvedMaxInstances: number;
  limits: MetaLimits;
  baseline: BaselineSettings;
  defaults: ExperimentConfig;
  presets: Preset[];
  target: { baseUrl: string; migName: string; zone: string; autoscalerName: string; region: string; projectId: string };
  costModel: CostModel;
  links: Record<string, string>;
  /** Whether the queue server exists in this lab (WAITING_ROOM_URL set on the control API). */
  waitingRoom?: { deployed: boolean };
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

export interface Report {
  experimentId: string;
  name: string;
  generatedAt: string;
  state: string;
  config: ExperimentConfig;
  timestamps: {
    createdAt: string;
    readyAt: string | null;
    trafficStartedAt: string | null;
    trafficStoppedAt: string | null;
    completedAt: string | null;
  };
  measured: {
    arrivalsRequested: number;
    arrivalsScheduled: number;
    arrivalsStarted: number;
    arrivalsDropped: number;
    achievedVsRequestedPct: number | null;
    httpRequests: number;
    peakRps: number;
    latencyMs: { p50: number | null; p95: number | null; p99: number | null; max: number | null };
    httpErrorPct: number | null;
    businessRejectionPct: number | null;
    peakCpu: { value: number; sampleTime: string; instance: string } | null;
    peakAverageCpu: { value: number; sampleTime: string } | null;
    initialServerCount: number | null;
    maxObservedServerCount: number | null;
    finalServerCount: number | null;
    ticketsSold: number | null;
    ticketsRemaining: number | null;
    expiredReservations: number | null;
    invariantViolations: number | null;
    reservations: { attempted: number; succeeded: number; rejected: number };
    purchases: { completed: number; declined: number; failed: number; expiredDuringCheckout: number };
    requestsPerInstance: Record<string, number>;
    /** null when the experiment ran without the waiting room (absent in older reports). */
    waitingRoom?: WaitingRoomReport | null;
  };
  configured: {
    cpuTarget: number;
    minReplicas: number;
    maxReplicas: number;
    initialServers: number;
    pattern: string;
    durationSeconds: number;
  };
  estimated: { infrastructureCostUsd: number; note: string };
  scalingTimeline: TimelineEvent[];
  generatorCapacityIssues: string[];
  failures: string[];
  notes: string[];
}

export interface WaitingRoomReport {
  enabled: boolean;
  joined: number | null;
  admitted: number | null;
  soldOutInQueue: number | null;
  peakWaiting: number | null;
  waitMs: { p50: number | null; p95: number | null; max: number | null };
  peakCapacity: number | null;
  peakHealthyServers: number | null;
  /** null in fixed mode; absent in reports older than adaptive admission. */
  adaptive?: WaitingRoomAdaptiveReport | null;
}

export interface WaitingRoomAdaptiveReport {
  perServer: Array<{ name: string; finalLimit: number; peakLimit: number; decreases: number }>;
  limitAvgAtPeakCapacity: number | null;
  decreasesTotal: number;
}
