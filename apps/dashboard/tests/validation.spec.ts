import { describe, expect, it } from 'vitest';
import { validateConfig } from '@/lib/validation';
import { makeConfig, makeMeta } from './fixtures';

const meta = makeMeta();

describe('validateConfig', () => {
  it('accepts the default config', () => {
    const r = validateConfig(makeConfig(), meta);
    expect(r.errors).toEqual({});
    expect(r.valid).toBe(true);
    expect(r.mixTotal).toBe(100);
  });

  it('requires the journey mix to sum to 100', () => {
    const c = makeConfig();
    c.traffic.mix.purchase = 20;
    const r = validateConfig(c, meta);
    expect(r.valid).toBe(false);
    expect(r.errors['traffic.mix']).toMatch(/sum to 100 % \(currently 105 %\)/);
  });

  it('enforces min ≤ initial ≤ max', () => {
    const c = makeConfig();
    c.servers = { ...c.servers, min: 3, initial: 2, max: 5 };
    expect(validateConfig(c, meta).errors['servers.initial']).toMatch(/≥ minimum/);
    c.servers = { ...c.servers, min: 1, initial: 6, max: 5 };
    expect(validateConfig(c, meta).errors['servers.initial']).toMatch(/≤ maximum/);
    c.servers = { ...c.servers, min: 4, initial: 4, max: 3 };
    expect(validateConfig(c, meta).errors['servers.min']).toMatch(/≤ maximum/);
  });

  it('rejects max above the approved maximum', () => {
    const c = makeConfig();
    c.servers.max = meta.approvedMaxInstances + 1;
    expect(validateConfig(c, meta).errors['servers.max']).toMatch(/approved maximum of 8|at most 8/);
  });

  it('applies meta.limits to rate, VUs and duration', () => {
    const c = makeConfig();
    c.traffic.pattern = { type: 'constant', rate: 501 };
    c.traffic.maxVUs = 2001;
    c.traffic.durationSeconds = 1801;
    const e = validateConfig(c, meta).errors;
    expect(e['traffic.pattern.rate']).toBeDefined();
    expect(e['traffic.maxVUs']).toBeDefined();
    expect(e['traffic.durationSeconds']).toBeDefined();
  });

  it('validates the custom schedule rows', () => {
    const c = makeConfig();
    c.traffic.pattern = { type: 'custom', points: [{ atSeconds: 0, rate: 5 }, { atSeconds: 0, rate: 10 }, { atSeconds: 9999, rate: 10 }] };
    const e = validateConfig(c, meta).errors;
    expect(e['traffic.pattern.points']).toMatch(/unique/);
    expect(e['traffic.pattern.points.2.atSeconds']).toBeDefined();
  });

  it('keeps synthetic CPU off unless a cap is configured', () => {
    const c = makeConfig();
    c.traffic.behaviour.syntheticCpuMs = 50;
    expect(validateConfig(c, meta).errors['traffic.behaviour.syntheticCpuMs']).toBeDefined();
    c.syntheticCpuMaxMs = 100;
    expect(validateConfig(c, meta).errors['traffic.behaviour.syntheticCpuMs']).toBeUndefined();
  });

  it('flags NaN (empty input) as invalid rather than treating it as 0', () => {
    const c = makeConfig();
    c.servers.min = Number.NaN;
    expect(validateConfig(c, meta).errors['servers.min']).toBeDefined();
  });

  it('reports expensive thresholds', () => {
    const c = makeConfig();
    c.servers.max = 6;
    c.traffic.pattern = { type: 'spike', baseRate: 5, peakRate: 300, spikeAtSeconds: 10, spikeDurationSeconds: 10 };
    const r = validateConfig(c, meta);
    expect(r.valid).toBe(true);
    expect(r.expensiveReasons).toHaveLength(2);
  });
});
