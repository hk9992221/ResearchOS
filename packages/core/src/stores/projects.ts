/**
 * Minimal store over global.db.projects. It never writes
 * Activity events: `entity.updated`/`status.changed` belong to the project
 * module, which composes these calls inside its own transaction.
 */
import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from '../clock'
import { AppErrorCode, appError } from '../errors'
import type { EventTx } from '../events'
import { isValidId, newId } from '../ids'
import { type ProjectStatus, isProjectStatus } from '../schema'

export interface Project {
  id: string
  name: string
  summary: string
  goals: string
  focusMd: string
  status: ProjectStatus
  archivedAt?: Date
  createdAt: Date
  updatedAt: Date
}

/** A missing field means "leave unchanged". */
export interface ProjectPatch {
  name?: string
  summary?: string
  goals?: string
  focusMd?: string
}

const SELECT_COLUMNS =
  'SELECT id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at'

interface ProjectRow {
  id: string
  name: string
  summary: string
  goals: string
  focus_md: string
  status: ProjectStatus
  archived_at: string | null
  created_at: string
  updated_at: string
}

export class ProjectStore {
  private readonly db: EventTx['db']
  private readonly clock: Clock

  constructor(source: EventTx, clock: Clock = new SystemClock()) {
    this.db = source.db
    this.clock = clock
  }

  /** Inserts a project with status `active` and returns its new UUIDv7. */
  create(tx: EventTx, name: string, summary: string, goals: string): string {
    const id = newId(this.clock)
    this.createWithId(tx, id, name, summary, goals)
    return id
  }

  /**
   * Inserts a project with a caller-supplied id. The project service generates
   * the UUIDv7 itself so the data directory name equals the project id.
   */
  createWithId(tx: EventTx, id: string, name: string, summary: string, goals: string): void {
    if (!isValidId(id)) {
      throw appError(AppErrorCode.BadEntityId, `projects: create: invalid project id ${JSON.stringify(id)}`)
    }
    const now = formatTimestamp(this.clock.now())
    tx.db
      .prepare(
        `INSERT INTO projects(id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at)
         VALUES (?, ?, ?, ?, '', 'active', NULL, ?, ?)`
      )
      .run(id, name, summary, goals, now, now)
  }

  get(id: string): Project {
    const row = this.db.prepare(`${SELECT_COLUMNS} FROM projects WHERE id = ?`).get(id) as
      | ProjectRow
      | undefined
    if (row === undefined) throw appError(AppErrorCode.NotFound, `projects: get ${id}: no such row`)
    return decode(row)
  }

  /** Projects ordered by creation, filtered by status when given. */
  list(status?: ProjectStatus): Project[] {
    const rows = (
      status === undefined
        ? this.db.prepare(`${SELECT_COLUMNS} FROM projects ORDER BY created_at, id`).all()
        : this.db.prepare(`${SELECT_COLUMNS} FROM projects WHERE status = ? ORDER BY created_at, id`).all(status)
    ) as ProjectRow[]
    return rows.map(decode)
  }

  /** Writes the given patch fields and refreshes `updated_at`; an empty patch is a no-op. */
  update(tx: EventTx, id: string, patch: ProjectPatch): void {
    const sets: string[] = []
    const args: unknown[] = []
    if (patch.name !== undefined) {
      sets.push('name = ?')
      args.push(patch.name)
    }
    if (patch.summary !== undefined) {
      sets.push('summary = ?')
      args.push(patch.summary)
    }
    if (patch.goals !== undefined) {
      sets.push('goals = ?')
      args.push(patch.goals)
    }
    if (patch.focusMd !== undefined) {
      sets.push('focus_md = ?')
      args.push(patch.focusMd)
    }
    if (sets.length === 0) return
    sets.push('updated_at = ?')
    args.push(formatTimestamp(this.clock.now()), id)
    const result = tx.db.prepare(`UPDATE projects SET ${sets.join(', ')} WHERE id = ?`).run(...args)
    requireOneRow(result.changes, 'update', id)
  }

  /** Archived sets `archived_at`; active clears it. The caller emits `status.changed`. */
  setStatus(tx: EventTx, id: string, status: ProjectStatus): void {
    if (!isProjectStatus(status)) {
      throw appError(AppErrorCode.InvalidArgument, `projects: set status ${id}: invalid status ${JSON.stringify(status)}`)
    }
    const now = formatTimestamp(this.clock.now())
    const result = tx.db
      .prepare('UPDATE projects SET status = ?, archived_at = ?, updated_at = ? WHERE id = ?')
      .run(status, status === 'archived' ? now : null, now, id)
    requireOneRow(result.changes, 'set status', id)
  }

  /** Removes the registration row; only the project deletion flow calls this. */
  delete(tx: EventTx, id: string): void {
    const result = tx.db.prepare('DELETE FROM projects WHERE id = ?').run(id)
    requireOneRow(result.changes, 'delete', id)
  }
}

function decode(row: ProjectRow): Project {
  const decoded: Project = {
    id: row.id,
    name: row.name,
    summary: row.summary,
    goals: row.goals,
    focusMd: row.focus_md,
    status: row.status,
    createdAt: parseTimestamp(row.created_at),
    updatedAt: parseTimestamp(row.updated_at)
  }
  if (row.archived_at !== null) decoded.archivedAt = parseTimestamp(row.archived_at)
  return decoded
}

function requireOneRow(changes: number, operation: string, id: string): void {
  if (changes === 0) {
    throw appError(AppErrorCode.NotFound, `projects: ${operation} ${id}: no such row`)
  }
}