import { lstatSync, readdirSync } from 'node:fs'
import * as path from 'node:path'

import type {
  AppConfig,
  AppConfigPatch,
  JobRef,
  MigratePlan,
  MigratePlanItem,
  MigrateProgress,
  ProjectHandle,
  WorkspaceScanEntry,
  WorkspaceScanReport,
  WorkspaceStatus
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { ConfigStore, EMPTY_CONFIG, type AppConfig as CoreAppConfig } from '@researchos/core/config'
import { appError } from '@researchos/core/errors'
import { WorkspaceManager } from '@researchos/core/workspace/index'
import { dbFilesSize, dirSize, diskSpace } from '@researchos/core/workspace/disk'
import { readMarker } from '@researchos/core/workspace/marker'
import { WorkspaceService, migrationErrorKind } from '@researchos/core/workspace/migration'
import { workspaceValidators } from '@researchos/core/validation/workspace'

import type { CommandRouter } from '../router'

/**
 * Workspace lifecycle IPC (workspace.*, app.config.*). The Electron main process
 * is the only writer; this adapter holds the current `WorkspaceManager`,
 * validates every payload, persists the chosen root in the app config and
 * translates the workspace migration service into job-shaped commands.
 */

export interface WorkspaceHandlerDeps {
  /** The manager the composition root currently owns, or null when closed. */
  getManager: () => WorkspaceManager | null
  /** Installs (or clears) the current manager after create/open/close. */
  setManager: (manager: WorkspaceManager | null) => void
  config?: ConfigStore
  /** Defaults to `WorkspaceManager.init`. */
  createManager?: (root: string) => WorkspaceManager
  /** Defaults to `WorkspaceManager.open`. */
  openManager?: (root: string) => WorkspaceManager
  /** Fires after a successful migration commit + reopen (oldRoot, newRoot). */
  onMoved?: (oldRoot: string, newRoot: string) => void | Promise<void>
}

const SCAN_ENTRY_LIMIT = 5000
const SCAN_MAX_DEPTH = 8

interface MigrationTrack {
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  currentStep: string
  processedBytes: number
  totalBytes: number
  error: string | null
  startedAt: string | null
  finishedAt: string | null
}

function isoTimestamp(): string {
  return new Date().toISOString()
}

function closedStatus(): WorkspaceStatus {
  return {
    root: null,
    name: '',
    initialized: false,
    markerVersion: null,
    schemaVersion: null,
    open: false,
    projectCount: 0,
    warnings: []
  }
}

function statusOf(manager: WorkspaceManager | null): WorkspaceStatus {
  if (manager === null || !manager.isOpen) return closedStatus()
  const status = manager.status()
  const marker = readMarker(manager.root)
  return {
    root: status.root,
    name: path.basename(status.root),
    initialized: marker.state === 'present',
    markerVersion: marker.marker?.schema_version ?? null,
    schemaVersion: status.globalVersion,
    open: true,
    projectCount: status.projectCount,
    warnings: status.issues
  }
}

function toSharedConfig(config: CoreAppConfig): AppConfig {
  return {
    schemaVersion: config.schema_version,
    workspaceRoot: config.workspace_root === '' ? null : config.workspace_root,
    zoteroDataDir: config.zotero_data_dir === '' ? null : config.zotero_data_dir,
    language: config.language === 'zh' ? 'zh' : 'en'
  }
}

function applyPatch(config: CoreAppConfig, patch: AppConfigPatch): CoreAppConfig {
  return {
    schema_version: config.schema_version === 0 ? 1 : config.schema_version,
    workspace_root:
      patch.workspaceRoot === undefined ? config.workspace_root : (patch.workspaceRoot ?? ''),
    zotero_data_dir:
      patch.zoteroDataDir === undefined ? config.zotero_data_dir : (patch.zoteroDataDir ?? ''),
    language: patch.language === undefined ? config.language : patch.language
  }
}

function emptyConfig(): CoreAppConfig {
  return { ...EMPTY_CONFIG }
}

/** Nearest existing ancestor, so free space is known before the target exists. */
function existingAncestor(target: string): string {
  let current = target
  for (;;) {
    try {
      lstatSync(current)
      return current
    } catch {
      const parent = path.dirname(current)
      if (parent === current) return current
      current = parent
    }
  }
}

/** Walks one root into the flat entry list the wizard renders. */
function scanRoot(root: string): WorkspaceScanReport {
  const entries: WorkspaceScanEntry[] = []
  const warnings: string[] = []
  let totalBytes = 0

  const walk = (directory: string, depth: number): void => {
    if (depth > SCAN_MAX_DEPTH) {
      warnings.push(`scan stopped below ${path.relative(root, directory) || '.'} (depth limit)`)
      return
    }
    let names: string[]
    try {
      names = readdirSync(directory)
    } catch (error) {
      warnings.push(`cannot read ${directory}: ${error instanceof Error ? error.message : String(error)}`)
      return
    }
    for (const name of names) {
      if (entries.length >= SCAN_ENTRY_LIMIT) {
        warnings.push(`scan stopped after ${SCAN_ENTRY_LIMIT} entries`)
        return
      }
      const full = path.join(directory, name)
      const relative = path.relative(root, full).split(path.sep).join('/')
      try {
        const stats = lstatSync(full)
        if (stats.isSymbolicLink()) {
          entries.push({ relativePath: relative, kind: 'symlink', bytes: 0, modifiedAt: stats.mtime.toISOString() })
        } else if (stats.isDirectory()) {
          entries.push({ relativePath: relative, kind: 'directory', bytes: 0, modifiedAt: stats.mtime.toISOString() })
          walk(full, depth + 1)
        } else if (stats.isFile()) {
          entries.push({
            relativePath: relative,
            kind: 'file',
            bytes: stats.size,
            modifiedAt: stats.mtime.toISOString()
          })
          totalBytes += stats.size
        }
      } catch (error) {
        warnings.push(`cannot stat ${relative}: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
  }

  if (root === '') {
    throw appError(AppErrorCode.InvalidArgument, 'workspace.scan: no workspace root', {
      details: { field: 'root' }
    })
  }
  if (!path.isAbsolute(root)) {
    throw appError(AppErrorCode.InvalidArgument, `workspace.scan: ${root} is not absolute`, {
      details: { field: 'root' }
    })
  }
  walk(root, 0)
  const space = diskSpace(root)
  return {
    root,
    entries,
    totalBytes,
    freeBytes: space.ok ? space.free : null,
    dbBytes: dbFilesSize(path.join(root, 'global.db')),
    libraryBytes: dirSize(path.join(root, 'library')),
    warnings
  }
}

/** Describes what the staged copy will move, one item per workspace subtree. */
function planItems(sourceRoot: string): MigratePlanItem[] {
  const items: MigratePlanItem[] = []
  const sizes = (relative: string): number => {
    const target = path.join(sourceRoot, ...relative.split('/'))
    let stats: ReturnType<typeof lstatSync>
    try {
      stats = lstatSync(target)
    } catch {
      return 0
    }
    if (stats.isFile()) return stats.size
    let total = 0
    try {
      for (const name of readdirSync(target)) {
        total += sizes(relative === '' ? name : `${relative}/${name}`)
      }
    } catch {
      return 0
    }
    return total
  }

  const push = (kind: MigratePlanItem['kind'], relative: string): void => {
    const bytes = sizes(relative)
    if (bytes === 0) return
    items.push({ kind, relativePath: relative, bytes, action: 'create' })
  }

  push('copy-db', 'global.db')
  push('copy-notes', 'projects')
  push('copy-papers', 'library')
  push('rewrite-paths', 'exports')
  items.push({ kind: 'verify', relativePath: '.researchos', bytes: 0, action: 'create' })
  return items
}

export function registerWorkspaceHandlers(router: CommandRouter, deps: WorkspaceHandlerDeps): void {
  const configStore = (): ConfigStore => deps.config ?? new ConfigStore()
  const createManager = (root: string): WorkspaceManager =>
    deps.createManager === undefined ? WorkspaceManager.init(root) : deps.createManager(root)
  const openManager = (root: string): WorkspaceManager =>
    deps.openManager === undefined ? WorkspaceManager.open(root) : deps.openManager(root)
  const trackedOpen = new Set<string>()

  const persistRoot = (root: string): AppConfig => {
    const store = configStore()
    let current: CoreAppConfig
    try {
      current = store.load()
    } catch {
      current = emptyConfig()
    }
    const next: CoreAppConfig = {
      ...current,
      schema_version: current.schema_version === 0 ? 1 : current.schema_version,
      workspace_root: root
    }
    store.save(next)
    return toSharedConfig(next)
  }

  let plannedService: WorkspaceService | null = null
  const plannedIds = new Map<string, string>()
  const migrations = new Map<string, MigrationTrack>()

  const migrationService = (manager: WorkspaceManager): WorkspaceService => {
    if (plannedService === null) {
      plannedService = new WorkspaceService(manager, {
        ...(deps.onMoved === undefined ? {} : { onMoved: deps.onMoved })
      })
    }
    return plannedService
  }

  const ensurePlan = (manager: WorkspaceManager, targetRoot: string): { service: WorkspaceService; planId: string } => {
    const service = migrationService(manager)
    const existing = plannedIds.get(targetRoot)
    if (existing !== undefined) return { service, planId: existing }
    const plan = service.planMigration({
      newRoot: targetRoot,
      includeExports: true,
      keepSource: true,
      verify: 'fast'
    })
    plannedIds.set(targetRoot, plan.id)
    migrations.set(plan.id, {
      state: 'queued',
      currentStep: 'probe',
      processedBytes: 0,
      totalBytes: plan.bytes,
      error: null,
      startedAt: null,
      finishedAt: null
    })
    return { service, planId: plan.id }
  }

  const requireManager = (channel: string): WorkspaceManager => {
    const manager = deps.getManager()
    if (manager === null || !manager.isOpen) {
      throw appError(AppErrorCode.NotWorkspace, `${channel}: no workspace is open`)
    }
    return manager
  }

  /* ---------------------------------------------------------------- status */

  router.registerHandler('workspace.status', workspaceValidators['workspace.status'], () =>
    statusOf(deps.getManager())
  )

  router.registerHandler('workspace.create', workspaceValidators['workspace.create'], (request) => {
    const root = path.resolve(request.root)
    const previous = deps.getManager()
    const manager = createManager(root)
    if (previous !== null && previous !== manager && previous.isOpen) previous.close()
    deps.setManager(manager)
    trackedOpen.clear()
    persistRoot(manager.root)
    return statusOf(manager)
  })

  router.registerHandler('workspace.open', workspaceValidators['workspace.open'], (request) => {
    const root = path.resolve(request.root)
    const previous = deps.getManager()
    const manager = openManager(root)
    if (previous !== null && previous !== manager && previous.isOpen) previous.close()
    deps.setManager(manager)
    plannedService = null
    plannedIds.clear()
    trackedOpen.clear()
    persistRoot(manager.root)
    return statusOf(manager)
  })

  router.registerHandler('workspace.close', workspaceValidators['workspace.close'], () => {
    const manager = deps.getManager()
    if (manager !== null && manager.isOpen) manager.close()
    deps.setManager(null)
    trackedOpen.clear()
    plannedService = null
    plannedIds.clear()
    return closedStatus()
  })

  router.registerHandler('workspace.scan', workspaceValidators['workspace.scan'], (request) => {
    const root = request.root ?? deps.getManager()?.root
    if (root === undefined || root === '') {
      throw appError(AppErrorCode.NotWorkspace, 'workspace.scan: no workspace is open')
    }
    if (!path.isAbsolute(root)) {
      throw appError(AppErrorCode.InvalidArgument, `workspace.scan: ${root} is not absolute`, {
        details: { field: 'root' }
      })
    }
    return scanRoot(path.resolve(root))
  })

  /* ------------------------------------------------------------- migration */

  router.registerHandler('workspace.migrate.plan', workspaceValidators['workspace.migrate.plan'], (request) => {
    const manager = requireManager('workspace.migrate.plan')
    const targetRoot = path.resolve(request.targetRoot)
    const { planId } = ensurePlan(manager, targetRoot)
    const space = diskSpace(existingAncestor(targetRoot))
    const requiredBytes = migrations.get(planId)?.totalBytes ?? 0
    const items = planItems(manager.root)
    const required = requiredBytes + Math.ceil(requiredBytes / 10)
    const shared: MigratePlan = {
      sourceRoot: manager.root,
      targetRoot,
      canProceed: space.ok && space.free >= required,
      requiredBytes,
      freeBytes: space.ok ? space.free : 0,
      items,
      warnings: space.ok ? [] : [`cannot determine free space for ${targetRoot}`]
    }
    return shared
  })

  router.registerHandler('workspace.migrate.start', workspaceValidators['workspace.migrate.start'], (request) => {
    const manager = requireManager('workspace.migrate.start')
    const targetRoot = path.resolve(request.targetRoot)
    const { service, planId } = ensurePlan(manager, targetRoot)
    const track = migrations.get(planId)
    if (track === undefined) {
      throw appError(AppErrorCode.NotFound, `workspace.migrate.start: unknown plan ${planId}`, {
        details: { jobId: planId }
      })
    }
    const job = service.migrate(planId)
    track.state = 'running'
    track.startedAt = isoTimestamp()
    job.subscribe((progress) => {
      const current = migrations.get(planId)
      if (current === undefined) return
      current.currentStep = progress.current ?? progress.phase
      current.processedBytes = progress.bytesDone
      current.totalBytes = progress.bytesTotal
    })
    void job.done.then(
      () => {
        const current = migrations.get(planId)
        if (current === undefined) return
        current.state = 'succeeded'
        current.finishedAt = isoTimestamp()
        current.processedBytes = current.totalBytes
        current.currentStep = 'done'
        plannedIds.delete(targetRoot)
        const moved = deps.getManager()
        if (moved !== null && moved.isOpen) persistRoot(moved.root)
      },
      (error: unknown) => {
        const current = migrations.get(planId)
        if (current === undefined) return
        const kind = migrationErrorKind(error)
        current.state = kind === 'CANCELLED' ? 'cancelled' : 'failed'
        current.finishedAt = isoTimestamp()
        current.error = error instanceof Error ? error.message : String(error)
      }
    )
    return { jobId: planId } satisfies JobRef
  })

  router.registerHandler('workspace.migrate.progress', workspaceValidators['workspace.migrate.progress'], (request) => {
    const track = migrations.get(request.jobId)
    if (track === undefined) {
      throw appError(AppErrorCode.NotFound, `workspace.migrate.progress: unknown job ${request.jobId}`, {
        details: { jobId: request.jobId }
      })
    }
    return {
      jobId: request.jobId,
      state: track.state,
      currentStep: track.currentStep,
      processedBytes: track.processedBytes,
      totalBytes: track.totalBytes,
      error: track.error,
      startedAt: track.startedAt,
      finishedAt: track.finishedAt
    } satisfies MigrateProgress
  })

  router.registerHandler('workspace.migrate.cancel', workspaceValidators['workspace.migrate.cancel'], (request) => {
    const track = migrations.get(request.jobId)
    if (track === undefined) {
      throw appError(AppErrorCode.NotFound, `workspace.migrate.cancel: unknown job ${request.jobId}`, {
        details: { jobId: request.jobId }
      })
    }
    // Cancelling an unknown or finished job is a no-op in the core service.
    plannedService?.cancelMigration(request.jobId)
    if (track.state === 'queued') {
      track.state = 'cancelled'
      track.finishedAt = isoTimestamp()
    }
  })

  /* -------------------------------------------------------------- projects */

  const projectRows = (includeArchived: boolean): Array<{ id: string; name: string; status: string }> => {
    const manager = deps.getManager()
    if (manager === null || !manager.isOpen) return []
    const sql =
      'SELECT id, name, status FROM projects' +
      (includeArchived ? ' ORDER BY created_at DESC, id' : " WHERE status = 'active' ORDER BY created_at DESC, id")
    return manager.global.db.prepare(sql).all() as Array<{ id: string; name: string; status: string }>
  }

  router.registerHandler('workspace.projects.list', workspaceValidators['workspace.projects.list'], (request) => {
    const manager = deps.getManager()
    const handles: ProjectHandle[] = []
    for (const row of projectRows(request.includeArchived === true)) {
      if (manager === null) break
      handles.push({
        projectId: row.id,
        name: row.name,
        dbPath: manager.paths.abs(manager.paths.projectDb(row.id)),
        open: trackedOpen.has(row.id)
      })
    }
    return handles
  })

  router.registerHandler('workspace.projects.open', workspaceValidators['workspace.projects.open'], (request) => {
    const manager = requireManager('workspace.projects.open')
    manager.project(request.projectId)
    trackedOpen.add(request.projectId)
    const row = manager.global.db
      .prepare('SELECT id, name FROM projects WHERE id = ?')
      .get(request.projectId) as { id: string; name: string } | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `项目不存在：${request.projectId}`, {
        details: { projectId: request.projectId }
      })
    }
    return {
      projectId: row.id,
      name: row.name,
      dbPath: manager.paths.abs(manager.paths.projectDb(row.id)),
      open: true
    } satisfies ProjectHandle
  })

  router.registerHandler('workspace.projects.close', workspaceValidators['workspace.projects.close'], (request) => {
    const manager = deps.getManager()
    manager?.closeProject(request.projectId)
    trackedOpen.delete(request.projectId)
  })

  /* ------------------------------------------------------- application config */

  router.registerHandler('app.config.get', workspaceValidators['app.config.get'], () => {
    const store = configStore()
    let config: CoreAppConfig
    try {
      config = store.load()
    } catch (error) {
      // A quarantined corrupt config still answers with defaults; the caller can
      // repair it through app.config.update.
      config = emptyConfig()
      void error
    }
    return toSharedConfig(config)
  })

  router.registerHandler('app.config.update', workspaceValidators['app.config.update'], (request) => {
    const store = configStore()
    let current: CoreAppConfig
    try {
      current = store.load()
    } catch {
      current = emptyConfig()
    }
    const next = applyPatch(current, request.patch as AppConfigPatch)
    store.save(next)
    return toSharedConfig(next)
  })

}