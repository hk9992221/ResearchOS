import type { SearchDegradeCode } from '@researchos/shared'

/** Catalog keys for the degradations the search service reports. */
export const DEGRADE_KEYS: Record<SearchDegradeCode, string> = {
  vector_unavailable: 'shell.search.degraded_vector',
  embedding_failed: 'shell.search.degraded_embedding',
  rerank_unavailable: 'shell.search.degraded_rerank',
  rerank_timeout: 'shell.search.degraded_rerank_timeout',
  project_timeout: 'shell.search.degraded_project_timeout',
  index_stale: 'shell.search.degraded_index_stale'
}