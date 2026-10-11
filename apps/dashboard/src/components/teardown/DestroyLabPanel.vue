<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import Card from '@/components/ui/Card.vue';
import { localCapabilities, startTeardown, teardownStatus, type LocalCapabilities, type TeardownStatus } from '@/api/teardown';

const caps = ref<LocalCapabilities | null>(null);
const status = ref<TeardownStatus | null>(null);
const dialogOpen = ref(false);
const typed = ref('');
const error = ref<string | null>(null);
const submitting = ref(false);
const logEl = ref<HTMLElement | null>(null);
let timer: ReturnType<typeof setInterval> | null = null;

const running = computed(() => status.value?.state === 'running');
const phraseOk = computed(() => !!caps.value && typed.value.trim() === caps.value.confirmPhrase);

async function refresh() {
  try {
    status.value = await teardownStatus();
    if (status.value.state !== 'running') stopPolling();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function startPolling() {
  stopPolling();
  timer = setInterval(refresh, 1500);
}
function stopPolling() {
  if (timer) clearInterval(timer);
  timer = null;
}

async function confirm() {
  if (!caps.value || !phraseOk.value) return;
  submitting.value = true;
  error.value = null;
  try {
    status.value = await startTeardown(caps.value.confirmPhrase);
    dialogOpen.value = false;
    startPolling();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    submitting.value = false;
  }
}

watch(() => status.value?.lines.length, async () => {
  await nextTick();
  if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight;
});

onMounted(async () => {
  caps.value = await localCapabilities();
  if (caps.value) {
    await refresh();
    if (running.value) startPolling();
  }
});
onBeforeUnmount(stopPolling);
</script>

<template>
  <Card v-if="caps" title="Destroy lab" tag="!" subtitle="Deletes every Google Cloud resource of this lab: servers, load balancer, database, generator, dashboard, registry, secrets and history." data-testid="destroy-panel">
    <div class="space-y-3 text-sm">
      <p class="text-xs text-slate-400">
        Runs <code class="text-slate-300">scripts/teardown.sh</code> on this computer with your Terraform state and
        <span class="text-slate-300">{{ caps.account }}</span>: stops traffic, <code>terraform destroy</code>, removes the temporary
        Firestore role, verifies nothing named <code>ticketing-scale-lab</code> is left, then disables the APIs the lab enabled in
        <span class="text-slate-300">{{ caps.project }}</span>. This page stays open to show progress after the cloud dashboard is gone.
      </p>
      <button
        type="button"
        class="btn btn-danger w-full py-2.5 font-semibold"
        :disabled="running || status?.state === 'succeeded'"
        data-testid="btn-destroy"
        @click="dialogOpen = true; typed = ''; error = null"
      >
        {{ running ? 'Destroying…' : status?.state === 'succeeded' ? 'Lab destroyed' : 'Destroy everything in Google Cloud' }}
      </button>

      <div v-if="status && status.state !== 'idle'" class="space-y-1" data-testid="teardown-progress">
        <p
          :class="[
            'text-xs font-medium',
            status.state === 'succeeded' ? 'text-emerald-300' : status.state === 'failed' ? 'text-red-300' : 'text-amber-300',
          ]"
          data-testid="teardown-state"
        >
          {{ status.state === 'running' ? 'Teardown running — resource deletion takes several minutes; Google may retain Cloud Run network IPs for 1–2 hours. The log shows progress.'
            : status.state === 'succeeded' ? 'Teardown complete: nothing from the lab remains in Google Cloud.'
            : `Teardown failed (exit ${status.exitCode}). Read the log, fix the cause and press the button again.` }}
        </p>
        <pre ref="logEl" class="max-h-80 overflow-auto rounded border border-slate-800 bg-black/60 p-2 font-mono text-[11px] leading-snug text-slate-300">{{ status.lines.join('\n') }}</pre>
      </div>
      <p v-if="error" class="text-xs text-red-300" role="alert">{{ error }}</p>
    </div>

    <div v-if="dialogOpen" class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4" role="dialog" aria-modal="true" aria-label="Destroy lab">
      <div class="w-full max-w-md rounded-lg border border-red-500/50 bg-slate-900 p-4 shadow-xl">
        <h3 class="text-base font-semibold text-red-200">Destroy the whole lab?</h3>
        <p class="mt-2 text-sm text-slate-300">
          This permanently deletes all servers, the database (tickets and orders), experiment history, images and secrets. It cannot be undone;
          a new deployment starts from scratch.
        </p>
        <label class="mt-3 block text-xs text-slate-400">
          Type <code class="text-slate-200">{{ caps.confirmPhrase }}</code> to confirm
          <input v-model="typed" type="text" autocomplete="off" class="mt-1 w-full rounded border border-slate-700 bg-slate-950 px-2 py-1.5 text-sm text-slate-100" data-testid="destroy-phrase" />
        </label>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="btn btn-default" @click="dialogOpen = false">Cancel</button>
          <button type="button" class="btn btn-danger" :disabled="!phraseOk || submitting" data-testid="destroy-confirm" @click="confirm">
            {{ submitting ? 'Starting…' : 'Destroy' }}
          </button>
        </div>
      </div>
    </div>
  </Card>
</template>
