import type { AppConfig } from '../config.js';
import { DEFAULT_WAITING_ROOM, type ExperimentConfig } from '../types.js';

const behaviour = {
  maxQuantity: 4,
  thinkTimeMinMs: 200,
  thinkTimeMaxMs: 1500,
  abandonCheckoutProbability: 0.1,
  duplicateSubmitProbability: 0.05,
  retryPaymentAfterDeclineProbability: 0.5,
  syntheticCpuMs: 0,
  syntheticCpuProbability: 0,
};

export function defaultExperiment(config: AppConfig): ExperimentConfig {
  const max = Math.min(5, config.approvedMaxInstances);
  return {
    name: 'Custom experiment',
    preset: null,
    servers: { initial: 1, min: 1, max, cpuTarget: config.baseline.cpuTarget, initPeriodSeconds: config.baseline.initPeriodSeconds,
      scaleInWindowSeconds: config.baseline.scaleInWindowSeconds },
    tickets: { eventName: 'Lab Arena Night', totalInventory: 500, categoryCount: 3, pricesCents: [4500, 8000, 15000],
      reservationTtlSeconds: 120, paymentDelayMinMs: 100, paymentDelayMaxMs: 400, paymentFailureProbability: 0.05,
      nearSelloutTtlSeconds: 0, nearSelloutThreshold: 0 },
    traffic: {
      seed: 42, durationSeconds: 600, maxVUs: 500, totalArrivals: 0, requestTimeoutMs: 10_000,
      pattern: { type: 'ramp_up', startRate: 5, endRate: 100 },
      mix: { browse: 40, availability: 20, reserve: 25, purchase: 15 },
      behaviour,
    },
    syntheticCpuMaxMs: 0,
    cooldownObserveSeconds: 900,
    // Presets A–E and custom experiments run without the waiting room.
    waitingRoom: { ...DEFAULT_WAITING_ROOM },
  };
}

/**
 * Scale-in stabilisation for the presets: the autoscaler removes servers about a minute after demand
 * drops instead of four, and the experiment completes as soon as the group is back at its minimum.
 */
export const QUICK_SCALE_IN_SECONDS = 60;

/** Preset E demonstrates the autoscaler's slower, default-like scale-in. */
export const DELAYED_SCALE_IN_SECONDS = 240;

export function presets(config: AppConfig) {
  const base = defaultExperiment(config);
  base.servers.scaleInWindowSeconds = QUICK_SCALE_IN_SECONDS;
  const max = Math.min(5, config.approvedMaxInstances);
  const flashMax = Math.min(3, config.approvedMaxInstances);
  const make = (patch: (c: ExperimentConfig) => void) => {
    const c = structuredClone(base);
    patch(c);
    return c;
  };
  return [
    {
      id: 'A' as const,
      title: 'Baseline traffic',
      description: '1 server, 10,000 tickets, a steady 5 customers/s for 5 minutes.',
      demonstrates: 'Stable utilisation and checkout metrics on a single server with no scaling pressure.',
      config: make((c) => {
        c.name = 'Scenario A – baseline'; c.preset = 'A';
        c.servers = { ...c.servers, initial: 1, min: 1, max: 1 };
        c.tickets = { ...c.tickets, totalInventory: 10_000 };
        c.traffic = { ...c.traffic, durationSeconds: 300, maxVUs: 200, pattern: { type: 'constant', rate: 5 } };
        c.cooldownObserveSeconds = 60;
      }),
    },
    {
      id: 'B' as const,
      title: 'Increasing traffic',
      description: `Starts on 1 server (max ${max}, CPU target 60%), 100,000 tickets, ramps 5 → 100 customers/s over 10 minutes.`,
      demonstrates: 'Whether the real autoscaler adds VMs as measured CPU crosses the target, and how long new VMs take to serve.',
      config: make((c) => {
        c.name = 'Scenario B – increasing traffic'; c.preset = 'B';
        c.servers = { ...c.servers, initial: 1, min: 1, max, cpuTarget: 0.6 };
        c.tickets = { ...c.tickets, totalInventory: 100_000 };
        c.traffic = { ...c.traffic, durationSeconds: 600, maxVUs: 800, pattern: { type: 'ramp_up', startRate: 5, endRate: 100 } };
        c.cooldownObserveSeconds = 900;
      }),
    },
    {
      id: 'C' as const,
      title: 'Flash ticket sale',
      description: '100 tickets, 1,000 purchase attempts arriving in a sudden 25-second burst (40 customers/s).',
      demonstrates: 'Correctness under contention: no more than 100 tickets can be sold, the rest get 409 sold out.',
      config: make((c) => {
        c.name = 'Scenario C – flash sale'; c.preset = 'C';
        c.servers = { ...c.servers, initial: 1, min: 1, max };
        c.tickets = { ...c.tickets, eventName: 'Flash Sale Night', totalInventory: 100, categoryCount: 1, pricesCents: [9900],
          paymentFailureProbability: 0, paymentDelayMinMs: 50, paymentDelayMaxMs: 250 };
        c.traffic = {
          ...c.traffic, durationSeconds: 120, maxVUs: 1000, totalArrivals: 1000, requestTimeoutMs: 20_000,
          pattern: { type: 'spike', baseRate: 2, peakRate: 40, spikeAtSeconds: 10, spikeDurationSeconds: 25 },
          mix: { browse: 0, availability: 0, reserve: 0, purchase: 100 },
          behaviour: { ...behaviour, maxQuantity: 1, thinkTimeMinMs: 20, thinkTimeMaxMs: 200, abandonCheckoutProbability: 0,
            duplicateSubmitProbability: 0.1 },
        };
        c.cooldownObserveSeconds = 120;
      }),
    },
    {
      id: 'D' as const,
      title: 'Reservation expiry',
      description: '100 tickets, 30-second holds, customers reserve and abandon checkout.',
      demonstrates: 'Inventory being held, reservations expiring in the background and capacity becoming available again.',
      config: make((c) => {
        c.name = 'Scenario D – reservation expiry'; c.preset = 'D';
        c.servers = { ...c.servers, initial: 1, min: 1, max };
        c.tickets = { ...c.tickets, eventName: 'Expiry Matinee', totalInventory: 100, categoryCount: 1, pricesCents: [5000],
          reservationTtlSeconds: 30 };
        c.traffic = {
          ...c.traffic, durationSeconds: 180, maxVUs: 200,
          pattern: { type: 'constant', rate: 3 },
          mix: { browse: 0, availability: 30, reserve: 70, purchase: 0 },
          behaviour: { ...behaviour, maxQuantity: 2 },
        };
        c.cooldownObserveSeconds = 60;
      }),
    },
    {
      id: 'E' as const,
      title: 'Recovery and scale-in',
      description: `Sustained 90 customers/s for 6 minutes (max ${max} servers), then traffic stops and the lab observes the autoscaler's normal delayed scale-in (up to 25 minutes).`,
      demonstrates: 'Scale-out under load, request distribution across VMs, and the autoscaler\'s delayed scale-in after its stabilisation period.',
      config: make((c) => {
        c.name = 'Scenario E – recovery and scale-in'; c.preset = 'E';
        // This preset demonstrates the autoscaler's own delayed scale-in, so it keeps the baseline window.
        c.servers = { ...c.servers, initial: 1, min: 1, max, cpuTarget: 0.6, scaleInWindowSeconds: DELAYED_SCALE_IN_SECONDS };
        c.tickets = { ...c.tickets, totalInventory: 100_000 };
        c.traffic = { ...c.traffic, durationSeconds: 360, maxVUs: 800, pattern: { type: 'constant', rate: 90 } };
        c.cooldownObserveSeconds = 1500;
      }),
    },
    {
      id: 'F' as const,
      title: '10,000 customers · 4,000 tickets · adaptive waiting room',
      description: `10,000 customers arrive in about 2 minutes (84/s) for 4,000 tickets. One ticket per checkout, 30-second holds (10 s for the last 200 tickets), 10% checkout abandonment. Starts on 1 VM (max ${flashMax}, CPU target 30%, 60 s scale-in window) with the adaptive waiting room on: one limit per server, learned from the busiest server, starting at 25 (up to 200). Completes 30 s after traffic ends; the scale-in that follows is still recorded in the report.`,
      demonstrates: `Adaptive admission control in front of autoscaling: customers beyond what the healthy servers can take wait in a FIFO waiting room instead of overloading them (no 503s). Every second the room adjusts one limit per server from how busy the busiest server really is — requests in flight per PHP worker, requests waiting for a worker and VM CPU — growing while every server has headroom and cutting back as soon as any is overloaded. Capacity is healthy servers × that limit, so each server the autoscaler adds (up to ${flashMax}) raises it as soon as the load balancer marks it healthy. When the 4,000 tickets are gone the room tells everyone still waiting that the event is sold out, without touching the application servers.`,
      config: make((c) => {
        c.name = 'Scenario F – 10,000 customers, 4,000 tickets, adaptive waiting room'; c.preset = 'F';
        // A low CPU target turns one busy VM straight into a scale-out recommendation (ceil(1 × 100% / 30%) = 4,
        // capped at the maximum of 3).
        // Scale-in window at the minimum (60 s): servers go about a minute after demand drops.
        c.servers = { ...c.servers, initial: 1, min: 1, max: flashMax, cpuTarget: 0.3, scaleInWindowSeconds: QUICK_SCALE_IN_SECONDS };
        // 30-second holds survive checkout latency on a busy VM; abandoned holds still return mid-sale. Near the end the
        // last 200 tickets are held for 10 s only, so tickets in checkouts abandoned near sell-out go back on sale about
        // 20 s sooner and the sale finishes sooner. The waiting room declares sold out once availability has stayed 0 for the full
        // TTL + 5 s (every hold resolved, whichever TTL it got).
        c.tickets = { ...c.tickets, eventName: '10,000 Customer Flash Sale', totalInventory: 4000, categoryCount: 1,
          pricesCents: [5000], reservationTtlSeconds: 30, paymentDelayMinMs: 100, paymentDelayMaxMs: 250, paymentFailureProbability: 0,
          nearSelloutTtlSeconds: 10, nearSelloutThreshold: 200 };
        // A sustained surge: 84 customers/s until the 10,000th arrives (≈ 119 s). The waiting room, not the traffic
        // shape, protects the servers; a waiting customer still occupies a VU, hence 10,000 VUs. durationSeconds is
        // only the safety timeout: long enough for 4,000 tickets to sell and the last abandoned 30 s holds to resolve
        // so the room can tell everyone still waiting that it is sold out.
        c.traffic = {
          ...c.traffic, durationSeconds: 300, totalArrivals: 10_000, maxVUs: 10_000, requestTimeoutMs: 20_000,
          pattern: { type: 'constant', rate: 84 },
          mix: { browse: 0, availability: 0, reserve: 0, purchase: 100 },
          behaviour: { ...behaviour, maxQuantity: 1, thinkTimeMinMs: 20, thinkTimeMaxMs: 100,
            abandonCheckoutProbability: 0.1, duplicateSubmitProbability: 0, retryPaymentAfterDeclineProbability: 0 },
        };
        // Adaptive admission (docs/waiting-room.md 2a): every healthy server starts at 25 customers in checkout (the
        // fixed value measured on one n1-standard-1, section 2; a conservative start on n2-standard-2) and the queue then learns its limit every second from
        // PHP-FPM workers busy, the listen queue and VM CPU — ×1.25 per step in slow start while there is headroom,
        // +1 once steady, ×0.75 on overload — never above 200. Capacity = sum of the healthy servers' limits. An
        // admitted customer who neither buys nor releases frees the slot after 60 s.
        c.waitingRoom = { enabled: true, mode: 'adaptive', activeUsersPerServer: 25, maxUsersPerServer: 200, admissionTtlSeconds: 60 };
        // Complete 30 s after traffic ends (the last holds expire); the scale-in window then removes the extra servers
        // about a minute after demand dropped. The control plane keeps watching the instance group after completion
        // (up to 15 min) and adds that scale-in to the report (measured.scaleIn).
        c.cooldownObserveSeconds = 30;
      }),
    },
  ];
}
