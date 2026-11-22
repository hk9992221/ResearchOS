/**
 * Kanban query keys.
 *
 * Every key is rooted at the shell's `['kanban', projectId]` prefix, so an
 * `entity.changed` event invalidates this module without touching another
 * project. The board key is the shell factory's; the other views add their
 * own stable segment, which keeps a checklist edit from invalidating the
 * board (see `ChecklistPanel`).
 */

import type { QueryKey } from '@tanstack/react-query'

import type { TaskListQuery } from '@researchos/shared'

import { queryKeys } from '../../lib/query'

export interface BoardFilters {
  readonly priority?: readonly string[]
  readonly labels?: readonly string[]
  readonly blocked?: boolean
  readonly q?: string
}

export interface ArchiveFilters {
  readonly q?: string
}

export const kanbanKeys = {
  all: (projectId: string): QueryKey => queryKeys.kanban.all(projectId),
  board: (projectId: string, filters: BoardFilters = {}): QueryKey => [
    ...queryKeys.kanban.board(projectId),
    filters
  ],
  columns: (projectId: string): QueryKey => [...queryKeys.kanban.all(projectId), 'columns'],
  list: (projectId: string, query: Partial<TaskListQuery>): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'list',
    query
  ],
  blocked: (projectId: string): QueryKey => [...queryKeys.kanban.all(projectId), 'blocked'],
  week: (projectId: string, week: string): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'week',
    week
  ],
  weekCandidates: (projectId: string): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'week',
    'candidates'
  ],
  archive: (projectId: string, query: ArchiveFilters): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'archive',
    query
  ],
  inbox: (projectId: string): QueryKey => [...queryKeys.kanban.all(projectId), 'inbox'],
  labels: (projectId: string): QueryKey => [...queryKeys.kanban.all(projectId), 'labels'],
  linkCandidates: (projectId: string, targetType: string, q: string): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'linkCandidates',
    targetType,
    q
  ],
  detail: (projectId: string, taskId: string): QueryKey => [
    ...queryKeys.kanban.all(projectId),
    'detail',
    taskId
  ]
} as const