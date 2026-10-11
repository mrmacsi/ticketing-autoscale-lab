import type { ExperimentConfig, Meta, PatternType } from '@/types/contracts';
import { peakRate, sortedPoints } from './rateProfile';

export type FieldErrors = Record<string, string>;

export interface ValidationResult {
  errors: FieldErrors;
  valid: boolean;
  /** Human readable reasons the config crosses meta.limits.expensive thresholds. */
  expensiveReasons: string[];
  mixTotal: number;
}

type Limits = Pick<Meta, 'approvedMaxInstances' | 'limits'> & Partial<Pick<Meta, 'waitingRoom'>>;

const isInt = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v);
const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

export const PATTERN_RATE_FIELDS: Record<PatternType, Array<'rate' | 'startRate' | 'endRate' | 'baseRate' | 'peakRate'>> = {
  constant: ['rate'],
  ramp_up: ['startRate', 'endRate'],
  ramp_down: ['startRate', 'endRate'],
  spike: ['baseRate', 'peakRate'],
  random_bursts: ['baseRate', 'peakRate'],
  custom: [],
};

export function validateConfig(c: ExperimentConfig, meta: Limits): ValidationResult {
  const e: FieldErrors = {};
  const L = meta.limits;
  const approved = meta.approvedMaxInstances;

  const intRange = (path: string, v: unknown, min: number, max: number, label: string) => {
    if (!isInt(v)) e[path] = `${label} must be a whole number`;
    else if (v < min) e[path] = `${label} must be at least ${min}`;
    else if (v > max) e[path] = `${label} must be at most ${max}`;
  };
  const numRange = (path: string, v: unknown, min: number, max: number, label: string) => {
    if (!isNum(v)) e[path] = `${label} is required`;
    else if (v < min) e[path] = `${label} must be at least ${min}`;
    else if (v > max) e[path] = `${label} must be at most ${max}`;
  };
  const prob = (path: string, v: unknown, label: string) => numRange(path, v, 0, 1, label);

  if (!c.name || !c.name.trim()) e.name = 'Experiment name is required';

  // Servers
  const s = c.servers;
  intRange('servers.min', s.min, 1, approved, 'Minimum servers');
  intRange('servers.max', s.max, 1, approved, 'Maximum servers');
  intRange('servers.initial', s.initial, 1, approved, 'Initial servers');
  if (isInt(s.max) && s.max > approved) e['servers.max'] = `Maximum servers cannot exceed the approved maximum of ${approved}`;
  if (isInt(s.min) && isInt(s.max) && s.min > s.max) e['servers.min'] = 'Minimum servers must be ≤ maximum servers';
  if (isInt(s.initial) && isInt(s.min) && s.initial < s.min && !e['servers.initial']) e['servers.initial'] = 'Initial servers must be ≥ minimum servers';
  if (isInt(s.initial) && isInt(s.max) && s.initial > s.max && !e['servers.initial']) e['servers.initial'] = 'Initial servers must be ≤ maximum servers';
  if (!isNum(s.cpuTarget) || s.cpuTarget <= 0 || s.cpuTarget > 1) e['servers.cpuTarget'] = 'CPU target must be between 1 % and 100 %';
  intRange('servers.initPeriodSeconds', s.initPeriodSeconds, 0, 3600, 'Initialisation period');
  intRange('servers.scaleInWindowSeconds', s.scaleInWindowSeconds, 0, 3600, 'Scale-in window');

  // Tickets
  const t = c.tickets;
  if (!t.eventName || !t.eventName.trim()) e['tickets.eventName'] = 'Event name is required';
  intRange('tickets.totalInventory', t.totalInventory, 1, L.maxTotalInventory, 'Total inventory');
  intRange('tickets.categoryCount', t.categoryCount, 1, 10, 'Category count');
  if (isInt(t.categoryCount) && t.pricesCents.length !== t.categoryCount) {
    e['tickets.pricesCents'] = `Provide exactly ${t.categoryCount} prices (one per category)`;
  } else if (t.pricesCents.some((p) => !isInt(p) || p < 0)) {
    e['tickets.pricesCents'] = 'Prices must be non-negative amounts';
  }
  if (isInt(t.totalInventory) && isInt(t.categoryCount) && t.totalInventory < t.categoryCount) {
    e['tickets.totalInventory'] = 'Total inventory must be at least one ticket per category';
  }
  intRange('tickets.reservationTtlSeconds', t.reservationTtlSeconds, 1, 86400, 'Reservation TTL');
  intRange('tickets.paymentDelayMinMs', t.paymentDelayMinMs, 0, 60000, 'Minimum payment delay');
  intRange('tickets.paymentDelayMaxMs', t.paymentDelayMaxMs, 0, 60000, 'Maximum payment delay');
  if (isInt(t.paymentDelayMinMs) && isInt(t.paymentDelayMaxMs) && t.paymentDelayMinMs > t.paymentDelayMaxMs) {
    e['tickets.paymentDelayMaxMs'] = 'Maximum payment delay must be ≥ minimum';
  }
  prob('tickets.paymentFailureProbability', t.paymentFailureProbability, 'Payment failure probability');

  // Traffic
  const tr = c.traffic;
  intRange('traffic.durationSeconds', tr.durationSeconds, 1, L.maxDurationSeconds, 'Duration (seconds)');
  intRange('traffic.maxVUs', tr.maxVUs, 1, L.maxVUs, 'Max virtual users');
  intRange('traffic.totalArrivals', tr.totalArrivals, 0, Number.MAX_SAFE_INTEGER, 'Total arrivals');
  intRange('traffic.requestTimeoutMs', tr.requestTimeoutMs, 100, 120000, 'Request timeout');
  if (!isInt(tr.seed)) e['traffic.seed'] = 'Seed must be a whole number';

  const p = tr.pattern;
  for (const f of PATTERN_RATE_FIELDS[p.type] ?? []) {
    numRange(`traffic.pattern.${f}`, p[f], 0, L.maxRate, 'Rate (arrivals/s)');
  }
  const dur = isInt(tr.durationSeconds) ? tr.durationSeconds : Infinity;
  if (p.type === 'spike') {
    intRange('traffic.pattern.spikeAtSeconds', p.spikeAtSeconds, 0, Math.max(0, dur - 1), 'Spike start');
    intRange('traffic.pattern.spikeDurationSeconds', p.spikeDurationSeconds, 1, dur, 'Spike duration');
  }
  if (p.type === 'random_bursts') {
    intRange('traffic.pattern.burstCount', p.burstCount, 1, 100, 'Burst count');
    intRange('traffic.pattern.burstDurationSeconds', p.burstDurationSeconds, 1, dur, 'Burst duration');
    if (isInt(p.burstCount) && isInt(p.burstDurationSeconds) && p.burstCount * p.burstDurationSeconds > dur) {
      e['traffic.pattern.burstDurationSeconds'] = 'Bursts do not fit in the duration (count × burst length > duration)';
    }
  }
  if (p.type === 'custom') {
    const pts = p.points ?? [];
    if (pts.length < 1) e['traffic.pattern.points'] = 'Add at least one schedule point';
    pts.forEach((pt, i) => {
      if (!isInt(pt.atSeconds) || pt.atSeconds < 0 || pt.atSeconds > dur) {
        e[`traffic.pattern.points.${i}.atSeconds`] = `Time must be a whole number between 0 and ${Number.isFinite(dur) ? dur : 'the duration'}`;
      }
      if (!isNum(pt.rate) || pt.rate < 0 || pt.rate > L.maxRate) {
        e[`traffic.pattern.points.${i}.rate`] = `Rate must be between 0 and ${L.maxRate}`;
      }
    });
    const times = sortedPoints(pts).map((x) => x.atSeconds);
    if (new Set(times).size !== times.length) e['traffic.pattern.points'] = 'Schedule times must be unique';
  }

  const mixTotal = tr.mix.browse + tr.mix.availability + tr.mix.reserve + tr.mix.purchase;
  for (const k of ['browse', 'availability', 'reserve', 'purchase'] as const) {
    intRange(`traffic.mix.${k}`, tr.mix[k], 0, 100, 'Mix percentage');
  }
  if (mixTotal !== 100) e['traffic.mix'] = `Journey mix must sum to 100 % (currently ${Number.isFinite(mixTotal) ? mixTotal : '?'} %)`;

  const b = tr.behaviour;
  intRange('traffic.behaviour.maxQuantity', b.maxQuantity, 1, 8, 'Max tickets per reservation');
  intRange('traffic.behaviour.thinkTimeMinMs', b.thinkTimeMinMs, 0, 60000, 'Minimum think time');
  intRange('traffic.behaviour.thinkTimeMaxMs', b.thinkTimeMaxMs, 0, 60000, 'Maximum think time');
  if (isInt(b.thinkTimeMinMs) && isInt(b.thinkTimeMaxMs) && b.thinkTimeMinMs > b.thinkTimeMaxMs) {
    e['traffic.behaviour.thinkTimeMaxMs'] = 'Maximum think time must be ≥ minimum';
  }
  prob('traffic.behaviour.abandonCheckoutProbability', b.abandonCheckoutProbability, 'Abandon probability');
  prob('traffic.behaviour.duplicateSubmitProbability', b.duplicateSubmitProbability, 'Duplicate submit probability');
  prob('traffic.behaviour.retryPaymentAfterDeclineProbability', b.retryPaymentAfterDeclineProbability, 'Retry probability');
  prob('traffic.behaviour.syntheticCpuProbability', b.syntheticCpuProbability, 'Synthetic CPU probability');

  intRange('syntheticCpuMaxMs', c.syntheticCpuMaxMs, 0, L.maxSyntheticCpuMs, 'Synthetic CPU cap');
  const cap = isInt(c.syntheticCpuMaxMs) ? c.syntheticCpuMaxMs : 0;
  intRange('traffic.behaviour.syntheticCpuMs', b.syntheticCpuMs, 0, cap, 'Synthetic CPU per journey');
  if (isInt(b.syntheticCpuMs) && b.syntheticCpuMs > 0 && cap === 0) {
    e['traffic.behaviour.syntheticCpuMs'] = 'Set a synthetic CPU cap above 0 first (it is disabled by default)';
  }

  intRange('cooldownObserveSeconds', c.cooldownObserveSeconds, 0, 7200, 'Cool-down observation');

  // Waiting room (same ranges as the control API).
  const wr = c.waitingRoom;
  if (wr) {
    intRange('waitingRoom.activeUsersPerServer', wr.activeUsersPerServer, 1, 1000, 'Active users per server');
    intRange('waitingRoom.admissionTtlSeconds', wr.admissionTtlSeconds, 10, 3600, 'Admission TTL');
    if (wr.mode !== undefined && wr.mode !== 'fixed' && wr.mode !== 'adaptive') e['waitingRoom.mode'] = 'Choose fixed or adaptive admission';
    if (wr.mode === 'adaptive') {
      intRange('waitingRoom.maxUsersPerServer', wr.maxUsersPerServer, 1, 1000, 'Maximum per server');
      if (!e['waitingRoom.maxUsersPerServer'] && isInt(wr.activeUsersPerServer) && (wr.maxUsersPerServer as number) < wr.activeUsersPerServer) {
        e['waitingRoom.maxUsersPerServer'] = 'Maximum per server must be at least the starting limit';
      }
    }
    if (wr.enabled && meta.waitingRoom?.deployed === false) {
      e['waitingRoom.enabled'] = 'The waiting room is not deployed in this lab; turn it off (or deploy the queue server)';
    }
  }

  return { errors: e, valid: Object.keys(e).length === 0, expensiveReasons: expensiveReasons(c, meta), mixTotal };
}

export function expensiveReasons(c: ExperimentConfig, meta: Limits): string[] {
  const x = meta.limits.expensive;
  const reasons: string[] = [];
  const peak = peakRate(c.traffic.pattern, c.traffic.durationSeconds);
  if (peak > x.rateAbove) reasons.push(`peak rate ${peak} arrivals/s > ${x.rateAbove}`);
  if (c.traffic.durationSeconds > x.durationSecondsAbove) reasons.push(`duration ${c.traffic.durationSeconds}s > ${x.durationSecondsAbove}s`);
  if (c.traffic.maxVUs > x.vusAbove) reasons.push(`max VUs ${c.traffic.maxVUs} > ${x.vusAbove}`);
  if (c.servers.max > x.instancesAbove) reasons.push(`max servers ${c.servers.max} > ${x.instancesAbove}`);
  return reasons;
}
