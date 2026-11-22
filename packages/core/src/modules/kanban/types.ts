import type {
  ActivityActor,
  ChecklistItem,
  ColumnCreateInput,
  KanbanTask,
  TaskColumnInfo,
  TaskColumnKind,
  TaskCreateInput,
  TaskDetail,
  TaskLabelCount,
  TaskLink,
  TaskMoveInput,
  TaskPage,
  TaskPatch,
  TaskPriority,
  TaskSort,
  TaskSummary,
  KanbanTaskSuggestions,
  TaskTargetState,
  TaskUpdateInput
} from '@researchos/shared'

/**
 * Limits (plan §4.7, §5.2). The title/description caps are
 * application-level; the 10k cap counts live, unarchived tasks.
 */
export const LIMITS = {
  titleChars: 200,
  descriptionChars: 100_000,
  labelChars: 32,
  labelsPerTask: 20,
  subtasksPerParent: 100,
  activeTasks: 10_000,
  checklistPerTask: 100,
  checklistChars: 500,
  listDefaultLimit: 50,
  listMaxLimit: 200,
  sortStep: 1024
} as const

export const COLUMN_KINDS: readonly TaskColumnKind[] = [
  'backlog',
  'todo',
  'in_progress',
  'review',
  'done',
  'custom'
]

export const DEFAULT_COLUMNS: ReadonlyArray<{ name: string; kind: TaskColumnKind; isDone: boolean }> = [
  { name: 'Backlog', kind: 'backlog', isDone: false },
  { name: 'Todo', kind: 'todo', isDone: false },
  { name: 'In Progress', kind: 'in_progress', isDone: false },
  { name: 'Review', kind: 'review', isDone: false },
  { name: 'Done', kind: 'done', isDone: true }
]

/** Task → entity links may only use these two relations (TASK-11). */
export const TASK_RELATIONS: Readonly<Record<string, true>> = {
  related_to: true,
  deliverable_of: true
}

/**
 * Direction table for `deliverable_of`; `related_to` accepts any
 * entity type. `task → paper` is therefore rejected.
 */
export const DELIVERABLE_OF_TARGETS: Readonly<Record<string, true>> = {
  note: true,
  idea: true,
  experiment: true,
  run: true,
  weekly_report: true
}

export type {
  ActivityActor,
  ChecklistItem,
  ColumnCreateInput,
  KanbanTask,
  TaskColumnInfo,
  TaskColumnKind,
  TaskCreateInput,
  TaskDetail,
  TaskLabelCount,
  TaskLink,
  TaskMoveInput,
  TaskPage,
  TaskPatch,
  TaskPriority,
  TaskSort,
  TaskSummary,
  KanbanTaskSuggestions,
  TaskTargetState,
  TaskUpdateInput
}

/** Raw `tasks` row (SQL column names). */
export interface TaskRow {
  id: string
  title: string
  column: string
  parent_task_id: string | null
  description_md: string
  priority: string | null
  deadline: string | null
  blocked: number
  labels: string
  week_plan: string | null
  sort_order: number
  completed_at: string | null
  archived_at: string | null
  deleted_at: string | null
  created_at: string
  updated_at: string
}

export interface SummaryRow {
  id: string
  title: string
  column: string
  parent_task_id: string | null
  priority: string | null
  deadline: string | null
  blocked: number
  labels: string
  week_plan: string | null
  sort_order: number
  completed_at: string | null
  archived_at: string | null
  updated_at: string
  subtask_total: number
  subtask_done: number
  checklist_total: number
  checklist_done: number
  link_count: number
}

export interface ColumnRow {
  name: string
  kind: string
  is_done: number
  position: number
}

export interface ChecklistRow {
  id: string
  task_id: string
  text: string
  done: number
  sort_order: number
}

/** One member of a sort domain (column × parent). */
export interface DomainSlot {
  id: string
  sort: number
}

export interface PageResult<T> {
  items: T[]
  nextCursor: string | null
  total: number
}