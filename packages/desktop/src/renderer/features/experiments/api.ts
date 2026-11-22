/**
 * Experiments IPC adapter. Components never name a channel: this module is the
 * single place that maps a feature action to its shared command, exactly as the
 * ideas/kanban adapters do.
 *
 * The AI output commands belong to the `ai` module but are used only here for
 * the manual edit and confirm flow of an experiment analysis. The snapshot
 * itself stays append-only; only the live draft changes.
 */

import type {
  AiAvailability,
  AiOutput,
  AnalysisKind,
  ComparisonTable,
  CompareRequest,
  ExperimentDetail,
  ExperimentPatch,
  ExperimentSummary,
  ExperimentView,
  GitInfo,
  MetricSet,
  RunCreateInput,
  RunDetail,
  RunPatch,
  RunSummary,
  RunView,
  ScanPlan,
  ScanRules,
  SnapshotCreateRequest,
  SnapshotDetail,
  SnapshotSummary,
  TargetRef
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export interface ExperimentsListParams {
  readonly q?: string
  readonly includeDeleted?: boolean
  readonly limit?: number
  readonly offset?: number
}

export interface SnapshotListParams {
  readonly kind?: AnalysisKind
  readonly limit?: number
  readonly offset?: number
}

export const experimentsApi = {
  list: (projectId: string, params: ExperimentsListParams = {}): Promise<ExperimentSummary[]> => {
    return ipc.invoke('experiments.list', {
      projectId,
      ...(params.q === undefined || params.q.trim() === '' ? {} : { q: params.q.trim() }),
      ...(params.includeDeleted === undefined
        ? {}
        : { includeDeleted: params.includeDeleted }),
      ...(params.limit === undefined ? {} : { limit: params.limit }),
      ...(params.offset === undefined ? {} : { offset: params.offset })
    })
  },

  get: (projectId: string, experimentId: string): Promise<ExperimentDetail> =>
    ipc.invoke('experiments.get', { projectId, experimentId }),

  create: (
    projectId: string,
    input: { title: string; purpose?: string; dirRef?: string; scanRules?: ScanRules }
  ): Promise<ExperimentView> =>
    ipc.invoke('experiments.create', {
      projectId,
      title: input.title,
      ...(input.purpose === undefined || input.purpose.trim() === ''
        ? {}
        : { purpose: input.purpose }),
      ...(input.dirRef === undefined || input.dirRef.trim() === ''
        ? {}
        : { dirRef: input.dirRef }),
      ...(input.scanRules === undefined ? {} : { scanRules: input.scanRules })
    }),

  update: (
    projectId: string,
    experimentId: string,
    patch: ExperimentPatch,
    expectedUpdatedAt?: string
  ): Promise<ExperimentView> =>
    ipc.invoke('experiments.update', {
      projectId,
      experimentId,
      patch,
      ...(expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt })
    }),

  remove: (projectId: string, experimentId: string): Promise<{ deleted: boolean }> =>
    ipc.invoke('experiments.delete', { projectId, experimentId }),

  scanPreview: (
    projectId: string,
    target: TargetRef,
    override?: ScanRules
  ): Promise<ScanPlan> =>
    ipc.invoke('experiments.scan.preview', {
      projectId,
      target,
      ...(override === undefined ? {} : { override })
    }),

  scanStart: (projectId: string, target: TargetRef, override?: ScanRules): Promise<{ jobId: string }> =>
    ipc.invoke('experiments.scan.start', {
      projectId,
      target,
      ...(override === undefined ? {} : { override })
    }),

  runs: (projectId: string, experimentId: string): Promise<RunSummary[]> =>
    ipc.invoke('experiments.runs.list', { projectId, experimentId }),

  createRun: (input: RunCreateInput): Promise<RunView> =>
    ipc.invoke('experiments.runs.create', input),

  updateRun: (
    projectId: string,
    runId: string,
    patch: RunPatch,
    expectedUpdatedAt?: string
  ): Promise<RunDetail> =>
    ipc.invoke('experiments.runs.update', {
      projectId,
      runId,
      patch,
      ...(expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt })
    }),

  deleteRun: (projectId: string, runId: string): Promise<{ deleted: boolean }> =>
    ipc.invoke('experiments.runs.delete', { projectId, runId }),

  snapshots: (
    projectId: string,
    target: TargetRef,
    params: SnapshotListParams = {}
  ): Promise<SnapshotSummary[]> =>
    ipc.invoke('experiments.snapshots.list', {
      projectId,
      target,
      ...(params.kind === undefined ? {} : { kind: params.kind }),
      ...(params.limit === undefined ? {} : { limit: params.limit }),
      ...(params.offset === undefined ? {} : { offset: params.offset })
    }),

  snapshot: (projectId: string, snapshotId: string): Promise<SnapshotDetail> =>
    ipc.invoke('experiments.snapshots.get', { projectId, snapshotId }),

  createSnapshot: (request: SnapshotCreateRequest): Promise<SnapshotDetail> =>
    ipc.invoke('experiments.snapshots.create', request),

  metrics: (projectId: string, target: TargetRef): Promise<MetricSet> =>
    ipc.invoke('experiments.metrics.extract', { projectId, target }),

  gitInfo: (projectId: string, target: TargetRef): Promise<GitInfo> =>
    ipc.invoke('experiments.git.info', { projectId, target }),

  compare: (request: CompareRequest): Promise<ComparisonTable> =>
    ipc.invoke('experiments.compare', request),

  generateOutline: (
    projectId: string,
    experimentId: string,
    extraInstructions?: string
  ): Promise<{ jobId: string }> =>
    ipc.invoke('experiments.ai.outline', {
      projectId,
      experimentId,
      ...(extraInstructions === undefined || extraInstructions.trim() === ''
        ? {}
        : { extraInstructions })
    }),

  generateResults: (
    projectId: string,
    target: TargetRef,
    extraInstructions?: string
  ): Promise<{ jobId: string }> =>
    ipc.invoke('experiments.ai.results', {
      projectId,
      target,
      ...(extraInstructions === undefined || extraInstructions.trim() === ''
        ? {}
        : { extraInstructions })
    }),

  /** Chat-branch availability; generation stays disabled when it is false. */
  aiAvailability: (): Promise<AiAvailability> => ipc.invoke('ai.availability', {}),

  /** Read the live draft behind a snapshot's frozen analysis. */
  aiOutput: (projectId: string, outputId: string): Promise<AiOutput> =>
    ipc.invoke('ai.outputs.get', { projectId, outputId }),

  updateAiOutput: (projectId: string, outputId: string, contentMd: string): Promise<AiOutput> =>
    ipc.invoke('ai.outputs.update', { projectId, outputId, contentMd }),

  confirmAiOutput: (
    projectId: string,
    outputId: string,
    contentMd?: string
  ): Promise<AiOutput> =>
    ipc.invoke('ai.outputs.confirm', {
      projectId,
      outputId,
      ...(contentMd === undefined ? {} : { contentMd })
    })
}

