<script setup lang="ts">
import FreshnessBadge from '@/components/ui/FreshnessBadge.vue';
import { ageSeconds, fmtAge, fmtTimeOfDay } from '@/lib/format';
import type { Freshness, FreshnessSource, LiveSnapshot } from '@/types/contracts';

const props = defineProps<{ freshness: LiveSnapshot['freshness'] | null; nowMs: number }>();

const SOURCES: Array<{ key: FreshnessSource; label: string; origin: string }> = [
  { key: 'traffic', label: 'Traffic', origin: 'load generator (Firestore)' },
  { key: 'mig', label: 'MIG', origin: 'Compute API' },
  { key: 'cpu', label: 'CPU', origin: 'Cloud Monitoring' },
  { key: 'inventory', label: 'Inventory', origin: 'Cloud SQL' },
  { key: 'lb', label: 'LB health', origin: 'backend service' },
  { key: 'waitingRoom', label: 'Waiting room', origin: 'queue server /admin/stats' },
];

function age(f: Freshness | undefined): string {
  if (!f) return 'N/A';
  const live = ageSeconds(f.lastAt, props.nowMs);
  return fmtAge(live ?? f.ageSeconds);
}
</script>

<template>
  <section class="rounded-lg border border-slate-800 bg-slate-900/60 px-3 py-2" aria-label="Telemetry freshness">
    <div class="mb-1.5 flex items-center justify-between">
      <h2 class="text-xs font-semibold uppercase tracking-wide text-slate-400">Telemetry health</h2>
      <span class="text-[11px] text-slate-500">live · delayed · <span class="text-amber-300">stale</span> · <span class="text-red-300">missing</span></span>
    </div>
    <ul class="grid grid-cols-1 gap-1.5 min-[480px]:grid-cols-2 md:grid-cols-3 lg:grid-cols-6">
      <li
        v-for="s in SOURCES"
        :key="s.key"
        :data-testid="`freshness-${s.key}`"
        :class="[
          'flex min-w-0 flex-col gap-0.5 rounded-md px-2 py-1.5 ring-1',
          freshness?.[s.key]?.status === 'missing'
            ? 'bg-red-500/10 ring-red-500/40'
            : freshness?.[s.key]?.status === 'stale'
              ? 'bg-amber-500/10 ring-amber-500/40'
              : 'bg-slate-950/50 ring-slate-800',
        ]"
      >
        <div class="flex items-center justify-between gap-2">
          <span class="truncate text-xs font-medium text-slate-200" :title="s.origin">{{ s.label }}</span>
          <FreshnessBadge :freshness="freshness?.[s.key]" />
        </div>
        <div class="truncate text-[11px] text-slate-400 tabular-nums">
          last {{ fmtTimeOfDay(freshness?.[s.key]?.lastAt) }} · age {{ age(freshness?.[s.key]) }}
        </div>
        <div v-if="freshness?.[s.key]?.note" class="truncate text-[11px] text-slate-500" :title="freshness?.[s.key]?.note">{{ freshness?.[s.key]?.note }}</div>
      </li>
    </ul>
  </section>
</template>
