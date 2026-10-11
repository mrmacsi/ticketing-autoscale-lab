import type { Baseline } from './types.js';

export type AuthMode = 'iam_proxy' | 'google_signin' | 'dev';

export interface AppConfig {
  port: number;
  staticDir: string | null;
  onCloudRun: boolean;
  auth: { mode: AuthMode; allowedEmails: string[]; googleClientId: string | null };
  gcp: {
    enabled: boolean;
    projectId: string; region: string; zone: string;
    migName: string; autoscalerName: string; instancePrefix: string; backendServiceName: string; appInstanceLabel: string;
    firestoreDatabase: string;
  };
  approvedMaxInstances: number;
  baseline: Baseline;
  ticketing: { publicBaseUrl: string; adminBaseUrl: string | null; adminToken: string; adminPort: number };
  /** Queue server private API (docs/waiting-room.md 3.4); null when the waiting room is not deployed. */
  waitingRoom: { baseUrl: string | null };
  database: { host: string; port: number; database: string; user: string; password: string; ssl: boolean } | null;
  limits: { maxRate: number; maxVUs: number; maxDurationSeconds: number; maxTotalInventory: number; maxSyntheticCpuMs: number;
            maxCooldownSeconds: number };
  expensive: { rateAbove: number; durationSecondsAbove: number; vusAbove: number; instancesAbove: number };
  pricing: Record<string, number>;
  appMachineType: string;
  generatorMachineType: string;
  /** Waiting-room queue VMs (only billed when the waiting room is deployed). */
  queueMachineType: string;
  dbTier: string;
  intervals: { migMs: number; lbMs: number; cpuMs: number; instanceInfoMs: number; inventoryMs: number; waitingRoomMs: number; tickMs: number };
}

let source: NodeJS.ProcessEnv = process.env;

const str = (name: string, fallback?: string): string => {
  const value = source[name];
  if (value !== undefined && value !== '') return value;
  if (fallback !== undefined) return fallback;
  throw new Error(`Missing required environment variable ${name}`);
};
const num = (name: string, fallback: number): number => {
  const raw = source[name];
  if (raw === undefined || raw === '') return fallback;
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error(`Environment variable ${name} must be a number`);
  return value;
};

export function loadConfig(env: NodeJS.ProcessEnv = process.env): AppConfig {
  source = env;
  const onCloudRun = Boolean(env.K_SERVICE);
  const mode = str('AUTH_MODE', onCloudRun ? 'iam_proxy' : 'dev') as AuthMode;
  if (!['iam_proxy', 'google_signin', 'dev'].includes(mode)) throw new Error(`Unknown AUTH_MODE ${mode}`);
  if (mode === 'dev' && onCloudRun) throw new Error('AUTH_MODE=dev is refused on Cloud Run');
  if (mode === 'google_signin' && !env.GOOGLE_CLIENT_ID) throw new Error('google_signin requires GOOGLE_CLIENT_ID');

  const approvedMaxInstances = num('APPROVED_MAX_INSTANCES', 5);
  const baseline: Baseline = {
    minReplicas: num('BASELINE_MIN_REPLICAS', 1),
    maxReplicas: Math.min(num('BASELINE_MAX_REPLICAS', approvedMaxInstances), approvedMaxInstances),
    cpuTarget: num('BASELINE_CPU_TARGET', 0.6),
    initPeriodSeconds: num('BASELINE_INIT_PERIOD_SECONDS', 60),
    scaleInWindowSeconds: num('BASELINE_SCALE_IN_WINDOW_SECONDS', 240),
  };

  const gcpEnabled = (env.GCP_ENABLED ?? (onCloudRun ? 'true' : 'false')) === 'true';
  const dbHost = env.DB_HOST;

  return {
    port: num('PORT', 8081),
    staticDir: env.STATIC_DIR || null,
    onCloudRun,
    auth: {
      mode,
      allowedEmails: (env.ALLOWED_EMAILS ?? '').split(',').map((e) => e.trim().toLowerCase()).filter(Boolean),
      googleClientId: env.GOOGLE_CLIENT_ID || null,
    },
    gcp: {
      enabled: gcpEnabled,
      projectId: str('GCP_PROJECT_ID', gcpEnabled ? undefined : 'local'),
      region: str('GCP_REGION', 'europe-west2'),
      zone: str('GCP_ZONE', 'europe-west2-b'),
      migName: str('MIG_NAME', 'ticketing-scale-lab-app-mig'),
      autoscalerName: str('AUTOSCALER_NAME', 'ticketing-scale-lab-app-autoscaler'),
      instancePrefix: str('APP_INSTANCE_PREFIX', 'ticketing-scale-lab-app'),
      backendServiceName: str('BACKEND_SERVICE_NAME', 'ticketing-scale-lab-app-backend'),
      appInstanceLabel: str('APP_INSTANCE_LABEL', 'app'),
      firestoreDatabase: str('FIRESTORE_DATABASE', 'ticketing-scale-lab'),
    },
    approvedMaxInstances,
    baseline,
    ticketing: {
      publicBaseUrl: str('TICKETING_PUBLIC_URL', 'http://localhost:8080'),
      // Locally the admin API is reached directly; on GCP the control plane picks a healthy VM's private IP.
      adminBaseUrl: env.TICKETING_ADMIN_URL || null,
      adminToken: str('LAB_ADMIN_TOKEN', ''),
      adminPort: num('TICKETING_ADMIN_PORT', 8080),
    },
    // Same LAB_ADMIN_TOKEN as the ticketing admin API. Empty = waiting room not deployed.
    waitingRoom: { baseUrl: (env.WAITING_ROOM_URL ?? '').trim().replace(/\/+$/, '') || null },
    database: dbHost
      ? { host: dbHost, port: num('DB_PORT', 5432), database: str('DB_NAME', 'ticketing'), user: str('DB_USER', 'lab_observer'), password: str('DB_PASSWORD', ''),
          ssl: env.DB_SSL === 'require' }
      : null,
    limits: {
      maxRate: num('LIMIT_MAX_RATE', 500),
      // Customers waiting in the waiting room occupy a VU each, so a 5,000-customer sale needs 5,000.
      maxVUs: num('LIMIT_MAX_VUS', 1_000_000),
      maxDurationSeconds: num('LIMIT_MAX_DURATION_SECONDS', 1800),
      maxTotalInventory: num('LIMIT_MAX_TOTAL_INVENTORY', 1_000_000),
      maxSyntheticCpuMs: num('LIMIT_MAX_SYNTHETIC_CPU_MS', 200),
      maxCooldownSeconds: num('LIMIT_MAX_COOLDOWN_SECONDS', 3600),
    },
    expensive: {
      rateAbove: num('EXPENSIVE_RATE_ABOVE', 150),
      durationSecondsAbove: num('EXPENSIVE_DURATION_ABOVE', 900),
      vusAbove: num('EXPENSIVE_VUS_ABOVE', 1000),
      instancesAbove: num('EXPENSIVE_INSTANCES_ABOVE', approvedMaxInstances),
    },
    // europe-west2 on-demand list-price ESTIMATES (USD/hour); override per resource with PRICE_* env vars.
    pricing: {
      appVmPerHour: num('PRICE_APP_VM_PER_HOUR', 0.113),               // n2-standard-2
      appDiskPerHour: num('PRICE_APP_DISK_PER_HOUR', 0.0022),
      generatorVmPerHour: num('PRICE_GENERATOR_VM_PER_HOUR', 0.078),   // e2-standard-2
      generatorIpPerHour: num('PRICE_EXTERNAL_IP_PER_HOUR', 0.005),
      cloudSqlPerHour: num('PRICE_CLOUD_SQL_PER_HOUR', 0.084),
      loadBalancerPerHour: num('PRICE_LB_RULE_PER_HOUR', 0.03),
      lbIpPerHour: num('PRICE_LB_IP_PER_HOUR', 0.005),
      cloudRunPerHour: num('PRICE_CLOUD_RUN_PER_HOUR', 0.068),
      miscPerHour: num('PRICE_MISC_PER_HOUR', 0.01),
      // Waiting room (only counted when WAITING_ROOM_URL is set): queue VMs + Memorystore Redis basic 1 GB.
      queueVmPerHour: num('PRICE_QUEUE_VM_PER_HOUR', 0.0195),          // e2-small, each
      queueVms: num('PRICE_QUEUE_VMS', 2),
      redisPerHour: num('PRICE_REDIS_PER_HOUR', 0.058),
    },
    appMachineType: str('APP_MACHINE_TYPE', 'n2-standard-2'),
    generatorMachineType: str('GENERATOR_MACHINE_TYPE', 'e2-standard-2'),
    queueMachineType: str('QUEUE_MACHINE_TYPE', 'e2-small'),
    dbTier: str('DB_TIER', 'db-custom-1-3840'),
    intervals: {
      migMs: num('POLL_MIG_MS', 5000),
      lbMs: num('POLL_LB_MS', 10000),
      cpuMs: num('POLL_CPU_MS', 20000),
      instanceInfoMs: num('POLL_INSTANCE_INFO_MS', 5000),
      inventoryMs: num('POLL_INVENTORY_MS', 2000),
      waitingRoomMs: num('POLL_WAITING_ROOM_MS', 1000),
      tickMs: num('TICK_MS', 1000),
    },
  };
}
