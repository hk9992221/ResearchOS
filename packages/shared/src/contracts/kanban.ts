import type { EntityType, RelationType, Task, TaskPriority } from '../entities'
import { TASK_PRIORITIES } from '../entities'
import type { EntityId, IsoDateTime } from '../types/common'

/**
 * Kanban contract (plan §4.7).
 *
 * Completion is a column property (`task_columns.is_done`), never a kind
 * string; `kind` is a display hint. A column name doubles as the
 * `tasks."column"` key, so a rename cascades the task rows.
 *
 * `Task` is the single canonical row shape, owned by `../entities` and
 * re-exported here. `KanbanTask` remains as an alias for existing kanban
 * module consumers; it introduces no second shape.
 */
export { TASK_PRIORITIES }
export type { Task, TaskPriority }
export type KanbanTask = Task

export const TASK_COLUMN_KINDS = [
  'backlog',
  'todo',
  'in_progress',
  'review',
  'done',
  'custom'
] as const
export type TaskColumnKind = (typeof TASK_COLUMN_KINDS)[number]

export const TASK_SORTS = ['position_asc', 'priority_desc', 'deadline_asc', 'updated_desc'] as const
export type TaskSort = (typeof TASK_SORTS)[number]

export interface TaskColumnInfo {
  name: string
  kind: TaskColumnKind
  isDone: boolean
  /** 0-based display order, stored with a 1024 step. */
  position: number
}

/** Card projection: the row plus the counters rendered on a board card. */
export interface TaskSummary {
  id: EntityId
  title: string
  column: string
  parentTaskId: EntityId | null
  priority: TaskPriority
  deadline: string | null
  blocked: boolean
  labels: string[]
  weekPlan: string | null
  sortOrder: number
  subtaskDone: number
  subtaskTotal: number
  checklistDone: number
  checklistTotal: number
  linkCount: number
  completedAt: IsoDateTime | null
  archivedAt: IsoDateTime | null
  updatedAt: IsoDateTime
}

export type TaskTargetState = 'alive' | 'deleted' | 'missing'

export interface TaskLink {
  relationId: EntityId
  relation: RelationType
  dstType: EntityType
  dstId: EntityId
  dstTitle: string
  /** `deleted` renders “target deleted”; `missing` rows are dirty data and dropped. */
  targetState: TaskTargetState
}

export interface ChecklistItem {
  id: EntityId
  taskId: EntityId
  text: string
  done: boolean
  sortOrder: number
}

/** Full detail projection returned by `task_get` and the detail inspector. */
export interface TaskDetail extends KanbanTask {
  columnInfo: TaskColumnInfo
  subtasks: TaskSummary[]
  checklist: ChecklistItem[]
  links: TaskLink[]
}

export interface TaskPage {
  items: TaskSummary[]
  /** Opaque keyset cursor; null = no more pages. */
  nextCursor: string | null
  /** Rows matching the filter, ignoring the cursor and limit. */
  total: number
}

export interface BoardColumn {
  column: TaskColumnInfo
  tasks: TaskSummary[]
}

export interface BoardView {
  projectId: EntityId
  columns: BoardColumn[]
}

export interface TaskLabelCount {
  label: string
  count: number
}

/* ----------------------------------------------------------------- inputs */

export interface TaskCreateInput {
  projectId: EntityId
  title: string
  /** Empty = the leftmost non-terminal column (quick capture). */
  column?: string
  parentTaskId?: EntityId
  descriptionMd?: string
  priority?: TaskPriority
  deadline?: string | null
  labels?: string[]
  weekPlan?: string | null
}

/**
 * Partial edit. `undefined` leaves a field unchanged; for `deadline` and
 * `weekPlan` an explicit `null` (or an empty string) clears the value.
 * A `column` change appends to the target column and emits `status.changed`.
 */
export interface TaskPatch {
  title?: string
  descriptionMd?: string
  priority?: TaskPriority
  deadline?: string | null
  weekPlan?: string | null
  blocked?: boolean
  labels?: string[]
  column?: string
}

export interface TaskUpdateInput {
  projectId: EntityId
  id: EntityId
  patch: TaskPatch
  expectedUpdatedAt?: IsoDateTime
}

export interface TaskMoveInput {
  projectId: EntityId
  id: EntityId
  column: string
  /** At most one anchor; both anchors and the task itself are rejected. */
  beforeId?: EntityId
  afterId?: EntityId
}

export interface TaskListQuery {
  projectId: EntityId
  column?: string
  parentId?: EntityId
  onlyTopLevel?: boolean
  priority?: TaskPriority[]
  /** AND semantics. */
  labels?: string[]
  deadlineFrom?: string
  deadlineTo?: string
  blocked?: boolean
  weekPlan?: string
  q?: string
  includeArchived?: boolean
  includeDeleted?: boolean
  sort?: TaskSort
  limit?: number
  cursor?: string
}

export interface ColumnCreateInput {
  projectId: EntityId
  name: string
  kind?: TaskColumnKind
  isDone?: boolean
  /** Insert directly after this column; omitted = far right. */
  after?: string
}

/* ---------------------------------------------------------- AI suggestions */

export interface KanbanTaskSuggestion {
  tempId: string
  title: string
  description: string
  priority: TaskPriority
  checklist: string[]
  targetEntity: { type: EntityType; id: EntityId } | null
  /** Deterministic rule that motivated the suggestion (`gap`, `limitation`, …). */
  sourceRule: string
}

/** Read-only preview. Nothing here is ever written to `tasks`. */
export interface KanbanTaskSuggestions {
  outputId: string
  suggestions: KanbanTaskSuggestion[]
  gaps: Array<{ rule: string; summary: string; suggestion: string; satisfied: boolean }>
  note: string
}

/* -------------------------------------------------------------- commands */

export interface KanbanCommands {
  'kanban.columns.list': { req: { projectId: EntityId }; res: TaskColumnInfo[] }
  'kanban.columns.create': { req: ColumnCreateInput; res: TaskColumnInfo }
  'kanban.columns.rename': {
    req: { projectId: EntityId; name: string; newName: string }
    res: TaskColumnInfo
  }
  'kanban.columns.move': {
    req: { projectId: EntityId; name: string; position: number }
    res: TaskColumnInfo[]
  }
  'kanban.columns.setDone': {
    req: { projectId: EntityId; name: string; isDone: boolean }
    res: TaskColumnInfo
  }
  'kanban.columns.delete': {
    req: { projectId: EntityId; name: string; migrateTo: string }
    res: { deleted: boolean; movedTasks: number }
  }

  'kanban.tasks.list': { req: TaskListQuery; res: TaskPage }
  'kanban.tasks.get': { req: { projectId: EntityId; id: EntityId }; res: TaskDetail }
  'kanban.tasks.create': { req: TaskCreateInput; res: KanbanTask }
  'kanban.tasks.update': { req: TaskUpdateInput; res: KanbanTask }
  'kanban.tasks.move': { req: TaskMoveInput; res: KanbanTask }
  /** `done:false` reopens a terminal task into the leftmost non-terminal column. */
  'kanban.tasks.complete': { req: { projectId: EntityId; id: EntityId; done: boolean }; res: KanbanTask }
  'kanban.tasks.archive': { req: { projectId: EntityId; id: EntityId }; res: KanbanTask }
  'kanban.tasks.unarchive': { req: { projectId: EntityId; id: EntityId }; res: KanbanTask }
  'kanban.tasks.delete': { req: { projectId: EntityId; id: EntityId }; res: { deleted: boolean } }
  'kanban.tasks.restore': { req: { projectId: EntityId; id: EntityId }; res: KanbanTask }

  'kanban.checklist.list': {
    req: { projectId: EntityId; taskId: EntityId }
    res: ChecklistItem[]
  }
  'kanban.checklist.add': {
    req: { projectId: EntityId; taskId: EntityId; text: string; beforeId?: EntityId }
    res: ChecklistItem
  }
  'kanban.checklist.update': {
    req: { projectId: EntityId; itemId: EntityId; text: string }
    res: ChecklistItem
  }
  'kanban.checklist.setDone': {
    req: { projectId: EntityId; itemId: EntityId; done: boolean }
    res: ChecklistItem
  }
  'kanban.checklist.remove': {
    req: { projectId: EntityId; itemId: EntityId }
    res: { deleted: boolean }
  }
  'kanban.checklist.reorder': {
    req: { projectId: EntityId; taskId: EntityId; itemIds: EntityId[] }
    res: ChecklistItem[]
  }

  'kanban.labels.list': { req: { projectId: EntityId }; res: TaskLabelCount[] }

  'kanban.links.add': {
    req: {
      projectId: EntityId
      taskId: EntityId
      relation: RelationType
      targetType: EntityType
      targetId: EntityId
    }
    res: TaskLink
  }
  'kanban.links.remove': {
    req: {
      projectId: EntityId
      taskId: EntityId
      relation: RelationType
      targetType: EntityType
      targetId: EntityId
    }
    res: { removed: boolean }
  }

  'kanban.views.board': {
    req: {
      projectId: EntityId
      priority?: TaskPriority[]
      labels?: string[]
      blocked?: boolean
      q?: string
    }
    res: BoardView
  }
  'kanban.views.list': { req: TaskListQuery; res: TaskPage }
  'kanban.views.week': {
    req: { projectId: EntityId; weekPlan?: string; limit?: number; cursor?: string }
    res: TaskPage
  }
  'kanban.views.blocked': {
    req: { projectId: EntityId; limit?: number; cursor?: string }
    res: TaskPage
  }
  /**
   * Quick-capture projection: live, unarchived top-level tasks in the default
   * (leftmost non-terminal) column.
   */
  'kanban.views.inbox': {
    req: { projectId: EntityId; limit?: number; cursor?: string }
    res: TaskPage
  }
  'kanban.views.archive': {
    req: {
      projectId: EntityId
      q?: string
      limit?: number
      cursor?: string
      sort?: TaskSort
    }
    res: TaskPage
  }

  'kanban.suggestTasks': {
    req: { projectId: EntityId; ideaId?: EntityId }
    res: KanbanTaskSuggestions
  }
}