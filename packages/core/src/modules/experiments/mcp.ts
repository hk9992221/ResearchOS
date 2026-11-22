import { z } from 'zod'

import type {
  ComparisonTableOptions,
  ExperimentDetail,
  ExperimentSummary,
  ExperimentView,
  GitInfo,
  RunDetail,
  RunParams,
  RunSummary,
  RunView,
  ScanRules,
  SnapshotDetail,
  SnapshotSummary,
  TargetRef
} from '@researchos/shared'
import { AppError, AppErrorCode } from '@researchos/shared'

import { jobQueued, jobTool, ok, readTool, runHandler, toolError, writeTool } from '../mcp/tools'
import type { HandlerContext, ProgressReporter, ToolDefinition } from '../mcp/types'
import { invalidInput } from './errors'
import type { ExperimentsService } from './service'

/** Services-bag key the composition root uses to hand over the runtime. */
export const EXPERIMENTS_RUNTIME_KEY = 'experiments.runtime'

export interface ExperimentsJobRequest {
  tool: string
  projectId: string
  clientId: string
  readOnly: boolean
  params: Record<string, unknown>
  traceId: string
  signal: AbortSignal
  run: (context: { signal: AbortSignal; progress: ProgressReporter }) => Promise<unknown>
}

export interface ExperimentsJobPort {
  submit(request: ExperimentsJobRequest): Promise<{ jobId: string }>
}

export interface ExperimentsMcpRuntime {
  service(projectId: string): ExperimentsService
  jobs: ExperimentsJobPort
  aiAvailable: boolean
}

function runtimeOf(ctx: HandlerContext): ExperimentsMcpRuntime {
  const runtime = ctx.services[EXPERIMENTS_RUNTIME_KEY]
  if (runtime === undefined) {
    throw toolError(AppErrorCode.Internal, 'experiments runtime is not wired', {
      traceId: ctx.traceId
    })
  }
  return runtime as ExperimentsMcpRuntime
}

function projectIdOf(ctx: HandlerContext, args: Record<string, unknown>): string {
  const projectId = args['project_id']
  if (typeof projectId !== 'string' || projectId === '') {
    throw invalidInput('project_id 必须是字符串')
  }
  if (ctx.projectId !== null && ctx.projectId !== projectId) {
    throw invalidInput('project_id 与请求上下文不一致')
  }
  return projectId
}

function idOf(args: Record<string, unknown>, field: string): string {
  const value = args[field]
  if (typeof value !== 'string' || value === '') throw invalidInput(`${field} 必须是字符串`)
  return value
}

function optionalString(args: Record<string, unknown>, field: string): string | undefined {
  const value = args[field]
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') throw invalidInput(`${field} 必须是字符串`)
  return value
}

function optionalBool(args: Record<string, unknown>, field: string, fallback: boolean): boolean {
  const value = args[field]
  if (value === undefined) return fallback
  if (typeof value !== 'boolean') throw invalidInput(`${field} 必须是布尔值`)
  return value
}

function stringArray(args: Record<string, unknown>, field: string): string[] {
  const value = args[field]
  if (value === undefined || value === null) return []
  if (!Array.isArray(value) || value.some((item) => typeof item !== 'string')) {
    throw invalidInput(`${field} 必须是字符串数组`)
  }
  return value as string[]
}

function scanRulesOf(args: Record<string, unknown>, field: string): ScanRules | undefined {
  if (!(field in args) || args[field] === undefined) return undefined
  const value = args[field]
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw invalidInput(`${field} 必须是对象`)
  }
  const raw = value as Record<string, unknown>
  const strings = (candidate: unknown): string[] | undefined =>
    Array.isArray(candidate) ? candidate.filter((item): item is string => typeof item === 'string') : undefined
  const number = (candidate: unknown): number | undefined =>
    typeof candidate === 'number' && Number.isFinite(candidate) ? candidate : undefined
  const bool = (candidate: unknown): boolean | undefined =>
    typeof candidate === 'boolean' ? candidate : undefined
  // MCP accepts the stored snake_case keys and the camelCase renderer shape.
  return {
    ...(strings(raw['include']) === undefined ? {} : { include: strings(raw['include']) }),
    ...(strings(raw['exclude']) === undefined ? {} : { exclude: strings(raw['exclude']) }),
    ...(number(raw['max_file_bytes'] ?? raw['maxFileBytes']) === undefined
      ? {}
      : { maxFileBytes: number(raw['max_file_bytes'] ?? raw['maxFileBytes']) }),
    ...(number(raw['max_total_bytes'] ?? raw['maxTotalBytes']) === undefined
      ? {}
      : { maxTotalBytes: number(raw['max_total_bytes'] ?? raw['maxTotalBytes']) }),
    ...(number(raw['max_files'] ?? raw['maxFiles']) === undefined
      ? {}
      : { maxFiles: number(raw['max_files'] ?? raw['maxFiles']) }),
    ...(number(raw['hash_max_bytes'] ?? raw['hashMaxBytes']) === undefined
      ? {}
      : { hashMaxBytes: number(raw['hash_max_bytes'] ?? raw['hashMaxBytes']) }),
    ...(bool(raw['follow_symlinks'] ?? raw['followSymlinks']) === undefined
      ? {}
      : { followSymlinks: bool(raw['follow_symlinks'] ?? raw['followSymlinks']) }),
    ...(bool(raw['include_images_in_ai'] ?? raw['includeImagesInAi']) === undefined
      ? {}
      : { includeImagesInAi: bool(raw['include_images_in_ai'] ?? raw['includeImagesInAi']) })
  }
}

function paramsOf(args: Record<string, unknown>): RunParams | undefined {
  if (!('params' in args) || args['params'] === undefined) return undefined
  const value = args['params']
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw invalidInput('params 必须是对象')
  }
  return value as RunParams
}

function cursorOffset(args: Record<string, unknown>): number {
  const cursor = args['cursor']
  if (cursor === undefined || cursor === '') return 0
  if (typeof cursor !== 'string') throw invalidInput('cursor 必须是字符串')
  const offset = Number.parseInt(cursor, 10)
  if (!Number.isFinite(offset) || offset < 0) throw invalidInput(`无效的 cursor：${cursor}`)
  return offset
}

function limitOf(args: Record<string, unknown>, fallback: number, max: number): number {
  const value = args['limit']
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isFinite(value)) throw invalidInput('limit 必须是数字')
  return Math.max(1, Math.min(max, Math.trunc(value)))
}

function targetOf(args: Record<string, unknown>, kind: 'experiment' | 'run'): TargetRef {
  return { kind, id: idOf(args, kind === 'run' ? 'run_id' : 'experiment_id') }
}

function scanRulesWire(rules: ScanRules): Record<string, unknown> {
  return {
    v: 1,
    include: rules.include ?? [],
    exclude: rules.exclude ?? [],
    max_file_bytes: rules.maxFileBytes ?? 0,
    max_total_bytes: rules.maxTotalBytes ?? 0,
    max_files: rules.maxFiles ?? 0,
    hash_max_bytes: rules.hashMaxBytes ?? 0,
    follow_symlinks: rules.followSymlinks === true,
    include_images_in_ai: rules.includeImagesInAi === true
  }
}

export function experimentWire(view: ExperimentView): Record<string, unknown> {
  return {
    experiment_id: view.id,
    title: view.title,
    purpose: view.purpose,
    scan_rules: scanRulesWire(view.scanRules),
    created_at: view.createdAt,
    updated_at: view.updatedAt,
    deleted_at: view.deletedAt,
    ...(view.dirRef === null ? {} : { dir_ref: view.dirRef })
  }
}

function experimentSummaryWire(item: ExperimentSummary): Record<string, unknown> {
  return {
    experiment_id: item.id,
    title: item.title,
    purpose: item.purpose,
    dir_status: item.dirStatus ?? '',
    dir_reason: item.dirReason,
    run_count: item.runCount,
    last_run_status: item.lastRunStatus,
    last_analyzed_at: item.lastAnalyzedAt,
    linked_ideas: item.linkedIdeas,
    updated_at: item.updatedAt,
    deleted_at: item.deletedAt,
    ...(item.dirRef === null ? {} : { dir_ref: item.dirRef })
  }
}

function experimentDetailWire(detail: ExperimentDetail): Record<string, unknown> {
  return {
    experiment: experimentWire(detail.experiment),
    dir_refs: detail.dirRefs,
    runs: detail.runs.map(runSummaryWire),
    recent_snapshots: detail.recentSnapshots.map(snapshotSummaryWire),
    linked_ideas: detail.linkedIdeas
  }
}

export function runWire(run: RunView): Record<string, unknown> {
  return {
    run_id: run.id,
    experiment_id: run.experimentId,
    title: run.title,
    params: run.params,
    status: run.status,
    created_at: run.createdAt,
    updated_at: run.updatedAt,
    deleted_at: run.deletedAt,
    ...(run.dirRef === null ? {} : { dir_ref: run.dirRef })
  }
}

function runDetailWire(detail: RunDetail): Record<string, unknown> {
  return { ...runWire(detail), dir_refs: detail.dirRefs }
}

function runSummaryWire(summary: RunSummary): Record<string, unknown> {
  return {
    run_id: summary.id,
    title: summary.title,
    display_name: summary.displayName,
    params: summary.params,
    param_count: summary.paramCount,
    status: summary.status,
    last_analyzed_at: summary.lastAnalyzedAt,
    deleted_at: summary.deletedAt,
    ...(summary.dirRef === null ? {} : { dir_ref: summary.dirRef })
  }
}

function snapshotSummaryWire(summary: SnapshotSummary): Record<string, unknown> {
  return {
    id: summary.id,
    target: { type: summary.targetType, id: summary.targetId },
    kind: summary.kind,
    created_at: summary.createdAt,
    provider_name: summary.providerName,
    model_id: summary.modelId,
    git_commit: summary.gitCommit,
    metric_count: summary.metricCount,
    warnings: summary.warnings,
    ai_status: summary.aiStatus,
    human_edited: summary.humanEdited,
    confirmed_at: summary.confirmedAt
  }
}

function snapshotWire(snapshot: SnapshotDetail): Record<string, unknown> {
  return {
    id: snapshot.id,
    target: { type: snapshot.targetType, id: snapshot.targetId },
    created_at: snapshot.createdAt,
    file_manifest: snapshot.fileManifest,
    git_info: snapshot.gitInfo,
    metrics: snapshot.metrics,
    ai_analysis: snapshot.aiAnalysis,
    model_info: snapshot.modelInfo
  }
}

function gitWire(info: GitInfo): Record<string, unknown> {
  return {
    repo: info.repoRoot,
    branch: info.branch,
    commit: info.commit,
    dirty: info.dirty,
    captured_at: info.capturedAt,
    detected: info.detected,
    available: info.available,
    changed_files: info.changedFiles,
    untracked_files: info.untrackedFiles,
    error: info.error
  }
}

async function submitJob(
  ctx: HandlerContext,
  runtime: ExperimentsMcpRuntime,
  tool: string,
  projectId: string,
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

const projectIdSchema = z.string().min(1).describe('项目 ID')
const experimentIdSchema = z.string().min(1).describe('实验 ID')

/**
 * The 17 experiments tools of docs/mcp-tools-catalog.json (13 `experiment_*` +
 * 4 `run_*`). Names, categories and annotations are unchanged; every write
 * tool declares `idempotency_key` through `writeTool`/`jobTool` (defect 5).
 */
export const experimentsTools: readonly ToolDefinition[] = [
  readTool({
    name: 'experiment_list',
    title: '实验列表',
    description: '列出项目实验，可按标题过滤。',
    inputSchema: {
      project_id: projectIdSchema,
      q: z.string().optional().describe('标题关键字'),
      include_deleted: z.boolean().optional().describe('包含已删除实验'),
      limit: z.number().int().min(1).max(200).optional().describe('返回条数，默认 50，上限 200'),
      cursor: z.string().optional().describe('上一页返回的 next_cursor')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const projectId = projectIdOf(ctx, args as Record<string, unknown>)
        const input = args as Record<string, unknown>
        const offset = cursorOffset(input)
        const limit = limitOf(input, 50, 200)
        const service = runtimeOf(ctx).service(projectId)
        const items = service.listExperiments({
          ...(optionalString(input, 'q') === undefined ? {} : { q: optionalString(input, 'q') }),
          includeDeleted: optionalBool(input, 'include_deleted', false),
          limit,
          offset
        })
        const next = items.length === limit ? String(offset + limit) : ''
        return ok({ items: items.map(experimentSummaryWire), next_cursor: next })
      }, ctx.traceId)
  }),

  readTool({
    name: 'experiment_get',
    title: '实验详情',
    description: '返回实验、目录引用、运行、快照与关联 Idea。',
    inputSchema: { project_id: projectIdSchema, experiment_id: experimentIdSchema },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        return ok(experimentDetailWire(service.getExperiment(idOf(input, 'experiment_id'))))
      }, ctx.traceId)
  }),

  writeTool({
    name: 'experiment_create',
    title: '创建实验',
    description:
      '创建实验；提供 dir_ref 时同时绑定目录（必须是已存在的绝对目录且不在工作区内）。',
    inputSchema: {
      project_id: projectIdSchema,
      title: z.string().min(1).max(200).describe('标题'),
      purpose: z.string().optional().describe('实验目的'),
      dir_ref: z.string().optional().describe('实验目录绝对路径'),
      scan_rules: z.record(z.string(), z.unknown()).optional().describe('扫描规则')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        const rules = scanRulesOf(input, 'scan_rules')
        const created = service.createExperiment({
          title: idOf(input, 'title'),
          ...(optionalString(input, 'purpose') === undefined ? {} : { purpose: optionalString(input, 'purpose') }),
          ...(optionalString(input, 'dir_ref') === undefined ? {} : { dirPath: optionalString(input, 'dir_ref') }),
          ...(rules === undefined ? {} : { rules }),
          actor: 'mcp'
        })
        return ok({
          experiment: experimentWire(created),
          ...(created.dirRef === null ? {} : { dir_ref: created.dirRef })
        })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'experiment_update',
    title: '更新实验',
    description:
      '更新实验标题、目的、扫描规则或目录。未绑定目录时 dir_ref 建立绑定，已绑定时重新指向新目录。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      title: z.string().optional().describe('标题'),
      purpose: z.string().optional().describe('实验目的'),
      scan_rules: z.record(z.string(), z.unknown()).optional().describe('扫描规则'),
      dir_ref: z.string().optional().describe('实验目录绝对路径（未绑定时建立，已绑定时重新定位）'),
      expected_updated_at: z.string().optional().describe('乐观并发令牌')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const title = optionalString(input, 'title')
        const purpose = optionalString(input, 'purpose')
        const rules = scanRulesOf(input, 'scan_rules')
        const dirRef = optionalString(input, 'dir_ref')
        if (title === undefined && purpose === undefined && rules === undefined && dirRef === undefined) {
          throw invalidInput('至少提供 title、purpose、scan_rules 或 dir_ref')
        }
        const service = runtimeOf(ctx).service(projectId)
        const updated = service.updateExperiment(experimentId, {
          ...(title === undefined ? {} : { title }),
          ...(purpose === undefined ? {} : { purpose }),
          ...(dirRef === undefined ? {} : { dirPath: dirRef }),
          ...(rules === undefined ? {} : { rules }),
          ...(optionalString(input, 'expected_updated_at') === undefined
            ? {}
            : { expectedUpdatedAt: optionalString(input, 'expected_updated_at') }),
          actor: 'mcp'
        })
        return ok({ experiment: experimentWire(updated) })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'experiment_delete',
    title: '删除实验',
    description: '软删除实验（进入回收站）。',
    inputSchema: { project_id: projectIdSchema, experiment_id: experimentIdSchema },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        service.deleteExperiment(idOf(input, 'experiment_id'), 'mcp')
        return ok({ deleted: true })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'experiment_relink_dir',
    title: '重新绑定实验目录',
    description: '把实验重新定位到已存在的绝对目录（修复 broken 引用，不清空旧快照）。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      dir_ref: z.string().describe('目录绝对路径')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        const ref = service.relocateDirectory(idOf(input, 'experiment_id'), idOf(input, 'dir_ref'), 'mcp')
        return ok({ experiment_id: idOf(input, 'experiment_id'), dir_status: ref })
      }, ctx.traceId)
  }),

  jobTool({
    name: 'experiment_scan',
    title: '扫描实验目录',
    description: '只读扫描实验目录并返回文件清单与排除项（不落库）。返回 job_id。',
    readOnly: true,
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      refresh: z.boolean().optional().describe('预留：当前扫描总是重新计算')
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const runtime = runtimeOf(ctx)
        const service = runtime.service(projectId)
        const job = await submitJob(
          ctx,
          runtime,
          'experiment_scan',
          projectId,
          true,
          { experiment_id: experimentId },
          async ({ progress, signal }) => {
            progress(20, '读取实验目录')
            const plan = service.scanPlan({ kind: 'experiment', id: experimentId }, undefined, signal)
            progress(90, '整理文件清单')
            return {
              files: plan.entries.map((entry) => ({
                path: entry.path,
                kind: entry.kind,
                bytes: entry.size,
                mtime: entry.mtime,
                hash: entry.sha256
              })),
              excluded: plan.pruned.map((entry) => ({ path: entry.path, reason: entry.reason })),
              truncated: plan.truncated
            }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  }),

  jobTool({
    name: 'experiment_generate_outline',
    title: '生成实验提纲',
    description: '用 AI 生成实验提纲草稿并保存为快照（不覆盖旧快照）。返回 job_id。',
    readOnly: false,
    inputSchema: { project_id: projectIdSchema, experiment_id: experimentIdSchema },
    handler: (ctx, args) =>
      runHandler(async () => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const runtime = runtimeOf(ctx)
        if (!runtime.aiAvailable) {
          throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置，无法生成实验提纲')
        }
        const service = runtime.service(projectId)
        const job = await submitJob(
          ctx,
          runtime,
          'experiment_generate_outline',
          projectId,
          false,
          { experiment_id: experimentId },
          async ({ progress, signal }) => {
            progress(20, '收集实验上下文')
            const snapshot = await service.generateOutline(experimentId, { signal })
            progress(95, '保存快照')
            return {
              snapshot_id: snapshot.id,
              outline: snapshot.aiAnalysis?.contentMd ?? ''
            }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  }),

  jobTool({
    name: 'experiment_analyze_results',
    title: '分析实验结果',
    description:
      '用 AI 分析实验结果并保存快照（不覆盖旧快照；指标解析遵循实验模块）。返回 job_id。',
    readOnly: false,
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      run_ids: z.array(z.string()).optional().describe('运行 ID（可选）')
    },
    handler: (ctx, args) =>
      runHandler(async () => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const runtime = runtimeOf(ctx)
        if (!runtime.aiAvailable) {
          throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置，无法分析实验结果')
        }
        const service = runtime.service(projectId)
        const runIds = stringArray(input, 'run_ids')
        const job = await submitJob(
          ctx,
          runtime,
          'experiment_analyze_results',
          projectId,
          false,
          { experiment_id: experimentId, run_ids: runIds },
          async ({ progress, signal }) => {
            const targets: TargetRef[] =
              runIds.length === 0
                ? [{ kind: 'experiment', id: experimentId }]
                : runIds.map((id) => ({ kind: 'run' as const, id }))
            const snapshots: Array<Record<string, unknown>> = []
            for (let index = 0; index < targets.length; index += 1) {
              const target = targets[index]
              if (target === undefined) continue
              progress(10 + Math.floor((80 * index) / targets.length), `分析 ${index + 1}/${targets.length}`)
              const snapshot = await service.generateResults(target, { signal })
              snapshots.push({
                snapshot_id: snapshot.id,
                content: snapshot.aiAnalysis?.contentMd ?? '',
                metrics: snapshot.metrics
              })
            }
            return snapshots.length === 1 ? (snapshots[0] ?? {}) : { snapshots }
          }
        )
        return jobQueued({ job_id: job.jobId, poll_after_ms: 1000 })
      }, ctx.traceId)
  }),

  readTool({
    name: 'experiment_snapshot_list',
    title: '快照列表',
    description: '列出实验或运行的 AI 分析快照。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      run_id: z.string().optional().describe('运行 ID（可选，替代 experiment_id 目标）')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const runId = optionalString(input, 'run_id')
        const target: TargetRef =
          runId === undefined || runId === ''
            ? { kind: 'experiment', id: experimentId }
            : { kind: 'run', id: runId }
        const service = runtimeOf(ctx).service(projectId)
        return ok({ snapshots: service.listSnapshots(target).map(snapshotSummaryWire) })
      }, ctx.traceId)
  }),

  readTool({
    name: 'experiment_snapshot_get',
    title: '快照详情',
    description: '返回快照的文件清单、Git 信息、指标、AI 分析与模型信息。',
    inputSchema: { project_id: projectIdSchema, snapshot_id: z.string().describe('快照 ID') },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        return ok({ snapshot: snapshotWire(service.getSnapshot(idOf(input, 'snapshot_id'))) })
      }, ctx.traceId)
  }),

  readTool({
    name: 'experiment_git_info',
    title: '实验 Git 信息',
    description: '只读检查实验或运行目录的 Git 仓库信息（分支、提交、脏状态）。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      run_id: z.string().optional().describe('运行 ID（可选）')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = idOf(input, 'experiment_id')
        const runId = optionalString(input, 'run_id')
        const service = runtimeOf(ctx).service(projectId)
        const target: TargetRef =
          runId === undefined || runId === '' ? { kind: 'experiment', id: experimentId } : { kind: 'run', id: runId }
        return ok(gitWire(service.gitInfo(target)))
      }, ctx.traceId)
  }),

  readTool({
    name: 'run_list',
    title: '运行列表',
    description: '列出实验下的运行。',
    inputSchema: { project_id: projectIdSchema, experiment_id: experimentIdSchema },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        return ok({ runs: service.listRuns(idOf(input, 'experiment_id')).map(runSummaryWire) })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'run_create',
    title: '创建运行',
    description: '在实验下创建运行，可指定目录与参数。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: experimentIdSchema,
      title: z.string().min(1).max(200).describe('标题'),
      dir_ref: z.string().optional().describe('运行目录绝对路径'),
      params: z.record(z.string(), z.unknown()).optional().describe('参数 {name: {value, source?, confirmed?}}')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        const params = paramsOf(input)
        const run = service.createRun({
          experimentId: idOf(input, 'experiment_id'),
          title: idOf(input, 'title'),
          ...(optionalString(input, 'dir_ref') === undefined ? {} : { dirPath: optionalString(input, 'dir_ref') }),
          ...(params === undefined ? {} : { params }),
          actor: 'mcp'
        })
        return ok({ run: runWire(run) })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'run_update',
    title: '更新运行',
    description:
      '单次调用只允许一组变更（title、params、status 或 dir_ref 之一）。状态仅接受实验模块定义的合法值，不做完成推断。',
    inputSchema: {
      project_id: projectIdSchema,
      run_id: z.string().describe('运行 ID'),
      title: z.string().min(1).max(200).optional().describe('运行标题'),
      params: z.record(z.string(), z.unknown()).optional().describe('参数 {name: {value, source?, confirmed?}}'),
      status: z.enum(['unknown', 'running', 'finished', 'failed']).optional().describe('运行状态'),
      dir_ref: z.string().optional().describe('运行目录绝对路径'),
      expected_updated_at: z.string().optional().describe('乐观并发令牌')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const runId = idOf(input, 'run_id')
        const hasTitle = input['title'] !== undefined
        const hasParams = input['params'] !== undefined
        const hasStatus = input['status'] !== undefined
        const hasDir = input['dir_ref'] !== undefined
        const groups = [hasTitle, hasParams, hasStatus, hasDir].filter(Boolean).length
        if (groups !== 1) throw invalidInput('title、params、status、dir_ref 每次只能提交一组')
        const service = runtimeOf(ctx).service(projectId)
        const expectedUpdatedAt = optionalString(input, 'expected_updated_at')
        const detail = service.updateRun(runId, {
          ...(hasTitle ? { title: idOf(input, 'title') } : {}),
          ...(hasParams ? { params: paramsOf(input) } : {}),
          ...(hasStatus ? { status: input['status'] as 'unknown' | 'running' | 'finished' | 'failed' } : {}),
          ...(hasDir ? { dirPath: idOf(input, 'dir_ref') } : {}),
          ...(expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt }),
          actor: 'mcp'
        })
        return ok({ run: runDetailWire(detail) })
      }, ctx.traceId)
  }),

  writeTool({
    name: 'run_delete',
    title: '删除运行',
    description: '软删除运行（进入回收站）。',
    inputSchema: { project_id: projectIdSchema, run_id: z.string().describe('运行 ID') },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const service = runtimeOf(ctx).service(projectId)
        service.deleteRun(idOf(input, 'run_id'), 'mcp')
        return ok({ deleted: true })
      }, ctx.traceId)
  }),

  readTool({
    name: 'experiment_compare_table',
    title: '实验对比矩阵与论文表格',
    description:
      '构建多 Run 对比矩阵（参数差异、指标最优/次优、相对 Baseline 的 Δ%）并生成 LaTeX booktabs / Markdown / CSV 表格源码。纯确定性计算，不调用 AI。',
    inputSchema: {
      project_id: projectIdSchema,
      experiment_id: z.string().optional().describe('实验 ID（run_ids 为空时必填）'),
      run_ids: z.array(z.string()).optional().describe('运行 ID（可跨实验自选）'),
      baseline_id: z.string().optional().describe('基准 Run ID，默认第一个 Run'),
      diff_params: z.boolean().optional().describe('仅保留各 Run 之间值不同的超参数'),
      directions: z
        .record(z.string(), z.enum(['higher', 'lower']))
        .optional()
        .describe('指标极值方向覆盖 {metric_key: higher|lower}'),
      format: z.enum(['latex_booktabs', 'markdown', 'csv']).optional().describe('表格格式'),
      caption: z.string().optional().describe('LaTeX caption'),
      label: z.string().optional().describe('LaTeX label（如 tab:ablation）'),
      include_params: z.array(z.string()).optional().describe('选入表格的超参数 key'),
      include_metrics: z.array(z.string()).optional().describe('选入表格的指标 key'),
      precision: z.number().int().min(0).max(10).optional().describe('小数位数，默认 4'),
      highlight_best: z.boolean().optional().describe('标注最优（粗体）与次优（下划线）'),
      show_delta: z.boolean().optional().describe('附带相对 Baseline 的 ±Δ% 列')
    },
    handler: (ctx, args) =>
      runHandler(() => {
        const input = args as Record<string, unknown>
        const projectId = projectIdOf(ctx, input)
        const experimentId = optionalString(input, 'experiment_id')
        const runIds = stringArray(input, 'run_ids')
        if ((experimentId === undefined || experimentId === '') && runIds.length === 0) {
          throw invalidInput('experiment_id 与 run_ids 至少提供一个')
        }
        const directions = input['directions']
        const service = runtimeOf(ctx).service(projectId)
        const tableOptions: ComparisonTableOptions = {
          ...(optionalString(input, 'format') === undefined
            ? {}
            : { format: optionalString(input, 'format') as ComparisonTableOptions['format'] }),
          ...(optionalString(input, 'caption') === undefined ? {} : { caption: optionalString(input, 'caption') }),
          ...(optionalString(input, 'label') === undefined ? {} : { label: optionalString(input, 'label') }),
          includeParams: stringArray(input, 'include_params'),
          includeMetrics: stringArray(input, 'include_metrics'),
          ...(typeof input['precision'] === 'number' ? { precision: input['precision'] } : {}),
          highlightBest: optionalBool(input, 'highlight_best', true),
          showDelta: optionalBool(input, 'show_delta', false)
        }
        const table = service.compareTable(
          {
            ...(experimentId === undefined || experimentId === '' ? {} : { experimentId }),
            ...(runIds.length === 0 ? {} : { runIds }),
            ...(optionalString(input, 'baseline_id') === undefined
              ? {}
              : { baselineId: optionalString(input, 'baseline_id') }),
            diffParams: optionalBool(input, 'diff_params', false),
            ...(directions === undefined || typeof directions !== 'object' || directions === null
              ? {}
              : { directions: directions as Record<string, 'higher' | 'lower'> })
          },
          tableOptions
        )
        return ok({
          format: table.format,
          table: table.table,
          baseline: table.baselineId,
          runs: table.runs,
          matrix: { param_rows: table.paramRows, metric_rows: table.metricRows }
        })
      }, ctx.traceId)
  })
]

/** Convenience for integration wiring and tests. */
export function experimentsToolByName(name: string): ToolDefinition | undefined {
  return experimentsTools.find((tool) => tool.name === name)
}

export function assertExperimentsToolCount(): void {
  if (experimentsTools.length !== 17) {
    throw new Error(`experiments tools must be 17, got ${experimentsTools.length}`)
  }
}
