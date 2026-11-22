import { createHash } from 'node:crypto'
import { chmodSync, mkdirSync, readdirSync, renameSync, rmSync, statSync, utimesSync } from 'node:fs'
import { chmod, mkdir, open, readdir, rm, stat, utimes, type FileHandle } from 'node:fs/promises'
import * as path from 'node:path'

import { AppErrorCode, appError, type AppError } from '../errors'
import { newId } from '../ids'
import type { WorkspaceManager } from './index'
import { diskFree as defaultDiskFree, type DiskSpace } from './disk'
import { workspaceFootprint, workspaceInfo, type Footprint, type ProjectFootprint, type WorkspaceInfo } from './info'
import { isDirEmpty } from './marker'
import { probeRoot, type Probe } from './probe'

/** Verification strength. `fast` hashes only.db and.md. */
export type MigrateVerifyMode = 'fast' | 'full'

export interface MigrateOptions {
  /** Target root; must be empty enough or missing with a writable parent. */
  newRoot: string
  includeExports: boolean
  verify?: MigrateVerifyMode
  /** Must be true in this version; the old root is the user's backup. */
  keepSource: boolean
}

/** Result of planning; planning creates nothing on disk. */
export interface MigratePlan {
  id: string
  sourceRoot: string
  targetRoot: string
  stagingRoot: string
  files: number
  bytes: number
  options: Required<MigrateOptions>
}

export type MigrationPhase = 'probe' | 'lock' | 'copy' | 'verify' | 'commit' | 'reopen' | 'done'

export interface MigrateProgress {
  phase: MigrationPhase
  filesDone: number
  filesTotal: number
  bytesDone: number
  bytesTotal: number
  current?: string
  error?: unknown
}

export type MigrationErrorKind =
  | 'WORKSPACE_BUSY'
  | 'INVALID_PLAN'
  | 'TARGET_NOT_EMPTY'
  | 'INSUFFICIENT_SPACE'
  | 'COPY_FAILED'
  | 'VERIFY_FAILED'
  | 'CANCELLED'

const ERROR_CODES: Readonly<Record<MigrationErrorKind, AppErrorCode>> = {
  WORKSPACE_BUSY: AppErrorCode.Conflict,
  INVALID_PLAN: AppErrorCode.InvalidArgument,
  TARGET_NOT_EMPTY: AppErrorCode.Conflict,
  INSUFFICIENT_SPACE: AppErrorCode.LimitExceeded,
  COPY_FAILED: AppErrorCode.Internal,
  VERIFY_FAILED: AppErrorCode.Internal,
  CANCELLED: AppErrorCode.Conflict
}

/** Builds the typed migration failure; `details.kind` is the stable discriminator. */
export function migrationError(
  kind: MigrationErrorKind,
  message: string,
  options: { cause?: unknown; details?: Record<string, unknown> } = {}
): AppError {
  return appError(ERROR_CODES[kind], message, {
    cause: options.cause,
    details: { kind, ...options.details }
  })
}

/** Reads the stable kind from a migration failure, if it is one. */
export function migrationErrorKind(error: unknown): MigrationErrorKind | undefined {
  if (!(error instanceof Error) || !('details' in error)) return undefined
  const details = error.details
  if (typeof details !== 'object' || details === null || !('kind' in details)) return undefined
  const kind = details.kind
  return typeof kind === 'string' ? (kind as MigrationErrorKind) : undefined
}

export interface MigrationJob {
  readonly planId: string
  /** Settles when the job commits or fails; cancellation rejects it. */
  readonly done: Promise<void>
  /** Cancels during copy/verify; ignored once the commit begins. */
  cancel(): void
  /** Progress callback; returns an unsubscribe function. */
  subscribe(listener: (progress: MigrateProgress) => void): () => void
}

export interface WorkspaceServiceOptions {
  /** Testing seam for the free-space check. */
  diskFree?: (target: string) => DiskSpace
  /** Testing seam: runs after the copy and before verification. */
  hookAfterCopy?: (plan: MigratePlan) => void | Promise<void>
  /** Testing seam: runs after verification and before the commit rename. */
  hookBeforeCommit?: (plan: MigratePlan) => void | Promise<void>
  /** Called after a successful commit and reopen. */
  onMoved?: (oldRoot: string, newRoot: string) => void | Promise<void>
}

const COPY_BUFFER_SIZE = 1 << 20
const STAGING_PREFIX = '.researchos-migrate-'

/** `Promise.withResolvers` equivalent; the package targets the ES2022 lib. */
function deferred<T>(): {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
  reject: (reason?: unknown) => void
} {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

interface ManifestEntry {
  rel: string
  size: number
  hash: string
}

interface Manifest {
  entries: ManifestEntry[]
  files: number
  bytes: number
}

interface JobState extends MigrationJob {
  readonly plan: MigratePlan
  readonly controller: AbortController
  readonly listeners: Set<(progress: MigrateProgress) => void>
  readonly history: MigrateProgress[]
  finished: boolean
  last?: MigrateProgress
  resolve: () => void
  reject: (error: unknown) => void
}

interface FlowState {
  oldRoot: string
  moved: string[]
  routed: boolean
}

/**
 * Workspace service: probe, info, footprint and the staged
 * copy-migration with verification, cancellation and atomic commit.
 *
 * The migration never merges workspaces: the target must be empty-enough, the
 * copy is built under a staging directory inside the target, verified, and only
 * then are its top-level entries renamed into place, the config repointed and
 * the workspace reopened. The old root is kept as the user's backup.
 */
export class WorkspaceService {
  private readonly manager: WorkspaceManager
  private readonly options: WorkspaceServiceOptions
  private readonly plans = new Map<string, MigratePlan>()
  private readonly jobs = new Map<string, JobState>()
  private active: JobState | undefined

  constructor(manager: WorkspaceManager, options: WorkspaceServiceOptions = {}) {
    this.manager = manager
    this.options = options
  }

  /** Read-only verdict on a candidate root; touches nothing that survives. */
  probe(root: string): Probe {
    return probeRoot(this.manager.root, root)
  }

  info(): WorkspaceInfo {
    return workspaceInfo(this.manager)
  }

  footprint(): Promise<Footprint> {
    return workspaceFootprint(this.manager)
  }

  /**
   * Reads the source footprint, probes the target and registers one plan. One
   * plan may exist at a time; planning never creates the staging directory.
   */
  planMigration(options: MigrateOptions): MigratePlan {
    if (this.active !== undefined && !this.active.finished) throw migrationError('WORKSPACE_BUSY', 'workspace: another migration is running')
    if (this.plans.size > 0) {
      throw migrationError('WORKSPACE_BUSY', 'workspace: a migration plan already exists')
    }
    if (!this.manager.isOpen) {
      throw migrationError('INVALID_PLAN', 'workspace: workspace is closed')
    }

    const release = this.manager.workspaceLock.tryAcquire()
    if (release === undefined) {
      throw migrationError('WORKSPACE_BUSY', 'workspace: a migration holds the workspace lock')
    }
    release()

    const verify = options.verify ?? 'fast'
    if (verify !== 'fast' && verify !== 'full') {
      throw migrationError('INVALID_PLAN', `workspace: unknown verification mode ${String(verify)}`)
    }
    if (!options.keepSource) {
      throw migrationError('INVALID_PLAN', 'workspace: keepSource must be true in this version')
    }
    if (options.newRoot.trim() === '') {
      throw migrationError('INVALID_PLAN', 'workspace: empty target root')
    }

    const probe = this.probe(options.newRoot)
    switch (true) {
      case probe.sameAsCurrent:
        throw migrationError('INVALID_PLAN', `${probe.root} is the current workspace root`)
      case probe.nested:
        throw migrationError(
          'INVALID_PLAN',
          `${probe.root} and the current workspace root ${this.manager.root} nest`
        )
      case probe.exists && !probe.empty:
        throw migrationError('TARGET_NOT_EMPTY', `${probe.root} (existing files are never merged)`)
      case !probe.writable:
        throw migrationError('INVALID_PLAN', `${probe.root} is not writable: ${probe.problems.join('; ')}`)
      case probe.problems.length > 0:
        throw migrationError('INVALID_PLAN', `${probe.root}: ${probe.problems.join('; ')}`)
    }

    const sourceRoot = this.manager.root
    const stats = scanMigrationSource(sourceRoot, options.includeExports)
    const space = (this.options.diskFree ?? defaultDiskFree)(probe.root)
    if (!space.ok) {
      throw migrationError('INVALID_PLAN', `workspace: cannot determine free disk space for ${probe.root}`)
    }
    const required = stats.bytes + Math.ceil(stats.bytes / 10)
    if (space.free < required) {
      throw migrationError(
        'INSUFFICIENT_SPACE',
        `${probe.root} needs ${required} bytes, ${space.free} available`
      )
    }

    const id = newId(this.manager.clock)
    const plan: MigratePlan = {
      id,
      sourceRoot,
      targetRoot: probe.root,
      stagingRoot: path.join(probe.root, STAGING_PREFIX + id),
      files: stats.files,
      bytes: stats.bytes,
      options: { newRoot: probe.root, includeExports: options.includeExports, verify, keepSource: true }
    }
    if (this.plans.size > 0 || this.active !== undefined) {
      throw migrationError('WORKSPACE_BUSY', 'workspace: a migration plan already exists')
    }
    this.plans.set(plan.id, plan)
    return plan
  }

  /** Starts one plan; progress is delivered through the returned handle. */
  migrate(planId: string): MigrationJob {
    const plan = this.plans.get(planId)
    if (plan === undefined) {
      throw migrationError('INVALID_PLAN', `workspace: unknown migration plan ${JSON.stringify(planId)}`)
    }
    if (this.active !== undefined && !this.active.finished) {
      throw migrationError('WORKSPACE_BUSY', 'workspace: another migration is running')
    }
    if (!this.manager.isOpen) {
      throw migrationError('INVALID_PLAN', 'workspace: workspace is closed')
    }
    this.plans.delete(planId)

    const { promise: done, resolve: resolveDone, reject: rejectDone } = deferred<void>()
    // The handle may be consumed only through subscribe(); keep an ignored
    // handler so an unobserved failure does not surface as an unhandled
    // rejection. Callers still see the rejection on `done`.
    done.catch(() => {})
    const job: JobState = {
      planId,
      plan,
      controller: new AbortController(),
      listeners: new Set(),
      history: [],
      finished: false,
      done,
      resolve: resolveDone,
      reject: rejectDone,
      cancel: () => job.controller.abort(),
      subscribe: (listener) => {
        for (const progress of job.history) listener(progress)
        if (job.finished) return () => {}
        job.listeners.add(listener)
        return () => job.listeners.delete(listener)
      }
    }
    this.jobs.set(planId, job)
    this.active = job
    void this.run(job)
    return job
  }

  /** Idempotent: cancelling an unknown or finished job is not an error. */
  cancelMigration(planId: string): void {
    this.jobs.get(planId)?.cancel()
  }

  /** Waits for a job; an unknown plan id is an invalid plan. */
  wait(planId: string): Promise<void> {
    const job = this.jobs.get(planId)
    if (job === undefined) {
      return Promise.reject(
        migrationError('INVALID_PLAN', `workspace: unknown migration ${JSON.stringify(planId)}`)
      )
    }
    return job.done
  }

  private async run(job: JobState): Promise<void> {
    const release = this.manager.workspaceLock.tryAcquire()
    if (release === undefined) {
      this.finish(job, migrationError('WORKSPACE_BUSY', 'workspace: a migration holds the workspace lock'))
      return
    }
    try {
      await this.runLocked(job)
      this.finish(job)
    } catch (error) {
      this.finish(job, error)
    } finally {
      release()
      if (this.active === job) this.active = undefined
    }
  }

  private finish(job: JobState, error?: unknown): void {
    if (job.finished) return
    job.finished = true
    const last: MigrateProgress = job.last ?? {
      phase: 'done',
      filesDone: 0,
      filesTotal: job.plan.files,
      bytesDone: 0,
      bytesTotal: job.plan.bytes
    }
    const terminal: MigrateProgress = {
      ...last,
      phase: error === undefined ? 'done' : last.phase,
      error
    }
    this.publish(job, terminal)
    job.listeners.clear()
    if (error === undefined) job.resolve()
    else job.reject(error)
  }

  private publish(job: JobState, progress: MigrateProgress): void {
    job.last = progress
    job.history.push(progress)
    if (job.history.length > 256) job.history.shift()
    for (const listener of job.listeners) {
      try {
        listener(progress)
      } catch {
        // Ignore consumer failures.
      }
    }
  }

  private async runLocked(job: JobState): Promise<void> {
    const plan = job.plan
    const oldRoot = this.manager.root
    if (oldRoot !== plan.sourceRoot) {
      throw migrationError(
        'INVALID_PLAN',
        `workspace: source root changed from ${plan.sourceRoot} to ${oldRoot}`
      )
    }
    this.publish(job, {
      phase: 'probe',
      filesDone: 0,
      filesTotal: plan.files,
      bytesDone: 0,
      bytesTotal: plan.bytes
    })
    checkMigrationTarget(plan.targetRoot)

    this.publish(job, {
      phase: 'lock',
      filesDone: 0,
      filesTotal: plan.files,
      bytesDone: 0,
      bytesTotal: plan.bytes
    })
    try {
      this.manager.close()
    } catch (error) {
      try {
        if (!this.manager.isOpen) this.manager.reopen()
      } catch {
        // The caller still receives the close failure below.
      }
      throw migrationError('COPY_FAILED', 'workspace: migrate: close handles failed', { cause: error })
    }

    const state: FlowState = { oldRoot, moved: [], routed: false }
    try {
      mkdirSync(plan.targetRoot, { recursive: true, mode: 0o755 })
      mkdirSync(plan.stagingRoot, { mode: 0o755 })
      const manifest = await this.copyTree(job)
      if (this.options.hookAfterCopy !== undefined) await this.options.hookAfterCopy(plan)
      await this.verify(job, manifest)
      if (this.options.hookBeforeCommit !== undefined) await this.options.hookBeforeCommit(plan)
      throwIfCancelled(job)
      checkTargetCommit(plan.targetRoot, plan.stagingRoot)

      this.publish(job, {
        phase: 'commit',
        filesDone: plan.files,
        filesTotal: plan.files,
        bytesDone: plan.bytes,
        bytesTotal: plan.bytes
      })
      const entries = await readdir(plan.stagingRoot, { withFileTypes: true })
      entries.sort((left, right) => left.name.localeCompare(right.name))
      for (const entry of entries) {
        renameSync(path.join(plan.stagingRoot, entry.name), path.join(plan.targetRoot, entry.name))
        state.moved.push(entry.name)
      }
      mkdirSync(path.join(plan.targetRoot, 'exports'), { recursive: true, mode: 0o755 })

      state.routed = true
      this.publish(job, {
        phase: 'reopen',
        filesDone: plan.files,
        filesTotal: plan.files,
        bytesDone: plan.bytes,
        bytesTotal: plan.bytes
      })
      this.manager.repoint(plan.targetRoot)
      this.manager.reopen()
    } catch (error) {
      throw await this.rollback(job, state, error)
    }

    // Staging is empty after the move; its removal is best-effort. The commit
    // already succeeded, so a cleanup failure must not roll the workspace back.
    try {
      rmSync(plan.stagingRoot, { recursive: true, force: true })
    } catch {
      // Leftover empty directory only.
    }
    // A failing module callback is reported but never rolls back committed
    // data; the caller must refresh cached paths.
    if (this.options.onMoved !== undefined) {
      await this.options.onMoved(oldRoot, this.manager.root)
    }
    this.publish(job, {
      phase: 'done',
      filesDone: plan.files,
      filesTotal: plan.files,
      bytesDone: plan.bytes,
      bytesTotal: plan.bytes
    })
  }

  /**
   * Removes the staging tree and any committed entries, restores the old root
   * in config and reopens it. The target is never deleted wholesale: a partial
   * move is cleaned entry by entry.
   */
  private async rollback(job: JobState, state: FlowState, error: unknown): Promise<unknown> {
    const problems: unknown[] = [error]
    try {
      await rm(job.plan.stagingRoot, { recursive: true, force: true })
    } catch (cleanupError) {
      problems.push(cleanupError)
    }
    if (!state.routed) {
      for (const name of state.moved) {
        try {
          await rm(path.join(job.plan.targetRoot, name), { recursive: true, force: true })
        } catch (cleanupError) {
          problems.push(cleanupError)
        }
      }
    }
    if (state.routed || this.manager.root !== state.oldRoot) {
      try {
        this.manager.repoint(state.oldRoot)
      } catch (rollbackError) {
        problems.push(rollbackError)
        return problems.length === 1 ? error : new AggregateError(problems, 'workspace: migrate: rollback failed')
      }
    }
    if (!this.manager.isOpen) {
      try {
        this.manager.reopen()
      } catch (reopenError) {
        problems.push(reopenError)
      }
    }
    return problems.length === 1 ? error : new AggregateError(problems, 'workspace: migrate: failed and cleaned up')
  }

  private async copyTree(job: JobState): Promise<Manifest> {
    const plan = job.plan
    const manifest: Manifest = { entries: [], files: 0, bytes: 0 }
    const directories: Array<{ path: string; mode: number; atime: Date; mtime: Date }> = []

    const walk = async (relativeDirectory: string): Promise<void> => {
      throwIfCancelled(job)
      const sourceDirectory =
        relativeDirectory === ''
          ? plan.sourceRoot
          : path.join(plan.sourceRoot, ...relativeDirectory.split('/'))
      const entries = await readdir(sourceDirectory, { withFileTypes: true })
      entries.sort((left, right) => left.name.localeCompare(right.name))
      for (const entry of entries) {
        throwIfCancelled(job)
        const relative = relativeDirectory === '' ? entry.name : `${relativeDirectory}/${entry.name}`
        if (
          !plan.options.includeExports &&
          (relative === 'exports' || relative.startsWith('exports/'))
        ) {
          continue
        }
        if (entry.isSymbolicLink()) continue
        const sourcePath = path.join(plan.sourceRoot, ...relative.split('/'))
        const destinationPath = path.join(plan.stagingRoot, ...relative.split('/'))
        if (entry.isDirectory()) {
          await mkdir(destinationPath, { recursive: true, mode: 0o755 })
          const stats = await stat(sourcePath)
          directories.push({
            path: destinationPath,
            mode: stats.mode & 0o777,
            atime: stats.atime,
            mtime: stats.mtime
          })
          await walk(relative)
          continue
        }
        if (!entry.isFile()) continue
        const stats = await stat(sourcePath)
        const copied = await copyMigrationFile(
          job,
          sourcePath,
          destinationPath,
          stats.mode & 0o777,
          stats.atime,
          stats.mtime,
          needsHash(plan, relative)
        )
        manifest.entries.push({ rel: relative, size: copied.written, hash: copied.hash })
        manifest.files += 1
        manifest.bytes += copied.written
        this.publish(job, {
          phase: 'copy',
          filesDone: manifest.files,
          filesTotal: plan.files,
          bytesDone: manifest.bytes,
          bytesTotal: plan.bytes,
          current: relative
        })
      }
    }

    await walk('')
    for (const directory of [...directories].reverse()) {
      try {
        chmodSync(directory.path, directory.mode)
        utimesSync(directory.path, directory.atime, directory.mtime)
      } catch {
        // Directory metadata is best-effort on filesystems that refuse it.
      }
    }
    return manifest
  }

  private async verify(job: JobState, manifest: Manifest): Promise<void> {
    const plan = job.plan
    const byRel = new Map(manifest.entries.map((entry) => [entry.rel, entry]))
    const seen = new Set<string>()
    let files = 0
    let bytes = 0

    const walk = async (relativeDirectory: string): Promise<void> => {
      throwIfCancelled(job)
      const directory =
        relativeDirectory === ''
          ? plan.stagingRoot
          : path.join(plan.stagingRoot, ...relativeDirectory.split('/'))
      const entries = await readdir(directory, { withFileTypes: true })
      entries.sort((left, right) => left.name.localeCompare(right.name))
      for (const entry of entries) {
        throwIfCancelled(job)
        const relative = relativeDirectory === '' ? entry.name : `${relativeDirectory}/${entry.name}`
        const full = path.join(directory, entry.name)
        if (entry.isDirectory()) {
          await walk(relative)
          continue
        }
        if (!entry.isFile()) {
          throw migrationError('VERIFY_FAILED', `workspace: migrate: verify: unexpected entry ${relative}`)
        }
        const stats = await stat(full)
        const expected = byRel.get(relative)
        if (expected === undefined) {
          throw migrationError(
            'VERIFY_FAILED',
            `workspace: migrate: verify: ${relative} was not in the copy manifest`
          )
        }
        seen.add(relative)
        files += 1
        bytes += stats.size
        if (stats.size !== expected.size) {
          throw migrationError(
            'VERIFY_FAILED',
            `workspace: migrate: verify: ${relative} size ${stats.size}, want ${expected.size}`
          )
        }
        if (expected.hash !== '') {
          const digest = await hashMigrationFile(job, full)
          if (digest !== expected.hash) {
            throw migrationError('VERIFY_FAILED', `workspace: migrate: verify: ${relative} sha256 mismatch`)
          }
        }
        this.publish(job, {
          phase: 'verify',
          filesDone: files,
          filesTotal: plan.files,
          bytesDone: bytes,
          bytesTotal: plan.bytes,
          current: relative
        })
      }
    }

    await walk('')
    if (files !== manifest.files) {
      throw migrationError(
        'VERIFY_FAILED',
        `workspace: migrate: verify: ${files} files copied, manifest has ${manifest.files}`
      )
    }
    if (bytes !== manifest.bytes) {
      throw migrationError(
        'VERIFY_FAILED',
        `workspace: migrate: verify: ${bytes} bytes copied, manifest has ${manifest.bytes}`
      )
    }
    if (seen.size !== manifest.entries.length) {
      throw migrationError(
        'VERIFY_FAILED',
        `workspace: migrate: verify: ${seen.size} entries seen, manifest has ${manifest.entries.length}`
      )
    }
  }
}

function needsHash(plan: MigratePlan, relative: string): boolean {
  if (plan.options.verify === 'full') return true
  const extension = path.extname(relative).toLowerCase()
  return extension === '.db' || extension === '.md'
}

function throwIfCancelled(job: JobState): void {
  if (job.controller.signal.aborted) {
    throw migrationError('CANCELLED', 'workspace: migration cancelled')
  }
}

async function copyMigrationFile(
  job: JobState,
  source: string,
  destination: string,
  mode: number,
  atime: Date,
  mtime: Date,
  hashIt: boolean
): Promise<{ hash: string; written: number }> {
  const input = await open(source, 'r')
  let output: FileHandle | undefined
  try {
    output = await open(destination, 'w', mode)
    const hasher = hashIt ? createHash('sha256') : undefined
    const buffer = Buffer.allocUnsafe(COPY_BUFFER_SIZE)
    let written = 0
    for (;;) {
      throwIfCancelled(job)
      const { bytesRead } = await input.read(buffer, 0, buffer.length, null)
      if (bytesRead === 0) break
      const chunk = buffer.subarray(0, bytesRead)
      await output.write(chunk, 0, bytesRead)
      hasher?.update(chunk)
      written += bytesRead
    }
    await output.close()
    output = undefined
    await chmod(destination, mode)
    await utimes(destination, atime, mtime)
    return { hash: hasher === undefined ? '' : hasher.digest('hex'), written }
  } catch (error) {
    if (migrationErrorKind(error) === 'CANCELLED') throw error
    throw migrationError('COPY_FAILED', `workspace: migrate: copy ${source} failed`, { cause: error })
  } finally {
    await input.close()
    if (output !== undefined) await output.close()
  }
}

async function hashMigrationFile(job: JobState, file: string): Promise<string> {
  const input = await open(file, 'r')
  try {
    const hasher = createHash('sha256')
    const buffer = Buffer.allocUnsafe(COPY_BUFFER_SIZE)
    for (;;) {
      throwIfCancelled(job)
      const { bytesRead } = await input.read(buffer, 0, buffer.length, null)
      if (bytesRead === 0) break
      hasher.update(buffer.subarray(0, bytesRead))
    }
    return hasher.digest('hex')
  } finally {
    await input.close()
  }
}

/** Counts what the copy will transfer, applying the same skip rules. */
function scanMigrationSource(root: string, includeExports: boolean): { files: number; bytes: number } {
  let files = 0
  let bytes = 0
  const walk = (relativeDirectory: string): void => {
    const directory =
      relativeDirectory === '' ? root : path.join(root, ...relativeDirectory.split('/'))
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const relative = relativeDirectory === '' ? entry.name : `${relativeDirectory}/${entry.name}`
      if (!includeExports && (relative === 'exports' || relative.startsWith('exports/'))) continue
      if (entry.isSymbolicLink()) continue
      const full = path.join(root, ...relative.split('/'))
      if (entry.isDirectory()) {
        walk(relative)
        continue
      }
      if (!entry.isFile()) continue
      files += 1
      bytes += statSync(full).size
    }
  }
  walk('')
  return { files, bytes }
}

/** Rejects a target that is not missing or empty (OS droppings aside). */
function checkMigrationTarget(target: string): void {
  let stats
  try {
    stats = statSync(target)
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
      const parent = path.dirname(target)
      let parentStats
      try {
        parentStats = statSync(parent)
      } catch (parentError) {
        throw migrationError('INVALID_PLAN', `workspace: parent directory ${parent} does not exist`, {
          cause: parentError
        })
      }
      if (!parentStats.isDirectory()) {
        throw migrationError('INVALID_PLAN', `workspace: parent ${parent} is not a directory`)
      }
      return
    }
    throw migrationError('INVALID_PLAN', `workspace: cannot inspect ${target}`, { cause: error })
  }
  if (!stats.isDirectory()) {
    throw migrationError('INVALID_PLAN', `workspace: ${target} is not a directory`)
  }
  let empty: boolean
  try {
    empty = isDirEmpty(target)
  } catch (error) {
    throw migrationError('INVALID_PLAN', `workspace: cannot read ${target}`, { cause: error })
  }
  if (!empty) {
    throw migrationError('TARGET_NOT_EMPTY', `workspace: ${target} is not empty`)
  }
}

/** Rejects entries added beside the staging directory before the commit. */
function checkTargetCommit(target: string, staging: string): void {
  for (const entry of readdirSync(target)) {
    const full = path.join(target, entry)
    if (full === staging) continue
    if (entry === '.DS_Store' || entry === 'desktop.ini') continue
    throw migrationError('TARGET_NOT_EMPTY', `workspace: ${target} gained entries during the migration`)
  }
}

export type { Probe, WorkspaceInfo, Footprint, ProjectFootprint }