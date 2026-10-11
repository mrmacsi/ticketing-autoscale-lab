<script setup lang="ts">
import { computed } from 'vue';
import NumField from './NumField.vue';
import { useDraft } from '@/store/draft';

defineProps<{ approvedMax: number }>();
const draft = useDraft();
const s = computed(() => draft.config.value!.servers);
const err = draft.fieldError;
</script>

<template>
  <fieldset class="min-w-0 space-y-2">
    <legend class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-400">Servers (MIG autoscaler)</legend>
    <div class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <NumField v-model="s.initial" label="Initial" description="Number of healthy application VMs required before the test is ready. Prepare temporarily raises the minimum to this count; Start restores your chosen minimum." :min="1" :max="approvedMax" :error="err('servers.initial')" testid="servers-initial" />
      <NumField v-model="s.min" label="Min" description="Fewest application VMs the autoscaler keeps running during the test, even with no traffic. A higher minimum keeps more capacity available and costs more." :min="1" :max="approvedMax" :error="err('servers.min')" testid="servers-min" />
      <NumField v-model="s.max" label="Max" description="Most application VMs the autoscaler may create. A higher maximum lets the lab handle more load, but can increase the cost of the run." :min="1" :max="approvedMax" :error="err('servers.max')" :hint="`approved max ${approvedMax}`" testid="servers-max" />
      <NumField v-model="s.cpuTarget" label="CPU target" description="Average CPU level the autoscaler aims for across application VMs. At 60%, sustained load above that level can add servers; a lower target keeps more spare capacity." :scale="100" suffix="%" :min="1" :max="100" :error="err('servers.cpuTarget')" testid="servers-cpu" />
      <NumField v-model="s.initPeriodSeconds" label="Init period" description="Seconds a new VM is allowed to warm up before its CPU is used for scale-out decisions. Set this to cover startup time so temporary boot activity does not trigger extra servers." suffix="s" :min="0" :error="err('servers.initPeriodSeconds')" />
      <NumField v-model="s.scaleInWindowSeconds" label="Scale-in window" description="Seconds of recent peak demand considered before removing servers. A longer window keeps spare capacity longer after a burst; removal is not immediate when traffic stops." suffix="s" :min="0" :error="err('servers.scaleInWindowSeconds')" />
    </div>
  </fieldset>
</template>
