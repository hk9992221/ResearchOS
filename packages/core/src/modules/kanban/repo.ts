import type {
  ChecklistItem,
  KanbanTask,
  RelationType,
  TaskColumnInfo,
  TaskColumnKind,
  TaskPriority,
  TaskSort,
  TaskSummary
} from '@researchos/shared'

import type { Db } from './deps'
import type { ChecklistRow, ColumnRow, DomainSlot, PageResult, SummaryRow, TaskRow } from './types'
import { decodeLabels } from './validate'
import { LIMITS } from './types'

export type SqlValue = string | number | null
export type SqlFragment = { where: string[]; args: SqlValue[] }

export const TASK_COLUMNS =
  'id, title, "column", parent_task_id, description_md, priority, deadline, blocked, labels, ' +
  'week_plan, sort_order, completed_at, archived_at, deleted_at, created_at, updated_at'

const SUMMARY_COLUMNS =
  't.id, t.title, t."column", t.parent_task_id, t.priority, t.deadline, t.blocked, t.labels, ' +
  't.week_plan, t.sort_order, t.completed_at, t.archived_at, t.updated_at, ' +
  '(SELECT count(*) FROM tasks s WHERE s.parent_task_id = t.id AND s.deleted_at IS NULL) AS subtask_total, ' +
  '(SELECT count(*) FROM tasks s WHERE s.parent_task_id = t.id AND s.deleted_at IS NULL ' +
  '  AND s."column" IN (SELECT name FROM task_columns WHERE is_done = 1)) AS subtask_done, ' +
  '(SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id) AS checklist_total, ' +
  '(SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id AND c.done = 1) AS checklist_done, ' +
  '(SELECT count(*) FROM entity_relations r WHERE r.src_type = ? AND r.src_id = t.id) AS link_count'

export function normalizePriority(value: string | null | undefined): TaskPriority {
  if (value === 'high' || value === 'medium' || value === 'low') return value
  return 'none'
}

export function mapTask(row: TaskRow): KanbanTask {
  return {
    id: row.id,
    title: row.title,
    column: row.column,
    parentTaskId: row.parent_task_id,
    descriptionMd: row.description_md,
    priority: normalizePriority(row.priority),
    deadline: row.deadline,
    blocked: row.blocked !== 0,
    labels: decodeLabels(row.labels),
    weekPlan: row.week_plan,
    sortOrder: row.sort_order,
    completedAt: row.completed_at,
    archivedAt: row.archived_at,
    deletedAt: row.deleted_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

export function mapSummary(row: SummaryRow): TaskSummary {
  return {
    id: row.id,
    title: row.title,
    column: row.column,
    parentTaskId: row.parent_task_id,
    priority: normalizePriority(row.priority),
    deadline: row.deadline,
    blocked: row.blocked !== 0,
    labels: decodeLabels(row.labels),
    weekPlan: row.week_plan,
    sortOrder: row.sort_order,
    subtaskDone: row.subtask_done,
    subtaskTotal: row.subtask_total,
    checklistDone: row.checklist_done,
    checklistTotal: row.checklist_total,
    linkCount: row.link_count,
    completedAt: row.completed_at,
    archivedAt: row.archived_at,
    updatedAt: row.updated_at
  }
}

export function mapColumn(row: ColumnRow): TaskColumnInfo {
  return {
    name: row.name,
    kind: row.kind as TaskColumnKind,
    isDone: row.is_done !== 0,
    position: row.position
  }
}

export function mapChecklist(row: ChecklistRow): ChecklistItem {
  return {
    id: row.id,
    taskId: row.task_id,
    text: row.text,
    done: row.done !== 0,
    sortOrder: row.sort_order
  }
}

/* ------------------------------------------------------------------ tasks */

export function getTaskRow(db: Db, id: string): TaskRow | undefined {
  return db.prepare(`SELECT ${TASK_COLUMNS} FROM tasks WHERE id = ?`).get(id) as TaskRow | undefined
}

export function getTaskRowTx(tx: Db, id: string): TaskRow | undefined {
  return tx.prepare(`SELECT ${TASK_COLUMNS} FROM tasks WHERE id = ?`).get(id) as TaskRow | undefined
}

export function countActiveTasks(db: Db): number {
  const row = db
    .prepare('SELECT count(*) AS n FROM tasks WHERE deleted_at IS NULL AND archived_at IS NULL')
    .get() as { n: number }
  return row.n
}

export function childTaskIds(db: Db, parentId: string, deleted: boolean): string[] {
  const predicate = deleted ? 'deleted_at IS NOT NULL' : 'deleted_at IS NULL'
  const rows = db
    .prepare(`SELECT id FROM tasks WHERE parent_task_id = ? AND ${predicate} ORDER BY sort_order, id`)
    .all(parentId) as Array<{ id: string }>
  return rows.map((row) => row.id)
}

/* --------------------------------------------------------------- columns */

export function listColumns(db: Db): TaskColumnInfo[] {
  const rows = db
    .prepare('SELECT name, kind, is_done, position FROM task_columns ORDER BY position, name')
    .all() as ColumnRow[]
  return rows.map(mapColumn)
}

export function getColumnRow(db: Db, name: string): ColumnRow | undefined {
  return db.prepare('SELECT name, kind, is_done, position FROM task_columns WHERE name = ?').get(name) as
    | ColumnRow
    | undefined
}

export function getColumn(db: Db, name: string): TaskColumnInfo | undefined {
  const row = getColumnRow(db, name)
  return row === undefined ? undefined : mapColumn(row)
}

/** Leftmost non-terminal column; falls back to the leftmost column. */
export function defaultColumnName(db: Db): string | undefined {
  const row = db
    .prepare('SELECT name FROM task_columns ORDER BY is_done ASC, position ASC LIMIT 1')
    .get() as { name: string } | undefined
  return row?.name
}

export function leftmostDoneColumn(db: Db): string | undefined {
  const row = db
    .prepare('SELECT name FROM task_columns WHERE is_done = 1 ORDER BY position ASC LIMIT 1')
    .get() as { name: string } | undefined
  return row?.name
}

export function isTerminalColumn(db: Db, name: string): boolean {
  const row = getColumnRow(db, name)
  return row !== undefined && row.is_done !== 0
}

export function otherDoneColumns(db: Db, name: string): number {
  const row = db
    .prepare('SELECT count(*) AS n FROM task_columns WHERE is_done = 1 AND name <> ?')
    .get(name) as { n: number }
  return row.n
}

export function countColumns(db: Db): number {
  return (db.prepare('SELECT count(*) AS n FROM task_columns').get() as { n: number }).n
}

/* ------------------------------------------------------- sort placement */

export function nextTaskSort(db: Db, column: string, parentId: string | null): number {
  const row = db
    .prepare(
      'SELECT MAX(sort_order) AS m FROM tasks WHERE "column" = ? AND parent_task_id IS ? AND deleted_at IS NULL'
    )
    .get(column, parentId as SqlValue) as { m: number | null }
  return row.m === null ? LIMITS.sortStep : row.m + LIMITS.sortStep
}

export function taskDomain(db: Db, column: string, parentId: string | null, excludeId: string): DomainSlot[] {
  return db
    .prepare(
      'SELECT id, sort_order AS sort FROM tasks WHERE "column" = ? AND parent_task_id IS ? ' +
        'AND deleted_at IS NULL AND id <> ? ORDER BY sort_order, id'
    )
    .all(column, parentId as SqlValue, excludeId) as DomainSlot[]
}

export function checklistDomain(db: Db, taskId: string, excludeId: string): DomainSlot[] {
  return db
    .prepare(
      'SELECT id, sort_order AS sort FROM task_checklist_items WHERE task_id = ? AND id <> ? ' +
        'ORDER BY sort_order, id'
    )
    .all(taskId, excludeId) as DomainSlot[]
}

export interface Placement {
  sort: number
  /** Present when the gap was exhausted and the whole domain is renumbered. */
  ordered?: DomainSlot[]
}

/**
 * Computes the insertion position inside a domain (mover excluded).
 * `before=true` treats the anchor as the right neighbour. When no integer gap
 * remains, the full domain is returned for a 1024-step rewrite.
 */
export function planInsert(
  domain: readonly DomainSlot[],
  moverId: string,
  anchorId: string,
  before: boolean
): Placement {
  if (anchorId === '') {
    const last = domain[domain.length - 1]
    return { sort: last === undefined ? LIMITS.sortStep : last.sort + LIMITS.sortStep }
  }
  const index = domain.findIndex((slot) => slot.id === anchorId)
  if (index < 0) {
    throw new RangeError(`anchor ${anchorId} is not in the target domain`)
  }
  const anchor = domain[index] as DomainSlot
  let left: DomainSlot | undefined
  let right: DomainSlot | undefined
  let insertAt = index
  if (before) {
    right = anchor
    left = index > 0 ? domain[index - 1] : undefined
  } else {
    left = anchor
    right = index + 1 < domain.length ? domain[index + 1] : undefined
    insertAt = index + 1
  }
  if (left !== undefined && right !== undefined) {
    if (right.sort - left.sort > 1) return { sort: left.sort + Math.floor((right.sort - left.sort) / 2) }
    const ids = [...domain.slice(0, insertAt).map((slot) => slot.id), moverId, ...domain.slice(insertAt).map((slot) => slot.id)]
    const ordered: DomainSlot[] = ids.map((id, index) => ({ id, sort: (index + 1) * LIMITS.sortStep }))
    return { sort: (insertAt + 1) * LIMITS.sortStep, ordered }
  }
  if (left !== undefined) return { sort: left.sort + LIMITS.sortStep }
  if (right !== undefined) return { sort: right.sort - LIMITS.sortStep }
  return { sort: LIMITS.sortStep }
}

/* ------------------------------------------------------------ pagination */

interface CursorPayload {
  k: readonly string[]
  id: string
}

export function encodeCursor(keys: readonly string[], id: string): string {
  return Buffer.from(JSON.stringify({ k: keys, id } satisfies CursorPayload), 'utf8').toString('base64url')
}

export function decodeCursor(cursor: string): CursorPayload | null {
  if (cursor === '') return null
  try {
    const parsed: unknown = JSON.parse(Buffer.from(cursor, 'base64url').toString('utf8'))
    if (typeof parsed !== 'object' || parsed === null || !('k' in parsed) || !('id' in parsed)) return null
    const keys: unknown = parsed.k
    const id: unknown = parsed.id
    if (!Array.isArray(keys) || !keys.every((entry) => typeof entry === 'string')) return null
    if (typeof id !== 'string' || id === '') return null
    return { k: keys, id }
  } catch {
    return null
  }
}

interface SortKey {
  expr: string
  numeric: boolean
  desc: boolean
}

export interface SortSpec {
  orderBy: string
  keys: SortKey[]
}

const PRIORITY_RANK_SQL = `CASE t.priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END`
const DEADLINE_NULL_SQL = 'CASE WHEN t.deadline IS NULL THEN 1 ELSE 0 END'

export function sortSpecFor(sort: TaskSort): SortSpec {
  switch (sort) {
    case 'updated_desc':
      return { orderBy: 't.updated_at DESC, t.id ASC', keys: [{ expr: 't.updated_at', numeric: false, desc: true }] }
    case 'priority_desc':
      return {
        orderBy: `${PRIORITY_RANK_SQL} ASC, ${DEADLINE_NULL_SQL} ASC, COALESCE(t.deadline, '') ASC, t.sort_order ASC, t.id ASC`,
        keys: [
          { expr: PRIORITY_RANK_SQL, numeric: true, desc: false },
          { expr: DEADLINE_NULL_SQL, numeric: true, desc: false },
          { expr: `COALESCE(t.deadline, '')`, numeric: false, desc: false },
          { expr: 't.sort_order', numeric: true, desc: false }
        ]
      }
    case 'deadline_asc':
      return {
        orderBy: `${DEADLINE_NULL_SQL} ASC, COALESCE(t.deadline, '') ASC, ${PRIORITY_RANK_SQL} ASC, t.sort_order ASC, t.id ASC`,
        keys: [
          { expr: DEADLINE_NULL_SQL, numeric: true, desc: false },
          { expr: `COALESCE(t.deadline, '')`, numeric: false, desc: false },
          { expr: PRIORITY_RANK_SQL, numeric: true, desc: false },
          { expr: 't.sort_order', numeric: true, desc: false }
        ]
      }
    case 'position_asc':
    default:
      return { orderBy: 't.sort_order ASC, t.id ASC', keys: [{ expr: 't.sort_order', numeric: true, desc: false }] }
  }
}

function cursorPredicate(keys: readonly SortKey[], values: readonly string[], id: string): SqlFragment {
  let chain = 't.id > ?'
  const args: SqlValue[] = [id]
  for (let i = keys.length - 1; i >= 0; i -= 1) {
    const key = keys[i] as SortKey
    const placeholder = key.numeric ? 'CAST(? AS INTEGER)' : '?'
    const operator = key.desc ? '<' : '>'
    chain = `(${key.expr} ${operator} ${placeholder} OR (${key.expr} = ${placeholder} AND ${chain}))`
    const value = values[i] ?? ''
    args.unshift(value, value)
  }
  return { where: [chain], args }
}

/* --------------------------------------------------------------- queries */

export interface SummaryQuery {
  where: SqlFragment
  spec: SortSpec
  limit: number
  cursor?: string
}

export function querySummaries(db: Db, query: SummaryQuery): Omit<PageResult<TaskSummary>, 'total'> {
  const limit = query.limit > 0 ? Math.min(query.limit, LIMITS.listMaxLimit) : LIMITS.listDefaultLimit
  const where = [...query.where.where]
  const args: SqlValue[] = [...query.where.args]

  const cursor = query.cursor === undefined ? null : decodeCursor(query.cursor)
  if (cursor !== null && cursor.k.length === query.spec.keys.length) {
    const predicate = cursorPredicate(query.spec.keys, cursor.k, cursor.id)
    where.push(...predicate.where)
    args.push(...predicate.args)
  }

  const keySelect = query.spec.keys
    .map((key, index) => `CAST(${key.expr} AS TEXT) AS k${index}`)
    .join(', ')
  const sql =
    `SELECT ${SUMMARY_COLUMNS}, ${keySelect} ` +
    `FROM tasks t WHERE ${where.join(' AND ')} ORDER BY ${query.spec.orderBy} LIMIT ?`
  const rows = db.prepare(sql).all('task', ...args, limit + 1) as Array<SummaryRow & Record<string, unknown>>
  const items = rows.slice(0, limit).map(mapSummary)
  const last = rows[limit - 1]
  const nextCursor =
    rows.length > limit && last !== undefined
      ? encodeCursor(
          query.spec.keys.map((_, index) => String(last[`k${index}`] ?? '')),
          last.id
        )
      : null
  return { items, nextCursor }
}

export function countSummaries(db: Db, where: SqlFragment): number {
  const sql = `SELECT count(*) AS n FROM tasks t WHERE ${where.where.join(' AND ')}`
  return (db.prepare(sql).get(...where.args) as { n: number }).n
}

export function escapeLike(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')
}

export interface TaskFilter {
  column?: string
  parentId?: string
  onlyTopLevel?: boolean
  priority?: readonly TaskPriority[]
  labels?: readonly string[]
  deadlineFrom?: string
  deadlineTo?: string
  blocked?: boolean
  weekPlan?: string
  text?: string
  includeArchived?: boolean
  includeDeleted?: boolean
}

export function buildWhere(filter: TaskFilter): SqlFragment {
  const where: string[] = ['1 = 1']
  const args: SqlValue[] = []
  if (filter.includeDeleted !== true) where.push('t.deleted_at IS NULL')
  if (filter.includeArchived !== true) where.push('t.archived_at IS NULL')
  if (filter.column !== undefined) {
    where.push('t."column" = ?')
    args.push(filter.column)
  }
  if (filter.parentId !== undefined) {
    where.push('t.parent_task_id = ?')
    args.push(filter.parentId)
  } else if (filter.onlyTopLevel === true) {
    where.push('t.parent_task_id IS NULL')
  }
  if (filter.priority !== undefined && filter.priority.length > 0) {
    const names: string[] = []
    let wantNone = false
    for (const priority of filter.priority) {
      if (priority === 'none') wantNone = true
      else names.push(priority)
    }
    if (names.length > 0 && wantNone) {
      where.push(`(t.priority IN (${names.map(() => '?').join(',')}) OR t.priority IS NULL)`)
      args.push(...names)
    } else if (names.length > 0) {
      where.push(`t.priority IN (${names.map(() => '?').join(',')})`)
      args.push(...names)
    } else {
      where.push('t.priority IS NULL')
    }
  }
  for (const label of filter.labels ?? []) {
    where.push('EXISTS (SELECT 1 FROM json_each(t.labels) WHERE json_each.value = ?)')
    args.push(label)
  }
  if (filter.deadlineFrom !== undefined) {
    where.push('t.deadline >= ?')
    args.push(filter.deadlineFrom)
  }
  if (filter.deadlineTo !== undefined) {
    where.push('t.deadline <= ?')
    args.push(filter.deadlineTo)
  }
  if (filter.blocked !== undefined) {
    where.push('t.blocked = ?')
    args.push(filter.blocked ? 1 : 0)
  }
  if (filter.weekPlan !== undefined) {
    where.push('t.week_plan = ?')
    args.push(filter.weekPlan)
  }
  if (filter.text !== undefined && filter.text.trim() !== '') {
    const pattern = `%${escapeLike(filter.text.trim())}%`
    where.push(`(t.title LIKE ? ESCAPE '\\' OR t.description_md LIKE ? ESCAPE '\\')`)
    args.push(pattern, pattern)
  }
  return { where, args }
}

/* ------------------------------------------------------------- checklist */

export function getChecklistRow(db: Db, id: string): ChecklistRow | undefined {
  return db
    .prepare('SELECT id, task_id, text, done, sort_order FROM task_checklist_items WHERE id = ?')
    .get(id) as ChecklistRow | undefined
}

export function listChecklistRows(db: Db, taskId: string): ChecklistItem[] {
  const rows = db
    .prepare(
      'SELECT id, task_id, text, done, sort_order FROM task_checklist_items WHERE task_id = ? ' +
        'ORDER BY sort_order, id'
    )
    .all(taskId) as ChecklistRow[]
  return rows.map(mapChecklist)
}

export function countChecklist(db: Db, taskId: string): number {
  return (
    db.prepare('SELECT count(*) AS n FROM task_checklist_items WHERE task_id = ?').get(taskId) as { n: number }
  ).n
}

/* ----------------------------------------------------------------- labels */

export function liveLabelRows(db: Db): string[] {
  const rows = db
    .prepare('SELECT labels FROM tasks WHERE deleted_at IS NULL AND archived_at IS NULL')
    .all() as Array<{ labels: string }>
  return rows.map((row) => row.labels)
}

