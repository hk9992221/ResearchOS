/**
 * Experiments module (rewrite plan §4.6). Public surface for IPC, MCP
 * and tests: one service, the pure scan/metadata/metric/extract/git/matrix
 * helpers, and the 17 MCP tool definitions.
 */
export {
  ExperimentsService,
  type AnalysisOptions,
  type CompareQuery,
  type CreateExperimentInput,
  type CreateRunInput,
  type ExperimentListFilter,
  type ExperimentsIndexHook,
  type ExperimentsServiceOptions,
  type UpdateExperimentInput,
  type UpdateRunInput
} from './service'

export {
  EXPERIMENTS_RUNTIME_KEY,
  experimentsToolByName,
  experimentsTools,
  type ExperimentsJobPort,
  type ExperimentsJobRequest,
  type ExperimentsMcpRuntime
} from './mcp'

export {
  createMetricRegistry,
  EXTRACTOR_CSV_SCALARS,
  EXTRACTOR_JSON_LEAVES,
  MetricRegistry,
  type MetricExtractor,
  type MetricFileAccessor
} from './metrics'

export { analysisJsonSchema, type ExperimentsAiPort, type StructuredGenerationRequest, type StructuredGenerationResult } from './ai'
export { createExperimentsAiPort, type ExperimentsAiAdapterDeps } from './ai-adapter'

export { buildContext, stripNotebookOutputs, truncateUtf8, type ContextBundle, type ContextEntry } from './context'
export { inspectGit, gitDiff, type GitDiff, type GitDiffFile, type GitDiffOptions } from './git'
export { extractMetadata } from './metadata'
export { buildComparisonMatrix, formatComparisonTable, inferMetricDirection, runDisplayName } from './matrix'
export {
  compileGlob,
  defaultScanRules,
  normalizeScanRules,
  mergeScanRules,
  RuleSet,
  validateScanRules,
  type CompiledGlob,
  type EffectiveScanRules
} from './rules'
export { classifyFile, scanDirectory, type ScanRequest } from './scan'
export {
  DEFAULT_MAX_FILE_BYTES,
  DEFAULT_MAX_TOTAL_BYTES,
  DEFAULT_MAX_FILES,
  DEFAULT_HASH_MAX_BYTES
} from './rules'
export { ExperimentErrorCode, SnapshotPersistError } from './errors'
export {
  appendSnapshot,
  decodeScanRules,
  encodeScanRules,
  loadExperiment,
  loadRun,
  loadSnapshot,
  type ExperimentRow,
  type RunRow,
  type SnapshotRow
} from './store'
export { percentChange, alignmentKey, walkJsonLeaves } from './metrics'