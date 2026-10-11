<script setup lang="ts">
import { computed } from 'vue';
import { useLab } from '@/store/lab';
import { useDraft } from '@/store/draft';

defineProps<{ disabled?: boolean }>();
const store = useLab();
const draft = useDraft();
const presets = computed(() => store.meta.value?.presets ?? []);
const active = computed(() => draft.config.value?.preset ?? null);
</script>

<template>
  <div>
    <div class="label">Presets</div>
    <p class="mb-2 text-xs leading-relaxed text-slate-400">Choose a ready-made scenario to fill the form. This replaces your current draft settings; review or edit them before pressing Prepare Experiment.</p>
    <p v-if="presets.length === 0" class="text-xs text-slate-500">No presets provided by the control API.</p>
    <div class="grid grid-cols-1 gap-2 @md:grid-cols-2">
      <button
        v-for="p in presets"
        :key="p.id"
        type="button"
        :disabled="disabled"
        :aria-pressed="active === p.id"
        :data-testid="`preset-${p.id}`"
        :class="[
          'min-w-0 rounded-md border px-2.5 py-2 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50',
          active === p.id ? 'border-sky-500 bg-sky-500/10' : 'border-slate-700 bg-slate-950/40 hover:border-slate-500',
        ]"
        @click="draft.applyPreset(p)"
      >
        <div class="flex items-center gap-2">
          <span class="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-slate-800 font-mono text-xs font-bold text-sky-300">{{ p.id }}</span>
          <span class="truncate text-sm font-medium text-slate-100">{{ p.title }}</span>
          <span
            v-if="p.config.waitingRoom?.enabled"
            class="ml-auto shrink-0 rounded bg-sky-500/15 px-1.5 py-0.5 text-[10px] font-medium text-sky-300 ring-1 ring-inset ring-sky-500/40"
            :data-testid="`preset-${p.id}-waiting-room`"
            >waiting room</span
          >
        </div>
        <p class="mt-1 text-xs text-slate-400">{{ p.description }}</p>
        <p class="mt-0.5 text-[11px] text-emerald-300/90"><span class="text-slate-500">Demonstrates:</span> {{ p.demonstrates }}</p>
      </button>
    </div>
  </div>
</template>
