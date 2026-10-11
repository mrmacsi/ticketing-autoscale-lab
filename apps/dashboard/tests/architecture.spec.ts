import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import ArchitectureDiagram from '@/components/live/ArchitectureDiagram.vue';
import { live, makeEvent, makeInstance, makeMeta, makeSnapshot, makeWaitingRoom, SERVER_TIME } from './fixtures';

const setup = () => mount(ArchitectureDiagram, { props: { snapshot: makeSnapshot(), meta: makeMeta(), nowMs: Date.parse(SERVER_TIME) } });

describe('live architecture', () => {
  it('branches only for listed VMs, grows and shrinks with real snapshots, and preserves target vs actual counts', async () => {
    const w = setup();
    expect(w.findAll('[data-testid="diagram-server"]')).toHaveLength(2);
    expect(w.text()).toContain('2 listed · actual 2 · target 3');
    const instances = Array.from({ length: 5 }, (_, i) => makeInstance(`app-${i}`));
    await w.setProps({ snapshot: makeSnapshot({ instances }) });
    expect(w.findAll('[data-testid="diagram-server"]')).toHaveLength(5);
    expect(w.findAll('[data-backend]')).toHaveLength(5);
    expect(w.find('svg').attributes('viewBox')).toBe('0 0 1100 758');
    await w.setProps({ snapshot: makeSnapshot({ instances: [instances[2]!] }) });
    expect(w.findAll('[data-testid="diagram-server"]')).toHaveLength(1);
    expect(w.find('[data-testid="diagram-server"]').attributes('data-instance')).toBe('app-2');
    await w.setProps({ snapshot: makeSnapshot({ instances: [], mig: null }) });
    expect(w.findAll('[data-backend]')).toHaveLength(0);
    expect(w.find('[data-testid="diagram-no-servers"]').exists()).toBe(true);
    w.unmount();
  });

  it('stops animation and dims the last-known topology if updates stop arriving', async () => {
    const w = setup();
    expect(w.findAll('.flowing').length).toBeGreaterThan(0);
    await w.setProps({ nowMs: Date.parse(SERVER_TIME) + 16000 });
    expect(w.findAll('.flowing')).toHaveLength(0);
    expect(w.find('[data-testid="flow-status"]').text()).toContain('snapshot stale');
    expect(w.find('[data-testid="diagram-server"]').classes()).toContain('outdated');
    expect(w.find('[data-testid="diagram-server"]').text()).toContain('N/A req/s');
    w.unmount();
  });

  it('animates observed active HTTP branches only, and stops on stale routing or traffic', async () => {
    const w = setup();
    const snapshot = makeSnapshot({ instances: [makeInstance('busy'), makeInstance('idle', { traffic: { rps: 0, avgLatencyMs: null, errorRate: null, windowSeconds: 5 } }), makeInstance('new', { phase: 'starting' })] });
    await w.setProps({ snapshot });
    expect(w.find('[data-backend="busy"]').classes()).toContain('flowing');
    expect(w.find('[data-backend="idle"]').classes()).not.toContain('flowing');
    expect(w.find('[data-backend="new"]').classes()).not.toContain('flowing');
    expect(w.findAll('.data-route.flowing')).toHaveLength(0);
    for (const source of ['traffic', 'lb', 'mig'] as const) {
      const stale = makeSnapshot();
      stale.freshness[source].status = 'stale';
      await w.setProps({ snapshot: stale });
      expect(w.findAll('.flowing')).toHaveLength(0);
      expect(w.find('[data-testid="flow-status"]').text()).toContain(`${source.toUpperCase()} stale`);
    }
    w.unmount();
  });

  it('marks unavailable values and local mode without inventing cloud nodes or DB load', async () => {
    const w = setup();
    const snapshot = makeSnapshot();
    snapshot.freshness.traffic.status = 'missing';
    snapshot.freshness.inventory.status = 'stale';
    snapshot.instances[0]!.cpu.sampleTime = '2026-10-09T14:00:00.000Z';
    await w.setProps({ snapshot });
    expect(w.find('[data-instance="ticketing-scale-lab-app-x7k2"]').text()).toContain('N/A req/s · CPU N/A');
    expect(w.text()).toContain('N/A sold · N/A held');
    expect(w.text()).toContain('DB query rate / CPU not measured');
    snapshot.freshness.mig.note = 'MIG not configured';
    await w.setProps({ snapshot: { ...snapshot } });
    expect(w.find('svg').exists()).toBe(false);
    expect(w.find('[data-testid="local-topology"]').exists()).toBe(true);
    w.unmount();
  });
});

describe('live architecture: latest step and waiting room', () => {
  const now = Date.parse(SERVER_TIME);
  const mountWith = (props: Record<string, unknown>) =>
    mount(ArchitectureDiagram, { props: { snapshot: makeSnapshot(), meta: makeMeta(), nowMs: now, ...props } });

  it('shows the newest timeline event as the current step, relative to traffic start', () => {
    const e = makeEvent('e1', { t: new Date(now - 4000).toISOString(), message: 'Autoscaler increased MIG target size 1 → 2' });
    const w = mountWith({ latestEvent: e, trafficStartedAt: new Date(now - 64000).toISOString() });
    expect(w.find('[data-testid="latest-step-message"]').text()).toBe('Autoscaler increased MIG target size 1 → 2');
    expect(w.find('[data-testid="latest-step"]').text()).toContain('01:00');
    expect(w.find('[data-testid="latest-step-age"]').text()).toBe('4 s ago');
    expect(w.find('[data-node="autoscaler"]').classes()).toContain('event-focus');
    w.unmount();
  });

  it('outlines the VM an event is about, only while the event is recent', async () => {
    const name = makeSnapshot().instances[0]!.name;
    const e = makeEvent('e2', { kind: 'instance', t: new Date(now - 2000).toISOString(), data: { instance: name } });
    const w = mountWith({ latestEvent: e });
    expect(w.find(`[data-instance="${name}"]`).classes()).toContain('event-focus');
    await w.setProps({ nowMs: now + 30_000 });
    expect(w.findAll('.event-focus')).toHaveLength(0);
    w.unmount();
  });

  it('hides the strip when there is no event', () => {
    expect(mountWith({}).find('[data-testid="latest-step"]').exists()).toBe(false);
  });

  it('draws the queue server with live waiting room numbers and outlines it for waiting-room events', () => {
    const snapshot = makeSnapshot({ waitingRoom: makeWaitingRoom(), freshness: { ...makeSnapshot().freshness, waitingRoom: live() } });
    const e = makeEvent('e3', { kind: 'waiting_room', source: 'waiting_room', t: new Date(now - 1000).toISOString() });
    const w = mountWith({ snapshot, latestEvent: e });
    const q = w.find('[data-testid="diagram-queue"]');
    expect(q.text()).toContain('3,110 waiting');
    expect(q.text()).toContain('75/75 in checkout');
    expect(q.classes()).toContain('event-focus');
    expect(w.find('[data-testid="queue-route"]').classes()).toContain('flowing');
    w.unmount();
  });

  it('shows N/A for a queue that is deployed but not observed, and nothing when it is not deployed', () => {
    const w = mountWith({});
    expect(w.find('[data-testid="diagram-queue"]').text()).toContain('N/A waiting');
    const meta = { ...makeMeta(), waitingRoom: { deployed: false } };
    expect(mountWith({ meta }).find('[data-testid="diagram-queue"]').exists()).toBe(false);
  });
});
