import type { EntityId, IsoDateTime, Locator } from './common'
import type { JobState } from './common'

export type SearchScope = 'project' | 'global'

export type SearchKind =
  | 'note'
  | 'db_record'
  | 'idea'
  | 'experiment_outline'
  | 'experiment_results'
  | 'paper_metadata'
  | 'paper_fulltext'

export type SearchRerankMode = 'auto' | 'off' | 'force'

export type SearchDegradeReason =
  | 'vector_unavailable'
  | 'rerank_unavailable'
  | 'keyword_only'
  | 'budget_exceeded'
  | 'index_incomplete'

export interface SearchRequest {
  scope: SearchScope
  projectId?: EntityId
  query: string
  kinds?: SearchKind[]
  limit?: number
  offset?: number
  rerank?: SearchRerankMode
  embeddingProfileId?: string
}

export interface SearchHit {
  kind: SearchKind
  entityId: EntityId
  title: string
  snippet: string
  score: number
  projectId: EntityId | null
  locator: Locator | null
  updatedAt: IsoDateTime
}

export interface SearchResponse {
  hits: SearchHit[]
  total: number
  degraded: SearchDegradeReason[]
  tookMs: number
}

export interface SearchIndexJob {
  jobId: string
  scope: SearchScope
  projectId: EntityId | null
  state: JobState
  total: number
  processed: number
  failed: number
  lastError: string | null
  cancelRequested: boolean
}

export interface SearchIndexStatus {
  vectorAvailable: boolean
  fts5Available: boolean
  projectCount: number
  docs: number
  chunks: number
  dirty: number
  lastError: string | null
  activeJobs: SearchIndexJob[]
}

export interface ReindexRequest {
  scope: SearchScope
  projectId?: EntityId
  reparsePdf?: boolean
  rebuildVector?: boolean
}