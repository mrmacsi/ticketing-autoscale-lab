import { Firestore, Timestamp, FieldValue } from '@google-cloud/firestore';
import type { Experiment, GeneratorStatus, TimelineEvent, TrafficBucket } from '../types.js';
import { ConflictError, emptySeriesChunk, type GeneratorCommand, type LabStore, type SeriesChunk } from './types.js';

/** Converts Firestore Timestamps (written by the Go generator) to ISO strings, recursively. */
export function plain<T>(value: unknown): T {
  if (value instanceof Timestamp) return value.toDate().toISOString() as T;
  if (Array.isArray(value)) return value.map((v) => plain(v)) as T;
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, plain(v)])) as T;
  }
  return value as T;
}

export class FirestoreStore implements LabStore {
  private readonly db: Firestore;

  constructor(projectId: string, databaseId: string) {
    this.db = new Firestore({ projectId, databaseId, ignoreUndefinedProperties: true });
  }

  private experiments() { return this.db.collection('experiments'); }
  private lockRef() { return this.db.collection('lab').doc('lock'); }

  async createExperiment(experiment: Experiment, isActive: (id: string) => Promise<boolean>): Promise<void> {
    const current = await this.activeExperimentId();
    // Stale locks (experiment finished but lock left behind after a crash) are cleared first.
    if (current && !(await isActive(current))) await this.releaseLock(current);

    await this.db.runTransaction(async (tx) => {
      const lock = await tx.get(this.lockRef());
      const holder = lock.exists ? (lock.data()?.experimentId as string | null) : null;
      if (holder) throw new ConflictError(`Experiment ${holder} is still active`);
      tx.set(this.lockRef(), { experimentId: experiment.id, claimedAt: FieldValue.serverTimestamp() });
      tx.create(this.experiments().doc(experiment.id), { ...experiment, generator: null });
    });
  }

  async activeExperimentId(): Promise<string | null> {
    const lock = await this.lockRef().get();
    return lock.exists ? ((lock.data()?.experimentId as string | null) ?? null) : null;
  }

  async releaseLock(experimentId: string): Promise<void> {
    await this.db.runTransaction(async (tx) => {
      const lock = await tx.get(this.lockRef());
      if (lock.exists && lock.data()?.experimentId === experimentId) tx.set(this.lockRef(), { experimentId: null });
    });
  }

  async updateExperiment(id: string, patch: Partial<Omit<Experiment, 'generator'>>): Promise<void> {
    // update() touches only the given fields, so the generator's `generator` map is never overwritten.
    await this.experiments().doc(id).update(patch as Record<string, unknown>);
  }

  async getExperiment(id: string): Promise<Experiment | null> {
    const doc = await this.experiments().doc(id).get();
    return doc.exists ? plain<Experiment>(doc.data()) : null;
  }

  async listExperiments(limit: number): Promise<Experiment[]> {
    const snap = await this.experiments().orderBy('createdAt', 'desc').limit(limit).get();
    return snap.docs.map((d) => plain<Experiment>(d.data()));
  }

  async addEvent(experimentId: string, event: TimelineEvent): Promise<void> {
    await this.experiments().doc(experimentId).collection('events').doc(event.id).set(event);
  }

  async listEvents(experimentId: string): Promise<TimelineEvent[]> {
    const snap = await this.experiments().doc(experimentId).collection('events').orderBy('t').get();
    return snap.docs.map((d) => plain<TimelineEvent>(d.data()));
  }

  async appendSeries(experimentId: string, chunk: SeriesChunk): Promise<void> {
    if (!chunk.scaling.length && !chunk.cpu.length && !chunk.inventory.length && !chunk.waitingRoom.length) return;
    await this.experiments().doc(experimentId).collection('series').add({ ...chunk, writtenAt: Date.now() });
  }

  async readSeries(experimentId: string): Promise<SeriesChunk> {
    const snap = await this.experiments().doc(experimentId).collection('series').orderBy('writtenAt').get();
    const merged = emptySeriesChunk();
    for (const doc of snap.docs) {
      const data = doc.data() as Partial<SeriesChunk>;
      merged.scaling.push(...(data.scaling ?? []));
      merged.cpu.push(...(data.cpu ?? []));
      merged.inventory.push(...(data.inventory ?? []));
      merged.waitingRoom.push(...(data.waitingRoom ?? []));   // absent in chunks written before the waiting room
    }
    return merged;
  }

  async listTrafficBuckets(experimentId: string): Promise<TrafficBucket[]> {
    const snap = await this.experiments().doc(experimentId).collection('traffic').orderBy('second').get();
    return snap.docs.map((d) => plain<TrafficBucket>(d.data()));
  }

  watchTrafficBuckets(experimentId: string, onBucket: (b: TrafficBucket) => void, onError: (e: Error) => void): () => void {
    return this.experiments().doc(experimentId).collection('traffic').orderBy('second').onSnapshot(
      (snap) => {
        for (const change of snap.docChanges()) {
          if (change.type !== 'removed') onBucket(plain<TrafficBucket>(change.doc.data()));
        }
      },
      onError,
    );
  }

  async sendGeneratorCommand(command: GeneratorCommand): Promise<number> {
    const ref = this.db.collection('generator').doc('command');
    return this.db.runTransaction(async (tx) => {
      const current = await tx.get(ref);
      const seq = ((current.data()?.seq as number | undefined) ?? 0) + 1;
      tx.set(ref, { ...command, seq, issuedAt: FieldValue.serverTimestamp() });
      return seq;
    });
  }

  async generatorStatus(): Promise<GeneratorStatus | null> {
    const doc = await this.db.collection('generator').doc('status').get();
    return doc.exists ? plain<GeneratorStatus>(doc.data()) : null;
  }
}
