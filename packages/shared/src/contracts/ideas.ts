import type { ActivityActor } from '../activity'
import type { Evidence, EvidenceDirection, EvidenceSourceType, Idea, IdeaStatus } from '../entities'
import type { AppErrorShape } from '../errors'
import type { EntityId, IsoDateTime, Locator, Page } from '../types/common'

/* ------------------------------------------------------------- ideas view */

export interface IdeaPatch {
  title?: string
  description?: string
}

/**
 * History classification. The first seven map 1:1 onto activity event types;
 * `derived` entries are synthesized from `derived_from` relations at read
 * time and never correspond to an activity row.
 */
export const IDEA_HISTORY_KINDS = [
  'created',
  'status',
  'relation',
  'evidence',
  'deleted',
  'restored',
  'ai',
  'derived'
] as const

export type IdeaHistoryKind = (typeof IDEA_HISTORY_KINDS)[number]

/** One resolved relation/evidence endpoint. Missing targets stay visible. */
export interface IdeaEntityRef {
  type: string
  id: EntityId
  title: string
  deleted: boolean
  /** The row is gone entirely (or a paper is no longer linked). */
  brokenRef: boolean
}

export interface IdeaRelationSet {
  papers: IdeaEntityRef[]
  notes: IdeaEntityRef[]
  experiments: IdeaEntityRef[]
  derivedFrom: IdeaEntityRef[]
  derived: IdeaEntityRef[]
}

export interface IdeaHistoryEntry {
  at: IsoDateTime
  /** Activity id; null for synthesized `derived` entries. */
  activityId: EntityId | null
  kind: IdeaHistoryKind
  eventType: string
  summary: string
  actor: ActivityActor
  /** `status.changed` old/new status values; empty for other events. */
  old: string
  new: string
  ref: IdeaEntityRef | null
  synthesized: boolean
}

export interface IdeaEvidenceSummary {
  support: number
  oppose: number
  drafts: number
}

/** List row: the Idea plus the aggregates the list and readiness cards show. */
export interface IdeaSummary extends Idea {
  supportCount: number
  opposeCount: number
  draftCount: number
  linkedPapers: number
  linkedNotes: number
  linkedExperiments: number
  derivedFromId: EntityId | null
  /** First `derived_from` parent title, for the lineage badge. */
  derivedFromTitle: string | null
}

export interface IdeaDetail extends Idea {
  evidence: IdeaEvidenceSummary
  relations: IdeaRelationSet
  /** The most recent history entries, newest first. */
  history: IdeaHistoryEntry[]
}

/**
 * Cursor page. `total` is the unpaged match count; `nextCursor` is null on the
 * last page. `limit`/`offset` are kept for the shared `Page` contract so the
 * renderer can reason about the request it made.
 */
export interface IdeaPage extends Page<IdeaSummary> {
  nextCursor: string | null
}

export interface IdeaListQuery {
  projectId: string
  status?: IdeaStatus[]
  /** Title/description keyword, case-insensitive. */
  q?: string
  /** true = trash view (soft-deleted rows only). */
  includeDeleted?: boolean
  limit?: number
  cursor?: string
}

export interface IdeaHistoryPage {
  items: IdeaHistoryEntry[]
  total: number
  nextCursor: string | null
}

export interface IdeaHistoryQuery {
  projectId: string
  ideaId: string
  kinds?: IdeaHistoryKind[]
  limit?: number
  cursor?: string
}

/* ---------------------------------------------------------------- evidence */

/** Polymorphic evidence source; `locator` is a read-time hint, never stored. */
export interface EvidenceSourceRef {
  type: EvidenceSourceType
  id: EntityId
  locator?: Locator
}

export interface EvidenceCreateInput {
  projectId: string
  ideaId: string
  direction: EvidenceDirection
  content: string
  source: EvidenceSourceRef
}

export interface EvidencePatch {
  direction?: EvidenceDirection
  content?: string
}

export interface EvidenceListQuery {
  projectId: string
  ideaId?: string
  direction?: EvidenceDirection
  confirmed?: boolean
  /** true = trash view (soft-deleted rows only). */
  includeDeleted?: boolean
  limit?: number
  cursor?: string
}

export interface EvidenceView extends Evidence {
  source: EvidenceSourceRef
  sourceTitle: string
  sourceDeleted: boolean
  sourceBroken: boolean
}

export interface EvidencePage extends Page<EvidenceView> {
  nextCursor: string | null
}

/** One refused item of a batch confirm/discard; the rest still apply. */
export interface EvidenceBatchFailure {
  id: EntityId
  /** Stable domain code (`evidence.not_found`, `evidence.confirmed`, …). */
  code: string
  message: string
}

export interface EvidenceConfirmResult {
  confirmed: EntityId[]
  failed: EvidenceBatchFailure[]
}

export interface EvidenceDiscardResult {
  discarded: EntityId[]
  failed: EvidenceBatchFailure[]
}

/* --------------------------------------------------------------- AI extract */

export type EvidenceExtractionSourceType = 'experiment' | 'run' | 'note'

export interface EvidenceExtractionSourceRef {
  type: EvidenceExtractionSourceType
  id: EntityId
}

export interface EvidenceExtractionRequest {
  projectId: string
  ideaId: string
  /** 1..8 experiment/run/note refs. */
  sourceRefs: EvidenceExtractionSourceRef[]
  /** Optional focus, at most 500 characters. */
  instruction?: string
  /** 1..20, default 8. */
  maxDrafts?: number
}

export interface EvidenceSkippedDraft {
  type: EvidenceExtractionSourceType | ''
  id: EntityId | ''
  reason: string
  contentHash: string
}

export interface EvidenceExtractionModelInfo {
  provider: string
  model: string
}

/**
 * Preview of an extraction job. Drafts are real rows (`ai_generated=true`,
 * `confirmed=false`); `state` mirrors the job and `error` carries the typed
 * job failure (`AI_UNAVAILABLE`, `AI_OUTPUT_INVALID`, …), never `INTERNAL`
 * for a known domain failure.
 */
export interface EvidenceExtractionPreview {
  jobId: EntityId
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  drafts: EvidenceView[]
  skipped: EvidenceSkippedDraft[]
  modelInfo: EvidenceExtractionModelInfo | null
  warnings: string[]
  error?: AppErrorShape
}

/* ---------------------------------------------------------------- relations */

export const IDEA_LINK_TARGETS = ['paper', 'note', 'experiment'] as const
export type IdeaLinkTargetType = (typeof IDEA_LINK_TARGETS)[number]

export interface IdeaLinkTarget {
  type: IdeaLinkTargetType
  id: EntityId
}

/* ----------------------------------------------------------------- commands */

export interface IdeaCommands {
  'ideas.list': { req: IdeaListQuery; res: IdeaPage }
  'ideas.get': { req: { projectId: string; ideaId: string }; res: IdeaDetail }
  'ideas.create': { req: { projectId: string; title: string; description?: string }; res: Idea }
  'ideas.update': {
    req: { projectId: string; ideaId: string; patch: IdeaPatch; expectedUpdatedAt?: IsoDateTime }
    res: Idea
  }
  'ideas.changeStatus': {
    req: {
      projectId: string
      ideaId: string
      status: IdeaStatus
      rejectionReason?: string
      note?: string
      expectedUpdatedAt?: IsoDateTime
    }
    res: Idea
  }
  'ideas.delete': { req: { projectId: string; ideaId: string }; res: { deleted: boolean } }
  'ideas.restore': { req: { projectId: string; ideaId: string }; res: Idea }
  'ideas.history': { req: IdeaHistoryQuery; res: IdeaHistoryPage }
  'ideas.derive': {
    req: { projectId: string; parentIdeaId: string; title: string; description?: string }
    res: Idea
  }

  'ideas.evidence.list': { req: EvidenceListQuery; res: EvidencePage }
  'ideas.evidence.create': { req: EvidenceCreateInput; res: EvidenceView }
  'ideas.evidence.update': {
    req: { projectId: string; evidenceId: string; patch: EvidencePatch; expectedUpdatedAt?: IsoDateTime }
    res: EvidenceView
  }
  'ideas.evidence.delete': { req: { projectId: string; evidenceId: string }; res: { deleted: boolean } }
  'ideas.evidence.confirm': { req: { projectId: string; evidenceId: string }; res: EvidenceView }
  'ideas.evidence.discard': {
    req: { projectId: string; ideaId: string; evidenceId: string }
    res: { discarded: boolean }
  }
  'ideas.evidence.extract': { req: EvidenceExtractionRequest; res: { jobId: string } }
  'ideas.evidence.extractPreview': { req: { jobId: string }; res: EvidenceExtractionPreview }

  'ideas.links.add': {
    req: { projectId: string; ideaId: string; target: IdeaLinkTarget }
    res: { added: boolean }
  }
  'ideas.links.remove': {
    req: { projectId: string; ideaId: string; target: IdeaLinkTarget }
    res: { removed: boolean }
  }
}

