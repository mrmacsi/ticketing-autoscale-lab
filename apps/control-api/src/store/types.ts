import type {
  CpuPoint, Experiment, GeneratorStatus, InventoryPoint, ScalingPoint, TimelineEvent, TrafficBucket, TrafficConfig, WaitingRoomPoint,
} from '../types.js';

export class ConflictError extends Error {}

export type SeriesKind = 'scaling' | 'cpu' | 'inventory' | 'waitingRoom';
export type SeriesChunk = { scaling: ScalingPoint[]; cpu: CpuPoint[]; inventory: InventoryPoint[]; waitingRoom: WaitingRoomPoint[] };

export const emptySeriesChunk = (): SeriesChunk => ({ scaling: [], cpu: [], inventory: [], waitingRoom: [] });

export interface GeneratorCommand {
  action: 'start' | 'stop';
  experimentId: string;
  config?: TrafficConfig;
  issuedBy: string;
}

/** Durable experiment state, shared with the load generator (Firestore in GCP). */
export interface LabStore {
  /** Atomically claims the single lab lock; throws ConflictError when another experiment holds it. */
  createExperiment(experiment: Experiment, isActive: (id: string) => Promise<boolean>): Promise<void>;
  activeExperimentId(): Promise<string | null>;
  releaseLock(experimentId: string): Promise<void>;
  updateExperiment(id: string, patch: Partial<Omit<Experiment, 'generator'>>): Promise<void>;
  getExperiment(id: string): Promise<Experiment | null>;
  listExperiments(limit: number): Promise<Experiment[]>;

  addEvent(experimentId: string, event: TimelineEvent): Promise<void>;
  listEvents(experimentId: string): Promise<TimelineEvent[]>;
  appendSeries(experimentId: string, chunk: SeriesChunk): Promise<void>;
  readSeries(experimentId: string): Promise<SeriesChunk>;

  listTrafficBuckets(experimentId: string): Promise<TrafficBucket[]>;
  watchTrafficBuckets(experimentId: string, onBucket: (bucket: TrafficBucket) => void, onError: (e: Error) => void): () => void;

  sendGeneratorCommand(command: GeneratorCommand): Promise<number>;
  generatorStatus(): Promise<GeneratorStatus | null>;
}
