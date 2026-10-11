/**
 * Calls the ticketing API's private /api/admin/* endpoints. On GCP these are reached on a healthy
 * VM's private IP (nginx refuses them through the public load balancer).
 */
/** Lab settings (`PUT /api/admin/settings`): numeric tunables plus `waiting_room_required`. */
export type LabSettings = Record<string, number | boolean>;

export class TicketingAdmin {
  constructor(private readonly token: string, private readonly resolveBaseUrl: () => string | null) {}

  private async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const base = this.resolveBaseUrl();
    if (!base) throw new Error('No healthy application instance is available for admin calls');
    const response = await fetch(`${base}${path}`, {
      method,
      headers: { Authorization: `Bearer ${this.token}`, 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(15000),
    });
    const text = await response.text();
    const json = text ? JSON.parse(text) : {};
    if (!response.ok) throw new Error(`Ticketing admin ${method} ${path} failed (${response.status}): ${json.message ?? text}`);
    return json.data as T;
  }

  prepare(input: {
    event_name: string;
    categories: Array<{ name: string; price_cents: number; capacity: number }>;
    settings: LabSettings;
  }) {
    return this.call<{ event: { id: string; name: string; categories: Array<{ id: string; name: string; price_cents: number; capacity: number }> }; settings: LabSettings }>(
      'POST', '/api/admin/experiments/prepare', input);
  }

  resetInventory(eventId: string) {
    return this.call<Record<string, unknown>>('POST', `/api/admin/events/${eventId}/reset-inventory`);
  }

  updateSettings(settings: LabSettings) {
    return this.call<LabSettings>('PUT', '/api/admin/settings', settings);
  }
}

export interface InstanceInfo {
  instance: string; version: string; ready: boolean; guest_cpu: { percent: number; sampled_at: string } | null;
}

export async function fetchInstanceInfo(ip: string, port: number): Promise<InstanceInfo> {
  const response = await fetch(`http://${ip}:${port}/instance-info`, { signal: AbortSignal.timeout(1500) });
  if (!response.ok) throw new Error(`instance-info ${response.status}`);
  return (await response.json()) as InstanceInfo;
}
