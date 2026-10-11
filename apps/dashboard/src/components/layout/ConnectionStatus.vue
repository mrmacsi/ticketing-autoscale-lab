<script setup lang="ts">
import { computed } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import type { StreamState } from '@/api/sse';
import type { Tone } from '@/lib/tones';

const props = defineProps<{ stream: StreamState; nowMs: number }>();
const emit = defineEmits<{ retry: [] }>();

const tone = computed<Tone>(() => {
  switch (props.stream.status) {
    case 'connected':
      return 'emerald';
    case 'connecting':
      return 'sky';
    case 'reconnecting':
      return 'amber';
    default:
      return 'red';
  }
});
const retryIn = computed(() => (props.stream.nextRetryAt ? Math.max(0, Math.ceil((props.stream.nextRetryAt - props.nowMs) / 1000)) : null));
</script>

<template>
  <div class="flex items-center gap-1.5" data-testid="connection-status">
    <Badge :tone="tone" dot :title="stream.lastError ?? undefined">
      <template v-if="stream.status === 'connected'">live stream</template>
      <template v-else-if="stream.status === 'reconnecting'">reconnecting #{{ stream.attempt }}<template v-if="retryIn != null"> in {{ retryIn }}s</template></template>
      <template v-else>{{ stream.status }}</template>
    </Badge>
    <button v-if="stream.status === 'reconnecting'" type="button" class="text-[11px] text-sky-300 hover:underline" @click="emit('retry')">retry now</button>
  </div>
</template>
