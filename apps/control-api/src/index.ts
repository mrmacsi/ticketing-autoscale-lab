import { pino } from 'pino';
import { Authenticator } from './auth/auth.js';
import { loadConfig } from './config.js';
import { ExperimentService } from './experiments/service.js';
import { GcpInfra } from './infra/gcp.js';
import { UnavailableInfra } from './infra/types.js';
import { buildServer } from './server.js';
import { FirestoreStore } from './store/firestore.js';
import { MemoryStore } from './store/memory.js';
import type { LabStore } from './store/types.js';
import { Collector } from './telemetry/collector.js';
import { TicketingAdmin } from './ticketing/admin.js';
import { InventoryReader } from './ticketing/inventory.js';
import { WaitingRoomClient } from './ticketing/waitingRoom.js';

const log = pino({ level: process.env.LOG_LEVEL ?? 'info', messageKey: 'message' });
const config = loadConfig();

const useFirestore = config.gcp.enabled || Boolean(process.env.FIRESTORE_EMULATOR_HOST);
const store: LabStore = useFirestore ? new FirestoreStore(config.gcp.projectId, config.gcp.firestoreDatabase) : new MemoryStore();
if (!useFirestore) log.warn('Using the in-memory store: experiment history is lost on restart and no generator can connect');
const infra = config.gcp.enabled ? new GcpInfra(config.gcp) : new UnavailableInfra();
const inventory = new InventoryReader(config.database);
const waitingRoom = new WaitingRoomClient(config.waitingRoom.baseUrl, config.ticketing.adminToken);
if (!waitingRoom.available) log.warn('WAITING_ROOM_URL is not set: the waiting room is not deployed and experiments cannot enable it');
const collector = new Collector(config, infra, store, inventory, log, waitingRoom);
const admin = new TicketingAdmin(config.ticketing.adminToken, () => collector.adminBaseUrl());
const experiments = new ExperimentService(config, store, infra, collector, admin, inventory, log, undefined, waitingRoom);

const server = buildServer({ config, auth: new Authenticator(config.auth, config.onCloudRun), store, collector, experiments });

collector.start();
await experiments.init();
await server.listen({ port: config.port, host: '0.0.0.0' });
log.info({ authMode: config.auth.mode, gcp: config.gcp.enabled, firestore: useFirestore }, 'control API listening');

const shutdown = async () => {
  experiments.stop();
  await collector.flushSeries();
  collector.stop();
  await server.close();
  await inventory.close();
  process.exit(0);
};
process.on('SIGTERM', () => void shutdown());
process.on('SIGINT', () => void shutdown());
