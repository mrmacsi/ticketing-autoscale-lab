<script setup lang="ts">
import { computed } from 'vue';
import Stat from '@/components/ui/Stat.vue';
import { trafficTotals } from '@/charts/options';
import { fmtMs, fmtNumber, fmtRate } from '@/lib/format';
import type { InventoryView, LiveTraffic, TrafficPoint } from '@/types/contracts';

const props = defineProps<{
  points: readonly TrafficPoint[];
  live: LiveTraffic | null;
  inventory: InventoryView | null;
  trafficUsable: boolean;
}>();

const t = computed(() => (props.points.length ? trafficTotals(props.points) : null));
const n = (v: number | undefined) => fmtNumber(v ?? null);
const liveVal = (v: number | null | undefined, fmt: (x: number | null) => string = fmtRate) => (props.trafficUsable ? fmt(v ?? null) : 'N/A');
</script>

<template>
  <div class="space-y-2">
    <div class="grid grid-cols-2 gap-2 min-[480px]:grid-cols-3 md:grid-cols-6">
      <Stat label="Requested arrivals/s" :value="liveVal(live?.targetRate)" testid="live-target-rate" hint="configured target" />
      <Stat label="Achieved arrivals/s" :value="liveVal(live?.achievedArrivalRate)" testid="live-achieved" />
      <Stat label="HTTP req/s" :value="liveVal(live?.rps)" testid="live-rps" />
      <Stat label="Active VUs" :value="liveVal(live?.activeVUs, (x) => fmtNumber(x))" testid="live-vus" hint="in-flight journeys" />
      <Stat label="p95 latency" :value="liveVal(live?.p95, fmtMs)" testid="live-p95" />
      <Stat label="Generator" :value="live?.generator.state ?? 'N/A'" testid="live-generator" :hint="live?.generator.heartbeatAgeSeconds != null ? `heartbeat ${live.generator.heartbeatAgeSeconds.toFixed(0)}s ago` : 'heartbeat N/A'" />
    </div>
    <div class="grid grid-cols-2 gap-2 min-[480px]:grid-cols-3 md:grid-cols-6">
      <Stat label="HTTP requests" :value="n(t?.httpRequests)" testid="total-requests" hint="total this run" />
      <Stat label="Successful" :value="n(t?.successful)" testid="total-ok" />
      <Stat label="HTTP errors" :value="n(t?.errors)" testid="total-errors" hint="5xx / network / timeout" />
      <Stat label="Business rejections" :value="n(t?.business)" testid="total-business" hint="409/410/402 — not errors" />
      <Stat label="Arrivals started" :value="n(t?.arrivals)" testid="total-arrivals" :hint="t ? `of ${fmtNumber(t.requested)} requested` : undefined" />
      <Stat label="Dropped iterations" :value="n(t?.dropped)" testid="total-dropped" hint="generator saturation" />
      <Stat label="Purchases completed" :value="n(t?.purchasesCompleted)" testid="total-purchases" />
      <Stat label="Tickets sold" :value="fmtNumber(inventory?.sold)" testid="tickets-sold" hint="database" />
      <Stat label="Available" :value="fmtNumber(inventory?.available)" testid="tickets-available" :hint="inventory ? `of ${fmtNumber(inventory.capacity)}` : undefined" />
      <Stat label="Held" :value="fmtNumber(inventory?.held)" testid="tickets-held" hint="active reservations" />
      <Stat label="Expired reservations" :value="fmtNumber(inventory?.expiredReservations)" testid="tickets-expired" />
      <Stat
        label="Invariant violations"
        :value="fmtNumber(inventory?.invariantViolations)"
        testid="invariant-violations"
        :hint="inventory == null ? undefined : inventory.invariantViolations === 0 ? 'none — consistent' : 'CHECK INVENTORY'"
      />
    </div>
  </div>
</template>
