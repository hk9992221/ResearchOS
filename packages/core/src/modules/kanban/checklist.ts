import type { ActivityActor, ChecklistItem } from '@researchos/shared'

import type { KanbanModule } from './module'
import {
  checklistDomain,
  countChecklist,
  getChecklistRow,
  getTaskRow,
  listChecklistRows,
  mapChecklist,
  planInsert
} from './repo'
import { LIMITS } from './types'
import { assertTaskId, invalid, notFound, validateChecklistText } from './validate'

/**
 * Editing-only checklist. Never writes Activity, never touches
 * the parent task's `updated_at` (a checkbox click cannot invalidate an
 * in-flight task edit), physical delete only (no trash).
 */
export class ChecklistService {
  constructor(private readonly m: KanbanModule) {}

  list(taskId: string): ChecklistItem[] {
    assertTaskId(taskId)
    if (getTaskRow(this.m.db, taskId) === undefined) throw notFound(`task ${taskId} not found`, { taskId })
    return listChecklistRows(this.m.db, taskId)
  }

  add(actor: ActivityActor, taskId: string, text: string, beforeId?: string): ChecklistItem {
    void actor
    this.m.assertWritable()
    this.m.loadGateTask(this.m.db, taskId)
    const clean = validateChecklistText(text)
    if (beforeId !== undefined) assertTaskId(beforeId, 'checklist anchor id')

    return this.m.tx((tx) => {
      const count = countChecklist(tx, taskId)
      if (count >= LIMITS.checklistPerTask) {
        throw invalid(`task already holds ${LIMITS.checklistPerTask} checklist items`, {
          taskId,
          reason: 'checklist-limit'
        })
      }
      const domain = checklistDomain(tx, taskId, '')
      let anchorId = ''
      if (beforeId !== undefined) {
        const anchor = getChecklistRow(tx, beforeId)
        if (anchor === undefined || anchor.task_id !== taskId) {
          throw invalid(`checklist anchor ${beforeId} is not on task ${taskId}`, { field: 'beforeId' })
        }
        anchorId = beforeId
      }
      const plan = planInsert(domain, '', anchorId, true)
      if (plan.ordered !== undefined) {
        const update = tx.prepare('UPDATE task_checklist_items SET sort_order = ? WHERE id = ?')
        for (const slot of plan.ordered) {
          if (slot.id === '') continue
          update.run(slot.sort, slot.id)
        }
      }
      const at = this.m.now()
      const id = this.m.newId()
      tx.prepare(
        'INSERT INTO task_checklist_items(id, task_id, text, done, sort_order, created_at, updated_at) ' +
          'VALUES (?, ?, ?, 0, ?, ?, ?)'
      ).run(id, taskId, clean, plan.sort, at, at)
      const row = getChecklistRow(tx, id)
      if (row === undefined) throw notFound(`checklist item ${id} vanished`)
      return mapChecklist(row)
    })
  }

  update(actor: ActivityActor, itemId: string, text: string): ChecklistItem {
    void actor
    this.m.assertWritable()
    const item = this.gateItem(itemId)
    const clean = validateChecklistText(text)
    if (clean === item.text) return item
    this.m.tx((tx) => {
      tx.prepare('UPDATE task_checklist_items SET text = ?, updated_at = ? WHERE id = ?').run(
        clean,
        this.m.now(),
        item.id
      )
    })
    return { ...item, text: clean }
  }

  setDone(actor: ActivityActor, itemId: string, done: boolean): ChecklistItem {
    void actor
    this.m.assertWritable()
    const item = this.gateItem(itemId)
    if (item.done === done) return item
    this.m.tx((tx) => {
      tx.prepare('UPDATE task_checklist_items SET done = ?, updated_at = ? WHERE id = ?').run(
        done ? 1 : 0,
        this.m.now(),
        item.id
      )
    })
    return { ...item, done }
  }

  /** Physical delete: the one editing write that is not in the trash (O-7). */
  remove(actor: ActivityActor, itemId: string): void {
    void actor
    this.m.assertWritable()
    const item = this.gateItem(itemId)
    this.m.tx((tx) => {
      tx.prepare('DELETE FROM task_checklist_items WHERE id = ?').run(item.id)
    })
  }

  /** `itemIds` must be a permutation of the task's current items. */
  reorder(actor: ActivityActor, taskId: string, itemIds: readonly string[]): ChecklistItem[] {
    void actor
    this.m.assertWritable()
    this.m.loadGateTask(this.m.db, taskId)
    const seen = new Set<string>()
    for (const id of itemIds) {
      assertTaskId(id, 'checklist item id')
      if (seen.has(id)) throw invalid(`duplicate checklist item id ${id}`, { field: 'itemIds' })
      seen.add(id)
    }

    return this.m.tx((tx) => {
      const current = listChecklistRows(tx, taskId)
      if (current.length !== itemIds.length) {
        throw invalid(`reorder covers ${itemIds.length} of ${current.length} items`, {
          taskId,
          reason: 'reorder-incomplete'
        })
      }
      for (const item of current) {
        if (!seen.has(item.id)) {
          throw invalid(`checklist item ${item.id} is not on task ${taskId}`, { field: 'itemIds' })
        }
      }
      const at = this.m.now()
      const update = tx.prepare('UPDATE task_checklist_items SET sort_order = ?, updated_at = ? WHERE id = ?')
      itemIds.forEach((id, index) => {
        update.run((index + 1) * LIMITS.sortStep, at, id)
      })
      return listChecklistRows(tx, taskId)
    })
  }

  /** Loads an item and gates its owning task (alive, unarchived, parent alive). */
  private gateItem(itemId: string): ChecklistItem {
    assertTaskId(itemId, 'checklist item id')
    const row = getChecklistRow(this.m.db, itemId)
    if (row === undefined) throw notFound(`checklist item ${itemId} not found`, { itemId })
    const item = mapChecklist(row)
    this.m.loadGateTask(this.m.db, item.taskId)
    return item
  }
}