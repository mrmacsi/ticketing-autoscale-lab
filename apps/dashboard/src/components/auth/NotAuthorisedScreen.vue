<script setup lang="ts">
defineProps<{ status: number | null; message: string | null; mode: string | null }>();
const emit = defineEmits<{ retry: [] }>();
</script>

<template>
  <main class="flex min-h-screen items-center justify-center p-4" data-testid="not-authorised-screen">
    <div class="w-full max-w-md rounded-lg border border-red-500/40 bg-slate-900 p-6">
      <h1 class="text-lg font-semibold text-red-200">Not authorised</h1>
      <p class="mt-2 text-sm text-slate-300">
        The control API refused this request<template v-if="status"> (HTTP {{ status }})</template>. Your identity is not allowed to use this lab, or your
        session is no longer valid.
      </p>
      <p v-if="message" class="mt-2 rounded bg-slate-950 px-2 py-1.5 font-mono text-xs text-slate-400">{{ message }}</p>
      <p v-if="mode === 'iam_proxy'" class="mt-3 text-xs text-slate-400">
        This deployment uses Cloud Run IAM. Open the dashboard through <code>gcloud run services proxy</code> (see <code>scripts/dashboard.sh</code>) with an
        account that has the invoker role and is on the allowlist.
      </p>
      <button type="button" class="btn btn-default mt-4" @click="emit('retry')">Try again</button>
    </div>
  </main>
</template>
