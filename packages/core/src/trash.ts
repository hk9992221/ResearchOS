/**
 * Soft deletion, restore, the trash listing and the irreversible purge
 * Bound to one project database, its path resolver, its event
 * writer and the confirmation service. It never touches global papers and never
 * cascades: deleting a database does not delete its records.
 *
 * Trash is DB-only: "回收站" is `deleted_at` plus the `v_trash` view; there is
 * no trash directory. File removal happens after the purge commits, so a file
 * failure is a warning and never rolls the rows back.
 */
import { type Dirent, type Stats, readdirSync, rmSync, statSync } from 'node:fs'

import { type Clock, SystemClock, formatTimestamp } from './clock'
import type { ConfirmScope, ConfirmService } from './confirm'
import { inImmediateTransaction } from './db/drizzle'
import { AppErrorCode, appError } from './errors'
import type { EventBus, EventTx } from './events'
import { isValidId } from './ids'
import type { Logger } from './logger'
import type { Actor, EntityType } from './schema'
import type { ProjectHandle } from './workspace/handle'

/** One entity to delete, restore or purge. */
export interface TrashRef {
  type: EntityType
  id: string
}

/** One `v_trash` row. */
export interface TrashItem {
  entityType: EntityType
  id: string
  title: string
  deletedAt: Date
}

export interface TrashListQuery {
  /** Empty means every type visible in `v_trash`. */
  entityTypes?: readonly EntityType[]
  /** Defaults to 100, capped at 500. */
  limit?: number
  offset?: number
}

/** Purge requires a `purge_entity` token scoped to exactly these refs. */
export interface PurgeRequest {
  refs: readonly TrashRef[]
  confirm: string
  actor: Actor
}

/** Empty requires an `empty_trash` token scoped to every current trash item. */
export interface EmptyRequest {
  confirm: string
  actor: Actor
}

export interface PurgeResult {
  purged: number
  removedFiles: number
  removedDirs: number
  clearedLinks: number
  purgedEvents: number
  /** File-cleanup failures are reported here instead of being swallowed. */
  warnings: string[]
}

export interface TrashServiceOptions {
  clock?: Clock
  logger?: Logger
}

/**
 * The ten soft-deletable entity types, verbatim. `db_fields` is soft-deletable
 * but deliberately absent from `v_trash`.
 */
const SOFT_DELETABLE: Readonly<Partial<Record<EntityType, string>>> = {
  note: 'notes',
  research_database: 'research_databases',
  db_field: 'db_fields',
  db_record: 'db_records',
  idea: 'ideas',
  evidence: 'evidence',
  experiment: 'experiments',
  run: 'runs',
  task: 'tasks',
  weekly_report: 'weekly_reports'
}

const DEFAULT_LIST_LIMIT = 100
const MAX_LIST_LIMIT = 500

function normalizedLimit(limit: number | undefined): number {
  if (limit === undefined || limit <= 0) return DEFAULT_LIST_LIMIT
  return limit > MAX_LIST_LIMIT ? MAX_LIST_LIMIT : Math.trunc(limit)
}

export class TrashService {
  private readonly project: ProjectHandle
  private readonly events: EventBus
  private readonly confirm: ConfirmService
  private readonly clock: Clock
  private readonly logger: Logger | undefined

  constructor(
    project: ProjectHandle,
    events: EventBus,
    confirm: ConfirmService,
    options: TrashServiceOptions = {}
  ) {
    this.project = project
    this.events = events
    this.confirm = confirm
    this.clock = options.clock ?? new SystemClock()
    this.logger = options.logger
  }

  /**
   * Moves an entity into the trash. Deleting an already-deleted entity is a
   * no-op that emits nothing. Note files and attachment directories stay.
   */
  softDelete(tx: EventTx, ref: TrashRef, actor: Actor): void {
    const table = this.tableFor(ref)
    const now = formatTimestamp(this.clock.now())
    const result = tx.db
      .prepare(`UPDATE ${table} SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`)
      .run(now, ref.id)
    if (result.changes === 0) {
      const row = tx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(ref.id) as
        | { deleted_at: string | null }
        | undefined
      if (row === undefined) {
        throw appError(AppErrorCode.NotFound, `trash: soft delete ${ref.type} ${ref.id}: no such row`)
      }
      return // already in the trash: idempotent, no event
    }
    this.events.emit(tx, {
      type: 'entity.deleted',
      entityType: ref.type,
      entityId: ref.id,
      summary: '移入回收站',
      actor
    })
  }

  /** Clears `deleted_at`. Restoring a live entity is a no-op that emits nothing. */
  restore(tx: EventTx, ref: TrashRef, actor: Actor): void {
    const table = this.tableFor(ref)
    const result = tx.db
      .prepare(`UPDATE ${table} SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`)
      .run(ref.id)
    if (result.changes === 0) {
      const row = tx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(ref.id) as
        | { deleted_at: string | null }
        | undefined
      if (row === undefined) {
        throw appError(AppErrorCode.NotFound, `trash: restore ${ref.type} ${ref.id}: no such row`)
      }
      return // already alive: idempotent, no event
    }
    this.events.emit(tx, {
      type: 'entity.restored',
      entityType: ref.type,
      entityId: ref.id,
      summary: '从回收站恢复',
      actor
    })
  }

  /** Reads `v_trash`, newest deletion first, then type and id. */
  list(query: TrashListQuery = {}): TrashItem[] {
    let sql = 'SELECT entity_type, id, title, deleted_at FROM v_trash'
    const args: unknown[] = []
    if (query.entityTypes !== undefined && query.entityTypes.length > 0) {
      sql += ` WHERE entity_type IN (${query.entityTypes.map(() => '?').join(', ')})`
      args.push(...query.entityTypes)
    }
    const rows = this.project.db.prepare(sql).all(...args) as Array<{
      entity_type: EntityType
      id: string
      title: string
      deleted_at: string
    }>
    const items = rows.map((row) => ({
      entityType: row.entity_type,
      id: row.id,
      title: row.title,
      deletedAt: new Date(row.deleted_at)
    }))
    items.sort((left, right) => {
      const delta = right.deletedAt.getTime() - left.deletedAt.getTime()
      if (delta !== 0) return delta
      if (left.entityType !== right.entityType) return left.entityType < right.entityType ? -1 : 1
      return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
    })
    const offset = query.offset !== undefined && query.offset > 0 ? Math.trunc(query.offset) : 0
    return items.slice(offset, offset + normalizedLimit(query.limit))
  }

  /** Number of trash items (the view excludes `db_fields`). */
  count(): number {
    const row = this.project.db.prepare('SELECT count(*) AS n FROM v_trash').get() as {
      n: number
    }
    return row.n
  }

  /**
   * Permanently deletes the requested entities after consuming a matching
   * confirmation token: relation rows first (each with `relation.removed`),
   * then the entity rows (`entity.purged`), then their files.
   */
  purge(request: PurgeRequest): PurgeResult {
    const refs = dedupe(request.refs)
    this.validateRefs(refs)
    this.consume('purge_entity', request.confirm, refs)
    return this.purgeRefs(refs, request.actor)
  }

  /** Purges every current trash item with one `empty_trash` token. */
  empty(request: EmptyRequest): PurgeResult {
    const refs = this.trashRefs()
    this.consume('empty_trash', request.confirm, refs)
    return this.purgeRefs(refs, request.actor)
  }

  private purgeRefs(refs: readonly TrashRef[], actor: Actor): PurgeResult {
    const result: PurgeResult = {
      purged: 0,
      removedFiles: 0,
      removedDirs: 0,
      clearedLinks: 0,
      purgedEvents: 0,
      warnings: []
    }
    if (refs.length === 0) return result
    this.validateRefs(refs)

    const noteIDs: string[] = []
    inImmediateTransaction(this.project.db, () => {
      for (const ref of refs) {
        const links = this.linksTouching(ref)
        for (const link of links) {
          this.project.db.prepare('DELETE FROM entity_relations WHERE id = ?').run(link.id)
          this.events.emit(this.project, {
            type: 'relation.removed',
            entityType: link.srcType,
            entityId: link.srcId,
            old: link.object,
            summary: '解除关系',
            actor
          })
          result.clearedLinks += 1
        }

        const table = SOFT_DELETABLE[ref.type]
        if (table === undefined) {
          throw appError(AppErrorCode.BadEntityType, `${ref.type} cannot be purged`)
        }
        const deleted = this.project.db.prepare(`DELETE FROM ${table} WHERE id = ?`).run(ref.id)
        if (deleted.changes > 0) {
          this.events.emit(this.project, {
            type: 'entity.purged',
            entityType: ref.type,
            entityId: ref.id,
            summary: '永久删除',
            actor
          })
          result.purged += 1
          result.purgedEvents += 1
        }
        if (ref.type === 'note') noteIDs.push(ref.id)
      }
    })

    // Files go after the commit: a file failure never rolls back rows.
    for (const noteID of noteIDs) {
      const cleanup = this.removeNoteFiles(noteID)
      result.removedFiles += cleanup.files
      result.removedDirs += cleanup.dirs
      result.warnings.push(...cleanup.warnings)
    }
    for (const warning of result.warnings) {
      this.logger?.warn('trash: purge cleanup warning', { warning })
    }
    return result
  }

  private validateRefs(refs: readonly TrashRef[]): void {
    for (const ref of refs) {
      if (SOFT_DELETABLE[ref.type] === undefined) {
        throw appError(AppErrorCode.BadEntityType, `${ref.type} cannot be purged`)
      }
      if (!isValidId(ref.id)) throw appError(AppErrorCode.BadEntityId, JSON.stringify(ref.id))
    }
  }

  private tableFor(ref: TrashRef): string {
    if (!isValidId(ref.id)) throw appError(AppErrorCode.BadEntityId, JSON.stringify(ref.id))
    const table = SOFT_DELETABLE[ref.type]
    if (table === undefined) {
      throw appError(AppErrorCode.BadEntityType, `${ref.type} is not soft-deletable`)
    }
    return table
  }

  private consume(kind: ConfirmScope['kind'], token: string, refs: readonly TrashRef[]): void {
    this.confirm.consume(token, {
      kind,
      projectId: this.project.id,
      entityTypes: refs.map((ref) => ref.type),
      entityIds: refs.map((ref) => ref.id)
    })
  }

  private trashRefs(): TrashRef[] {
    const rows = this.project.db
      .prepare('SELECT entity_type, id FROM v_trash')
      .all() as Array<{ entity_type: EntityType; id: string }>
    return dedupe(rows.map((row) => ({ type: row.entity_type, id: row.id })))
  }

  /** Every relation row naming `ref` on either side. */
  private linksTouching(ref: TrashRef): Array<{
    id: string
    srcType: EntityType
    srcId: string
    object: Record<string, string>
  }> {
    const rows = this.project.db
      .prepare(
        `SELECT id, src_type, src_id, relation, dst_type, dst_id FROM entity_relations
         WHERE (src_type = ? AND src_id = ?) OR (dst_type = ? AND dst_id = ?)`
      )
      .all(ref.type, ref.id, ref.type, ref.id) as Array<{
      id: string
      src_type: EntityType
      src_id: string
      relation: string
      dst_type: EntityType
      dst_id: string
    }>
    return rows.map((row) => ({
      id: row.id,
      srcType: row.src_type,
      srcId: row.src_id,
      object: {
        src_type: row.src_type,
        src_id: row.src_id,
        relation: row.relation,
        dst_type: row.dst_type,
        dst_id: row.dst_id
      }
    }))
  }

  /** Deletes `notes/<id>.md` and the `attachments/<id>/` tree of a purged note. */
  private removeNoteFiles(noteID: string): {
    files: number
    dirs: number
    warnings: string[]
  } {
    let files = 0
    let dirs = 0
    const warnings: string[] = []

    let notePath: string
    try {
      notePath = this.project.paths.abs(this.project.paths.noteFile(this.project.id, noteID))
    } catch (error) {
      return {
        files,
        dirs,
        warnings: [`note ${noteID}: note file path rejected: ${messageOf(error)}`]
      }
    }
    try {
      const info = statSync(notePath)
      if (info.isDirectory()) {
        warnings.push(`note ${noteID}: note file path is a directory, left in place`)
      } else {
        rmSync(notePath)
        files += 1
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') {
        warnings.push(`note ${noteID}: remove file: ${messageOf(error)}`)
      }
    }

    let attachments: string
    try {
      attachments = this.project.paths.abs(
        this.project.paths.attachmentsDir(this.project.id, noteID)
      )
    } catch (error) {
      return {
        files,
        dirs,
        warnings: [...warnings, `note ${noteID}: attachments path rejected: ${messageOf(error)}`]
      }
    }
    let info: Stats
    try {
      info = statSync(attachments)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return { files, dirs, warnings }
      return { files, dirs, warnings: [...warnings, `note ${noteID}: stat attachments: ${messageOf(error)}`] }
    }
    if (!info.isDirectory()) {
      return {
        files,
        dirs,
        warnings: [...warnings, `note ${noteID}: attachments path is not a directory`]
      }
    }
    const counted = countTree(attachments)
    try {
      rmSync(attachments, { recursive: true, force: true })
    } catch (error) {
      return {
        files,
        dirs,
        warnings: [...warnings, `note ${noteID}: remove attachments: ${messageOf(error)}`]
      }
    }
    return { files: files + counted.files, dirs: dirs + counted.dirs + 1, warnings }
  }
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function countTree(root: string): { files: number; dirs: number } {
  let files = 0
  let dirs = 0
  let entries: Dirent[]
  try {
    entries = readdirSync(root, { withFileTypes: true })
  } catch {
    return { files, dirs }
  }
  for (const entry of entries) {
    if (entry.isDirectory()) {
      dirs += 1
      const nested = countTree(`${root}/${entry.name}`)
      files += nested.files
      dirs += nested.dirs
    } else {
      files += 1
    }
  }
  return { files, dirs }
}

/** De-duplicates and sorts by type then id so the confirm scope is stable. */
function dedupe(refs: readonly TrashRef[]): TrashRef[] {
  const seen = new Set<string>()
  const unique: TrashRef[] = []
  for (const ref of refs) {
    const key = `${ref.type}\u0000${ref.id}`
    if (seen.has(key)) continue
    seen.add(key)
    unique.push({ type: ref.type, id: ref.id })
  }
  return unique.sort((left, right) => {
    if (left.type !== right.type) return left.type < right.type ? -1 : 1
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
  })
}

