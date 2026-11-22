import { mkdirSync, statSync } from 'node:fs'
import * as path from 'node:path'

import { type Clock, SystemClock } from '../clock'
import { ConfigStore, type AppConfig } from '../config'
import { AppError, AppErrorCode, appError } from '../errors'
import { isValidId } from '../ids'
import type { Logger } from '../logger'
import { Paths } from '../paths'
import { migrateDatabase, type MigrationResult, inspectDatabase } from '../db/migrate'
import { openDb } from '../db/open'
import { GlobalHandle, ProjectHandle } from './handle'
import { WorkspaceLock } from './lock'
import { isDirEmpty, MARKER_NAME, readMarker, writeMarker, type MarkerState } from './marker'
import { scanWorkspace, type WorkspaceScan } from './scan'

/** Directories every workspace owns. */
const WORKSPACE_DIRS = ['library/papers', 'projects', 'exports'] as const

export interface WorkspaceOptions {
  clock?: Clock
  logger?: Logger
  /** Disables migration backups. Tests only. */
  skipBackup?: boolean
  /** Overrides the migration backup root. Defaults to the OS cache directory. */
  backupRoot?: string
  /** Application config used by `repoint`. Defaults to the OS config file. */
  config?: ConfigStore
  /** Project handle cache size. */
  maxOpenProjects?: number
}

export interface WorkspaceStatus {
  root: string
  marker: MarkerState
  globalVersion: number
  globalTarget: number
  projectCount: number
  archivedCount: number
  openProjects: number
  vectorAvailable: boolean
  fts5TrigramAvailable: boolean
  issues: string[]
}

export interface WorkspaceMigrateReport {
  global: MigrationResult
  projects: Array<{ id: string; migrations: MigrationResult }>
  issues: string[]
}

function requireDirectory(target: string, notWorkspace: boolean): void {
  let isDirectory = false
  try {
    isDirectory = statSync(target).isDirectory()
  } catch (error) {
    if (notWorkspace) {
      throw appError(AppErrorCode.NotWorkspace, `${target} does not exist`, { cause: error })
    }
    throw new Error(`workspace: ${target} does not exist`)
  }
  if (!isDirectory) {
    throw appError(AppErrorCode.NotWorkspace, `${target} is not a directory`)
  }
}

function fileExists(target: string): boolean {
  try {
    return !statSync(target).isDirectory()
  } catch {
    return false
  }
}

function isSameOrNested(left: string, right: string): boolean {
  const contains = (outer: string, inner: string): boolean => {
    const relative = path.relative(outer, inner)
    return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))
  }
  return contains(left, right) || contains(right, left)
}

/**
 * Owns the workspace lifecycle: initialization, opening, validation, project
 * handles (LRU max 8), scanning, whole-workspace database migration, repointing
 * and closing. It is the only writer of global.db and the project databases.
 */
export class WorkspaceManager {
  private readonly options: WorkspaceOptions
  private readonly clockSource: Clock
  private rootPath: string
  private pathResolver: Paths
  private globalHandle: GlobalHandle | undefined
  private readonly projects = new Map<string, ProjectHandle>()
  vectorAvailable: boolean
  fts5TrigramAvailable: boolean

  private constructor(
    root: string,
    paths: Paths,
    global: GlobalHandle,
    vectorAvailable: boolean,
    fts5TrigramAvailable: boolean,
    options: WorkspaceOptions
  ) {
    this.options = options
    this.clockSource = options.clock ?? new SystemClock()
    this.rootPath = root
    this.pathResolver = paths
    this.globalHandle = global
    this.vectorAvailable = vectorAvailable
    this.fts5TrigramAvailable = fts5TrigramAvailable
  }

  /**
   * Creates the workspace layout and global.db under `root`. `root` may be
   * missing or empty; `.DS_Store` and `desktop.ini` are ignored. Repeating init
   * on a healthy workspace preserves its data.
   */
  static init(root: string, options: WorkspaceOptions = {}): WorkspaceManager {
    const absolute = path.resolve(root)
    mkdirSync(absolute, { recursive: true, mode: 0o755 })
    requireDirectory(absolute, true)

    const marker = readMarker(absolute)
    const globalPath = path.join(absolute, 'global.db')
    const hasGlobal = fileExists(globalPath)
    switch (marker.state) {
      case 'invalid':
        throw appError(AppErrorCode.WorkspaceCorrupt, `${absolute} has an invalid marker file`)
      case 'present':
        if (!hasGlobal) {
          throw appError(AppErrorCode.WorkspaceCorrupt, `marker exists but global.db is missing in ${absolute}`)
        }
        break
      case 'missing':
        if (hasGlobal) {
          throw appError(AppErrorCode.WorkspaceCorrupt, `global.db exists but the marker is missing in ${absolute}`)
        }
        if (!isDirEmpty(absolute)) {
          throw appError(AppErrorCode.DirectoryNotEmpty, `${absolute} already contains foreign files`)
        }
        break
    }

    for (const relative of WORKSPACE_DIRS) {
      mkdirSync(path.join(absolute, ...relative.split('/')), { recursive: true, mode: 0o755 })
    }
    if (marker.state === 'missing') writeMarker(absolute, options.clock ?? new SystemClock())

    const paths = new Paths(absolute)
    const opened = openDb(globalPath, 'global', {
      clock: options.clock,
      logger: options.logger,
      skipBackup: options.skipBackup,
      backupRoot: options.backupRoot
    })
    return new WorkspaceManager(
      paths.root,
      paths,
      new GlobalHandle(opened.db),
      opened.vectorAvailable,
      opened.fts5TrigramAvailable,
      options
    )
  }

  /**
   * Opens an existing workspace. A directory without a marker/global.db is
   * `NOT_WORKSPACE`; an inconsistent pair is `WORKSPACE_CORRUPT`.
   */
  static open(root: string, options: WorkspaceOptions = {}): WorkspaceManager {
    const absolute = path.resolve(root)
    requireDirectory(absolute, true)
    const marker = readMarker(absolute)
    const globalPath = path.join(absolute, 'global.db')
    const hasGlobal = fileExists(globalPath)

    switch (marker.state) {
      case 'invalid':
        throw appError(AppErrorCode.WorkspaceCorrupt, `${absolute} has an invalid marker file`)
      case 'missing':
        if (hasGlobal) {
          throw appError(AppErrorCode.WorkspaceCorrupt, `global.db exists but the marker is missing in ${absolute}`)
        }
        throw appError(AppErrorCode.NotWorkspace, `${absolute} has no marker and no global.db`)
      case 'present':
        if (!hasGlobal) {
          throw appError(AppErrorCode.WorkspaceCorrupt, `marker exists but global.db is missing in ${absolute}`)
        }
        break
    }

    const paths = new Paths(absolute)
    const opened = openDb(globalPath, 'global', {
      clock: options.clock,
      logger: options.logger,
      skipBackup: options.skipBackup,
      backupRoot: options.backupRoot
    })
    return new WorkspaceManager(
      paths.root,
      paths,
      new GlobalHandle(opened.db),
      opened.vectorAvailable,
      opened.fts5TrigramAvailable,
      options
    )
  }

  get root(): string {
    return this.rootPath
  }

  get paths(): Paths {
    return this.pathResolver
  }

  /** Injectable clock used for IDs and timestamps by workspace services. */
  get clock(): Clock {
    return this.clockSource
  }

  /** True while the global database handle is open. */
  get isOpen(): boolean {
    return this.globalHandle !== undefined
  }

  /** Exclusive workspace lock shared by migration and other wide operations. */
  readonly workspaceLock = new WorkspaceLock()

  get global(): GlobalHandle {
    if (this.globalHandle === undefined) throw new Error('workspace: workspace is closed')
    return this.globalHandle
  }

  /** The config store for read-only reporting; never written without one. */
  get config(): ConfigStore {
    return this.options.config ?? new ConfigStore()
  }

  get openProjectCount(): number {
    return this.projects.size
  }

  /**
   * Opens (and caches) the project database, migrating it on first open. The
   * cache holds at most `maxOpenProjects` (default 8) handles and evicts the
   * least recently used one, closing its connection.
   */
  project(projectId: string): ProjectHandle {
    if (!isValidId(projectId)) {
      throw appError(AppErrorCode.BadEntityId, `workspace: invalid project id ${JSON.stringify(projectId)}`)
    }
    const cached = this.projects.get(projectId)
    if (cached !== undefined) {
      this.projects.delete(projectId)
      this.projects.set(projectId, cached)
      return cached
    }

    const directory = this.pathResolver.abs(this.pathResolver.projectDir(projectId))
    requireDirectory(directory, false)
    const opened = openDb(path.join(directory, 'project.db'), 'project', {
      clock: this.options.clock,
      logger: this.options.logger,
      skipBackup: this.options.skipBackup,
      backupRoot: this.options.backupRoot
    })
    const handle = new ProjectHandle(projectId, opened.db, this.pathResolver)
    this.projects.set(projectId, handle)
    this.evictOverflow()
    return handle
  }

  /** Closes and evicts one project handle. A no-op when it is not cached. */
  closeProject(projectId: string): void {
    const handle = this.projects.get(projectId)
    if (handle === undefined) return
    this.projects.delete(projectId)
    handle.close()
  }

  /**
   * Creates `projects/<id>/{notes,attachments}` and a migrated project.db. The
   * `projects` registration row is the project service's job.
   */
  createProjectData(projectId: string): void {
    if (!isValidId(projectId)) {
      throw appError(AppErrorCode.BadEntityId, `workspace: invalid project id ${JSON.stringify(projectId)}`)
    }
    const base = this.pathResolver.abs(this.pathResolver.projectDir(projectId))
    for (const sub of ['notes', 'attachments']) {
      mkdirSync(path.join(base, sub), { recursive: true, mode: 0o755 })
    }
    this.project(projectId)
  }

  status(): WorkspaceStatus {
    const global = this.global
    const inspection = inspectDatabase(path.join(this.rootPath, 'global.db'), 'global', this.options.logger)
    const counts = global.db
      .prepare('SELECT status, COUNT(*) AS n FROM projects GROUP BY status')
      .all() as Array<{ status: string; n: number }>
    let projectCount = 0
    let archivedCount = 0
    for (const row of counts) {
      projectCount += row.n
      if (row.status === 'archived') archivedCount += row.n
    }
    return {
      root: this.rootPath,
      marker: readMarker(this.rootPath).state,
      globalVersion: inspection.version,
      globalTarget: inspection.target,
      projectCount,
      archivedCount,
      openProjects: this.projects.size,
      vectorAvailable: this.vectorAvailable,
      fts5TrigramAvailable: this.fts5TrigramAvailable,
      issues: inspection.issues
    }
  }

  scan(): WorkspaceScan {
    return scanWorkspace(this.rootPath, this.global.db)
  }

  /**
   * Migrates global.db and every registered project database, collecting
   * per-project failures instead of aborting the whole run.
   */
  migrate(): WorkspaceMigrateReport {
    const globalPath = path.join(this.rootPath, 'global.db')
    const global = migrateDatabase(this.global.db, 'global', globalPath, {
      clock: this.options.clock,
      logger: this.options.logger,
      skipBackup: this.options.skipBackup,
      backupRoot: this.options.backupRoot
    })
    const report: WorkspaceMigrateReport = { global, projects: [], issues: [] }
    const ids = this.global.db.prepare('SELECT id FROM projects ORDER BY id').pluck().all() as string[]
    for (const rawId of ids) {
      const id = String(rawId)
      const wasOpen = this.projects.has(id)
      try {
        const handle = this.project(id)
        const migrations = migrateDatabase(handle.db, 'project', path.join(this.rootPath, 'projects', id, 'project.db'), {
          clock: this.options.clock,
          logger: this.options.logger,
          skipBackup: this.options.skipBackup,
          backupRoot: this.options.backupRoot
        })
        report.projects.push({ id, migrations })
        if (!wasOpen) this.closeProject(id)
      } catch (error) {
        report.issues.push(`${id}: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
    return report
  }

  /**
   * Switches the workspace root after the user moved the whole tree: validates
   * the new layout, updates the application config and invalidates the old
   * handles. The caller opens the new root afterwards.
   */
  repoint(newRoot: string): void {
    const absolute = path.resolve(newRoot)
    requireDirectory(absolute, true)
    const marker = readMarker(absolute)
    if (marker.state === 'missing') {
      throw appError(AppErrorCode.NotWorkspace, `${absolute} has no marker`)
    }
    if (marker.state === 'invalid') {
      throw appError(AppErrorCode.WorkspaceCorrupt, `${absolute} has an invalid marker`)
    }
    if (!fileExists(path.join(absolute, 'global.db'))) {
      throw appError(AppErrorCode.WorkspaceCorrupt, `marker exists but global.db is missing in ${absolute}`)
    }
    const paths = new Paths(absolute)
    // Only the composition root owns the OS config. Without an explicit
    // store the manager must not touch the real %APPDATA% file — tests and
    // helper callers pass none, and silently rewriting the user's last
    // workspace was the bug this guard removes.
    if (this.options.config !== undefined) {
      const config = this.options.config
      const configDir = path.dirname(config.path())
      if (isSameOrNested(paths.root, configDir)) {
        throw appError(
          AppErrorCode.WorkspaceCorrupt,
          `config directory ${configDir} and workspace ${paths.root} must not nest`
        )
      }

      let current: AppConfig = { schema_version: 0, workspace_root: '', zotero_data_dir: '', language: '' }
      try {
        current = config.load()
      } catch (error) {
        if (!(error instanceof AppError) || error.code !== AppErrorCode.ConfigCorrupt) throw error
      }
      config.save({
        ...current,
        schema_version: current.schema_version === 0 ? 1 : current.schema_version,
        workspace_root: paths.root
      })
    }

    for (const id of [...this.projects.keys()]) this.closeProject(id)
    this.globalHandle?.close()
    this.globalHandle = undefined
    this.rootPath = paths.root
    this.pathResolver = paths
  }

  /** Reopens global.db after `close`/`repoint`. Project handles reopen lazily. */
  reopen(): void {
    if (this.globalHandle !== undefined) throw new Error('workspace: workspace is already open')
    const absolute = this.rootPath
    requireDirectory(absolute, true)
    const marker = readMarker(absolute)
    if (marker.state === 'missing') throw appError(AppErrorCode.NotWorkspace, `${absolute} has no marker`)
    if (marker.state === 'invalid') {
      throw appError(AppErrorCode.WorkspaceCorrupt, `${absolute} has an invalid marker`)
    }
    if (!fileExists(path.join(absolute, 'global.db'))) {
      throw appError(AppErrorCode.WorkspaceCorrupt, `marker exists but global.db is missing in ${absolute}`)
    }
    this.pathResolver = new Paths(absolute)
    this.rootPath = this.pathResolver.root
    const opened = openDb(path.join(this.rootPath, 'global.db'), 'global', {
      clock: this.options.clock,
      logger: this.options.logger,
      skipBackup: this.options.skipBackup,
      backupRoot: this.options.backupRoot
    })
    this.globalHandle = new GlobalHandle(opened.db)
    this.vectorAvailable = opened.vectorAvailable
    this.fts5TrigramAvailable = opened.fts5TrigramAvailable
  }

  /** Closes every open database handle. Safe to call twice. */
  close(): void {
    const failures: unknown[] = []
    for (const id of [...this.projects.keys()]) {
      try {
        this.closeProject(id)
      } catch (error) {
        failures.push(error)
      }
    }
    if (this.globalHandle !== undefined) {
      try {
        this.globalHandle.close()
      } catch (error) {
        failures.push(error)
      }
      this.globalHandle = undefined
    }
    if (failures.length > 0) throw new AggregateError(failures, 'workspace: close failed')
  }

  private evictOverflow(): void {
    const limit = this.options.maxOpenProjects ?? 8
    while (this.projects.size > limit) {
      const oldest = this.projects.keys().next().value
      if (oldest === undefined) return
      this.closeProject(oldest)
    }
  }
}

export { MARKER_NAME, scanWorkspace }
export type { WorkspaceScan }
export type { MigrationResult }
export { WorkspaceService, migrationError, migrationErrorKind } from './migration'
export { WorkspaceLock } from './lock'
export type {
  Footprint,
  MigrationErrorKind,
  MigrationJob,
  MigrationPhase,
  MigrateOptions,
  MigratePlan,
  MigrateProgress,
  MigrateVerifyMode,
  Probe,
  ProjectFootprint,
  WorkspaceInfo,
  WorkspaceServiceOptions
} from './migration'