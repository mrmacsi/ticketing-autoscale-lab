<script setup lang="ts">
import { ref, watch } from 'vue';

const props = withDefaults(
  defineProps<{ open: boolean; title: string; confirmLabel?: string; danger?: boolean; requireAck?: string | null; busy?: boolean }>(),
  { confirmLabel: 'Confirm', danger: false, requireAck: null, busy: false },
);
const emit = defineEmits<{ confirm: []; cancel: [] }>();
const ack = ref(false);
watch(
  () => props.open,
  (o) => {
    if (o) ack.value = false;
  },
);
</script>

<template>
    <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" role="dialog" aria-modal="true" :aria-label="title" @keydown.esc="emit('cancel')">
      <div class="w-full max-w-md rounded-lg border border-slate-700 bg-slate-900 p-4 shadow-xl">
        <h3 class="text-base font-semibold text-slate-50">{{ title }}</h3>
        <div class="mt-2 text-sm text-slate-300"><slot /></div>
        <label v-if="requireAck" class="mt-3 flex items-start gap-2 text-sm text-slate-200">
          <input v-model="ack" type="checkbox" class="mt-0.5 h-4 w-4 accent-sky-500" data-testid="confirm-ack" />
          <span>{{ requireAck }}</span>
        </label>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="btn btn-default" @click="emit('cancel')">Cancel</button>
          <button
            type="button"
            :class="['btn', danger ? 'btn-danger' : 'btn-primary']"
            :disabled="busy || (!!requireAck && !ack)"
            data-testid="confirm-ok"
            @click="emit('confirm')"
          >
            {{ confirmLabel }}
          </button>
        </div>
      </div>
    </div>
</template>
