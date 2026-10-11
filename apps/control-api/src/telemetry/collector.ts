import type { AppConfig } from '../config.js';
import type { InfraProvider, MigReading } from '../infra/types.js';
import type { LabStore } from '../store/types.js';
import { fetchInstanceInfo, type InstanceInfo } from '../ticketing/admin.js';
import type { InventoryReader } from '../ticketing/inventory.js';
import { WaitingRoomClient } from '../ticketing/waitingRoom.js';
import {
  ACTIVE_STATES, type CpuPoint, type Experiment, type GeneratorStatus, type InstanceView, type InventoryPoint, type InventoryView,
  type LiveSnapshot, type MigView, type ScalingPoint, type TimelineEvent, type TrafficBucket, type TrafficPoint, type WaitingRoomPoint,
  type WaitingRoomStats, type WaitingRoomView,
} from '../types.js';
import { EventDetector } from './detector.js';
import { cpuFreshness, freshness } from './freshness.js';
import { instancePhase } from './phase.js';
import { instanceTraffic, toTrafficPoint } from './traffic.js';

export interface Append { traffic: TrafficPoint[]; scaling: ScalingPoint[]; cpu: CpuPoint[]; inventory: InventoryPoint[]; waitingRoom: WaitingRoomPoint[] }
export interface CollectorListener {
  tick(snapshot: LiveSnapshot, append: Append): void;
  timeline(event: TimelineEvent): void;
}

const AVG = '__avg__';
export const emptyAppend = (): Append => ({ traffic: [], scaling: [], cpu: [], inventory: [], waitingRoom: [] });
const WAITING_ROOM_NOT_DEPLOYED = 'Waiting room not deployed (WAITING_ROOM_URL is not set)';

/**
 * Polls every real telemetry source on its own interval, keeps the latest observation with its
 * timestamp, and records series/events while an experiment is active. A failing source only
 * marks its own freshness as stale/missing; it never blocks the others.
 */
export class Collector {
  private experiment: Experiment | null = null;
  private detector = new EventDetector();
  private timers: NodeJS.Timeout[] = [];
  private stopped = false;

  private mig: { reading: MigReading; at: number } | null = null;
  private migError: string | null = null;
  private lb: { health: Map<string, string>; at: number } | null = null;
  private cpuLatest = new Map<string, { t: number; value: number }>();
  private cpuSeen = new Set<string>();
  private cpuError: string | null = null;
  private guest = new Map<string, { info: InstanceInfo; at: number }>();
  private inventory: { view: InventoryView; at: number } | null = null;
  private inventoryError: string | null = null;
  private room: { stats: WaitingRoomStats; at: number } | null = null;
  private roomError: string | null = null;
  private buckets: TrafficBucket[] = [];
  private lastBucketAt: number | null = null;
  private bucketSeconds = new Set<number>();
  private unwatchTraffic: (() => void) | null = null;
  private generator: GeneratorStatus | null = null;
  private rejectedTotal = 0;

  private pending = emptyAppend();
  private unflushed = emptyAppend();
  private listeners = new Set<CollectorListener>();

  constructor(
    private readonly config: AppConfig,
    private readonly infra: InfraProvider,
    private readonly store: LabStore,
    private readonly inventoryReader: InventoryReader,
    private readonly log: { warn: (o: object, m: string) => void },
    private readonly waitingRoom = new WaitingRoomClient(config.waitingRoom.baseUrl, config.ticketing.adminToken),
  ) {}

  subscribe(listener: CollectorListener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  start() {
    const i = this.config.intervals;
    this.every(i.migMs, () => this.pollMig());
    this.every(i.lbMs, () => this.pollLb());
    this.every(i.cpuMs, () => this.pollCpu());
    this.every(i.instanceInfoMs, () => this.pollInstanceInfo());
    this.every(i.inventoryMs, () => this.pollInventory());
    if (this.waitingRoom.available) this.every(i.waitingRoomMs, () => this.pollWaitingRoom());
    this.every(2000, () => this.pollGenerator());
    this.every(i.tickMs, async () => this.tick());
    this.every(10_000, () => this.flushSeries());
  }

  stop() { this.stopped = true; this.timers.forEach(clearTimeout); this.unwatchTraffic?.(); }

  private every(ms: number, fn: () => Promise<void>) {
    const run = async () => {
      if (this.stopped) return;
      try { await fn(); } catch (error) { this.log.warn({ err: String(error) }, 'collector task failed'); }
      if (!this.stopped) this.timers.push(setTimeout(run, ms));
    };
    void run();
  }

  /** Called by the experiment service whenever the experiment document changes. */
  setExperiment(experiment: Experiment | null) {
    const previous = this.experiment;
    this.experiment = experiment;
    if (experiment?.id === previous?.id) return;
    if (previous) void this.flushSeries(previous.id);
    this.unwatchTraffic?.();
    this.unwatchTraffic = null;
    this.detector = new EventDetector();
    this.buckets = [];
    this.bucketSeconds.clear();
    this.lastBucketAt = null;
    this.inventory = null;
    this.rejectedTotal = 0;
    this.pending = emptyAppend();
    if (experiment) {
      this.unwatchTraffic = this.store.watchTrafficBuckets(experiment.id, (b) => this.onBucket(b),
        (e) => this.log.warn({ err: String(e) }, 'traffic watch failed'));
    }
  }

  get detectorForExperiment() { return this.detector; }

  private get recording() { return this.experiment !== null && ACTIVE_STATES.includes(this.experiment.state); }

  async recordEvent(event: TimelineEvent) {
    if (!this.experiment) return;
    try { await this.store.addEvent(this.experiment.id, event); } catch (e) { this.log.warn({ err: String(e) }, 'event persist failed'); }
    for (const l of this.listeners) l.timeline(event);
  }

  private async recordEvents(events: TimelineEvent[]) {
    if (!this.recording) return;
    for (const e of events) await this.recordEvent(e);
  }

  // ---- sources -------------------------------------------------------------------------------

  private async pollMig() {
    if (!this.infra.available) return;
    try {
      this.mig = { reading: await this.infra.readMig(), at: Date.now() };
      this.migError = null;
    } catch (error) {
      this.migError = String(error);
      return;
    }
    const view = this.migView()!;
    const instances = this.instanceViews();
    if (this.recording) {
      const point: ScalingPoint = {
        t: this.mig.at, actual: view.actualCount, running: view.runningCount, healthy: view.healthyCount,
        target: view.targetSize, min: view.autoscaler.minReplicas, max: view.autoscaler.maxReplicas,
      };
      this.pending.scaling.push(point);
      this.unflushed.scaling.push(point);
      await this.recordEvents(this.detector.observeMig(view, instances));
    }
  }

  private async pollLb() {
    if (!this.infra.available || !this.mig) return;
    this.lb = { health: await this.infra.readBackendHealth(this.mig.reading.instanceGroupUrl), at: Date.now() };
  }

  private async pollCpu() {
    if (!this.infra.available) return;
    let samples;
    try {
      samples = await this.infra.readCpu(15);
      this.cpuError = null;
    } catch (error) {
      this.cpuError = String(error);
      return;
    }
    const byTime = new Map<number, number[]>();
    for (const s of samples.sort((a, b) => a.t - b.t)) {
      const latest = this.cpuLatest.get(s.instance);
      if (!latest || s.t > latest.t) this.cpuLatest.set(s.instance, { t: s.t, value: s.value });
      byTime.set(s.t, [...(byTime.get(s.t) ?? []), s.value]);
      const key = `${s.instance}@${s.t}`;
      if (this.recording && !this.cpuSeen.has(key)) {
        this.cpuSeen.add(key);
        const point = { t: s.t, instance: s.instance, value: round3(s.value) };
        this.pending.cpu.push(point);
        this.unflushed.cpu.push(point);
      }
    }
    for (const [t, values] of [...byTime.entries()].sort((a, b) => a[0] - b[0])) {
      const key = `${AVG}@${t}`;
      const avg = values.reduce((a, b) => a + b, 0) / values.length;
      if (this.recording && !this.cpuSeen.has(key)) {
        this.cpuSeen.add(key);
        const point = { t, instance: AVG, value: round3(avg) };
        this.pending.cpu.push(point);
        this.unflushed.cpu.push(point);
        const target = this.mig?.reading.autoscaler.cpuTarget;
        if (target) await this.recordEvents(this.detector.observeCpu(avg, t, target));
      }
    }
    // Drop instances that no longer exist so their last sample is not shown as current.
    const names = new Set(this.mig?.reading.instances.map((i) => i.name) ?? []);
    for (const name of this.cpuLatest.keys()) if (this.mig && !names.has(name)) this.cpuLatest.delete(name);
  }

  private async pollInstanceInfo() {
    if (!this.mig) return;
    await Promise.all(this.mig.reading.instances.map(async (i) => {
      const ip = this.mig?.reading.details.get(i.name)?.privateIp;
      if (!ip || i.instanceStatus !== 'RUNNING') return;
      try {
        this.guest.set(i.name, { info: await fetchInstanceInfo(ip, this.config.ticketing.adminPort), at: Date.now() });
      } catch {
        // Not reachable yet (booting) or overloaded: keep the previous reading with its timestamp.
      }
    }));
    for (const name of this.guest.keys()) if (!this.mig.reading.details.has(name)) this.guest.delete(name);
  }

  private async pollInventory() {
    const eventId = this.experiment?.eventId;
    if (!eventId || !this.inventoryReader.available) return;
    try {
      const view = await this.inventoryReader.read(eventId);
      this.inventory = { view, at: Date.now() };
      this.inventoryError = null;
      if (this.recording) {
        const point: InventoryPoint = { t: this.inventory.at, capacity: view.capacity, available: view.available, held: view.held,
          sold: view.sold, expired: view.expiredReservations, rejected: this.rejectedTotal };
        this.pending.inventory.push(point);
        this.unflushed.inventory.push(point);
        await this.recordEvents(this.detector.observeInventory(view));
      }
    } catch (error) {
      this.inventoryError = String(error);
    }
  }

  private async pollWaitingRoom() {
    let stats: WaitingRoomStats;
    try {
      stats = await this.waitingRoom.stats();
      this.room = { stats, at: Date.now() };
      this.roomError = null;
    } catch (error) {
      this.roomError = String(error);
      return;
    }
    // Series and events only describe the room while it serves this experiment's event.
    const eventId = this.experiment?.eventId;
    if (!this.recording || !eventId || !stats.enabled || stats.eventId !== eventId) return;
    const point: WaitingRoomPoint = {
      t: this.room.at, waiting: stats.waiting, active: stats.active, capacity: stats.capacity, healthyServers: stats.healthyServers,
      admissionRate: stats.admissionRatePerSecond, joinedTotal: stats.joinedTotal, admittedTotal: stats.admittedTotal,
      soldOutTotal: stats.soldOutTotal, soldOut: stats.soldOut,
      waitP50Ms: stats.waitMs.p50, waitP95Ms: stats.waitMs.p95, waitMaxMs: stats.waitMs.max,
      ...adaptiveLimits(stats),
    };
    this.pending.waitingRoom.push(point);
    this.unflushed.waitingRoom.push(point);
    await this.recordEvents(this.detector.observeWaitingRoom(stats));
  }

  waitingRoomView(now = Date.now()): WaitingRoomView | null {
    if (!this.room) return null;
    return { ...this.room.stats, observedAt: new Date(this.room.at).toISOString(), freshness: this.waitingRoomFreshness(now) };
  }

  private waitingRoomFreshness(now: number) {
    return freshness(this.room?.at ?? null, now, 3, 15, this.roomError ?? (this.waitingRoom.available ? undefined : WAITING_ROOM_NOT_DEPLOYED));
  }

  private async pollGenerator() {
    this.generator = await this.store.generatorStatus();
  }

  private onBucket(bucket: TrafficBucket) {
    if (this.bucketSeconds.has(bucket.second)) return;
    this.bucketSeconds.add(bucket.second);
    this.lastBucketAt = Date.now();
    this.buckets.push(bucket);
    this.buckets.sort((a, b) => a.second - b.second);
    if (this.buckets.length > 10) this.buckets.shift();
    this.rejectedTotal += bucket.business.reservationsRejected;
    const point = toTrafficPoint(bucket);
    this.pending.traffic.push(point);
    void this.recordEvents(this.detector.observeTraffic(point));
  }

  // ---- views ---------------------------------------------------------------------------------

  migView(): MigView | null {
    if (!this.mig) return null;
    const r = this.mig.reading;
    const instances = this.instanceViews();
    const b = this.config.baseline;
    const a = r.autoscaler;
    return {
      observedAt: new Date(this.mig.at).toISOString(),
      name: this.config.gcp.migName,
      zone: this.config.gcp.zone,
      targetSize: r.targetSize,
      actualCount: r.instances.length,
      runningCount: r.instances.filter((i) => i.instanceStatus === 'RUNNING').length,
      healthyCount: instances.filter((i) => i.phase === 'healthy').length,
      isStable: r.isStable,
      autoscaler: a,
      baseline: b,
      overridesActive: a.minReplicas !== b.minReplicas || a.maxReplicas !== b.maxReplicas
        || Math.abs(a.cpuTarget - b.cpuTarget) > 1e-6 || a.initPeriodSeconds !== b.initPeriodSeconds
        || (a.scaleInWindowSeconds ?? b.scaleInWindowSeconds) !== b.scaleInWindowSeconds,
      approvedMaxInstances: this.config.approvedMaxInstances,
    };
  }

  instanceViews(): InstanceView[] {
    if (!this.mig) return [];
    const perInstance = instanceTraffic(this.buckets.slice(-5), 5);
    const trafficFresh = this.lastBucketAt !== null && Date.now() - this.lastBucketAt < 10_000;
    return this.mig.reading.instances.map((i) => {
      const details = this.mig!.reading.details.get(i.name);
      const lbHealth = this.lb?.health.get(i.name) ?? null;
      const cpu = this.cpuLatest.get(i.name);
      const guest = this.guest.get(i.name);
      const traffic = trafficFresh ? perInstance.get(i.name) : undefined;
      return {
        name: i.name,
        id: i.id,
        zone: this.config.gcp.zone,
        privateIp: details?.privateIp ?? null,
        createdAt: details?.createdAt ?? null,
        vmStatus: i.instanceStatus,
        currentAction: i.currentAction,
        migHealth: i.migHealth,
        lbHealth,
        phase: instancePhase(i.instanceStatus, i.currentAction, i.migHealth, lbHealth),
        cpu: { value: cpu ? round3(cpu.value) : null, sampleTime: cpu ? new Date(cpu.t).toISOString() : null, source: 'cloud_monitoring' },
        guestCpu: guest?.info.guest_cpu
          ? { value: round3(guest.info.guest_cpu.percent / 100), sampleTime: guest.info.guest_cpu.sampled_at, source: 'instance_info' }
          : null,
        traffic: traffic ?? { rps: 0, avgLatencyMs: null, errorRate: null, windowSeconds: 5 },
        appVersion: guest?.info.version ?? null,
        ready: guest ? guest.info.ready : null,
      };
    });
  }

  /** Private IP of a VM the load balancer considers healthy, for admin calls. */
  adminBaseUrl(): string | null {
    if (this.config.ticketing.adminBaseUrl) return this.config.ticketing.adminBaseUrl;
    const candidates = this.instanceViews().filter((i) => i.privateIp && i.vmStatus === 'RUNNING');
    const best = candidates.find((i) => i.phase === 'healthy') ?? candidates.find((i) => i.ready);
    return best ? `http://${best.privateIp}:${this.config.ticketing.adminPort}` : null;
  }

  snapshot(): LiveSnapshot {
    const now = Date.now();
    const latest = this.buckets.at(-1);
    const point = latest ? toTrafficPoint(latest) : null;
    const trafficFresh = this.lastBucketAt !== null && now - this.lastBucketAt < 15_000;
    const instances = this.instanceViews();
    const live = instances.filter((i) => i.phase !== 'terminating');
    const cpuValues = live.map((i) => this.cpuLatest.get(i.name)).filter((c): c is { t: number; value: number } => Boolean(c));
    const cpuTime = cpuValues.length ? Math.min(...cpuValues.map((c) => c.t)) : null;
    const heartbeat = this.generator?.heartbeatAt ? Date.parse(this.generator.heartbeatAt) : null;
    const show = <T>(v: T) => (trafficFresh ? v : null);

    return {
      serverTime: new Date(now).toISOString(),
      experiment: this.experiment,
      mig: this.migView(),
      instances,
      traffic: {
        second: show(point?.second ?? null),
        targetRate: show(point?.targetRate ?? null),
        achievedArrivalRate: show(point?.achieved ?? null),
        rps: show(point?.rps ?? null),
        successRps: show(point?.ok ?? null),
        errorRps: show(point?.errors ?? null),
        businessRejectRps: show(point?.business ?? null),
        p50: show(point?.p50 ?? null),
        p95: show(point?.p95 ?? null),
        p99: show(point?.p99 ?? null),
        activeVUs: show(point?.activeVUs ?? null),
        dropped: show(point?.dropped ?? null),
        queueWaiting: show(point?.queueWaiting ?? null),
        queueJoinedRate: show(point?.queueJoined ?? null),
        queueAdmittedRate: show(point?.queueAdmitted ?? null),
        queueWaitP95: show(point?.queueWaitP95 ?? null),
        generator: {
          state: heartbeat && now - heartbeat < 15_000 ? this.generator!.state : this.generator ? 'offline' : 'unknown',
          heartbeatAgeSeconds: heartbeat ? Math.round((now - heartbeat) / 1000) : null,
          message: this.generator?.message ?? null,
        },
      },
      inventory: this.inventory?.view ?? null,
      waitingRoom: this.waitingRoomView(now),
      cpu: {
        average: cpuValues.length ? round3(cpuValues.reduce((s, c) => s + c.value, 0) / cpuValues.length) : null,
        sampleTime: cpuTime ? new Date(cpuTime).toISOString() : null,
        target: this.mig?.reading.autoscaler.cpuTarget ?? null,
      },
      freshness: {
        traffic: freshness(this.lastBucketAt, now, 3, 15, this.experiment?.state === 'running' ? undefined : 'No traffic is running'),
        mig: freshness(this.mig?.at ?? null, now, 15, 60, this.migError ?? (this.infra.available ? undefined : 'Compute API not configured')),
        cpu: { ...cpuFreshness(cpuTime, now), ...(this.cpuError ? { note: this.cpuError } : {}) },
        inventory: freshness(this.inventory?.at ?? null, now, 6, 30, this.inventoryError
          ?? (this.experiment?.eventId ? undefined : 'No experiment event prepared')),
        lb: freshness(this.lb?.at ?? null, now, 30, 120, this.infra.available ? undefined : 'Compute API not configured'),
        waitingRoom: this.waitingRoomFreshness(now),
      },
    };
  }

  private async tick() {
    const append = this.pending;
    this.pending = emptyAppend();
    const snapshot = this.snapshot();
    for (const l of this.listeners) l.tick(snapshot, append);
  }

  async flushSeries(experimentId = this.experiment?.id) {
    const chunk = this.unflushed;
    this.unflushed = emptyAppend();
    if (!experimentId) return;
    try {
      await this.store.appendSeries(experimentId, { scaling: chunk.scaling, cpu: chunk.cpu, inventory: chunk.inventory, waitingRoom: chunk.waitingRoom });
    } catch (error) {
      this.log.warn({ err: String(error) }, 'series flush failed');
      this.unflushed.scaling.unshift(...chunk.scaling);
      this.unflushed.cpu.unshift(...chunk.cpu);
      this.unflushed.inventory.unshift(...chunk.inventory);
      this.unflushed.waitingRoom.unshift(...chunk.waitingRoom);
    }
  }

  get rejectedSoldOutTotal() { return this.rejectedTotal; }
  get currentInventory() { return this.inventory?.view ?? null; }
  get currentMig() { return this.migView(); }
  get generatorStatus() { return this.generator; }
}

/**
 * Per-server learned limits for the series (adaptive mode only; all null otherwise or when the queue
 * reports no limit), keyed by server name so the chart can draw one line per server.
 */
export function adaptiveLimits(stats: WaitingRoomStats): Pick<WaitingRoomPoint, 'limits' | 'decreases' | 'limitAvg' | 'limitMin' | 'limitMax'> {
  const withLimit = stats.admissionMode === 'adaptive' ? stats.servers.filter((s) => s.name && s.limit !== null) : [];
  if (!withLimit.length) return { limits: null, decreases: null, limitAvg: null, limitMin: null, limitMax: null };
  const values = withLimit.map((s) => s.limit as number);
  const decreases: Record<string, number> = {};
  for (const s of withLimit) if (s.decreases !== null) decreases[s.name] = s.decreases;
  return {
    limits: Object.fromEntries(withLimit.map((s) => [s.name, s.limit as number])),
    decreases: Object.keys(decreases).length ? decreases : null,
    limitAvg: Math.round((values.reduce((a, b) => a + b, 0) / values.length) * 10) / 10,
    limitMin: Math.min(...values),
    limitMax: Math.max(...values),
  };
}

// All utilisation values are fractions 0..1, matching the autoscaler's cpuTarget.
const round3 = (n: number) => Math.round(n * 1000) / 1000;
