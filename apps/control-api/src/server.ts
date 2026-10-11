import fastifyStatic from '@fastify/static';
import Fastify, { type FastifyReply, type FastifyRequest } from 'fastify';
import { existsSync } from 'node:fs';
import { AuthError, type Authenticator, type Identity } from './auth/auth.js';
import type { AppConfig } from './config.js';
import { costModel } from './costs.js';
import { defaultExperiment, presets } from './experiments/presets.js';
import { ConfirmationRequiredError, StateError, type ExperimentService } from './experiments/service.js';
import { ValidationError } from './experiments/validation.js';
import { buildReport, eventsCsv, timeseriesCsv } from './report/report.js';
import { ConflictError, type LabStore } from './store/types.js';
import { emptyAppend, type Collector } from './telemetry/collector.js';
import { toTrafficPoint } from './telemetry/traffic.js';
import type { Series } from './types.js';

declare module 'fastify' {
  interface FastifyRequest { identity?: Identity }
}

export interface ServerDeps {
  config: AppConfig;
  auth: Authenticator;
  store: LabStore;
  collector: Collector;
  experiments: ExperimentService;
}

export function buildServer({ config, auth, store, collector, experiments }: ServerDeps) {
  const app = Fastify({ logger: { level: process.env.LOG_LEVEL ?? 'info' }, trustProxy: true });

  app.setErrorHandler((error, request, reply) => {
    if (error instanceof AuthError) return reply.code(error.status).send({ error: 'unauthorised', message: error.message });
    if (error instanceof ValidationError) return reply.code(422).send({ error: 'validation_failed', message: error.message, details: error.details });
    if (error instanceof ConfirmationRequiredError) {
      return reply.code(428).send({ error: 'confirmation_required', message: error.message, details: { reasons: error.reasons } });
    }
    if (error instanceof ConflictError || error instanceof StateError) return reply.code(409).send({ error: 'conflict', message: error.message });
    const status = (error as { statusCode?: number }).statusCode;
    if (status && status < 500) return reply.code(status).send({ error: 'bad_request', message: (error as Error).message });
    request.log.error({ err: error }, 'request failed');
    return reply.code(500).send({ error: 'server_error', message: (error as Error).message });
  });

  // Public: only what the browser needs before it can authenticate.
  app.get('/auth/config', async () => ({ authMode: config.auth.mode, googleClientId: config.auth.googleClientId }));
  app.get('/healthz', async () => ({ status: 'ok' }));

  app.addHook('onRequest', async (request: FastifyRequest) => {
    if (!request.url.startsWith('/api/')) return;
    request.identity = await auth.authenticate(request.headers);
  });

  const user = (request: FastifyRequest) => request.identity?.email ?? 'unknown';

  app.get('/api/me', async (request) => ({ email: user(request), authMode: config.auth.mode, verifiedBy: request.identity?.verified }));

  app.get('/api/meta', async () => ({
    approvedMaxInstances: config.approvedMaxInstances,
    limits: { ...config.limits, expensive: config.expensive },
    baseline: config.baseline,
    defaults: defaultExperiment(config),
    presets: presets(config),
    target: {
      baseUrl: config.ticketing.publicBaseUrl, migName: config.gcp.migName, zone: config.gcp.zone,
      autoscalerName: config.gcp.autoscalerName, region: config.gcp.region, projectId: config.gcp.projectId,
    },
    // Whether the queue server exists in this lab; experiments with the waiting room on are refused otherwise.
    waitingRoom: { deployed: config.waitingRoom.baseUrl !== null },
    costModel: costModel(config, collector.currentMig?.actualCount ?? config.baseline.minReplicas),
    links: consoleLinks(config),
  }));

  app.get('/api/state', async () => collector.snapshot());

  app.get('/api/stream', (request, reply) => {
    reply.hijack();
    const res = reply.raw;
    res.writeHead(200, {
      'Content-Type': 'text/event-stream; charset=utf-8',
      'Cache-Control': 'no-cache, no-transform',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    });
    const send = (event: string, data: unknown) => res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
    send('tick', { snapshot: collector.snapshot(), append: emptyAppend() });
    const unsubscribe = collector.subscribe({ tick: (snapshot, append) => send('tick', { snapshot, append }), timeline: (e) => send('timeline', e) });
    const onExperiment = (e: unknown) => send('experiment', e);
    experimentListeners.add(onExperiment);
    const ping = setInterval(() => res.write(': ping\n\n'), 15_000);
    request.raw.on('close', () => { clearInterval(ping); unsubscribe(); experimentListeners.delete(onExperiment); });
  });

  const experimentListeners = new Set<(e: unknown) => void>();
  experiments.onChange = (e) => experimentListeners.forEach((l) => l(e));

  app.post('/api/experiments', async (request, reply) => reply.code(201).send(await experiments.create(request.body, user(request))));
  app.post<{ Params: { id: string } }>('/api/experiments/:id/start', async (r) => { await experiments.start(r.params.id, user(r)); return experiments.experiment; });
  app.post<{ Params: { id: string } }>('/api/experiments/:id/stop', async (r) => { await experiments.stopTraffic(r.params.id, user(r)); return experiments.experiment; });
  app.post<{ Params: { id: string } }>('/api/experiments/:id/complete', async (r) => { await experiments.complete(r.params.id, user(r)); return experiments.experiment; });
  app.post<{ Params: { id: string } }>('/api/experiments/:id/reset-inventory', async (r) => {
    await experiments.resetInventory(r.params.id, user(r));
    return { ok: true };
  });
  app.post('/api/reset', async (r) => { await experiments.reset(user(r)); return { ok: true }; });

  app.get('/api/experiments', async () => {
    const list = await store.listExperiments(100);
    return list.map((e) => ({
      id: e.id, name: e.name, preset: e.config.preset, state: e.state, createdAt: e.createdAt,
      trafficStartedAt: e.trafficStartedAt, trafficStoppedAt: e.trafficStoppedAt, completedAt: e.completedAt,
      durationSeconds: e.trafficStartedAt && e.trafficStoppedAt
        ? Math.round((Date.parse(e.trafficStoppedAt) - Date.parse(e.trafficStartedAt)) / 1000) : null,
      peakRps: e.report?.measured.peakRps ?? null,
      maxServers: e.report?.measured.maxObservedServerCount ?? null,
      ticketsSold: e.report?.measured.ticketsSold ?? null,
      invariantViolations: e.report?.measured.invariantViolations ?? null,
      minServers: e.config.servers.min,
      // null while the scale-in after completion is still observed (and for reports older than it).
      scaleIn: e.report?.measured.scaleIn ?? null,
    }));
  });

  const load = async (id: string) => {
    const e = await store.getExperiment(id);
    if (!e) throw Object.assign(new Error('Experiment not found'), { statusCode: 404 });
    return e;
  };
  const series = async (id: string): Promise<Series> => {
    const [buckets, stored, events] = await Promise.all([store.listTrafficBuckets(id), store.readSeries(id), store.listEvents(id)]);
    return { traffic: buckets.map(toTrafficPoint), ...stored, events };
  };

  app.get<{ Params: { id: string } }>('/api/experiments/:id', async (r) => load(r.params.id));
  app.get<{ Params: { id: string } }>('/api/experiments/:id/series', async (r) => { await load(r.params.id); return series(r.params.id); });
  app.get<{ Params: { id: string } }>('/api/experiments/:id/report', async (r) => {
    const e = await load(r.params.id);
    if (e.report) return e.report;
    // Interim report for an experiment that has not finished (or failed before reporting).
    const [buckets, stored, events] = await Promise.all([store.listTrafficBuckets(e.id), store.readSeries(e.id), store.listEvents(e.id)]);
    return buildReport({ experiment: e, buckets, series: stored, events, finalInventory: e.id === experiments.experiment?.id ? collector.currentInventory : null }, config);
  });
  app.get<{ Params: { id: string } }>('/api/experiments/:id/export.json', async (r, reply) => {
    const e = await load(r.params.id);
    const body = { experiment: e, series: await series(e.id) };
    return reply.header('Content-Disposition', `attachment; filename="${e.id}.json"`).type('application/json').send(JSON.stringify(body, null, 2));
  });
  app.get<{ Params: { id: string }; Querystring: { kind?: string } }>('/api/experiments/:id/export.csv', async (r, reply) => {
    const e = await load(r.params.id);
    const kind = r.query.kind === 'events' ? 'events' : 'timeseries';
    const csv = kind === 'events'
      ? eventsCsv(await store.listEvents(e.id))
      : timeseriesCsv(await store.listTrafficBuckets(e.id), await store.readSeries(e.id));
    return reply.header('Content-Disposition', `attachment; filename="${e.id}-${kind}.csv"`).type('text/csv').send(csv);
  });

  if (config.staticDir && existsSync(config.staticDir)) {
    void app.register(fastifyStatic, { root: config.staticDir, wildcard: false });
    app.setNotFoundHandler((request, reply: FastifyReply) => {
      if (request.url.startsWith('/api/') || request.method !== 'GET') return reply.code(404).send({ error: 'not_found', message: 'Not found' });
      return reply.sendFile('index.html');
    });
  }

  return app;
}

function consoleLinks(config: AppConfig): Record<string, string> {
  const p = config.gcp.projectId;
  const z = config.gcp.zone;
  return {
    mig: `https://console.cloud.google.com/compute/instanceGroups/details/${z}/${config.gcp.migName}?project=${p}`,
    vms: `https://console.cloud.google.com/compute/instances?project=${p}`,
    loadBalancer: `https://console.cloud.google.com/net-services/loadbalancing/list/loadBalancers?project=${p}`,
    monitoring: `https://console.cloud.google.com/monitoring/metrics-explorer?project=${p}`,
    logs: `https://console.cloud.google.com/logs/query;query=resource.type%3D%22gce_instance%22?project=${p}`,
    cloudSql: `https://console.cloud.google.com/sql/instances?project=${p}`,
    billing: `https://console.cloud.google.com/billing/linkedaccount?project=${p}`,
  };
}
