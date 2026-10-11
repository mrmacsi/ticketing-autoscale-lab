import { describe, expect, it } from 'vitest';
import { expectedArrivals, peakRate, rateAt } from '../src/experiments/pattern.js';
import { defaultExperiment, presets } from '../src/experiments/presets.js';
import { expensiveReasons, splitInventory, validateExperiment, ValidationError } from '../src/experiments/validation.js';
import { testConfig } from './fakes.js';

const config = testConfig({ WAITING_ROOM_URL: 'http://10.60.0.20:8080/' });
const errorsOf = (input: unknown) => {
  try { validateExperiment(input, config); return {}; } catch (e) { return (e as ValidationError).details; }
};

describe('experiment validation', () => {
  it('accepts the defaults and every preset', () => {
    expect(() => validateExperiment(defaultExperiment(config), config)).not.toThrow();
    for (const p of presets(config)) expect(() => validateExperiment(p.config, config), p.id).not.toThrow();
  });

  it('sizes the waiting-room preset: 10,000 customers in about two minutes for 4,000 tickets, at most 3 servers, adaptive admission', () => {
    const sale = presets(config).find((p) => p.id === 'F')!;
    const c = validateExperiment(sale.config, config);
    expect(c.preset).toBe('F');
    expect(c.tickets.totalInventory).toBe(4000);
    expect(c.tickets.reservationTtlSeconds).toBe(30);
    // The last 200 tickets are held for 10 s, so abandoned checkouts near sell-out return them sooner.
    expect(c.tickets).toMatchObject({ nearSelloutTtlSeconds: 10, nearSelloutThreshold: 200 });
    expect(sale.description).toMatch(/10 s for the last 200 tickets/);
    expect(c.tickets.categoryCount).toBe(1);
    expect(c.traffic.mix).toEqual({ browse: 0, availability: 0, reserve: 0, purchase: 100 });
    expect(c.traffic.totalArrivals).toBe(10_000);
    expect(expectedArrivals(c.traffic.pattern, c.traffic.durationSeconds, c.traffic.totalArrivals)).toBe(10_000);
    // A simple sustained surge; the waiting room, not the traffic shape, protects the servers.
    expect(c.traffic.pattern).toEqual({ type: 'constant', rate: 84 });
    expect(peakRate(c.traffic.pattern, c.traffic.durationSeconds)).toBe(84);
    expect(c.traffic.durationSeconds).toBe(300);
    // The 10,000th customer arrives at about two minutes; 300 s is the safety timeout that leaves time for the
    // last holds to resolve so waiting customers are told "sold out".
    const lastArrival = Math.ceil(c.traffic.totalArrivals / 84);
    expect(lastArrival).toBeGreaterThanOrEqual(115);
    expect(lastArrival).toBeLessThanOrEqual(125);
    expect(lastArrival).toBeLessThan(c.traffic.durationSeconds);
    // Waiting customers occupy a VU each.
    expect(c.traffic.maxVUs).toBe(10_000);
    expect(c.traffic.maxVUs).toBeLessThanOrEqual(config.limits.maxVUs);
    expect(config.limits.maxVUs).toBe(1_000_000);
    // One busy VM at a 30% target asks for ceil(1 / 0.3) = 4 servers, capped at 3.
    expect(c.servers).toMatchObject({ initial: 1, min: 1, max: 3, cpuTarget: 0.3, scaleInWindowSeconds: 60 });
    expect(Math.min(c.servers.max, Math.ceil(1 / c.servers.cpuTarget))).toBe(3);
    // Adaptive admission: each healthy server starts at 25 customers in checkout and its limit is learned up to 200.
    expect(c.waitingRoom).toEqual({ enabled: true, mode: 'adaptive', activeUsersPerServer: 25, maxUsersPerServer: 200, admissionTtlSeconds: 60 });
    expect(sale.title).toMatch(/adaptive waiting room/);
    expect(sale.description).toMatch(/starting at 25/);
    expect(sale.demonstrates).toMatch(/per PHP worker/);
    expect(sale.demonstrates).toMatch(/VM CPU/);
    expect(sale.demonstrates).toMatch(/healthy servers × that limit/);
    expect(c.traffic.behaviour).toMatchObject({ maxQuantity: 1, abandonCheckoutProbability: 0.1,
      duplicateSubmitProbability: 0, retryPaymentAfterDeclineProbability: 0, syntheticCpuMs: 0 });
    expect(c.tickets.paymentFailureProbability).toBe(0);
    expect(c.cooldownObserveSeconds).toBe(30);
    expect(expensiveReasons(c, config).length).toBeGreaterThan(0);
    expect(c.confirmExpensive).not.toBe(true);
  });

  it('runs presets A–E and custom defaults without the waiting room', () => {
    expect(defaultExperiment(config).waitingRoom).toEqual({ enabled: false, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200 });
    for (const p of presets(config).filter((x) => x.id !== 'F')) expect(p.config.waitingRoom.enabled, p.id).toBe(false);
  });

  it('removes servers quickly after the load in every preset except E (which shows the normal delay)', () => {
    for (const p of presets(config)) {
      expect(p.config.servers.scaleInWindowSeconds, p.id).toBe(p.id === 'E' ? 240 : 60);
    }
  });

  it('keeps the waiting-room preset within the recorded VM spending approval', () => {
    const lowerCap = testConfig({ APPROVED_MAX_INSTANCES: '2', WAITING_ROOM_URL: 'http://10.60.0.20:8080' });
    const c = presets(lowerCap).find((p) => p.id === 'F')!.config;
    expect(c.servers.max).toBe(2);
    expect(() => validateExperiment(c, lowerCap)).not.toThrow();
  });

  it('refuses the waiting room when it is not deployed, with a clear message', () => {
    const notDeployed = testConfig();
    expect(notDeployed.waitingRoom.baseUrl).toBeNull();
    const f = presets(notDeployed).find((p) => p.id === 'F')!.config;
    try {
      validateExperiment(f, notDeployed);
      throw new Error('expected a validation error');
    } catch (e) {
      expect((e as ValidationError).details['waitingRoom.enabled']).toMatch(/not deployed.*WAITING_ROOM_URL/);
    }
    expect(() => validateExperiment({ ...f, waitingRoom: { ...f.waitingRoom, enabled: false } }, notDeployed)).not.toThrow();
  });

  it('defaults the waiting room for configs stored before it existed and range-checks its fields', () => {
    const { waitingRoom: _omit, ...old } = defaultExperiment(config);
    expect(validateExperiment(old, config).waitingRoom).toEqual({ enabled: false, activeUsersPerServer: 25, admissionTtlSeconds: 60, mode: 'fixed', maxUsersPerServer: 200 });
    const c = defaultExperiment(config);
    const bad = errorsOf({ ...c, waitingRoom: { enabled: true, activeUsersPerServer: 0, admissionTtlSeconds: 5 } });
    expect(Object.keys(bad)).toEqual(expect.arrayContaining(['waitingRoom.activeUsersPerServer', 'waitingRoom.admissionTtlSeconds']));
    expect(Object.keys(errorsOf({ ...c, waitingRoom: { enabled: true, activeUsersPerServer: 1001, admissionTtlSeconds: 3601 } })))
      .toEqual(expect.arrayContaining(['waitingRoom.activeUsersPerServer', 'waitingRoom.admissionTtlSeconds']));
  });

  it('turns near-sell-out holds off in every preset except F and for configs stored before they existed', () => {
    for (const p of presets(config).filter((x) => x.id !== 'F')) {
      expect(p.config.tickets, p.id).toMatchObject({ nearSelloutTtlSeconds: 0, nearSelloutThreshold: 0 });
    }
    const c = defaultExperiment(config);
    const { nearSelloutTtlSeconds: _t, nearSelloutThreshold: _n, ...oldTickets } = c.tickets;
    expect(validateExperiment({ ...c, tickets: oldTickets }, config).tickets).toMatchObject({ nearSelloutTtlSeconds: 0, nearSelloutThreshold: 0 });
  });

  it('range-checks near-sell-out holds: 0 or 5–3600 s, never longer than the reservation TTL', () => {
    const c = defaultExperiment(config);   // reservation TTL 120 s
    const tickets = (o: object) => ({ ...c, tickets: { ...c.tickets, ...o } });
    expect(errorsOf(tickets({ nearSelloutTtlSeconds: 0, nearSelloutThreshold: 0 }))).toEqual({});
    expect(errorsOf(tickets({ nearSelloutTtlSeconds: 5, nearSelloutThreshold: 1_000_000 }))).toEqual({});
    expect(errorsOf(tickets({ nearSelloutTtlSeconds: 120, nearSelloutThreshold: 50 }))).toEqual({});
    expect(Object.keys(errorsOf(tickets({ nearSelloutTtlSeconds: 4 })))).toEqual(['tickets.nearSelloutTtlSeconds']);
    expect(Object.keys(errorsOf(tickets({ nearSelloutTtlSeconds: 3601 })))).toContain('tickets.nearSelloutTtlSeconds');
    expect(Object.keys(errorsOf(tickets({ nearSelloutTtlSeconds: 7.5 })))).toEqual(['tickets.nearSelloutTtlSeconds']);
    expect(errorsOf(tickets({ nearSelloutTtlSeconds: 121 }))['tickets.nearSelloutTtlSeconds']).toMatch(/not be longer than the reservation TTL/);
    expect(Object.keys(errorsOf(tickets({ nearSelloutThreshold: -1 })))).toEqual(['tickets.nearSelloutThreshold']);
    expect(Object.keys(errorsOf(tickets({ nearSelloutThreshold: 1_000_001 })))).toEqual(['tickets.nearSelloutThreshold']);
  });

  it('defaults the admission mode for waiting-room settings stored before adaptive admission', () => {
    const c = defaultExperiment(config);
    const stored = { ...c, waitingRoom: { enabled: true, activeUsersPerServer: 30, admissionTtlSeconds: 90 } };
    expect(validateExperiment(stored, config).waitingRoom).toEqual({ enabled: true, activeUsersPerServer: 30, admissionTtlSeconds: 90, mode: 'fixed', maxUsersPerServer: 200 });
  });

  it('range-checks the admission mode and the adaptive maximum', () => {
    const c = defaultExperiment(config);
    const wr = { enabled: true, activeUsersPerServer: 25, admissionTtlSeconds: 60 };
    expect(Object.keys(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'learned' } }))).toContain('waitingRoom.mode');
    expect(Object.keys(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'adaptive', maxUsersPerServer: 0 } }))).toContain('waitingRoom.maxUsersPerServer');
    expect(Object.keys(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'adaptive', maxUsersPerServer: 1001 } }))).toContain('waitingRoom.maxUsersPerServer');
    expect(Object.keys(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'adaptive', maxUsersPerServer: 2.5 } }))).toContain('waitingRoom.maxUsersPerServer');
  });

  it('requires the adaptive maximum to be at least the starting limit, but only in adaptive mode', () => {
    const c = defaultExperiment(config);
    const wr = { enabled: true, activeUsersPerServer: 50, admissionTtlSeconds: 60, maxUsersPerServer: 40 };
    expect(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'adaptive' } })['waitingRoom.maxUsersPerServer']).toMatch(/at least the starting limit/);
    expect(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'adaptive', maxUsersPerServer: 50 } })).toEqual({});
    expect(errorsOf({ ...c, waitingRoom: { ...wr, mode: 'fixed' } })).toEqual({});
  });

  it('enforces the approved instance maximum and server ordering', () => {
    const c = defaultExperiment(config);
    expect(Object.keys(errorsOf({ ...c, servers: { ...c.servers, max: 6 } }))).toContain('servers.max');
    expect(Object.keys(errorsOf({ ...c, servers: { ...c.servers, initial: 3, max: 2 } }))).toContain('servers.initial');
  });

  it('requires the traffic mix to sum to 100 and pattern parameters to be present', () => {
    const c = defaultExperiment(config);
    expect(errorsOf({ ...c, traffic: { ...c.traffic, mix: { browse: 50, availability: 20, reserve: 20, purchase: 20 } } })).toHaveProperty(['traffic.mix']);
    expect(errorsOf({ ...c, traffic: { ...c.traffic, pattern: { type: 'spike', baseRate: 1 } } })).toHaveProperty(['traffic.pattern.peakRate']);
  });

  it('flags expensive configurations for explicit confirmation', () => {
    const c = defaultExperiment(config);
    expect(expensiveReasons(c, config)).toEqual([]);
    const big = { ...c, traffic: { ...c.traffic, durationSeconds: 1800, pattern: { type: 'constant' as const, rate: 300 } } };
    expect(expensiveReasons(big, config)).toHaveLength(2);
  });

  it('splits inventory exactly', () => {
    expect(splitInventory(100, 3)).toEqual([34, 33, 33]);
    expect(splitInventory(100, 3).reduce((a, b) => a + b)).toBe(100);
  });
});

describe('traffic patterns', () => {
  it('computes requested rates for each pattern', () => {
    expect(rateAt({ type: 'constant', rate: 7 }, 99, 100)).toBe(7);
    expect(rateAt({ type: 'ramp_up', startRate: 5, endRate: 105 }, 50, 100)).toBe(55);
    expect(rateAt({ type: 'ramp_down', startRate: 100, endRate: 0 }, 25, 100)).toBe(75);
    const spike = { type: 'spike' as const, baseRate: 2, peakRate: 150, spikeAtSeconds: 10, spikeDurationSeconds: 5 };
    expect([9, 10, 14, 15].map((t) => rateAt(spike, t, 60))).toEqual([2, 150, 150, 2]);
    const custom = { type: 'custom' as const, points: [{ atSeconds: 0, rate: 0 }, { atSeconds: 10, rate: 100 }, { atSeconds: 20, rate: 50 }] };
    expect([0, 5, 15, 30].map((t) => rateAt(custom, t, 30))).toEqual([0, 50, 75, 50]);
    expect(peakRate(custom, 30)).toBe(100);
  });

  it('caps expected arrivals by the configured total', () => {
    expect(expectedArrivals({ type: 'constant', rate: 10 }, 60, 0)).toBe(600);
    expect(expectedArrivals({ type: 'constant', rate: 10 }, 60, 100)).toBe(100);
  });
});
