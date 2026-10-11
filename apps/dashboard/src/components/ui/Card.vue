<script setup lang="ts">
defineProps<{ title?: string; subtitle?: string; tag?: string }>();
</script>

<template>
  <section class="min-w-0 rounded-lg border border-slate-800 bg-slate-900/60 shadow-sm">
    <header v-if="title || $slots.actions || $slots.header" class="flex flex-wrap items-start justify-between gap-2 border-b border-slate-800 px-3 py-2 sm:px-4">
      <div class="min-w-0">
        <h2 class="flex items-center gap-2 text-sm font-semibold text-slate-100">
          <span v-if="tag" class="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-[11px] text-slate-400">{{ tag }}</span>
          {{ title }}
        </h2>
        <p v-if="subtitle" class="mt-0.5 text-xs text-slate-400">{{ subtitle }}</p>
        <slot name="header" />
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
        <slot name="actions" />
      </div>
    </header>
    <div class="p-3 sm:p-4">
      <slot />
    </div>
  </section>
</template>
