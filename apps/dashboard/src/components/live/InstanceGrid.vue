<script setup lang="ts">
import { computed } from 'vue';
import InstanceCard from './InstanceCard.vue';
import type { Freshness, InstanceView } from '@/types/contracts';

const props = defineProps<{
  instances: readonly InstanceView[];
  trafficFreshness: Freshness | null;
  cpuFreshness: Freshness | null;
  nowMs: number;
}>();

const sorted = computed(() => [...props.instances].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? '') || a.name.localeCompare(b.name)));
const totalRps = computed(() => props.instances.reduce((s, i) => s + (Number.isFinite(i.traffic.rps) ? i.traffic.rps : 0), 0));
</script>

<template>
  <div>
    <p v-if="instances.length === 0" class="rounded-md border border-dashed border-slate-700 p-4 text-center text-sm text-slate-400" data-testid="no-instances">
      No instances reported by the managed instance group.
    </p>
    <TransitionGroup tag="div" name="instance" class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      <InstanceCard
        v-for="inst in sorted"
        :key="inst.name"
        :instance="inst"
        :total-rps="totalRps"
        :traffic-freshness="trafficFreshness"
        :cpu-freshness="cpuFreshness"
        :now-ms="nowMs"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.instance-enter-active,
.instance-leave-active {
  transition: all 0.45s ease;
}
.instance-enter-from {
  opacity: 0;
  transform: translateY(8px) scale(0.98);
}
.instance-leave-to {
  opacity: 0;
  transform: scale(0.95);
}
.instance-move {
  transition: transform 0.45s ease;
}
</style>
