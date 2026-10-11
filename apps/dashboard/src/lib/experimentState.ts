import type { ExperimentState } from '@/types/contracts';

export type ExperimentAction = 'prepare' | 'start' | 'stop' | 'complete' | 'resetInventory' | 'reset' | 'export';

const ACTIVE: readonly ExperimentState[] = ['preparing', 'ready', 'running', 'stopping', 'cooling_down'];

export function isActive(state: ExperimentState | null | undefined): boolean {
  return state != null && ACTIVE.includes(state);
}

/** Why an action is unavailable in this state, or null when it is allowed. */
export function blockedReason(action: ExperimentAction, state: ExperimentState | null, opts: { overridesActive?: boolean } = {}): string | null {
  const s = state ?? 'idle';
  switch (action) {
    case 'prepare':
      return isActive(s) ? `Experiment is ${label(s)}; finish or reset it first.` : null;
    case 'start':
      return s === 'ready' ? null : 'Traffic can only be started once the experiment is ready.';
    case 'stop':
      return s === 'running' ? null : 'Traffic is not running.';
    case 'complete':
      return s === 'cooling_down' ? null : 'Observation can only be completed during cool-down.';
    case 'resetInventory':
      if (s === 'running' || s === 'stopping') return 'Inventory cannot be reset while traffic is running or stopping — stop traffic and wait for cool-down.';
      if (s === 'idle' || s === 'preparing') return 'No prepared ticket event to reset yet.';
      return null;
    case 'reset':
      return s === 'idle' && !opts.overridesActive ? 'Nothing to reset: no experiment and the autoscaler is at its baseline.' : null;
    case 'export':
      return s === 'idle' ? 'No experiment to export.' : null;
  }
}

export function label(state: ExperimentState | string): string {
  return state.replace(/_/g, ' ');
}
