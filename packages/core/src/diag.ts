/**
 * Read-only self check: schema versions, integrity, foreign
 * keys, note/file state, broken external references and the application
 * configuration. It never repairs anything; every problem becomes a warning or
 * an `integrityError`.
 */
import { existsSync, readdirSync, statSync } from 'node:fs'
import * as path from 'node:path'

import { inspectDatabase } from './db/migrate'
import { type DbKind, openReadOnlyDb } from './db/open'
import { AppErrorCode, isAppError } from './errors'
import type { Logger } from './logger'
import type { WorkspaceManager } from './workspace'

export interface DiagOptions {
  /** Also checks that every active note body file exists. */
  includeFileScan?: boolean
  /** Limits the report; empty means every registered project. */
  projectIds?: readonly string[]
  logger?: Logger
}

export interface DbReport {
  path: string
  schemaVersion: number
  targetVersion: number
  integrityOk: boolean
  integrityError?: string
  foreignKeyViolations: string[]
  sizeBytes: number
  walBytes: number
}

export interface ProjectReport {
  projectId: string
  db?: DbReport
  dirExists: boolean
  notesTotal: number
  notesMissing: number
  brokenRefs: number
}

export interface ConfigReport {
  path: string
  exists: boolean
  /** Most recent quarantined copy when the config is corrupt. */
  corruptBackup?: string
  workspaceRoot: string
  rootReachable: boolean
  zoteroDataDir: string
}

export interface DiagReport {
  generatedAt: Date
  global: DbReport
  projects: ProjectReport[]
  config: ConfigReport
  warnings: string[]
}

/** Runs the read-only self check over the workspace. */
export function runDiagnostics(workspace: WorkspaceManager, options: DiagOptions = {}): DiagReport {
  const warnings: string[] = []
  const globalPath = path.join(workspace.root, 'global.db')
  const globalInspection = inspectDatabaseFile(globalPath, 'global', options.logger)
  if (globalInspection.issues.length > 0) {
    warnings.push(...globalInspection.issues.map((issue) => `global: ${issue}`))
  }

  const ids =
    options.projectIds !== undefined && options.projectIds.length > 0
      ? [...new Set(options.projectIds)].sort()
      : registeredProjectIds(workspace)

  const projects: ProjectReport[] = []
  for (const projectId of ids) {
    const { report, issues } = inspectProject(workspace, projectId, options)
    projects.push(report)
    warnings.push(...issues)
  }

  const config = inspectConfig(workspace, warnings)
  warnings.sort()
  return {
    generatedAt: workspace.clock.now(),
    global: globalInspection.report,
    projects,
    config,
    warnings
  }
}

function registeredProjectIds(workspace: WorkspaceManager): string[] {
  return workspace.global.db
    .prepare('SELECT id FROM projects ORDER BY id')
    .pluck()
    .all() as string[]
}

function inspectProject(
  workspace: WorkspaceManager,
  projectId: string,
  options: DiagOptions
): { report: ProjectReport; issues: string[] } {
  const report: ProjectReport = {
    projectId,
    dirExists: false,
    notesTotal: 0,
    notesMissing: 0,
    brokenRefs: 0
  }
  const issues: string[] = []
  const projectDir = path.join(workspace.root, 'projects', projectId)
  report.dirExists = existsSync(projectDir) && statSync(projectDir).isDirectory()

  const dbPath = path.join(projectDir, 'project.db')
  if (!existsSync(dbPath)) return { report, issues }
  const inspection = inspectDatabaseFile(dbPath, 'project', options.logger)
  report.db = inspection.report
  issues.push(...inspection.issues.map((issue) => `${projectId}: ${issue}`))
  if (!inspection.report.integrityOk) return { report, issues }

  let opened
  try {
    opened = openReadOnlyDb(dbPath, { loadVector: false, verifyFts: false, logger: options.logger })
  } catch (error) {
    issues.push(`${projectId}: ${messageOf(error)}`)
    return { report, issues }
  }
  const db = opened.db
  try {
    report.notesTotal = count(db, 'SELECT count(*) AS n FROM notes WHERE deleted_at IS NULL')
    report.notesMissing = count(
      db,
      `SELECT count(*) AS n FROM notes WHERE deleted_at IS NULL AND file_state = 'missing'`
    )
    if (options.includeFileScan === true) {
      const rows = db
        .prepare(`SELECT file_path FROM notes WHERE deleted_at IS NULL AND file_state <> 'missing'`)
        .pluck()
        .all() as string[]
      for (const relative of rows) {
        if (!fileExists(workspace, relative)) report.notesMissing += 1
      }
    }
    report.brokenRefs = count(db, `SELECT count(*) AS n FROM external_refs WHERE status = 'broken'`)
  } catch (error) {
    issues.push(`${projectId}: ${messageOf(error)}`)
  } finally {
    db.close()
  }
  return { report, issues }
}

function count(db: { prepare(sql: string): { get(): unknown } }, sql: string): number {
  const row = db.prepare(sql).get() as { n: number } | undefined
  return row?.n ?? 0
}

function fileExists(workspace: WorkspaceManager, relative: string): boolean {
  try {
    return existsSync(workspace.paths.abs(relative))
  } catch {
    return false
  }
}

function inspectDatabaseFile(
  file: string,
  kind: DbKind,
  logger: Logger | undefined
): { report: DbReport; issues: string[] } {
  const issues: string[] = []
  const report: DbReport = {
    path: file,
    schemaVersion: 0,
    targetVersion: 0,
    integrityOk: false,
    foreignKeyViolations: [],
    sizeBytes: 0,
    walBytes: 0
  }
  const inspection = inspectDatabase(file, kind, logger)
  report.schemaVersion = inspection.version
  report.targetVersion = inspection.target
  if (existsSync(file)) report.sizeBytes = statSync(file).size
  if (existsSync(`${file}-wal`)) report.walBytes = statSync(`${file}-wal`).size
  if (inspection.issues.length > 0) {
    issues.push(...inspection.issues)
    return { report, issues }
  }

  let db
  try {
    db = openReadOnlyDb(file, { loadVector: false, verifyFts: false, logger }).db
  } catch (error) {
    report.integrityError = messageOf(error)
    return { report, issues }
  }
  try {
    const integrity = db.pragma('integrity_check') as Array<Record<string, unknown>>
    const problems = integrity
      .map((row) => String(row.integrity_check ?? Object.values(row)[0] ?? ''))
      .filter((value) => value !== 'ok' && value !== '')
    report.integrityOk = problems.length === 0
    if (problems.length > 0) report.integrityError = problems.join('; ')

    const violations = db.pragma('foreign_key_check') as Array<Record<string, unknown>>
    report.foreignKeyViolations = violations.map((row) =>
      ['table', 'rowid', 'parent', 'fkid'].map((key) => String(row[key] ?? '')).join('/')
    )
  } catch (error) {
    report.integrityError = messageOf(error)
  } finally {
    db.close()
  }
  return { report, issues }
}

function inspectConfig(workspace: WorkspaceManager, warnings: string[]): ConfigReport {
  const store = workspace.config
  const report: ConfigReport = {
    path: store.path(),
    exists: existsSync(store.path()),
    workspaceRoot: '',
    rootReachable: false,
    zoteroDataDir: ''
  }
  try {
    const config = store.load()
    report.workspaceRoot = config.workspace_root
    report.zoteroDataDir = config.zotero_data_dir
  } catch (error) {
    if (isAppError(error) && error.code === AppErrorCode.ConfigCorrupt) {
      const backup = latestCorruptBackup(store.path())
      if (backup !== undefined) report.corruptBackup = backup
    } else {
      warnings.push(`config: ${messageOf(error)}`)
    }
  }
  report.rootReachable = existsSync(workspace.root) && statSync(workspace.root).isDirectory()
  return report
}

function latestCorruptBackup(configPath: string): string | undefined {
  const directory = path.dirname(configPath)
  const prefix = `${path.basename(configPath)}.corrupt-`
  try {
    const matches = readdirSync(directory)
      .filter((name) => name.startsWith(prefix))
      .sort()
    const latest = matches[matches.length - 1]
    return latest === undefined ? undefined : path.join(directory, latest)
  } catch {
    return undefined
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}