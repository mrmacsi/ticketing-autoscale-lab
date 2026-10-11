import type { InstancePhase } from '../types.js';

const TERMINATING_ACTIONS = new Set(['DELETING', 'ABANDONING']);
const STARTING_ACTIONS = new Set(['CREATING', 'CREATING_WITHOUT_RETRIES', 'RECREATING', 'VERIFYING', 'STARTING', 'RESUMING']);
const TERMINATING_STATUS = new Set(['STOPPING', 'STOPPED', 'TERMINATED', 'SUSPENDING', 'SUSPENDED']);
const STARTING_STATUS = new Set(['PROVISIONING', 'STAGING']);

/** Derives the card state purely from Compute Engine / load-balancer reported states. */
export function instancePhase(vmStatus: string, currentAction: string, migHealth: string | null, lbHealth: string | null): InstancePhase {
  if (TERMINATING_ACTIONS.has(currentAction) || TERMINATING_STATUS.has(vmStatus)) return 'terminating';
  if (lbHealth === 'DRAINING' || migHealth === 'DRAINING') return 'draining';
  if (STARTING_STATUS.has(vmStatus) || STARTING_ACTIONS.has(currentAction)) return 'starting';
  if (vmStatus !== 'RUNNING') return 'unknown';
  if (lbHealth === 'HEALTHY' || (lbHealth === null && migHealth === 'HEALTHY')) return 'healthy';
  if (lbHealth === 'UNHEALTHY' || migHealth === 'UNHEALTHY' || migHealth === 'TIMEOUT') return 'unhealthy';
  return 'starting';
}
