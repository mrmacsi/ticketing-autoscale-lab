import { randomBytes } from 'node:crypto';
import type { AppConfig } from '../config.js';
import type { AutoscalerPatch, InfraProvider } from '../infra/types.js';
import { buildReport } from '../report/report.js';
import { ConflictError, type LabStore } from '../store/types.js';
import type { Collector } from '../telemetry/collector.js';
import type { TicketingAdmin } from '../ticketing/admin.js';
import type { InventoryReader } from '../ticketing/inventory.js';
import { WaitingRoomClient, type WaitingRoomConfig } from '../ticketing/waitingRoom.js';
import { ACTIVE_STATES, waitingRoomOf, type Experiment, type ExperimentConfig, type ExperimentState, type TimelineEvent } from '../types.js';
import { CATEGORY_NAMES, expensiveReasons, splitInventory, validateExperiment } from './validation.js';

export class StateError extends Error {}
export class ConfirmationRequiredError extends Error {
  constructor(readonly reasons: string[]) { super('This configuration needs explicit confirmation'); }
}

type Logger = { info: (o: object, m: string) => void; warn: (o: object, m: string) => void; error: (o: object, m: string) => void };

const READY_TIMEOUT_MS = 15 * 60_000;
const GENERATOR_ACK_TIMEOUT_MS = 30_000;
const GENERATOR_STOP_TIMEOUT_MS = 20_000;

/**
 * Owns the experiment lifecycle:
 *   idle → preparing → ready → running → stopping → cooling_down → completed   (any → failed)
 * A Firestore lock guarantees a single active experiment; the generator is commanded through
 * Firestore and reports back through the experiment's `generator` map.
 */
/**
 * Shortest cool-down: long enough for holds left by the last customers to expire (reservation TTL + 5 s,
 * at least 30 s), never longer than the configured maximum.
 */
export function minObserveMs(c: ExperimentConfig): number {
  return Math.min(c.cooldownObserveSeconds, Math.max(30, c.tickets.reservationTtlSeconds + 5)) * 1000;
}

export class ExperimentService {
  private current: Experiment | null = null;
  private watchTimer: NodeJS.Timeout | null = null;
  private busy = false;
  private stopRequestedAt: number | null = null;
  private startRequestedAt: number | null = null;

  constructor(
    private readonly config: AppConfig,
    private readonly store: LabStore,
    private readonly infra: InfraProvider,
    private readonly collector: Collector,
    private readonly admin: TicketingAdmin,
    private readonly inventory: InventoryReader,
    private readonly log: Logger,
    private readonly sleep = (ms: number) => new Promise((r) => setTimeout(r, ms)),
    private readonly waitingRoom = new WaitingRoomClient(config.waitingRoom.baseUrl, config.ticketing.adminToken),
  ) {}

  get experiment() { return this.current; }

  /** Resume whatever was active when this process (re)started. */
  async init() {
    const id = await this.store.activeExperimentId();
    if (id) {
      const experiment = await this.store.getExperiment(id);
      if (experiment && ACTIVE_STATES.includes(experiment.state)) {
        this.setCurrent(experiment);
        await this.event('experiment', 'control_plane', `Control plane restarted; resumed experiment in state ${experiment.state}`);
        if (experiment.state === 'preparing') void this.waitUntilReady(experiment.config);
      } else if (experiment) {
        await this.store.releaseLock(id);
      }
    }
    this.watchTimer = setInterval(() => void this.watch(), 2000);
  }

  stop() { if (this.watchTimer) clearInterval(this.watchTimer); }

  async create(input: unknown, user: string): Promise<Experiment> {
    const config = validateExperiment(input, this.config);
    const reasons = expensiveReasons(config, this.config);
    if (reasons.length && !config.confirmExpensive) throw new ConfirmationRequiredError(reasons);

    const now = new Date();
    const id = `exp_${now.toISOString().replace(/[-:]/g, '').replace('T', '_').slice(0, 15)}_${randomBytes(2).toString('hex')}`;
    const experiment: Experiment = {
      id, name: config.name, state: 'preparing', config, createdBy: user, createdAt: now.toISOString(),
      readyAt: null, trafficStartedAt: null, trafficStoppedAt: null, completedAt: null, failedReason: null,
      eventId: null, categories: [], generator: null, initialServerCount: null, report: null,
    };
    await this.store.createExperiment(experiment, async (otherId) => {
      const other = await this.store.getExperiment(otherId);
      return Boolean(other && ACTIVE_STATES.includes(other.state));
    });
    this.setCurrent(experiment);
    await this.event('experiment', 'control_plane', `Experiment "${config.name}" created by ${user}`, 'operator',
      { preset: config.preset, expensiveConfirmed: reasons.length > 0 ? reasons : undefined });
    void this.prepare(config).catch((error) => this.fail(`Preparation failed: ${String(error instanceof Error ? error.message : error)}`));
    return experiment;
  }

  /** True once the instance group is back at the experiment's minimum with no extra VM left (or there is no instance group to watch). */
  private backToMinimum(c: ExperimentConfig): boolean {
    if (!this.infra.available) return true;
    const mig = this.collector.currentMig;
    return mig !== null && mig.targetSize <= c.servers.min && mig.actualCount <= c.servers.min;
  }

  private async prepare(config: ExperimentConfig) {
    const s = config.servers;
    await this.applyAutoscaler({
      minReplicas: Math.max(s.min, s.initial), maxReplicas: s.max, cpuTarget: s.cpuTarget,
      initPeriodSeconds: s.initPeriodSeconds, scaleInWindowSeconds: s.scaleInWindowSeconds,
    }, s.initial > s.min ? `Baseline: minimum raised to ${s.initial} so the experiment starts with ${s.initial} server(s)` : 'Experiment autoscaler settings applied');

    const adminReadyBy = Date.now() + READY_TIMEOUT_MS;
    while (!this.collector.adminBaseUrl()) {
      if (Date.now() > adminReadyBy) throw new Error('No application instance became reachable for preparation');
      await this.sleep(3000);
    }
    const t = config.tickets;
    const capacities = splitInventory(t.totalInventory, t.categoryCount);
    const prepared = await this.admin.prepare({
      event_name: t.eventName,
      categories: capacities.map((capacity, i) => ({ name: CATEGORY_NAMES[i], price_cents: t.pricesCents[i], capacity })),
      settings: {
        reservation_ttl_seconds: t.reservationTtlSeconds,
        payment_delay_min_ms: t.paymentDelayMinMs,
        payment_delay_max_ms: t.paymentDelayMaxMs,
        payment_failure_probability: t.paymentFailureProbability,
        synthetic_cpu_max_ms: config.syntheticCpuMaxMs,
        // POST /api/reservations requires a queue admission token while this is on (docs/waiting-room.md 3.3).
        waiting_room_required: waitingRoomOf(config).enabled,
      },
    });
    await this.patch({ eventId: prepared.event.id, categories: prepared.event.categories });
    await this.event('inventory', 'control_plane', `Event "${t.eventName}" created with ${t.totalInventory} tickets in ${t.categoryCount} categor${t.categoryCount === 1 ? 'y' : 'ies'}`,
      null, { eventId: prepared.event.id, syntheticCpuMaxMs: config.syntheticCpuMaxMs });
    await this.configureWaitingRoom(config, prepared.event.id);
    if (config.syntheticCpuMaxMs > 0) {
      await this.event('config', 'control_plane', `Demo-only synthetic CPU work ENABLED (max ${config.syntheticCpuMaxMs} ms per request)`, 'operator');
    }
    await this.waitUntilReady(config);
  }

  private waitingRoomBody(config: ExperimentConfig | null | undefined, enabled: boolean, eventId: string | null): WaitingRoomConfig {
    const wr = waitingRoomOf(config);
    return {
      enabled,
      ...(eventId ? { eventId } : {}),
      activeUsersPerServer: wr.activeUsersPerServer,
      admissionTtlSeconds: wr.admissionTtlSeconds,
      // Every outstanding hold has been bought or returned before waiting customers are told "sold out".
      soldOutGraceSeconds: Math.min(3600, (config?.tickets.reservationTtlSeconds ?? 0) + 5),
      // Admission mode only matters while the room is on; disabling bodies stay as they were.
      // Adaptive: activeUsersPerServer above is each new server's starting limit (docs/waiting-room.md 3.4).
      ...(enabled ? { admissionMode: wr.mode, adaptive: { maxPerServer: wr.maxUsersPerServer } } : {}),
    };
  }

  /** Points the queue at the new event (enabled) or makes sure it is off (disabled). */
  private async configureWaitingRoom(config: ExperimentConfig, eventId: string) {
    const wr = waitingRoomOf(config);
    const body = this.waitingRoomBody(config, wr.enabled, eventId);
    if (!wr.enabled) {
      // waiting_room_required=false already went out with the prepare settings; switch the queue off too.
      if (this.waitingRoom.available) {
        await this.waitingRoom.configure(body).catch((error) => this.log.warn({ err: String(error) }, 'could not disable the waiting room'));
      }
      return;
    }
    // Enabled: a queue that cannot be configured would leave every reservation refused, so preparation fails.
    await this.waitingRoom.configure(body);
    const admission = wr.mode === 'adaptive'
      ? `adaptive admission (each healthy server starts at ${wr.activeUsersPerServer} active customers, limit learned up to ${wr.maxUsersPerServer})`
      : `${wr.activeUsersPerServer} active customers per healthy server`;
    await this.event('config', 'control_plane',
      `Waiting room enabled: ${admission}, admission TTL ${wr.admissionTtlSeconds} s, sold out after ${body.soldOutGraceSeconds} s without availability`,
      'operator', { ...body });
  }

  /** Best effort: turn the queue and the application's token requirement off. Never throws. */
  private async disableWaitingRoom(e: Experiment | null, why: string) {
    const failures: string[] = [];
    const queue = this.waitingRoom.available
      ? this.waitingRoom.configure(this.waitingRoomBody(e?.config, false, e?.eventId ?? null)).catch((error) => { failures.push(`queue: ${String(error)}`); })
      : Promise.resolve();
    const app = this.admin.updateSettings({ waiting_room_required: false }).catch((error) => { failures.push(`app: ${String(error)}`); });
    await Promise.all([queue, app]);
    if (failures.length) this.log.warn({ experiment: e?.id, failures }, 'could not fully disable the waiting room');
    if (e && waitingRoomOf(e.config).enabled) {
      await this.event(failures.length ? 'error' : 'config', 'control_plane', failures.length
        ? `Waiting room may still be on (${why}): ${failures.join('; ')}`
        : `Waiting room turned off (${why})`).catch(() => undefined);
    }
  }

  private async waitUntilReady(config: ExperimentConfig) {
    const deadline = Date.now() + READY_TIMEOUT_MS;
    while (this.current?.state === 'preparing') {
      if (!this.infra.available) {
        await this.event('config', 'control_plane', 'Compute API not available (local mode): server readiness not verified');
        break;
      }
      const mig = this.collector.currentMig;
      if (mig && mig.healthyCount >= config.servers.initial && mig.targetSize >= config.servers.initial) break;
      if (Date.now() > deadline) throw new Error(`Fewer than ${config.servers.initial} healthy servers after 15 minutes`);
      await this.sleep(3000);
    }
    if (this.current?.state !== 'preparing') return;
    const healthy = this.collector.currentMig?.healthyCount ?? null;
    await this.transition('ready', { readyAt: new Date().toISOString(), initialServerCount: healthy });
    await this.event('experiment', 'control_plane', `Experiment ready: ${healthy ?? 'unknown'} healthy server(s), inventory prepared`);
  }

  async start(id: string, user: string) {
    const e = this.require(id, ['ready']);
    const generator = await this.store.generatorStatus();
    const heartbeat = generator?.heartbeatAt ? Date.parse(generator.heartbeatAt) : 0;
    if (!generator || Date.now() - heartbeat > 15_000) throw new StateError('The load generator is offline (no heartbeat in the last 15 s)');
    if (generator.state !== 'idle') throw new StateError(`The load generator is busy (${generator.state})`);

    const s = e.config.servers;
    if (s.initial > s.min) {
      await this.applyAutoscaler({ minReplicas: s.min }, `Baseline: minimum restored to ${s.min}; capacity is now up to the autoscaler`);
    }
    await this.store.sendGeneratorCommand({
      action: 'start', experimentId: e.id, issuedBy: user,
      config: { ...e.config.traffic, experimentId: e.id, eventId: e.eventId!, waitingRoom: { enabled: waitingRoomOf(e.config).enabled } },
    });
    this.startRequestedAt = Date.now();
    await this.transition('running', { trafficStartedAt: new Date().toISOString() });
    await this.event('traffic', 'control_plane', `Start traffic requested by ${user}`, 'operator');
  }

  async stopTraffic(id: string, user: string) {
    this.require(id, ['running']);
    await this.store.sendGeneratorCommand({ action: 'stop', experimentId: id, issuedBy: user });
    this.stopRequestedAt = Date.now();
    await this.transition('stopping', {});
    await this.event('traffic', 'control_plane', `Stop traffic requested by ${user} (hard stop)`, 'operator');
  }

  async complete(id: string, user: string) {
    this.require(id, ['cooling_down', 'ready']);
    await this.finish(`Observation ended by ${user}`);
  }

  async resetInventory(id: string, user: string) {
    const e = this.require(id, ['ready', 'cooling_down', 'completed', 'failed']);
    if (!e.eventId) throw new StateError('The experiment has no prepared event yet');
    const result = await this.admin.resetInventory(e.eventId);
    await this.event('inventory', 'control_plane', `Ticket inventory reset by ${user}`, 'operator', result);
    if (waitingRoomOf(e.config).enabled && this.waitingRoom.available) {
      // A room that already declared "sold out" would otherwise keep turning everyone away.
      await this.waitingRoom.reset()
        .then(() => this.event('waiting_room', 'control_plane', 'Waiting room queue and counters reset with the inventory', 'operator'))
        .catch((error) => this.event('error', 'control_plane', `Waiting room reset failed: ${String(error)}`));
    }
  }

  /** Abort or forget the current experiment and restore the Terraform baseline. */
  async reset(user: string) {
    const e = this.current;
    if (e) {
      if (e.state === 'running' || e.state === 'stopping') {
        await this.store.sendGeneratorCommand({ action: 'stop', experimentId: e.id, issuedBy: user });
        await this.event('traffic', 'control_plane', 'Traffic stopped because the experiment was reset', 'operator');
      }
      if (ACTIVE_STATES.includes(e.state)) {
        await this.transition('failed', { failedReason: `Reset by ${user} before completion`, completedAt: new Date().toISOString() });
      }
    }
    await this.restoreBaseline(`Runtime overrides cleared by ${user}`);
    await this.disableWaitingRoom(e, `lab reset by ${user}`);
    if (e) await this.store.releaseLock(e.id);
    this.setCurrent(null);
  }

  /** Periodic reconciliation with the generator's reports and the cool-down timer. */
  private async watch() {
    if (this.busy || !this.current || !ACTIVE_STATES.includes(this.current.state)) return;
    this.busy = true;
    try {
      const fresh = await this.store.getExperiment(this.current.id);
      if (!fresh) return;
      this.current = { ...this.current, generator: fresh.generator };
      this.collector.setExperiment(this.current);
      const e = this.current;
      const g = e.generator;

      if (e.state === 'running' && !g?.state && this.startRequestedAt && Date.now() - this.startRequestedAt > GENERATOR_ACK_TIMEOUT_MS) {
        await this.store.sendGeneratorCommand({ action: 'stop', experimentId: e.id, issuedBy: 'control-plane' });
        await this.fail('The load generator did not acknowledge the start command within 30 s');
        return;
      }
      if ((e.state === 'running' || e.state === 'stopping') && g?.state === 'finished') {
        await this.transition('cooling_down', { trafficStoppedAt: g.finishedAt ?? new Date().toISOString() });
        await this.event('traffic', 'generator', `Traffic stopped (${g.finishReason ?? 'finished'})${g.message ? `: ${g.message}` : ''}`, null);
        await this.event('experiment', 'control_plane',
          `Cooling down: completes when the extra servers are removed (at most ${Math.round(e.config.cooldownObserveSeconds / 60)} min)`);
        return;
      }
      if (e.state === 'stopping' && this.stopRequestedAt && Date.now() - this.stopRequestedAt > GENERATOR_STOP_TIMEOUT_MS) {
        await this.transition('cooling_down', { trafficStoppedAt: new Date().toISOString() });
        await this.event('error', 'control_plane', 'The load generator did not confirm the stop within 20 s; check its status', null);
        return;
      }
      if (e.state === 'cooling_down' && e.trafficStoppedAt) {
        const since = Date.now() - Date.parse(e.trafficStoppedAt);
        if (since >= e.config.cooldownObserveSeconds * 1000) {
          await this.finish('Cool-down observation period elapsed');
        } else if (since >= minObserveMs(e.config) && this.backToMinimum(e.config)) {
          await this.finish('No more servers needed: back to the minimum');
        }
      }
    } catch (error) {
      this.log.warn({ err: String(error) }, 'experiment watch failed');
    } finally {
      this.busy = false;
    }
  }

  private async finish(reason: string) {
    const e = this.current!;
    await this.event('experiment', 'control_plane', reason);
    await this.collector.flushSeries();
    let finalInventory = null;
    if (e.eventId && this.inventory.available) {
      finalInventory = await this.inventory.read(e.eventId).catch(() => null);
    }
    finalInventory ??= this.collector.currentInventory;
    const completedAt = new Date().toISOString();
    await this.transition('completed', { completedAt });
    await this.restoreBaseline('Experiment completed: autoscaler restored to the Terraform baseline');
    await this.disableWaitingRoom(e, 'experiment completed');
    const [buckets, series, events] = await Promise.all([
      this.store.listTrafficBuckets(e.id), this.store.readSeries(e.id), this.store.listEvents(e.id),
    ]);
    const report = buildReport({ experiment: { ...this.current!, completedAt }, buckets, series, events, finalInventory }, this.config);
    await this.patch({ report });
    // The completed experiment stays visible until the next one is created or the lab is reset.
    await this.store.releaseLock(e.id);
  }

  private async fail(reason: string) {
    if (!this.current) return;
    const e = this.current;
    this.log.error({ experiment: e.id, reason }, 'experiment failed');
    await this.event('error', 'control_plane', reason);
    await this.transition('failed', { failedReason: reason, completedAt: new Date().toISOString() });
    await this.restoreBaseline('Experiment failed: autoscaler restored to the Terraform baseline').catch(() => undefined);
    await this.disableWaitingRoom(e, 'experiment failed');
    await this.store.releaseLock(e.id);
  }

  private async applyAutoscaler(patch: AutoscalerPatch, message: string) {
    if (patch.maxReplicas !== undefined && patch.maxReplicas > this.config.approvedMaxInstances) {
      throw new StateError(`Maximum ${patch.maxReplicas} exceeds the approved ${this.config.approvedMaxInstances} instances`);
    }
    if (!this.infra.available) {
      await this.event('config', 'control_plane', `Skipped (local mode, no Compute API): ${message}`);
      return;
    }
    this.collector.detectorForExperiment.noteOperatorChange({ ...patch, reason: message });
    await this.infra.patchAutoscaler(patch);
    const attribution = message.startsWith('Baseline') ? 'baseline' : 'operator';
    await this.event('config', 'control_plane', message, attribution, { ...patch });
  }

  private async restoreBaseline(message: string) {
    if (!this.infra.available) return;
    const b = this.config.baseline;
    const mig = this.collector.currentMig;
    if (mig && !mig.overridesActive) return;
    await this.applyAutoscaler({
      minReplicas: b.minReplicas, maxReplicas: b.maxReplicas, cpuTarget: b.cpuTarget,
      initPeriodSeconds: b.initPeriodSeconds, scaleInWindowSeconds: b.scaleInWindowSeconds,
    }, message);
  }

  private require(id: string, states: ExperimentState[]): Experiment {
    const e = this.current;
    if (!e || e.id !== id) throw new StateError('This experiment is not the active experiment');
    if (!states.includes(e.state)) throw new StateError(`Not allowed while the experiment is ${e.state}`);
    return e;
  }

  private async transition(state: ExperimentState, patch: Partial<Experiment>) {
    await this.patch({ ...patch, state });
  }

  private async patch(patch: Partial<Experiment>) {
    if (!this.current) return;
    const { generator: _ignored, ...rest } = patch;
    await this.store.updateExperiment(this.current.id, rest);
    this.setCurrent({ ...this.current, ...rest });
  }

  private setCurrent(experiment: Experiment | null) {
    this.current = experiment;
    this.collector.setExperiment(experiment);
    this.onChange?.(experiment);
  }

  onChange?: (experiment: Experiment | null) => void;

  private async event(kind: TimelineEvent['kind'], source: TimelineEvent['source'], message: string,
    attribution: TimelineEvent['attribution'] = null, data?: Record<string, unknown>) {
    await this.collector.recordEvent(this.collector.detectorForExperiment.event(kind, source, message, attribution, data));
  }
}

export { ConflictError };
