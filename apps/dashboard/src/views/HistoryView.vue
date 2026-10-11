<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { RouterLink } from 'vue-router';
import Card from '@/components/ui/Card.vue';
import StatePill from '@/components/layout/StatePill.vue';
import { fmtDateTime, fmtDuration, fmtNumber, fmtRate } from '@/lib/format';
import { scaleInSummary } from '@/lib/scaleIn';
import { useLab } from '@/store/lab';
import type { ExperimentSummary } from '@/types/contracts';

const store = useLab();
const scaleInOf = (r: ExperimentSummary) => scaleInSummary(r.scaleIn, r.minServers, { state: r.state, completedAt: r.completedAt });
const rows = ref<ExperimentSummary[] | null>(null);
const error = ref<string | null>(null);
const loading = ref(false);

async function load() {
  loading.value = true;
  error.value = null;
  try {
    rows.value = await store.api.listExperiments();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}
onMounted(load);
</script>

<template>
  <main class="mx-auto max-w-[1800px] px-3 py-3 sm:px-4">
    <Card title="Historical experiments" subtitle="Measured values come from each experiment's recorded telemetry.">
      <template #actions>
        <button type="button" class="btn btn-default text-xs" :disabled="loading" @click="load">{{ loading ? 'Loading…' : 'Refresh' }}</button>
      </template>
      <p v-if="error" class="text-sm text-red-300" role="alert">Could not load history: {{ error }}</p>
      <p v-else-if="rows && rows.length === 0" class="text-sm text-slate-400">No experiments recorded yet.</p>
      <div v-else-if="rows" class="overflow-x-auto">
        <table class="w-full min-w-[1040px] text-left text-sm" data-testid="history-table">
          <thead class="text-[11px] uppercase tracking-wide text-slate-400">
            <tr class="border-b border-slate-800">
              <th class="px-2 py-1.5 font-medium">Name</th>
              <th class="px-2 py-1.5 font-medium">Preset</th>
              <th class="px-2 py-1.5 font-medium">State</th>
              <th class="px-2 py-1.5 font-medium">Traffic start</th>
              <th class="px-2 py-1.5 text-right font-medium">Duration</th>
              <th class="px-2 py-1.5 text-right font-medium">Peak RPS</th>
              <th class="px-2 py-1.5 text-right font-medium">Max servers</th>
              <th class="px-2 py-1.5 font-medium">Scale-in</th>
              <th class="px-2 py-1.5 text-right font-medium">Tickets sold</th>
              <th class="px-2 py-1.5 text-right font-medium">Violations</th>
              <th class="px-2 py-1.5" />
            </tr>
          </thead>
          <tbody class="tabular-nums">
            <tr v-for="r in rows" :key="r.id" class="border-b border-slate-800/60 hover:bg-slate-800/30" data-testid="history-row">
              <td class="max-w-[16rem] truncate px-2 py-1.5 text-slate-100" :title="r.id">{{ r.name }}</td>
              <td class="px-2 py-1.5">{{ r.preset ?? '—' }}</td>
              <td class="px-2 py-1.5"><StatePill :state="r.state" /></td>
              <td class="px-2 py-1.5 text-xs text-slate-300">{{ fmtDateTime(r.trafficStartedAt ?? null, '—') }}</td>
              <td class="px-2 py-1.5 text-right">{{ fmtDuration(r.durationSeconds ?? null) }}</td>
              <td class="px-2 py-1.5 text-right">{{ fmtRate(r.peakRps ?? null) }}</td>
              <td class="px-2 py-1.5 text-right">{{ fmtNumber(r.maxServers ?? null) }}</td>
              <td
                :class="['max-w-[18rem] truncate px-2 py-1.5 text-xs', scaleInOf(r).tone === 'good' ? 'text-emerald-300' : scaleInOf(r).tone === 'bad' ? 'text-amber-300' : 'text-slate-400']"
                :title="scaleInOf(r).text"
                data-testid="history-scale-in"
              >
                {{ r.state === 'completed' ? scaleInOf(r).text : '—' }}
              </td>
              <td class="px-2 py-1.5 text-right">{{ fmtNumber(r.ticketsSold ?? null) }}</td>
              <td :class="['px-2 py-1.5 text-right font-semibold', r.invariantViolations == null ? 'text-slate-400' : r.invariantViolations === 0 ? 'text-emerald-300' : 'text-red-300']">
                {{ fmtNumber(r.invariantViolations ?? null) }}
              </td>
              <td class="px-2 py-1.5 text-right">
                <RouterLink :to="`/experiments/${encodeURIComponent(r.id)}/report`" class="text-sky-300 hover:underline">Report</RouterLink>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="text-sm text-slate-400">Loading…</p>
    </Card>
  </main>
</template>
