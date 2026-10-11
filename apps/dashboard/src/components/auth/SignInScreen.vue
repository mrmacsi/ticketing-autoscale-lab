<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { loadGoogleIdentity } from '@/api/auth';

const props = defineProps<{ clientId: string | null; message: string | null }>();
const emit = defineEmits<{ credential: [idToken: string] }>();

const buttonEl = ref<HTMLDivElement | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  if (!props.clientId) {
    error.value = 'The control API is in google_signin mode but returned no googleClientId.';
    return;
  }
  try {
    const gis = await loadGoogleIdentity();
    gis.initialize({
      client_id: props.clientId,
      callback: (resp) => emit('credential', resp.credential),
      auto_select: true,
      use_fedcm_for_prompt: true,
    });
    if (buttonEl.value) gis.renderButton(buttonEl.value, { theme: 'filled_black', size: 'large', text: 'signin_with', shape: 'pill' });
    gis.prompt();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
});
</script>

<template>
  <main class="flex min-h-screen items-center justify-center p-4" data-testid="sign-in-screen">
    <div class="w-full max-w-sm rounded-lg border border-slate-800 bg-slate-900 p-6 text-center">
      <h1 class="text-lg font-semibold text-slate-50">Ticketing Autoscale Lab</h1>
      <p class="mt-1 text-sm text-slate-400">Sign in with an allow-listed Google account to view and control the lab.</p>
      <p v-if="message" class="mt-3 rounded bg-amber-500/10 px-2 py-1.5 text-xs text-amber-200" role="alert">{{ message }}</p>
      <div ref="buttonEl" class="mt-5 flex justify-center" />
      <p v-if="error" class="mt-3 text-xs text-red-300" role="alert">{{ error }}</p>
    </div>
  </main>
</template>
