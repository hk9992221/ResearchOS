/**
 * In-process job store for IPC-driven literature operations (import, metadata
 * refresh). MCP jobs use `mcp_job` and the host's `Jobs`; this store only backs
 * the desktop `{ jobId }` contracts, which never cross the process boundary.
 */
import type { ImportReport } from '@researchos/shared'

import type { Clock } from '../../clock'
import { SystemClock } from '../../clock'
import { appError, AppErrorCode } from '../../errors'
import { newId } from '../../ids'

interface ImportJob {
  id: string
  kind: 'import'
  state: ImportReport['state']
  progress: number
  report: ImportReport | null
  error: string | null
  promise: Promise<void>
}

interface GenericJob {
  id: string
  kind: 'refresh_metadata'
  state: ImportReport['state']
  progress: number
  result: unknown
  error: string | null
  promise: Promise<void>
}

type Job = ImportJob | GenericJob

const EMPTY_REPORT: ImportReport = {
  state: 'queued',
  progress: 0,
  outcomes: [],
  imported: 0,
  reused: 0,
  skipped: 0,
  failed: 0,
  warnings: [],
  error: null
}

export class LiteratureJobStore {
  private readonly clock: Clock
  private readonly jobs = new Map<string, Job>()

  constructor(clock: Clock = new SystemClock()) {
    this.clock = clock
  }

  startImport(run: (progress: (percent: number, stage: string) => void) => Promise<ImportReport>): string {
    const id = newId(this.clock)
    // The package lib target predates Promise.withResolvers; the executor form
    // is the only way to keep the deferred handy for `wait()`.
    let resolveDone: () => void = () => undefined
    const promise = new Promise<void>((resolve) => {
      resolveDone = resolve
    })
    const job: ImportJob = {
      id,
      kind: 'import',
      state: 'queued',
      progress: 0,
      report: { ...EMPTY_REPORT },
      error: null,
      promise
    }
    this.jobs.set(id, job)
    queueMicrotask(() => {
      job.state = 'running'
      void run((percent, stage) => {
        job.progress = Math.max(0, Math.min(100, Math.round(percent)))
        if (job.report !== null) {
          job.report.progress = job.progress
          job.report.state = 'running'
          job.report.warnings = [stage]
        }
      })
        .then((report) => {
          job.report = { ...report, state: 'succeeded', progress: 100 }
          job.state = 'succeeded'
          job.progress = 100
        })
        .catch((error: unknown) => {
          job.state = 'failed'
          job.error = error instanceof Error ? error.message : String(error)
          job.report = {
            ...EMPTY_REPORT,
            state: 'failed',
            error: job.error
          }
        })
        .finally(resolveDone)
    })
    return id
  }

  startRun<T>(_kind: 'refresh_metadata', run: () => Promise<T>): string {
    const id = newId(this.clock)
    let resolveDone: () => void = () => undefined
    const promise = new Promise<void>((resolve) => {
      resolveDone = resolve
    })
    const job: GenericJob = {
      id,
      kind: 'refresh_metadata',
      state: 'queued',
      progress: 0,
      result: null,
      error: null,
      promise
    }
    this.jobs.set(id, job)
    queueMicrotask(() => {
      job.state = 'running'
      void run()
        .then((result) => {
          job.result = result
          job.state = 'succeeded'
          job.progress = 100
        })
        .catch((error: unknown) => {
          job.state = 'failed'
          job.error = error instanceof Error ? error.message : String(error)
        })
        .finally(resolveDone)
    })
    return id
  }

  report(id: string): ImportReport {
    const job = this.jobs.get(id)
    if (job === undefined || job.kind !== 'import') {
      throw appError(AppErrorCode.NotFound, `导入作业 ${id} 不存在`, { details: { job_id: id } })
    }
    return job.report ?? { ...EMPTY_REPORT, state: job.state, progress: job.progress }
  }

  state(id: string): { state: ImportReport['state']; progress: number; error: string | null } {
    const job = this.jobs.get(id)
    if (job === undefined) {
      throw appError(AppErrorCode.NotFound, `作业 ${id} 不存在`, { details: { job_id: id } })
    }
    return { state: job.state, progress: job.progress, error: job.error }
  }

  async wait(id: string): Promise<void> {
    const job = this.jobs.get(id)
    if (job === undefined) {
      throw appError(AppErrorCode.NotFound, `作业 ${id} 不存在`, { details: { job_id: id } })
    }
    await job.promise
  }
}