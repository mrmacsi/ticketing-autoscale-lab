<script setup lang="ts">
import { computed } from 'vue';
import ChartCard from './ChartCard.vue';
import {
  adaptiveLimitsOption,
  cpuOption,
  hasAdaptiveLimits,
  inventoryOption,
  latencyOption,
  outcomesOption,
  scalingOption,
  trafficOption,
  waitingRoomOption,
} from '@/charts/options';
import { fmtDateTime } from '@/lib/format';
import type { CpuPoint, InventoryPoint, LiveSnapshot, ScalingPoint, TimelineEvent, TrafficPoint, WaitingRoomPoint } from '@/types/contracts';

const props = defineProps<{
  traffic: readonly TrafficPoint[];
  scaling: readonly ScalingPoint[];
  cpu: readonly CpuPoint[];
  inventory: readonly InventoryPoint[];
  events: readonly TimelineEvent[];
  cpuTarget: number | null;
  approvedMax?: number | null;
  /** Live mode passes freshness so charts can flag stale sources; report mode omits it. */
  freshness?: LiveSnapshot['freshness'] | null;
  /** Queue-server observations; the chart appears when there are some or `showWaitingRoom` is set. */
  waitingRoom?: readonly WaitingRoomPoint[];
  showWaitingRoom?: boolean;
  /** The experiment uses adaptive admission: show the learned-limits chart even before data arrives. */
  showAdaptiveLimits?: boolean;
}>();

const traffic = computed(() => trafficOption(props.traffic));
const scaling = computed(() => scalingOption({ points: props.scaling, events: props.events, approvedMax: props.approvedMax ?? null }));
const cpu = computed(() => cpuOption(props.cpu, props.cpuTarget));
const latency = computed(() => latencyOption(props.traffic));
const inventory = computed(() => inventoryOption(props.inventory));
const outcomes = computed(() => outcomesOption(props.traffic));
const roomPoints = computed(() => props.waitingRoom ?? []);
const room = computed(() => waitingRoomOption(roomPoints.value));
const showRoom = computed(() => props.showWaitingRoom === true || roomPoints.value.length > 0);
const limitsData = computed(() => hasAdaptiveLimits(roomPoints.value));
const limits = computed(() => adaptiveLimitsOption(roomPoints.value));
const showLimits = computed(() => props.showAdaptiveLimits === true || limitsData.value);

const latestCpuSample = computed(() => props.cpu.reduce<number | null>((m, p) => (m === null || p.t > m ? p.t : m), null));

// An older control API sends no waiting-room freshness: treat it as missing, not as fresh.
const f = (k: keyof LiveSnapshot['freshness']) => (props.freshness === undefined ? undefined : (props.freshness?.[k] ?? null));
</script>

<template>
  <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
    <ChartCard title="Incoming traffic" :option="traffic" :has-data="props.traffic.length > 0" :freshness="f('traffic')">
      <template #note>Arrivals = customer journeys started; HTTP req/s counts every request those journeys made.</template>
    </ChartCard>
    <ChartCard title="Server scaling" :option="scaling" :has-data="props.scaling.length > 0" :freshness="f('mig')">
      <template #note>Vertical markers = scaling / instance events from the timeline, labelled by attribution.</template>
    </ChartCard>
    <ChartCard title="CPU utilisation (Cloud Monitoring)" :option="cpu" :has-data="props.cpu.length > 0" :freshness="f('cpu')">
      <template #note>
        <span class="text-amber-200/90">Cloud Monitoring samples are delayed ~1–4 min.</span>
        x = sample time. Latest sample: <span class="tabular-nums" data-testid="cpu-latest-sample">{{ fmtDateTime(latestCpuSample) }}</span>
      </template>
    </ChartCard>
    <ChartCard title="API latency (generator-observed)" :option="latency" :has-data="props.traffic.some((p) => p.p50 != null)" :freshness="f('traffic')" />
    <ChartCard title="Ticket inventory (database)" :option="inventory" :has-data="props.inventory.length > 0" :freshness="f('inventory')" />
    <ChartCard title="Outcomes" :option="outcomes" :has-data="props.traffic.length > 0" :freshness="f('traffic')">
      <template #note>Business rejections (409 sold out / 410 expired / 402 declined) are expected outcomes, not errors.</template>
    </ChartCard>
    <ChartCard v-if="showRoom" title="Waiting room" :option="room" :has-data="roomPoints.length > 0" :freshness="f('waitingRoom')">
      <template #note>Queue server observations: customers waiting vs. admitted and in checkout vs. capacity (healthy servers × active users per server, or the sum of learned limits in adaptive mode).</template>
    </ChartCard>
    <ChartCard v-if="showLimits" title="Adaptive limits" :option="limits" :has-data="limitsData" :freshness="f('waitingRoom')">
      <template #note>Each server's learned limit (left axis), from PHP workers busy, listen queue and VM CPU; room capacity = sum of the healthy servers' limits (right axis).</template>
    </ChartCard>
  </div>
</template>
