import { KanbanModule } from './module'
import type { KanbanDeps } from './deps'

/**
 * Kanban module (plan §4.7). One instance is bound to one project
 * database; `projectStatus` carries the archived-project gate.
 */
export function createKanbanModule(deps: KanbanDeps): KanbanModule {
  return new KanbanModule(deps)
}

export { KanbanModule, quoteTitle, columnState } from './module'
export { TaskService, type TaskListQueryInput } from './tasks'
export { ColumnService } from './columns'
export { ChecklistService } from './checklist'
export { LabelService } from './labels'
export { LinkService, type LinkInput } from './links'
export { ViewService, type BoardQuery } from './views'
export { applyMove, type MoveTarget } from './move'
export { createTaskCompleter, type TaskCompleter, type KanbanModuleLike } from './deps'
export type {
  Db,
  Tx,
  KanbanDeps,
  KanbanEvent,
  KanbanEventWriter,
  KanbanRelations,
  KanbanTrash,
  ProjectStatus,
  TaskSuggester
} from './deps'
export * from './types'
export { COLUMN_NAME_MAX, anchorError, currentIsoWeek, decodeLabels } from './validate'
export { LIMITS as KANBAN_LIMITS } from './types'
export {
  createKanbanTools,
  createColumnDeleteEffect,
  kanbanModuleFromContext,
  kanbanTools,
  KANBAN_TOOL_NAMES,
  checklistView,
  columnView,
  taskDetailView,
  taskSummaryView,
  taskView,
  type KanbanToolDeps
} from './mcp'
export type { HandlerContext, ToolDefinition, ToolResult } from '../mcp/types'