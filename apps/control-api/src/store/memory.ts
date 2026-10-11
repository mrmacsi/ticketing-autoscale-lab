import type { Experiment, GeneratorStatus, TimelineEvent, TrafficBucket } from '../types.js';
import { ConflictError, emptySeriesChunk, type GeneratorCommand, type LabStore, type SeriesChunk } from './types.js';

/** In-process store for unit tests and local development without Firestore. */
export class MemoryStore implements LabStore {
  experiments = new Map<string, Experiment>();
  events = new Map<string, TimelineEvent[]>();
  series = new Map<string, SeriesChunk>();
  buckets = new Map<string, TrafficBucket[]>();
  commands: Array<GeneratorCommand & { seq: number }> = [];
  status: GeneratorStatus | null = null;
  lock: string | null = null;
  private watchers = new Map<string, Set<(b: TrafficBucket) => void>>();

  async createExperiment(experiment: Experiment, isActive: (id: string) => Promise<boolean>): Promise<void> {
    if (this.lock && (await isActive(this.lock))) throw new ConflictError(`Experiment ${this.lock} is still active`);
    this.lock = experiment.id;
    this.experiments.set(experiment.id, structuredClone({ ...experiment, generator: null }));
  }
  async activeExperimentId() { return this.lock; }
  async releaseLock(id: string) { if (this.lock === id) this.lock = null; }
  async updateExperiment(id: string, patch: Partial<Experiment>) {
    const current = this.experiments.get(id);
    if (!current) throw new Error(`Unknown experiment ${id}`);
    this.experiments.set(id, { ...current, ...structuredClone(patch) });
  }
  async getExperiment(id: string) { return structuredClone(this.experiments.get(id) ?? null); }
  async listExperiments(limit: number) {
    return [...this.experiments.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt)).slice(0, limit).map((e) => structuredClone(e));
  }
  async addEvent(id: string, event: TimelineEvent) { this.events.set(id, [...(this.events.get(id) ?? []), event]); }
  async listEvents(id: string) { return [...(this.events.get(id) ?? [])].sort((a, b) => a.t.localeCompare(b.t)); }
  async appendSeries(id: string, chunk: SeriesChunk) {
    const s = this.series.get(id) ?? emptySeriesChunk();
    s.scaling.push(...chunk.scaling); s.cpu.push(...chunk.cpu); s.inventory.push(...chunk.inventory);
    s.waitingRoom.push(...(chunk.waitingRoom ?? []));
    this.series.set(id, s);
  }
  async readSeries(id: string) { return structuredClone(this.series.get(id) ?? emptySeriesChunk()); }
  async listTrafficBuckets(id: string) { return [...(this.buckets.get(id) ?? [])]; }
  watchTrafficBuckets(id: string, onBucket: (b: TrafficBucket) => void) {
    for (const b of this.buckets.get(id) ?? []) onBucket(b);
    const set = this.watchers.get(id) ?? new Set();
    set.add(onBucket);
    this.watchers.set(id, set);
    return () => set.delete(onBucket);
  }
  async sendGeneratorCommand(command: GeneratorCommand) {
    const seq = this.commands.length + 1;
    this.commands.push({ ...command, seq });
    return seq;
  }
  async generatorStatus() { return this.status; }

  /** Test helpers simulating the generator's writes. */
  pushBucket(id: string, bucket: TrafficBucket) {
    this.buckets.set(id, [...(this.buckets.get(id) ?? []), bucket]);
    for (const w of this.watchers.get(id) ?? []) w(bucket);
  }
  setGenerator(id: string, generator: Experiment['generator']) {
    const e = this.experiments.get(id);
    if (e) e.generator = generator;
  }
}
