<script setup lang="ts">
import { computed, useId } from 'vue';
import Field from '@/components/ui/Field.vue';

const props = withDefaults(
  defineProps<{
    label: string;
    modelValue: number | undefined;
    error?: string | null;
    hint?: string;
    description?: string;
    min?: number;
    max?: number;
    step?: number | 'any';
    suffix?: string;
    /** Display scale: shown = value × scale (e.g. 100 for fractions shown as %). */
    scale?: number;
    testid?: string;
  }>(),
  { error: null, hint: undefined, min: undefined, max: undefined, step: 1, suffix: undefined, scale: 1, testid: undefined },
);
const emit = defineEmits<{ 'update:modelValue': [value: number] }>();
const id = useId();

const shown = computed(() => {
  const v = props.modelValue;
  if (v === undefined || !Number.isFinite(v)) return '';
  return String(Math.round(v * props.scale * 1e6) / 1e6);
});

function onInput(e: Event) {
  const raw = (e.target as HTMLInputElement).value;
  const n = raw.trim() === '' ? Number.NaN : Number(raw);
  emit('update:modelValue', Number.isFinite(n) ? Math.round((n / props.scale) * 1e9) / 1e9 : Number.NaN);
}
</script>

<template>
  <Field v-slot="{ describedBy }" :label="label" :error="error" :hint="hint" :description="description" :for="id">
    <div class="relative">
      <input
        :id="id"
        type="number"
        inputmode="decimal"
        :class="['input pr-8 tabular-nums', error ? 'input-error' : '']"
        :value="shown"
        :min="min"
        :max="max"
        :step="step"
        :aria-invalid="!!error"
        :aria-describedby="describedBy"
        :data-testid="testid"
        @input="onInput"
      />
      <span v-if="suffix" class="pointer-events-none absolute inset-y-0 right-2 flex items-center text-xs text-slate-500">{{ suffix }}</span>
    </div>
  </Field>
</template>
