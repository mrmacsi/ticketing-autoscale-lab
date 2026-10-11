import { randomUUID } from 'node:crypto';
import type {
  Attribution, InstanceView, InventoryView, MigView, TimelineEvent, TimelineKind, TimelineSource, TrafficPoint, WaitingRoomServer,
  WaitingRoomStats,
} from '../types.js';

interface Milestones { running: boolean; migHealthy: boolean; lbHealthy: boolean; served: boolean; terminating: boolean }

const RPS_MILESTONES = [10, 25, 50, 100, 200, 400, 800, 1600];
/** Successive observations with nobody waiting before the room is reported empty (avoids 0 ↔ 1 flapping). */
const EMPTY_OBSERVATIONS = 3;

/** At most one "Adaptive limit … cut" event in this window; the limit can be cut every few seconds. */
export const ADAPTIVE_CUT_EVENT_INTERVAL_MS = 10_000;

/** "ticketing-scale-lab-app-ab12" → "app-ab12" (MIG base name tail + random suffix); short names stay as they are. */
export const shortServerName = (name: string) => {
  const parts = name.split('-');
  return parts.length > 2 ? parts.slice(-2).join('-') : name;
};

type AdaptiveServerState = Pick<WaitingRoomServer, 'limit' | 'phase' | 'decreases'>;

const customers = (n: number) => `${n.toLocaleString('en-US')} customer${n === 1 ? '' : 's'}`;

/**
 * Turns successive real observations into timeline events. It only reports transitions it has
 * actually observed; nothing is scheduled or predicted. Operator/baseline changes are recorded via
 * noteOperatorChange so capacity changes can be attributed honestly.
 */
export class EventDetector {
  private instances = new Map<string, Milestones>();
  private initialised = false;
  private lastTarget: number | null = null;
  private lastRecommended: number | null = null;
  private lastAutoscalerMessages = '';
  private cpuAbove: boolean | null = null;
  private lastCpuSampleTime = 0;
  private rpsReached = new Set<number>();
  private soldOut = false;
  private expiryReported = false;
  private droppedReported = false;
  private errorBurstAt = 0;
  private operatorChanges: Array<{ at: number; minReplicas?: number; maxReplicas?: number; reason: string }> = [];
  private room: { waiting: number; capacity: number; soldOut: boolean; healthyServers: number } | null = null;
  /** Adaptive mode: each server's previous observation, its last cut event time and cuts not reported since. */
  private adaptiveServers = new Map<string, AdaptiveServerState>();
  private cutEventAt = new Map<string, number>();
  private cutsSuppressed = new Map<string, number>();
  private slowStartLeft = new Set<string>();
  private roomOpen = false;
  private roomEmptySince: number | null = null;
  private roomEmptyCount = 0;

  constructor(private readonly now: () => number = Date.now) {}

  noteOperatorChange(change: { minReplicas?: number; maxReplicas?: number; reason: string }) {
    this.operatorChanges.push({ ...change, at: this.now() });
  }

  observeMig(mig: MigView, instances: InstanceView[]): TimelineEvent[] {
    const events: TimelineEvent[] = [];
    const seen = new Set<string>();

    if (!this.initialised) {
      // Starting state is a fact, not a transition: record it once without inventing history.
      this.initialised = true;
      this.lastTarget = mig.targetSize;
      this.lastRecommended = mig.autoscaler.recommendedSize;
      for (const i of instances) this.instances.set(i.name, this.milestonesOf(i));
      events.push(this.event('scaling', 'compute_api',
        `Observed ${instances.length} instance(s), MIG target size ${mig.targetSize} (min ${mig.autoscaler.minReplicas}, max ${mig.autoscaler.maxReplicas})`,
        null, { instances: instances.map((i) => i.name), targetSize: mig.targetSize }));
      return events;
    }

    if (this.lastTarget !== null && mig.targetSize !== this.lastTarget) {
      const attribution = this.attribute(this.lastTarget, mig.targetSize, mig);
      const verb = mig.targetSize > this.lastTarget ? 'increased' : 'decreased';
      const who = attribution === 'autoscaler' ? 'Autoscaler' : attribution === 'baseline' ? 'Baseline change (control plane)' : 'Operator change';
      events.push(this.event('scaling', 'compute_api', `${who} ${verb} MIG target size ${this.lastTarget} → ${mig.targetSize}`,
        attribution, { from: this.lastTarget, to: mig.targetSize, recommendedSize: mig.autoscaler.recommendedSize }));
    }
    this.lastTarget = mig.targetSize;

    const recommended = mig.autoscaler.recommendedSize;
    if (recommended !== null && this.lastRecommended !== null && recommended !== this.lastRecommended) {
      events.push(this.event('scaling', 'compute_api', `Autoscaler recommended size ${this.lastRecommended} → ${recommended}`,
        'autoscaler', { from: this.lastRecommended, to: recommended }));
    }
    this.lastRecommended = recommended;

    const messages = mig.autoscaler.messages.join(' | ');
    if (messages && messages !== this.lastAutoscalerMessages) {
      events.push(this.event('scaling', 'compute_api', `Autoscaler status: ${messages}`, 'autoscaler'));
    }
    this.lastAutoscalerMessages = messages;

    for (const instance of instances) {
      seen.add(instance.name);
      const known = this.instances.get(instance.name);
      if (!known) {
        const attribution = this.attributeNewInstance(mig);
        events.push(this.event('instance', 'compute_api', `${instance.name} created (MIG action ${instance.currentAction}, VM ${instance.vmStatus})`,
          attribution, { instance: instance.name }));
        this.instances.set(instance.name, { running: false, migHealthy: false, lbHealthy: false, served: false, terminating: false });
      }
      const m = this.instances.get(instance.name)!;
      if (!m.running && instance.vmStatus === 'RUNNING') {
        m.running = true;
        events.push(this.event('instance', 'compute_api', `${instance.name} VM is RUNNING (booted)`, null, { instance: instance.name }));
      }
      if (!m.migHealthy && instance.migHealth === 'HEALTHY') {
        m.migHealthy = true;
        events.push(this.event('health', 'compute_api', `${instance.name} passed the MIG health check`, null, { instance: instance.name }));
      }
      if (!m.lbHealthy && instance.lbHealth === 'HEALTHY') {
        m.lbHealthy = true;
        events.push(this.event('health', 'load_balancer', `${instance.name} became HEALTHY in the load-balancer backend`, null, { instance: instance.name }));
      }
      if (!m.terminating && instance.phase === 'terminating') {
        m.terminating = true;
        events.push(this.event('instance', 'compute_api', `${instance.name} termination started (${instance.currentAction})`,
          this.attributeRemoval(mig), { instance: instance.name }));
      }
      if (!m.served && instance.traffic.rps > 0) {
        m.served = true;
        events.push(this.event('instance', 'generator', `${instance.name} served its first synthetic requests`, null, { instance: instance.name }));
      }
    }

    for (const name of [...this.instances.keys()]) {
      if (!seen.has(name)) {
        this.instances.delete(name);
        events.push(this.event('instance', 'compute_api', `${name} removed from the MIG (terminated)`, null, { instance: name }));
      }
    }
    return events;
  }

  observeCpu(average: number, sampleTime: number, target: number): TimelineEvent[] {
    if (sampleTime <= this.lastCpuSampleTime) return [];
    this.lastCpuSampleTime = sampleTime;
    const above = average > target;
    const events: TimelineEvent[] = [];
    if (this.cpuAbove !== null && above !== this.cpuAbove) {
      const at = new Date(sampleTime).toISOString().substring(11, 19);
      events.push(this.event('cpu', 'cloud_monitoring',
        `Average CPU ${Math.round(average * 100)}% ${above ? 'exceeded' : 'fell below'} the ${Math.round(target * 100)}% target (sample at ${at} UTC)`,
        null, { average, sampleTime: new Date(sampleTime).toISOString() }));
    }
    this.cpuAbove = above;
    return events;
  }

  observeTraffic(point: TrafficPoint): TimelineEvent[] {
    const events: TimelineEvent[] = [];
    // One event per observation: the highest newly crossed level.
    const crossed = RPS_MILESTONES.filter((level) => point.rps >= level && !this.rpsReached.has(level));
    crossed.forEach((level) => this.rpsReached.add(level));
    if (crossed.length) {
      events.push(this.event('traffic', 'generator',
        `HTTP traffic reached ${crossed.at(-1)} req/s (measured ${point.rps} req/s, ${point.achieved} arrivals/s)`,
        null, { rps: point.rps, second: point.second }));
    }
    if (!this.droppedReported && point.dropped > 0) {
      this.droppedReported = true;
      events.push(this.event('error', 'generator', `Load generator dropped ${point.dropped} arrival(s): no free virtual user (generator capacity, not app capacity)`,
        null, { second: point.second }));
    }
    const total = point.ok + point.errors + point.business;
    if (total >= 20 && point.errors / total > 0.05 && point.t - this.errorBurstAt > 60_000) {
      this.errorBurstAt = point.t;
      events.push(this.event('error', 'generator', `${point.errors} of ${total} requests failed in one second (HTTP 5xx / timeouts / network)`,
        null, { second: point.second }));
    }
    return events;
  }

  observeInventory(view: InventoryView): TimelineEvent[] {
    const events: TimelineEvent[] = [];
    if (!this.soldOut && view.capacity > 0 && view.sold === view.capacity) {
      this.soldOut = true;
      events.push(this.event('inventory', 'database', `Inventory sold out (${view.sold}/${view.capacity} sold)`, null, { sold: view.sold }));
    }
    if (!this.expiryReported && view.expiredReservations > 0) {
      this.expiryReported = true;
      events.push(this.event('inventory', 'database', 'First reservations expired and released their held tickets', null,
        { expired: view.expiredReservations }));
    }
    if (view.invariantViolations > 0) {
      events.push(this.event('error', 'database', `Inventory invariant violations detected: ${view.invariantViolations}`, null,
        { violations: view.invariantViolations }));
    }
    return events;
  }

  /**
   * Queue-server stats for the experiment's event (docs/waiting-room.md 3.7). The first observation is
   * a starting state; only changes between successive observations produce events.
   */
  observeWaitingRoom(stats: WaitingRoomStats): TimelineEvent[] {
    const events: TimelineEvent[] = [];
    const prev = this.room;
    const adaptive = stats.admissionMode === 'adaptive';
    this.room = { waiting: stats.waiting, capacity: stats.capacity, soldOut: stats.soldOut, healthyServers: stats.healthyServers };
    const adaptiveEvents = this.observeAdaptiveServers(stats, prev !== null);
    if (!prev) {
      this.roomOpen = stats.waiting > 0 && !stats.soldOut;
      return events;
    }
    const servers = `${stats.healthyServers} healthy server${stats.healthyServers === 1 ? '' : 's'}`;
    // Adaptive capacity moves with every learned limit (each second), so only a change in the number of healthy
    // servers is a timeline event there; per-server cuts and slow-start exits are reported separately below.
    const capacityEvent = adaptive ? stats.healthyServers !== prev.healthyServers : stats.capacity !== prev.capacity;
    if (capacityEvent) {
      events.push(this.event('waiting_room', 'waiting_room', `Waiting room capacity ${prev.capacity} → ${stats.capacity} (${servers})`, null,
        { from: prev.capacity, to: stats.capacity, healthyServers: stats.healthyServers, activeUsersPerServer: stats.activeUsersPerServer,
          admissionMode: stats.admissionMode ?? 'fixed' }));
    }
    events.push(...adaptiveEvents);
    if (stats.soldOut && !prev.soldOut) {
      // soldOutTotal counts everyone told so far; fall back to the line seen just before the declaration.
      const told = stats.soldOutTotal > 0 ? stats.soldOutTotal : prev.waiting;
      events.push(this.event('waiting_room', 'waiting_room', `Waiting room declared sold out: ${customers(told)} told`, null,
        { told, soldOutTotal: stats.soldOutTotal, waitingBefore: prev.waiting }));
      this.roomOpen = false;
      this.roomEmptyCount = 0;
      return events;
    }
    if (stats.soldOut) return events;
    if (!this.roomOpen && stats.waiting > 0) {
      this.roomOpen = true;
      this.roomEmptyCount = 0;
      events.push(this.event('waiting_room', 'waiting_room', `Waiting room opened: ${customers(stats.waiting)} waiting`, null,
        { waiting: stats.waiting, active: stats.active, capacity: stats.capacity }));
    } else if (this.roomOpen && stats.waiting === 0) {
      if (this.roomEmptyCount === 0) this.roomEmptySince = this.now();
      this.roomEmptyCount += 1;
      if (this.roomEmptyCount >= EMPTY_OBSERVATIONS) {
        this.roomOpen = false;
        this.roomEmptyCount = 0;
        events.push(this.event('waiting_room', 'waiting_room', 'Waiting room empty', null,
          { emptySince: new Date(this.roomEmptySince ?? this.now()).toISOString(), admittedTotal: stats.admittedTotal, joinedTotal: stats.joinedTotal }));
      }
    } else if (stats.waiting > 0) {
      this.roomEmptyCount = 0;
    }
    return events;
  }

  /**
   * Adaptive mode: the queue keeps ONE limit per healthy server, decided by the busiest server (the
   * load balancer gives every server the same share), so limit, phase and cut counter are the same on
   * every server entry. Compares that shared state with its previous observation; the first
   * observation is a starting state.
   */
  private observeAdaptiveServers(stats: WaitingRoomStats, report: boolean): TimelineEvent[] {
    const events: TimelineEvent[] = [];
    const shared = stats.admissionMode === 'adaptive' ? stats.servers.find((s) => s.name) : undefined;
    if (!shared) {
      if (stats.admissionMode !== 'adaptive') this.adaptiveServers.clear();
      return events;
    }
    const key = 'shared';
    const before = this.adaptiveServers.get(key);
    this.adaptiveServers.set(key, { limit: shared.limit, phase: shared.phase, decreases: shared.decreases });
    if (!report || !before) return events;

    // The queue's cut counter is authoritative; without it, a falling limit with a "decrease" decision is a cut.
    const cut = shared.decreases !== null && before.decreases !== null
      ? shared.decreases > before.decreases
      : shared.decision === 'decrease' && shared.limit !== null && before.limit !== null && shared.limit < before.limit;
    if (cut) {
      const last = this.cutEventAt.get(key);
      if (last !== undefined && this.now() - last < ADAPTIVE_CUT_EVENT_INTERVAL_MS) {
        this.cutsSuppressed.set(key, (this.cutsSuppressed.get(key) ?? 0) + 1);
      } else {
        const suppressed = this.cutsSuppressed.get(key) ?? 0;
        this.cutEventAt.set(key, this.now());
        this.cutsSuppressed.delete(key);
        events.push(this.event('waiting_room', 'waiting_room',
          `Adaptive limit cut ${before.limit ?? '?'} → ${shared.limit ?? '?'} per server (${shared.reason ?? 'reason not reported'})`, null,
          { from: before.limit, to: shared.limit, reason: shared.reason, decisionAt: shared.decisionAt, decreases: shared.decreases,
            healthyServers: stats.healthyServers, signals: Object.fromEntries(stats.servers.map((s) => [s.name, s.signals])),
            cutsNotReportedBefore: suppressed }));
      }
    }
    if (before.phase === 'slow_start' && shared.phase === 'steady' && !this.slowStartLeft.has(key)) {
      this.slowStartLeft.add(key);
      // The limit slow start had reached (the step that ends it usually cuts it right away).
      const reached = before.limit ?? shared.limit;
      events.push(this.event('waiting_room', 'waiting_room', `Adaptive limit left slow start at ${reached ?? '?'} per server`, null,
        { limitReached: reached, limitNow: shared.limit, reason: shared.reason }));
    }
    return events;
  }

  event(kind: TimelineKind, source: TimelineSource, message: string, attribution: Attribution = null, data?: Record<string, unknown>): TimelineEvent {
    return { id: `${this.now().toString(36)}-${randomUUID().slice(0, 8)}`, t: new Date(this.now()).toISOString(), kind, source, attribution, message, data };
  }

  private milestonesOf(i: InstanceView): Milestones {
    return { running: i.vmStatus === 'RUNNING', migHealthy: i.migHealth === 'HEALTHY', lbHealthy: i.lbHealth === 'HEALTHY',
      served: i.traffic.rps > 0, terminating: i.phase === 'terminating' };
  }

  /**
   * A growth is "baseline" when the control plane raised the minimum to at least the new size in
   * the last 15 minutes; a shrink is "operator" when it lowered the maximum below the old size.
   * Everything else is the autoscaler's own decision.
   */
  private attribute(from: number, to: number, mig: MigView): Attribution {
    const recent = this.operatorChanges.filter((c) => this.now() - c.at < 15 * 60_000);
    if (to > from && recent.some((c) => (c.minReplicas ?? 0) >= to) && mig.autoscaler.minReplicas >= to) return 'baseline';
    if (to < from && recent.some((c) => c.maxReplicas !== undefined && c.maxReplicas < from)) return 'operator';
    return 'autoscaler';
  }

  private attributeNewInstance(mig: MigView): Attribution {
    const recent = this.operatorChanges.filter((c) => this.now() - c.at < 15 * 60_000);
    return recent.some((c) => (c.minReplicas ?? 0) >= mig.targetSize) && mig.autoscaler.minReplicas >= mig.targetSize ? 'baseline' : 'autoscaler';
  }

  private attributeRemoval(mig: MigView): Attribution {
    const recent = this.operatorChanges.filter((c) => this.now() - c.at < 15 * 60_000);
    return recent.some((c) => c.maxReplicas !== undefined && c.maxReplicas <= mig.targetSize) ? 'operator' : 'autoscaler';
  }
}
