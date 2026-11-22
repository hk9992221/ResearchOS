/**
 * Kanban view model helpers.
 *
 * The drag payload is typed exactly like the Go implementation: a column can
 * only drop on a column and a task only on a task or a column. Keeping the
 * payload and the optimistic reorder here makes both behaviours testable
 * without a DOM drag gesture, and keeps the React components thin.
 */

import type { BoardView, TaskSummary } from '@researchos/shared'

export type TaskDragPayload =
  | { readonly kind: 'task'; readonly id: string; readonly column: string }
  | { readonly kind: 'column'; readonly name: string }

export interface TaskMoveTarget {
  readonly column: string
  /** Insert before this card; omitted = append to the column. */
  readonly beforeId?: string
}

/** canDrop enforces the typed-payload rule of §7.6. */
export function canDrop(payload: TaskDragPayload, target: 'task' | 'column'): boolean {
  if (payload.kind === 'column') return target === 'column'
  return target === 'task' || target === 'column'
}

function cloneBoard(board: BoardView): BoardView {
  return {
    projectId: board.projectId,
    columns: board.columns.map((column) => ({
      column: column.column,
      tasks: [...column.tasks]
    }))
  }
}

/**
 * applyOptimisticTaskMove moves one card in the cached board immediately; the
 * server write either confirms it or the cache is rolled back by the caller.
 */
export function applyOptimisticTaskMove(board: BoardView, target: TaskMoveTarget & { id: string }): BoardView {
  const next = cloneBoard(board)
  let task: TaskSummary | undefined
  for (const column of next.columns) {
    const index = column.tasks.findIndex((candidate) => candidate.id === target.id)
    if (index >= 0) {
      task = column.tasks[index]
      column.tasks.splice(index, 1)
      break
    }
  }
  if (task === undefined) return board
  const destination = next.columns.find((column) => column.column.name === target.column)
  if (destination === undefined) return board
  const moved: TaskSummary = { ...task, column: target.column }
  const before = target.beforeId === undefined
    ? -1
    : destination.tasks.findIndex((candidate) => candidate.id === target.beforeId)
  if (before >= 0) destination.tasks.splice(before, 0, moved)
  else destination.tasks.push(moved)
  return next
}

/** applyOptimisticColumnMove reorders the columns; position is 0-based. */
export function applyOptimisticColumnMove(board: BoardView, name: string, position: number): BoardView {
  const next = cloneBoard(board)
  const from = next.columns.findIndex((column) => column.column.name === name)
  if (from < 0) return board
  const clamped = Math.max(0, Math.min(position, next.columns.length - 1))
  const [column] = next.columns.splice(from, 1)
  if (column === undefined) return board
  next.columns.splice(clamped, 0, column)
  return next
}

export type TaskSortKey =
  | 'title'
  | 'column'
  | 'priority'
  | 'deadline'
  | 'labels'
  | 'subtasks'
  | 'blocked'
  | 'updated'

export type SortDirection = 'asc' | 'desc'

const PRIORITY_ORDER: Readonly<Record<string, number>> = {
  high: 0,
  medium: 1,
  low: 2,
  none: 3
}

/** sortTasks is the client-side table sort; the service sorts the page only. */
export function sortTasks(
  tasks: readonly TaskSummary[],
  key: TaskSortKey,
  direction: SortDirection
): TaskSummary[] {
  const factor = direction === 'asc' ? 1 : -1
  return [...tasks].sort((left, right) => {
    let result = 0
    switch (key) {
      case 'title':
        result = left.title.localeCompare(right.title)
        break
      case 'column':
        result = left.column.localeCompare(right.column)
        break
      case 'priority':
        result = (PRIORITY_ORDER[left.priority] ?? 3) - (PRIORITY_ORDER[right.priority] ?? 3)
        break
      case 'deadline':
        result = (left.deadline ?? '9999-12-31').localeCompare(right.deadline ?? '9999-12-31')
        break
      case 'labels':
        result = left.labels.join(',').localeCompare(right.labels.join(','))
        break
      case 'subtasks':
        result = left.subtaskTotal - right.subtaskTotal
        break
      case 'blocked':
        result = Number(left.blocked) - Number(right.blocked)
        break
      case 'updated':
        result = left.updatedAt.localeCompare(right.updatedAt)
        break
    }
    if (result !== 0) return result * factor
    return left.sortOrder - right.sortOrder
  })
}

/** isOverdue compares a local calendar date against today's local date. */
export function isOverdue(deadline: string | null): boolean {
  if (deadline === null) return false
  const today = new Date()
  const local = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(
    today.getDate()
  ).padStart(2, '0')}`
  return deadline < local
}

const DATE_TIME = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit'
})

export function formatDateTime(iso: string | null): string {
  if (iso === null) return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return DATE_TIME.format(date)
}

export function formatDate(iso: string | null): string {
  if (iso === null) return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  }).format(date)
}