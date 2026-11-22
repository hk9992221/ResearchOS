/**
 * Kanban IPC adapter. The typed client already mirrors the shared command
 * contract; this module only names the calls the feature makes, so the
 * components stay free of channel strings.
 */

import type {
  BoardView,
  ChecklistItem,
  EntityId,
  KanbanTask,
  RelationType,
  TaskColumnInfo,
  TaskCreateInput,
  TaskDetail,
  TaskLabelCount,
  TaskLink,
  TaskListQuery,
  TaskPage,
  TaskPatch,
  TaskPriority
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'
import type { BoardFilters, ArchiveFilters } from './keys'

export interface ChecklistCounts {
  readonly done: number
  readonly total: number
}

export const kanbanApi = {
  board: (projectId: string, filters: BoardFilters): Promise<BoardView> =>
    ipc.invoke('kanban.views.board', {
      projectId,
      priority: filters.priority as TaskPriority[] | undefined,
      labels: filters.labels === undefined ? undefined : [...filters.labels],
      blocked: filters.blocked,
      q: filters.q === '' ? undefined : filters.q
    }),

  columns: (projectId: string): Promise<TaskColumnInfo[]> =>
    ipc.invoke('kanban.columns.list', { projectId }),

  list: (projectId: string, query: Partial<TaskListQuery> = {}): Promise<TaskPage> =>
    ipc.invoke('kanban.views.list', { projectId, limit: 200, ...query }),

  blocked: (projectId: string): Promise<TaskPage> =>
    ipc.invoke('kanban.views.blocked', { projectId, limit: 200 }),

  week: (projectId: string, weekPlan: string): Promise<TaskPage> =>
    ipc.invoke('kanban.views.week', { projectId, weekPlan, limit: 200 }),

  archive: (projectId: string, filters: ArchiveFilters = {}): Promise<TaskPage> =>
    ipc.invoke('kanban.views.archive', {
      projectId,
      q: filters.q === '' ? undefined : filters.q,
      limit: 200,
      sort: 'updated_desc'
    }),

  inbox: (projectId: string): Promise<TaskPage> =>
    ipc.invoke('kanban.views.inbox', { projectId, limit: 20 }),

  getTask: (projectId: string, id: EntityId): Promise<TaskDetail> =>
    ipc.invoke('kanban.tasks.get', { projectId, id }),

  createTask: (input: TaskCreateInput): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.create', input),

  updateTask: (
    projectId: string,
    id: EntityId,
    patch: TaskPatch,
    expectedUpdatedAt?: string
  ): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.update', { projectId, id, patch, expectedUpdatedAt }),

  moveTask: (
    projectId: string,
    id: EntityId,
    column: string,
    anchors: { beforeId?: EntityId; afterId?: EntityId } = {}
  ): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.move', { projectId, id, column, ...anchors }),

  completeTask: (projectId: string, id: EntityId, done: boolean): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.complete', { projectId, id, done }),

  archiveTask: (projectId: string, id: EntityId): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.archive', { projectId, id }),

  unarchiveTask: (projectId: string, id: EntityId): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.unarchive', { projectId, id }),

  deleteTask: (projectId: string, id: EntityId): Promise<{ deleted: boolean }> =>
    ipc.invoke('kanban.tasks.delete', { projectId, id }),

  restoreTask: (projectId: string, id: EntityId): Promise<KanbanTask> =>
    ipc.invoke('kanban.tasks.restore', { projectId, id }),

  renameColumn: (projectId: string, name: string, newName: string): Promise<TaskColumnInfo> =>
    ipc.invoke('kanban.columns.rename', { projectId, name, newName }),

  moveColumn: (projectId: string, name: string, position: number): Promise<TaskColumnInfo[]> =>
    ipc.invoke('kanban.columns.move', { projectId, name, position }),

  setColumnDone: (projectId: string, name: string, isDone: boolean): Promise<TaskColumnInfo> =>
    ipc.invoke('kanban.columns.setDone', { projectId, name, isDone }),

  deleteColumn: (
    projectId: string,
    name: string,
    migrateTo: string
  ): Promise<{ deleted: boolean; movedTasks: number }> =>
    ipc.invoke('kanban.columns.delete', { projectId, name, migrateTo }),

  checklist: {
    add: (projectId: string, taskId: string, text: string): Promise<ChecklistItem> =>
      ipc.invoke('kanban.checklist.add', { projectId, taskId, text }),
    update: (projectId: string, itemId: string, text: string): Promise<ChecklistItem> =>
      ipc.invoke('kanban.checklist.update', { projectId, itemId, text }),
    setDone: (projectId: string, itemId: string, done: boolean): Promise<ChecklistItem> =>
      ipc.invoke('kanban.checklist.setDone', { projectId, itemId, done }),
    remove: (projectId: string, itemId: string): Promise<{ deleted: boolean }> =>
      ipc.invoke('kanban.checklist.remove', { projectId, itemId }),
    reorder: (projectId: string, taskId: string, itemIds: string[]): Promise<ChecklistItem[]> =>
      ipc.invoke('kanban.checklist.reorder', { projectId, taskId, itemIds })
  },

  labels: (projectId: string): Promise<TaskLabelCount[]> =>
    ipc.invoke('kanban.labels.list', { projectId }),

  addLink: (
    projectId: string,
    taskId: string,
    relation: RelationType,
    targetType: TaskLink['dstType'],
    targetId: string
  ): Promise<TaskLink> =>
    ipc.invoke('kanban.links.add', { projectId, taskId, relation, targetType, targetId }),

  removeLink: (
    projectId: string,
    taskId: string,
    relation: RelationType,
    targetType: TaskLink['dstType'],
    targetId: string
  ): Promise<{ removed: boolean }> =>
    ipc.invoke('kanban.links.remove', { projectId, taskId, relation, targetType, targetId })
}