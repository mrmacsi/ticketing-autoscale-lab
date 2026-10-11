import { describe, expect, it } from 'vitest';
import { costModel, estimateExperimentCost } from '../src/costs.js';
import { testConfig } from './fakes.js';

describe('cost model (list-price estimates)', () => {
  it('prices the lab machine types: n2-standard-2 app VMs and an e2-standard-2 generator', () => {
    const config = testConfig();
    expect(config.appMachineType).toBe('n2-standard-2');
    expect(config.generatorMachineType).toBe('e2-standard-2');
    expect(config.pricing).toMatchObject({ appVmPerHour: 0.113, generatorVmPerHour: 0.078, queueVmPerHour: 0.0195, queueVms: 2, redisPerHour: 0.058 });
    const model = costModel(config, 1);
    const app = model.rows.find((r) => r.resource === 'Application VMs (n2-standard-2)');
    expect(app).toMatchObject({ perAppServer: true, quantity: 1 });
    expect(app?.unitCostPerHour).toBeCloseTo(0.1152, 6);
    expect(model.rows.find((r) => r.resource.startsWith('Load-generator VM (e2-standard-2)'))?.unitCostPerHour).toBeCloseTo(0.083, 6);
    // Only the application-VM row scales with the live VM count.
    expect(model.rows.filter((r) => r.perAppServer)).toHaveLength(1);
  });

  it('includes the waiting-room queue VMs and Redis only when the waiting room is deployed', () => {
    const without = costModel(testConfig(), 1);
    expect(without.rows.some((r) => /queue|Redis/i.test(r.resource))).toBe(false);

    const deployed = costModel(testConfig({ WAITING_ROOM_URL: 'http://10.60.0.20:8080' }), 1);
    const queue = deployed.rows.find((r) => r.resource === 'Waiting-room queue VMs (2 × e2-small)');
    expect(queue).toMatchObject({ unitCostPerHour: 0.0195, quantity: 2, perHour: 0.039, billedWhileIdle: true, perAppServer: false });
    expect(deployed.rows.find((r) => r.resource === 'Memorystore for Redis (basic, 1 GB)')).toMatchObject({ perHour: 0.058, billedWhileIdle: true });
    expect(deployed.idlePerHour).toBeCloseTo(without.idlePerHour + 0.039 + 0.058, 4);
    expect(deployed.peakPerHour - deployed.idlePerHour).toBeCloseTo(without.peakPerHour - without.idlePerHour, 4);
    expect(deployed.notes.join(' ')).toMatch(/waiting-room VMs and Redis/);
    // The experiment estimate in reports covers them too.
    const env = { WAITING_ROOM_URL: 'http://10.60.0.20:8080' };
    expect(estimateExperimentCost(testConfig(env), 3, 3600)).toBeCloseTo(estimateExperimentCost(testConfig(), 3, 3600) + 0.097, 4);
  });

  it('takes every price and the queue VM count from PRICE_* overrides', () => {
    const config = testConfig({ WAITING_ROOM_URL: 'http://q', PRICE_QUEUE_VMS: '3', PRICE_QUEUE_VM_PER_HOUR: '0.02', PRICE_REDIS_PER_HOUR: '0.1',
      PRICE_APP_VM_PER_HOUR: '0.2', APP_MACHINE_TYPE: 'n2-standard-4' });
    const model = costModel(config, 2);
    expect(model.rows.find((r) => r.resource === 'Waiting-room queue VMs (3 × e2-small)')).toMatchObject({ quantity: 3, perHour: 0.06 });
    expect(model.rows.find((r) => r.resource.startsWith('Memorystore'))?.perHour).toBe(0.1);
    const app = model.rows.find((r) => r.resource === 'Application VMs (n2-standard-4)');
    expect(app?.quantity).toBe(2);
    expect(app?.unitCostPerHour).toBeCloseTo(0.2022, 6);
  });
});
