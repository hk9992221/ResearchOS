import type { ActivityActor, KanbanTask, TaskLabelCount } from '@researchos/shared'

import type { KanbanModule } from './module'
import { liveLabelRows, mapTask, getTaskRow } from './repo'
import { decodeLabels, validateLabel, sanitizeLabels, conflict, notFound } from './validate'
import type { SqlValue } from './repo'

/**
 * Labels live inside `tasks.labels` only; there is no global
 * dictionary. Comparison is case-sensitive and label edits emit no Activity.
 */
export class LabelService {
  constructor(private readonly m: KanbanModule) {}

  /** count DESC, label ASC over live, unarchived tasks. */
  list(): TaskLabelCount[] {
    const counts = new Map<string, number>()
    for (const raw of liveLabelRows(this.m.db)) {
      for (const label of decodeLabels(raw)) {
        counts.set(label, (counts.get(label) ?? 0) + 1)
      }
    }
    return [...counts.entries()]
      .map(([label, count]) => ({ label, count }))
      .sort((left, right) => {
        if (left.count !== right.count) return right.count - left.count
        return left.label < right.label ? -1 : 1
      })
  }

  add(actor: ActivityActor, taskId: string, label: string, expectedUpdatedAt?: string): KanbanTask {
    void actor
    const clean = validateLabel(label)
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, taskId)
    if (task.labels.includes(clean)) return task
    return this.replace(task, [...task.labels, clean], expectedUpdatedAt)
  }

  remove(actor: ActivityActor, taskId: string, label: string, expectedUpdatedAt?: string): KanbanTask {
    void actor
    const clean = validateLabel(label)
    this.m.assertWritable()
    const task = this.m.loadGateTask(this.m.db, taskId)
    if (!task.labels.includes(clean)) return task
    return this.replace(
      task,
      task.labels.filter((entry) => entry !== clean),
      expectedUpdatedAt
    )
  }

  /** Whole-group token-guarded write shared by update/add/remove. */
  private replace(task: KanbanTask, labels: readonly string[], expectedUpdatedAt?: string): KanbanTask {
    const clean = sanitizeLabels(labels)
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== task.updatedAt) {
      throw conflict('task was modified since it was read', {
        taskId: task.id,
        expectedUpdatedAt,
        current: task.updatedAt
      })
    }
    return this.m.tx((tx) => {
      const args: SqlValue[] = [JSON.stringify(clean), this.m.now(), task.id]
      let sql = 'UPDATE tasks SET labels = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL'
      if (expectedUpdatedAt !== undefined) {
        sql += ' AND updated_at = ?'
        args.push(expectedUpdatedAt)
      }
      const info = tx.prepare(sql).run(...args)
      if (info.changes === 0) {
        if (expectedUpdatedAt !== undefined) {
          throw conflict('task was modified since it was read', { taskId: task.id, expectedUpdatedAt })
        }
        throw notFound(`task ${task.id} not found`, { taskId: task.id })
      }
      const row = getTaskRow(tx, task.id)
      if (row === undefined) throw notFound(`task ${task.id} not found`, { taskId: task.id })
      return mapTask(row)
    })
  }
}