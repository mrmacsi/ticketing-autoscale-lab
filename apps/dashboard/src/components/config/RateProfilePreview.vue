<script setup lang="ts">
import { computed } from 'vue';
import EChart from '@/components/charts/EChart.vue';
import { baseOption, C, line, type ChartOption } from '@/charts/theme';
import { fmtClock, fmtNumber, fmtRate } from '@/lib/format';
import { useDraft } from '@/store/draft';

const draft = useDraft();
const profile = computed(() => draft.profile.value);
const pattern = computed(() => draft.config.value?.traffic.pattern.type);
const totalArrivals = computed(() => draft.config.value?.traffic.totalArrivals ?? 0);

const option = computed<ChartOption>(() => {
  const base = baseOption('arrivals/s');
  return {
    ...base,
    useUTC: false,
    grid: { left: 8, right: 12, top: 16, bottom: 8, containLabel: true },
    legend: { show: false },
    xAxis: {
      type: 'value',
      min: 0,
      axisLabel: { color: C.muted, formatter: (v: number) => fmtClock(v), hideOverlap: true },
      axisLine: { lineStyle: { color: C.axis } },
      splitLine: { show: false },
    },
    series: [line('Requested arrivals/s', C.target, profile.value?.points ?? [], { step: pattern.value === 'spike' || pattern.value === 'random_bursts' ? 'end' : undefined, areaStyle: { color: 'rgba(56,189,248,0.10)' } })],
  };
});
</script>

<template>
  <div class="rounded-md border border-slate-800 bg-slate-950/40 p-2.5">
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <h4 class="text-xs font-semibold text-slate-300">Requested arrival-rate profile (plan, not telemetry)</h4>
      <span class="text-[11px] text-slate-500">x = time from traffic start</span>
    </div>
    <EChart :option="option" height="150px" label="Requested arrival-rate profile preview" />
    <dl class="grid grid-cols-2 gap-2 text-xs @md:grid-cols-4">
      <div>
        <dt class="text-slate-500">Expected arrivals</dt>
        <dd class="font-semibold tabular-nums text-slate-100" data-testid="expected-arrivals">{{ fmtNumber(profile?.expectedArrivals ?? null) }}</dd>
      </div>
      <div>
        <dt class="text-slate-500">Peak rate</dt>
        <dd class="font-semibold tabular-nums text-slate-100" data-testid="peak-rate">{{ fmtRate(profile?.peakRate ?? null) }}/s</dd>
      </div>
      <div>
        <dt class="text-slate-500">Average rate</dt>
        <dd class="font-semibold tabular-nums text-slate-100">{{ fmtRate(profile?.averageRate ?? null) }}/s</dd>
      </div>
      <div>
        <dt class="text-slate-500">Arrival cap</dt>
        <dd class="font-semibold tabular-nums text-slate-100">
          <template v-if="totalArrivals > 0">
            {{ fmtNumber(totalArrivals) }}<span v-if="profile?.capReachedAtSeconds != null" class="font-normal text-amber-300"> · reached at {{ fmtClock(profile.capReachedAtSeconds) }}</span>
          </template>
          <template v-else>unlimited</template>
        </dd>
      </div>
    </dl>
    <p v-if="pattern === 'random_bursts'" class="mt-1 text-[11px] text-amber-200/80">
      Burst positions are chosen by the load generator from the seed; the preview spaces them evenly. Totals are unaffected.
    </p>
  </div>
</template>
