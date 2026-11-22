/**
 * `mcp_job`: background job runner for `J` tools.
 *
 * The row stores the typed error `{code,message,details}` — the cause survives
 * the process boundary instead of becoming an opaque INTERNAL (defect 1).
 * Cancellation uses one AbortController per job; startup recovery marks every
 * queued/running row failed(INTERRUPTED) so a restart never loses the fact.
 */
import type { JsonObject, McpJob } from '@researchos/shared'
import { AppError, AppErrorCode, isAppError } from '@researchos/shared'

import { type Clock } from '../../clock'
import { appError } from '../../errors'
import { newId } from '../../ids'
import type { Principal, ProgressReporter } from './types'
import { type Db, type RuntimeOptions, clampLimit, clockOf, jsonObject, jsonText, nullText, nowText } from './store'

/** Write-job concurrency caps. */
export const MAX_WRITE_JOBS_PER_PROJECT = 2
export const MAX_WRITE_JOBS_GLOBAL = 4
export const JOB_RATE_LIMIT_RETRY_MS = 5000

export interface JobRequest {
  tool: string
  projectId?: string | null
  readOnly: boolean
  params?: JsonObject
  traceId?: string | null
  run: (run: JobExecutionContext) => Promise<unknown>
}

export interface JobExecutionContext {
  readonly signal: AbortSignal
  readonly progress: ProgressReporter
  readonly projectId: string | null
}

export interface JobFilter {
  projectId?: string
  state?: McpJob['state']
  limit?: number
}

/**
 * Renderer-facing job notifications. The desktop composition root forwards
 * these as `job.progress` / `job.done`, so the UI can finish the operation it
 * started without polling `jobs.get`.
 */
export interface JobEventHooks {
  onProgress?: (job: McpJob) => void
  onDone?: (job: McpJob) => void
}

export interface JobsOptions extends RuntimeOptions, JobEventHooks {}

interface JobRow {
  id: string
  tool: string
  project_id: string | null
  client_id: string
  state: string
  progress: number
  stage: string
  read_only: number
  params_json: string | null
  result_json: string | null
  error_json: string | null
  created_at: string
  started_at: string | null
  finished_at: string | null
  trace_id: string | null
}

const COLUMNS = `id, tool, project_id, client_id, state, progress, stage, read_only,
  params_json, result_json, error_json, created_at, started_at, finished_at, trace_id`

function toJob(row: JobRow): McpJob {
  const raw = jsonObject(row.error_json)
  return {
    id: row.id,
    tool: row.tool,
    projectId: row.project_id,
    clientId: row.client_id,
    state: row.state as McpJob['state'],
    progress: row.progress,
    stage: row.stage,
    readOnly: row.read_only === 1,
    params: jsonObject(row.params_json),
    result: jsonObject(row.result_json),
    error: raw === null ? null : (raw as unknown as McpJob['error']),
    createdAt: row.created_at,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    traceId: row.trace_id
  }
}

function terminalProgress(state: McpJob['state'], last: number): number {
  if (state === 'succeeded') return 100
  return last
}

function terminalStage(state: McpJob['state']): string {
  switch (state) {
    case 'succeeded':
      return '已完成'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    default:
      return ''
  }
}

export class Jobs {
  private readonly db: Db
  private readonly clock: Clock
  private readonly cancels = new Map<string, AbortController>()
  private readonly projectWrites = new Map<string, number>()
  private readonly hooks: JobEventHooks
  private totalWrites = 0

  constructor(db: Db, options: JobsOptions = {}) {
    this.db = db
    this.clock = clockOf(options)
    this.hooks = {
      ...(options.onProgress === undefined ? {} : { onProgress: options.onProgress }),
      ...(options.onDone === undefined ? {} : { onDone: options.onDone })
    }
  }

  /**
   * Creates and starts a job. The write concurrency check happens before the
   * insert, so a rejected submission leaves no row.
   */
  submit(principal: Principal, request: JobRequest): McpJob {
    if (!request.readOnly && this.totalWrites >= MAX_WRITE_JOBS_GLOBAL) {
      throw appError(
        AppErrorCode.RateLimited,
        `后台写作业并发已达上限（全局 ${MAX_WRITE_JOBS_GLOBAL}），请稍后重试`,
        { details: { retry_after_ms: JOB_RATE_LIMIT_RETRY_MS } }
      )
    }
    if (!request.readOnly) {
      const projectKey = request.projectId ?? ''
      const current = this.projectWrites.get(projectKey) ?? 0
      if (current >= MAX_WRITE_JOBS_PER_PROJECT) {
        throw appError(
          AppErrorCode.RateLimited,
          `后台写作业并发已达上限（每项目 ${MAX_WRITE_JOBS_PER_PROJECT}），请稍后重试`,
          { details: { retry_after_ms: JOB_RATE_LIMIT_RETRY_MS } }
        )
      }
      this.projectWrites.set(projectKey, current + 1)
      this.totalWrites += 1
    }

    const id = newId(this.clock)
    const clientId = principal.clientId === '' ? 'local' : principal.clientId
    const projectId = request.projectId ?? null
    try {
      this.db
        .prepare(
          `INSERT INTO mcp_job (id, tool, project_id, client_id, state, progress, stage, read_only,
             params_json, created_at, trace_id)
           VALUES (?, ?, ?, ?, 'queued', 0, '排队中', ?, ?, ?, ?)`
        )
        .run(
          id,
          request.tool,
          nullText(projectId),
          clientId,
          request.readOnly ? 1 : 0,
          jsonText(request.params ?? {}),
          nowText(this.clock),
          nullText(request.traceId)
        )
    } catch (error) {
      this.releaseWriteSlot(request.readOnly, projectId)
      throw error
    }

    const controller = new AbortController()
    this.cancels.set(id, controller)
    const record = this.requireJob(id)
    void this.execute(id, request, controller)
    return record
  }

  get(principal: Principal, id: string): McpJob {
    const job = this.requireJob(id)
    if (principal.clientId !== '' && job.clientId !== principal.clientId) {
      throw jobNotFound(id)
    }
    return job
  }

  list(principal: Principal, filter: JobFilter = {}): McpJob[] {
    const limit = clampLimit(filter.limit)
    let sql = `SELECT ${COLUMNS} FROM mcp_job WHERE 1 = 1`
    const params: Array<string | number> = []
    if (filter.projectId !== undefined && filter.projectId !== '') {
      sql += ' AND project_id = ?'
      params.push(filter.projectId)
    }
    if (filter.state !== undefined) {
      sql += ' AND state = ?'
      params.push(filter.state)
    }
    if (principal.clientId !== '') {
      sql += ' AND client_id = ?'
      params.push(principal.clientId)
    }
    sql += ' ORDER BY created_at DESC, id DESC LIMIT ?'
    params.push(limit)
    const rows = this.db.prepare(sql).all(...params) as JobRow[]
    return rows.map(toJob)
  }

  /** Best-effort cancel: a terminal job is a CONFLICT. */
  cancel(principal: Principal, id: string): McpJob {
    const job = this.get(principal, id)
    if (job.state !== 'queued' && job.state !== 'running') {
      throw appError(AppErrorCode.Conflict, `作业 ${id} 已结束（${job.state}），不能取消`, {
        details: { job_id: id, state: job.state }
      })
    }
    this.cancels.get(id)?.abort()
    return this.requireJob(id)
  }

  /** Startup recovery: every unfinished job becomes failed(INTERRUPTED). */
  recoverAtStartup(): number {
    const error = JSON.stringify({
      code: AppErrorCode.Interrupted,
      message: '应用重启，作业中断，不自动重试'
    })
    return this.db
      .prepare(
        `UPDATE mcp_job SET state = 'failed', finished_at = ?, stage = '失败', error_json = ?
         WHERE state IN ('queued','running')`
      )
      .run(nowText(this.clock), error).changes
  }

  /** Retention sweep; only terminal rows older than `before` are removed. */
  purge(before: string): number {
    return this.db
      .prepare(
        `DELETE FROM mcp_job WHERE state IN ('succeeded','failed','cancelled') AND created_at < ?`
      )
      .run(before).changes
  }

  /** Number of jobs still queued/running (status + tests). */
  activeCount(): number {
    const row = this.db
      .prepare(`SELECT COUNT(*) AS n FROM mcp_job WHERE state IN ('queued','running')`)
      .get() as { n: number }
    return row.n
  }

  private async execute(id: string, request: JobRequest, controller: AbortController): Promise<void> {
    const started = nowText(this.clock)
    this.db
      .prepare(
        `UPDATE mcp_job SET state = 'running', started_at = ?, stage = '开始执行'
         WHERE id = ? AND state = 'queued'`
      )
      .run(started, id)

    let lastProgress = 0
    const progress: ProgressReporter = (percent, stage = '') => {
      const next = Math.max(lastProgress, Math.min(100, Math.max(0, Math.trunc(percent))))
      if (next === lastProgress && stage === '') return
      lastProgress = next
      this.db
        .prepare(
          `UPDATE mcp_job SET progress = ?, stage = ? WHERE id = ? AND state IN ('queued','running')`
        )
        .run(next, stage, id)
      this.notify('progress', id)
    }

    let result: unknown
    let failure: unknown
    try {
      if (controller.signal.aborted) throw cancelled()
      result = await request.run({
        signal: controller.signal,
        progress,
        projectId: request.projectId ?? null
      })
      if (controller.signal.aborted) throw cancelled()
    } catch (error) {
      failure = error
    }

    const cancelledRun = controller.signal.aborted || (failure instanceof JobCancelled)
    const state: McpJob['state'] = cancelledRun ? 'cancelled' : failure === undefined ? 'succeeded' : 'failed'
    const errorShape =
      state === 'failed' ? shapeError(failure, request.traceId ?? '') : null
    this.db
      .prepare(
        `UPDATE mcp_job SET state = ?, progress = ?, stage = ?, result_json = ?, error_json = ?,
           finished_at = ? WHERE id = ?`
      )
      .run(
        state,
        terminalProgress(state, lastProgress),
        terminalStage(state),
        jsonText(result ?? null),
        errorShape === null ? null : JSON.stringify(errorShape),
        nowText(this.clock),
        id
      )
    this.cancels.delete(id)
    this.releaseWriteSlot(request.readOnly, request.projectId ?? null)
    this.notify('done', id)
  }

  /** A failed notification must never fail the job it describes. */
  private notify(kind: 'progress' | 'done', id: string): void {
    const hook = kind === 'progress' ? this.hooks.onProgress : this.hooks.onDone
    if (hook === undefined) return
    try {
      hook(this.requireJob(id))
    } catch {
      // The row was purged or the listener threw; the job outcome is unchanged.
    }
  }

  private releaseWriteSlot(readOnly: boolean, projectId: string | null): void {
    if (readOnly) return
    const key = projectId ?? ''
    const current = this.projectWrites.get(key) ?? 0
    if (current <= 1) this.projectWrites.delete(key)
    else this.projectWrites.set(key, current - 1)
    this.totalWrites = Math.max(0, this.totalWrites - 1)
  }

  private requireJob(id: string): McpJob {
    const row = this.db.prepare(`SELECT ${COLUMNS} FROM mcp_job WHERE id = ?`).get(id) as JobRow | undefined
    if (row === undefined) throw jobNotFound(id)
    return toJob(row)
  }
}

/** Marker for cooperative cancellation; never crosses the tool boundary. */
class JobCancelled extends Error {
  constructor() {
    super('job cancelled')
    this.name = 'JobCancelled'
  }
}

function cancelled(): JobCancelled {
  return new JobCancelled()
}

function shapeError(error: unknown, traceId: string): { code: string; message: string; details?: JsonObject } {
  if (isAppError(error)) {
    const shape: { code: string; message: string; details?: JsonObject } = {
      code: error.code,
      message: error.message
    }
    if (error.details !== undefined) shape.details = error.details
    return shape
  }
  const message = error instanceof Error ? error.message : String(error)
  return { code: AppErrorCode.Internal, message, details: traceId === '' ? undefined : { traceId } }
}

function jobNotFound(id: string) {
  return new AppError(AppErrorCode.NotFound, `作业 ${id} 不存在`, { job_id: id })
}