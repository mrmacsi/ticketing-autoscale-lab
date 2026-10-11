<script setup lang="ts">
import { computed } from 'vue';
import NumField from './NumField.vue';
import { useDraft } from '@/store/draft';
import type { SchedulePoint } from '@/types/contracts';

defineProps<{ maxRate: number }>();
const draft = useDraft();
const pattern = computed(() => draft.config.value!.traffic.pattern);
const points = computed<SchedulePoint[]>(() => pattern.value.points ?? []);

function ensure(): SchedulePoint[] {
  if (!pattern.value.points) pattern.value.points = [];
  return pattern.value.points;
}
function add() {
  const pts = ensure();
  const last = pts[pts.length - 1];
  const duration = draft.config.value!.traffic.durationSeconds;
  const at = last ? Math.min(duration, last.atSeconds + 60) : 0;
  pts.push({ atSeconds: at, rate: last?.rate ?? 10 });
}
function remove(i: number) {
  ensure().splice(i, 1);
}
function sort() {
  ensure().sort((a, b) => a.atSeconds - b.atSeconds);
}
</script>

<template>
  <div class="space-y-2" data-testid="custom-schedule">
    <div class="flex items-center justify-between">
      <span class="label mb-0">Custom schedule (piecewise linear)</span>
      <div class="flex gap-1">
        <button type="button" class="btn btn-ghost text-xs" @click="sort">Sort</button>
        <button type="button" class="btn btn-default text-xs" data-testid="add-point" @click="add">+ Point</button>
      </div>
    </div>
    <p class="text-xs leading-relaxed text-slate-400">Add a point for each change in traffic. Times are measured from traffic start; the rate changes gradually between points and stays at the last rate afterwards. Sort puts points in time order; ✕ removes a point.</p>
    <p v-if="draft.fieldError('traffic.pattern.points')" class="text-xs text-red-300" role="alert">{{ draft.fieldError('traffic.pattern.points') }}</p>
    <div v-for="(p, i) in points" :key="i" class="grid grid-cols-[1fr_1fr_auto] items-end gap-2">
      <NumField v-model="p.atSeconds" label="At (s)" description="Seconds from traffic start for this point. Use 0 for the initial rate; later points define when the rate changes." :min="0" :error="draft.fieldError(`traffic.pattern.points.${i}.atSeconds`)" />
      <NumField v-model="p.rate" label="Rate (arrivals/s)" description="Requested customer journeys per second at this point. The generator changes the rate gradually between neighbouring points." :min="0" :max="maxRate" step="any" :error="draft.fieldError(`traffic.pattern.points.${i}.rate`)" />
      <button type="button" class="btn btn-ghost mb-0.5 text-red-300" :aria-label="`Remove point ${i + 1}`" @click="remove(i)">✕</button>
    </div>
  </div>
</template>
