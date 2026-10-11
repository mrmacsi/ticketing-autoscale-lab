<script setup lang="ts">
import { computed, useId } from 'vue';
import NumField from './NumField.vue';
import Field from '@/components/ui/Field.vue';
import { useDraft } from '@/store/draft';
import { DEFAULT_WAITING_ROOM, type AdmissionMode } from '@/types/contracts';

const props = defineProps<{ deployed: boolean | null }>();
const draft = useDraft();
const c = computed(() => draft.config.value!);
// cloneConfig always fills this in; this only guards a draft set from elsewhere.
if (!c.value.waitingRoom) c.value.waitingRoom = { ...DEFAULT_WAITING_ROOM };
const wr = computed(() => c.value.waitingRoom ?? { ...DEFAULT_WAITING_ROOM });
const err = draft.fieldError;
const toggleId = useId();
const descriptionId = `${toggleId}-description`;
const notDeployed = computed(() => props.deployed === false);
const modeId = useId();
const adaptive = computed(() => wr.value.mode === 'adaptive');

const MODES: Array<{ value: AdmissionMode; label: string }> = [
  { value: 'fixed', label: 'Fixed' },
  { value: 'adaptive', label: 'Adaptive' },
];

const ADAPTIVE_HELP = "Adaptive: each server's limit is learned from PHP workers busy, request queue and VM CPU; the number below is the starting limit.";
const MODE_HELP = {
  fixed: `Fixed: every healthy server takes the same number of customers in checkout. ${ADAPTIVE_HELP}`,
  adaptive: `${ADAPTIVE_HELP} Room capacity is the sum of the healthy servers' limits.`,
};
const PER_SERVER_HELP = {
  fixed:
    'How many admitted customers may be in checkout at once for each healthy server. Capacity = healthy servers × this value; 25 keeps one n1-standard-1 busy enough to scale without starving its health check.',
  adaptive:
    'How many admitted customers a server may have in checkout when it first turns healthy. From there the queue raises the limit while the server has headroom and cuts it as soon as the server is overloaded.',
};

function setMode(mode: AdmissionMode) {
  wr.value.mode = mode;
  // The maximum is only edited (and validated here) in adaptive mode; never leave a hidden value the API would refuse.
  const max = wr.value.maxUsersPerServer;
  if (mode === 'fixed' && !(Number.isInteger(max) && (max as number) >= 1 && (max as number) <= 1000)) {
    wr.value.maxUsersPerServer = DEFAULT_WAITING_ROOM.maxUsersPerServer;
  }
}
</script>

<template>
  <fieldset class="min-w-0 space-y-2" data-testid="waiting-room-section">
    <legend class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-400">Waiting room</legend>
    <div class="flex flex-wrap items-start gap-x-3 gap-y-1">
      <label :for="toggleId" class="flex flex-1 basis-[110px] items-center gap-2 text-sm text-slate-100">
        <input
          :id="toggleId"
          v-model="wr.enabled"
          type="checkbox"
          class="h-4 w-4 accent-sky-500"
          :aria-describedby="descriptionId"
          :aria-invalid="!!err('waitingRoom.enabled')"
          data-testid="waiting-room-enabled"
        />
        Use the waiting room
      </label>
      <p :id="descriptionId" class="min-w-0 flex-[1.4_1_140px] text-xs leading-relaxed text-slate-400" data-testid="field-description">
        Customers who arrive while the servers are full wait in a first-come, first-served queue instead of overloading them.
        They are let into checkout only as fast as the servers the load balancer reports healthy can take them; when the
        autoscaler adds a server the queue drains faster. When tickets run out, everyone still waiting is told “sold out”.
        Applies to reserve and purchase journeys.
      </p>
    </div>
    <p v-if="err('waitingRoom.enabled')" class="text-xs text-red-300" role="alert" data-testid="waiting-room-error">{{ err('waitingRoom.enabled') }}</p>
    <p v-else-if="notDeployed" class="text-xs text-slate-500" data-testid="waiting-room-not-deployed">The queue server is not deployed in this lab, so the waiting room cannot be turned on.</p>
    <div v-if="wr.enabled" class="grid grid-cols-1 gap-3 @md:grid-cols-2">
      <Field
        v-slot="{ describedBy }"
        class="@md:col-span-2"
        label="Admission mode"
        :description="adaptive ? MODE_HELP.adaptive : MODE_HELP.fixed"
        :error="err('waitingRoom.mode')"
        :for="modeId"
      >
        <select
          :id="modeId"
          :aria-describedby="describedBy"
          class="input"
          :value="wr.mode ?? 'fixed'"
          data-testid="waiting-room-mode"
          @change="setMode(($event.target as HTMLSelectElement).value as AdmissionMode)"
        >
          <option v-for="o in MODES" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
      </Field>
      <NumField
        v-model="wr.activeUsersPerServer"
        :label="adaptive ? 'Starting limit per server' : 'Active users per server'"
        :description="adaptive ? PER_SERVER_HELP.adaptive : PER_SERVER_HELP.fixed"
        :min="1"
        :max="1000"
        :error="err('waitingRoom.activeUsersPerServer')"
        testid="waiting-room-per-server"
      />
      <NumField
        v-if="adaptive"
        v-model="wr.maxUsersPerServer"
        label="Maximum per server"
        description="Upper bound of a server's learned limit, however much headroom it reports. Must be at least the starting limit."
        :min="1"
        :max="1000"
        :error="err('waitingRoom.maxUsersPerServer')"
        testid="waiting-room-max-per-server"
      />
      <NumField
        v-model="wr.admissionTtlSeconds"
        label="Admission TTL"
        description="Seconds an admitted customer keeps their checkout slot (and admission token) if they never finish or leave. After that the slot goes to the next person in line."
        suffix="s"
        :min="10"
        :max="3600"
        :error="err('waitingRoom.admissionTtlSeconds')"
        testid="waiting-room-ttl"
      />
    </div>
  </fieldset>
</template>
