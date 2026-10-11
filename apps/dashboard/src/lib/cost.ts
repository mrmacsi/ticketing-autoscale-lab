import type { CostModel, CostRow } from '@/types/contracts';

/**
 * The application-VM row (`perAppServer`) is scaled by the live instance count; every other row
 * (Cloud SQL, load balancer, generator, waiting-room VMs, Redis, …) contributes its fixed `perHour`.
 * Rows that are not billed while idle only count while an experiment is active. Older control APIs
 * send no `perAppServer`: their per-VM row is the one whose unit starts with "instance-hour (VM".
 */
export function isPerInstanceRow(row: CostRow): boolean {
  return row.perAppServer ?? /^instance-hour \(VM/i.test(row.unit);
}

export interface CostEstimate {
  idlePerHour: number;
  currentPerHour: number | null;
  perInstancePerHour: number | null;
}

export function estimateCost(model: CostModel, liveInstances: number | null, experimentActive: boolean): CostEstimate {
  const instanceRows = model.rows.filter(isPerInstanceRow);
  const perInstance = instanceRows.length ? instanceRows.reduce((sum, r) => sum + r.unitCostPerHour, 0) : null;
  if (liveInstances == null || perInstance == null) {
    return { idlePerHour: model.idlePerHour, currentPerHour: null, perInstancePerHour: perInstance };
  }
  let fixed = 0;
  for (const r of model.rows) {
    if (isPerInstanceRow(r)) continue;
    if (r.billedWhileIdle || experimentActive) fixed += r.perHour;
  }
  return { idlePerHour: model.idlePerHour, currentPerHour: fixed + perInstance * liveInstances, perInstancePerHour: perInstance };
}
