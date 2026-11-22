/**
 * The 19 literature MCP tools of docs/mcp-tools-catalog.json (plan §1.4):
 * Zotero browsing, import, project papers, Paper Notes, Overview and sync.
 * `writeTool`/`confirmTool`/`jobTool` inject the required idempotency key
 * (defect 5); handlers never map a domain AppError to INTERNAL.
 */
import { z } from 'zod'

import type {
  PaperNoteFields,
  PaperNoteView,
  PaperView,
  ProjectPaper,
  SyncItem
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import { Confirmations } from '../mcp/confirmations'
import { argsHash } from '../mcp/idempotency'
import { jobQueued, jobTool, ok, pending, readTool, writeTool, confirmTool } from '../mcp/tools'
import type { HandlerContext, ProgressReporter, ToolDefinition } from '../mcp/types'
import type { LiteratureModule } from './module'

export const LITERATURE_RUNTIME_KEY = 'literature.runtime'

export interface LiteratureJobRequest {
  tool: string
  projectId: string | null
  clientId: string
  readOnly: boolean
  params: Record<string, unknown>
  traceId: string
  signal: AbortSignal
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
}

export interface LiteratureJobPort {
  submit(request: LiteratureJobRequest): Promise<{ jobId: string }>
}

export interface LiteratureMcpRuntime {
  service(): LiteratureModule
  jobs: LiteratureJobPort
}

function runtimeOf(ctx: HandlerContext): LiteratureMcpRuntime {
  const value = ctx.services[LITERATURE_RUNTIME_KEY]
  if (value === undefined) {
    throw appError(AppErrorCode.Internal, 'literature runtime is not wired', {
      details: { traceId: ctx.traceId }
    })
  }
  // The composition root installs exactly this shape; the bag is string-keyed.
  const runtime: LiteratureMcpRuntime = value as LiteratureMcpRuntime
  return runtime
}

function serviceOf(ctx: HandlerContext): LiteratureModule {
  return runtimeOf(ctx).service()
}

async function submitJob(
  ctx: HandlerContext,
  runtime: LiteratureMcpRuntime,
  tool: string,
  projectId: string | null,
  readOnly: boolean,
  params: Record<string, unknown>,
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
): Promise<{ jobId: string }> {
  return runtime.jobs.submit({
    tool,
    projectId,
    clientId: ctx.principal.clientId,
    readOnly,
    params,
    traceId: ctx.traceId,
    signal: ctx.signal,
    run
  })
}

/* ---------------------------------------------------------- projections */

function metadataText(metadata: Record<string, unknown>, key: string): string {
  const value = metadata[key]
  return typeof value === 'string' ? value : ''
}

function metadataAuthors(metadata: Record<string, unknown>): string {
  const creators = metadata['creators']
  if (!Array.isArray(creators)) return ''
  const names: string[] = []
  for (const entry of creators) {
    if (entry === null || typeof entry !== 'object') continue
    const creator: Record<string, unknown> = entry as Record<string, unknown>
    const first = typeof creator['firstName'] === 'string' ? creator['firstName'] : ''
    const last = typeof creator['lastName'] === 'string' ? creator['lastName'] : ''
    const name = typeof creator['name'] === 'string' ? creator['name'] : ''
    names.push(last === '' ? name || first : first === '' ? last : `${last}, ${first}`)
  }
  return names.filter((name) => name !== '').join(', ')
}

function metadataYear(metadata: Record<string, unknown>): number | null {
  const date = metadataText(metadata, 'date')
  const match = /[0-9]{4}/.exec(date)
  return match === null ? null : Number.parseInt(match[0], 10)
}

function paperMdv(paper: PaperView | ProjectPaper): Record<string, unknown> {
  const hasView = 'title' in paper && typeof paper.title === 'string'
  const metadata = paper.metadata
  const title = hasView ? paper.title : metadataText(metadata, 'title')
  const authors = hasView ? paper.authors : metadataAuthors(metadata)
  const year = hasView ? paper.year : metadataYear(metadata)
  const doi = hasView ? paper.doi : metadataText(metadata, 'DOI') || null
  return {
    paper_id: paper.id,
    title,
    authors,
    year,
    doi,
    zotero_key: paper.zoteroKey,
    library_id: paper.zoteroLibraryId,
    file: {
      path: paper.filePath,
      exists: !paper.broken,
      hash: paper.fileHash,
      bytes: paper.fileSize
    },
    imported_at: paper.importedAt,
    created_at: paper.importedAt,
    updated_at: paper.updatedAt,
    linked_at: paper.linkedAt,
    note_id: paper.noteId,
    broken: paper.broken
  }
}

function noteView(note: PaperNoteView): Record<string, unknown> {
  return {
    note_id: note.note.id,
    paper_id: note.note.paperId,
    title: note.note.title,
    fields: note.fields,
    body_md: note.bodyMd,
    body_hash: note.bodyHash,
    created_at: note.note.createdAt,
    updated_at: note.note.updatedAt
  }
}

function overviewView(overview: {
  note: { id: string; updatedAt: string }
  markdown: string
  bodyHash: string | null
}): Record<string, unknown> {
  return {
    note_id: overview.note.id,
    body_md: overview.markdown,
    body_hash: overview.bodyHash,
    updated_at: overview.note.updatedAt
  }
}

function syncItemView(item: SyncItem): Record<string, unknown> {
  return {
    paper_id: item.paperId,
    state: item.missing ? 'missing' : item.error !== null ? 'error' : item.changed ? 'changed' : 'unchanged',
    changed: item.changed,
    old_hash: item.localHash,
    new_hash: item.sourceHash,
    upstream_path: null,
    local_size: item.localSize,
    source_size: item.sourceSize,
    warnings: item.warnings
  }
}

function paperSyncReport(result: {
  checked: number
  items: SyncItem[]
  source: 'local_api' | 'sqlite'
  checkedAt: string
}): Record<string, unknown> {
  return {
    items: result.items.map(syncItemView),
    checked: result.checked,
    source: result.source,
    checked_at: result.checkedAt,
    errors: result.items
      .filter((item) => item.error !== null)
      .map((item) => ({ paper_id: item.paperId, message: item.error }))
  }
}

function fieldsOf(value: Record<string, unknown> | undefined): PaperNoteFields {
  const read = (key: keyof PaperNoteFields): string => {
    const field = value?.[key]
    return typeof field === 'string' ? field : ''
  }
  return {
    summary: read('summary'),
    problem: read('problem'),
    method: read('method'),
    contribution: read('contribution'),
    limitations: read('limitations'),
    understanding: read('understanding')
  }
}

/* -------------------------------------------------------------- schemas */

const projectId = z.string().min(1).describe('项目 ID')
const paperId = z.string().min(1).describe('文献 ID')
const noteId = z.string().min(1).describe('笔记 ID')
const libraryId = z.string().optional().describe('文库 ID，默认 0')
const bodyMd = z.string().max(5 * 1024 * 1024).describe('Markdown 正文')
const fieldsSchema = z
  .record(z.string(), z.unknown())
  .describe('结构化字段 {summary, problem, method, contribution, limitations, understanding}')

function confirmationsOf(ctx: HandlerContext): Confirmations {
  const value = ctx.services['confirmations']
  if (!(value instanceof Confirmations)) {
    throw appError(AppErrorCode.Internal, 'confirmation service is not wired', {
      details: { traceId: ctx.traceId }
    })
  }
  return value
}

/* ---------------------------------------------------------------- tools */

export function createLiteratureTools(): ToolDefinition[] {
  return [
    readTool({
      name: 'zotero_status',
      title: 'Zotero 状态',
      description: '返回 Zotero 连接状态：数据目录、metadata 来源（本地 API/SQLite）与最近错误。',
      inputSchema: {},
      handler: (ctx) => serviceOf(ctx).zoteroStatus().then((status) => ok({
        connected: status.available,
        api_mode: status.mode === 'unavailable' ? 'sqlite' : status.mode,
        local_api: { available: status.mode === 'local_api', base_url: status.localApiUrl },
        checked_at: new Date().toISOString(),
        data_dir: status.dataDir,
        zotero_version: null,
        last_error: status.lastError,
        diagnostics: status.diagnostics
      }))
    }),

    readTool({
      name: 'zotero_libraries',
      title: 'Zotero 文库列表',
      description: '列出 Zotero 文库。',
      inputSchema: {},
      handler: async (ctx) => ok({ libraries: await serviceOf(ctx).zoteroLibraries() })
    }),

    readTool({
      name: 'zotero_collections',
      title: 'Zotero 分类列表',
      description: '列出 Zotero 文库下的分类。',
      inputSchema: {
        library_id: libraryId,
        parent_key: z.string().optional().describe('父分类 key（可选）')
      },
      handler: async (ctx, args) => {
        const input = z.object({ library_id: libraryId, parent_key: z.string() }).parse(args)
        const collections = await serviceOf(ctx).zoteroCollections(input.library_id ?? '0')
        const filtered =
          input.parent_key === undefined
            ? collections
            : collections.filter((collection) => collection.parentKey === input.parent_key)
        return ok({ collections: filtered })
      }
    }),

    readTool({
      name: 'zotero_items',
      title: 'Zotero 条目列表',
      description: '浏览 Zotero 条目（导入前预览，不创建任何数据）。',
      inputSchema: {
        library_id: libraryId,
        collection_key: z.string().optional().describe('分类 key'),
        q: z.string().optional().describe('查询关键字'),
        limit: z.number().int().min(1).max(100).optional().describe('返回条数，默认 50，上限 100'),
        cursor: z.string().optional().describe('上一页返回的 next_cursor')
      },
      handler: async (ctx, args) => {
        const input = z
          .object({
            library_id: libraryId,
            collection_key: z.string().optional(),
            q: z.string().optional(),
            limit: z.number().int().min(1).max(100).optional(),
            cursor: z.string().optional()
          })
          .parse(args)
        const offset = input.cursor === undefined ? 0 : Number.parseInt(input.cursor, 10)
        const page = await serviceOf(ctx).zoteroItems({
          libraryId: input.library_id ?? '0',
          ...(input.collection_key === undefined ? {} : { collectionKey: input.collection_key }),
          ...(input.q === undefined ? {} : { query: input.q }),
          ...(input.limit === undefined ? {} : { limit: input.limit }),
          offset: Number.isFinite(offset) && offset >= 0 ? offset : 0
        })
        return ok({
          items: page.items.map((item) => ({
            key: item.key,
            title: item.title,
            creators: item.creators,
            year: item.year,
            has_pdf: item.attachments.some((attachment) => attachment.resolvable)
          })),
          next_cursor: page.nextCursor
        })
      }
    }),

    readTool({
      name: 'paper_list',
      title: '文献列表',
      description: '列出全局文献，或某项目关联的文献。',
      inputSchema: {
        project_id: z.string().optional().describe('项目 ID（可选；提供时列出项目关联文献）'),
        q: z.string().optional().describe('标题/作者关键字'),
        imported_after: z.string().optional().describe('导入时间下界 RFC3339（实现不支持时忽略）'),
        limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200'),
        cursor: z.string().optional().describe('上一页返回的 next_cursor')
      },
      handler: (ctx, args) => {
        const input = z
          .object({
            project_id: z.string().optional(),
            q: z.string().optional(),
            imported_after: z.string().optional(),
            limit: z.number().int().min(1).max(200).optional(),
            cursor: z.string().optional()
          })
          .parse(args)
        const offset = input.cursor === undefined ? 0 : Number.parseInt(input.cursor, 10)
        if (input.project_id === undefined || input.project_id === '') {
          const page = serviceOf(ctx).listGlobalPapers({
            ...(input.q === undefined ? {} : { q: input.q }),
            ...(input.imported_after === undefined ? {} : { importedAfter: input.imported_after }),
            ...(input.limit === undefined ? {} : { limit: input.limit }),
            cursor: input.cursor ?? null
          })
          return ok({ items: page.items.map(paperMdv), next_cursor: page.nextCursor })
        }
        const page = serviceOf(ctx).listPapers({
          projectId: input.project_id,
          ...(input.q === undefined ? {} : { q: input.q }),
          ...(input.limit === undefined ? {} : { limit: input.limit }),
          offset: Number.isFinite(offset) && offset >= 0 ? offset : 0
        })
        const next = page.offset + page.items.length
        return ok({
          items: page.items.map((paper) => paperMdv(paper)),
          next_cursor: page.total > next ? String(next) : null
        })
      }
    }),

    readTool({
      name: 'paper_get',
      title: '文献详情',
      description: '返回文献元数据、文件状态与关联项目、Paper Note。',
      inputSchema: { paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ paper_id: paperId }).parse(args)
        const service = serviceOf(ctx)
        const paper = service.getPaper(input.paper_id, ctx.projectId ?? undefined)
        const projects = service.paperProjects(input.paper_id)
        const notes =
          ctx.projectId === null
            ? []
            : [service.getPaperNote(ctx.projectId, input.paper_id)].filter(
                (note): note is PaperNoteView => note !== null
              )
        return ok({
          ...paperMdv(paper),
          projects: projects.map((project) => ({
            project_id: project.projectId,
            name: project.name,
            status: project.status
          })),
          notes: notes.map((note) => ({
            project_id: ctx.projectId,
            note_id: note.note.id,
            title: note.note.title
          }))
        })
      }
    }),

    writeTool({
      name: 'paper_link',
      title: '关联文献到项目',
      description: '把全局文献关联到项目（只增删项目关联，不动全局副本）。',
      inputSchema: { project_id: projectId, paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ project_id: projectId, paper_id: paperId }).parse(args)
        const service = serviceOf(ctx)
        const added = !service.isPaperLinked(input.project_id, input.paper_id)
        const paper = service.linkPaper(input.project_id, input.paper_id, 'mcp')
        return ok({ linked: true, added, broken: paper.broken })
      }
    }),

    writeTool({
      name: 'paper_unlink',
      title: '解除文献关联',
      description: '解除项目与文献的关联。',
      inputSchema: { project_id: projectId, paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ project_id: projectId, paper_id: paperId }).parse(args)
        const result = serviceOf(ctx).unlinkPaper(input.project_id, input.paper_id)
        return ok({ unlinked: true, detached_notes: result.detachedNoteIds })
      }
    }),

    jobTool({
      name: 'paper_import',
      title: '导入文献',
      description:
        '从 Zotero 批量导入文献到项目（按 zotero_key 去重，固定复制 PDF）。条目与分类可同时给出，合并去重。返回 job_id。',
      readOnly: false,
      inputSchema: {
        project_id: projectId,
        library_id: libraryId,
        collection_keys: z.array(z.string()).optional().describe('分类 key；与 item_keys 合并去重'),
        include_subcollections: z.boolean().optional().describe('分类导入是否包含子分类，默认 false'),
        item_keys: z.array(z.string()).optional().describe('条目 key'),
        attachment_choices: z
          .array(
            z.object({ item_key: z.string().min(1), attachment_key: z.string().min(1) })
          )
          .optional()
          .describe('多 PDF 条目的附件选择'),
        copy_pdfs: z.boolean().optional().describe('是否复制 PDF（仅允许 true）')
      },
      handler: async (ctx, args) => {
        const input = z
          .object({
            project_id: projectId,
            library_id: libraryId,
            collection_keys: z.array(z.string()).optional(),
            include_subcollections: z.boolean().optional(),
            item_keys: z.array(z.string()).optional(),
            attachment_choices: z
              .array(z.object({ item_key: z.string(), attachment_key: z.string() }))
              .optional(),
            copy_pdfs: z.boolean().optional()
          })
          .parse(args)
        if (input.copy_pdfs === false) {
          throw appError(AppErrorCode.InvalidArgument, 'copy_pdfs 只能为 true')
        }
        const service = serviceOf(ctx)
        const runtime = runtimeOf(ctx)
        const choices: Record<string, string> = {}
        for (const choice of input.attachment_choices ?? []) {
          choices[choice.item_key] = choice.attachment_key
        }
        const params = {
          project_id: input.project_id,
          library_id: input.library_id ?? '0',
          collection_keys: input.collection_keys ?? [],
          item_keys: input.item_keys ?? []
        }
        const job = await submitJob(
          ctx,
          runtime,
          'paper_import',
          input.project_id,
          false,
          params,
          async ({ progress }) => {
            const started = service.startMcpImport(
              input.project_id,
              {
                libraryId: input.library_id ?? '0',
                collectionKeys: input.collection_keys ?? [],
                includeSubcollections: input.include_subcollections === true,
                itemKeys: input.item_keys ?? [],
                attachmentChoices: choices
              },
              'mcp'
            )
            progress(5, '读取 Zotero')
            await service.jobs.wait(started.jobId)
            const report = service.jobs.report(started.jobId)
            progress(100, '完成')
            return {
              imported: report.outcomes
                .filter((outcome) => outcome.status === 'imported')
                .map((outcome) => ({ key: outcome.key, paper_id: outcome.paperId })),
              reused: report.outcomes
                .filter((outcome) => outcome.status === 'reused')
                .map((outcome) => ({ key: outcome.key, paper_id: outcome.paperId })),
              skipped: report.outcomes
                .filter((outcome) => outcome.status === 'skipped' || outcome.status === 'needs_attachment')
                .map((outcome) => ({ key: outcome.key, status: outcome.status })),
              unsatisfied: report.outcomes
                .filter((outcome) => outcome.status === 'needs_attachment')
                .map((outcome) => ({ key: outcome.key })),
              failed: report.outcomes
                .filter((outcome) => outcome.status === 'failed')
                .map((outcome) => ({ key: outcome.key, error: outcome.error })),
              counts: {
                imported: report.imported,
                linked: report.reused,
                skipped: report.skipped,
                failed: report.failed
              }
            }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }
    }),

    jobTool({
      name: 'paper_sync_check',
      title: '检查文献同步差异',
      description: '只读比对 Zotero 上游与本地副本（不写数据、不覆盖）。返回 job_id。',
      readOnly: true,
      inputSchema: {
        project_id: z.string().optional().describe('项目 ID（可选）'),
        paper_ids: z.array(z.string()).optional().describe('文献 ID（可选）')
      },
      handler: async (ctx, args) => {
        const input = z
          .object({ project_id: z.string().optional(), paper_ids: z.array(z.string()).optional() })
          .parse(args)
        if (
          (input.project_id === undefined || input.project_id === '') &&
          (input.paper_ids === undefined || input.paper_ids.length === 0)
        ) {
          throw appError(AppErrorCode.InvalidArgument, 'project_id 与 paper_ids 至少提供一个')
        }
        const service = serviceOf(ctx)
        const runtime = runtimeOf(ctx)
        const job = await submitJob(
          ctx,
          runtime,
          'paper_sync_check',
          input.project_id ?? null,
          true,
          { project_id: input.project_id, paper_ids: input.paper_ids },
          async ({ progress }) => {
            progress(10, '解析 Zotero 源')
            const report = await service.syncCheck({
              ...(input.project_id === undefined ? {} : { projectId: input.project_id }),
              ...(input.paper_ids === undefined ? {} : { paperIds: input.paper_ids })
            })
            progress(100, '比较完成')
            return paperSyncReport(report)
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }
    }),

    confirmTool({
      name: 'paper_sync_apply',
      title: '确认覆盖本地文献副本',
      description:
        '请求用 Zotero 上游文件覆盖一篇文献的本地副本。工具只入队并冻结差异，必须在应用内逐篇人工批准；执行前重新校验上游哈希，变化则冲突不覆盖。',
      inputSchema: {
        project_id: projectId,
        paper_id: paperId,
        expected_new_hash: z.string().min(1).describe('批准时所基于的上游哈希')
      },
      handler: async (ctx, args) => {
        const input = z
          .object({
            project_id: projectId,
            paper_id: paperId,
            expected_new_hash: z.string().min(1)
          })
          .parse(args)
        const service = serviceOf(ctx)
        const report = await service.syncCheck({
          projectId: input.project_id,
          paperIds: [input.paper_id]
        })
        const item = report.items.find((entry) => entry.paperId === input.paper_id)
        if (item === undefined) {
          throw appError(AppErrorCode.NotFound, '同步检查中没有该文献', {
            details: { paper_id: input.paper_id }
          })
        }
        if (item.sourceHash === null) {
          throw appError(AppErrorCode.ExternalUnavailable, '上游 PDF 不可用，无法覆盖', {
            details: { paper_id: input.paper_id }
          })
        }
        if (item.sourceHash !== input.expected_new_hash) {
          throw appError(AppErrorCode.Conflict, '上游哈希已变化，请重新检查差异', {
            details: {
              paper_id: input.paper_id,
              expected_new_hash: input.expected_new_hash,
              current_source_hash: item.sourceHash
            }
          })
        }
        if (!item.changed) {
          return ok({ paper_id: input.paper_id, changed: false })
        }
        const confirmation = confirmationsOf(ctx).enqueue(ctx.principal, {
          tool: 'paper_sync_apply',
          args: {
            project_id: input.project_id,
            paper_id: input.paper_id,
            expected_new_hash: input.expected_new_hash
          },
          argsHash: argsHash({
            project_id: input.project_id,
            paper_id: input.paper_id,
            expected_new_hash: input.expected_new_hash
          }),
          title: '覆盖文献副本',
          summary: `用 Zotero 上游文件覆盖文献 ${input.paper_id} 的本地副本`,
          impact: {
            paper_id: input.paper_id,
            old_hash: item.localHash,
            new_hash: item.sourceHash,
            old_bytes: item.localSize,
            new_bytes: item.sourceSize,
            expected_new_hash: input.expected_new_hash,
            upstream_path: null,
            source_state: item.missing ? 'missing' : 'ok',
            local_state: item.missing ? 'missing' : 'ok'
          },
          projectId: input.project_id,
          traceId: ctx.traceId
        })
        return pending({
          confirmation_id: confirmation.id,
          expires_at: confirmation.expiresAt,
          title: '覆盖文献副本',
          summary: `用 Zotero 上游文件覆盖文献 ${input.paper_id} 的本地副本`,
          impact: {
            paper_id: input.paper_id,
            old_hash: item.localHash,
            new_hash: item.sourceHash,
            old_bytes: item.localSize,
            new_bytes: item.sourceSize,
            expected_new_hash: input.expected_new_hash
          }
        })
      }
    }),

    jobTool({
      name: 'paper_refresh_metadata',
      title: '刷新文献元数据',
      description: '只刷新文献元数据，不触碰 PDF 副本。返回 job_id。',
      readOnly: false,
      inputSchema: {
        project_id: z.string().optional().describe('项目 ID（可选）'),
        paper_ids: z.array(z.string()).optional().describe('文献 ID（可选）')
      },
      handler: async (ctx, args) => {
        const input = z
          .object({ project_id: z.string().optional(), paper_ids: z.array(z.string()).optional() })
          .parse(args)
        if (
          (input.project_id === undefined || input.project_id === '') &&
          (input.paper_ids === undefined || input.paper_ids.length === 0)
        ) {
          throw appError(AppErrorCode.InvalidArgument, 'project_id 与 paper_ids 至少提供一个')
        }
        const service = serviceOf(ctx)
        const runtime = runtimeOf(ctx)
        const job = await submitJob(
          ctx,
          runtime,
          'paper_refresh_metadata',
          input.project_id ?? null,
          false,
          { project_id: input.project_id, paper_ids: input.paper_ids },
          async ({ progress }) => {
            progress(10, '读取 Zotero 元数据')
            const result = await service.refreshMetadata({
              ...(input.project_id === undefined ? {} : { projectId: input.project_id }),
              ...(input.paper_ids === undefined ? {} : { paperIds: input.paper_ids })
            })
            progress(100, '完成')
            return {
              updated: result.updated.length,
              missing: result.missing,
              failed: result.failed,
              refreshed_at: result.refreshedAt
            }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }
    }),

    readTool({
      name: 'paper_reconcile',
      title: '文献一致性诊断',
      description: '只读诊断文献副本：文件是否存在、哈希是否一致、上游状态。不自动修改。',
      inputSchema: { paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ paper_id: paperId }).parse(args)
        return ok({ report: serviceOf(ctx).reconcilePaper(input.paper_id) })
      }
    }),

    readTool({
      name: 'paper_note_get',
      title: '论文笔记读取',
      description: '读取论文的 Paper Note（结构化字段、正文、关联）。',
      inputSchema: { project_id: projectId, paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ project_id: projectId, paper_id: paperId }).parse(args)
        const note = serviceOf(ctx).getPaperNote(input.project_id, input.paper_id)
        return ok({ note: note === null ? null : noteView(note) })
      }
    }),

    writeTool({
      name: 'paper_note_create',
      title: '创建论文笔记',
      description: '为项目已关联的论文创建 Paper Note。',
      inputSchema: { project_id: projectId, paper_id: paperId },
      handler: (ctx, args) => {
        const input = z.object({ project_id: projectId, paper_id: paperId }).parse(args)
        return ok({
          note: noteView(serviceOf(ctx).createPaperNote(input.project_id, input.paper_id, 'mcp'))
        })
      }
    }),

    writeTool({
      name: 'paper_note_update',
      title: '更新论文笔记字段',
      description:
        '更新 Paper Note 的结构化字段或标题（整体替换 fields，未提供则不改）。expected_updated_at 必填。',
      inputSchema: {
        project_id: projectId,
        note_id: noteId,
        title: z.string().optional().describe('标题'),
        fields: fieldsSchema.optional(),
        expected_updated_at: z.string().min(1).describe('必填并发令牌 RFC3339')
      },
      handler: (ctx, args) => {
        const input = z
          .object({
            project_id: projectId,
            note_id: noteId,
            title: z.string().optional(),
            fields: z.record(z.string(), z.unknown()).optional(),
            expected_updated_at: z.string().min(1)
          })
          .parse(args)
        const service = serviceOf(ctx)
        const patch = {
          ...(input.title === undefined ? {} : { title: input.title }),
          ...(input.fields === undefined ? {} : { fields: fieldsOf(input.fields) })
        }
        const updated = service.updatePaperNoteByNoteId(
          input.note_id,
          patch,
          input.expected_updated_at,
          'mcp'
        )
        return ok({ note: noteView(updated.view) })
      }
    }),

    writeTool({
      name: 'paper_note_save_body',
      title: '保存论文笔记正文',
      description: '以乐观并发保存 Paper Note 正文；base hash 不一致返回 CONFLICT。',
      inputSchema: {
        project_id: projectId,
        note_id: noteId,
        body_md: bodyMd,
        expected_updated_at: z.string().min(1).describe('必填并发令牌 RFC3339')
      },
      handler: (ctx, args) => {
        const input = z
          .object({
            project_id: projectId,
            note_id: noteId,
            body_md: bodyMd,
            expected_updated_at: z.string().min(1)
          })
          .parse(args)
        const service = serviceOf(ctx)
        const current = service.paperNoteByNoteId(input.note_id)
        if (current.view.note.updatedAt !== input.expected_updated_at) {
          throw appError(AppErrorCode.Conflict, '笔记已被其他写入修改', {
            details: {
              expected_updated_at: input.expected_updated_at,
              current_updated_at: current.view.note.updatedAt
            }
          })
        }
        const saved = service.savePaperNoteBodyByNoteId(
          input.note_id,
          input.body_md,
          current.view.bodyHash ?? '',
          'mcp'
        )
        return ok({
          note: saved.view === null ? null : noteView(saved.view),
          body_hash: saved.fileHash
        })
      }
    }),

    readTool({
      name: 'literature_overview_get',
      title: '文献综述读取',
      description: '读取项目的 Literature Overview（Markdown 正文）。',
      inputSchema: { project_id: projectId },
      handler: (ctx, args) => {
        const input = z.object({ project_id: projectId }).parse(args)
        const overview = serviceOf(ctx).getOverview(input.project_id)
        return ok({ overview: overview === null ? null : overviewView(overview) })
      }
    }),

    writeTool({
      name: 'literature_overview_update',
      title: '保存文献综述',
      description: '保存 Literature Overview 正文；可带 expected_hash 做乐观并发。',
      inputSchema: {
        project_id: projectId,
        body_md: bodyMd,
        expected_hash: z.string().optional().describe('读取时的 body_hash（可选）')
      },
      handler: (ctx, args) => {
        const input = z
          .object({
            project_id: projectId,
            body_md: bodyMd,
            expected_hash: z.string().optional()
          })
          .parse(args)
        const saved = serviceOf(ctx).saveOverview(
          input.project_id,
          input.body_md,
          input.expected_hash,
          'mcp'
        )
        return ok({ overview: overviewView(saved.overview) })
      }
    })
  ]
}

/** The tool names this module contributes, in catalog order. */
export const LITERATURE_TOOL_NAMES = [
  'zotero_status',
  'zotero_libraries',
  'zotero_collections',
  'zotero_items',
  'paper_list',
  'paper_get',
  'paper_link',
  'paper_unlink',
  'paper_import',
  'paper_sync_check',
  'paper_sync_apply',
  'paper_refresh_metadata',
  'paper_reconcile',
  'paper_note_get',
  'paper_note_create',
  'paper_note_update',
  'paper_note_save_body',
  'literature_overview_get',
  'literature_overview_update'
] as const