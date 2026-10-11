<script setup lang="ts">
import Badge from '@/components/ui/Badge.vue';
import { label } from '@/lib/experimentState';
import { stateTone } from '@/lib/tones';
import type { ExperimentState } from '@/types/contracts';

defineProps<{ state: ExperimentState | null | undefined }>();
</script>

<template>
  <Badge :tone="stateTone(state)" dot data-testid="state-pill" :data-state="state ?? 'idle'">{{ label(state ?? 'idle') }}</Badge>
</template>
