import type {
  SearchDegradeCode,
  SearchHitView,
  SearchIndexJobView,
  SearchIndexStatusView,
  SearchQueryRequest,
  SearchQueryResult
} from '@researchos/shared'

import type { SearchEngine } from '@researchos/core/modules/search/engine'
import type { IndexJob, Scope, SearchHit, SearchRequest } from '@researchos/core/modules/search/types'
import {
  searchCancelValidator,
  searchIndexStatusValidator,
  searchQueryValidator,
  searchReindexValidator
} from '@researchos/core/validation/search'

import type { CommandRouter } from '../router'

export interface SearchHandlerDeps {
  search: SearchEngine
}

function scopeOf(request: { scope?: 'project' | 'global'; projectId?: string }): Scope {
  const kind = request.scope ?? (request.projectId === undefined ? 'global' : 'project')
  return kind === 'global' ? { kind: 'global' } : { kind: 'project', projectId: request.projectId ?? '' }
}

function toIpcRequest(request: SearchQueryRequest): SearchRequest {
  const rerank =
    request.rerank === undefined
      ? 'auto'
      : typeof request.rerank === 'boolean'
        ? request.rerank
          ? 'auto'
          : 'off'
        : request.rerank
  return {
    query: request.query,
    scope: scopeOf(request),
    filters: {
      ...(request.filters ?? {}),
      ...(request.kinds === undefined ? {} : { entityTypes: request.kinds })
    },
    limit: request.limit,
    offset: request.offset,
    includeDeleted: request.includeDeleted,
    rerank
  }
}

function jobState(state: IndexJob['state']): SearchIndexJobView['state'] {
  switch (state) {
    case 'pending':
      return 'queued'
    case 'done':
      return 'succeeded'
    case 'canceled':
      return 'cancelled'
    default:
      return state
  }
}

function toIpcHit(hit: SearchHit): SearchHitView {
  return {
    docId: hit.docId,
    source: hit.source,
    entityType: hit.entityType,
    entityId: hit.entityId,
    projectId: hit.projectId,
    projectName: hit.projectName,
    archived: hit.archived,
    deleted: hit.deleted,
    title: hit.title,
    snippet: hit.snippets[0]?.text ?? '',
    snippets: hit.snippets,
    locator: hit.locator,
    score: hit.score,
    updatedAt: hit.updatedAt,
    target: hit.target
  }
}

/** Registers the four search IPC commands on the shared router. */
export function registerSearchHandlers(router: CommandRouter, deps: SearchHandlerDeps): void {
  router.registerHandler('search.query', searchQueryValidator, async (request): Promise<SearchQueryResult> => {
    const response = await deps.search.query(toIpcRequest(request))
    return {
      hits: response.hits.map(toIpcHit),
      total: response.total,
      hasMore: response.hasMore,
      partial: response.partial,
      degraded: response.degraded as SearchDegradeCode[],
      tookMs: response.tookMs,
      timings: response.timings,
      indexEpoch: response.indexEpoch
    }
  })

  router.registerHandler(
    'search.index.status',
    searchIndexStatusValidator,
    async (request): Promise<SearchIndexStatusView> => {
      const scope = scopeOf(request)
      const status = await deps.search.status(scope)
      return {
        scope: scope.kind,
        projectId: scope.kind === 'project' ? (scope.projectId ?? '') : null,
        vectorAvailable: deps.search.vectorAvailable,
        fts5Available: deps.search.fts5TrigramAvailable,
        docs: status.docs,
        chunks: status.chunks,
        dirty: status.dirty,
        failed: status.failed,
        indexing: status.indexing,
        progress: status.progress,
        lastIndexedAt: status.lastIndexedAt,
        lastError: status.lastError,
        stale: status.stale,
        schemaVersion: status.schemaVersion,
        parserVersion: status.parserVersion,
        embeddingModel: status.embeddingModel,
        rerankStatus: deps.search.rerankStatus(),
        activeJobs: status.jobs
          .filter((job) => job.state === 'pending' || job.state === 'running')
          .map((job) => ({
            jobId: job.jobId,
            scope: job.scopeKind,
            projectId: job.scopeKind === 'project' ? job.scopeId : null,
            state: jobState(job.state),
            total: job.total,
            processed: job.processed,
            failed: job.failed,
            lastError: job.lastError,
            cancelRequested: job.cancelRequested
          }))
      }
    }
  )

  router.registerHandler('search.index.reindex', searchReindexValidator, async (request) => {
    const jobId = await deps.search.reindex({
      scope: scopeOf(request),
      reparsePdf: request.reparsePdf,
      rebuildVector: request.rebuildVector
    })
    return { jobId }
  })

  router.registerHandler('search.index.cancel', searchCancelValidator, async (request) => {
    await deps.search.cancelJob(request.jobId)
    return deps.search.nowIso()
  })
}
