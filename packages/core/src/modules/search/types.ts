import type { Rect } from '@researchos/shared'

import { AppErrorCode, appError } from '../../errors'

/**
 * Domain types of the unified retrieval layer.
 *
 * Storage primitives live in `../../index-store`; this module owns source
 * extraction, chunking, the coordinator and the hybrid query pipeline. Search
 * and indexing never write Activity rows and never mutate researched entities.
 */

export type SourceType =
  | 'paper_metadata'
  | 'paper_fulltext'
  | 'paper_note'
  | 'scratchpad'
  | 'db_record'
  | 'idea'
  | 'experiment_outline'
  | 'experiment_results'

export const ALL_SOURCE_TYPES: readonly SourceType[] = [
  'paper_metadata',
  'paper_fulltext',
  'paper_note',
  'scratchpad',
  'db_record',
  'idea',
  'experiment_outline',
  'experiment_results'
]

export function isSourceType(value: string): value is SourceType {
  return (ALL_SOURCE_TYPES as readonly string[]).includes(value)
}

export type EntityType = 'paper' | 'note' | 'db_record' | 'idea' | 'experiment'

export const ENTITY_TYPES: readonly EntityType[] = ['paper', 'note', 'db_record', 'idea', 'experiment']

export function isEntityType(value: string): value is EntityType {
  return (ENTITY_TYPES as readonly string[]).includes(value)
}

export interface Locator {
  pageNo?: number
  blockNo?: number
  charStart?: number
  charEnd?: number
  heading?: string
  fieldId?: string
  rects?: Rect[]
}

export interface Chunk {
  chunkId: string
  docId: string
  ordinal: number
  text: string
  locator: Locator
  contentHash: string
}

export interface IndexDoc {
  docId: string
  projectId: string
  source: SourceType
  entityType: EntityType
  entityId: string
  title: string
  lang: string
  updatedAt: Date
  contentHash: string
  parserVersion: string
  chunks: Chunk[]
  /** A parsed PDF without a text layer; its status is `no_text`. */
  noText?: boolean
}

/** Stable document keys. */
export const docIdFor = (entityType: EntityType, entityId: string): string => `${entityType}:${entityId}`

export const paperMetadataDocId = (paperId: string): string => `paper:${paperId}:metadata`
export const paperFulltextDocId = (paperId: string): string => `paper:${paperId}:fulltext`

export function experimentDocId(experimentId: string, source: SourceType): string {
  return `experiment:${experimentId}:${source === 'experiment_results' ? 'results' : 'outline'}`
}

export const PARSER_VERSION_MARKDOWN = 'md-1'
export const PARSER_VERSION_PDF = 'pdf-1'
export const SCHEMA_VERSION = 1

export type ChangeKind = 'upsert' | 'delete' | 'restore'

export interface Change {
  projectId: string
  entityType: EntityType
  entityId: string
  kind: ChangeKind
  at?: string
}

export type ScopeKind = 'project' | 'global'

export interface Scope {
  kind: ScopeKind
  projectId?: string
}

export function validateScope(scope: Scope): void {
  if (scope.kind === 'project') {
    if (scope.projectId === undefined || scope.projectId === '') {
      throw appError(AppErrorCode.InvalidArgument, 'search: project scope requires projectId')
    }
    return
  }
  if (scope.kind === 'global') {
    if (scope.projectId !== undefined && scope.projectId !== '') {
      throw appError(AppErrorCode.InvalidArgument, 'search: global scope does not accept projectId')
    }
    return
  }
  throw appError(
    AppErrorCode.InvalidArgument,
    `search: unknown scope kind ${JSON.stringify((scope as { kind?: unknown }).kind)}`
  )
}

export interface SearchFilters {
  sources?: readonly SourceType[]
  entityTypes?: readonly EntityType[]
  yearMin?: number
  yearMax?: number
  authors?: readonly string[]
  paperIds?: readonly string[]
  /** ISO-8601 UTC timestamp; only documents updated at or after it match. */
  updatedAfter?: string
}

export type RerankMode = 'auto' | 'off' | 'force'

export interface SearchRequest {
  query: string
  scope: Scope
  filters?: SearchFilters
  limit?: number
  offset?: number
  includeDeleted?: boolean
  rerank?: RerankMode
}

export interface ScoreBreakdown {
  keywordRank: number
  vectorRank: number
  fusion: number
  rerank: number | null
}

export type JumpKind = 'note' | 'idea' | 'experiment' | 'db_record' | 'paper'

export interface JumpTarget {
  kind: JumpKind | string
  projectId: string
  entityId: string
  locator: Locator
  paperId: string
  pdfPath: string
  source: SourceType | ''
}

export interface Snippet {
  text: string
  highlights: Array<{ start: number; end: number }>
  locator: Locator
}

export interface SearchHit {
  docId: string
  source: SourceType
  entityType: EntityType
  entityId: string
  projectId: string
  projectName: string
  archived: boolean
  deleted: boolean
  title: string
  snippets: Snippet[]
  locator: Locator
  score: ScoreBreakdown
  updatedAt: string
  target: JumpTarget
}

export type DegradeReason =
  | 'vector_unavailable'
  | 'embedding_failed'
  | 'rerank_unavailable'
  | 'rerank_timeout'
  | 'project_timeout'
  | 'index_stale'

export interface SearchResponse {
  hits: SearchHit[]
  total: number
  hasMore: boolean
  partial: boolean
  degraded: DegradeReason[]
  tookMs: number
  timings: Record<string, number>
  indexEpoch: string
}

export interface EntityRef {
  projectId: string
  entityType: EntityType
  entityId: string
}

export type DocStatus =
  | 'pending'
  | 'ok'
  | 'vector_pending'
  | 'failed'
  | 'no_text'
  | 'deleted'
  | 'stale'
  | 'index_stale'

export interface IndexJob {
  jobId: string
  scopeKind: ScopeKind
  scopeId: string
  reparsePdf: boolean
  rebuildVector: boolean
  state: 'pending' | 'running' | 'done' | 'failed' | 'canceled'
  total: number
  processed: number
  failed: number
  lastError: string | null
  cancelRequested: boolean
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
}

export interface IndexStatus {
  docs: number
  chunks: number
  dirty: number
  failed: number
  indexing: boolean
  progress: number
  lastIndexedAt: string | null
  schemaVersion: string
  parserVersion: string
  embeddingModel: string
  stale: boolean
  lastError: string | null
  jobs: IndexJob[]
}

export interface ReindexRequest {
  scope: Scope
  reparsePdf?: boolean
  rebuildVector?: boolean
}

/** Post-commit hook implemented by the entity services (foundation Indexer). */
export interface IndexNotifier {
  onEntityChanged(change: Change): Promise<void> | void
  rebuild(scope: Scope): Promise<void> | void
}

export interface EmbeddingProfile {
  id: string
  dim: number
}

/** AI gateway seam; absent means keyword-only search still works. */
export interface EmbeddingPort {
  currentProfile(): Promise<EmbeddingProfile | null>
  embed(texts: readonly string[], signal?: AbortSignal): Promise<readonly number[][]>
}

export interface RerankHit {
  index: number
  score: number
}

export interface RerankPort {
  rerank(
    query: string,
    documents: readonly string[],
    topN: number,
    signal?: AbortSignal
  ): Promise<readonly RerankHit[]>
}

export interface PdfBlock {
  pageNo: number
  blockNo: number
  text: string
  charStart: number
  charEnd: number
  rects?: Rect[]
}

export interface PdfDocument {
  pageCount: number
  blocks: PdfBlock[]
}

export interface PdfParseOptions {
  startPage?: number
  maxPage?: number
}

/** PDF extraction port; the default adapter loads pdfjs-dist lazily. */
export interface PdfParser {
  parse(buffer: Uint8Array, options?: PdfParseOptions): Promise<PdfDocument>
}
