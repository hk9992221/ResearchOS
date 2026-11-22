import { AppError, AppErrorCode, type ActivityActor, type KanbanTask } from '@researchos/shared'
import type {
  TaskColumnInfo,
  TaskCreateInput,
  TaskDetail,
  TaskListQuery,
  TaskMoveInput,
  TaskPage,
  TaskPatch,
  TaskUpdateInput
} from '@researchos/shared'

import type { Db } from './deps'
import type { KanbanModule } from './module'
import { columnState, quoteTitle } from './module'
import { applyMove } from './move'
import {
  buildWhere,
  countActiveTasks,
  countSummaries,
  defaultColumnName,
  getColumn,
  getTaskRow,
  leftmostDoneColumn,
  listChecklistRows,
  nextTaskSort,
  querySummaries,
  sortSpecFor,
  type SqlValue,
  type TaskFilter
} from './repo'
import { LIMITS } from './types'
import {
  conflict,
  invalid,
  notFound,
  sanitizeLabels,
  validateDeadline,
  validateDescription,
  validatePriority,
  validateTitle,
  validateWeekPlan,
  assertTaskId,
  limitExceeded
} from './validate'

function clampLimit(limit: number | undefined): number {
  if (limit === undefined || limit <= 0) return LIMITS.listDefaultLimit
  return Math.min(limit, LIMITS.listMaxLimit)
}

/** Query shape accepted by the service (projectId is bound by the handle). */
export type TaskListQueryInput = Omit<TaskListQuery, 'projectId'>

/** Task services. */
export class TaskService {
  constructor(private readonly m: KanbanModule) {}

  /* ------------------------------------------------------------- create */

  create(actor: ActivityActor, input: TaskCreateInput): KanbanTask {
    const title = validateTitle(input.title)
    const description = input.descriptionMd ?? ''
    validateDescription(description)
    const priority = input.priority ?? 'none'
    validatePriority(priority)
    if (input.deadline !== undefined && input.deadline !== null) validateDeadline(input.deadline)
    if (input.weekPlan !== undefined && input.weekPlan !== null) validateWeekPlan(input.weekPlan)
    const labels = sanitizeLabels(input.labels ?? [])
    if (input.parentTaskId !== undefined) assertTaskId(input.parentTaskId, 'parent task id')
    this.m.assertWritable()

    return this.m.seededTx((tx) => {
      const column =
        input.column !== undefined && input.column !== ''
          ? input.column
          : defaultColumnName(tx)
      if (column === undefined) throw notFound('no columns configured', { reason: 'no-columns' })
      const columnRecord = getColumn(tx, column)
      if (columnRecord === undefined) throw notFound(`column ${column} not found`, { column })

      if (input.parentTaskId !== undefined) {
        const parent = getTaskRow(tx, input.parentTaskId)
        if (parent === undefined || parent.deleted_at !== null || parent.archived_at !== null) {
          throw invalid(`parent task ${input.parentTaskId} is missing, deleted or archived`, {
            field: 'parentTaskId',
            parentId: input.parentTaskId
          })
        }
        if (parent.parent_task_id !== null) {
          throw invalid('subtasks cannot nest more than two levels', {
            field: 'parentTaskId',
            reason: 'subtask-on-subtask'
          })
        }
        const children = tx
          .prepare('SELECT count(*) AS n FROM tasks WHERE parent_task_id = ? AND deleted_at IS NULL')
          .get(input.parentTaskId) as { n: number }
        if (children.n >= LIMITS.subtasksPerParent) {
          throw invalid(`parent already has ${LIMITS.subtasksPerParent} subtasks`, {
            field: 'parentTaskId',
            reason: 'subtask-limit'
          })
        }
      }

      const active = countActiveTasks(tx)
      if (active >= LIMITS.activeTasks) {
        throw limitExceeded(`active task limit of ${LIMITS.activeTasks} reached`, {
          limit: LIMITS.activeTasks,
          current: active
        })
      }

      const sortOrder = nextTaskSort(tx, column, input.parentTaskId ?? null)
      const at = this.m.now()
      const id = this.m.newId()
      const completedAt = columnRecord.isDone ? at : null
      tx.prepare(
        'INSERT INTO tasks(id, title, "column", parent_task_id, description_md, priority, deadline, blocked, ' +
          'labels, week_plan, sort_order, completed_at, archived_at, created_at, updated_at) ' +
          'VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, NULL, ?, ?)'
      ).run(
        id,
        title,
        column,
        input.parentTaskId ?? null,
        description,
        priority === 'none' ? null : priority,
        input.deadline ?? null,
        JSON.stringify(labels),
        input.weekPlan ?? null,
        sortOrder,
        completedAt,
        at,
        at
      )

      const created = this.m.readTask(tx, id)
      this.m.emit(tx, this.m.createdEvent(actor, created))
      return created
    })
  }

  /* ---------------------------------------------------------------- get */

  get(id: string): TaskDetail {
    assertTaskId(id)
    const row = getTaskRow(this.m.db, id)
    if (row === undefined) throw notFound(`task ${id} not found`, { taskId: id })
    const task = this.m.readTask(this.m.db, id)
    const spec = sortSpecFor('position_asc')
    const subtasks = querySummaries(this.m.db, {
      where: buildWhere({ parentId: id }),
      spec,
      limit: LIMITS.subtasksPerParent
    })
    const column = getColumn(this.m.db, task.column) ?? {
      name: task.column,
      kind: 'custom' as const,
      isDone: false,
      position: 0
    }
    return {
      ...task,
      columnInfo: column,
      subtasks: subtasks.items,
      checklist: listChecklistRows(this.m.db, id),
      links: this.m.links.list(id)
    }
  }

  /* --------------------------------------------------------------- list */

  list(query: TaskListQueryInput): TaskPage {
    const page = this.page(query)
    return page
  }

  listArchived(query: TaskListQueryInput): TaskPage {
    return this.page({ ...query, includeArchived: true, includeDeleted: false }, true)
  }

  private page(query: TaskListQueryInput, archivedOnly = false): TaskPage {
    const sort = query.sort ?? 'position_asc'
    const spec = sortSpecFor(sort)
    if (query.parentId !== undefined) assertTaskId(query.parentId, 'parent task id')
    if (query.column !== undefined && query.column === '') {
      throw invalid('empty column filter', { field: 'column' })
    }
    const filter: TaskFilter = {
      column: query.column,
      parentId: query.parentId,
      onlyTopLevel: query.onlyTopLevel,
      priority: query.priority,
      labels: query.labels,
      deadlineFrom: query.deadlineFrom,
      deadlineTo: query.deadlineTo,
      blocked: query.blocked,
      weekPlan: query.weekPlan,
      text: query.q,
      includeArchived: query.includeArchived === true || archivedOnly,
      includeDeleted: query.includeDeleted === true
    }
    const where = buildWhere(filter)
    if (archivedOnly) {
      where.where.push('t.archived_at IS NOT NULL')
    }
    const total = countSummaries(this.m.db, where)
    const result = querySummaries(this.m.db, {
      where,
      spec,
      limit: clampLimit(query.limit),
      cursor: query.cursor ?? ''
    })
    return { items: result.items, nextCursor: result.nextCursor, total }
  }

  /* ------------------------------------------------------------- update */

  update(actor: ActivityActor, input: TaskUpdateInput): KanbanTask {
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, input.id)
    if (input.expectedUpdatedAt !== undefined && input.expectedUpdatedAt !== task.updatedAt) {
      throw conflict('task was modified since it was read', {
        taskId: task.id,
        expectedUpdatedAt: input.expectedUpdatedAt,
        current: task.updatedAt
      })
    }

    const patch = input.patch
    const title = patch.title === undefined ? undefined : validateTitle(patch.title)
    if (patch.descriptionMd !== undefined) validateDescription(patch.descriptionMd)
    if (patch.priority !== undefined) validatePriority(patch.priority)
    // `undefined` = leave unchanged; `null` or `''` = clear.
    const deadline = patch.deadline === undefined ? undefined : patch.deadline || null
    if (deadline !== undefined && deadline !== null) validateDeadline(deadline)
    const weekPlan = patch.weekPlan === undefined ? undefined : patch.weekPlan || null
    if (weekPlan !== undefined && weekPlan !== null) validateWeekPlan(weekPlan)
    const labels = patch.labels === undefined ? undefined : sanitizeLabels(patch.labels)
    if (patch.column !== undefined && patch.column.trim() === '') {
      throw invalid('empty column', { field: 'column' })
    }

    const newColumn = patch.column !== undefined && patch.column !== task.column ? patch.column : undefined
    const blockedTo = patch.blocked !== undefined && patch.blocked !== task.blocked ? patch.blocked : undefined
    const hasFieldEdit =
      title !== undefined ||
      patch.descriptionMd !== undefined ||
      patch.priority !== undefined ||
      deadline !== undefined ||
      weekPlan !== undefined ||
      labels !== undefined
    if (!hasFieldEdit && blockedTo === undefined && newColumn === undefined) return task

    return this.m.seededTx((tx) => {
      const from = getColumn(tx, task.column) ?? {
        name: task.column,
        kind: 'custom' as const,
        isDone: false,
        position: 0
      }
      let to: TaskColumnInfo | undefined
      let sortOrder: number | undefined
      if (newColumn !== undefined) {
        to = getColumn(tx, newColumn)
        if (to === undefined) throw notFound(`column ${newColumn} not found`, { column: newColumn })
        sortOrder = nextTaskSort(tx, newColumn, task.parentTaskId)
      }

      const set: string[] = []
      const args: SqlValue[] = []
      if (title !== undefined) {
        set.push('title = ?')
        args.push(title)
      }
      if (patch.descriptionMd !== undefined) {
        set.push('description_md = ?')
        args.push(patch.descriptionMd)
      }
      if (patch.priority !== undefined) {
        set.push('priority = ?')
        args.push(patch.priority === 'none' ? null : patch.priority)
      }
      if (deadline !== undefined) {
        set.push('deadline = ?')
        args.push(deadline)
      }
      if (weekPlan !== undefined) {
        set.push('week_plan = ?')
        args.push(weekPlan)
      }
      if (labels !== undefined) {
        set.push('labels = ?')
        args.push(JSON.stringify(labels))
      }
      if (blockedTo !== undefined) {
        set.push('blocked = ?')
        args.push(blockedTo ? 1 : 0)
      }
      if (newColumn !== undefined && to !== undefined && sortOrder !== undefined) {
        set.push('"column" = ?', 'sort_order = ?')
        args.push(newColumn, sortOrder)
        if (!from.isDone && to.isDone && task.completedAt === null) {
          set.push('completed_at = ?')
          args.push(this.m.now())
        } else if (from.isDone && !to.isDone) {
          set.push('completed_at = NULL')
        }
      }
      set.push('updated_at = ?')
      args.push(this.m.now())

      let where = 'WHERE id = ? AND deleted_at IS NULL'
      const updateArgs: SqlValue[] = [...args, task.id]
      if (input.expectedUpdatedAt !== undefined) {
        where += ' AND updated_at = ?'
        updateArgs.push(input.expectedUpdatedAt)
      }
      const info = tx.prepare(`UPDATE tasks SET ${set.join(', ')} ${where}`).run(...updateArgs)
      if (info.changes === 0) {
        if (input.expectedUpdatedAt !== undefined) {
          throw conflict('task was modified since it was read', {
            taskId: task.id,
            expectedUpdatedAt: input.expectedUpdatedAt
          })
        }
        throw notFound(`task ${task.id} not found`, { taskId: task.id })
      }

      if (newColumn !== undefined && to !== undefined) {
        this.emitColumnMoved(tx, actor, task, from, to)
      }
      if (blockedTo !== undefined) {
        this.m.emit(tx, {
          type: 'status.changed',
          entityType: 'task',
          entityId: task.id,
          old: { blocked: !blockedTo },
          new: { blocked: blockedTo },
          summary: blockedTo
            ? `任务${quoteTitle(task.title)}标记为受阻`
            : `任务${quoteTitle(task.title)}解除受阻`,
          actor: this.m.actor(actor)
        })
      }
      return this.m.readTask(tx, task.id)
    })
  }

  /* --------------------------------------------------------------- move */

  move(actor: ActivityActor, input: TaskMoveInput): KanbanTask {
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, input.id)
    if (input.column === undefined || input.column === '') {
      throw invalid('target column is required', { field: 'column' })
    }
    return this.m.seededTx((tx) =>
      applyMove(this.m, tx, this.m.actor(actor), task, {
        column: input.column,
        beforeId: input.beforeId ?? null,
        afterId: input.afterId ?? null
      })
    )
  }

  /** Writes or clears week_plan. Plain field edit: no Activity. */
  setWeekPlan(actor: ActivityActor, id: string, week: string | null, expected?: string): KanbanTask {
    void actor
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, id)
    if (week !== null && week !== '') validateWeekPlan(week)
    if (expected !== undefined && expected !== task.updatedAt) {
      throw conflict('task was modified since it was read', { taskId: id, expectedUpdatedAt: expected })
    }
    const value = week === '' ? null : week
    return this.m.tx((tx) => {
      const set = 'UPDATE tasks SET week_plan = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL'
      const args: SqlValue[] = [value, this.m.now(), id]
      let sql = set
      if (expected !== undefined) {
        sql += ' AND updated_at = ?'
        args.push(expected)
      }
      const info = tx.prepare(sql).run(...args)
      if (info.changes === 0) {
        if (expected !== undefined) throw conflict('task was modified since it was read', { taskId: id })
        throw notFound(`task ${id} not found`, { taskId: id })
      }
      return this.m.readTask(tx, id)
    })
  }

  /**
   * `done:true` appends to the leftmost done column; `done:false` reopens it
   * into the leftmost non-terminal column. Already in that state → unchanged,
   * no event (AC-27).
   */
  complete(actor: ActivityActor, id: string, done: boolean): KanbanTask {
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, id)
    return this.m.seededTx((tx) => {
      const current = getColumn(tx, task.column)
      const terminal = current?.isDone === true
      if (done === terminal) return task

      const target = done
        ? leftmostDoneColumn(tx)
        : (tx.prepare('SELECT name FROM task_columns WHERE is_done = 0 ORDER BY position ASC LIMIT 1').get() as
            | { name: string }
            | undefined)?.name
      if (target === undefined) {
        throw notFound(done ? 'no done column configured' : 'no non-terminal column to reopen into', {
          reason: done ? 'no-done-column' : 'no-open-column'
        })
      }
      return applyMove(this.m, tx, this.m.actor(actor), task, { column: target })
    })
  }

  /* ------------------------------------------------------- archive cycle */

  archive(actor: ActivityActor, id: string): KanbanTask {
    assertTaskId(id)
    this.m.assertWritable()
    const row = getTaskRow(this.m.db, id)
    if (row === undefined || row.deleted_at !== null) throw notFound(`task ${id} not found`, { taskId: id })
    const task = this.m.readTask(this.m.db, id)
    if (task.archivedAt !== null) return task
    if (task.parentTaskId !== null) {
      const parent = getTaskRow(this.m.db, task.parentTaskId)
      if (parent !== undefined && parent.archived_at !== null) {
        throw conflict(`parent task ${task.parentTaskId} is archived`, {
          taskId: id,
          reason: 'parent-archived'
        })
      }
    }
    return this.setArchived(actor, task, true)
  }

  unarchive(actor: ActivityActor, id: string): KanbanTask {
    assertTaskId(id)
    this.m.assertWritable()
    const row = getTaskRow(this.m.db, id)
    if (row === undefined || row.deleted_at !== null) throw notFound(`task ${id} not found`, { taskId: id })
    const task = this.m.readTask(this.m.db, id)
    if (task.archivedAt === null) return task
    return this.setArchived(actor, task, false)
  }

  /** Archiving keeps column, sort_order and completed_at (TASK-10). */
  private setArchived(actor: ActivityActor, task: KanbanTask, archived: boolean): KanbanTask {
    return this.m.tx((tx) => {
      const at = this.m.now()
      const info = tx
        .prepare('UPDATE tasks SET archived_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL')
        .run(archived ? at : null, at, task.id)
      if (info.changes === 0) throw notFound(`task ${task.id} not found`, { taskId: task.id })
      this.m.emit(tx, {
        type: 'status.changed',
        entityType: 'task',
        entityId: task.id,
        old: { archived: !archived },
        new: { archived },
        summary: archived ? `归档任务${quoteTitle(task.title)}` : `取消归档任务${quoteTitle(task.title)}`,
        actor: this.m.actor(actor)
      })
      return this.m.readTask(tx, task.id)
    })
  }

  /* ------------------------------------------------------- trash cycle */

  /** Soft delete; a parent cascades every live subtask (each its own event). */
  softDelete(actor: ActivityActor, id: string): void {
    assertTaskId(id)
    this.m.assertWritable()
    const row = getTaskRow(this.m.db, id)
    if (row === undefined) throw notFound(`task ${id} not found`, { taskId: id })
    if (row.deleted_at !== null) return
    const trash = this.m.deps.trash
    if (trash === undefined) throw missingPort('trash')

    const task = this.m.readTask(this.m.db, id)
    const actorValue = this.m.actor(actor)
    this.m.tx((tx) => {
      if (task.parentTaskId === null) {
        const children = tx
          .prepare('SELECT id FROM tasks WHERE parent_task_id = ? AND deleted_at IS NULL ORDER BY sort_order, id')
          .all(id) as Array<{ id: string }>
        for (const child of children) {
          trash.softDelete({ db: tx }, { type: 'task', id: child.id }, actorValue)
        }
      }
      trash.softDelete({ db: tx }, { type: 'task', id }, actorValue)
    })
  }

  /** Restore; a subtask whose parent is still trashed is rejected. */
  restore(actor: ActivityActor, id: string): KanbanTask {
    assertTaskId(id)
    this.m.assertWritable()
    const row = getTaskRow(this.m.db, id)
    if (row === undefined) throw notFound(`task ${id} not found`, { taskId: id })
    if (row.deleted_at === null) return this.m.readTask(this.m.db, id)
    const task = this.m.readTask(this.m.db, id)
    if (task.parentTaskId !== null) {
      const parent = getTaskRow(this.m.db, task.parentTaskId)
      if (parent === undefined || parent.deleted_at !== null) {
        throw invalid(`parent ${task.parentTaskId} is still deleted`, {
          field: 'parentTaskId',
          reason: 'parent-deleted'
        })
      }
    }
    const trash = this.m.deps.trash
    if (trash === undefined) throw missingPort('trash')
    const actorValue = this.m.actor(actor)
    this.m.tx((tx) => {
      trash.restore({ db: tx }, { type: 'task', id }, actorValue)
      if (task.parentTaskId === null) {
        const children = tx
          .prepare('SELECT id FROM tasks WHERE parent_task_id = ? AND deleted_at IS NOT NULL ORDER BY sort_order, id')
          .all(id) as Array<{ id: string }>
        for (const child of children) {
          trash.restore({ db: tx }, { type: 'task', id: child.id }, actorValue)
        }
      }
    })
    return this.m.readTask(this.m.db, id)
  }

  /* -------------------------------------------------------------- events */

  private emitColumnMoved(
    tx: Db,
    actor: ActivityActor,
    task: KanbanTask,
    from: TaskColumnInfo,
    to: TaskColumnInfo
  ): void {
    const moved = this.m.readTask(tx, task.id)
    const next = columnState(to)
    let summary = `${quoteTitle(task.title)}：${from.name} → ${to.name}`
    if (!from.isDone && to.isDone) {
      next.completed_at = moved.completedAt
      summary = `完成任务${quoteTitle(task.title)}`
    } else if (from.isDone && !to.isDone) {
      next.completed_at = null
      summary = `重新打开任务${quoteTitle(task.title)}`
    }
    this.m.emit(tx, {
      type: 'status.changed',
      entityType: 'task',
      entityId: task.id,
      old: columnState(from),
      new: next,
      summary,
      actor: this.m.actor(actor)
    })
  }
}

function missingPort(name: string): AppError {
  return new AppError(AppErrorCode.Internal, `kanban: ${name} service is not configured`, { port: name })
}
