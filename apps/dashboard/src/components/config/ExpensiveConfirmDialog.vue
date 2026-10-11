<script setup lang="ts">
import { computed } from 'vue';
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue';
import { useDraft } from '@/store/draft';
import { useLab } from '@/store/lab';

const draft = useDraft();
const store = useLab();
const reasons = computed(() => draft.validation.value?.expensiveReasons ?? []);
</script>

<template>
  <ConfirmDialog
    :open="draft.confirmation.open"
    title="Confirm expensive experiment"
    confirm-label="Prepare anyway"
    require-ack="I understand this run may scale to more servers / run longer and cost more."
    :busy="store.status.busy !== null"
    @confirm="draft.confirmExpensive()"
    @cancel="draft.cancelConfirmation()"
  >
    <p data-testid="confirm-message">{{ draft.confirmation.message || 'The control API requires explicit confirmation for this configuration.' }}</p>
    <ul v-if="reasons.length" class="mt-2 list-disc pl-5 text-xs text-amber-200">
      <li v-for="r in reasons" :key="r">{{ r }}</li>
    </ul>
  </ConfirmDialog>
</template>
