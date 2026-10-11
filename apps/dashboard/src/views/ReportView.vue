<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { RouterLink } from 'vue-router';
import StatePill from '@/components/layout/StatePill.vue';
import ValueGroup from '@/components/report/ValueGroup.vue';
import type { ValueItem } from '@/components/report/types';
import ChartGrid from '@/components/charts/ChartGrid.vue';
import { shortInstance } from '@/charts/options';
import Badge from '@/components/ui/Badge.vue';
import { fmtClock, fmtDateTime, fmtFractionPct, fmtMs, fmtNumber, fmtPct, fmtRate, fmtTimeOfDay, fmtUsd, parseTime } from '@/lib/format';
import { attributionTone } from '@/lib/tones';
import { useLab } from '@/store/lab';
import type { ExportFormat } from '@/api/client';
import type { ExperimentState, Report, Series } from '@/types/contracts';

const props = defineProps<{ id: string }>();
const store = useLab();
const report = ref<Report | null>(null);
const series = ref<Series | null>(null);
const error = ref<string | null>(null);
const seriesError = ref<string | null>(null);
const exportError = ref<string | null>(null);

async function load(id: string) {
  report.value = null;
  series.value = null;
  error.value = null;
  seriesError.value = null;
  const [r, s] = await Promise.allSettled([store.api.getReport(id), store.api.getSeries(id)]);
  if (r.status === 'fulfilled') report.value = r.value;
  else error.value = r.reason instanceof Error ? r.reason.message : String(r.reason);
  if (s.status === 'fulfilled') series.value = s.value;
  else seriesError.value = s.reason instanceof Error ? s.reason.message : String(s.reason);
}
watch(() => props.id, load, { immediate: true });

const m = computed(() => report.value?.measured ?? null);
const violations = computed(() => m.value?.invariantViolations ?? null);

const measured = computed<ValueItem[]>(() => {
  const x = m.value;
  if (!x) return [];
  return [
    { label: 'Arrivals requested', value: fmtNumber(x.arrivalsRequested) },
    { label: 'Arrivals scheduled', value: fmtNumber(x.arrivalsScheduled) },
    { label: 'Arrivals started', value: fmtNumber(x.arrivalsStarted) },
    { label: 'Arrivals dropped', value: fmtNumber(x.arrivalsDropped), hint: 'generator saturation', tone: x.arrivalsDropped > 0 ? 'bad' : null },
    { label: 'Achieved vs requested', value: fmtPct(x.achievedVsRequestedPct) },
    { label: 'HTTP requests', value: fmtNumber(x.httpRequests) },
    { label: 'Peak RPS', value: fmtRate(x.peakRps) },
    { label: 'Latency p50', value: fmtMs(x.latencyMs.p50) },
    { label: 'Latency p95', value: fmtMs(x.latencyMs.p95) },
    { label: 'Latency p99', value: fmtMs(x.latencyMs.p99) },
    { label: 'Latency max', value: fmtMs(x.latencyMs.max) },
    { label: 'HTTP error rate', value: fmtPct(x.httpErrorPct, 2), hint: '5xx / network / timeouts' },
    { label: 'Business rejections', value: fmtPct(x.businessRejectionPct, 2), hint: '409/410/402 — not errors' },
    { label: 'Peak instance CPU', value: fmtFractionPct(x.peakCpu?.value), hint: x.peakCpu ? `${x.peakCpu.instance} @ ${fmtTimeOfDay(x.peakCpu.sampleTime)}` : undefined },
    { label: 'Peak average CPU', value: fmtFractionPct(x.peakAverageCpu?.value), hint: x.peakAverageCpu ? `@ ${fmtTimeOfDay(x.peakAverageCpu.sampleTime)} UTC` : undefined },
    { label: 'Initial servers', value: fmtNumber(x.initialServerCount) },
    { label: 'Max observed servers', value: fmtNumber(x.maxObservedServerCount) },
    { label: 'Final servers', value: fmtNumber(x.finalServerCount) },
    { label: 'Tickets sold', value: fmtNumber(x.ticketsSold) },
    { label: 'Tickets remaining', value: fmtNumber(x.ticketsRemaining) },
    { label: 'Expired reservations', value: fmtNumber(x.expiredReservations) },
    { label: 'Reservations attempted', value: fmtNumber(x.reservations.attempted) },
    { label: 'Reservations succeeded', value: fmtNumber(x.reservations.succeeded) },
    { label: 'Reservations rejected', value: fmtNumber(x.reservations.rejected) },
    { label: 'Purchases completed', value: fmtNumber(x.purchases.completed) },
    { label: 'Payments declined', value: fmtNumber(x.purchases.declined) },
    { label: 'Purchases failed', value: fmtNumber(x.purchases.failed) },
    { label: 'Expired during checkout', value: fmtNumber(x.purchases.expiredDuringCheckout) },
  ];
});

const room = computed(() => m.value?.waitingRoom ?? null);
const roomConfig = computed(() => report.value?.config.waitingRoom ?? null);
const waitingRoom = computed<ValueItem[]>(() => {
  const w = room.value;
  if (!w) return [];
  return [
    { label: 'Joined the queue', value: fmtNumber(w.joined) },
    { label: 'Admitted to checkout', value: fmtNumber(w.admitted) },
    { label: 'Told sold out in queue', value: fmtNumber(w.soldOutInQueue), hint: 'never reached a server' },
    { label: 'Peak waiting', value: fmtNumber(w.peakWaiting) },
    { label: 'Wait p50', value: fmtMs(w.waitMs.p50), hint: 'join → admitted' },
    { label: 'Wait p95', value: fmtMs(w.waitMs.p95) },
    { label: 'Wait max', value: fmtMs(w.waitMs.max) },
    {
      label: 'Peak capacity',
      value: fmtNumber(w.peakCapacity),
      hint: w.adaptive ? 'sum of learned server limits' : roomConfig.value ? `${roomConfig.value.activeUsersPerServer} per healthy server` : undefined,
    },
    { label: 'Peak healthy servers', value: fmtNumber(w.peakHealthyServers), hint: 'load-balancer view' },
    ...adaptiveItems.value,
  ];
});

const adaptiveItems = computed<ValueItem[]>(() => {
  const a = room.value?.adaptive;
  if (!a) return [];
  const c = roomConfig.value;
  return [
    { label: 'Admission mode', value: 'adaptive', hint: c ? `start ${c.activeUsersPerServer}, max ${c.maxUsersPerServer ?? 200} per server` : undefined },
    { label: 'Avg limit at peak capacity', value: fmtNumber(a.limitAvgAtPeakCapacity, 1), hint: 'learned per-server limit' },
    { label: 'Limit cuts', value: fmtNumber(a.decreasesTotal), hint: 'overload decreases, all servers' },
    ...a.perServer.map((s) => ({
      label: `Limit ${shortInstance(s.name)}`,
      value: `${fmtNumber(s.finalLimit)} (peak ${fmtNumber(s.peakLimit)})`,
      hint: `final · ${s.decreases} cut${s.decreases === 1 ? '' : 's'}`,
    })),
  ];
});

const configured = computed<ValueItem[]>(() => {
  const c = report.value?.configured;
  if (!c) return [];
  return [
    { label: 'CPU target', value: fmtFractionPct(c.cpuTarget, 0) },
    { label: 'Min replicas', value: fmtNumber(c.minReplicas) },
    { label: 'Max replicas', value: fmtNumber(c.maxReplicas) },
    { label: 'Initial servers', value: fmtNumber(c.initialServers) },
    { label: 'Traffic pattern', value: c.pattern },
    { label: 'Duration', value: fmtClock(c.durationSeconds) },
  ];
});

const estimated = computed<ValueItem[]>(() => {
  const e = report.value?.estimated;
  return e ? [{ label: 'Infrastructure cost', value: fmtUsd(e.infrastructureCostUsd, 3), hint: e.note }] : [];
});

const perInstance = computed(() => {
  const r = m.value?.requestsPerInstance ?? {};
  const total = Object.values(r).reduce((a, b) => a + b, 0);
  return Object.entries(r)
    .sort((a, b) => b[1] - a[1])
    .map(([name, n]) => ({ name, n, share: total > 0 ? n / total : null }));
});

const refT = computed(() => parseTime(report.value?.timestamps.trafficStartedAt) ?? parseTime(report.value?.timestamps.createdAt));
const rel = (iso: string) => {
  const t = parseTime(iso);
  return t == null || refT.value == null ? '—' : fmtClock((t - refT.value) / 1000);
};

async function download(format: ExportFormat) {
  exportError.value = null;
  try {
    const { blob, filename } = await store.api.downloadExport(props.id, format);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (err) {
    exportError.value = err instanceof Error ? err.message : String(err);
  }
}

const print = () => window.print();
</script>

<template>
  <main class="mx-auto max-w-[1400px] space-y-3 px-3 py-3 sm:px-4">
    <div class="no-print flex flex-wrap items-center gap-2 text-sm">
      <RouterLink to="/history" class="text-sky-300 hover:underline">← History</RouterLink>
      <span class="ml-auto" />
      <button type="button" class="btn btn-default text-xs" data-testid="report-export-json" @click="download('json')">Export JSON</button>
      <button type="button" class="btn btn-default text-xs" data-testid="report-export-csv" @click="download('csv')">Export CSV</button>
      <button type="button" class="btn btn-ghost text-xs" @click="print">Print</button>
    </div>
    <p v-if="exportError" class="text-xs text-red-300" role="alert">Export failed: {{ exportError }}</p>
    <p v-if="error" class="rounded-md border border-red-500/40 bg-red-500/10 p-3 text-sm text-red-200" role="alert">Could not load report: {{ error }}</p>
    <p v-else-if="!report" class="text-sm text-slate-400">Loading report…</p>

    <template v-if="report">
      <header class="rounded-lg border border-slate-800 bg-slate-900/60 p-3">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="text-lg font-semibold text-slate-50">{{ report.name }}</h1>
          <StatePill :state="report.state as ExperimentState" />
          <span class="font-mono text-xs text-slate-500">{{ report.experimentId }}</span>
        </div>
        <dl class="mt-2 grid grid-cols-1 gap-x-4 gap-y-0.5 text-xs text-slate-400 min-[480px]:grid-cols-2 lg:grid-cols-3">
          <div>Created: <span class="text-slate-200">{{ fmtDateTime(report.timestamps.createdAt) }}</span></div>
          <div>Ready: <span class="text-slate-200">{{ fmtDateTime(report.timestamps.readyAt, '—') }}</span></div>
          <div>Traffic started: <span class="text-slate-200">{{ fmtDateTime(report.timestamps.trafficStartedAt, '—') }}</span></div>
          <div>Traffic stopped: <span class="text-slate-200">{{ fmtDateTime(report.timestamps.trafficStoppedAt, '—') }}</span></div>
          <div>Completed: <span class="text-slate-200">{{ fmtDateTime(report.timestamps.completedAt, '—') }}</span></div>
          <div>Report generated: <span class="text-slate-200">{{ fmtDateTime(report.generatedAt) }}</span></div>
        </dl>
      </header>

      <div
        :class="[
          'rounded-lg border p-3 text-sm font-semibold',
          violations == null ? 'border-slate-700 bg-slate-800/50 text-slate-300' : violations === 0 ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200' : 'border-red-500/50 bg-red-500/15 text-red-200',
        ]"
        data-testid="report-violations"
        :data-violations="violations ?? 'unknown'"
      >
        Inventory invariant violations: {{ fmtNumber(violations) }}
        <span class="font-normal">{{ violations == null ? '— not measured' : violations === 0 ? '— no overselling or inconsistent holds detected' : '— inventory integrity broken, investigate' }}</span>
      </div>

      <ValueGroup kind="measured" title="Results" :items="measured" />
      <ValueGroup v-if="room" kind="measured" title="Waiting room" :items="waitingRoom" data-testid="report-waiting-room" />
      <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <ValueGroup kind="configured" title="Autoscaler & traffic plan" :items="configured" />
        <ValueGroup kind="estimated" title="Cost" :items="estimated" />
      </div>

      <section v-if="perInstance.length" class="rounded-lg border border-slate-800 bg-slate-900/60 p-3">
        <h2 class="mb-2 flex items-center gap-2 text-sm font-semibold text-slate-100"><Badge tone="emerald">Measured</Badge> Requests per instance</h2>
        <ul class="space-y-1">
          <li v-for="p in perInstance" :key="p.name" class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 text-xs">
            <span class="truncate font-mono text-slate-300" :title="p.name">{{ p.name }}</span>
            <span class="tabular-nums text-slate-200">{{ fmtNumber(p.n) }} · {{ fmtFractionPct(p.share, 0) }}</span>
            <div class="col-span-2 h-1.5 overflow-hidden rounded bg-slate-800"><div class="h-full bg-violet-400" :style="{ width: `${(p.share ?? 0) * 100}%` }" /></div>
          </li>
        </ul>
      </section>

      <section class="rounded-lg border border-slate-800 bg-slate-900/60 p-3">
        <h2 class="mb-2 text-sm font-semibold text-slate-100">Scaling timeline</h2>
        <p v-if="report.scalingTimeline.length === 0" class="text-sm text-slate-400">No scaling events recorded.</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full min-w-[640px] text-left text-xs" data-testid="scaling-table">
            <thead class="text-[11px] uppercase text-slate-400">
              <tr class="border-b border-slate-800">
                <th class="px-2 py-1 font-medium">T+</th>
                <th class="px-2 py-1 font-medium">UTC</th>
                <th class="px-2 py-1 font-medium">Kind</th>
                <th class="px-2 py-1 font-medium">Attribution</th>
                <th class="px-2 py-1 font-medium">Event</th>
              </tr>
            </thead>
            <tbody class="tabular-nums">
              <tr v-for="e in report.scalingTimeline" :key="e.id" class="border-b border-slate-800/60">
                <td class="px-2 py-1 font-mono">{{ rel(e.t) }}</td>
                <td class="px-2 py-1">{{ fmtTimeOfDay(e.t) }}</td>
                <td class="px-2 py-1">{{ e.kind }}</td>
                <td class="px-2 py-1"><Badge v-if="e.attribution" :tone="attributionTone(e.attribution)">{{ e.attribution }}</Badge><span v-else>—</span></td>
                <td class="px-2 py-1 text-slate-200">{{ e.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <h2 class="mb-2 text-sm font-semibold text-slate-100">Recorded series</h2>
        <p v-if="seriesError" class="text-sm text-red-300">Could not load series: {{ seriesError }}</p>
        <ChartGrid
          v-else-if="series"
          :traffic="series.traffic"
          :scaling="series.scaling"
          :cpu="series.cpu"
          :inventory="series.inventory"
          :waiting-room="series.waitingRoom ?? []"
          :show-waiting-room="roomConfig?.enabled === true"
          :show-adaptive-limits="roomConfig?.enabled === true && roomConfig?.mode === 'adaptive'"
          :events="series.events"
          :cpu-target="report.configured.cpuTarget"
        />
      </section>

      <section v-for="block in [
        { title: 'Generator capacity issues', items: report.generatorCapacityIssues, tone: 'text-amber-200' },
        { title: 'Failures', items: report.failures, tone: 'text-red-200' },
        { title: 'Notes', items: report.notes, tone: 'text-slate-300' },
      ]" :key="block.title" class="rounded-lg border border-slate-800 bg-slate-900/60 p-3">
        <h2 class="mb-1 text-sm font-semibold text-slate-100">{{ block.title }}</h2>
        <p v-if="block.items.length === 0" class="text-xs text-slate-500">None.</p>
        <ul v-else :class="['list-disc space-y-0.5 pl-5 text-xs', block.tone]">
          <li v-for="(t, i) in block.items" :key="i">{{ t }}</li>
        </ul>
      </section>
    </template>
  </main>
</template>
