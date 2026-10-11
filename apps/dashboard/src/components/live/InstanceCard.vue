<script setup lang="ts">
import { computed } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import { ageSeconds, fmtAge, fmtFractionPct, fmtMs, fmtRate, fmtTimeOfDay, NA } from '@/lib/format';
import { healthTone, phaseTone } from '@/lib/tones';
import type { Freshness, InstanceView } from '@/types/contracts';

const props = defineProps<{
  instance: InstanceView;
  /** Sum of rps over all instances; used for the request share bar. */
  totalRps: number | null;
  trafficFreshness: Freshness | null;
  cpuFreshness: Freshness | null;
  nowMs: number;
}>();

const i = computed(() => props.instance);
const trafficUsable = computed(() => props.trafficFreshness?.status === 'live' || props.trafficFreshness?.status === 'delayed');
const cpuAge = computed(() => ageSeconds(i.value.cpu.sampleTime, props.nowMs));
// Judged per VM: one VM's own sample age, or a missing sample on a VM old enough to have one
// (new VMs get their first Cloud Monitoring point after 1–4 minutes, which the card already says).
const vmAge = computed(() => ageSeconds(i.value.createdAt, props.nowMs));
const cpuStatus = computed<'stale' | 'missing' | null>(() => {
  if (i.value.cpu.sampleTime) return cpuAge.value != null && cpuAge.value > 300 ? 'stale' : null;
  if (props.cpuFreshness?.status === 'missing' && (vmAge.value == null || vmAge.value > 300)) return 'missing';
  return vmAge.value != null && vmAge.value > 300 ? 'missing' : null;
});
const cpuStale = computed(() => cpuStatus.value !== null);
const guestAge = computed(() => ageSeconds(i.value.guestCpu?.sampleTime, props.nowMs));

const rps = computed(() => (trafficUsable.value ? fmtRate(i.value.traffic.rps) : NA));
const latency = computed(() => (trafficUsable.value ? fmtMs(i.value.traffic.avgLatencyMs) : NA));
const errorRate = computed(() => (trafficUsable.value ? fmtFractionPct(i.value.traffic.errorRate, 2) : NA));
const share = computed<number | null>(() => {
  if (!trafficUsable.value || props.totalRps == null || props.totalRps <= 0) return null;
  return i.value.traffic.rps / props.totalRps;
});
const cpuBar = computed(() => (i.value.cpu.value == null ? 0 : Math.min(100, Math.max(0, i.value.cpu.value * 100))));
</script>

<template>
  <article
    class="flex min-w-0 flex-col gap-2 rounded-lg border border-slate-800 bg-slate-950/60 p-3"
    :data-testid="`instance-card`"
    :data-instance="i.name"
  >
    <header class="flex items-start justify-between gap-2">
      <div class="min-w-0">
        <h3 class="truncate font-mono text-xs font-semibold text-slate-100" :title="i.name">{{ i.name }}</h3>
        <p class="truncate text-[11px] text-slate-500">
          {{ i.zone }} · {{ i.privateIp ?? 'IP N/A' }} · v{{ i.appVersion ?? 'N/A' }}
        </p>
      </div>
      <Badge :tone="phaseTone(i.phase)" dot data-testid="phase">{{ i.phase }}</Badge>
    </header>

    <div class="flex flex-wrap gap-1">
      <Badge :tone="healthTone(i.vmStatus)" title="Compute Engine instance status">VM {{ i.vmStatus }}</Badge>
      <Badge :tone="healthTone(i.currentAction)" title="MIG current action">MIG {{ i.currentAction }}</Badge>
      <Badge :tone="healthTone(i.migHealth)" title="MIG health check">HC {{ i.migHealth ?? NA }}</Badge>
      <Badge :tone="healthTone(i.lbHealth)" title="Load balancer backend health">LB {{ i.lbHealth ?? NA }}</Badge>
      <Badge v-if="i.ready !== null" :tone="i.ready ? 'emerald' : 'amber'" title="/instance-info ready flag">{{ i.ready ? 'app ready' : 'app not ready' }}</Badge>
    </div>

    <div class="rounded-md bg-slate-900/70 p-2 ring-1 ring-slate-800">
      <div class="flex items-baseline justify-between gap-2">
        <span class="text-[11px] uppercase tracking-wide text-slate-400">CPU · Cloud Monitoring</span>
        <span class="text-lg font-semibold tabular-nums text-slate-50" data-testid="cpu-value">{{ fmtFractionPct(i.cpu.value) }}</span>
      </div>
      <div class="mt-1 h-1.5 overflow-hidden rounded bg-slate-800" aria-hidden="true">
        <div class="h-full rounded bg-sky-400 transition-[width] duration-500" :style="{ width: `${cpuBar}%` }" />
      </div>
      <p class="mt-1 text-[11px] text-slate-400" data-testid="cpu-sample">
        <template v-if="i.cpu.sampleTime">
          sample {{ fmtTimeOfDay(i.cpu.sampleTime) }} UTC · sampled {{ fmtAge(cpuAge) }} ago — Cloud Monitoring delay
        </template>
        <template v-else>no Cloud Monitoring sample yet (new instances take 1–4 min)</template>
      </p>
      <Badge v-if="cpuStale" :tone="cpuStatus === 'missing' ? 'red' : 'amber'" dot data-testid="cpu-stale">CPU telemetry {{ cpuStatus }}</Badge>
    </div>

    <div class="text-[11px] text-slate-400" data-testid="guest-cpu">
      Guest CPU <span class="font-medium tabular-nums text-slate-200">{{ fmtFractionPct(i.guestCpu?.value) }}</span>
      <span class="text-slate-500"> — guest /proc/stat, real-time, not used by autoscaler</span>
      <span v-if="guestAge != null" class="text-slate-500"> · {{ fmtAge(guestAge) }} ago</span>
    </div>

    <dl class="grid grid-cols-3 gap-2 text-center">
      <div class="rounded bg-slate-900/70 px-1 py-1 ring-1 ring-slate-800">
        <dt class="text-[10px] uppercase text-slate-500">req/s</dt>
        <dd class="text-sm font-semibold tabular-nums text-slate-100" data-testid="rps">{{ rps }}</dd>
      </div>
      <div class="rounded bg-slate-900/70 px-1 py-1 ring-1 ring-slate-800">
        <dt class="text-[10px] uppercase text-slate-500">avg latency</dt>
        <dd class="text-sm font-semibold tabular-nums text-slate-100" data-testid="latency">{{ latency }}</dd>
      </div>
      <div class="rounded bg-slate-900/70 px-1 py-1 ring-1 ring-slate-800">
        <dt class="text-[10px] uppercase text-slate-500">errors</dt>
        <dd class="text-sm font-semibold tabular-nums text-slate-100" data-testid="error-rate">{{ errorRate }}</dd>
      </div>
    </dl>

    <div>
      <div class="flex justify-between text-[11px] text-slate-400">
        <span>Share of requests</span>
        <span class="tabular-nums" data-testid="share">{{ share == null ? '—' : fmtFractionPct(share, 0) }}</span>
      </div>
      <div class="mt-1 h-1.5 overflow-hidden rounded bg-slate-800" aria-hidden="true">
        <div class="h-full rounded bg-violet-400 transition-[width] duration-500" :style="{ width: `${(share ?? 0) * 100}%` }" />
      </div>
      <p v-if="!trafficUsable" class="mt-1 text-[11px] text-amber-300" data-testid="traffic-stale">
        Traffic telemetry {{ trafficFreshness?.status ?? 'missing' }} — per-instance load not shown
      </p>
    </div>
  </article>
</template>
