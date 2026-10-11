import { GoogleAuth } from 'google-auth-library';
import type { AppConfig } from '../config.js';
import type { AutoscalerPatch, CpuSample, InfraProvider, MigReading, RawAutoscaler, RawInstanceDetails, RawManagedInstance } from './types.js';

const COMPUTE = 'https://compute.googleapis.com/compute/v1';
const MONITORING = 'https://monitoring.googleapis.com/v3';

const lastSegment = (url: string) => url.substring(url.lastIndexOf('/') + 1);

/** Compute Engine + Cloud Monitoring over REST with the Cloud Run service account (no key files). */
export class GcpInfra implements InfraProvider {
  readonly available = true;
  private readonly auth = new GoogleAuth({ scopes: ['https://www.googleapis.com/auth/cloud-platform'] });
  private readonly zonePath: string;

  constructor(private readonly config: AppConfig['gcp']) {
    this.zonePath = `${COMPUTE}/projects/${config.projectId}/zones/${config.zone}`;
  }

  private async call<T>(url: string, method: 'GET' | 'POST' | 'PATCH' = 'GET', data?: unknown, timeout = 8000): Promise<T> {
    const client = await this.auth.getClient();
    const response = await client.request<T>({ url, method, data, timeout, retry: false });
    return response.data;
  }

  async readMig(): Promise<MigReading> {
    const migUrl = `${this.zonePath}/instanceGroupManagers/${this.config.migName}`;
    const [mig, managed, autoscaler, instances] = await Promise.all([
      this.call<any>(migUrl),
      this.call<any>(`${migUrl}/listManagedInstances`, 'POST'),
      this.call<any>(`${this.zonePath}/autoscalers/${this.config.autoscalerName}`),
      this.call<any>(`${this.zonePath}/instances?filter=${encodeURIComponent(`labels.lab-role=${this.config.appInstanceLabel}`)}`),
    ]);

    const details = new Map<string, RawInstanceDetails>();
    for (const item of instances.items ?? []) {
      details.set(item.name, {
        name: item.name,
        privateIp: item.networkInterfaces?.[0]?.networkIP ?? null,
        createdAt: item.creationTimestamp ?? null,
        status: item.status,
      });
    }

    const managedInstances: RawManagedInstance[] = (managed.managedInstances ?? []).map((m: any) => ({
      name: lastSegment(m.instance ?? ''),
      id: String(m.id ?? ''),
      instanceStatus: m.instanceStatus ?? 'UNKNOWN',
      currentAction: m.currentAction ?? 'NONE',
      migHealth: m.instanceHealth?.[0]?.detailedHealthState ?? null,
    }));

    return {
      targetSize: mig.targetSize ?? 0,
      isStable: Boolean(mig.status?.isStable),
      instanceGroupUrl: mig.instanceGroup,
      instances: managedInstances,
      autoscaler: parseAutoscaler(autoscaler),
      details,
    };
  }

  async readBackendHealth(instanceGroupUrl: string): Promise<Map<string, string>> {
    const url = `${COMPUTE}/projects/${this.config.projectId}/global/backendServices/${this.config.backendServiceName}/getHealth`;
    const result = await this.call<any>(url, 'POST', { group: instanceGroupUrl });
    const health = new Map<string, string>();
    for (const status of result.healthStatus ?? []) {
      health.set(lastSegment(status.instance), status.healthState ?? 'UNKNOWN');
    }
    return health;
  }

  async readCpu(lookbackMinutes: number): Promise<CpuSample[]> {
    const end = new Date();
    const start = new Date(end.getTime() - lookbackMinutes * 60_000);
    const filter = [
      'metric.type="compute.googleapis.com/instance/cpu/utilization"',
      `metric.labels.instance_name=starts_with("${this.config.instancePrefix}")`,
    ].join(' AND ');
    const params = new URLSearchParams({
      filter,
      'interval.startTime': start.toISOString(),
      'interval.endTime': end.toISOString(),
      view: 'FULL',
    });
    const samples: CpuSample[] = [];
    let pageToken: string | undefined;
    do {
      if (pageToken) params.set('pageToken', pageToken);
      const page = await this.call<any>(`${MONITORING}/projects/${this.config.projectId}/timeSeries?${params}`);
      for (const series of page.timeSeries ?? []) {
        const instance = series.metric?.labels?.instance_name;
        for (const point of series.points ?? []) {
          samples.push({ instance, t: Date.parse(point.interval.endTime), value: Number(point.value.doubleValue) });
        }
      }
      pageToken = page.nextPageToken;
    } while (pageToken);
    return samples;
  }

  async patchAutoscaler(patch: AutoscalerPatch): Promise<void> {
    const current = await this.call<any>(`${this.zonePath}/autoscalers/${this.config.autoscalerName}`);
    const policy = { ...current.autoscalingPolicy };
    if (patch.minReplicas !== undefined) policy.minNumReplicas = patch.minReplicas;
    if (patch.maxReplicas !== undefined) policy.maxNumReplicas = patch.maxReplicas;
    if (patch.initPeriodSeconds !== undefined) policy.coolDownPeriodSec = patch.initPeriodSeconds;
    if (patch.cpuTarget !== undefined) policy.cpuUtilization = { ...policy.cpuUtilization, utilizationTarget: patch.cpuTarget };
    // Scale-in stabilisation: how long the autoscaler looks back at peak load before removing VMs.
    if (patch.scaleInWindowSeconds !== undefined) policy.stabilizationPeriodSec = patch.scaleInWindowSeconds;
    const operation = await this.call<any>(
      `${this.zonePath}/autoscalers?autoscaler=${encodeURIComponent(this.config.autoscalerName)}`,
      'PATCH',
      { name: this.config.autoscalerName, autoscalingPolicy: policy },
    );
    await this.waitForOperation(operation);
  }

  private async waitForOperation(operation: any): Promise<void> {
    const deadline = Date.now() + 120_000;
    let op = operation;
    while (op.status !== 'DONE') {
      if (Date.now() > deadline) throw new Error(`Timed out waiting for operation ${op.name}`);
      op = await this.call<any>(`${this.zonePath}/operations/${op.name}/wait`, 'POST', undefined, 65_000);
    }
    if (op.error?.errors?.length) {
      throw new Error(`Autoscaler update failed: ${op.error.errors.map((e: any) => e.message).join('; ')}`);
    }
  }
}

export function parseAutoscaler(raw: any): RawAutoscaler {
  const policy = raw.autoscalingPolicy ?? {};
  return {
    minReplicas: policy.minNumReplicas ?? 0,
    maxReplicas: policy.maxNumReplicas ?? 0,
    cpuTarget: policy.cpuUtilization?.utilizationTarget ?? 0,
    initPeriodSeconds: policy.coolDownPeriodSec ?? 60,
    scaleInWindowSeconds: policy.stabilizationPeriodSec ?? null,
    recommendedSize: raw.recommendedSize ?? null,
    status: raw.status ?? 'UNKNOWN',
    messages: (raw.statusDetails ?? []).map((d: any) => d.message).filter(Boolean),
  };
}
