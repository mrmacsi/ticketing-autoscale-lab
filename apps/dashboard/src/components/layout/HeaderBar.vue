<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';
import StatePill from './StatePill.vue';
import ConnectionStatus from './ConnectionStatus.vue';
import CostIndicator from './CostIndicator.vue';
import { isActive } from '@/lib/experimentState';
import { useLab } from '@/store/lab';
import type { Me } from '@/types/contracts';

const props = defineProps<{ me: Me | null }>();
const emit = defineEmits<{ signOut: [] }>();
const store = useLab();
const exp = computed(() => store.experiment.value);
const liveInstances = computed(() => store.snapshot.value?.mig?.actualCount ?? null);
const canSignOut = computed(() => props.me?.authMode === 'google_signin');
</script>

<template>
  <header class="no-print z-30 sm:sticky sm:top-0 border-b border-slate-800 bg-slate-950/90 backdrop-blur">
    <div class="mx-auto flex max-w-[1800px] flex-wrap items-center gap-x-4 gap-y-1.5 px-4 py-2">
      <RouterLink to="/" class="flex items-center gap-2 text-sm font-semibold text-slate-50">
        <svg viewBox="0 0 16 16" class="h-5 w-5" aria-hidden="true"><rect width="16" height="16" rx="3" fill="#1e293b" /><path d="M3 12V8m3.3 4V5.5m3.4 6.5V7m3.3 5V3.5" stroke="#34d399" stroke-width="1.8" stroke-linecap="round" /></svg>
        Ticketing Autoscale Lab
      </RouterLink>
      <nav class="flex items-center gap-3 text-xs">
        <RouterLink to="/" class="text-slate-400 hover:text-slate-100" active-class="!text-sky-300" exact-active-class="!text-sky-300">Live</RouterLink>
        <RouterLink to="/history" class="text-slate-400 hover:text-slate-100" active-class="!text-sky-300">History</RouterLink>
      </nav>
      <div class="flex items-center gap-2">
        <StatePill :state="exp?.state ?? null" />
        <ConnectionStatus :stream="store.stream" :now-ms="store.clock.nowMs" @retry="store.retryStream()" />
      </div>
      <CostIndicator :model="store.meta.value?.costModel ?? null" :live-instances="liveInstances" :experiment-active="isActive(exp?.state)" />
      <div class="ml-auto flex min-w-0 items-center gap-2 text-[11px] text-slate-400">
        <span class="truncate" data-testid="me-email">{{ me?.email ?? 'N/A' }}</span>
        <span class="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-[10px] text-slate-300" data-testid="auth-mode">{{ me?.authMode ?? '?' }}</span>
        <button v-if="canSignOut" type="button" class="text-sky-300 hover:underline" @click="emit('signOut')">sign out</button>
      </div>
    </div>
  </header>
</template>
