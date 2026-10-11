<script setup lang="ts">
import Badge from '@/components/ui/Badge.vue';
import type { Tone } from '@/lib/tones';
import type { ValueItem } from './types';

defineProps<{ kind: 'measured' | 'configured' | 'estimated'; title: string; items: ValueItem[] }>();

const KIND: Record<'measured' | 'configured' | 'estimated', { label: string; tone: Tone }> = {
  measured: { label: 'Measured', tone: 'emerald' },
  configured: { label: 'Configured target', tone: 'sky' },
  estimated: { label: 'Estimate', tone: 'amber' },
};
</script>

<template>
  <section class="rounded-lg border border-slate-800 bg-slate-900/60 p-3" :data-testid="`group-${kind}`">
    <h2 class="mb-2 flex items-center gap-2 text-sm font-semibold text-slate-100">
      <Badge :tone="KIND[kind].tone">{{ KIND[kind].label }}</Badge>
      {{ title }}
    </h2>
    <dl class="grid grid-cols-2 gap-2 min-[480px]:grid-cols-3 lg:grid-cols-4">
      <div v-for="it in items" :key="it.label" class="min-w-0 rounded-md bg-slate-950/60 px-2.5 py-1.5 ring-1 ring-slate-800">
        <dt class="truncate text-[11px] text-slate-400">{{ it.label }}</dt>
        <dd :class="['truncate font-semibold tabular-nums', it.tone === 'bad' ? 'text-red-300' : it.tone === 'good' ? 'text-emerald-300' : 'text-slate-100']">{{ it.value }}</dd>
        <dd v-if="it.hint" class="truncate text-[11px] text-slate-500">{{ it.hint }}</dd>
      </div>
    </dl>
  </section>
</template>
