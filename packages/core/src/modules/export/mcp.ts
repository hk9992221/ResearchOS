/**
 * The three export/import MCP tools of the catalog:
 *   project_export          J  (writing job) → job_id
 *   project_import_inspect  R                 → importPlanView
 *   project_import_confirm  C                 → pending confirmation
 *
 * Schemas are the catalog's argument names; `writeTool`/`jobTool`/`confirmTool`
 * inject the mandatory idempotency key (defect 5). Errors stay typed AppErrors
 * and never collapse to INTERNAL for a known domain failure.
 */
import { z } from 'zod'

import {
  AppErrorCode,
  isAppError,
  type ExportArtifact,
  type ExportPlan,
  type ImportInspectPlan,
  type ImportResult,
  type JsonObject
} from '@researchos/shared'

import { argsHash } from '../mcp/idempotency'
import {
  confirmTool,
  jobQueued,
  jobTool,
  ok,
  pending,
  readTool,
  runHandler,
  toolError
} from '../mcp/tools'
import type { HandlerContext, Principal, ProgressReporter, ToolDefinition } from '../mcp/types'
import type { ConfirmationEffect } from '../mcp/executors'

export const EXPORT_RUNTIME_KEY = 'export.runtime'

export const EXPORT_TOOL_NAMES = [
  'project_export',
  'project_import_inspect',
  'project_import_confirm'
] as const

/** The service methods the tools call; `ExportService` satisfies it. */
export interface ExportToolService {
  plan(request: { projectId: string; format: 'folder' | 'zip' | 'tar.gz'; destDir?: string }): ExportPlan
  run(
    request: { projectId: string; format: 'folder' | 'zip' | 'tar.gz'; destDir?: string },
    options: { signal?: AbortSignal; progress?: (percent: number, stage: string) => void },
    actor?: 'user' | 'mcp'
  ): Promise<ExportArtifact>
  inspect(sourcePath: string): Promise<ImportInspectPlan>
  importBundle(
    planId: string,
    request: { newIdentity?: boolean; name?: string },
    options?: { signal?: AbortSignal; progress?: (percent: number, stage: string) => void }
  ): Promise<ImportResult>
}

export interface ExportJobSubmitRequest {
  tool: string
  projectId: string
  clientId: string
  readOnly: boolean
  params: JsonObject
  traceId: string
  signal: AbortSignal
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
}

export interface ExportJobPort {
  submit(request: ExportJobSubmitRequest): Promise<{ jobId: string }>
}

export interface ExportConfirmationRequest {
  tool: string
  args: JsonObject
  argsHash: string
  title: string
  summary: string
  impact?: JsonObject
  projectId?: string | null
  traceId: string
}

export interface ExportConfirmationPort {
  enqueue(principal: Principal, request: ExportConfirmationRequest): { id: string; expiresAt: string }
}

export interface ExportMcpRuntime {
  service: ExportToolService
  jobs: ExportJobPort
  confirmations?: ExportConfirmationPort
}

function runtimeOf(ctx: HandlerContext): ExportMcpRuntime {
  const runtime = ctx.services[EXPORT_RUNTIME_KEY]
  if (runtime === undefined) {
    throw toolError(AppErrorCode.Internal, 'export runtime is not wired', { traceId: ctx.traceId })
  }
  return runtime as ExportMcpRuntime
}

function confirmationsOf(runtime: ExportMcpRuntime): ExportConfirmationPort {
  if (runtime.confirmations === undefined) {
    throw toolError(AppErrorCode.Internal, 'export: confirmation service is not configured', {
      port: 'confirmations'
    })
  }
  return runtime.confirmations
}

const projectIdSchema = z.string().min(1).describe('项目 ID')
const sourcePathSchema = z.string().min(1).max(4096).describe('归档包路径（绝对路径）')
const mcpFormatSchema = z.enum(['folder', 'zip', 'tar_gz']).describe('导出格式')

function internalFormat(format: 'folder' | 'zip' | 'tar_gz'): 'folder' | 'zip' | 'tar.gz' {
  return format === 'tar_gz' ? 'tar.gz' : format
}

function planView(plan: ImportInspectPlan): Record<string, unknown> {
  return {
    plan_id: plan.planId,
    source_path: plan.sourcePath,
    container: plan.container,
    exported_at: plan.exportedAt ?? null,
    schema_project: plan.schemaVersion,
    schema_global: plan.schemaGlobal ?? 0,
    project: { id: plan.projectId, name: plan.projectName },
    counts: {
      files: plan.fileCount,
      notes: plan.counts?.notes ?? 0,
      papers: plan.counts?.papers ?? 0,
      attachments: plan.counts?.attachments ?? 0,
      bytes: plan.totalBytes
    },
    integrity_ok: plan.files.every((file) => file.matchesManifest),
    verified_files: plan.files.filter((file) => file.matchesManifest).length,
    issues: plan.issues ?? [],
    conflict: { id_taken: plan.idConflict, name_taken: plan.nameConflict },
    expires_at: plan.expiresAt ?? null,
    can_import: plan.canImport
  }
}

/** Confirm-time inspection: an integrity failure is reported as CONFLICT. */
async function inspectForConfirm(
  service: ExportToolService,
  sourcePath: string
): Promise<ImportInspectPlan> {
  try {
    return await service.inspect(sourcePath)
  } catch (error) {
    if (isAppError(error) && error.code === AppErrorCode.Integrity) {
      throw toolError(AppErrorCode.Conflict, '归档包完整性校验失败，无法导入', {
        ...(error.details ?? {}),
        reason: 'integrity'
      })
    }
    throw error
  }
}

export const exportTools: ToolDefinition[] = [
  jobTool({
    name: 'project_export',
    title: '导出项目',
    description:
      '导出项目为 folder/zip/tar.gz 包，固定包含全部关联文献 PDF。返回 job_id，可用 job_get 轮询。',
    readOnly: false,
    inputSchema: {
      project_id: projectIdSchema,
      dest_dir: z
        .string()
        .max(4096)
        .optional()
        .describe('目标目录（绝对路径，必须存在且可写；省略时为 <工作区>/exports）'),
      format: mcpFormatSchema
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const raw = args as Record<string, unknown>
        const runtime = runtimeOf(ctx)
        const projectId = String(raw['project_id'])
        const format = internalFormat(raw['format'] as 'folder' | 'zip' | 'tar_gz')
        const destDir = typeof raw['dest_dir'] === 'string' ? raw['dest_dir'] : undefined
        const request = { projectId, format, ...(destDir === undefined ? {} : { destDir }) }
        // Preflight in the tool call so an invalid destination fails immediately.
        runtime.service.plan(request)
        const submitted = await runtime.jobs.submit({
          tool: 'project_export',
          projectId,
          clientId: ctx.principal.clientId,
          readOnly: false,
          params: {
            project_id: projectId,
            format: raw['format'],
            ...(destDir === undefined ? {} : { dest_dir: destDir })
          },
          traceId: ctx.traceId,
          signal: ctx.signal,
          run: async ({ signal, progress }) => {
            const artifact = await runtime.service.run(request, { signal, progress }, 'mcp')
            return {
              artifact_path: artifact.path,
              bytes: artifact.bytes,
              format,
              papers_included: artifact.papersIncluded,
              files_included: artifact.fileCount ?? 0,
              archive_sha256: artifact.sha256,
              warnings: artifact.warnings
            }
          }
        })
        return jobQueued({ job_id: submitted.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  }),

  readTool({
    name: 'project_import_inspect',
    title: '预检项目归档包',
    description:
      '只读预检一个导出包（文件夹 / .zip / .tar.gz）：识别容器与 manifest、逐文件校验 SHA-256、报告 schema 兼容性与项目 ID/名称冲突。返回 plan_id 供确认导入使用，不写入工作区。',
    inputSchema: { source_path: sourcePathSchema },
    handler: (ctx, args) =>
      runHandler(async () => {
        const raw = args as Record<string, unknown>
        const runtime = runtimeOf(ctx)
        const plan = await runtime.service.inspect(String(raw['source_path']))
        return ok(planView(plan))
      }, ctx.traceId)
  }),

  confirmTool({
    name: 'project_import_confirm',
    title: '确认导入项目',
    description:
      '请求导入已预检的项目归档包。工具只做预检并入队，必须在 ResearchOS 应用内人工确认后才恢复；冲突的 project.id 绝不被覆盖（可指定 new_identity）。',
    inputSchema: {
      source_path: sourcePathSchema,
      mode: z.enum(['restore_exact', 'new_identity']).describe('ID 冲突策略'),
      name: z.string().max(200).optional().describe('可选的项目显示名；与现有项目重名时自动加后缀')
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const raw = args as Record<string, unknown>
        const runtime = runtimeOf(ctx)
        const confirmations = confirmationsOf(runtime)
        const sourcePath = String(raw['source_path'])
        const mode = raw['mode'] === 'new_identity' ? 'new_identity' : 'restore_exact'
        const name = typeof raw['name'] === 'string' ? raw['name'] : undefined
        const plan = await inspectForConfirm(runtime.service, sourcePath)
        if (mode === 'restore_exact' && plan.idConflict) {
          throw toolError(
            AppErrorCode.Conflict,
            `项目 ${plan.projectId} 已存在；请改用 new_identity 或先删除旧项目`,
            { project_id: plan.projectId }
          )
        }
        const storedArgs: JsonObject = {
          source_path: sourcePath,
          mode,
          plan_id: plan.planId,
          ...(name === undefined ? {} : { name })
        }
        const impact: JsonObject = {
          plan_id: plan.planId,
          project_id: plan.projectId,
          project_name: plan.projectName,
          files: plan.fileCount,
          bytes: plan.totalBytes,
          papers: plan.counts?.papers ?? 0,
          id_conflict: plan.idConflict,
          name_conflict: plan.nameConflict
        }
        const summary = `从归档包导入「${plan.projectName}」（${plan.fileCount} 个文件）`
        const record = confirmations.enqueue(ctx.principal, {
          tool: 'project_import_confirm',
          args: storedArgs,
          argsHash: argsHash(storedArgs),
          title: '导入项目',
          summary,
          impact,
          projectId: null,
          traceId: ctx.traceId
        })
        return pending({
          project_id: plan.projectId,
          confirmation_id: record.id,
          expires_at: record.expiresAt,
          title: '导入项目',
          summary,
          impact
        })
      }, ctx.traceId)
  })
]

/**
 * The desktop executor for `project_import_confirm` (wired by W17). It
 * re-inspects the bundle at execution time so an on-disk change between the
 * request and the human approval cannot slip through.
 */
export function createImportConfirmEffect(service: ExportToolService): ConfirmationEffect {
  return async ({ args }) => {
    const sourcePath = typeof args['source_path'] === 'string' ? args['source_path'] : ''
    if (sourcePath === '') {
      throw toolError(AppErrorCode.InvalidArgument, 'project_import_confirm: 缺少 source_path', {
        field: 'source_path'
      })
    }
    const mode = args['mode'] === 'new_identity' ? 'new_identity' : 'restore_exact'
    const name = typeof args['name'] === 'string' ? args['name'] : undefined
    const plan = await inspectForConfirm(service, sourcePath)
    if (mode === 'restore_exact' && plan.idConflict) {
      throw toolError(AppErrorCode.Conflict, `项目 ${plan.projectId} 已存在`, {
        project_id: plan.projectId
      })
    }
    const result = await service.importBundle(plan.planId, {
      newIdentity: mode === 'new_identity',
      ...(name === undefined ? {} : { name })
    })
    return {
      project_id: result.projectId,
      project_name: result.projectName,
      mode: result.mode ?? mode,
      restored_files: result.restoredFiles,
      migrated_schema: result.migratedSchema,
      broken_refs: result.brokenRefs ?? 0,
      warnings: result.warnings
    }
  }
}

/** Alias used by the catalog aggregator once W17 wires the module. */
export const exportToolNames = EXPORT_TOOL_NAMES