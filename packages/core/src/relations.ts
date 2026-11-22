/**
 * Reads and writes the generic `entity_relations` triples. The
 * service validates the direction registry, refuses self relations and dead
 * endpoints, and emits `relation.added`/`relation.removed` in the caller's
 * transaction with the 5-key tuple payload.
 *
 * Project↔Paper associations never come through here: they live in
 * `project_papers` and are recorded by the literature service.
 */
import { type Clock, SystemClock, formatTimestamp } from './clock'
import { AppErrorCode, appError } from './errors'
import type { EventBus, EventObject, EventTx } from './events'
import { isValidId, newId } from './ids'
import {
  type Actor,
  type EntityType,
  type Relation,
  RELATION_RULES,
  isEntityType,
  isRelation
} from './schema'
import type { ProjectHandle } from './workspace/handle'

export interface RelationLink {
  srcType: EntityType
  srcId: string
  relation: Relation
  dstType: EntityType
  dstId: string
}

/**
 * Resolution of a relation target. A soft-deleted target keeps its rows (the
 * reader renders "target deleted"); a missing one is dirty data.
 */
export type TargetState = 'alive' | 'soft_deleted' | 'missing'

export interface RelationServiceOptions {
  /** Global database handle, needed for `project`/`paper` endpoints and targets. */
  global?: EventTx
  clock?: Clock
}

interface EntityTable {
  table: string
  global: boolean
  softDeletable: boolean
}

/** Entity→table map, verbatim from  */
const ENTITY_TABLES: Readonly<Record<EntityType, EntityTable>> = {
  project: { table: 'projects', global: true, softDeletable: false },
  paper: { table: 'papers', global: true, softDeletable: false },
  note: { table: 'notes', global: false, softDeletable: true },
  research_database: { table: 'research_databases', global: false, softDeletable: true },
  db_field: { table: 'db_fields', global: false, softDeletable: true },
  db_record: { table: 'db_records', global: false, softDeletable: true },
  idea: { table: 'ideas', global: false, softDeletable: true },
  evidence: { table: 'evidence', global: false, softDeletable: true },
  experiment: { table: 'experiments', global: false, softDeletable: true },
  run: { table: 'runs', global: false, softDeletable: true },
  snapshot: { table: 'snapshots', global: false, softDeletable: false },
  task: { table: 'tasks', global: false, softDeletable: true },
  weekly_report: { table: 'weekly_reports', global: false, softDeletable: true },
  entity_relation: { table: 'entity_relations', global: false, softDeletable: false }
}

const SELECT_TUPLE = 'SELECT src_type, src_id, relation, dst_type, dst_id FROM entity_relations'

/** True when `src --relation--> dst` is in the direction registry (§4.6). */
export function isDirectionAllowed(src: EntityType, relation: Relation, dst: EntityType): boolean {
  const rule = RELATION_RULES[relation]
  if (rule.src.length > 0 && !rule.src.includes(src)) return false
  return rule.dst.length === 0 || rule.dst.includes(dst)
}

export class RelationService {
  private readonly project: ProjectHandle
  private readonly global: EventTx | undefined
  private readonly events: EventBus
  private readonly clock: Clock

  constructor(project: ProjectHandle, events: EventBus, options: RelationServiceOptions = {}) {
    this.project = project
    this.global = options.global
    this.events = events
    this.clock = options.clock ?? new SystemClock()
  }

  /**
   * Creates a relation. An existing triple is returned unchanged (same id, no
   * write, no event) even when an endpoint has since been soft-deleted.
   */
  link(tx: EventTx, link: RelationLink, actor: Actor): string {
    this.validateShape(link)
    if (link.srcType === link.dstType && link.srcId === link.dstId) {
      throw appError(AppErrorCode.SelfRelation, `${link.srcType} ${link.srcId}`)
    }
    if (!isDirectionAllowed(link.srcType, link.relation, link.dstType)) {
      throw appError(
        AppErrorCode.RelationNotAllowed,
        `${link.srcType} --${link.relation}--> ${link.dstType}`
      )
    }
    this.checkCrossDatabase(link)

    const existing = this.find(tx, link)
    if (existing !== undefined) return existing

    this.requireAlive(tx, link.srcType, link.srcId, 'source')
    this.requireAlive(tx, link.dstType, link.dstId, 'target')

    const id = newId(this.clock)
    const result = tx.db
      .prepare(
        `INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(src_type, src_id, relation, dst_type, dst_id) DO NOTHING`
      )
      .run(
        id,
        link.srcType,
        link.srcId,
        link.relation,
        link.dstType,
        link.dstId,
        formatTimestamp(this.clock.now())
      )
    if (result.changes === 0) {
      const raced = this.find(tx, link)
      if (raced === undefined) throw new Error('relations: link: conflict without an existing row')
      return raced
    }
    this.events.emit(tx, {
      type: 'relation.added',
      entityType: link.srcType,
      entityId: link.srcId,
      new: relationTuple(link),
      summary: '建立关系',
      actor
    })
    return id
  }

  /** Deletes the triple. A missing triple is a no-op. */
  unlink(tx: EventTx, link: RelationLink, actor: Actor): void {
    const id = this.find(tx, link)
    if (id === undefined) return
    tx.db.prepare('DELETE FROM entity_relations WHERE id = ?').run(id)
    this.events.emit(tx, {
      type: 'relation.removed',
      entityType: link.srcType,
      entityId: link.srcId,
      old: relationTuple(link),
      summary: '解除关系',
      actor
    })
  }

  /** Relations with the given source; an omitted `relation` means all. */
  from(srcType: EntityType, srcId: string, relation?: Relation): RelationLink[] {
    const query = `${SELECT_TUPLE} WHERE src_type = ? AND src_id = ?`
    return this.queryLinks(
      relation === undefined ? query : `${query} AND relation = ?`,
      relation === undefined ? [srcType, srcId] : [srcType, srcId, relation]
    )
  }

  /** Relations with the given target; an omitted `relation` means all. */
  to(dstType: EntityType, dstId: string, relation?: Relation): RelationLink[] {
    const query = `${SELECT_TUPLE} WHERE dst_type = ? AND dst_id = ?`
    return this.queryLinks(
      relation === undefined ? query : `${query} AND relation = ?`,
      relation === undefined ? [dstType, dstId] : [dstType, dstId, relation]
    )
  }

  /** Reports whether the target exists, is soft-deleted, or is gone. */
  resolve(dstType: EntityType, dstId: string): TargetState {
    const target = ENTITY_TABLES[dstType]
    if (target === undefined) throw appError(AppErrorCode.BadEntityType, JSON.stringify(dstType))
    if (!target.global) return probeTarget(this.project.db, target, dstId)
    if (this.global === undefined) throw new Error(`relations: no global database for ${dstType}`)
    return probeTarget(this.global.db, target, dstId)
  }

  private validateShape(link: RelationLink): void {
    if (!isEntityType(link.srcType) || !isEntityType(link.dstType) || !isRelation(link.relation)) {
      throw appError(
        AppErrorCode.BadEntityType,
        `source ${JSON.stringify(link.srcType)} target ${JSON.stringify(link.dstType)} relation ${JSON.stringify(link.relation)}`
      )
    }
    if (!isValidId(link.srcId) || !isValidId(link.dstId)) {
      throw appError(
        AppErrorCode.BadEntityId,
        `source ${JSON.stringify(link.srcId)} target ${JSON.stringify(link.dstId)}`
      )
    }
  }

  /**
   * Only `references → paper` may cross databases, and a global entity is
   * never a source (§5.7).
   */
  private checkCrossDatabase(link: RelationLink): void {
    if (ENTITY_TABLES[link.srcType].global) {
      throw appError(AppErrorCode.RelationNotAllowed, `source ${link.srcType} lives in global.db`)
    }
    if (ENTITY_TABLES[link.dstType].global) {
      if (link.relation !== 'references' || link.dstType !== 'paper') {
        throw appError(
          AppErrorCode.RelationNotAllowed,
          'only references -> paper may cross databases'
        )
      }
    }
  }

  private find(tx: EventTx, link: RelationLink): string | undefined {
    const row = tx.db
      .prepare(
        `SELECT id FROM entity_relations
         WHERE src_type = ? AND src_id = ? AND relation = ? AND dst_type = ? AND dst_id = ?`
      )
      .get(link.srcType, link.srcId, link.relation, link.dstType, link.dstId) as
      | { id: string }
      | undefined
    return row?.id
  }

  /**
   * Confirms that the endpoint exists and is live. Project-local entities are
   * read through the caller's transaction; global ones through global.db.
   */
  private requireAlive(tx: EventTx, type: EntityType, id: string, role: string): void {
    const target = ENTITY_TABLES[type]
    if (target === undefined) throw appError(AppErrorCode.BadEntityType, JSON.stringify(type))
    let db = this.project.db
    if (target.global) {
      if (this.global === undefined) throw new Error(`relations: no global database for ${type}`)
      db = this.global.db
    } else if (tx.db !== this.project.db) {
      db = tx.db
    }

    if (target.softDeletable) {
      const row = db.prepare(`SELECT deleted_at FROM ${target.table} WHERE id = ?`).get(id) as
        | { deleted_at: string | null }
        | undefined
      if (row === undefined) {
        throw appError(AppErrorCode.NotFound, `relations: ${role} ${type} ${id} not found`)
      }
      if (row.deleted_at !== null) {
        throw appError(AppErrorCode.EntityDeleted, `relations: ${role} ${type} ${id} is soft-deleted`)
      }
      return
    }
    if (db.prepare(`SELECT 1 AS one FROM ${target.table} WHERE id = ?`).get(id) === undefined) {
      throw appError(AppErrorCode.NotFound, `relations: ${role} ${type} ${id} not found`)
    }
  }

  private queryLinks(query: string, args: unknown[]): RelationLink[] {
    const rows = this.project.db.prepare(`${query} ORDER BY created_at, id`).all(...args) as Array<{
      src_type: EntityType
      src_id: string
      relation: Relation
      dst_type: EntityType
      dst_id: string
    }>
    return rows.map((row) => ({
      srcType: row.src_type,
      srcId: row.src_id,
      relation: row.relation,
      dstType: row.dst_type,
      dstId: row.dst_id
    }))
  }
}

function probeTarget(db: EventTx['db'], target: EntityTable, id: string): TargetState {
  if (target.softDeletable) {
    const row = db.prepare(`SELECT deleted_at FROM ${target.table} WHERE id = ?`).get(id) as
      | { deleted_at: string | null }
      | undefined
    if (row === undefined) return 'missing'
    return row.deleted_at === null ? 'alive' : 'soft_deleted'
  }
  return db.prepare(`SELECT 1 AS one FROM ${target.table} WHERE id = ?`).get(id) === undefined
    ? 'missing'
    : 'alive'
}

/** The 5-key payload object shared with the event writer. */
function relationTuple(link: RelationLink): EventObject {
  return {
    src_type: link.srcType,
    src_id: link.srcId,
    relation: link.relation,
    dst_type: link.dstType,
    dst_id: link.dstId
  }
}