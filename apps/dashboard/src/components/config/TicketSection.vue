<script setup lang="ts">
import { computed, useId } from 'vue';
import NumField from './NumField.vue';
import Field from '@/components/ui/Field.vue';
import { useDraft } from '@/store/draft';

defineProps<{ maxInventory: number }>();
const draft = useDraft();
const t = computed(() => draft.config.value!.tickets);
const err = draft.fieldError;
const nameId = useId();

function setCategoryCount(n: number) {
  t.value.categoryCount = n;
  if (!Number.isInteger(n) || n < 1 || n > 10) return;
  const prices = t.value.pricesCents;
  while (prices.length < n) prices.push(prices[prices.length - 1] ?? 5000);
  prices.length = n;
}
</script>

<template>
  <fieldset class="min-w-0 space-y-2">
    <legend class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-400">Ticketing</legend>
    <Field v-slot="{ describedBy }" label="Event name" description="Name of the ticket event created by Prepare Experiment. It identifies the event that the simulated customers browse and buy tickets for." :error="err('tickets.eventName')" :for="nameId">
      <input :id="nameId" v-model="t.eventName" type="text" :aria-describedby="describedBy" :aria-invalid="!!err('tickets.eventName')" :class="['input', err('tickets.eventName') ? 'input-error' : '']" />
    </Field>
    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="t.totalInventory" label="Total inventory" description="Total tickets available across all categories when the event is created. Smaller inventory sells out sooner and makes contention easier to observe." :min="1" :max="maxInventory" :error="err('tickets.totalInventory')" :hint="`max ${maxInventory.toLocaleString('en-US')}`" />
      <NumField :model-value="t.categoryCount" label="Categories" description="Number of ticket categories to create, from 1 to 10. Inventory is split across them, and each category gets its own simulated price." :min="1" :max="10" :error="err('tickets.categoryCount')" @update:model-value="setCategoryCount" />
      <NumField v-model="t.reservationTtlSeconds" label="Reservation TTL" description="Seconds a ticket hold lasts before it expires. Customers who do not pay in time lose the hold, returning those tickets to available inventory." suffix="s" :min="1" :error="err('tickets.reservationTtlSeconds')" />
    </div>
    <div>
      <div class="label">Simulated prices per category (no real payments)</div>
      <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
        <NumField
          v-for="(_, i) in t.pricesCents"
          :key="i"
          v-model="t.pricesCents[i]"
          :label="`Category ${i + 1}`"
          description="Simulated price for one ticket in this category, shown in dollars. It changes the order total, not the amount of available inventory. No real money is charged."
          :scale="0.01"
          :step="0.01"
          suffix="$"
          :min="0"
        />
      </div>
      <p v-if="err('tickets.pricesCents')" class="mt-1 text-xs text-red-300" role="alert">{{ err('tickets.pricesCents') }}</p>
    </div>
    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="t.paymentDelayMinMs" label="Payment delay min" description="Shortest simulated payment processing time, in milliseconds. Actual delays are chosen between this value and the maximum." suffix="ms" :min="0" :error="err('tickets.paymentDelayMinMs')" />
      <NumField v-model="t.paymentDelayMaxMs" label="Payment delay max" description="Longest simulated payment processing time, in milliseconds. Longer delays keep payments in flight for longer and can increase concurrent work." suffix="ms" :min="0" :error="err('tickets.paymentDelayMaxMs')" />
      <NumField v-model="t.paymentFailureProbability" label="Payment failure" description="Chance that a simulated payment attempt is declined. For example, 5% means roughly 5 in 100 attempts; a decline may be retried. No real payments occur." :scale="100" step="any" suffix="%" :min="0" :max="100" :error="err('tickets.paymentFailureProbability')" />
    </div>
  </fieldset>
</template>
