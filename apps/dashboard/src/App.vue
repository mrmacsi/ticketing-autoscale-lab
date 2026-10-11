<script setup lang="ts">
import { RouterView } from 'vue-router';
import AuthGate from '@/components/auth/AuthGate.vue';
import type { AuthSession } from '@/api/auth';

defineProps<{ session: AuthSession }>();
</script>

<template>
  <AuthGate :session="session">
    <RouterView />
  </AuthGate>
</template>
