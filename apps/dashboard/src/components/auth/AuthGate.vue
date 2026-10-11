<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, watch } from 'vue';
import type { AuthSession } from '@/api/auth';
import { createLabStore, type LabStore } from '@/store/lab';
import ProvideStore from './ProvideStore.vue';
import SignInScreen from './SignInScreen.vue';
import NotAuthorisedScreen from './NotAuthorisedScreen.vue';
import HeaderBar from '@/components/layout/HeaderBar.vue';

const props = defineProps<{ session: AuthSession; storeFactory?: (session: AuthSession) => LabStore }>();

const store = shallowRef<LabStore | null>(null);
const state = props.session.state;

// The store exists only while signed in; on any auth failure it is cleared so no data remains visible.

let storeSeq = 0;
const storeKey = shallowRef(0);

function makeStore(): LabStore {
  storeKey.value = ++storeSeq;
  return props.storeFactory ? props.storeFactory(props.session) : createLabStore(props.session.api);
}

watch(
  () => state.status,
  (status) => {
    if (status === 'signed_in' && !store.value) {
      store.value = makeStore();
      void store.value.startLive();
    } else if (status !== 'signed_in' && status !== 'checking' && store.value) {
      store.value.clear();
      store.value = null;
    }
  },
  { immediate: true },
);

onMounted(() => {
  if (state.status === 'loading') void props.session.init();
});
onBeforeUnmount(() => store.value?.clear());
</script>

<template>
  <div v-if="state.status === 'loading' || (state.status === 'checking' && !store)" class="flex min-h-screen items-center justify-center text-sm text-slate-400" data-testid="auth-loading">
    Checking access…
  </div>
  <SignInScreen
    v-else-if="state.status === 'needs_signin'"
    :client-id="state.config?.googleClientId ?? null"
    :message="state.message"
    @credential="session.setGoogleCredential"
  />
  <NotAuthorisedScreen v-else-if="state.status === 'unauthorised'" :status="state.httpStatus" :message="state.message" :mode="state.config?.authMode ?? null" @retry="session.init()" />
  <main v-else-if="state.status === 'error'" class="flex min-h-screen items-center justify-center p-4" data-testid="auth-error">
    <div class="max-w-md rounded-lg border border-slate-800 bg-slate-900 p-6 text-sm text-slate-300">
      <h1 class="font-semibold text-slate-100">Control API unavailable</h1>
      <p class="mt-2">{{ state.message }}</p>
      <button type="button" class="btn btn-default mt-4" @click="session.init()">Retry</button>
    </div>
  </main>
  <ProvideStore v-else-if="store" :key="storeKey" :store="store">
    <HeaderBar :me="state.me" @sign-out="session.signOut()" />
    <slot />
  </ProvideStore>
</template>
