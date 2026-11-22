/**
 * Public surface of the Ideas module: one service, its ports, the 14 MCP tool
 * definitions and the stable error reasons.
 */
export { IdeasService } from './service'
export { IDEA_ERROR, ideaError } from './errors'
export { IDEA_LIMITS } from './limits'
export { DatabaseSourceResolver } from './sources'
export {
  IDEAS_RUNTIME_KEY,
  createEvidenceConfirmEffect,
  ideasTools,
  type IdeasMcpRuntime
} from './mcp'
export type {
  AddEvidenceInput,
  ChangeStatusInput,
  EvidenceBatchConfirmResult,
  EvidenceBatchDiscardResult,
  EvidenceBatchFailure,
  ExtractDraftsInput,
  ExtractDraftsResult,
  ExtractionJobContext,
  ExtractionJobResult,
  ExtractionJobSpec,
  IdeaSourceResolver,
  IdeasActor,
  IdeasServiceOptions,
  IdeasWorkspace,
  JobStatus,
  JobsPort,
  ListEvidenceInput,
  ListIdeasInput,
  ResolvedIdeaSource,
  UpdateEvidenceInput,
  UpdateIdeaPatch,
  WriteOptions
} from './types'
