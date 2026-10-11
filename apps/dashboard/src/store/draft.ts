import { computed, inject, reactive, ref, watch, type InjectionKey } from 'vue';
import type { FieldIssue } from '@/api/client';
import { buildRateProfile } from '@/lib/rateProfile';
import { validateConfig } from '@/lib/validation';
import { DEFAULT_WAITING_ROOM, NEAR_SELLOUT_DEFAULTS, type ExperimentConfig, type Preset } from '@/types/contracts';
import type { LabStore } from './lab';

/** Deep copy; settings missing from an older control API's config get their disabled defaults. */
export function cloneConfig(c: ExperimentConfig): ExperimentConfig {
  const copy = JSON.parse(JSON.stringify(c)) as ExperimentConfig;
  copy.waitingRoom = { ...DEFAULT_WAITING_ROOM, ...(copy.waitingRoom ?? {}) };
  copy.tickets = { ...NEAR_SELLOUT_DEFAULTS, ...copy.tickets };
  return copy;
}

/** Editable experiment configuration shared by the config form (A) and the controls (B). */
export function createExperimentDraft(store: Pick<LabStore, 'meta' | 'prepare' | 'status'>) {
  const config = ref<ExperimentConfig | null>(null);
  const serverIssues = ref<FieldIssue[]>([]);
  const serverMessage = ref<string | null>(null);
  const confirmation = reactive({ open: false, message: '' });

  watch(
    () => store.meta.value,
    (m) => {
      if (m && !config.value) config.value = cloneConfig(m.defaults);
    },
    { immediate: true },
  );

  const validation = computed(() => (config.value && store.meta.value ? validateConfig(config.value, store.meta.value) : null));

  const profile = computed(() => {
    const c = config.value;
    if (!c) return null;
    const d = Number.isFinite(c.traffic.durationSeconds) ? Math.min(c.traffic.durationSeconds, 24 * 3600) : 0;
    return buildRateProfile(c.traffic.pattern, d, Number.isFinite(c.traffic.totalArrivals) ? c.traffic.totalArrivals : 0);
  });

  function fieldError(path: string): string | null {
    const client = validation.value?.errors[path];
    if (client) return client;
    const server = serverIssues.value.find((i) => i.path === path);
    return server ? server.message : null;
  }

  function clearServerFeedback(): void {
    serverIssues.value = [];
    serverMessage.value = null;
  }

  function applyPreset(p: Preset): void {
    config.value = { ...cloneConfig(p.config), preset: p.id };
    clearServerFeedback();
  }

  function resetToDefaults(): void {
    if (store.meta.value) config.value = cloneConfig(store.meta.value.defaults);
    clearServerFeedback();
  }

  async function submit(confirmExpensive = false): Promise<boolean> {
    const c = config.value;
    if (!c || !validation.value?.valid) return false;
    const body: ExperimentConfig = { ...cloneConfig(c), confirmExpensive };
    const result = await store.prepare(body);
    if (result.ok) {
      clearServerFeedback();
      confirmation.open = false;
      return true;
    }
    const f = result.failure;
    if (f.status === 428) {
      confirmation.open = true;
      confirmation.message = f.message;
      return false;
    }
    confirmation.open = false;
    serverMessage.value = f.message;
    serverIssues.value = f.status === 422 ? f.issues : [];
    return false;
  }

  return {
    config,
    validation,
    profile,
    serverIssues,
    serverMessage,
    confirmation,
    fieldError,
    applyPreset,
    resetToDefaults,
    submit,
    confirmExpensive: () => submit(true),
    cancelConfirmation: () => {
      confirmation.open = false;
    },
  };
}

export type ExperimentDraft = ReturnType<typeof createExperimentDraft>;

export const DRAFT: InjectionKey<ExperimentDraft> = Symbol('experiment-draft');

export function useDraft(): ExperimentDraft {
  const d = inject(DRAFT);
  if (!d) throw new Error('Experiment draft not provided');
  return d;
}
