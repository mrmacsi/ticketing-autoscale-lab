<script setup lang="ts">
import { computed, useId } from 'vue';
const props = defineProps<{ label: string; error?: string | null; hint?: string; description?: string; for?: string }>();
const id = useId();
const descriptionId = `${id}-description`;
const hintId = `${id}-hint`;
const errorId = `${id}-error`;
const describedBy = computed(() => [props.description && descriptionId, props.hint && hintId, props.error && errorId].filter(Boolean).join(' ') || undefined);
</script>

<template>
  <div class="min-w-0" data-testid="form-field">
    <div :class="['min-w-0 gap-x-3 gap-y-1', description ? 'flex flex-wrap' : '']">
      <div :class="['min-w-0', description ? 'flex-1 basis-[110px]' : '']">
        <label class="label" :for="$props.for">{{ label }}</label>
        <slot :described-by="describedBy" />
        <p v-if="hint" :id="hintId" class="mt-1 text-[11px] text-slate-400">{{ hint }}</p>
        <p v-if="error" :id="errorId" class="mt-1 text-xs text-red-300" role="alert" data-testid="field-error">{{ error }}</p>
      </div>
      <p v-if="description" :id="descriptionId" class="min-w-0 flex-[1.4_1_140px] self-center text-xs leading-relaxed text-slate-400" data-testid="field-description">{{ description }}</p>
    </div>
  </div>
</template>
