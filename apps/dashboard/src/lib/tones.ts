import type { Attribution, ExperimentState, FreshnessStatus, InstancePhase, TimelineKind } from '@/types/contracts';

export type Tone = 'emerald' | 'amber' | 'orange' | 'red' | 'slate' | 'sky' | 'violet' | 'yellow';

// Full class strings (not interpolated) so Tailwind can see them.
export const TONE_CLASS: Record<Tone, string> = {
  emerald: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/40',
  amber: 'bg-amber-500/15 text-amber-300 ring-amber-500/40',
  orange: 'bg-orange-500/15 text-orange-300 ring-orange-500/40',
  red: 'bg-red-500/15 text-red-300 ring-red-500/40',
  slate: 'bg-slate-500/15 text-slate-300 ring-slate-500/40',
  sky: 'bg-sky-500/15 text-sky-300 ring-sky-500/40',
  violet: 'bg-violet-500/15 text-violet-300 ring-violet-500/40',
  yellow: 'bg-yellow-500/15 text-yellow-200 ring-yellow-500/40',
};

export const DOT_CLASS: Record<Tone, string> = {
  emerald: 'bg-emerald-400',
  amber: 'bg-amber-400',
  orange: 'bg-orange-400',
  red: 'bg-red-400',
  slate: 'bg-slate-400',
  sky: 'bg-sky-400',
  violet: 'bg-violet-400',
  yellow: 'bg-yellow-300',
};

export function phaseTone(phase: InstancePhase): Tone {
  switch (phase) {
    case 'healthy':
      return 'emerald';
    case 'starting':
      return 'amber';
    case 'draining':
    case 'terminating':
      return 'orange';
    case 'unhealthy':
      return 'red';
    default:
      return 'slate';
  }
}

export function healthTone(value: string | null | undefined): Tone {
  switch ((value ?? '').toUpperCase()) {
    case 'HEALTHY':
    case 'RUNNING':
    case 'NONE':
      return 'emerald';
    case 'PROVISIONING':
    case 'STAGING':
    case 'CREATING':
    case 'VERIFYING':
    case 'RECREATING':
    case 'REFRESHING':
    case 'RESTARTING':
      return 'amber';
    case 'DRAINING':
    case 'STOPPING':
    case 'DELETING':
    case 'ABANDONING':
    case 'SUSPENDING':
      return 'orange';
    case 'UNHEALTHY':
    case 'TIMEOUT':
    case 'TERMINATED':
      return 'red';
    default:
      return 'slate';
  }
}

export function stateTone(state: ExperimentState | null | undefined): Tone {
  switch (state) {
    case 'running':
      return 'emerald';
    case 'ready':
      return 'sky';
    case 'preparing':
    case 'stopping':
      return 'amber';
    case 'cooling_down':
      return 'violet';
    case 'completed':
      return 'emerald';
    case 'failed':
      return 'red';
    default:
      return 'slate';
  }
}

export function freshnessTone(status: FreshnessStatus | null | undefined): Tone {
  switch (status) {
    case 'live':
      return 'emerald';
    case 'delayed':
      return 'sky';
    case 'stale':
      return 'amber';
    case 'missing':
      return 'red';
    default:
      return 'slate';
  }
}

export function attributionTone(a: Attribution | null | undefined): Tone {
  switch (a) {
    case 'autoscaler':
      return 'sky';
    case 'baseline':
      return 'violet';
    case 'operator':
      return 'amber';
    default:
      return 'slate';
  }
}

export function kindTone(kind: TimelineKind): Tone {
  switch (kind) {
    case 'scaling':
      return 'sky';
    case 'instance':
      return 'violet';
    case 'health':
      return 'emerald';
    case 'cpu':
      return 'yellow';
    case 'inventory':
      return 'orange';
    case 'error':
      return 'red';
    case 'traffic':
      return 'emerald';
    case 'config':
      return 'amber';
    case 'waiting_room':
      return 'sky';
    default:
      return 'slate';
  }
}

export const KIND_GLYPH: Record<TimelineKind, string> = {
  experiment: '◆',
  traffic: '↗',
  scaling: '⇅',
  instance: '▣',
  health: '♥',
  cpu: '◔',
  inventory: '▤',
  config: '⚙',
  error: '!',
  waiting_room: '≡',
};
