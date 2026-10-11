<script setup lang="ts">
import { computed, useId } from 'vue';
import Badge from '@/components/ui/Badge.vue';
import { ageSeconds, eventOffset, fmtFractionPct, fmtNumber, fmtRate, fmtTimeOfDay } from '@/lib/format';
import { KIND_GLYPH, kindTone, TONE_CLASS } from '@/lib/tones';
import type { InstanceView, LiveSnapshot, Meta, TimelineEvent } from '@/types/contracts';

const props = withDefaults(defineProps<{
  snapshot: LiveSnapshot | null; meta: Meta | null; nowMs: number;
  /** Newest timeline event, shown as the current step and outlined on the node it concerns. */
  latestEvent?: TimelineEvent | null; trafficStartedAt?: string | null; createdAt?: string | null;
}>(), { latestEvent: null, trafficStartedAt: null, createdAt: null });

/** How long the node an event concerns stays outlined. */
const FOCUS_SECONDS = 20;
const markerId = `flow-${useId().replace(/[^a-zA-Z0-9-]/g, '')}`;
const s = computed(() => props.snapshot);
const local = computed(() => /not configured/i.test(s.value?.freshness.mig.note ?? ''));
const instances = computed(() => [...(s.value?.instances ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
const room = computed(() => s.value?.waitingRoom ?? null);
/** The queue server is drawn when it is deployed or has been observed. */
const showRoom = computed(() => props.meta?.waitingRoom?.deployed === true || room.value !== null);
const roomUsable = computed(() => !snapshotStale.value && ['live', 'delayed'].includes(s.value?.freshness.waitingRoom?.status ?? ''));
const roomFlowing = computed(() => roomUsable.value && room.value?.enabled === true && (room.value.waiting > 0 || room.value.active > 0));
const roomLimit = computed(() => {
  const r = room.value;
  if (!r) return null;
  return r.admissionMode === 'adaptive' ? (r.servers[0]?.limit ?? null) : r.activeUsersPerServer;
});
const roomState = computed(() => {
  const r = room.value;
  if (!r) return s.value?.freshness.waitingRoom?.status ?? 'missing';
  if (!r.enabled) return 'off';
  return r.soldOut ? 'sold out' : (r.admissionMode ?? 'fixed');
});
// The queue node sits under the load balancer: 80 px below the centre line, 104 px tall.
const ROOM_OFFSET = 80;
const ROOM_HEIGHT = 104;
const bodyHeight = computed(() => Math.max(showRoom.value ? 2 * (ROOM_OFFSET + ROOM_HEIGHT + 8) : 240, instances.value.length * 108 + 80));
const center = computed(() => bodyHeight.value / 2);
const controlY = computed(() => bodyHeight.value + 28);
const snapshotStale = computed(() => (ageSeconds(s.value?.serverTime, props.nowMs) ?? Infinity) > 15);
const trafficUsable = computed(() => !snapshotStale.value && ['live', 'delayed'].includes(s.value?.freshness.traffic.status ?? ''));
const routingLive = computed(() => !snapshotStale.value && !local.value && s.value?.freshness.traffic.status === 'live' && s.value?.freshness.mig.status === 'live' && s.value?.freshness.lb.status === 'live');
const flowing = computed(() => routingLive.value && (s.value?.traffic.rps ?? 0) > 0);
const flowStatus = computed(() => {
  if (local.value) return 'Local mode · cloud topology unavailable';
  if (!s.value) return 'Waiting for telemetry';
  if (snapshotStale.value) return 'Flow paused · snapshot stale';
  if (flowing.value) return 'Live HTTP traffic';
  if (s.value.traffic.generator.state === 'idle' && s.value.freshness.traffic.status === 'missing') return 'Idle · no test traffic';
  if (!routingLive.value) return 'Flow paused · ' + (['traffic', 'mig', 'lb'] as const)
    .filter((source) => s.value!.freshness[source].status !== 'live')
    .map((source) => `${source.toUpperCase()} ${s.value!.freshness[source].status}`).join(' / ');
  return 'Idle · 0 requests/s';
});
const rowY = (index: number) => center.value + (index - (instances.value.length - 1) / 2) * 108;
const backendPath = (index: number) => `M 406 ${center.value} H 430 Q 450 ${center.value} 450 ${rowY(index)} H 480`;
const databasePath = (index: number) => `M 726 ${rowY(index)} H 770 Q 800 ${rowY(index)} 800 ${center.value} H 838`;
function activeBackend(instance: InstanceView) {
  return routingLive.value && instance.traffic.rps > 0 && !['starting', 'terminating', 'unknown'].includes(instance.phase);
}
function cpu(instance: InstanceView) {
  const age = ageSeconds(instance.cpu.sampleTime, props.nowMs);
  return age != null && age <= 300 && ['live', 'delayed'].includes(s.value?.freshness.cpu.status ?? '')
    ? fmtFractionPct(instance.cpu.value, 0) : 'N/A';
}
function shortName(name: string) {
  return name.length > 28 ? '…' + name.slice(-27) : name;
}
const latestAge = computed(() => (props.latestEvent ? ageSeconds(props.latestEvent.t, props.nowMs) : null));
const latestOffset = computed(() => (props.latestEvent ? eventOffset(props.latestEvent.t, props.trafficStartedAt, props.createdAt) : null));
/** Which node the newest event is about, while it is recent: a VM name, or one of the fixed nodes. */
const focus = computed<string | null>(() => {
  const e = props.latestEvent;
  if (!e || (latestAge.value ?? Infinity) > FOCUS_SECONDS) return null;
  const instance = e.data?.instance ?? e.data?.server;
  if (typeof instance === 'string' && instances.value.some((i) => i.name === instance)) return instance;
  if (e.kind === 'waiting_room' || e.source === 'waiting_room') return 'queue';
  if (e.kind === 'scaling') return 'autoscaler';
  if (e.source === 'load_balancer') return 'load-balancer';
  if (e.kind === 'inventory' || e.source === 'database') return 'database';
  if (e.source === 'generator' || e.kind === 'traffic') return 'generator';
  return null;
});
const inventoryUsable = computed(() => !snapshotStale.value && ['live', 'delayed'].includes(s.value?.freshness.inventory.status ?? ''));
</script>

<template>
  <div class="architecture rounded-lg border border-slate-800 bg-slate-950/70" data-testid="architecture-diagram">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-slate-800 px-3 py-2">
      <div>
        <h3 class="text-sm font-semibold text-slate-100">Live architecture</h3>
        <p class="text-[11px] text-slate-400">One node per observed VM · branches update automatically</p>
      </div>
      <span class="flex items-center gap-2 text-xs" :class="flowing ? 'text-emerald-300' : 'text-slate-400'" role="status" data-testid="flow-status">
        <span class="h-2 w-2 rounded-full" :class="flowing ? 'bg-emerald-400' : 'bg-slate-500'" />{{ flowStatus }}
      </span>
    </div>
    <div v-if="latestEvent" :key="latestEvent.id" class="latest-step flex items-start gap-2 border-b border-slate-800 px-3 py-2" role="status" aria-live="polite" data-testid="latest-step">
      <span :class="['mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded text-xs font-bold ring-1 ring-inset', TONE_CLASS[kindTone(latestEvent.kind)]]" aria-hidden="true">{{ KIND_GLYPH[latestEvent.kind] ?? '•' }}</span>
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-x-2 text-[11px] text-slate-400">
          <span class="font-semibold uppercase tracking-wide text-slate-300">Latest step</span>
          <span v-if="latestOffset" class="font-mono tabular-nums text-slate-200" :title="latestOffset.label">{{ latestOffset.text }}</span>
          <span class="tabular-nums">{{ fmtTimeOfDay(latestEvent.t) }} UTC</span>
          <span v-if="latestAge != null" class="tabular-nums" data-testid="latest-step-age">{{ Math.round(latestAge) }} s ago</span>
          <Badge :tone="kindTone(latestEvent.kind)">{{ latestEvent.kind }}</Badge>
        </div>
        <p class="break-words text-sm text-slate-100" data-testid="latest-step-message">{{ latestEvent.message }}</p>
      </div>
    </div>
    <div v-if="local" class="px-3 py-6 text-sm text-slate-400" data-testid="local-topology">
      Cloud load balancer, managed instance group, and autoscaler are not configured in local mode. Use the cloud dashboard to observe the deployed architecture.
    </div>
    <template v-else>
      <div class="overflow-x-auto p-2" tabindex="0" aria-label="Architecture diagram; scroll horizontally on small screens">
        <svg class="block min-w-[820px] w-full" :viewBox="`0 0 1100 ${bodyHeight + 138}`" role="img" aria-label="Traffic flows from the load generator through the load balancer to the waiting room (/queue/*) and to application VMs and Cloud SQL. The autoscaler manages the VM group; Cloud Run uses Firestore for experiment control.">
          <defs>
            <marker :id="markerId" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="context-stroke" /></marker>
          </defs>
          <rect x="466" y="16" width="274" :height="bodyHeight - 24" rx="14" fill="#0f172a" stroke="#334155" stroke-dasharray="5 5" />
          <text x="480" y="38" class="eyebrow">MANAGED INSTANCE GROUP · {{ snapshotStale ? 'stale snapshot' : s?.freshness.mig.status ?? 'missing' }}</text>
          <text x="480" :y="bodyHeight - 22" class="muted">{{ instances.length }} listed · actual {{ fmtNumber(s?.mig?.actualCount) }} · target {{ fmtNumber(s?.mig?.targetSize) }}</text>

          <path :d="`M 190 ${center} H 236`" class="http-route" :class="{ flowing }" :marker-end="`url(#${markerId})`" data-testid="generator-route" />
          <g v-for="(instance, index) in instances" :key="instance.name">
            <path :d="backendPath(index)" class="http-route" :class="{ flowing: activeBackend(instance) }" :marker-end="`url(#${markerId})`" :data-backend="instance.name" />
            <path :d="databasePath(index)" class="data-route" :marker-end="`url(#${markerId})`" />
          </g>

          <g :transform="`translate(20 ${center - 56})`" :class="{ 'event-focus': focus === 'generator' }" data-node="generator">
            <rect width="170" height="112" rx="12" class="node" />
            <text x="14" y="23" class="eyebrow">TRAFFIC SOURCE</text>
            <text x="14" y="48" class="heading">Load generator</text>
            <text x="14" y="70" class="muted">{{ s?.traffic.generator.state ?? 'unknown' }} · dedicated VM</text>
            <text x="14" y="94" class="value">{{ trafficUsable ? fmtRate(s?.traffic.rps) : 'N/A' }} HTTP req/s</text>
          </g>
          <template v-if="showRoom">
            <path :d="`M 321 ${center + 56} V ${center + ROOM_OFFSET - 2}`" class="http-route" :class="{ flowing: roomFlowing }" :marker-end="`url(#${markerId})`" data-testid="queue-route" />
            <text x="328" :y="center + 72" class="muted">/queue/*</text>
            <path :d="`M 406 ${center + ROOM_OFFSET + 52} H 464`" class="control-route" :marker-end="`url(#${markerId})`" />
            <g :transform="`translate(236 ${center + ROOM_OFFSET})`" :class="{ 'event-focus': focus === 'queue' }" data-node="queue" data-testid="diagram-queue">
              <title>Waiting room: admits customers while active &lt; healthy servers × limit; the load balancer picks their server</title>
              <rect width="170" :height="ROOM_HEIGHT" rx="12" class="node amber-node" />
              <text x="14" y="23" class="eyebrow">WAITING ROOM · {{ roomState.toUpperCase() }}</text>
              <text x="14" y="46" class="heading">Queue server</text>
              <text x="14" y="68" class="value">{{ roomUsable ? fmtNumber(room?.waiting) : 'N/A' }} waiting</text>
              <text x="14" y="89" class="muted">{{ roomUsable ? `${fmtNumber(room?.active)}/${fmtNumber(room?.capacity)} in checkout · ${fmtNumber(roomLimit)}/VM` : 'N/A' }}</text>
            </g>
          </template>
          <g :transform="`translate(236 ${center - 56})`" :class="{ 'event-focus': focus === 'load-balancer' }" data-node="load-balancer">
            <rect width="170" height="112" rx="12" class="node blue-node" />
            <text x="14" y="23" class="eyebrow">REQUEST ROUTING</text>
            <text x="14" y="48" class="heading">Load balancer</text>
            <text x="14" y="70" class="muted">External HTTP · {{ s?.freshness.lb.status ?? 'missing' }}</text>
            <text x="14" y="94" class="muted">{{ fmtNumber(s?.mig?.healthyCount) }} MIG healthy</text>
            <title>{{ meta?.target.baseUrl ?? 'Load balancer address unavailable' }}; LB telemetry {{ s?.freshness.lb.status ?? 'missing' }}</title>
          </g>

          <g v-for="(instance, index) in instances" :key="instance.name" :transform="`translate(480 ${rowY(index) - 42})`" data-testid="diagram-server" :data-instance="instance.name" :class="['server', instance.phase, { outdated: snapshotStale || s?.freshness.mig.status !== 'live', 'event-focus': focus === instance.name }]">
            <title>{{ instance.name }} · {{ instance.phase }} · {{ instance.privateIp ?? 'IP unavailable' }} · LB {{ instance.lbHealth ?? 'unknown' }}</title>
            <rect width="246" height="84" rx="10" class="node" />
            <circle cx="15" cy="19" r="4" class="phase-dot" />
            <text x="27" y="23" class="eyebrow">VM {{ index + 1 }} · {{ instance.phase }}</text>
            <text x="13" y="44" class="server-name">{{ shortName(instance.name) }}</text>
            <text x="13" y="65" class="muted">{{ trafficUsable ? fmtRate(instance.traffic.rps) : 'N/A' }} req/s · CPU {{ cpu(instance) }} · LB {{ instance.lbHealth ?? 'N/A' }}</text>
          </g>
          <text v-if="!instances.length" x="490" :y="center" class="muted" data-testid="diagram-no-servers">No VMs reported</text>

          <g :transform="`translate(838 ${center - 68})`" :class="{ 'event-focus': focus === 'database' }" data-node="database">
            <rect width="242" height="136" rx="12" class="node purple-node" />
            <text x="14" y="23" class="eyebrow">DATABASE · PRIVATE PSC / TLS</text>
            <text x="14" y="48" class="heading">Cloud SQL · PostgreSQL</text>
            <text x="14" y="70" class="muted">Ticket inventory · {{ s?.freshness.inventory.status ?? 'missing' }}</text>
            <text x="14" y="94" class="value">{{ inventoryUsable ? fmtNumber(s?.inventory?.sold) : 'N/A' }} sold · {{ inventoryUsable ? fmtNumber(s?.inventory?.held) : 'N/A' }} held</text>
            <text x="14" y="117" class="muted">DB query rate / CPU not measured</text>
          </g>

          <path :d="`M 240 ${controlY + 44} H 280`" class="control-route" :marker-end="`url(#${markerId})`" />
          <path :d="`M 603 ${controlY} V ${bodyHeight - 8}`" class="control-route" :marker-end="`url(#${markerId})`" />
          <g :transform="`translate(20 ${controlY})`">
            <rect width="220" height="88" rx="10" class="node" />
            <text x="14" y="23" class="eyebrow">CONTROL PLANE</text>
            <text x="14" y="47" class="heading">Cloud Run · dashboard</text>
            <text x="14" y="69" class="muted">Experiment control + telemetry</text>
          </g>
          <g :transform="`translate(280 ${controlY})`">
            <rect width="170" height="88" rx="10" class="node" />
            <text x="14" y="23" class="eyebrow">COMMAND BUS / HISTORY</text>
            <text x="14" y="47" class="heading">Firestore</text>
            <text x="14" y="69" class="muted">Generator commands / metrics</text>
          </g>
          <g :transform="`translate(480 ${controlY})`" :class="{ 'event-focus': focus === 'autoscaler' }" data-node="autoscaler">
            <rect width="246" height="88" rx="10" class="node blue-node" />
            <text x="14" y="23" class="eyebrow">AUTOSCALER · {{ s?.mig?.autoscaler.status ?? 'unknown' }}</text>
            <text x="14" y="47" class="heading">{{ fmtNumber(s?.mig?.autoscaler.minReplicas) }}–{{ fmtNumber(s?.mig?.autoscaler.maxReplicas) }} VMs · CPU {{ fmtFractionPct(s?.mig?.autoscaler.cpuTarget, 0) }}</text>
            <text x="14" y="69" class="muted">Recommended {{ fmtNumber(s?.mig?.autoscaler.recommendedSize) }} · {{ s?.mig?.isStable ? 'group stable' : 'group changing / unknown' }}</text>
          </g>
          <text x="838" :y="controlY + 33" class="muted">{{ meta?.target.region ?? 'Region unavailable' }}</text>
          <text x="838" :y="controlY + 55" class="muted">{{ s?.mig?.zone ?? meta?.target.zone ?? 'Zone unavailable' }}</text>
        </svg>
      </div>
      <div class="flex flex-wrap gap-x-5 gap-y-1 border-t border-slate-800 px-3 py-2 text-[11px] text-slate-400">
        <span><span class="mr-1 text-emerald-400">→</span>HTTP requests · animation follows observed traffic</span>
        <span><span class="mr-1 text-violet-400">⇢</span>Database access · configured path</span>
        <span><span class="mr-1 text-sky-400">⇢</span>Control / scaling / admission · configured path</span>
        <span>CPU = Cloud Monitoring, sampled with delay</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.node { fill: #111c30; stroke: #334155; stroke-width: 1.3; }
.blue-node { stroke: #38bdf8; }
.purple-node { stroke: #a78bfa; }
.amber-node { stroke: #f59e0b; }
text { font-family: inherit; fill: #e2e8f0; }
.heading { font-size: 16px; font-weight: 600; }
.eyebrow { font-size: 10px; letter-spacing: .65px; fill: #94a3b8; }
.muted { font-size: 11px; fill: #94a3b8; }
.value { font-size: 13px; font-weight: 600; }
.server-name { font-size: 12px; font-family: ui-monospace, monospace; }
.http-route, .data-route, .control-route { fill: none; stroke-width: 1.6; }
.http-route { stroke: #475569; }
.http-route.flowing { stroke: #34d399; stroke-dasharray: 7 7; animation: flow .8s linear infinite; }
.data-route { stroke: #a78bfa; stroke-dasharray: 4 5; opacity: .55; }
.control-route { stroke: #38bdf8; stroke-dasharray: 4 5; opacity: .65; }
.phase-dot { fill: #64748b; }
.server.healthy .node { stroke: #34d399; }
.server.healthy .phase-dot { fill: #34d399; }
.server.starting .node, .server.draining .node { stroke: #fbbf24; }
.server.starting .phase-dot, .server.draining .phase-dot { fill: #fbbf24; }
.server.unhealthy .node, .server.terminating .node { stroke: #fb7185; }
.server.unhealthy .phase-dot, .server.terminating .phase-dot { fill: #fb7185; }
.server.outdated { opacity: .55; }
.event-focus .node, .server.event-focus .node { stroke: #fbbf24; stroke-width: 3; }
.latest-step { animation: arrive 1.6s ease-out; }
@keyframes flow { to { stroke-dashoffset: -28; } }
@keyframes arrive { from { background-color: rgba(251, 191, 36, .18); } to { background-color: transparent; } }
@media (prefers-reduced-motion: reduce) { .http-route.flowing, .latest-step { animation: none; } }
</style>
