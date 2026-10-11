<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import { init, use, type ECharts } from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { DataZoomComponent, GridComponent, LegendComponent, MarkLineComponent, MarkPointComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { ChartOption } from '@/charts/theme';

use([LineChart, GridComponent, TooltipComponent, LegendComponent, MarkLineComponent, MarkPointComponent, DataZoomComponent, CanvasRenderer]);

const props = withDefaults(defineProps<{ option: ChartOption; height?: string; label: string }>(), { height: '260px' });

const el = ref<HTMLDivElement | null>(null);
const chart = shallowRef<ECharts | null>(null);
let observer: ResizeObserver | null = null;

onMounted(() => {
  if (!el.value) return;
  chart.value = init(el.value, undefined, { renderer: 'canvas' });
  // Legend selection is preserved because we merge (notMerge=false) and only replace series data.
  chart.value.setOption(props.option);
  if (typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(() => chart.value?.resize());
    observer.observe(el.value);
  }
});

watch(
  () => props.option,
  (opt) => chart.value?.setOption(opt, { replaceMerge: ['series'], lazyUpdate: true }),
);

onBeforeUnmount(() => {
  observer?.disconnect();
  chart.value?.dispose();
});
</script>

<template>
  <div ref="el" role="img" :aria-label="label" class="w-full" :style="{ height }" />
</template>
