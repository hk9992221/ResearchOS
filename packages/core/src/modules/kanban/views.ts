import type { BoardView, TaskPage, TaskPriority } from '@researchos/shared'

import type { KanbanModule } from './module'
import {
  buildWhere,
  countSummaries,
  defaultColumnName,
  listColumns,
  querySummaries,
  sortSpecFor
} from './repo'
import { LIMITS } from './types'
import { currentIsoWeek, validateWeekPlan } from './validate'

export interface BoardQuery {
  priority?: TaskPriority[]
  labels?: string[]
  blocked?: boolean
  q?: string
}

function clamp(limit: number | undefined): number {
  if (limit === undefined || limit <= 0) return LIMITS.listDefaultLimit
  return Math.min(limit, LIMITS.listMaxLimit)
}

/**
 * Read-only projections. Board and Blocked always exclude
 * archived/deleted tasks; counters arrive with the rows (one statement).
 */
export class ViewService {
  constructor(private readonly m: KanbanModule) {}

  board(query: BoardQuery = {}): BoardView {
    const columns = this.m.seededTx((tx) => listColumns(tx))
    const where = buildWhere({
      onlyTopLevel: true,
      priority: query.priority,
      labels: query.labels,
      blocked: query.blocked,
      text: query.q
    })
    const result = querySummaries(this.m.db, {
      where,
      spec: sortSpecFor('position_asc'),
      limit: LIMITS.activeTasks
    })
    const byColumn = new Map<string, typeof result.items>()
    for (const task of result.items) {
      const list = byColumn.get(task.column) ?? []
      list.push(task)
      byColumn.set(task.column, list)
    }
    const view: BoardView = { projectId: this.m.projectId, columns: [] }
    for (const column of columns) {
      view.columns.push({ column, tasks: byColumn.get(column.name) ?? [] })
      byColumn.delete(column.name)
    }
    if (byColumn.size > 0) {
      this.m.logger.warn('kanban: board found tasks on unknown columns', { count: byColumn.size })
    }
    return view
  }

  /** Blocked and unfinished: completion reads `is_done`, never the kind. */
  blocked(limit?: number, cursor?: string): TaskPage {
    const where = buildWhere({ blocked: true })
    where.where.push('NOT EXISTS (SELECT 1 FROM task_columns c WHERE c.name = t."column" AND c.is_done = 1)')
    const total = countSummaries(this.m.db, where)
    const result = querySummaries(this.m.db, {
      where,
      spec: sortSpecFor('deadline_asc'),
      limit: clamp(limit),
      cursor: cursor ?? ''
    })
    return { items: result.items, nextCursor: result.nextCursor, total }
  }

  /** My Week: tasks planned for one ISO week (defaults to the current one). */
  week(weekPlan?: string, limit?: number, cursor?: string): TaskPage {
    const week = weekPlan === undefined || weekPlan === '' ? currentIsoWeek(this.m.clock.now()) : weekPlan
    validateWeekPlan(week)
    const where = buildWhere({ weekPlan: week })
    const total = countSummaries(this.m.db, where)
    const result = querySummaries(this.m.db, {
      where,
      spec: sortSpecFor('deadline_asc'),
      limit: clamp(limit),
      cursor: cursor ?? ''
    })
    return { items: result.items, nextCursor: result.nextCursor, total }
  }

  /**
   * Quick-capture projection: live, unarchived top-level tasks in the
   * leftmost non-terminal column (the `TaskService.create` default).
   */
  inbox(limit?: number, cursor?: string): TaskPage {
    const column = this.m.seededTx((tx) => defaultColumnName(tx))
    if (column === undefined) return { items: [], nextCursor: null, total: 0 }
    const where = buildWhere({ column, onlyTopLevel: true })
    const total = countSummaries(this.m.db, where)
    const result = querySummaries(this.m.db, {
      where,
      spec: sortSpecFor('position_asc'),
      limit: clamp(limit),
      cursor: cursor ?? ''
    })
    return { items: result.items, nextCursor: result.nextCursor, total }
  }
}
