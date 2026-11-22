import { randomUUID } from 'node:crypto'

import { AppError, AppErrorCode } from '@researchos/shared'

import type { Clock } from '@researchos/core/clock'
import type { Logger } from '@researchos/core/logger'
import {
  ExperimentsService,
  type ExperimentsAiPort,
  type ExperimentsIndexHook,
  type ExperimentsJobPort
} from '@researchos/core/modules/experiments/index'
import type { WorkspaceManager } from '@researchos/core/workspace/index'
import {
  aiOutlineSchema,
  aiResultsSchema,
  compareSchema,
  experimentCreateSchema,
  experimentIdSchema,
  experimentListSchema,
  experimentUpdateSchema,
  gitInfoSchema,
  metricsExtractSchema,
  runCreateSchema,
  runIdSchema,
  runListSchema,
  runUpdateSchema,
  scanPreviewSchema,
  scanStartSchema,
  snapshotCreateSchema,
  snapshotGetSchema,
  snapshotListSchema
} from '@researchos/core/validation/experiments'

import type { CommandRouter } from '../router'

export interface ExperimentsHandlerDeps {
  /** The open workspace, or null while the wizard has not opened one. */
  workspace: () => WorkspaceManager | null
  /** MCP-style job runner; W4's JobManager implements it. */
  jobs: ExperimentsJobPort
  ai?: ExperimentsAiPort
  aiAvailable?: () => boolean
  indexer?: ExperimentsIndexHook
  clock?: Clock
  logger?: Logger
}

function requireWorkspace(deps: ExperimentsHandlerDeps): WorkspaceManager {
  const workspace = deps.workspace()
  if (workspace === null) {
    throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
  }
  return workspace
}

/**
 * Registers the 19 `experiments.*` IPC commands (rewrite plan §3.5, §4.6).
 * Long operations return `{jobId}`; validation lives in
 * `@researchos/core/validation/experiments` and every handler calls the same
 * service the MCP tools use, so schema drift cannot reappear (defect 10).
 */
export function registerExperimentsHandlers(router: CommandRouter, deps: ExperimentsHandlerDeps): void {
  const serviceFor = (projectId: string): ExperimentsService =>
    new ExperimentsService({
      workspace: requireWorkspace(deps),
      projectId,
      ...(deps.clock === undefined ? {} : { clock: deps.clock }),
      ...(deps.logger === undefined ? {} : { logger: deps.logger }),
      ...(deps.ai === undefined ? {} : { ai: deps.ai }),
      ...(deps.indexer === undefined ? {} : { indexer: deps.indexer })
    })

  const submit = async (
    projectId: string,
    tool: string,
    readOnly: boolean,
    params: Record<string, unknown>,
    run: (context: { signal: AbortSignal; progress: (percent: number, stage?: string) => void }) => Promise<unknown>
  ): Promise<{ jobId: string }> =>
    deps.jobs.submit({
      tool,
      projectId,
      clientId: 'desktop',
      readOnly,
      params,
      traceId: randomUUID(),
      signal: new AbortController().signal,
      run
    })

  router.registerHandler('experiments.list', experimentListSchema, (request) => {
    const service = serviceFor(request.projectId)
    return service.listExperiments({
      ...(request.q === undefined ? {} : { q: request.q }),
      ...(request.includeDeleted === undefined ? {} : { includeDeleted: request.includeDeleted }),
      ...(request.limit === undefined ? {} : { limit: request.limit }),
      ...(request.offset === undefined ? {} : { offset: request.offset })
    })
  })

  router.registerHandler('experiments.get', experimentIdSchema, (request) =>
    serviceFor(request.projectId).getExperiment(request.experimentId)
  )

  router.registerHandler('experiments.create', experimentCreateSchema, (request) =>
    serviceFor(request.projectId).createExperiment({
      title: request.title,
      ...(request.purpose === undefined ? {} : { purpose: request.purpose }),
      ...(request.dirRef === undefined ? {} : { dirPath: request.dirRef }),
      ...(request.scanRules === undefined ? {} : { rules: request.scanRules }),
      actor: 'user'
    })
  )

  router.registerHandler('experiments.update', experimentUpdateSchema, (request) =>
    serviceFor(request.projectId).updateExperiment(request.experimentId, {
      ...(request.patch.title === undefined ? {} : { title: request.patch.title }),
      ...(request.patch.purpose === undefined ? {} : { purpose: request.patch.purpose }),
      ...(request.patch.dirRef === undefined ? {} : { dirPath: request.patch.dirRef }),
      ...(request.patch.scanRules === undefined ? {} : { rules: request.patch.scanRules }),
      ...(request.expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt: request.expectedUpdatedAt }),
      actor: 'user'
    })
  )

  router.registerHandler('experiments.delete', experimentIdSchema, (request) => {
    serviceFor(request.projectId).deleteExperiment(request.experimentId, 'user')
    return { deleted: true }
  })

  router.registerHandler('experiments.scan.preview', scanPreviewSchema, (request) =>
    serviceFor(request.projectId).scanPlan(request.target, request.override)
  )

  router.registerHandler('experiments.scan.start', scanStartSchema, async (request) => {
    const service = serviceFor(request.projectId)
    return submit(request.projectId, 'experiment_scan', true, { target: request.target }, async ({ progress, signal }) => {
      progress(20, '读取实验目录')
      const plan = service.scanPlan(request.target, request.override, signal)
      progress(90, '整理文件清单')
      return plan
    })
  })

  router.registerHandler('experiments.runs.list', runListSchema, (request) =>
    serviceFor(request.projectId).listRuns(request.experimentId)
  )

  router.registerHandler('experiments.runs.get', runIdSchema, (request) =>
    serviceFor(request.projectId).getRun(request.runId)
  )

  router.registerHandler('experiments.runs.create', runCreateSchema, (request) =>
    serviceFor(request.projectId).createRun({
      experimentId: request.experimentId,
      title: request.title ?? '',
      ...(request.dirRef === undefined ? {} : { dirPath: request.dirRef }),
      ...(request.params === undefined ? {} : { params: request.params }),
      ...(request.status === undefined ? {} : { status: request.status }),
      actor: 'user'
    })
  )

  router.registerHandler('experiments.runs.update', runUpdateSchema, (request) =>
    serviceFor(request.projectId).updateRun(request.runId, {
      ...(request.patch.title === undefined ? {} : { title: request.patch.title }),
      ...(request.patch.dirRef === undefined ? {} : { dirPath: request.patch.dirRef }),
      ...(request.patch.params === undefined ? {} : { params: request.patch.params }),
      ...(request.patch.status === undefined ? {} : { status: request.patch.status }),
      ...(request.expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt: request.expectedUpdatedAt }),
      actor: 'user'
    })
  )

  router.registerHandler('experiments.runs.delete', runIdSchema, (request) => {
    serviceFor(request.projectId).deleteRun(request.runId, 'user')
    return { deleted: true }
  })

  router.registerHandler('experiments.snapshots.list', snapshotListSchema, (request) =>
    serviceFor(request.projectId).listSnapshots(request.target, {
      ...(request.kind === undefined ? {} : { kind: request.kind }),
      ...(request.limit === undefined ? {} : { limit: request.limit }),
      ...(request.offset === undefined ? {} : { offset: request.offset })
    })
  )

  router.registerHandler('experiments.snapshots.get', snapshotGetSchema, (request) =>
    serviceFor(request.projectId).getSnapshot(request.snapshotId)
  )

  router.registerHandler('experiments.snapshots.create', snapshotCreateSchema, (request) =>
    serviceFor(request.projectId).createSnapshot({
      target: request.target,
      ...(request.metrics === undefined ? {} : { metrics: request.metrics }),
      ...(request.aiAnalysis === undefined ? {} : { aiAnalysis: request.aiAnalysis }),
      ...(request.aiOutputId === undefined ? {} : { aiOutputId: request.aiOutputId }),
      ...(request.kind === undefined ? {} : { kind: request.kind })
    })
  )

  router.registerHandler('experiments.metrics.extract', metricsExtractSchema, (request) =>
    serviceFor(request.projectId).extractMetrics(request.target)
  )

  router.registerHandler('experiments.git.info', gitInfoSchema, (request) =>
    serviceFor(request.projectId).gitInfo(request.target)
  )

  router.registerHandler('experiments.compare', compareSchema, (request) =>
    serviceFor(request.projectId).compareTable(
      {
        ...(request.experimentId === undefined ? {} : { experimentId: request.experimentId }),
        ...(request.runIds === undefined ? {} : { runIds: request.runIds }),
        ...(request.baselineId === undefined ? {} : { baselineId: request.baselineId }),
        ...(request.diffParams === undefined ? {} : { diffParams: request.diffParams }),
        ...(request.directions === undefined ? {} : { directions: request.directions })
      },
      request.table ?? {}
    )
  )

  router.registerHandler('experiments.ai.outline', aiOutlineSchema, async (request) => {
    if (deps.aiAvailable !== undefined && !deps.aiAvailable()) {
      throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置')
    }
    const service = serviceFor(request.projectId)
    return submit(
      request.projectId,
      'experiment_generate_outline',
      false,
      { experimentId: request.experimentId },
      async ({ progress, signal }) => {
        progress(20, '收集实验上下文')
        const snapshot = await service.generateOutline(request.experimentId, {
          ...(request.extraInstructions === undefined ? {} : { extraInstructions: request.extraInstructions }),
          signal
        })
        progress(95, '保存快照')
        return { snapshotId: snapshot.id }
      }
    )
  })

  router.registerHandler('experiments.ai.results', aiResultsSchema, async (request) => {
    if (deps.aiAvailable !== undefined && !deps.aiAvailable()) {
      throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置')
    }
    const service = serviceFor(request.projectId)
    return submit(
      request.projectId,
      'experiment_analyze_results',
      false,
      { target: request.target },
      async ({ progress, signal }) => {
        progress(20, '收集实验上下文')
        const snapshot = await service.generateResults(request.target, {
          ...(request.extraInstructions === undefined ? {} : { extraInstructions: request.extraInstructions }),
          signal
        })
        progress(95, '保存快照')
        return { snapshotId: snapshot.id }
      }
    )
  })
}