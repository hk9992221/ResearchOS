import type { Experiment, RunStatus, SnapshotTargetType } from '../entities'
import type { IsoDateTime, JsonObject, JobRef, TargetRef } from '../types/common'

/* ------------------------------------------------------------------ scanning */

/** The ten registered file kinds. */
export const FILE_KINDS = [
  'code',
  'readme',
  'config',
  'data',
  'metrics',
  'log',
  'image',
  'notebook',
  'binary',
  'other'
] as const
export type FileKind = (typeof FILE_KINDS)[number]

/** The nine registered exclusion reasons. */
export const EXCLUSION_REASONS = [
  'too_large',
  'model_weight',
  'cache',
  'binary',
  'secret',
  'user_exclude',
  'archive_or_media',
  'symlink_outside',
  'unreadable'
] as const
export type ExclusionReason = (typeof EXCLUSION_REASONS)[number]

/**
 * The configurable rule set stored in `experiments.scan_rules`.
 * Every field is optional on input; the service fills the documented defaults.
 */
export interface ScanRules {
  include?: string[]
  exclude?: string[]
  maxFileBytes?: number
  maxTotalBytes?: number
  maxFiles?: number
  hashMaxBytes?: number
  followSymlinks?: boolean
  includeImagesInAi?: boolean
}

export interface ScanFileEntry {
  path: string
  kind: FileKind
  size: number
  mtime: IsoDateTime
  sha256: string | null
  hashSkipped: 'too_large' | 'binary' | null
  contentEligible: boolean
  exclusionReason: ExclusionReason | null
  sampled: boolean
}

export interface ExcludedEntry {
  path: string
  reason: ExclusionReason
}

export interface ScanWarning {
  code: string
  path?: string
  message: string
}

export interface ScanPlan {
  target: TargetRef
  root: string
  entries: ScanFileEntry[]
  pruned: ExcludedEntry[]
  totalFiles: number
  totalBytes: number
  includedFiles: number
  includedBytes: number
  contextBytes: number
  warnings: ScanWarning[]
  truncated: boolean
}

export interface ParseError {
  line?: number
  column?: number
  message: string
}

export interface ExtractedConfig {
  path: string
  sha256: string
  values: JsonObject
  truncated?: boolean
  parseError?: ParseError
}

export interface CsvSummary {
  path: string
  delimiter: string
  header: string[]
  columns: number
  rows: number
  parseError?: ParseError
}

export interface ReadmeSummary {
  path: string
  title: string
  headings: string[]
}

export interface DependencySet {
  path: string
  kind: 'requirements' | 'pyproject' | 'environment'
  dependencies: string[]
}

export interface EntryCandidate {
  path: string
  heuristic: true
}

export interface LogSummary {
  path: string
  errorCount: number
  warnCount: number
  lines: number
}

export interface ScanMetadata {
  totalFiles: number
  totalBytes: number
  kindCounts: Partial<Record<FileKind, number>>
  configs: ExtractedConfig[]
  csvs: CsvSummary[]
  readmes: ReadmeSummary[]
  dependencies: DependencySet[]
  entryPoints: EntryCandidate[]
  languages: Record<string, number>
  logs: LogSummary[]
  warnings: ScanWarning[]
}

export interface ScanResult {
  plan: ScanPlan
  metadata: ScanMetadata
  configs: ExtractedConfig[]
  gitInfo: GitInfo | null
  warnings: ScanWarning[]
}

/* --------------------------------------------------------------------- git */

export interface GitInfo {
  detected: boolean
  /** False when only the repository metadata could be read and CLI status failed. */
  available: boolean
  repoRoot: string
  branch: string | null
  commit: string | null
  commitShort: string | null
  commitTime: IsoDateTime | null
  dirty: boolean
  changedFiles: number
  untrackedFiles: number
  capturedAt: IsoDateTime
  source: 'read_at_analysis' | 'runtime_metadata'
  truncated: boolean
  error: string | null
}

/* ----------------------------------------------------------------- metrics */

export interface MetricSource {
  path: string
  pointer: string | null
  line: number | null
  sha256: string | null
}

export interface Metric {
  key: string
  label: string
  value: number | null
  raw: string
  unit: string | null
  context?: JsonObject
  source: MetricSource
  extractor: string
}

export interface UnparsedFile {
  path: string
  reason: string
}

export interface MetricSet {
  v: number
  metrics: Metric[]
  unparsed: UnparsedFile[]
}

export interface MetricRow {
  key: string
  context?: JsonObject
  values: Record<string, Metric>
  delta: Record<string, number>
  pct: Record<string, number>
}

export interface MetricComparison {
  runs: Array<{ runId: string; label: string }>
  rows: MetricRow[]
}

/* ------------------------------------------------------------ run parameters */

export interface ParamSource {
  path: string
  pointer?: string | null
  sha256?: string | null
}

/** `{value, source?, confirmed?}` — always stored as an object. */
export interface RunParamValue {
  value: string
  source?: ParamSource | null
  confirmed?: boolean
}

export type RunParams = Record<string, RunParamValue>

/* --------------------------------------------------------------- snapshots */

export type AnalysisKind = 'outline' | 'results'

export interface SnapshotSourceRef {
  path: string
  sha256?: string
  metricKeys?: string[]
}

export interface SnapshotSection {
  key: string
  title: string
  textMd: string
  sources: SnapshotSourceRef[]
  unconfirmed: boolean
  unconfirmedReason: string | null
}

export interface UnverifiedClaim {
  section: string
  text: string
  reason: string
}

export interface SnapshotAiConfig {
  scanRules: JsonObject
  runParams: RunParams
  extracted: ExtractedConfig[]
  comparison?: ComparisonMatrix | null
}

export interface SnapshotAiAnalysis {
  v: number
  kind: AnalysisKind
  aiOutputId: string
  promptVersion: string
  contentMd: string
  contentSha256: string
  sections: SnapshotSection[]
  unverifiedClaims: UnverifiedClaim[]
  config: SnapshotAiConfig
  warnings: string[]
}

export interface SnapshotProvenanceRef {
  entityType: string
  entityId: string
  locator: string
  title: string
}

export interface SnapshotModelInfo {
  v: number
  providerId: string
  providerName: string
  protocol: string
  modelId: string
  promptVersion: string
  parameters: JsonObject
  sourceRefs: SnapshotProvenanceRef[]
  generatedAt: IsoDateTime
}

export interface SnapshotDetail {
  id: string
  targetType: SnapshotTargetType
  targetId: string
  createdAt: IsoDateTime
  fileManifest: ScanFileEntry[]
  gitInfo: GitInfo | null
  metrics: MetricSet
  aiAnalysis: SnapshotAiAnalysis | null
  modelInfo: SnapshotModelInfo | null
}

export interface SnapshotSummary {
  id: string
  targetType: SnapshotTargetType
  targetId: string
  kind: AnalysisKind | null
  createdAt: IsoDateTime
  providerName: string
  modelId: string
  gitCommit: string
  metricCount: number
  warnings: number
  aiStatus: string
  humanEdited: boolean
  confirmedAt: IsoDateTime | null
}

export interface SnapshotCreateRequest {
  projectId: string
  target: TargetRef
  metrics?: JsonObject
  aiAnalysis?: string
  aiOutputId?: string
  /** Analysis flavour of `aiAnalysis`; defaults to `outline`. */
  kind?: AnalysisKind
}

/* -------------------------------------------------- experiment / run views */

export interface ExperimentView extends Omit<Experiment, 'scanRules'> {
  scanRules: ScanRules
}

export interface DirReference {
  refId: string
  ownerType: 'experiment' | 'run'
  ownerId: string
  path: string
  status: 'ok' | 'broken'
  exists: boolean
  reason: string
  lastCheckedAt: IsoDateTime | null
}

export interface ExperimentSummary {
  id: string
  title: string
  purpose: string
  dirRef: string | null
  dirStatus: 'ok' | 'broken' | null
  dirReason: string
  runCount: number
  lastRunStatus: RunStatus
  lastAnalyzedAt: IsoDateTime | null
  linkedIdeas: number
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export interface IdeaRef {
  id: string
  title: string
  targetDeleted: boolean
}

export interface ExperimentDetail {
  experiment: ExperimentView
  dirRefs: DirReference[]
  runs: RunSummary[]
  recentSnapshots: SnapshotSummary[]
  linkedIdeas: IdeaRef[]
}

export interface RunView {
  id: string
  experimentId: string
  title: string
  dirRef: string | null
  params: RunParams
  gitInfo: GitInfo | null
  status: RunStatus
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export interface RunSummary {
  id: string
  title: string
  displayName: string
  dirRef: string | null
  params: RunParams
  paramCount: number
  status: RunStatus
  lastAnalyzedAt: IsoDateTime | null
  deletedAt: IsoDateTime | null
}

export interface RunDetail extends RunView {
  dirRefs: DirReference[]
}

export interface RunCreateInput {
  projectId: string
  experimentId: string
  title?: string
  dirRef?: string | null
  params?: RunParams
  status?: RunStatus
}

export interface RunPatch {
  title?: string
  dirRef?: string | null
  params?: RunParams
  status?: RunStatus
  /** The MCP contract permits one change group per call; the service does not. */
  ack?: boolean
}

export interface ExperimentPatch {
  title?: string
  purpose?: string
  dirRef?: string | null
  scanRules?: ScanRules
}

/* ------------------------------------------------------------- comparison */

export type MetricDirection = 'higher' | 'lower'
export type TableFormat = 'latex_booktabs' | 'markdown' | 'csv'

export interface ComparisonRun {
  runId: string
  label: string
  gitShortSha: string
  dirty: boolean
  status: RunStatus
}

export interface ParamDiffRow {
  key: string
  values: Record<string, string>
  isDiff: boolean
}

export interface MetricComparisonRow {
  key: string
  context?: JsonObject
  values: Record<string, number | null>
  delta: Record<string, number>
  pct: Record<string, number>
  bestRunId: string | null
  secondBestRunId: string | null
  direction: MetricDirection
}

export interface ComparisonMatrix {
  experimentId: string | null
  baselineId: string | null
  runs: ComparisonRun[]
  paramRows: ParamDiffRow[]
  metricRows: MetricComparisonRow[]
}

export interface ComparisonTableOptions {
  format?: TableFormat
  caption?: string
  label?: string
  includeParams?: string[]
  includeMetrics?: string[]
  precision?: number
  highlightBest?: boolean
  showDelta?: boolean
}

export interface ComparisonTable {
  format: TableFormat
  table: string
  baselineId: string | null
  runs: ComparisonRun[]
  paramRows: ParamDiffRow[]
  metricRows: MetricComparisonRow[]
}

export interface CompareRequest {
  projectId: string
  experimentId?: string
  runIds?: string[]
  baselineId?: string
  diffParams?: boolean
  directions?: Record<string, MetricDirection>
  table?: ComparisonTableOptions
}

/* --------------------------------------------------------------- commands */

export interface ExperimentsCommands {
  'experiments.list': {
    req: { projectId: string; q?: string; includeDeleted?: boolean; limit?: number; offset?: number }
    res: ExperimentSummary[]
  }
  'experiments.get': { req: { projectId: string; experimentId: string }; res: ExperimentDetail }
  'experiments.create': {
    req: { projectId: string; title: string; purpose?: string; dirRef?: string; scanRules?: ScanRules }
    res: ExperimentView
  }
  'experiments.update': {
    req: { projectId: string; experimentId: string; patch: ExperimentPatch; expectedUpdatedAt?: IsoDateTime }
    res: ExperimentView
  }
  'experiments.delete': { req: { projectId: string; experimentId: string }; res: { deleted: boolean } }

  'experiments.scan.preview': {
    req: { projectId: string; target: TargetRef; override?: ScanRules }
    res: ScanPlan
  }
  'experiments.scan.start': { req: { projectId: string; target: TargetRef; override?: ScanRules }; res: JobRef }

  'experiments.runs.list': { req: { projectId: string; experimentId: string }; res: RunSummary[] }
  'experiments.runs.get': { req: { projectId: string; runId: string }; res: RunDetail }
  'experiments.runs.create': { req: RunCreateInput; res: RunView }
  'experiments.runs.update': {
    req: { projectId: string; runId: string; patch: RunPatch; expectedUpdatedAt?: IsoDateTime }
    res: RunDetail
  }
  'experiments.runs.delete': { req: { projectId: string; runId: string }; res: { deleted: boolean } }

  'experiments.snapshots.list': {
    req: { projectId: string; target: TargetRef; kind?: AnalysisKind; limit?: number; offset?: number }
    res: SnapshotSummary[]
  }
  'experiments.snapshots.get': { req: { projectId: string; snapshotId: string }; res: SnapshotDetail }
  'experiments.snapshots.create': { req: SnapshotCreateRequest; res: SnapshotDetail }

  'experiments.metrics.extract': { req: { projectId: string; target: TargetRef }; res: MetricSet }
  'experiments.git.info': { req: { projectId: string; target: TargetRef }; res: GitInfo }
  'experiments.compare': { req: CompareRequest; res: ComparisonTable }

  'experiments.ai.outline': {
    req: { projectId: string; experimentId: string; extraInstructions?: string }
    res: JobRef
  }
  'experiments.ai.results': {
    req: { projectId: string; target: TargetRef; extraInstructions?: string }
    res: JobRef
  }
}