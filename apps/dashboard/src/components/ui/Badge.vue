<script setup lang="ts">
import { computed } from 'vue';
import { DOT_CLASS, TONE_CLASS, type Tone } from '@/lib/tones';

const props = withDefaults(defineProps<{ tone?: Tone; dot?: boolean; title?: string }>(), { tone: 'slate', dot: false, title: undefined });
const cls = computed(() => TONE_CLASS[props.tone]);
</script>

<template>
  <span :class="['inline-flex max-w-full items-center gap-1 truncate rounded px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset', cls]" :title="title">
    <span v-if="dot" :class="['h-1.5 w-1.5 shrink-0 rounded-full', DOT_CLASS[tone]]" aria-hidden="true" />
    <slot />
  </span>
</template>
