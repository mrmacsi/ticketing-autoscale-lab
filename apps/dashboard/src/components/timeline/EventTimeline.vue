<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import { eventOffset, eventReference, fmtTimeOfDay } from '@/lib/format';
import { attributionTone, KIND_GLYPH, kindTone, TONE_CLASS } from '@/lib/tones';
import type { TimelineEvent } from '@/types/contracts';

const props = withDefaults(
  defineProps<{
    events: readonly TimelineEvent[];
    /** ISO time of traffic start; relative times count from here when set. */
    trafficStartedAt?: string | null;
    /** ISO time of experiment creation; used before traffic starts. */
    createdAt?: string | null;
    maxHeight?: string;
  }>(),
  { trafficStartedAt: null, createdAt: null, maxHeight: '28rem' },
);

const newestFirst = ref(true);
const autoScroll = ref(true);
const list = ref<HTMLElement | null>(null);

const reference = computed(() => eventReference(props.trafficStartedAt, props.createdAt));

const ordered = computed(() => {
  const arr = [...props.events];
  return newestFirst.value ? arr.reverse() : arr;
});

function relative(e: TimelineEvent): string {
  return eventOffset(e.t, props.trafficStartedAt, props.createdAt)?.text ?? '—';
}

watch(
  () => props.events.length,
  async () => {
    if (!autoScroll.value || !list.value) return;
    await nextTick();
    const el = list.value;
    if (!el) return;
    el.scrollTop = newestFirst.value ? 0 : el.scrollHeight;
  },
);
</script>

<template>
  <div>
    <div class="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs text-slate-400">
      <span>
        {{ events.length }} event{{ events.length === 1 ? '' : 's' }} from the control API
        <template v-if="reference"> · relative time {{ reference.label }}</template>
      </span>
      <div class="flex items-center gap-3">
        <label class="flex items-center gap-1.5"><input v-model="newestFirst" type="checkbox" class="accent-sky-500" /> newest first</label>
        <label class="flex items-center gap-1.5"><input v-model="autoScroll" type="checkbox" class="accent-sky-500" /> auto-scroll</label>
      </div>
    </div>
    <p v-if="events.length === 0" class="rounded-md border border-dashed border-slate-700 p-4 text-center text-sm text-slate-400" data-testid="timeline-empty">
      No events recorded yet.
    </p>
    <ol v-else ref="list" class="space-y-1 overflow-y-auto pr-1" :style="{ maxHeight }" data-testid="timeline">
      <li
        v-for="e in ordered"
        :key="e.id"
        class="grid grid-cols-[auto_1fr] gap-x-2 rounded-md bg-slate-950/50 px-2 py-1.5 ring-1 ring-slate-800"
        data-testid="timeline-event"
        :data-event-id="e.id"
        :data-kind="e.kind"
      >
        <span
          :class="['mt-0.5 flex h-6 w-6 items-center justify-center rounded text-xs font-bold ring-1 ring-inset', TONE_CLASS[kindTone(e.kind)]]"
          :title="e.kind"
          aria-hidden="true"
          >{{ KIND_GLYPH[e.kind] ?? '•' }}</span
        >
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-slate-400">
            <span class="font-mono font-semibold text-slate-200 tabular-nums" data-testid="event-relative">{{ relative(e) }}</span>
            <span class="tabular-nums">{{ fmtTimeOfDay(e.t) }} UTC</span>
            <Badge :tone="kindTone(e.kind)">{{ e.kind }}</Badge>
            <span class="text-slate-500">{{ e.source.replace(/_/g, ' ') }}</span>
            <Badge v-if="e.attribution" :tone="attributionTone(e.attribution)" dot data-testid="event-attribution">{{ e.attribution }}</Badge>
          </div>
          <p class="break-words text-sm text-slate-200" data-testid="event-message">{{ e.message }}</p>
        </div>
      </li>
    </ol>
  </div>
</template>
