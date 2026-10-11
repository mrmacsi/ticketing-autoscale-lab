<script setup lang="ts">
import { computed, ref, useId } from 'vue';
import NumField from './NumField.vue';
import CustomScheduleEditor from './CustomScheduleEditor.vue';
import Field from '@/components/ui/Field.vue';
import { useDraft } from '@/store/draft';
import type { MetaLimits, PatternType, TrafficPattern } from '@/types/contracts';

defineProps<{ limits: MetaLimits }>();
const draft = useDraft();
const tr = computed(() => draft.config.value!.traffic);
const p = computed(() => tr.value.pattern);
const err = draft.fieldError;
const unit = ref<'seconds' | 'minutes'>('seconds');
const patternId = useId();

const PATTERNS: Array<{ value: PatternType; label: string }> = [
  { value: 'constant', label: 'Constant' },
  { value: 'ramp_up', label: 'Ramp up' },
  { value: 'ramp_down', label: 'Ramp down' },
  { value: 'spike', label: 'Spike' },
  { value: 'random_bursts', label: 'Random bursts' },
  { value: 'custom', label: 'Custom schedule' },
];

/** Fill parameters the newly chosen pattern needs (existing values are kept). */
function setPattern(type: PatternType) {
  const d = Number.isFinite(tr.value.durationSeconds) ? tr.value.durationSeconds : 600;
  const cur: TrafficPattern = { ...p.value, type };
  const def = (k: keyof TrafficPattern, v: number) => {
    if (typeof cur[k] !== 'number') (cur as unknown as Record<string, unknown>)[k] = v;
  };
  if (type === 'constant') def('rate', 10);
  if (type === 'ramp_up') {
    def('startRate', 5);
    def('endRate', 50);
  }
  if (type === 'ramp_down') {
    def('startRate', 50);
    def('endRate', 5);
  }
  if (type === 'spike' || type === 'random_bursts') {
    def('baseRate', 5);
    def('peakRate', 50);
  }
  if (type === 'spike') {
    def('spikeAtSeconds', Math.floor(d / 3));
    def('spikeDurationSeconds', Math.min(60, Math.max(1, Math.floor(d / 10))));
  }
  if (type === 'random_bursts') {
    def('burstCount', 3);
    def('burstDurationSeconds', Math.min(30, Math.max(1, Math.floor(d / 10))));
  }
  if (type === 'custom' && !cur.points?.length) {
    cur.points = [
      { atSeconds: 0, rate: 5 },
      { atSeconds: Math.floor(d / 2), rate: 50 },
      { atSeconds: d, rate: 5 },
    ];
  }
  tr.value.pattern = cur;
}
</script>

<template>
  <fieldset class="min-w-0 space-y-2">
    <legend class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-400">Traffic</legend>
    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <Field v-slot="{ describedBy }" label="Pattern" description="How the requested customer arrival rate changes over time: steady, gradual increase or decrease, one spike, random bursts, or your own schedule." :for="patternId">
        <select :id="patternId" :aria-describedby="describedBy" class="input" :value="p.type" data-testid="pattern-type" @change="setPattern(($event.target as HTMLSelectElement).value as PatternType)">
          <option v-for="o in PATTERNS" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </Field>
      <div class="min-w-0">
        <NumField
          v-model="tr.durationSeconds"
          :label="`Duration (${unit})`"
          description="How long to schedule new customers after traffic starts. For example, 600 seconds is 10 minutes. An arrival cap can end scheduling sooner; ongoing journeys then get a short time to finish."
          :scale="unit === 'minutes' ? 1 / 60 : 1"
          :step="unit === 'minutes' ? 'any' : 1"
          :min="1"
          :error="err('traffic.durationSeconds')"
          :hint="`max ${limits.maxDurationSeconds}s`"
          testid="duration"
        />
        <div class="mt-1 flex gap-1 text-[11px]" role="group" aria-label="Duration unit">
          <button
            v-for="u in ['seconds', 'minutes'] as const"
            :key="u"
            type="button"
            :aria-pressed="unit === u"
            :class="['rounded px-1.5 py-0.5', unit === u ? 'bg-sky-600 text-white' : 'bg-slate-800 text-slate-300']"
            @click="unit = u"
          >
            {{ u }}
          </button>
        </div>
        <p class="mt-1 text-[11px] text-slate-400">Switch units without changing the actual duration.</p>
      </div>
      <NumField v-model="tr.totalArrivals" label="Total arrivals" description="Stop scheduling after this many customer arrivals, or when duration ends, whichever comes first. Includes arrivals dropped when all virtual users are busy. Set 0 to use only the duration limit." :min="0" :error="err('traffic.totalArrivals')" hint="0 = unlimited" testid="total-arrivals" />
    </div>

    <div v-if="p.type === 'constant'" class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="p.rate" label="Rate" description="Requested new customer journeys per second. Each journey can make several HTTP requests, so 10 arrivals/s can produce more than 10 requests/s." suffix="/s" step="any" :min="0" :max="limits.maxRate" :error="err('traffic.pattern.rate')" testid="rate" />
    </div>
    <div v-else-if="p.type === 'ramp_up' || p.type === 'ramp_down'" class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="p.startRate" label="Start rate" description="Requested customer arrivals per second at the start of the test. The rate changes gradually from here to the end rate over the full duration." suffix="/s" step="any" :min="0" :max="limits.maxRate" :error="err('traffic.pattern.startRate')" />
      <NumField v-model="p.endRate" label="End rate" description="Requested customer arrivals per second at the end of the test. Set above the start rate for a ramp up, or below it for a ramp down." suffix="/s" step="any" :min="0" :max="limits.maxRate" :error="err('traffic.pattern.endRate')" />
    </div>
    <div v-else-if="p.type === 'spike' || p.type === 'random_bursts'" class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="p.baseRate" label="Base rate" description="Requested customer arrivals per second outside spikes or bursts. This is the background traffic level." suffix="/s" step="any" :min="0" :max="limits.maxRate" :error="err('traffic.pattern.baseRate')" />
      <NumField v-model="p.peakRate" label="Peak rate" description="Requested customer arrivals per second during a spike or burst. A higher peak adds sudden pressure without changing the background rate." suffix="/s" step="any" :min="0" :max="limits.maxRate" :error="err('traffic.pattern.peakRate')" />
      <template v-if="p.type === 'spike'">
        <NumField v-model="p.spikeAtSeconds" label="Spike at" description="Seconds after traffic starts when the spike begins. For example, 60 puts the spike one minute into the run." suffix="s" :min="0" :error="err('traffic.pattern.spikeAtSeconds')" />
        <NumField v-model="p.spikeDurationSeconds" label="Spike length" description="Seconds to stay at the peak rate before returning to the base rate. Any part beyond the test duration is cut off." suffix="s" :min="1" :error="err('traffic.pattern.spikeDurationSeconds')" />
      </template>
      <template v-else>
        <NumField v-model="p.burstCount" label="Bursts" description="Number of burst windows spread across the run. Their start times vary using the random seed; long windows can overlap." :min="1" :error="err('traffic.pattern.burstCount')" />
        <NumField v-model="p.burstDurationSeconds" label="Burst length" description="Seconds each random burst stays at the peak rate. Shorter bursts create briefer load surges; longer bursts sustain the pressure." suffix="s" :min="1" :error="err('traffic.pattern.burstDurationSeconds')" />
      </template>
    </div>
    <CustomScheduleEditor v-else-if="p.type === 'custom'" :max-rate="limits.maxRate" />

    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="tr.maxVUs" label="Max virtual users" description="Maximum customer journeys in progress at the same time. If all are busy, new arrivals are dropped rather than queued. Increase this if the generator cannot keep up with the requested rate." :min="1" :max="limits.maxVUs" :error="err('traffic.maxVUs')" :hint="`max ${limits.maxVUs}`" testid="max-vus" />
      <NumField v-model="tr.requestTimeoutMs" label="Request timeout" description="Maximum time to wait for each HTTP request, in milliseconds. A slower response counts as a timeout error; 10000 allows 10 seconds." suffix="ms" :min="100" :error="err('traffic.requestTimeoutMs')" />
    </div>
  </fieldset>
</template>
