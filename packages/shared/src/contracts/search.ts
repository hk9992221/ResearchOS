import type { EntityId, IsoDateTime, JobRef, JobState, Locator, Rect } from '../types/common'

/**
 * Search IPC and MCP contract. Requests are scope + query; `kinds`
 * filters indexed entity types, `sources` narrows to extraction sources.
 * Results carry an executable jump target and page-precise locator so a
 * `ros://#page=N` navigation needs no extra read.
 */

export type SearchScopeKind = 'project' | 'global'

export type SearchEntityKind = 'paper' | 'note' | 'db_record' | 'idea' | 'experiment'

export type SearchSourceKind =
  | 'paper_metadata'
  | 'paper_fulltext'
  | 'paper_note'
  | 'scratchpad'
  | 'db_record'
  | 'idea'
  | 'experiment_outline'
  | 'experiment_results'

export type SearchRerankPolicy = 'auto' | 'off' | 'force'

export type SearchDegradeCode =
  | 'vector_unavailable'
  | 'embedding_failed'
  | 'rerank_unavailable'
  | 'rerank_timeout'
  | 'project_timeout'
  | 'index_stale'

export interface SearchFilterRequest {
  sources?: SearchSourceKind[]
  entityTypes?: SearchEntityKind[]
  yearMin?: number
  yearMax?: number
  authors?: string[]
  paperIds?: EntityId[]
  /** UTC millisecond timestamp; only documents updated at or after it match. */
  updatedAfter?: IsoDateTime
}

export interface SearchQueryRequest {
  scope: SearchScopeKind
  /** Required when scope is `project`; forbidden when `global`. */
  projectId?: EntityId
  query: string
  kinds?: SearchEntityKind[]
  filters?: SearchFilterRequest
  limit?: number
  offset?: number
  rerank?: SearchRerankPolicy | boolean
  includeDeleted?: boolean
  embeddingProfileId?: string
}

export interface SearchSnippetView {
  text: string
  highlights: Array<{ start: number; end: number }>
  locator: Locator
  rects?: Rect[]
}

export interface SearchScoreView {
  keywordRank: number
  vectorRank: number
  fusion: number
  rerank: number | null
}

export interface SearchJumpView {
  kind: 'paper' | 'note' | 'db_record' | 'idea' | 'experiment' | string
  projectId: EntityId
  entityId: EntityId
  paperId: EntityId | ''
  pdfPath: string
  source: SearchSourceKind | ''
  locator: Locator
}

export interface SearchHitView {
  docId: string
  source: SearchSourceKind
  entityType: SearchEntityKind
  entityId: EntityId
  projectId: EntityId
  projectName: string
  archived: boolean
  deleted: boolean
  title: string
  snippet: string
  snippets: SearchSnippetView[]
  locator: Locator
  score: SearchScoreView
  updatedAt: IsoDateTime
  target: SearchJumpView
}

export interface SearchQueryResult {
  hits: SearchHitView[]
  total: number
  hasMore: boolean
  partial: boolean
  degraded: SearchDegradeCode[]
  tookMs: number
  timings: Record<string, number>
  indexEpoch: string
}

export interface SearchIndexJobView {
  jobId: string
  scope: SearchScopeKind
  projectId: EntityId | null
  state: JobState
  total: number
  processed: number
  failed: number
  lastError: string | null
  cancelRequested: boolean
}

export interface SearchIndexStatusView {
  scope: SearchScopeKind
  projectId: EntityId | null
  vectorAvailable: boolean
  fts5Available: boolean
  docs: number
  chunks: number
  dirty: number
  failed: number
  indexing: boolean
  progress: number
  lastIndexedAt: IsoDateTime | null
  lastError: string | null
  stale: boolean
  schemaVersion: string
  parserVersion: string
  embeddingModel: string
  activeJobs: SearchIndexJobView[]
  /** Rerank availability for this index (timeout surfaces per query). */
  rerankStatus: 'available' | 'unavailable' | 'timeout'
}

export interface SearchIndexStatusRequest {
  scope?: SearchScopeKind
  projectId?: EntityId
}

export interface SearchReindexRequest {
  scope: SearchScopeKind
  projectId?: EntityId
  reparsePdf?: boolean
  rebuildVector?: boolean
}

export interface SearchCommands {
  'search.query': { req: SearchQueryRequest; res: SearchQueryResult }
  'search.index.status': { req: SearchIndexStatusRequest; res: SearchIndexStatusView }
  'search.index.reindex': { req: SearchReindexRequest; res: JobRef }
  'search.index.cancel': { req: { jobId: string }; res: IsoDateTime }
}
