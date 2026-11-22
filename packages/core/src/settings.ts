import type Database from 'better-sqlite3'

import { formatTimestamp, type Clock, SystemClock } from './clock'
import { AppError, AppErrorCode, appError } from './errors'

type Db = Database.Database

/** The registered key namespace: module segment + one or more dot segments. */
const KEY_PATTERN = /^[a-z][a-z0-9_]*(\.[a-z0-9][a-z0-9_-]*)+$/

export type SettingsScope = 'workspace' | 'project'

const TABLES: Readonly<Record<SettingsScope, string>> = {
  workspace: 'settings',
  project: 'project_settings'
}

export function isValidSettingKey(key: string): boolean {
  return KEY_PATTERN.test(key)
}

function assertKey(key: string): void {
  if (!KEY_PATTERN.test(key)) {
    throw appError(AppErrorCode.InvalidSettingKey, `settings: invalid key ${JSON.stringify(key)}`)
  }
}

/**
 * Typed access to the workspace-level `settings` table or the per-project
 * `project_settings` table. Both share `(key, value, updated_at)`, so one
 * implementation serves both; the constructor picks the table.
 *
 * Writes are plain statements: wrap them in `ProjectHandle.inTx` (or
 * `inImmediateTransaction`) when they must share a transaction with a business
 * change.
 */
export class SettingsStore {
  private readonly db: Db
  private readonly table: string
  private readonly clock: Clock

  constructor(db: Db, scope: SettingsScope, clock: Clock = new SystemClock()) {
    this.db = db
    this.table = TABLES[scope]
    this.clock = clock
  }

  get(key: string): string | undefined {
    assertKey(key)
    const row = this.db.prepare(`SELECT value FROM ${this.table} WHERE key = ?`).get(key) as
      | { value: string }
      | undefined
    return row?.value
  }

  getOr(key: string, fallback: string): string {
    return this.get(key) ?? fallback
  }

  getNumber(key: string, fallback: number): number {
    const raw = this.get(key)
    if (raw === undefined) return fallback
    const parsed = Number(raw)
    return Number.isFinite(parsed) ? parsed : fallback
  }

  getBoolean(key: string, fallback: boolean): boolean {
    const raw = this.get(key)
    if (raw === undefined) return fallback
    return raw === '1' || raw === 'true'
  }

  /** Decodes a JSON value; a missing key returns undefined. */
  getJson<T = unknown>(key: string): T | undefined {
    const raw = this.get(key)
    if (raw === undefined) return undefined
    try {
      return JSON.parse(raw) as T
    } catch (cause) {
      throw appError(AppErrorCode.InvalidSettingKey, `settings: ${key} does not hold JSON`, {
        cause,
        details: { key }
      })
    }
  }

  set(key: string, value: string): void {
    assertKey(key)
    this.db
      .prepare(
        `INSERT INTO ${this.table} (key, value, updated_at) VALUES (?, ?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
      )
      .run(key, value, formatTimestamp(this.clock.now()))
  }

  setJson(key: string, value: unknown): void {
    this.set(key, JSON.stringify(value))
  }

  /** Deleting a missing key is a no-op. */
  delete(key: string): void {
    assertKey(key)
    this.db.prepare(`DELETE FROM ${this.table} WHERE key = ?`).run(key)
  }

  /** Every key with the literal prefix (`''` means all), ordered by key. */
  list(prefix = ''): Record<string, string> {
    const statement =
      prefix === ''
        ? this.db.prepare(`SELECT key, value FROM ${this.table} ORDER BY key`)
        : this.db.prepare(`SELECT key, value FROM ${this.table} WHERE key LIKE ? ESCAPE '\\' ORDER BY key`)
    const rows = (prefix === '' ? statement.all() : statement.all(`${escapeLike(prefix)}%`)) as Array<{
      key: string
      value: string
    }>
    const result: Record<string, string> = {}
    for (const row of rows) result[row.key] = row.value
    return result
  }
}

function escapeLike(prefix: string): string {
  return prefix.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')
}

/**
 * Precedence for the project-scoped export keys: the project
 * store first, then the read-only workspace compatibility key
 * `project.<project-id>.<key>`. Writes always go to the project store.
 */
export function resolveSettingPriority(
  project: SettingsStore | undefined,
  workspace: SettingsStore | undefined,
  projectId: string,
  key: string
): string | undefined {
  assertKey(key)
  const projectValue = project?.get(key)
  if (projectValue !== undefined) return projectValue
  return workspace?.get(`project.${projectId}.${key}`)
}