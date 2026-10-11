import type { CostModel, CostRow } from '@/types/contracts';

/**
 * Cost rows whose unit mentions "instance" are treated as per-app-server rows and scaled by
 * the live instance count; every other row contributes its fixed `perHour`. Rows that are not
 * billed while idle (e.g. the load-generator VM) only count while an experiment is active.
 */
export function isPerInstanceRow(row: CostRow): boolean {
  return /instance/i.test(row.unit);
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
