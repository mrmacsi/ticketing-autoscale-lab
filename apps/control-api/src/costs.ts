import type { AppConfig } from './config.js';

export interface CostRow {
  resource: string; unit: string; unitCostPerHour: number; quantity: number; perHour: number; billedWhileIdle: boolean; note: string;
}

/**
 * List-price ESTIMATES for europe-west2 (override with PRICE_* env vars). Real bills depend on
 * discounts, network egress and exact usage; this is for orientation, not accounting.
 */
export function costModel(config: AppConfig, appInstances: number) {
  const p = config.pricing;
  const row = (resource: string, unit: string, unitCost: number, quantity: number, idle: boolean, note: string): CostRow => ({
    resource, unit, unitCostPerHour: unitCost, quantity, perHour: round(unitCost * quantity), billedWhileIdle: idle, note,
  });
  const rows = [
    row(`Application VMs (${config.appMachineType})`, 'instance-hour (VM + 10 GB disk)', p.appVmPerHour + p.appDiskPerHour, appInstances, true,
      'Billed per running VM; the MIG keeps at least the minimum running.'),
    row(`Cloud SQL PostgreSQL (${config.dbTier})`, 'instance-hour incl. storage', p.cloudSqlPerHour, 1, true, 'Billed 24/7 until destroyed.'),
    row('External Application Load Balancer', 'forwarding rule + static IP', p.loadBalancerPerHour + p.lbIpPerHour, 1, true,
      'Billed 24/7 until destroyed, plus data processing per GB.'),
    row(`Load-generator VM (${config.generatorMachineType})`, 'VM-hour incl. disk + external IP', p.generatorVmPerHour + p.generatorIpPerHour, 1, true,
      'Runs idle between experiments.'),
    row('Cloud Run control plane (1 vCPU, always allocated)', 'instance-hour', p.cloudRunPerHour, 1, true,
      'Min one instance so telemetry keeps flowing without a browser open.'),
    row('Firestore, Secret Manager, Artifact Registry, Logging, Monitoring', 'approx.', p.miscPerHour, 1, true,
      'Mostly within free tiers at lab volumes.'),
  ];
  const vmUnit = p.appVmPerHour + p.appDiskPerHour;
  const idlePerHour = round(rows.reduce((sum, r) => sum + r.perHour, 0) - vmUnit * appInstances + vmUnit * config.baseline.minReplicas);
  const peakPerHour = round(idlePerHour + vmUnit * (config.approvedMaxInstances - config.baseline.minReplicas));
  return {
    currency: 'USD' as const,
    rows,
    idlePerHour,
    peakPerHour,
    notes: [
      'Estimates from public list prices for europe-west2; not a bill.',
      'Network egress and load-balancer data processing are extra (small for this lab).',
      'Stopping traffic does not stop billing: Cloud SQL, the load balancer, the generator VM, Cloud Run and the minimum application VMs keep running until `scripts/terraform-destroy.sh`.',
      'A budget alert notifies; it is not a spending cap.',
    ],
  };
}

export function estimateExperimentCost(config: AppConfig, maxInstances: number, seconds: number): number {
  const model = costModel(config, config.baseline.minReplicas);
  const vm = config.pricing.appVmPerHour + config.pricing.appDiskPerHour;
  // Upper bound: assume the maximum instance count for the whole run.
  const perHour = model.idlePerHour + vm * Math.max(0, maxInstances - config.baseline.minReplicas);
  return round((perHour * seconds) / 3600);
}

const round = (n: number) => Math.round(n * 10000) / 10000;
