export interface RawManagedInstance {
  name: string; id: string; instanceStatus: string; currentAction: string; migHealth: string | null;
}
export interface RawAutoscaler {
  minReplicas: number; maxReplicas: number; cpuTarget: number; initPeriodSeconds: number;
  scaleInWindowSeconds: number | null; recommendedSize: number | null; status: string; messages: string[];
}
export interface RawInstanceDetails { name: string; privateIp: string | null; createdAt: string | null; status: string }
export interface MigReading {
  targetSize: number; isStable: boolean; instanceGroupUrl: string;
  instances: RawManagedInstance[]; autoscaler: RawAutoscaler; details: Map<string, RawInstanceDetails>;
}
/** CPU utilisation as a fraction 0..1 (like the autoscaler's target). */
export interface CpuSample { instance: string; t: number; value: number }
export interface AutoscalerPatch {
  minReplicas?: number; maxReplicas?: number; cpuTarget?: number; initPeriodSeconds?: number; scaleInWindowSeconds?: number;
}

/** Read and control the real Compute Engine resources. Never synthesises values. */
export interface InfraProvider {
  readonly available: boolean;
  readMig(): Promise<MigReading>;
  readBackendHealth(instanceGroupUrl: string): Promise<Map<string, string>>;
  readCpu(lookbackMinutes: number): Promise<CpuSample[]>;
  patchAutoscaler(patch: AutoscalerPatch): Promise<void>;
}

export class InfraUnavailableError extends Error {}

/** Used when running without GCP (local development): every read reports "unavailable". */
export class UnavailableInfra implements InfraProvider {
  readonly available = false;
  private error() {
    return new InfraUnavailableError('Google Cloud APIs are not configured (local mode)');
  }
  async readMig(): Promise<MigReading> { throw this.error(); }
  async readBackendHealth(): Promise<Map<string, string>> { throw this.error(); }
  async readCpu(): Promise<CpuSample[]> { throw this.error(); }
  async patchAutoscaler(): Promise<void> { throw this.error(); }
}
