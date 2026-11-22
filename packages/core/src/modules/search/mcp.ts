import { z } from 'zod'

import { AppErrorCode } from '@researchos/shared'

import type { SearchEngine } from './engine'
import type { Scope, SearchHit, SearchRequest } from './types'
import { fail, jobQueued, jobTool, ok, readTool, toolError } from '../mcp/tools'
import type { HandlerContext, ProgressReporter, ToolDefinition } from '../mcp/types'

/**
 * The three search tools, exactly as listed in
 * `docs/mcp-tools-catalog.json` (classes R, R, J; the reindex job is read-only
 * and needs no idempotency key). The composition root binds the engine through
 * the services bag under `SEARCH_RUNTIME_KEY`.
 */

export const SEARCH_RUNTIME_KEY = 'search.runtime'

export interface SearchJobRequest {
  tool: string
  projectId: string
  clientId: string
  readOnly: boolean
  params: Record<string, unknown>
  traceId: string
  signal: AbortSignal
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
}

export interface SearchJobPort {
  submit(request: SearchJobRequest): Promise<{ jobId: string }>
}

export interface SearchMcpRuntime {
  engine: SearchEngine
  jobs?: SearchJobPort
}

function runtimeOf(ctx: HandlerContext): SearchMcpRuntime {
  const runtime = ctx.services[SEARCH_RUNTIME_KEY]
  if (runtime === undefined) {
    throw toolError(AppErrorCode.Internal, 'search runtime is not wired', { traceId: ctx.traceId })
  }
  return runtime as SearchMcpRuntime
}

function scopeOf(ctx: HandlerContext, args: { scope: 'project' | 'global'; project_id?: string }): Scope {
  if (args.scope === 'global') {
    if (args.project_id !== undefined) {
      throw toolError(AppErrorCode.InvalidArgument, 'global scope does not accept project_id', {
        field: 'project_id'
      })
    }
    return { kind: 'global' }
  }
  const projectId = args.project_id
  if (projectId === undefined || projectId === '') {
    throw toolError(AppErrorCode.InvalidArgument, 'project scope requires project_id', {
      field: 'project_id'
    })
  }
  if (ctx.projectId !== null && ctx.projectId !== projectId) {
    throw toolError(AppErrorCode.InvalidArgument, 'project_id does not match the request context', {
      field: 'project_id'
    })
  }
  return { kind: 'project', projectId }
}

function hitView(hit: SearchHit): Record<string, unknown> {
  return {
    doc_id: hit.docId,
    kind: hit.entityType,
    source: hit.source,
    entity_id: hit.entityId,
    project_id: hit.projectId === '' ? null : hit.projectId,
    title: hit.title,
    snippet: hit.snippets[0]?.text ?? '',
    page_no: hit.locator.pageNo ?? null,
    block_no: hit.locator.blockNo ?? null,
    heading: hit.locator.heading ?? null,
    rects: hit.locator.rects ?? [],
    score: hit.score,
    target: {
      kind: hit.target.kind,
      project_id: hit.target.projectId === '' ? null : hit.target.projectId,
      entity_id: hit.target.entityId,
      paper_id: hit.target.paperId === '' ? null : hit.target.paperId,
      pdf_path: hit.target.pdfPath === '' ? null : hit.target.pdfPath,
      page_no: hit.target.locator.pageNo ?? null
    }
  }
}

const scopeArg = z.enum(['project', 'global']).describe('检索作用域（project 时必须给 project_id）')
const projectIdArg = z.string().min(1).max(64).optional().describe('项目 ID')
const entityKindArg = z.enum(['paper', 'note', 'db_record', 'idea', 'experiment'])

const searchShape = {
  query: z.string().describe('检索词'),
  scope: scopeArg,
  project_id: projectIdArg,
  kinds: z.array(entityKindArg).describe('实体类型过滤').optional(),
  limit: z.number().int().min(1).max(100).describe('返回条数，默认 20，上限 100').optional(),
  offset: z.number().int().min(0).describe('偏移量，默认 0').optional(),
  rerank: z.boolean().describe('是否启用 rerank，默认 true；不可用时降级并附 warning，不报错').optional(),
  include_deleted: z.boolean().describe('是否包含已删除文档，默认 false').optional()
}

const statusShape = {
  scope: scopeArg,
  project_id: projectIdArg
}

const reindexShape = {
  scope: scopeArg,
  project_id: projectIdArg,
  reparse_pdf: z.boolean().describe('是否重新解析 PDF，默认 false').optional(),
  rebuild_vector: z.boolean().describe('是否重建向量，默认 false').optional()
}

const searchTool = readTool({
  name: 'search',
  title: '检索',
  description:
    '检索项目或全局文献索引（关键词与向量融合；rerank 不可用时降级为融合排序）。scope=global 时归档项目默认参与；kinds 用实体类型过滤；只读，不记 Activity。',
  inputSchema: searchShape,
  handler: async (ctx, args) => {
    try {
      const runtime = runtimeOf(ctx)
      const scope = scopeOf(ctx, args)
      const request: SearchRequest = {
        query: args.query,
        scope,
        filters: args.kinds === undefined ? {} : { entityTypes: args.kinds },
        limit: args.limit,
        offset: args.offset,
        includeDeleted: args.include_deleted,
        rerank: args.rerank === false ? 'off' : 'auto'
      }
      const response = await runtime.engine.query(request)
      const warning = response.degraded.some(
        (reason) => reason === 'rerank_unavailable' || reason === 'rerank_timeout'
      )
        ? { code: 'RERANK_UNAVAILABLE' }
        : undefined
      return ok({
        hits: response.hits.map(hitView),
        took_ms: response.tookMs,
        total: response.total,
        has_more: response.hasMore,
        partial: response.partial,
        degraded: response.degraded,
        ...(warning === undefined ? {} : { warning }),
        index_epoch: response.indexEpoch
      })
    } catch (error) {
      return fail(error, ctx.traceId)
    }
  }
})

const statusTool = readTool({
  name: 'search_index_status',
  title: '索引状态',
  description: '返回项目或全局文献索引的健康度、进度与最近错误（只读）。',
  inputSchema: statusShape,
  handler: async (ctx, args) => {
    try {
      const runtime = runtimeOf(ctx)
      const scope = scopeOf(ctx, args)
      const status = await runtime.engine.status(scope)
      const activeJobs = status.jobs.filter((job) => job.state === 'pending' || job.state === 'running')
      const state = status.indexing
        ? 'indexing'
        : status.failed > 0 || status.lastError !== null
          ? 'degraded'
          : 'ready'
      return ok({
        scope: scope.kind,
        project_id: scope.kind === 'project' ? (scope.projectId ?? '') : null,
        state,
        progress: status.progress,
        doc_counts: { docs: status.docs, chunks: status.chunks, dirty: status.dirty, failed: status.failed },
        vector_available: runtime.engine.vectorAvailable,
        fts5_available: runtime.engine.fts5TrigramAvailable,
        last_indexed_at: status.lastIndexedAt,
        last_error: status.lastError,
        stale: status.stale,
        schema_version: status.schemaVersion,
        parser_version: status.parserVersion,
        embedding_model: status.embeddingModel,
        active_jobs: activeJobs.map((job) => ({
          job_id: job.jobId,
          scope_kind: job.scopeKind,
          state: job.state,
          total: job.total,
          processed: job.processed,
          failed: job.failed,
          cancel_requested: job.cancelRequested
        }))
      })
    } catch (error) {
      return fail(error, ctx.traceId)
    }
  }
})

const reindexTool = jobTool({
  name: 'search_reindex',
  title: '重建索引',
  description:
    '提交索引重建作业。只读作业：不改变科研数据、不需要确认、不记 Activity。返回 MCP job_id；作业结果中的 job_id 是索引作业 ID。',
  readOnly: true,
  inputSchema: reindexShape,
  handler: async (ctx, args) => {
    try {
      const runtime = runtimeOf(ctx)
      const scope = scopeOf(ctx, args)
      const params = {
        scope: scope.kind,
        project_id: scope.projectId ?? null,
        reparse_pdf: args.reparse_pdf === true,
        rebuild_vector: args.rebuild_vector === true
      }
      const run = async (): Promise<Record<string, unknown>> => {
        const indexJobId = await runtime.engine.reindex({
          scope,
          reparsePdf: args.reparse_pdf,
          rebuildVector: args.rebuild_vector
        })
        return { job_id: indexJobId, scope: scope.kind, project_id: scope.projectId ?? null }
      }
      if (runtime.jobs !== undefined) {
        const job = await runtime.jobs.submit({
          tool: 'search_reindex',
          projectId: scope.projectId ?? '',
          clientId: ctx.principal.clientId,
          readOnly: true,
          params,
          traceId: ctx.traceId,
          signal: ctx.signal,
          run: async () => run()
        })
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }
      // No MCP job manager in this process: the index job id is the pollable
      // handle and `search_index_status` reports its progress.
      const result = await run()
      return jobQueued({ job_id: result.job_id, poll_after_ms: 1000 })
    } catch (error) {
      return fail(error, ctx.traceId)
    }
  }
})

export const searchTools: readonly ToolDefinition[] = [searchTool, statusTool, reindexTool]
