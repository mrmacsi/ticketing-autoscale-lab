<script setup lang="ts">
import Badge from './Badge.vue';
import { freshnessTone } from '@/lib/tones';
import type { Freshness } from '@/types/contracts';

defineProps<{ freshness: Freshness | null | undefined; label?: string }>();
</script>

<template>
  <Badge
    :tone="freshnessTone(freshness?.status)"
    dot
    :title="freshness?.note ?? undefined"
    :data-freshness="freshness?.status ?? 'missing'"
  >
    <template v-if="label">{{ label }}: </template>{{ freshness?.status ?? 'missing' }}
  </Badge>
</template>
