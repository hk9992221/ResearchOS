/**
 * Ports and options of the Ideas module. The module owns its domain semantics
 * but not the cross-cutting services: activities, jobs, AI structured chat and
 * confirmations arrive as narrow structural ports so the same service serves
 * IPC (actor=user) and MCP (actor=mcp).
 */
import type {
  AppErrorShape,
  EvidenceDirection,
  EvidenceExtractionModelInfo,
  EvidenceSkippedDraft,
  EvidenceSourceType,
  IdeaStatus,
  JsonObject
} from '@researchos/shared'

import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { ProjectHandle } from '../../workspace/handle'
import type { ActivityEvent, EventTx } from '../../events'
import type { Logger } from '../../logger'
import type { AiGateway } from '../ai/gateway'

export type IdeasActor = 'user' | 'mcp'

export interface IdeasWorkspace {
  project(projectId: string): ProjectHandle
  readonly global: { readonly db: Db }
}

/** The single activities writer; EventBus satisfies this. */
export interface EventSink {
  emit(tx: EventTx, event: ActivityEvent): unknown
}

/* -------------------------------------------------------------- source port */

export interface ResolvedIdeaSource {
  title: string
  text: string
  /** Source `updated_at` (or content hash) recorded in provenance. */
  version: string
  truncated: boolean
  deleted: boolean
}

/**
 * Resolves experiment/run/note text for the extraction skill. The default
 * implementation reads the project database and note files; modules may
 * override it in wiring.
 */
export interface IdeaSourceResolver {
  resolve(projectId: string, ref: { type: EvidenceSourceType; id: string }): Promise<ResolvedIdeaSource>
}

/* ----------------------------------------------------------------- job port */

export interface ExtractionJobResult {
  runId: string
  draftIds: string[]
  skipped: EvidenceSkippedDraft[]
  modelInfo: EvidenceExtractionModelInfo | null
}

export interface ExtractionJobContext {
  readonly jobId: string
  readonly signal?: AbortSignal
  progress(percent: number, stage: string): void
}

export interface ExtractionJobSpec {
  projectId: string
  kind: 'ideas.evidence_extract'
  params: JsonObject
  run(context: ExtractionJobContext): Promise<ExtractionJobResult>
}

export interface JobStatus<TResult> {
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  /** The job row's project, so a preview can resolve draft views. */
  projectId: string
  result?: TResult
  /** Typed cause of a failed job (never `INTERNAL` for a domain failure). */
  error?: AppErrorShape
}

/** The MCP job manager (§3.6); one row per job, cancel via AbortSignal. */
export interface JobsPort {
  enqueue(spec: ExtractionJobSpec): Promise<{ jobId: string }> | { jobId: string }
  get(jobId: string): Promise<JobStatus<ExtractionJobResult> | undefined> | JobStatus<ExtractionJobResult> | undefined
}

/* -------------------------------------------------------------------- options */

export interface IdeasServiceOptions {
  workspace: IdeasWorkspace
  events: EventSink
  clock: Clock
  /** Absent disables the skill with `AI_UNAVAILABLE` (`ai.disabled`). */
  ai?: AiGateway
  /** Defaults to the database/note-file resolver. */
  sources?: IdeaSourceResolver
  jobs?: JobsPort
  logger?: Logger
}

/* ------------------------------------------------------------ write inputs */

export interface UpdateIdeaPatch {
  title?: string
  description?: string
}

export interface WriteOptions {
  actor: IdeasActor
  expectedUpdatedAt?: string
}

export interface ChangeStatusInput extends WriteOptions {
  status: IdeaStatus
  rejectionReason?: string
  note?: string
}

export interface AddEvidenceInput extends WriteOptions {
  ideaId: string
  direction: EvidenceDirection
  content: string
  source: { type: EvidenceSourceType; id: string }
}

export interface UpdateEvidenceInput extends WriteOptions {
  evidenceId: string
  direction?: EvidenceDirection
  content?: string
}

export interface ListIdeasInput {
  statuses?: IdeaStatus[]
  text?: string
  includeDeleted?: boolean
  limit?: number
  cursor?: string
}

export interface ListEvidenceInput {
  ideaId?: string
  direction?: EvidenceDirection
  confirmed?: boolean
  includeDeleted?: boolean
  limit?: number
  cursor?: string
}

export interface HistoryInput {
  ideaId: string
  kinds?: string[]
  limit?: number
  cursor?: string
}

export interface ExtractDraftsInput {
  ideaId: string
  sourceRefs: Array<{ type: 'experiment' | 'run' | 'note'; id: string }>
  instruction?: string
  maxDrafts?: number
  actor: IdeasActor
  signal?: AbortSignal
}

export interface ExtractDraftsResult {
  runId: string
  draftIds: string[]
  skipped: EvidenceSkippedDraft[]
  modelInfo: EvidenceExtractionModelInfo | null
}

export interface EvidenceBatchFailure {
  id: string
  code: string
  message: string
}

export interface EvidenceBatchConfirmResult {
  confirmed: string[]
  failed: EvidenceBatchFailure[]
}

export interface EvidenceBatchDiscardResult {
  discarded: string[]
  failed: EvidenceBatchFailure[]
}