<script setup lang="ts">
import { computed } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import FreshnessBadge from '@/components/ui/FreshnessBadge.vue';
import Stat from '@/components/ui/Stat.vue';
import { shortInstance } from '@/charts/options';
import { ageSeconds, fmtAge, fmtFractionPct, fmtMs, fmtNumber, fmtRate, NA, parseTime } from '@/lib/format';
import type { Freshness, WaitingRoomDecision, WaitingRoomServer, WaitingRoomServerPhase, WaitingRoomSettings, WaitingRoomView } from '@/types/contracts';

const props = defineProps<{
  /** Latest queue-server observation; null when it has never been observed. */
  room: WaitingRoomView | null;
  freshness: Freshness | null;
  /** Event of the current experiment, to flag a room serving something else. */
  eventId: string | null;
  /** Waiting-room settings of the current experiment, if any. */
  configured: WaitingRoomSettings | null;
  /** Server-corrected clock for signal ages; falls back to the observation time of the stats. */
  nowMs?: number | null;
}>();

const r = computed(() => props.room);
const stale = computed(() => !r.value || props.freshness?.status === 'stale' || props.freshness?.status === 'missing');
const otherEvent = computed(() => !!r.value && r.value.enabled && !!props.eventId && !!r.value.eventId && r.value.eventId !== props.eventId);
const activeOfCapacity = computed(() => (r.value ? `${fmtNumber(r.value.active)} / ${fmtNumber(r.value.capacity)}` : 'N/A'));
const adaptive = computed(() => r.value?.admissionMode === 'adaptive');
const perServer = computed(() => {
  if (!r.value) return undefined;
  return adaptive.value ? 'sum of learned server limits' : `${fmtNumber(r.value.activeUsersPerServer)} per healthy server`;
});
const configuredText = computed(() => {
  const c = props.configured;
  if (!c) return '';
  if (!c.enabled) return 'off';
  const admission = c.mode === 'adaptive'
    ? `adaptive, start ${c.activeUsersPerServer} per server, max ${c.maxUsersPerServer ?? 200}`
    : `${c.activeUsersPerServer} per server`;
  return `on, ${admission}, TTL ${c.admissionTtlSeconds}s`;
});

const PHASE_LABEL: Record<WaitingRoomServerPhase, string> = { slow_start: 'slow start', steady: 'steady', fixed: 'fixed' };
const DECISION_CLASS: Record<WaitingRoomDecision, string> = {
  increase: 'text-emerald-300',
  decrease: 'text-red-300',
  hold: 'text-slate-300',
};

const reference = computed(() => props.nowMs ?? parseTime(r.value?.observedAt) ?? null);

function workers(s: WaitingRoomServer): string {
  const g = s.signals;
  if (!g) return NA;
  const busy = g.busy ?? (g.activeWorkers != null && g.totalWorkers ? g.activeWorkers / g.totalWorkers : null);
  const counts = g.activeWorkers != null && g.totalWorkers != null ? `${fmtNumber(g.activeWorkers)}/${fmtNumber(g.totalWorkers)}` : null;
  if (counts === null && busy === null) return NA;
  return counts === null ? fmtFractionPct(busy, 0) : `${counts} (${fmtFractionPct(busy, 0)})`;
}

const rows = computed(() =>
  (r.value?.servers ?? []).map((s) => {
    const age = reference.value === null ? null : ageSeconds(s.signals?.observedAt, reference.value);
    return {
      name: s.name,
      short: shortInstance(s.name),
      limit: fmtNumber(s.limit),
      phase: s.phase ? PHASE_LABEL[s.phase] : NA,
      workers: workers(s),
      busyHigh: (s.signals?.busy ?? 0) >= 0.9,
      listenQueue: fmtNumber(s.signals?.listenQueue),
      queued: (s.signals?.listenQueue ?? 0) > 0,
      cpu: fmtFractionPct(s.signals?.cpu, 0),
      decision: s.decision ?? null,
      decisionClass: s.decision ? DECISION_CLASS[s.decision] : 'text-slate-400',
      reason: s.reason ?? null,
      decreases: s.decreases ?? null,
      age: s.signals ? fmtAge(age) : NA,
      ageStale: age !== null && age > 3,
    };
  }),
);
</script>

<template>
  <div class="space-y-2" data-testid="waiting-room-panel">
    <div class="flex flex-wrap items-center gap-2">
      <FreshnessBadge :freshness="freshness" label="Queue" />
      <template v-if="r">
        <Badge v-if="r.enabled" tone="emerald" dot data-testid="wr-state">Waiting room on</Badge>
        <Badge v-else tone="slate" dot data-testid="wr-state">Waiting room off</Badge>
        <Badge
          v-if="adaptive"
          tone="violet"
          data-testid="wr-mode"
          title="Each server's limit is learned every second from PHP workers busy, the listen queue and VM CPU (slow start, then +1 / ×0.75)"
          >Adaptive admission</Badge
        >
        <Badge v-else tone="sky" data-testid="wr-mode" title="Capacity = healthy servers × active users per server">Fixed admission</Badge>
        <Badge v-if="r.soldOut" tone="red" dot data-testid="wr-sold-out">Sold out — waiting customers told</Badge>
        <Badge v-else-if="r.inventory?.soldOut" tone="amber" dot data-testid="wr-paused" title="Availability is 0; admissions pause until holds resolve or the grace period ends">No tickets available — admissions paused</Badge>
        <span class="truncate text-[11px] text-slate-500">capacity from {{ r.capacitySource === 'gce' ? 'load-balancer backend health' : r.capacitySource }}</span>
      </template>
      <Badge v-else tone="red" dot>Waiting room telemetry unavailable</Badge>
      <span v-if="configured" class="truncate text-[11px] text-slate-500" data-testid="wr-configured">
        experiment: {{ configuredText }}
      </span>
    </div>
    <p v-if="!r && freshness?.note" class="text-xs text-slate-400" data-testid="wr-note">{{ freshness.note }}</p>
    <p v-if="r?.capacityError" class="break-words text-xs text-amber-200/90" data-testid="wr-capacity-error">Capacity source error: {{ r.capacityError }}</p>
    <p v-if="otherEvent" class="text-xs text-amber-200/90" data-testid="wr-other-event">The queue is configured for a different event than this experiment's.</p>
    <div :class="['grid grid-cols-2 gap-2 min-[480px]:grid-cols-4 xl:grid-cols-8', stale ? 'opacity-70' : '']">
      <Stat label="Waiting" :value="fmtNumber(r?.waiting)" emphasis testid="wr-waiting" :hint="r ? `oldest ${fmtAge(r.oldestWaitingSeconds)}` : undefined" />
      <Stat label="Active / capacity" :value="activeOfCapacity" emphasis testid="wr-active" :hint="perServer" />
      <Stat label="Healthy servers" :value="fmtNumber(r?.healthyServers)" testid="wr-healthy" hint="LB backend HEALTHY" />
      <Stat label="Admission rate" :value="r ? `${fmtRate(r.admissionRatePerSecond)}/s` : 'N/A'" testid="wr-rate" hint="last 10 s" />
      <Stat label="Wait p50" :value="fmtMs(r?.waitMs.p50)" testid="wr-wait-p50" hint="join → admitted" />
      <Stat label="Wait p95" :value="fmtMs(r?.waitMs.p95)" testid="wr-wait-p95" :hint="r ? `max ${fmtMs(r.waitMs.max)}` : undefined" />
      <Stat label="Joined / admitted" :value="r ? `${fmtNumber(r.joinedTotal)} / ${fmtNumber(r.admittedTotal)}` : 'N/A'" testid="wr-totals" :hint="r ? `${fmtNumber(r.releasedTotal)} released · ${fmtNumber(r.expiredTotal)} expired` : undefined" />
      <Stat label="Told sold out" :value="fmtNumber(r?.soldOutTotal)" testid="wr-sold-out-total" hint="never reached a server" />
      <Stat label="Queue store" :value="r?.store ? (r.store === 'redis' ? 'Redis (shared)' : 'memory (1 server)') : 'N/A'" testid="wr-store" :hint="r?.leaderInstance ? `leader ${r.leaderInstance}` : undefined" />
      <Stat label="Left the line" :value="r?.abandonedTotal == null ? 'N/A' : fmtNumber(r.abandonedTotal)" testid="wr-abandoned" hint="stopped checking in for 45 s" />
    </div>
    <div v-if="r && adaptive" :class="['space-y-1', stale ? 'opacity-70' : '']" data-testid="wr-servers">
      <h4 class="text-[11px] font-semibold uppercase tracking-wide text-slate-400">Learned limit per server (shared, set by the busiest server)</h4>
      <p v-if="rows.length === 0" class="text-xs text-slate-400" data-testid="wr-servers-empty">No healthy servers reported by the queue.</p>
      <div v-else class="overflow-x-auto rounded-md ring-1 ring-slate-800">
        <table class="w-full min-w-[720px] text-left text-xs" data-testid="wr-servers-table">
          <thead class="bg-slate-950/60 text-[11px] uppercase text-slate-400">
            <tr class="border-b border-slate-800">
              <th class="px-2 py-1 font-medium">Server</th>
              <th class="px-2 py-1 text-right font-medium">Limit</th>
              <th class="px-2 py-1 font-medium">Phase</th>
              <th class="px-2 py-1 text-right font-medium" title="Active / total PHP-FPM workers">Workers busy</th>
              <th class="px-2 py-1 text-right font-medium" title="Requests waiting for a free PHP-FPM worker">Listen queue</th>
              <th class="px-2 py-1 text-right font-medium" title="Sampled inside the VM every 2 s">VM CPU</th>
              <th class="px-2 py-1 font-medium">Last decision</th>
              <th class="px-2 py-1 text-right font-medium">Signal age</th>
            </tr>
          </thead>
          <tbody class="tabular-nums">
            <tr v-for="row in rows" :key="row.name" class="border-b border-slate-800/60 last:border-b-0" :data-testid="`wr-server-${row.name}`">
              <td class="max-w-[180px] truncate px-2 py-1 font-mono text-slate-300" :title="row.name">{{ row.short }}</td>
              <td class="px-2 py-1 text-right font-semibold text-slate-100" data-testid="wr-server-limit">{{ row.limit }}</td>
              <td class="px-2 py-1 text-slate-300" data-testid="wr-server-phase">{{ row.phase }}</td>
              <td :class="['px-2 py-1 text-right', row.busyHigh ? 'text-amber-300' : 'text-slate-200']" data-testid="wr-server-workers">{{ row.workers }}</td>
              <td :class="['px-2 py-1 text-right', row.queued ? 'text-amber-300' : 'text-slate-200']" data-testid="wr-server-queue">{{ row.listenQueue }}</td>
              <td class="px-2 py-1 text-right text-slate-200" data-testid="wr-server-cpu">{{ row.cpu }}</td>
              <td class="max-w-[220px] truncate px-2 py-1" :title="row.reason ?? undefined" data-testid="wr-server-decision" :data-decision="row.decision ?? 'unknown'">
                <span v-if="row.decision" :class="['font-medium', row.decisionClass]">{{ row.decision }}</span>
                <span v-else class="text-slate-400">N/A</span>
                <span v-if="row.reason" class="text-slate-400"> · {{ row.reason }}</span>
                <span v-if="row.decreases" class="text-slate-500"> ({{ row.decreases }} cut{{ row.decreases === 1 ? '' : 's' }})</span>
              </td>
              <td :class="['px-2 py-1 text-right', row.ageStale ? 'text-amber-300' : 'text-slate-300']" data-testid="wr-server-age">{{ row.age }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
