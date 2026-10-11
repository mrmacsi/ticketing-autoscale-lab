<script setup lang="ts">
import { computed, provide } from 'vue';
import Card from '@/components/ui/Card.vue';
import ExperimentConfigForm from '@/components/config/ExperimentConfigForm.vue';
import ExperimentControls from '@/components/controls/ExperimentControls.vue';
import DestroyLabPanel from '@/components/teardown/DestroyLabPanel.vue';
import FreshnessPanel from '@/components/live/FreshnessPanel.vue';
import ServerSummary from '@/components/live/ServerSummary.vue';
import InstanceGrid from '@/components/live/InstanceGrid.vue';
import ArchitectureDiagram from '@/components/live/ArchitectureDiagram.vue';
import TotalsStrip from '@/components/live/TotalsStrip.vue';
import WaitingRoomPanel from '@/components/live/WaitingRoomPanel.vue';
import ChartGrid from '@/components/charts/ChartGrid.vue';
import EventTimeline from '@/components/timeline/EventTimeline.vue';
import { createExperimentDraft, DRAFT } from '@/store/draft';
import { useLab } from '@/store/lab';

const store = useLab();
const draft = createExperimentDraft(store);
provide(DRAFT, draft);

const snap = computed(() => store.snapshot.value);
const freshness = computed(() => snap.value?.freshness ?? null);
const nowMs = computed(() => store.clock.nowMs + store.clock.offsetMs);
const trafficUsable = computed(() => freshness.value?.traffic.status === 'live' || freshness.value?.traffic.status === 'delayed');
const roomConfig = computed(() => store.experiment.value?.config.waitingRoom ?? null);
// Shown when the queue server exists, has been observed, or the current experiment uses it.
const showRoom = computed(() => store.meta.value?.waitingRoom?.deployed === true || !!snap.value?.waitingRoom || roomConfig.value?.enabled === true);
const cpuTarget = computed(() => snap.value?.cpu.target ?? snap.value?.mig?.autoscaler.cpuTarget ?? store.experiment.value?.config.servers.cpuTarget ?? null);
</script>

<template>
  <main class="mx-auto max-w-[1800px] space-y-3 px-3 py-3 sm:px-4">
    <p v-if="store.status.stateError && !snap" class="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-200" role="alert">
      Could not load live state: {{ store.status.stateError }}
    </p>
    <FreshnessPanel :freshness="freshness" :now-ms="nowMs" />
    <ArchitectureDiagram
      :snapshot="snap ?? null" :meta="store.meta.value" :now-ms="nowMs"
      :latest-event="store.timeline.value.at(-1) ?? null"
      :traffic-started-at="store.experiment.value?.trafficStartedAt ?? null"
      :created-at="store.experiment.value?.createdAt ?? null"
    />

    <div class="grid grid-cols-1 gap-3 xl:grid-cols-12">
      <div class="min-w-0 space-y-3 xl:col-span-4 2xl:col-span-3">
        <ExperimentControls />
        <ExperimentConfigForm />
        <DestroyLabPanel />
      </div>

      <div class="min-w-0 space-y-3 xl:col-span-8 2xl:col-span-9">
        <Card title="Live servers" tag="C" subtitle="Instances as reported by the managed instance group — never inferred from CPU or load.">
          <div class="space-y-3">
            <ServerSummary
              :mig="snap?.mig ?? null"
              :instance-cards="snap?.instances.length ?? 0"
              :freshness="freshness?.mig ?? null"
              :approved-max-fallback="store.meta.value?.approvedMaxInstances ?? null"
            />
            <InstanceGrid
              :instances="snap?.instances ?? []"
              :traffic-freshness="freshness?.traffic ?? null"
              :cpu-freshness="freshness?.cpu ?? null"
              :now-ms="nowMs"
            />
          </div>
        </Card>

        <Card v-if="showRoom" title="Waiting room" subtitle="Queue server in front of checkout: customers beyond what the healthy servers can take wait here in FIFO order.">
          <WaitingRoomPanel
            :room="snap?.waitingRoom ?? null"
            :freshness="freshness?.waitingRoom ?? null"
            :event-id="store.experiment.value?.eventId ?? null"
            :configured="roomConfig"
            :now-ms="nowMs"
          />
        </Card>

        <Card title="Real-time metrics" tag="D">
          <template #actions>
            <span v-if="store.status.seriesLoading" class="text-[11px] text-slate-400">loading history…</span>
            <span v-else-if="store.status.seriesError" class="text-[11px] text-red-300">series: {{ store.status.seriesError }}</span>
          </template>
          <div class="space-y-3">
            <TotalsStrip :points="store.series.traffic" :live="snap?.traffic ?? null" :inventory="snap?.inventory ?? null" :traffic-usable="trafficUsable" />
            <ChartGrid
              :traffic="store.series.traffic"
              :scaling="store.series.scaling"
              :cpu="store.series.cpu"
              :inventory="store.series.inventory"
              :waiting-room="store.series.waitingRoom"
              :show-waiting-room="roomConfig?.enabled === true"
              :show-adaptive-limits="roomConfig?.enabled === true && roomConfig?.mode === 'adaptive'"
              :events="store.timeline.value"
              :cpu-target="cpuTarget"
              :approved-max="snap?.mig?.approvedMaxInstances ?? store.meta.value?.approvedMaxInstances ?? null"
              :freshness="freshness"
            />
          </div>
        </Card>

        <Card title="Live event timeline" tag="E">
          <EventTimeline
            :events="store.timeline.value"
            :traffic-started-at="store.experiment.value?.trafficStartedAt ?? null"
            :created-at="store.experiment.value?.createdAt ?? null"
          />
        </Card>
      </div>
    </div>
  </main>
</template>
