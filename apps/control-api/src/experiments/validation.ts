import { z } from 'zod';
import type { AppConfig } from '../config.js';
import { DEFAULT_WAITING_ROOM, type ExperimentConfig } from '../types.js';
import { peakRate } from './pattern.js';

export class ValidationError extends Error {
  constructor(message: string, readonly details: Record<string, string>) { super(message); }
}

const int = (min: number, max: number) => z.number().int().min(min).max(max);
const prob = z.number().min(0).max(1);

export function experimentSchema(config: AppConfig) {
  const L = config.limits;
  const rate = z.number().min(0).max(L.maxRate);
  return z.object({
    name: z.string().trim().min(1).max(120),
    preset: z.enum(['A', 'B', 'C', 'D', 'E', 'F']).nullable().default(null),
    servers: z.object({
      initial: int(1, config.approvedMaxInstances),
      min: int(1, config.approvedMaxInstances),
      max: int(1, config.approvedMaxInstances),
      cpuTarget: z.number().min(0.1).max(0.95),
      initPeriodSeconds: int(0, 900),
      scaleInWindowSeconds: int(60, 3600),
    }),
    tickets: z.object({
      eventName: z.string().trim().min(1).max(200),
      totalInventory: int(1, L.maxTotalInventory),
      categoryCount: int(1, 10),
      pricesCents: z.array(int(0, 10_000_000)).min(1).max(10),
      reservationTtlSeconds: int(10, 3600),
      paymentDelayMinMs: int(0, 10_000),
      paymentDelayMaxMs: int(0, 10_000),
      paymentFailureProbability: prob,
    }),
    traffic: z.object({
      seed: int(0, 2_147_483_647),
      durationSeconds: int(10, L.maxDurationSeconds),
      maxVUs: int(1, L.maxVUs),
      totalArrivals: int(0, 10_000_000),
      requestTimeoutMs: int(500, 60_000),
      pattern: z.object({
        type: z.enum(['constant', 'ramp_up', 'spike', 'ramp_down', 'random_bursts', 'custom']),
        rate: rate.optional(),
        startRate: rate.optional(),
        endRate: rate.optional(),
        baseRate: rate.optional(),
        peakRate: rate.optional(),
        spikeAtSeconds: int(0, L.maxDurationSeconds).optional(),
        spikeDurationSeconds: int(1, L.maxDurationSeconds).optional(),
        burstCount: int(1, 100).optional(),
        burstDurationSeconds: int(1, 600).optional(),
        points: z.array(z.object({ atSeconds: int(0, L.maxDurationSeconds), rate })).max(200).optional(),
      }),
      mix: z.object({ browse: int(0, 100), availability: int(0, 100), reserve: int(0, 100), purchase: int(0, 100) }),
      behaviour: z.object({
        maxQuantity: int(1, 8),
        thinkTimeMinMs: int(0, 60_000),
        thinkTimeMaxMs: int(0, 60_000),
        abandonCheckoutProbability: prob,
        duplicateSubmitProbability: prob,
        retryPaymentAfterDeclineProbability: prob,
        syntheticCpuMs: int(0, L.maxSyntheticCpuMs),
        syntheticCpuProbability: prob,
      }),
    }),
    syntheticCpuMaxMs: int(0, L.maxSyntheticCpuMs),
    cooldownObserveSeconds: int(0, L.maxCooldownSeconds),
    // Optional with a default so configs stored before the waiting room existed still parse; mode and
    // maxUsersPerServer default too, for configs stored before adaptive admission.
    waitingRoom: z.object({
      enabled: z.boolean(),
      activeUsersPerServer: int(1, 1000),
      admissionTtlSeconds: int(10, 3600),
      mode: z.enum(['fixed', 'adaptive']).default(DEFAULT_WAITING_ROOM.mode),
      maxUsersPerServer: int(1, 1000).default(DEFAULT_WAITING_ROOM.maxUsersPerServer),
    }).default({ ...DEFAULT_WAITING_ROOM }),
    confirmExpensive: z.boolean().optional(),
  });
}

/** Parses and cross-validates an experiment configuration; throws ValidationError with per-field messages. */
export function validateExperiment(input: unknown, config: AppConfig): ExperimentConfig {
  const parsed = experimentSchema(config).safeParse(input);
  const details: Record<string, string> = {};
  const add = (path: string, message: string) => { details[path] = details[path] ? `${details[path]}; ${message}` : message; };
  if (!parsed.success) {
    for (const issue of parsed.error.issues) add(issue.path.join('.') || '(root)', issue.message);
    throw new ValidationError('Invalid experiment configuration', details);
  }
  const c = parsed.data as ExperimentConfig;

  if (c.servers.min > c.servers.max) add('servers.min', 'Minimum must not exceed maximum');
  if (c.servers.initial < c.servers.min || c.servers.initial > c.servers.max) add('servers.initial', 'Initial count must be between minimum and maximum');
  if (c.tickets.pricesCents.length !== c.tickets.categoryCount) add('tickets.pricesCents', 'Provide one price per category');
  if (c.tickets.totalInventory < c.tickets.categoryCount) add('tickets.totalInventory', 'Need at least one ticket per category');
  if (c.tickets.paymentDelayMinMs > c.tickets.paymentDelayMaxMs) add('tickets.paymentDelayMinMs', 'Minimum delay exceeds maximum');
  const mix = c.traffic.mix;
  if (mix.browse + mix.availability + mix.reserve + mix.purchase !== 100) add('traffic.mix', 'Percentages must add up to 100');
  if (c.traffic.behaviour.thinkTimeMinMs > c.traffic.behaviour.thinkTimeMaxMs) add('traffic.behaviour.thinkTimeMinMs', 'Minimum exceeds maximum');
  if (c.waitingRoom.enabled && !config.waitingRoom.baseUrl) {
    add('waitingRoom.enabled', 'The waiting room is not deployed in this lab (the control API has no WAITING_ROOM_URL); turn it off or deploy the queue server');
  }
  if (c.waitingRoom.mode === 'adaptive' && c.waitingRoom.maxUsersPerServer < c.waitingRoom.activeUsersPerServer) {
    add('waitingRoom.maxUsersPerServer', 'In adaptive mode the maximum per server must be at least the starting limit');
  }
  if (c.traffic.behaviour.syntheticCpuMs > 0 && c.syntheticCpuMaxMs === 0) {
    add('syntheticCpuMaxMs', 'Enable synthetic CPU work (max ms > 0) to use it in journeys');
  }

  const p = c.traffic.pattern;
  const need = (fields: Array<keyof typeof p>) => fields.forEach((f) => p[f] === undefined && add(`traffic.pattern.${String(f)}`, `Required for ${p.type}`));
  if (p.type === 'constant') need(['rate']);
  if (p.type === 'ramp_up' || p.type === 'ramp_down') need(['startRate', 'endRate']);
  if (p.type === 'spike') need(['baseRate', 'peakRate', 'spikeAtSeconds', 'spikeDurationSeconds']);
  if (p.type === 'random_bursts') need(['baseRate', 'peakRate', 'burstCount', 'burstDurationSeconds']);
  if (p.type === 'custom' && (p.points?.length ?? 0) < 2) add('traffic.pattern.points', 'Provide at least two points');
  if (p.type === 'ramp_up' && (p.endRate ?? 0) < (p.startRate ?? 0)) add('traffic.pattern.endRate', 'A ramp up must end higher than it starts');
  if (p.type === 'ramp_down' && (p.endRate ?? 0) > (p.startRate ?? 0)) add('traffic.pattern.endRate', 'A ramp down must end lower than it starts');

  if (Object.keys(details).length) throw new ValidationError('Invalid experiment configuration', details);
  return c;
}

/** Reasons this configuration needs explicit confirmation; empty when within conservative defaults. */
export function expensiveReasons(c: ExperimentConfig, config: AppConfig): string[] {
  const e = config.expensive;
  const reasons: string[] = [];
  const peak = peakRate(c.traffic.pattern, c.traffic.durationSeconds);
  if (peak > e.rateAbove) reasons.push(`Peak arrival rate ${peak}/s is above ${e.rateAbove}/s`);
  if (c.traffic.durationSeconds > e.durationSecondsAbove) reasons.push(`Duration ${c.traffic.durationSeconds}s is above ${e.durationSecondsAbove}s`);
  if (c.traffic.maxVUs > e.vusAbove) reasons.push(`${c.traffic.maxVUs} virtual users is above ${e.vusAbove}`);
  if (c.servers.max > e.instancesAbove) reasons.push(`Maximum ${c.servers.max} servers is above ${e.instancesAbove}`);
  return reasons;
}

/** Splits the total inventory across categories; the remainder goes to the first category. */
export function splitInventory(total: number, count: number): number[] {
  const base = Math.floor(total / count);
  return Array.from({ length: count }, (_, i) => base + (i === 0 ? total - base * count : 0));
}

export const CATEGORY_NAMES = ['Standing', 'Seated', 'Premium', 'VIP', 'Box', 'Balcony', 'Terrace', 'Gallery', 'Lounge', 'Pit'];
