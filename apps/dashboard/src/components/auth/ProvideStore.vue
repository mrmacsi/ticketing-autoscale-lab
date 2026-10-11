<script setup lang="ts">
import { provide } from 'vue';
import { LAB_STORE, type LabStore } from '@/store/lab';

const props = defineProps<{ store: LabStore }>();
provide(LAB_STORE, props.store);
</script>

<template>
  <slot />
</template>
