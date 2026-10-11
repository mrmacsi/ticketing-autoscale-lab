<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';
import Card from '@/components/ui/Card.vue';
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue';
import StatePill from '@/components/layout/StatePill.vue';
import { blockedReason, type ExperimentAction } from '@/lib/experimentState';
import { ageSeconds, fmtClock, fmtDateTime } from '@/lib/format';
import { useDraft } from '@/store/draft';
import { useLab, type ActionName } from '@/store/lab';
import type { ExportFormat } from '@/api/client';

const store = useLab();
const draft = useDraft();

const exp = computed(() => store.experiment.value);
const state = computed(() => exp.value?.state ?? null);
const busy = computed(() => store.status.busy);
const overridesActive = computed(() => store.snapshot.value?.mig?.overridesActive ?? false);
const confirmReset = ref(false);
const exporting = ref<ExportFormat | null>(null);
const exportError = ref<string | null>(null);

const reason = (a: ExperimentAction) => blockedReason(a, state.value, { overridesActive: overridesActive.value });

const prepareReason = computed(() => {
  const r = reason('prepare');
  if (r) return r;
  if (!draft.config.value) return 'Configuration not loaded yet.';
  if (draft.validation.value && !draft.validation.value.valid) return 'Fix the highlighted configuration fields first.';
  return null;
});

const failure = computed(() => {
  const f = store.status.lastFailure;
  // 422/428 for prepare are rendered next to the form fields / in the confirmation dialog.
  if (!f || (f.action === 'prepare' && (f.status === 422 || f.status === 428))) return null;
  return f;
});

const elapsed = computed(() => {
  const started = exp.value?.trafficStartedAt;
  if (!started) return null;
  const end = exp.value?.trafficStoppedAt ? Date.parse(exp.value.trafficStoppedAt) : store.serverNow();
  return ageSeconds(started, end);
});

async function run(action: Exclude<ActionName, 'prepare'>) {
  await store[action]();
}

async function onReset() {
  const r = await store.reset();
  if (r.ok) confirmReset.value = false;
}

async function download(format: ExportFormat) {
  const id = exp.value?.id;
  if (!id || exporting.value) return;
  exporting.value = format;
  exportError.value = null;
  try {
    const { blob, filename } = await store.api.downloadExport(id, format);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (err) {
    exportError.value = err instanceof Error ? err.message : String(err);
  } finally {
    exporting.value = null;
  }
}
</script>

<template>
  <Card title="Experiment controls" tag="B">
    <div class="space-y-3">
      <div class="flex flex-wrap items-center gap-2 text-sm" data-testid="current-experiment">
        <StatePill :state="state" />
        <template v-if="exp">
          <span class="min-w-0 truncate font-medium text-slate-100" :title="exp.id">{{ exp.name }}</span>
          <span class="font-mono text-[11px] text-slate-500">{{ exp.id }}</span>
        </template>
        <span v-else class="text-slate-400">No experiment — configure one and press Prepare.</span>
      </div>
      <dl v-if="exp" class="grid grid-cols-2 gap-x-3 gap-y-0.5 text-[11px] text-slate-400">
        <dt>Created</dt>
        <dd class="tabular-nums text-slate-300">{{ fmtDateTime(exp.createdAt) }}</dd>
        <dt>Traffic started</dt>
        <dd class="tabular-nums text-slate-300">{{ fmtDateTime(exp.trafficStartedAt ?? null, '—') }}</dd>
        <dt>Traffic elapsed</dt>
        <dd class="tabular-nums text-slate-300" data-testid="elapsed">{{ elapsed == null ? '—' : fmtClock(elapsed) }}</dd>
        <template v-if="exp.message || exp.error">
          <dt>Message</dt>
          <dd :class="exp.error ? 'text-red-300' : 'text-slate-300'">{{ exp.error ?? exp.message }}</dd>
        </template>
      </dl>

      <div class="grid grid-cols-2 gap-2">
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!!busy || !!prepareReason"
          :title="prepareReason ?? 'Create the experiment, apply autoscaler overrides and seed the ticket event'"
          data-testid="btn-prepare"
          @click="draft.submit()"
        >
          {{ busy === 'prepare' ? 'Preparing…' : 'Prepare Experiment' }}
        </button>
        <button
          type="button"
          class="btn btn-default"
          :disabled="!!busy || !!reason('start')"
          :title="reason('start') ?? 'Start the load generator'"
          data-testid="btn-start"
          @click="run('start')"
        >
          {{ busy === 'start' ? 'Starting…' : 'Start Traffic' }}
        </button>
      </div>
      <button
        type="button"
        class="btn btn-danger w-full py-2.5 text-base font-semibold"
        :disabled="!!busy || !!reason('stop')"
        :title="reason('stop') ?? 'Hard stop: the generator stops scheduling arrivals immediately'"
        data-testid="btn-stop"
        @click="run('stop')"
      >
        ■ {{ busy === 'stop' ? 'Stopping…' : 'Stop Traffic' }}
      </button>
      <div class="grid grid-cols-2 gap-2">
        <button
          type="button"
          class="btn btn-default"
          :disabled="!!busy || !!reason('complete')"
          :title="reason('complete') ?? 'End the cool-down observation and build the report'"
          data-testid="btn-complete"
          @click="run('complete')"
        >
          Complete observation
        </button>
        <button
          type="button"
          class="btn btn-default"
          :disabled="!!busy || !!reason('resetInventory')"
          :title="reason('resetInventory') ?? 'Release reservations and delete orders/tickets for this event'"
          data-testid="btn-reset-inventory"
          @click="run('resetInventory')"
        >
          Reset Ticket Inventory
        </button>
      </div>
      <p v-if="reason('resetInventory') && (state === 'running' || state === 'stopping')" class="text-[11px] text-amber-300" data-testid="reset-inventory-reason">
        {{ reason('resetInventory') }}
      </p>
      <div class="grid grid-cols-2 gap-2">
        <button type="button" class="btn btn-default" :disabled="!!busy || !!reason('reset')" :title="reason('reset') ?? undefined" data-testid="btn-reset" @click="confirmReset = true">
          Reset Experiment
        </button>
        <RouterLink to="/history" class="btn btn-ghost" data-testid="link-history">View Historical Experiments</RouterLink>
      </div>
      <div class="flex flex-wrap items-center gap-2 text-xs">
        <span class="text-slate-400">Export results:</span>
        <button type="button" class="btn btn-ghost text-xs" :disabled="!exp || !!exporting" data-testid="btn-export-json" @click="download('json')">
          {{ exporting === 'json' ? 'Exporting…' : 'JSON' }}
        </button>
        <button type="button" class="btn btn-ghost text-xs" :disabled="!exp || !!exporting" data-testid="btn-export-csv" @click="download('csv')">
          {{ exporting === 'csv' ? 'Exporting…' : 'CSV' }}
        </button>
        <RouterLink v-if="exp" :to="`/experiments/${encodeURIComponent(exp.id)}/report`" class="text-sky-300 hover:underline">Report</RouterLink>
      </div>
      <p v-if="exportError" class="text-xs text-red-300" role="alert">Export failed: {{ exportError }}</p>
      <p v-if="failure" class="rounded-md border border-red-500/40 bg-red-500/10 px-2.5 py-2 text-xs text-red-200" role="alert" data-testid="action-error">
        {{ failure.action }} failed<template v-if="failure.status"> (HTTP {{ failure.status }})</template>: {{ failure.message }}
      </p>
    </div>

    <ConfirmDialog :open="confirmReset" title="Reset experiment?" confirm-label="Reset" danger :busy="busy === 'reset'" @confirm="onReset" @cancel="confirmReset = false">
      This aborts and forgets the current experiment, stops any traffic and restores the autoscaler to its Terraform baseline.
    </ConfirmDialog>
  </Card>
</template>
