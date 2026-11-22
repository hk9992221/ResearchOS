import {
  AppError,
  AppErrorCode,
  isEntityType,
  type ActivityActor,
  type EntityType,
  type RelationType,
  type TaskLink,
  type TaskSummary
} from '@researchos/shared'

import type { Db } from './deps'
import type { KanbanModule } from './module'
import {
  buildWhere,
  querySummaries,
  sortSpecFor,
  type SqlValue
} from './repo'
import { DELIVERABLE_OF_TARGETS, LIMITS, TASK_RELATIONS } from './types'
import { assertTaskId, invalid, notFound } from './validate'

export interface LinkInput {
  taskId: string
  relation: RelationType
  targetType: EntityType
  targetId: string
}

function relationNotAllowed(message: string, details: Record<string, unknown>): AppError {
  return new AppError(AppErrorCode.RelationNotAllowed, message, details)
}

function missingPort(): AppError {
  return new AppError(AppErrorCode.Internal, 'kanban: relations service is not configured', {
    port: 'relations'
  })
}

/**
 * Task → entity links. Validation, idempotency and the
 * relation.added/removed events belong to foundation relations; this service
 * maps task semantics onto it and resolves display state/backlinks.
 */
export class LinkService {
  constructor(private readonly m: KanbanModule) {}

  add(actor: ActivityActor, input: LinkInput): TaskLink {
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, input.taskId)
    this.assertRelation(input.relation, input.targetType, input.targetId, task.id)

    const relations = this.m.deps.relations
    if (relations === undefined) throw missingPort()
    const state = relations.resolve(input.targetType, input.targetId)
    if (state === 'missing') {
      throw relationNotAllowed(
        `target ${input.targetType} ${input.targetId} is not in this project`,
        { targetType: input.targetType, targetId: input.targetId }
      )
    }

    this.m.tx((tx) => {
      relations.link(
        { db: tx },
        {
          srcType: 'task',
          srcId: task.id,
          relation: input.relation,
          dstType: input.targetType,
          dstId: input.targetId
        },
        this.m.actor(actor)
      )
    })

    const link = this.list(task.id).find(
      (entry) => entry.relation === input.relation && entry.dstType === input.targetType && entry.dstId === input.targetId
    )
    if (link === undefined) {
      throw notFound(`relation ${input.relation} → ${input.targetId} not readable after link`)
    }
    return link
  }

  /** A missing triple is a no-op. */
  remove(actor: ActivityActor, input: LinkInput): void {
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, input.taskId)
    if (!TASK_RELATIONS[input.relation]) {
      throw relationNotAllowed(`task --${input.relation}--> ${input.targetType}`, {
        relation: input.relation
      })
    }
    if (!isEntityType(input.targetType)) {
      throw relationNotAllowed(`unknown entity type ${String(input.targetType)}`, {
        targetType: input.targetType
      })
    }
    assertTaskId(input.targetId, 'target id')
    const relations = this.m.deps.relations
    if (relations === undefined) throw missingPort()
    this.m.tx((tx) => {
      relations.unlink(
        { db: tx },
        {
          srcType: 'task',
          srcId: task.id,
          relation: input.relation,
          dstType: input.targetType,
          dstId: input.targetId
        },
        this.m.actor(actor)
      )
    })
  }

  /**
   * Outbound links with title and lifecycle state. `missing` targets are dirty
   * data and dropped with a warning; `deleted` targets stay and render
   * “target deleted” (AC-24).
   */
  list(taskId: string): TaskLink[] {
    assertTaskId(taskId)
    if (this.m.db.prepare('SELECT 1 AS x FROM tasks WHERE id = ?').get(taskId) === undefined) {
      throw notFound(`task ${taskId} not found`, { taskId })
    }
    const relations = this.m.deps.relations
    if (relations === undefined) throw missingPort()

    const rows = this.m.db
      .prepare(
        'SELECT id, relation, dst_type, dst_id FROM entity_relations ' +
          'WHERE src_type = ? AND src_id = ? ORDER BY created_at, id'
      )
      .all('task', taskId) as Array<{ id: string; relation: string; dst_type: string; dst_id: string }>

    const links: TaskLink[] = []
    for (const row of rows) {
      if (!isEntityType(row.dst_type)) continue
      const state = relations.resolve(row.dst_type, row.dst_id)
      if (state === 'missing') {
        this.m.logger.warn('kanban: relation target vanished', {
          relationId: row.id,
          dstType: row.dst_type,
          dstId: row.dst_id
        })
        continue
      }
      links.push({
        relationId: row.id,
        relation: row.relation as RelationType,
        dstType: row.dst_type,
        dstId: row.dst_id,
        dstTitle: this.targetTitle(this.m.db, row.dst_type, row.dst_id),
        targetState: state === 'soft_deleted' ? 'deleted' : 'alive'
      })
    }
    return links
  }

  /** Backlinks for another entity's detail view (live, unarchived tasks only). */
  listTasksByTarget(dstType: EntityType, dstId: string): TaskSummary[] {
    if (!isEntityType(dstType)) {
      throw new AppError(AppErrorCode.BadEntityType, `unknown entity type ${String(dstType)}`, {
        targetType: dstType
      })
    }
    assertTaskId(dstId, 'target id')
    const relations = this.m.deps.relations
    if (relations === undefined) throw missingPort()
    const taskIds = relations
      .to(dstType, dstId)
      .filter((entry) => entry.srcType === 'task')
      .map((entry) => entry.srcId)
    if (taskIds.length === 0) return []

    const placeholders = taskIds.map(() => '?').join(',')
    const where = buildWhere({})
    where.where.push(`t.id IN (${placeholders})`)
    where.args.push(...(taskIds as SqlValue[]))
    const result = querySummaries(this.m.db, {
      where,
      spec: sortSpecFor('position_asc'),
      limit: LIMITS.listMaxLimit
    })
    return result.items
  }

  private assertRelation(relation: RelationType, targetType: EntityType, targetId: string, taskId: string): void {
    if (!TASK_RELATIONS[relation]) {
      throw relationNotAllowed(`task --${relation}--> ${String(targetType)}`, { relation })
    }
    if (!isEntityType(targetType)) {
      throw relationNotAllowed(`unknown entity type ${String(targetType)}`, { targetType })
    }
    if (relation === 'deliverable_of' && !DELIVERABLE_OF_TARGETS[targetType]) {
      throw relationNotAllowed(`task --deliverable_of--> ${targetType}`, { relation, targetType })
    }
    assertTaskId(targetId, 'target id')
    if (targetType === 'task' && targetId === taskId) {
      throw invalid('a task cannot relate to itself', { field: 'targetId', reason: 'self-relation' })
    }
  }

  /**
   * Display title from the owning tables (module read projection). Runs and
   * weekly reports have no stable title column, so their labels are derived.
   */
  private targetTitle(db: Db, targetType: EntityType, targetId: string): string {
    const table =
      targetType === 'task'
        ? 'tasks'
        : targetType === 'note'
          ? 'notes'
          : targetType === 'idea'
            ? 'ideas'
            : targetType === 'experiment'
              ? 'experiments'
              : targetType === 'run'
                ? 'runs'
                : null
    if (table !== null) {
      const row = db.prepare(`SELECT title FROM ${table} WHERE id = ?`).get(targetId) as
        | { title: string | null }
        | undefined
      if (row?.title !== undefined && row.title !== null) return row.title
      return targetId
    }
    if (targetType === 'weekly_report') {
      const row = db.prepare('SELECT period_start, period_end FROM weekly_reports WHERE id = ?').get(targetId) as
        | { period_start: string; period_end: string }
        | undefined
      if (row !== undefined) return `周报 ${row.period_start} ~ ${row.period_end}`
    }
    return targetId
  }
}