import { z } from 'zod'

import { AppErrorCode, type JsonObject } from '@researchos/shared'

import { argsHash } from '../mcp/idempotency'
import {
  jobQueued,
  jobTool,
  ok,
  pending,
  readTool,
  runHandler,
  stripIdempotencyKey,
  toolError,
  confirmTool,
  writeTool
} from '../mcp/tools'
import type { HandlerContext, ProgressReporter, ToolDefinition } from '../mcp/types'

import type { GenerateOutcomeView, ReportPageView, ReportView } from './context'
import { periodFromDates } from './period'
import type {
  CreateReportInput,
  ExportInput,
  ExportResult,
  GenerateInput,
  ListReportsInput,
  UpdateContentInput,
  WeeklyPeriod
} from './types'

/**
 * The eight weekly-review MCP tools of the catalog (`report_*`).
 *
 * Names, categories and annotations match docs/mcp-tools-catalog.json. Every
 * write, confirm and writing-job tool declares a required `idempotency_key`
 * through `writeTool`/`confirmTool`/`jobTool` (defect 5); errors stay typed
 * AppErrors because every handler runs inside `runHandler`.
 */

/** Services-bag key the composition root uses to hand over the runtime. */
export const WEEKLY_RUNTIME_KEY = 'weekly.runtime'

export const WEEKLY_TOOL_NAMES = [
  'report_list',
  'report_get',
  'report_create',
  'report_generate',
  'report_update',
  'report_confirm',
  'report_delete',
  'report_export'
] as const

export interface WeeklyJobSubmitRequest {
  tool: string
  projectId: string
  clientId: string
  readOnly: boolean
  params: JsonObject
  traceId: string
  signal: AbortSignal
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
}

export interface WeeklyJobPort {
  submit(request: WeeklyJobSubmitRequest): Promise<{ jobId: string }>
}

export interface WeeklyConfirmationRequest {
  tool: string
  args: JsonObject
  argsHash: string
  title: string
  summary: string
  impact?: JsonObject
  projectId: string
  traceId: string
}

export interface WeeklyConfirmationPort {
  enqueue(
    principal: HandlerContext['principal'],
    request: WeeklyConfirmationRequest
  ): { id: string; expiresAt: string }
}

export interface WeeklyMcpRuntime {
  service(projectId: string): WeeklyToolService
  jobs: WeeklyJobPort
  confirmations?: WeeklyConfirmationPort
}

/** The service methods the tools call; `WeeklyService` satisfies it. */
export interface WeeklyToolService {
  currentPeriod(): WeeklyPeriod
  listReports(input: ListReportsInput): ReportPageView
  getReport(reportId: string): ReportView
  createReport(input: CreateReportInput): ReportView
  generate(input: GenerateInput): Promise<GenerateOutcomeView>
  updateContent(input: UpdateContentInput): ReportView
  deleteReport(reportId: string, actor?: 'user' | 'mcp'): void
  export(input: ExportInput): ExportResult
}

function runtimeOf(ctx: HandlerContext): WeeklyMcpRuntime {
  const runtime = ctx.services[WEEKLY_RUNTIME_KEY]
  if (runtime === undefined) {
    throw toolError(AppErrorCode.Internal, 'weekly runtime is not wired', { traceId: ctx.traceId })
  }
  return runtime as WeeklyMcpRuntime
}

function confirmationsOf(runtime: WeeklyMcpRuntime): WeeklyConfirmationPort {
  if (runtime.confirmations === undefined) {
    throw toolError(AppErrorCode.Internal, 'weekly: confirmation service is not configured', {
      port: 'confirmations'
    })
  }
  return runtime.confirmations
}

/** Reads arguments as a plain record; the Zod parse below owns validation. */
function recordOf(args: unknown): Record<string, unknown> {
  return args as Record<string, unknown>
}

function requireIdempotency(ctx: HandlerContext, args: Record<string, unknown>): string {
  const fromArgs = args.idempotency_key
  const key = ctx.idempotencyKey ?? (typeof fromArgs === 'string' ? fromArgs : null)
  if (key === null || key.trim() === '') {
    throw toolError(AppErrorCode.InvalidArgument, 'idempotency_key is required for write tools', {
      field: 'idempotency_key'
    })
  }
  if (key.length > 128) {
    throw toolError(AppErrorCode.InvalidArgument, 'idempotency_key exceeds 128 characters', {
      field: 'idempotency_key'
    })
  }
  return key
}

const projectIdSchema = z.string().min(1).describe('项目 ID')
const reportIdSchema = z.string().min(1).describe('周报 ID')

const listInput = z
  .object({
    project_id: projectIdSchema,
    limit: z.number().int().min(1).max(100).optional().describe('每页条数，默认 20，上限 100'),
    cursor: z.string().optional().describe('上一页返回的 next_cursor')
  })
  .strict()

const getInput = z.object({ project_id: projectIdSchema, report_id: reportIdSchema }).strict()
const createInput = z
  .object({
    project_id: projectIdSchema,
    period_start: z.string().length(10).describe('周期开始日期 YYYY-MM-DD'),
    period_end: z.string().length(10).describe('周期结束日期 YYYY-MM-DD')
  })
  .strict()
const generateInput = z
  .object({
    project_id: projectIdSchema,
    report_id: reportIdSchema,
    replace: z.boolean().optional().describe('是否覆盖已有内容，默认 false'),
    skill_id: z.string().optional().describe('写作 Skill ID，省略使用工作区默认')
  })
  .strict()
const updateInput = z
  .object({
    project_id: projectIdSchema,
    report_id: reportIdSchema,
    content_md: z.string().max(512 * 1024).describe('Markdown 正文'),
    expected_updated_at: z.string().optional().describe('并发令牌 RFC3339')
  })
  .strict()
const exportInput = z
  .object({
    project_id: projectIdSchema,
    report_id: reportIdSchema,
    dest_dir: z.string().optional().describe('目标目录（绝对路径，可省略）'),
    format: z.enum(['md', 'html']).describe('导出格式')
  })
  .strict()

function projectIdOf(ctx: HandlerContext, input: { project_id: string }): string {
  if (ctx.projectId !== null && ctx.projectId !== input.project_id) {
    throw toolError(AppErrorCode.InvalidArgument, 'project_id 与请求上下文不一致', {
      field: 'project_id'
    })
  }
  return input.project_id
}

function parsePeriod(
  input: { period_start: string; period_end: string },
  timeZone: string
): WeeklyPeriod {
  try {
    return periodFromDates(input.period_start, input.period_end, timeZone)
  } catch (error) {
    throw toolError(
      AppErrorCode.InvalidArgument,
      error instanceof Error ? error.message : 'period is invalid',
      { field: 'period_start' }
    )
  }
}

function cursorOffset(raw: string | undefined): number {
  if (raw === undefined || raw === '') return 0
  if (!/^\d+$/.test(raw)) {
    throw toolError(AppErrorCode.InvalidArgument, 'cursor is invalid', { field: 'cursor' })
  }
  return Number.parseInt(raw, 10)
}

/** One report without the body, for list/get metadata payloads. */
export function reportView(report: ReportView): Record<string, unknown> {
  return {
    report_id: report.reportId,
    period_start: report.periodStart,
    period_end: report.periodEnd,
    title: report.title,
    content_hash: report.contentHash,
    status: report.status,
    meta: report.meta,
    confirmed_at: report.confirmedAt,
    deleted_at: report.deletedAt,
    created_at: report.createdAt,
    updated_at: report.updatedAt
  }
}

function summaryView(summary: ReportPageView['items'][number]): Record<string, unknown> {
  return {
    report_id: summary.reportId,
    title: summary.title,
    period_start: summary.periodStart,
    period_end: summary.periodEnd,
    status: summary.status,
    excerpt: summary.excerpt,
    updated_at: summary.updatedAt,
    confirmed_at: summary.confirmedAt,
    deleted_at: summary.deletedAt
  }
}

async function submitJob(
  ctx: HandlerContext,
  runtime: WeeklyMcpRuntime,
  tool: string,
  projectId: string,
  options: { readOnly: boolean; params: JsonObject },
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
): Promise<{ jobId: string }> {
  return runtime.jobs.submit({
    tool,
    projectId,
    clientId: ctx.principal.clientId,
    readOnly: options.readOnly,
    params: options.params,
    traceId: ctx.traceId,
    signal: ctx.signal,
    run
  })
}

export const weeklyTools: readonly ToolDefinition[] = [
  readTool({
    name: 'report_list',
    title: '周报列表',
    description: '列出项目周报，按周期倒序。',
    inputSchema: listInput.shape,
    handler: (ctx, args) =>
      runHandler(() => {
        const input = listInput.parse(stripIdempotencyKey(recordOf(args)))
        const projectId = projectIdOf(ctx, input)
        const offset = cursorOffset(input.cursor)
        const limit = input.limit ?? 20
        const page = runtimeOf(ctx)
          .service(projectId)
          .listReports({ page: Math.floor(offset / limit) + 1, pageSize: limit })
        const next = offset + page.items.length < page.total ? String(offset + page.items.length) : null
        return ok({
          items: page.items.map(summaryView),
          total: page.total,
          next_cursor: next
        })
      }, ctx.traceId)
  }),

  readTool({
    name: 'report_get',
    title: '周报详情',
    description: '返回周报元数据与 Markdown 正文。',
    inputSchema: getInput.shape,
    handler: (ctx, args) =>
      runHandler(() => {
        const input = getInput.parse(stripIdempotencyKey(recordOf(args)))
        const projectId = projectIdOf(ctx, input)
        const report = runtimeOf(ctx).service(projectId).getReport(input.report_id)
        return ok({ report: reportView(report), content_md: report.contentMd })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'report_create',
    title: '创建周报',
    description: '创建一个空 draft 周报。内容可用 report_generate 生成或 report_update 写入。',
    inputSchema: {
      project_id: projectIdSchema,
      period_start: z.string().length(10).describe('周期开始日期 YYYY-MM-DD'),
      period_end: z.string().length(10).describe('周期结束日期 YYYY-MM-DD')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const raw = recordOf(args)
        requireIdempotency(ctx, raw)
        const input = createInput.parse(stripIdempotencyKey(raw))
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        const period = parsePeriod(input, service.currentPeriod().timeZone)
        const report = service.createReport({ period, actor: 'mcp' })
        return ok({ report_id: report.reportId, status: report.status })
      }, ctx.traceId)
  }),

  jobTool({
    name: 'report_generate',
    title: '生成周报草稿',
    description: '依据活动汇总生成周报 draft（AI 不可用时降级为确定性骨架）。返回 job_id。',
    readOnly: false,
    inputSchema: {
      project_id: projectIdSchema,
      report_id: reportIdSchema,
      replace: z.boolean().optional().describe('是否覆盖已有内容，默认 false'),
      skill_id: z.string().optional().describe('写作 Skill ID，省略使用工作区默认')
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const raw = recordOf(args)
        const key = requireIdempotency(ctx, raw)
        const input = generateInput.parse(stripIdempotencyKey(raw))
        const projectId = projectIdOf(ctx, input)
        const runtime = runtimeOf(ctx)
        const service = runtime.service(projectId)
        const replace = input.replace === true
        if (replace) {
          const current = service.getReport(input.report_id)
          if (current.contentMd.trim() !== '') {
            // Spec §4.8: overwriting existing content is an in-app decision,
            // so this variant is a confirmation, not a job.
            const request = { report_id: input.report_id, replace, skill_id: input.skill_id ?? '' }
            const impact: JsonObject = {
              report_id: input.report_id,
              status: current.status,
              content_hash: current.contentHash
            }
            const confirmation = confirmationsOf(runtime).enqueue(ctx.principal, {
              tool: 'report_generate',
              args: request,
              argsHash: argsHash(request),
              title: '重新生成周报',
              summary: `覆盖周报《${current.title}》的现有正文（重新生成会替换全部内容）。`,
              impact,
              projectId,
              traceId: ctx.traceId
            })
            return pending({
              confirmation_id: confirmation.id,
              expires_at: confirmation.expiresAt,
              title: '重新生成周报',
              summary: `覆盖周报《${current.title}》的现有正文。`,
              impact
            })
          }
        }
        const job = await submitJob(
          ctx,
          runtime,
          'report_generate',
          projectId,
          {
            readOnly: false,
            params: {
              report_id: input.report_id,
              replace,
              skill_id: input.skill_id ?? '',
              idempotency_key: key
            }
          },
          async ({ progress }) => {
            progress(10, '汇总活动')
            const outcome = await service.generate({
              reportId: input.report_id,
              replace,
              actor: 'mcp',
              skillId: input.skill_id,
              progress: (done, total, section) => {
                if (total <= 0) return
                progress(
                  10 + Math.round((done / total) * 80),
                  section === '' ? '写入草稿' : `生成分区 ${section}`
                )
              }
            })
            progress(95, '写入草稿')
            return {
              report_id: outcome.report.reportId,
              status: outcome.report.status,
              updated_at: outcome.report.updatedAt
            }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'report_update',
    title: '编辑周报正文',
    description: '替换周报 Markdown 正文（仅 draft 可改）。可带 expected_updated_at 做乐观并发。',
    inputSchema: {
      project_id: projectIdSchema,
      report_id: reportIdSchema,
      content_md: z.string().max(512 * 1024).describe('Markdown 正文'),
      expected_updated_at: z.string().optional().describe('并发令牌 RFC3339')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const raw = recordOf(args)
        requireIdempotency(ctx, raw)
        const input = updateInput.parse(stripIdempotencyKey(raw))
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        const current = input.expected_updated_at === undefined ? service.getReport(input.report_id) : undefined
        const report = service.updateContent({
          reportId: input.report_id,
          contentMd: input.content_md,
          expectedUpdatedAt: input.expected_updated_at,
          expectedHash: current?.contentHash,
          actor: 'mcp'
        })
        return ok({ report: reportView(report) })
      }, ctx.traceId)
  }),

  confirmTool({
    name: 'report_confirm',
    title: '确认周报',
    description: '请求确认周报。工具只入队，必须在应用内人工批准后以 actor=user 执行。',
    inputSchema: { project_id: projectIdSchema, report_id: reportIdSchema },
    handler: (ctx, args) =>
      runHandler(() => {
        const raw = recordOf(args)
        requireIdempotency(ctx, raw)
        const input = getInput.parse(stripIdempotencyKey(raw))
        const projectId = projectIdOf(ctx, input)
        const runtime = runtimeOf(ctx)
        const report = runtime.service(projectId).getReport(input.report_id)
        const preview =
          report.contentMd.length > 4000
            ? `${report.contentMd.slice(0, 4000)}\n…（预览已截断）`
            : report.contentMd
        const impact: JsonObject = {
          report_id: report.reportId,
          period_start: report.periodStart,
          period_end: report.periodEnd,
          status: report.status,
          preview_md: preview
        }
        const request: JsonObject = { report_id: input.report_id }
        const summary = `确认周报《${report.title}》，确认后正文只读。`
        const confirmation = confirmationsOf(runtime).enqueue(ctx.principal, {
          tool: 'report_confirm',
          args: request,
          argsHash: argsHash(request),
          title: '确认周报',
          summary,
          impact,
          projectId,
          traceId: ctx.traceId
        })
        return pending({
          confirmation_id: confirmation.id,
          expires_at: confirmation.expiresAt,
          title: '确认周报',
          summary,
          impact
        })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'report_delete',
    title: '删除周报',
    description: '软删除周报（进入回收站）。',
    annotations: { destructiveHint: true },
    inputSchema: { project_id: projectIdSchema, report_id: reportIdSchema },
    handler: (ctx, args) =>
      runHandler(() => {
        const raw = recordOf(args)
        requireIdempotency(ctx, raw)
        const input = getInput.parse(stripIdempotencyKey(raw))
        runtimeOf(ctx).service(projectIdOf(ctx, input)).deleteReport(input.report_id, 'mcp')
        return ok({ deleted: true })
      }, ctx.traceId)
  }),

  jobTool({
    name: 'report_export',
    title: '导出周报',
    description: '把周报导出为 Markdown 或 HTML 文件。返回 job_id。',
    readOnly: false,
    inputSchema: {
      project_id: projectIdSchema,
      report_id: reportIdSchema,
      dest_dir: z.string().optional().describe('目标目录（绝对路径，可省略）'),
      format: z.enum(['md', 'html']).describe('导出格式')
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const raw = recordOf(args)
        const key = requireIdempotency(ctx, raw)
        const input = exportInput.parse(stripIdempotencyKey(raw))
        const projectId = projectIdOf(ctx, input)
        const runtime = runtimeOf(ctx)
        const service = runtime.service(projectId)
        const job = await submitJob(
          ctx,
          runtime,
          'report_export',
          projectId,
          {
            readOnly: false,
            params: {
              report_id: input.report_id,
              dest_dir: input.dest_dir ?? '',
              format: input.format,
              idempotency_key: key
            }
          },
          async ({ progress }) => {
            progress(30, '渲染周报')
            const result = service.export({
              reportId: input.report_id,
              format: input.format,
              destDir: input.dest_dir,
              actor: 'mcp'
            })
            progress(100, '已导出')
            return { artifact_path: result.path, bytes: result.bytes, format: result.format }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  })
]
