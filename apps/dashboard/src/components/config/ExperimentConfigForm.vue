<script setup lang="ts">
import { computed, useId } from 'vue';
import Card from '@/components/ui/Card.vue';
import Field from '@/components/ui/Field.vue';
import NumField from './NumField.vue';
import PresetPicker from './PresetPicker.vue';
import MetricsExplainer from './MetricsExplainer.vue';
import ServerSection from './ServerSection.vue';
import TicketSection from './TicketSection.vue';
import WaitingRoomSection from './WaitingRoomSection.vue';
import TrafficSection from './TrafficSection.vue';
import MixBehaviourSection from './MixBehaviourSection.vue';
import RateProfilePreview from './RateProfilePreview.vue';
import ExpensiveConfirmDialog from './ExpensiveConfirmDialog.vue';
import { useDraft } from '@/store/draft';
import { useLab } from '@/store/lab';
import { isActive, label } from '@/lib/experimentState';

const store = useLab();
const draft = useDraft();
const meta = computed(() => store.meta.value);
const c = computed(() => draft.config.value);
const locked = computed(() => isActive(store.experiment.value?.state));
const nameId = useId();
const errorCount = computed(() => Object.keys(draft.validation.value?.errors ?? {}).length);
const unmatchedServerIssues = computed(() => draft.serverIssues.value);
</script>

<template>
  <Card title="Experiment configuration" tag="A" subtitle="Defines the next experiment. Applied when you press Prepare Experiment.">
    <template #actions>
      <button type="button" class="btn btn-ghost text-xs" :disabled="locked || !meta" @click="draft.resetToDefaults()">Defaults</button>
    </template>

    <p v-if="!meta" class="text-sm text-slate-400" data-testid="config-loading">
      {{ store.status.metaError ? `Could not load /api/meta: ${store.status.metaError}` : 'Loading limits and presets…' }}
    </p>

    <form v-else-if="c" class="@container min-w-0 space-y-4" novalidate @submit.prevent>
      <MetricsExplainer />
      <p v-if="locked" class="rounded-md bg-slate-800/80 px-2.5 py-2 text-xs text-slate-300" data-testid="config-locked">
        Locked while experiment “{{ store.experiment.value?.name }}” is {{ label(store.experiment.value!.state) }}.
      </p>
      <fieldset :disabled="locked" class="min-w-0 space-y-4">
        <PresetPicker :disabled="locked" />
        <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
          <Field v-slot="{ describedBy }" label="Experiment name" description="A label for this run in history and exported results. Use a name that describes the scenario you want to compare." :error="draft.fieldError('name')" :for="nameId">
            <input :id="nameId" v-model="c.name" type="text" :aria-describedby="describedBy" :aria-invalid="!!draft.fieldError('name')" :class="['input', draft.fieldError('name') ? 'input-error' : '']" data-testid="exp-name" />
          </Field>
          <NumField v-model="c.cooldownObserveSeconds" label="Cool-down observe" description="Seconds to keep collecting metrics after traffic stops. For example, 900 gives 15 minutes to watch servers scale in; it does not stop or delete the lab." suffix="s" :min="0" :error="draft.fieldError('cooldownObserveSeconds')" />
        </div>
        <ServerSection :approved-max="meta.approvedMaxInstances" />
        <WaitingRoomSection :deployed="meta.waitingRoom?.deployed ?? null" />
        <TicketSection :max-inventory="meta.limits.maxTotalInventory" />
        <TrafficSection :limits="meta.limits" />
        <RateProfilePreview />
        <MixBehaviourSection :max-synthetic-cpu-ms="meta.limits.maxSyntheticCpuMs" />
      </fieldset>

      <div v-if="draft.validation.value?.expensiveReasons.length" class="rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-xs text-amber-200" data-testid="expensive-warning">
        <p class="font-semibold">Expensive configuration — the control API will ask for confirmation</p>
        <ul class="mt-1 list-disc pl-5">
          <li v-for="r in draft.validation.value.expensiveReasons" :key="r">{{ r }}</li>
        </ul>
      </div>

      <p v-if="errorCount > 0" class="text-xs text-red-300" data-testid="validation-summary">{{ errorCount }} field{{ errorCount === 1 ? '' : 's' }} need attention before preparing.</p>

      <div v-if="draft.serverMessage.value" class="rounded-md border border-red-500/40 bg-red-500/10 p-2.5 text-xs text-red-200" role="alert" data-testid="server-error">
        <p class="font-semibold">Control API rejected the configuration: {{ draft.serverMessage.value }}</p>
        <ul v-if="unmatchedServerIssues.length" class="mt-1 list-disc pl-5">
          <li v-for="(i, k) in unmatchedServerIssues" :key="k"><span v-if="i.path" class="font-mono">{{ i.path }}: </span>{{ i.message }}</li>
        </ul>
      </div>
    </form>
    <ExpensiveConfirmDialog />
  </Card>
</template>
