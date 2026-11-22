import {
  closeSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readdirSync,
  renameSync,
  rmSync
} from 'node:fs'
import { homedir } from 'node:os'
import * as path from 'node:path'

import { formatTimestamp, type Clock, SystemClock } from '../clock'
import type { Logger } from '../logger'

/** Retention count per database. */
export const BACKUP_KEEP = 5

export interface BackupOptions {
  clock?: Clock
  /** Defaults to `<OS cache dir>/ResearchOS/backups`. */
  backupRoot?: string
  logger?: Logger
}

/** The slice of better-sqlite3 the snapper needs. */
export interface SqlExec {
  exec(sql: string): unknown
}

/**
 * Per-database backup directory name. `global.db` maps to `global`;
 * `project.db` maps to `project-<project-id>` so snapshots of different
 * projects never compete in one retention bucket.
 */
export function backupStem(dbPath: string): string {
  const stem = path.basename(dbPath, path.extname(dbPath))
  if (stem !== 'project') return stem
  const parent = path.basename(path.dirname(dbPath))
  return parent === '' || parent === '.' ? stem : `project-${parent}`
}

/** `<platform cache dir>/ResearchOS/backups`, mirroring the Go default. */
export function defaultBackupRoot(): string {
  const cache =
    process.platform === 'win32'
      ? process.env.LOCALAPPDATA
      : process.platform === 'darwin'
        ? path.join(homedir(), 'Library', 'Caches')
        : (process.env.XDG_CACHE_HOME ?? path.join(homedir(), '.cache'))
  return path.join(cache ?? homedir(), 'ResearchOS', 'backups')
}

/**
 * Snapshots one database with `VACUUM INTO`, fsyncs the copy and renames it to
 * its final name. An unavailable backup root is a warning, not a failure: the
 * migration itself is transaction protected. A failed snapshot or rename aborts
 * the migration.
 */
export function backupDatabase(db: SqlExec, dbPath: string, fromVersion: number, options: BackupOptions = {}): string {
  const clock = options.clock ?? new SystemClock()
  const logger = options.logger
  const root = options.backupRoot ?? defaultBackupRoot()
  const dir = path.join(root, backupStem(dbPath))

  try {
    mkdirSync(dir, { recursive: true })
  } catch (error) {
    logger?.warn('migration backup skipped: backup directory unavailable', { dir, error: String(error) })
    return ''
  }

  const tmp = path.join(dir, `.tmp-${process.pid}-${Date.now()}-${Math.trunc(clock.now().getTime())}.db`)
  rmSync(tmp, { force: true })
  try {
    db.exec(`VACUUM INTO '${tmp.replaceAll("'", "''")}'`)
    fsyncFile(tmp)
    const final = uniqueBackupPath(dir, clock, fromVersion)
    renameSync(tmp, final)
    retainBackups(dir, BACKUP_KEEP, logger)
    return final
  } catch (error) {
    rmSync(tmp, { force: true })
    throw error
  }
}

function uniqueBackupPath(dir: string, clock: Clock, fromVersion: number): string {
  // Go layout 20060102-150405: YYYYMMDD-HHMMSS in UTC.
  const iso = formatTimestamp(clock.now())
  const stamp =
    `${iso.slice(0, 4)}${iso.slice(5, 7)}${iso.slice(8, 10)}` +
    `-${iso.slice(11, 13)}${iso.slice(14, 16)}${iso.slice(17, 19)}`
  const base = path.join(dir, `${stamp}-v${fromVersion}.db`)
  let candidate = base
  for (let attempt = 2; existsSync(candidate); attempt += 1) {
    candidate = path.join(dir, `${stamp}-v${fromVersion}-${attempt}.db`)
  }
  return candidate
}

function fsyncFile(file: string): void {
  const fd = openSync(file, 'r+')
  try {
    fsyncSync(fd)
  } finally {
    closeSync(fd)
  }
}

/** Keeps the newest `keep` `.db` snapshots; names sort chronologically. */
export function retainBackups(dir: string, keep: number, logger?: Logger): void {
  let entries: string[]
  try {
    entries = readdirSync(dir)
  } catch (error) {
    logger?.warn('migration backup retention failed', { dir, error: String(error) })
    return
  }
  const snapshots = entries
    .filter((name) => !name.startsWith('.') && name.endsWith('.db'))
    .sort((left, right) => right.localeCompare(left))
  for (const stale of snapshots.slice(keep)) {
    try {
      rmSync(path.join(dir, stale), { force: true })
    } catch (error) {
      logger?.warn('migration backup retention failed', { dir, file: stale, error: String(error) })
    }
  }
}
