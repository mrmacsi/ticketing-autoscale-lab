<script setup lang="ts">
import { computed } from 'vue';
import EChart from './EChart.vue';
import FreshnessBadge from '@/components/ui/FreshnessBadge.vue';
import type { ChartOption } from '@/charts/theme';
import type { Freshness } from '@/types/contracts';

const props = defineProps<{ title: string; option: ChartOption; hasData: boolean; freshness?: Freshness | null; height?: string }>();

const stale = computed(() => props.freshness && (props.freshness.status === 'stale' || props.freshness.status === 'missing'));
</script>

<template>
  <section class="print-dark min-w-0 rounded-lg border border-slate-800 bg-slate-900/60" :data-testid="`chart-${title}`">
    <header class="flex flex-wrap items-center justify-between gap-2 px-3 pt-2.5">
      <h3 class="text-sm font-semibold text-slate-100">{{ title }}</h3>
      <FreshnessBadge v-if="freshness !== undefined" :freshness="freshness" />
    </header>
    <div v-if="$slots.note" class="px-3 pt-1 text-[11px] text-slate-400"><slot name="note" /></div>
    <div class="relative px-1 pb-2">
      <EChart :option="option" :height="height ?? '260px'" :label="title" />
      <div
        v-if="!hasData"
        class="absolute inset-0 flex items-center justify-center rounded bg-slate-950/70 text-sm text-slate-400"
        data-testid="chart-no-data"
      >
        No data received yet
      </div>
      <div
        v-else-if="stale"
        class="pointer-events-none absolute inset-x-3 top-1 rounded bg-amber-500/15 px-2 py-1 text-center text-xs text-amber-200 ring-1 ring-amber-500/40"
        data-testid="chart-stale"
      >
        Telemetry {{ freshness?.status }} — showing last received data only
      </div>
    </div>
  </section>
</template>
