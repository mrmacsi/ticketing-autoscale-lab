<script setup lang="ts">
import { computed } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import FreshnessBadge from '@/components/ui/FreshnessBadge.vue';
import Stat from '@/components/ui/Stat.vue';
import { fmtFractionPct, fmtNumber } from '@/lib/format';
import type { Freshness, MigView } from '@/types/contracts';

const props = defineProps<{ mig: MigView | null; instanceCards: number; freshness: Freshness | null; approvedMaxFallback: number | null }>();

const stale = computed(() => !props.mig || props.freshness?.status === 'stale' || props.freshness?.status === 'missing');
const a = computed(() => props.mig?.autoscaler ?? null);
// The control API reports this note when it runs without Google Cloud APIs (docker compose).
const localMode = computed(() => /not configured/i.test(props.freshness?.note ?? ''));
const baselineText = computed(() => {
  const b = props.mig?.baseline;
  if (!b) return '';
  return `Terraform baseline: min ${b.minReplicas}, max ${b.maxReplicas}, CPU target ${fmtFractionPct(b.cpuTarget, 0)}, init ${b.initPeriodSeconds}s, scale-in ${b.scaleInWindowSeconds}s`;
});
</script>

<template>
  <div class="space-y-2">
    <div class="flex flex-wrap items-center gap-2">
      <FreshnessBadge :freshness="freshness" label="MIG" />
      <template v-if="mig">
        <Badge v-if="mig.overridesActive" tone="amber" dot :title="baselineText" data-testid="overrides-badge">Runtime overrides active (vs Terraform baseline)</Badge>
        <Badge v-else tone="emerald" dot :title="baselineText" data-testid="overrides-badge">Autoscaler at Terraform baseline</Badge>
        <Badge :tone="mig.isStable ? 'emerald' : 'amber'">{{ mig.isStable ? 'MIG stable' : 'MIG changing' }}</Badge>
        <span class="truncate text-[11px] text-slate-500">{{ mig.name }} · {{ mig.zone }}</span>
      </template>
      <Badge v-else tone="red" dot>MIG telemetry unavailable</Badge>
    </div>
    <p v-if="!mig && localMode" class="rounded border border-slate-700 bg-slate-800/60 px-3 py-2 text-xs text-slate-300" data-testid="local-mode-note">
      Local mode: there is no Managed Instance Group, autoscaler, load balancer or Cloud Monitoring on this machine, so these
      values stay N/A instead of being simulated. Traffic, latency, outcome and inventory charts are live. Server cards and
      scaling appear once the lab runs on Google Cloud.
    </p>
    <p v-else-if="!mig && freshness?.note" class="text-xs text-red-300" data-testid="mig-error-note">{{ freshness.note }}</p>
    <div :class="['grid grid-cols-2 gap-2 min-[480px]:grid-cols-4 xl:grid-cols-8', stale ? 'opacity-70' : '']">
      <Stat label="Actual instances" :value="fmtNumber(mig?.actualCount)" emphasis testid="stat-actual" hint="from MIG, not CPU" />
      <Stat label="Desired / target" :value="fmtNumber(mig?.targetSize)" testid="stat-target" :hint="a?.recommendedSize != null ? `recommended ${a.recommendedSize}` : 'recommended N/A'" />
      <Stat label="Running" :value="fmtNumber(mig?.runningCount)" testid="stat-running" />
      <Stat label="Healthy" :value="fmtNumber(mig?.healthyCount)" testid="stat-healthy" />
      <Stat label="Min" :value="fmtNumber(a?.minReplicas)" testid="stat-min" :hint="mig ? `baseline ${mig.baseline.minReplicas}` : undefined" />
      <Stat label="Max" :value="fmtNumber(a?.maxReplicas)" testid="stat-max" :hint="mig ? `baseline ${mig.baseline.maxReplicas}` : undefined" />
      <Stat label="Approved max" :value="fmtNumber(mig?.approvedMaxInstances ?? approvedMaxFallback)" testid="stat-approved" hint="hard ceiling" />
      <Stat label="CPU target" :value="fmtFractionPct(a?.cpuTarget, 0)" testid="stat-cpu-target" :hint="mig ? `baseline ${fmtFractionPct(mig.baseline.cpuTarget, 0)}` : undefined" />
    </div>
    <div class="flex flex-wrap items-start gap-x-3 gap-y-1 text-xs text-slate-400">
      <span>Autoscaler status: <span class="font-medium text-slate-200" data-testid="autoscaler-status">{{ a?.status ?? 'N/A' }}</span></span>
      <span>Instance cards: <span class="font-medium text-slate-200" data-testid="instance-card-count">{{ instanceCards }}</span></span>
      <span v-if="a">init period {{ a.initPeriodSeconds }}s · scale-in window {{ a.scaleInWindowSeconds ?? 'N/A' }}{{ a.scaleInWindowSeconds != null ? 's' : '' }}</span>
    </div>
    <ul v-if="a?.messages?.length" class="space-y-0.5 text-xs text-amber-200/90">
      <li v-for="(m, i) in a.messages" :key="i" class="break-words">• {{ m }}</li>
    </ul>
  </div>
</template>
