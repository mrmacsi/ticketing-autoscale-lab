<script setup lang="ts">
import { computed } from 'vue';
import { estimateCost } from '@/lib/cost';
import { fmtUsd } from '@/lib/format';
import type { CostModel } from '@/types/contracts';

const props = defineProps<{ model: CostModel | null; liveInstances: number | null; experimentActive: boolean }>();
const est = computed(() => (props.model ? estimateCost(props.model, props.liveInstances, props.experimentActive) : null));
const tooltip = computed(() => {
  if (!props.model) return 'Cost model not loaded';
  const rows = props.model.rows.map((r) => `${r.resource}: ${fmtUsd(r.perHour, 3)}/h (${r.quantity} × ${r.unit})${r.billedWhileIdle ? ' · billed while idle' : ''}`);
  return ['Estimate only — list prices from the control API cost model, not billing data.', ...rows, ...props.model.notes].join('\n');
});
</script>

<template>
  <div class="flex items-center gap-2 text-[11px] text-slate-400" :title="tooltip" data-testid="cost">
    <span>est. idle <span class="font-medium tabular-nums text-slate-200">{{ fmtUsd(est?.idlePerHour, 3) }}/h</span></span>
    <span>
      now <span class="font-medium tabular-nums text-slate-200" data-testid="cost-current">{{ fmtUsd(est?.currentPerHour, 3) }}/h</span>
    </span>
    <span class="rounded bg-slate-800 px-1 text-[10px] uppercase tracking-wide text-slate-400">estimate</span>
  </div>
</template>
