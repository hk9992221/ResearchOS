import type { ActivityActor, ColumnCreateInput, TaskColumnInfo } from '@researchos/shared'

import type { Db } from './deps'
import type { KanbanModule } from './module'
import { COLUMN_KINDS, LIMITS, type TaskRow } from './types'
import {
  TASK_COLUMNS,
  countColumns,
  getColumn,
  listColumns,
  mapTask,
  nextTaskSort,
  otherDoneColumns
} from './repo'
import { invalid, notFound, validateColumnName } from './validate'

/** Custom status columns. Column management writes no Activity. */
export class ColumnService {
  constructor(private readonly m: KanbanModule) {}

  list(): TaskColumnInfo[] {
    return this.m.seededTx((tx) => listColumns(tx))
  }

  create(actor: ActivityActor, input: ColumnCreateInput): TaskColumnInfo {
    void actor
    this.m.assertWritable()
    const name = validateColumnName(input.name)
    const kind = input.kind ?? 'custom'
    if (!COLUMN_KINDS.includes(kind)) throw invalid(`column kind ${kind}`, { field: 'kind' })

    return this.m.seededTx((tx) => {
      if (getColumn(tx, name) !== undefined) {
        throw invalid(`duplicate column name: ${name}`, { field: 'name', column: name })
      }
      const order = listColumns(tx)
      let insertAt = order.length
      if (input.after !== undefined) {
        const index = order.findIndex((column) => column.name === input.after)
        if (index < 0) throw notFound(`column ${input.after} not found`, { column: input.after })
        insertAt = index + 1
      }
      const names = [...order.map((column) => column.name)]
      names.splice(insertAt, 0, name)
      const at = this.m.now()
      tx.prepare(
        'INSERT INTO task_columns(name, kind, is_done, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)'
      ).run(name, kind, input.isDone === true ? 1 : 0, 0, at, at)
      this.renumber(tx, names)
      const created = getColumn(tx, name)
      if (created === undefined) throw notFound(`column ${name} vanished`, { column: name })
      return created
    })
  }

  /** Renames the definition and cascades `tasks."column"` (all rows, incl. deleted). */
  rename(actor: ActivityActor, oldName: string, newName: string): TaskColumnInfo {
    void actor
    this.m.assertWritable()
    const from = oldName.trim()
    const to = validateColumnName(newName)
    if (from === to) throw invalid('new name equals old name', { field: 'newName' })

    return this.m.tx((tx) => {
      if (getColumn(tx, from) === undefined) throw notFound(`column ${from} not found`, { column: from })
      if (getColumn(tx, to) !== undefined) {
        throw invalid(`duplicate column name: ${to}`, { field: 'newName', column: to })
      }
      // Cards first: the intermediate state never points at a missing column.
      tx.prepare('UPDATE tasks SET "column" = ? WHERE "column" = ?').run(to, from)
      tx.prepare('UPDATE task_columns SET name = ?, updated_at = ? WHERE name = ?').run(to, this.m.now(), from)
      const renamed = getColumn(tx, to)
      if (renamed === undefined) throw notFound(`column ${to} vanished`, { column: to })
      return renamed
    })
  }

  /** 0-based target index, clamped; re-sequences every position with the 1024 step. */
  move(actor: ActivityActor, name: string, toPosition: number): TaskColumnInfo[] {
    void actor
    this.m.assertWritable()
    const target = name.trim()

    return this.m.seededTx((tx) => {
      const order = listColumns(tx)
      const index = order.findIndex((column) => column.name === target)
      if (index < 0) throw notFound(`column ${target} not found`, { column: target })
      const names = order.map((column) => column.name)
      names.splice(index, 1)
      const clamped = Math.max(0, Math.min(Math.trunc(toPosition), names.length))
      names.splice(clamped, 0, target)
      this.renumber(tx, names)
      return listColumns(tx)
    })
  }

  /** Mark/unmark a terminal column. The last done column cannot be unmarked. */
  setDone(actor: ActivityActor, name: string, done: boolean): TaskColumnInfo {
    void actor
    this.m.assertWritable()
    const target = name.trim()

    return this.m.seededTx((tx) => {
      const column = getColumn(tx, target)
      if (column === undefined) throw notFound(`column ${target} not found`, { column: target })
      if (column.isDone === done) return column
      if (!done && otherDoneColumns(tx, target) === 0) {
        throw invalid('at least one done column is required', { column: target, reason: 'last-done-column' })
      }
      tx.prepare('UPDATE task_columns SET is_done = ?, updated_at = ? WHERE name = ?').run(
        done ? 1 : 0,
        this.m.now(),
        target
      )
      const updated = getColumn(tx, target)
      if (updated === undefined) throw notFound(`column ${target} vanished`, { column: target })
      return updated
    })
  }

  /**
   * Deletes a column after migrating its live tasks into `migrateTo`, appended
   * per parent domain in their original order. Structural migration: no
   * per-task events and no `updated_at` rewrite. Guards: last column, last
   * done column, non-empty without a target.
   */
  delete(actor: ActivityActor, name: string, migrateTo: string): { deleted: boolean; movedTasks: number } {
    void actor
    this.m.assertWritable()
    const target = name.trim()
    const destination = migrateTo.trim()

    return this.m.seededTx((tx) => {
      const column = getColumn(tx, target)
      if (column === undefined) throw notFound(`column ${target} not found`, { column: target })
      if (countColumns(tx) <= 1) {
        throw invalid('cannot delete the last column', { column: target, reason: 'last-column' })
      }
      if (column.isDone && otherDoneColumns(tx, target) === 0) {
        throw invalid('at least one done column is required', { column: target, reason: 'last-done-column' })
      }

      const movers = tx
        .prepare(`SELECT ${TASK_COLUMNS} FROM tasks WHERE "column" = ? AND deleted_at IS NULL ORDER BY sort_order, id`)
        .all(target) as TaskRow[]

      if (movers.length > 0 && destination === '') {
        throw invalid(`column ${target} still holds ${movers.length} tasks; migrate_to is required`, {
          column: target,
          taskCount: movers.length,
          reason: 'column-not-empty'
        })
      }
      if (destination !== '') {
        if (destination === target) throw invalid('migration target is the deleted column', { field: 'migrateTo' })
        if (getColumn(tx, destination) === undefined) {
          throw notFound(`column ${destination} not found`, { column: destination })
        }
      }
      if (movers.length > 0 && destination !== '') {
        this.migrate(tx, column, destination, movers)
      }
      tx.prepare('DELETE FROM task_columns WHERE name = ?').run(target)
      return { deleted: true, movedTasks: movers.length }
    })
  }

  private migrate(tx: Db, from: TaskColumnInfo, destination: string, rows: readonly TaskRow[]): void {
    const to = getColumn(tx, destination)
    if (to === undefined) throw notFound(`column ${destination} not found`, { column: destination })
    const cursors = new Map<string | null, number>()
    const update = tx.prepare('UPDATE tasks SET "column" = ?, sort_order = ?, completed_at = ? WHERE id = ?')
    for (const row of rows) {
      const task = mapTask(row)
      let cursor = cursors.get(task.parentTaskId)
      if (cursor === undefined) {
        cursor = nextTaskSort(tx, destination, task.parentTaskId)
      }
      cursors.set(task.parentTaskId, cursor + LIMITS.sortStep)

      let completedAt = task.completedAt
      if (!from.isDone && to.isDone && completedAt === null) completedAt = this.m.now()
      if (from.isDone && !to.isDone) completedAt = null

      update.run(destination, cursor, completedAt, task.id)
    }
  }

  private renumber(tx: Db, orderedNames: readonly string[]): void {
    const at = this.m.now()
    const update = tx.prepare('UPDATE task_columns SET position = ?, updated_at = ? WHERE name = ?')
    orderedNames.forEach((name, index) => {
      update.run((index + 1) * LIMITS.sortStep, at, name)
    })
  }
}
