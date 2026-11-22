/**
 * Workspace-scoped export/import service. Owns the preflight cache, the one
 * in-flight export/import lock, the in-memory job records and the inspection
 * plans. I/O lives in `export-run.ts` and `import.ts`; this file only
 * orchestrates, validates and shapes IPC/MCP results.
 */
import { existsSync } from 'node:fs'

import {
  AppErrorCode,
  type ExportArtifact,
  type ExportJobStatus,
  type ExportPlan,
  type ImportInspectPlan,
  type ImportJobStatus,
  type ImportResult,
  type JobRef
} from '@researchos/shared'

import { formatTimestamp, SystemClock, type Clock } from '../../clock'
import { appError, isAppError } from '../../errors'
import type { EventBus } from '../../events'
import { newId } from '../../ids'
import type { Locker } from '../../ports'
import type { WorkspaceManager } from '../../workspace'
import { deferred } from './deferred'
import { runExportPlan } from './export-run'
import { inspectBundle, restoreBundle } from './import'
import { MemoryLocker } from './locker'
import { freeBytesOf, planExport, type PlanRequest } from './plan'
import type {
  ExportJobRecord,
  ExportPlanState,
  ExportServiceOptions,
  ImportJobRecord,
  ImportPlanState
} from './types'

export interface StartExportRequest extends PlanRequest {
  /** Accepted by the contract; a bundle is never overwritten. */
  overwrite?: boolean
}

export interface StartImportRequest {
  newIdentity?: boolean
  name?: string
}

export interface ExportRunOptions {
  signal?: AbortSignal
  progress?: (percent: number, stage: string) => void
}

function busyError(resource: string): Error {
  return appError(AppErrorCode.RateLimited, `${resource} is already running`, {
    reason: 'busy',
    retry_after_ms: 2000
  })
}

function messageOf(error: unknown): string {
  if (isAppError(error)) return `${error.code}: ${error.message}`
  return error instanceof Error ? error.message : String(error)
}

function normalizeActor(actor: 'user' | 'mcp' | undefined): 'user' | 'mcp' {
  return actor === 'mcp' ? 'mcp' : 'user'
}

export class ExportService {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock
  private readonly locker: Locker
  private readonly events: EventBus | undefined
  private readonly reindex: ((projectId: string) => Promise<void> | void) | undefined
  private readonly options: ExportServiceOptions
  private readonly plans = new Map<string, ExportPlanState>()
  private readonly importPlans = new Map<string, ImportPlanState>()
  private readonly exportJobs = new Map<string, ExportJobRecord>()
  private readonly importJobs = new Map<string, ImportJobRecord>()
  private activeExport: ExportJobRecord | null = null
  private activeImport: ImportJobRecord | null = null
  private exportRunning = false
  private importRunning = false

  constructor(options: ExportServiceOptions) {
    this.options = options
    this.workspace = options.workspace
    this.clock = options.clock ?? new SystemClock()
    this.locker = options.locker ?? new MemoryLocker()
    this.events = options.events
    this.reindex = options.reindex
  }

  private planDeps(): {
    workspace: WorkspaceManager
    clock: Clock
    diskFree?: (dir: string) => { free: number; total: number } | null
  } {
    return {
      workspace: this.workspace,
      clock: this.clock,
      ...(this.options.diskFree === undefined ? {} : { diskFree: this.options.diskFree })
    }
  }

  /* ------------------------------------------------------------ preflight */

  private prunePlans(now: number): void {
    for (const [id, plan] of this.plans) {
      if (plan.expiresAt <= now) this.plans.delete(id)
    }
    for (const [id, state] of this.importPlans) {
      if (state.expiresAt <= now) this.importPlans.delete(id)
    }
  }

  /** Read-only preflight (§4.11). Never writes project data. */
  plan(request: PlanRequest): ExportPlan {
    this.prunePlans(this.clock.now().getTime())
    const state = planExport(this.planDeps(), request)
    this.plans.set(state.id, state)
    return this.viewOf(state)
  }

  private viewOf(state: ExportPlanState): ExportPlan {
    return {
      planId: state.id,
      projectId: state.projectId,
      projectName: state.project.name,
      format: state.format,
      destination: state.outputPath,
      requiredBytes: state.totalBytes,
      freeBytes: freeBytesOf(this.planDeps(), state.destDir),
      files: state.files.map((file) => ({
        relativePath: file.bundlePath,
        bytes: file.bytes,
        sha256: null
      })),
      missing: state.missing.map((ref) => `${ref.kind}: ${ref.ref}`),
      warnings: state.warnings.map((warning) => `${warning.code}: ${warning.path}`)
    }
  }

  private planForStart(request: StartExportRequest): ExportPlanState {
    const now = this.clock.now().getTime()
    this.prunePlans(now)
    for (const state of this.plans.values()) {
      if (
        state.projectId === request.projectId &&
        state.format === request.format &&
        (request.destDir === undefined || state.destDir === request.destDir) &&
        !existsSync(state.outputPath)
      ) {
        return state
      }
    }
    const state = planExport(this.planDeps(), request)
    this.plans.set(state.id, state)
    return state
  }

  /* --------------------------------------------------------------- export */

  private assertExportFree(): void {
    if (this.exportRunning) throw busyError('export')
  }

  /**
   * Starts a background export. Preflight errors are thrown synchronously so the
   * caller gets a precise code; copy failures land on the job status.
   */
  start(request: StartExportRequest, actor?: 'user' | 'mcp'): JobRef {
    if (request.overwrite === true) {
      throw appError(AppErrorCode.InvalidArgument, 'overwriting an existing bundle is not supported', {
        field: 'overwrite'
      })
    }
    this.assertExportFree()
    const plan = this.planForStart(request)
    const jobId = newId(this.clock)
    const completion = deferred<void>()
    const record: ExportJobRecord = {
      id: jobId,
      projectId: plan.projectId,
      format: plan.format,
      state: 'running',
      percent: 0,
      stage: 'planning',
      artifact: null,
      error: null,
      controller: new AbortController(),
      done: completion.promise
    }
    this.exportRunning = true
    this.activeExport = record
    this.exportJobs.set(jobId, record)
    void this.runExportJob(record, plan, actor, completion.resolve)
    return { jobId }
  }

  private async runExportJob(
    record: ExportJobRecord,
    plan: ExportPlanState,
    actor: 'user' | 'mcp' | undefined,
    finish: () => void
  ): Promise<void> {
    try {
      const artifact = await runExportPlan(
        plan,
        {
          workspace: this.workspace,
          clock: this.clock,
          locker: this.locker,
          ...(this.events === undefined ? {} : { events: this.events }),
          ...(this.options.hooks === undefined ? {} : { hooks: this.options.hooks })
        },
        {
          signal: record.controller.signal,
          report: (progress) => {
            record.percent = progress.percent
            record.stage = progress.stage
            this.options.onExportProgress?.(record.id, progress)
          }
        },
        normalizeActor(actor)
      )
      record.state = 'succeeded'
      record.percent = 100
      record.stage = 'done'
      record.artifact = artifact
    } catch (error) {
      if (isAppError(error) && error.code === AppErrorCode.Interrupted) {
        record.state = 'cancelled'
        record.stage = 'cancelled'
      } else {
        record.state = 'failed'
        record.stage = 'failed'
        record.error = messageOf(error)
      }
    } finally {
      this.exportRunning = false
      this.activeExport = null
      finish()
    }
  }

  /**
   * Runs one export to completion in the caller's context. MCP job bodies use
   * this so the artifact belongs to the MCP job row, not a second job registry.
   */
  async run(request: PlanRequest, options: ExportRunOptions = {}, actor?: 'user' | 'mcp'): Promise<ExportArtifact> {
    this.assertExportFree()
    this.exportRunning = true
    try {
      const plan = this.planForStart(request)
      return await runExportPlan(
        plan,
        {
          workspace: this.workspace,
          clock: this.clock,
          locker: this.locker,
          ...(this.events === undefined ? {} : { events: this.events }),
          ...(this.options.hooks === undefined ? {} : { hooks: this.options.hooks })
        },
        {
          ...(options.signal === undefined ? {} : { signal: options.signal }),
          report: (progress) => options.progress?.(progress.percent, progress.stage)
        },
        normalizeActor(actor)
      )
    } finally {
      this.exportRunning = false
    }
  }

  exportStatus(jobId: string): ExportJobStatus {
    const record = this.exportJobs.get(jobId)
    if (record === undefined) {
      throw appError(AppErrorCode.NotFound, `export job ${jobId} does not exist`, { job_id: jobId })
    }
    return {
      jobId: record.id,
      state: record.state,
      percent: record.percent,
      stage: record.stage,
      artifact: record.artifact,
      error: record.error
    }
  }

  cancelExport(jobId: string): boolean {
    const record = this.exportJobs.get(jobId)
    if (record === undefined || record.state !== 'running') return false
    record.controller.abort()
    return true
  }

  async waitExport(jobId: string): Promise<ExportJobStatus> {
    const record = this.exportJobs.get(jobId)
    if (record === undefined) {
      throw appError(AppErrorCode.NotFound, `export job ${jobId} does not exist`, { job_id: jobId })
    }
    await record.done
    return this.exportStatus(jobId)
  }

  /* --------------------------------------------------------------- import */

  /** Inspects a folder, zip or tar.gz bundle without writing (§4.11). */
  async inspect(sourcePath: string): Promise<ImportInspectPlan> {
    this.prunePlans(this.clock.now().getTime())
    const state = await inspectBundle(
      {
        workspace: this.workspace,
        clock: this.clock,
        ...(this.events === undefined ? {} : { events: this.events }),
        ...(this.reindex === undefined ? {} : { reindex: this.reindex })
      },
      sourcePath
    )
    this.importPlans.set(state.plan.planId, state)
    return state.plan
  }

  async startImport(planId: string, request: StartImportRequest = {}): Promise<JobRef> {
    const state = this.importPlans.get(planId)
    if (state === undefined) {
      throw appError(AppErrorCode.NotFound, `import plan ${planId} is unknown or expired`, { plan_id: planId })
    }
    if (this.importRunning) throw appError(AppErrorCode.Conflict, 'another import is running', { reason: 'busy' })
    const jobId = newId(this.clock)
    const completion = deferred<void>()
    const record: ImportJobRecord = {
      id: jobId,
      planId,
      projectId: state.plan.projectId,
      state: 'running',
      percent: 0,
      stage: 'planning',
      result: null,
      error: null,
      finishedAt: null,
      controller: new AbortController(),
      done: completion.promise
    }
    this.importRunning = true
    this.activeImport = record
    this.importJobs.set(jobId, record)
    void this.runImportJob(record, state, request, completion.resolve)
    return { jobId }
  }

  private async runImportJob(
    record: ImportJobRecord,
    state: ImportPlanState,
    request: StartImportRequest,
    finish: () => void
  ): Promise<void> {
    try {
      const result = await this.performImport(state, request, {
        signal: record.controller.signal,
        progress: (percent, stage) => {
          record.percent = percent
          record.stage = stage
          this.options.onImportProgress?.(record.id, percent, stage)
        }
      })
      record.state = 'succeeded'
      record.percent = 100
      record.stage = 'done'
      record.result = result
    } catch (error) {
      if (isAppError(error) && error.code === AppErrorCode.Interrupted) {
        record.state = 'cancelled'
        record.stage = 'cancelled'
      } else {
        record.state = 'failed'
        record.stage = 'failed'
        record.error = messageOf(error)
      }
    } finally {
      record.finishedAt = formatTimestamp(this.clock.now())
      this.importRunning = false
      this.activeImport = null
      finish()
    }
  }

  /** Restores a previously inspected bundle to completion. */
  async importBundle(
    planId: string,
    request: StartImportRequest = {},
    options: { signal?: AbortSignal; progress?: (percent: number, stage: string) => void } = {}
  ): Promise<ImportResult> {
    const state = this.importPlans.get(planId)
    if (state === undefined) {
      throw appError(AppErrorCode.NotFound, `import plan ${planId} is unknown or expired`, { plan_id: planId })
    }
    if (this.importRunning) {
      throw appError(AppErrorCode.Conflict, 'another import is running', { reason: 'busy' })
    }
    this.importRunning = true
    try {
      return await this.performImport(state, request, options)
    } finally {
      this.importRunning = false
    }
  }

  private async performImport(
    state: ImportPlanState,
    request: StartImportRequest,
    options: { signal?: AbortSignal; progress?: (percent: number, stage: string) => void }
  ): Promise<ImportResult> {
    const planId = state.plan.planId
    if (this.clock.now().getTime() > state.expiresAt) {
      this.importPlans.delete(planId)
      throw appError(AppErrorCode.NotFound, `import plan ${planId} expired`, {
        plan_id: planId,
        reason: 'expired'
      })
    }
    const result = await restoreBundle(
      {
        workspace: this.workspace,
        clock: this.clock,
        ...(this.events === undefined ? {} : { events: this.events }),
        ...(this.reindex === undefined ? {} : { reindex: this.reindex })
      },
      state,
      {
        ...(request.newIdentity === undefined ? {} : { newIdentity: request.newIdentity }),
        ...(request.name === undefined ? {} : { name: request.name }),
        actor: 'user'
      },
      {
        ...(options.signal === undefined ? {} : { signal: options.signal }),
        ...(options.progress === undefined ? {} : { report: options.progress })
      }
    )
    this.importPlans.delete(planId)
    return result
  }

  importStatus(jobId: string): ImportJobStatus {
    const record = this.importJobs.get(jobId)
    if (record === undefined) {
      throw appError(AppErrorCode.NotFound, `import job ${jobId} does not exist`, { job_id: jobId })
    }
    return {
      jobId: record.id,
      state: record.state,
      percent: record.percent,
      stage: record.stage,
      result: record.result,
      error: record.error,
      finishedAt: record.finishedAt
    }
  }

  cancelImport(jobId: string): boolean {
    const record = this.importJobs.get(jobId)
    if (record === undefined || record.state !== 'running') return false
    record.controller.abort()
    return true
  }

  async waitImport(jobId: string): Promise<ImportJobStatus> {
    const record = this.importJobs.get(jobId)
    if (record === undefined) {
      throw appError(AppErrorCode.NotFound, `import job ${jobId} does not exist`, { job_id: jobId })
    }
    await record.done
    return this.importStatus(jobId)
  }

  /** Aborts every running job; called on workspace switch/shutdown. */
  dispose(): void {
    this.activeExport?.controller.abort()
    this.activeImport?.controller.abort()
    this.plans.clear()
    this.importPlans.clear()
  }
}
