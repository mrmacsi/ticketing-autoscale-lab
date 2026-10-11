<script setup lang="ts">
import { computed } from 'vue';
import NumField from './NumField.vue';
import { useDraft } from '@/store/draft';

defineProps<{ maxSyntheticCpuMs: number }>();
const draft = useDraft();
const c = computed(() => draft.config.value!);
const mix = computed(() => c.value.traffic.mix);
const b = computed(() => c.value.traffic.behaviour);
const err = draft.fieldError;
const mixTotal = computed(() => draft.validation.value?.mixTotal ?? Number.NaN);

function randomiseSeed() {
  c.value.traffic.seed = Math.floor(Math.random() * 2_147_483_647);
}
</script>

<template>
  <fieldset class="min-w-0 space-y-2">
    <legend class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-400">Journey mix &amp; behaviour</legend>
    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="mix.browse" label="Browse" description="Share of customer journeys that browse events and event details. For example, 40% sends about 40 in 100 customers down this path; all four shares must total 100%." suffix="%" :min="0" :max="100" :error="err('traffic.mix.browse')" testid="mix-browse" />
      <NumField v-model="mix.availability" label="Availability" description="Share of journeys that check ticket availability. This reads remaining inventory without reserving or buying tickets." suffix="%" :min="0" :max="100" :error="err('traffic.mix.availability')" testid="mix-availability" />
      <NumField v-model="mix.reserve" label="Reserve" description="Share of journeys that hold tickets and leave without buying. These holds temporarily reduce availability, then expire after the reservation TTL." suffix="%" :min="0" :max="100" :error="err('traffic.mix.reserve')" testid="mix-reserve" />
      <NumField v-model="mix.purchase" label="Purchase" description="Share of journeys that reserve tickets, create an order, and attempt payment. Purchases can still be abandoned, declined, or rejected when tickets sell out." suffix="%" :min="0" :max="100" :error="err('traffic.mix.purchase')" testid="mix-purchase" />
    </div>
    <p :class="['text-xs', err('traffic.mix') ? 'text-red-300' : 'text-emerald-300']" :role="err('traffic.mix') ? 'alert' : undefined" data-testid="mix-total">
      {{ err('traffic.mix') ?? `Mix total ${mixTotal} %` }}
    </p>

    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="b.maxQuantity" label="Max tickets / reservation" description="Largest number of tickets a simulated customer can request in one reservation, from 1 to 8. Smaller quantities are more common; larger limits can exhaust inventory faster." :min="1" :max="8" :error="err('traffic.behaviour.maxQuantity')" />
      <NumField v-model="b.thinkTimeMinMs" label="Think time min" description="Shortest simulated pause between customer actions, in milliseconds. Longer pauses keep journeys in progress for longer and use more virtual users." suffix="ms" :min="0" :error="err('traffic.behaviour.thinkTimeMinMs')" />
      <NumField v-model="b.thinkTimeMaxMs" label="Think time max" description="Longest simulated pause between customer actions, in milliseconds. Pauses vary between this value and the minimum, representing time spent deciding or checking out." suffix="ms" :min="0" :error="err('traffic.behaviour.thinkTimeMaxMs')" />
      <NumField v-model="b.abandonCheckoutProbability" label="Abandon checkout" description="Chance a purchase journey leaves after creating an order, before payment. For example, 10% leaves about 1 in 10 checkouts unpaid; held tickets later expire." :scale="100" step="any" suffix="%" :error="err('traffic.behaviour.abandonCheckoutProbability')" />
      <NumField v-model="b.duplicateSubmitProbability" label="Duplicate submit" description="Chance a reservation, order, or initial payment request is submitted again with the same key. This tests that repeated clicks do not create duplicate purchases." :scale="100" step="any" suffix="%" :error="err('traffic.behaviour.duplicateSubmitProbability')" />
      <NumField v-model="b.retryPaymentAfterDeclineProbability" label="Retry after decline" description="Chance of trying another payment after a simulated decline. Each retry uses a new key and adds requests; it does not guarantee a successful payment." :scale="100" step="any" suffix="%" :error="err('traffic.behaviour.retryPaymentAfterDeclineProbability')" />
    </div>

    <div class="flex items-end gap-2">
      <div class="min-w-0 flex-1">
        <NumField v-model="c.traffic.seed" label="Random seed" description="Number used to repeat planned arrival timing and customer choices with the same settings. Actual response times and outcomes can still differ. Randomise chooses a new seed." :error="err('traffic.seed')" testid="seed" />
      </div>
      <button type="button" class="btn btn-default mb-5" data-testid="randomise-seed" @click="randomiseSeed">Randomise</button>
    </div>

    <div class="rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5">
      <p class="text-xs font-semibold text-amber-200">Demo-only synthetic CPU work — disabled by default, never affects inventory</p>
      <p class="mt-0.5 text-[11px] text-amber-100/70">Adds <code>GET /api/lab/cpu-work?ms=N</code> to some journeys to make CPU scaling visible at low traffic. Leave at 0 for realistic runs.</p>
      <div class="mt-2 grid grid-cols-1 gap-3 @md:grid-cols-2">
        <NumField v-model="c.syntheticCpuMaxMs" label="Server cap (ms)" description="Largest amount of demo CPU work the API allows per request, in milliseconds. Set 0 to disable it; larger per-journey requests are capped at this value." :min="0" :max="maxSyntheticCpuMs" :error="err('syntheticCpuMaxMs')" :hint="`limit ${maxSyntheticCpuMs} ms`" />
        <NumField v-model="b.syntheticCpuMs" label="Per journey (ms)" description="Milliseconds of extra CPU work requested by each selected journey. This adds a separate demo request to make CPU scaling visible; it does not change ticket inventory." :min="0" :error="err('traffic.behaviour.syntheticCpuMs')" />
        <NumField v-model="b.syntheticCpuProbability" label="Probability" description="Share of journeys that add the demo CPU request. For example, 25% adds it to about 1 in 4 journeys. Set 0 to leave synthetic CPU work off." :scale="100" step="any" suffix="%" :error="err('traffic.behaviour.syntheticCpuProbability')" />
      </div>
    </div>
  </fieldset>
</template>
