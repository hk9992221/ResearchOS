import type { ActivityActor, KanbanTask, TaskColumnInfo } from '@researchos/shared'

import type { Db } from './deps'
import type { KanbanModule } from './module'
import { columnState, quoteTitle } from './module'
import { getColumn, getTaskRow, mapTask, planInsert, taskDomain, type Placement } from './repo'
import { LIMITS } from './types'
import { anchorError, invalid, notFound } from './validate'

export interface MoveTarget {
  column: string
  beforeId?: string | null
  afterId?: string | null
}

/**
 * Rules 3/4: the single drag/move engine shared by Move,
 * Update's column delegation and Complete. Anchors must sit in the target
 * column **and** inside the mover's parent domain (defect 10); the resulting
 * status.changed is emitted only when the column actually changes.
 */
export function applyMove(
  m: KanbanModule,
  tx: Db,
  actor: ActivityActor,
  task: KanbanTask,
  target: MoveTarget
): KanbanTask {
  const destination = getColumn(tx, target.column)
  if (destination === undefined) throw notFound(`column ${target.column} not found`, { column: target.column })

  const beforeId = target.beforeId ?? null
  const afterId = target.afterId ?? null
  if (beforeId !== null && afterId !== null) {
    throw invalid('before_id and after_id are mutually exclusive', { field: 'beforeId' })
  }
  const anchorId = beforeId ?? afterId ?? ''
  if (anchorId !== '') {
    if (anchorId === task.id) throw invalid('anchor is the moved task itself', { field: 'anchorId' })
    const row = getTaskRow(tx, anchorId)
    if (row === undefined || row.deleted_at !== null) throw anchorError(anchorId, target.column, task.parentTaskId)
    const anchor = mapTask(row)
    if (anchor.column !== target.column || anchor.parentTaskId !== task.parentTaskId) {
      throw anchorError(anchorId, target.column, task.parentTaskId)
    }
  }

  const domain = taskDomain(tx, target.column, task.parentTaskId, task.id)
  let plan: Placement
  try {
    plan = planInsert(domain, task.id, anchorId, beforeId !== null)
  } catch {
    throw anchorError(anchorId, target.column, task.parentTaskId)
  }

  if (plan.ordered !== undefined) {
    // Gap exhausted: rewrite the domain with the 1024 step. Only sort_order
    // changes, so no other task's concurrency token moves.
    const updateSort = tx.prepare('UPDATE tasks SET sort_order = ? WHERE id = ?')
    for (const slot of plan.ordered) {
      if (slot.id === task.id) continue
      updateSort.run(slot.sort, slot.id)
    }
  }

  const columnChanged = destination.name !== task.column
  const from: TaskColumnInfo =
    getColumn(tx, task.column) ?? { name: task.column, kind: 'custom', isDone: false, position: 0 }

  const set: string[] = ['sort_order = ?', 'updated_at = ?']
  const args: Array<string | number | null> = [plan.sort, m.now()]
  if (columnChanged) {
    set.push('"column" = ?')
    args.push(destination.name)
    if (!from.isDone && destination.isDone && task.completedAt === null) {
      set.push('completed_at = ?')
      args.push(m.now())
    } else if (from.isDone && !destination.isDone) {
      set.push('completed_at = NULL')
    }
  }
  tx.prepare(`UPDATE tasks SET ${set.join(', ')} WHERE id = ?`).run(...args, task.id)

  if (columnChanged) {
    const moved = m.readTask(tx, task.id)
    const next = columnState(destination)
    let summary = `${quoteTitle(task.title)}：${from.name} → ${destination.name}`
    if (!from.isDone && destination.isDone) {
      next.completed_at = moved.completedAt
      summary = `完成任务${quoteTitle(task.title)}`
    } else if (from.isDone && !destination.isDone) {
      next.completed_at = null
      summary = `重新打开任务${quoteTitle(task.title)}`
    }
    m.emit(tx, {
      type: 'status.changed',
      entityType: 'task',
      entityId: task.id,
      old: columnState(from),
      new: next,
      summary,
      actor
    })
  }

  return m.readTask(tx, task.id)
}

/** Domain step constant, re-exported for callers building domains. */
export const SORT_STEP = LIMITS.sortStep