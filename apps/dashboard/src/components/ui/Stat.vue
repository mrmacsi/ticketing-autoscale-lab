<script setup lang="ts">
defineProps<{ label: string; value: string; hint?: string; emphasis?: boolean; testid?: string }>();
</script>

<template>
  <div class="min-w-0 rounded-md bg-slate-950/60 px-2.5 py-2 ring-1 ring-slate-800">
    <div class="text-[11px] uppercase leading-tight tracking-wide text-slate-400">{{ label }}</div>
    <div :class="['truncate font-semibold tabular-nums', emphasis ? 'text-xl text-slate-50' : 'text-base text-slate-100']" :data-testid="testid">
      {{ value }}
    </div>
    <div v-if="hint || $slots.default" class="truncate text-[11px] text-slate-500"><slot>{{ hint }}</slot></div>
  </div>
</template>
