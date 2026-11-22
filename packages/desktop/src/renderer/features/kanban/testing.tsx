import type { ReactNode } from 'react'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import {
  type BoardView,
  type ChecklistItem,
  type Project,
  type TaskColumnInfo,
  type TaskDetail,
  type TaskSummary
} from '@researchos/shared'

import { I18nProvider } from '../../i18n'
import { installBridge, makeProject, type FakeBridge, type FakeBridgeOptions } from '../../app/testing'
import { useShellStore } from '../../state/shell'

export const NOW = '2026-11-18T09:00:00.000Z'

export const COLUMNS: readonly TaskColumnInfo[] = [
  { name: 'Backlog', kind: 'backlog', isDone: false, position: 0 },
  { name: 'Todo', kind: 'todo', isDone: false, position: 1 },
  { name: 'Doing', kind: 'in_progress', isDone: false, position: 2 },
  { name: 'Done', kind: 'done', isDone: true, position: 3 }
]

export function makeTask(overrides: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id: 't1',
    title: 'Write report',
    column: 'Todo',
    parentTaskId: null,
    priority: 'medium',
    deadline: null,
    blocked: false,
    labels: [],
    weekPlan: null,
    sortOrder: 1,
    subtaskDone: 0,
    subtaskTotal: 0,
    checklistDone: 0,
    checklistTotal: 0,
    linkCount: 0,
    completedAt: null,
    archivedAt: null,
    updatedAt: NOW,
    ...overrides
  }
}

export function makeDetail(overrides: Partial<TaskDetail> = {}): TaskDetail {
  const base: TaskDetail = {
    id: 't1',
    title: 'Write report',
    column: 'Todo',
    parentTaskId: null,
    descriptionMd: 'Draft the **weekly** report.',
    priority: 'medium',
    deadline: null,
    blocked: false,
    labels: ['writing'],
    weekPlan: null,
    sortOrder: 1,
    completedAt: null,
    archivedAt: null,
    deletedAt: null,
    createdAt: NOW,
    updatedAt: NOW,
    columnInfo: { name: 'Todo', kind: 'todo', isDone: false, position: 1 },
    subtasks: [],
    checklist: [],
    links: []
  }
  return { ...base, ...overrides }
}

export function makeChecklist(overrides: Partial<ChecklistItem> = {}): ChecklistItem {
  return {
    id: 'c1',
    taskId: 't1',
    text: 'Check the numbers',
    done: false,
    sortOrder: 0,
    ...overrides
  }
}

export function makeBoard(tasks: readonly TaskSummary[], columns: readonly TaskColumnInfo[] = COLUMNS): BoardView {
  return {
    projectId: 'p1',
    columns: columns.map((column) => ({
      column,
      tasks: tasks.filter((task) => task.column === column.name)
    }))
  }
}

export function makeProjectRow(overrides: Partial<Project> = {}): Project {
  return { ...makeProject('p1', 'Alpha'), ...overrides }
}

export interface KanbanBridgeOptions extends FakeBridgeOptions {
  readonly board?: BoardView
  readonly detail?: TaskDetail
  readonly archived?: boolean
  readonly taskPage?: { items: TaskSummary[]; total?: number; nextCursor?: string | null }
}

export interface KanbanBridge {
  readonly fake: FakeBridge
  readonly queryClient: QueryClient
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 60_000 },
      mutations: { retry: false }
    }
  })
}

/** installKanbanBridge wires the commands the kanban feature can call. */
export function installKanbanBridge(options: KanbanBridgeOptions = {}): KanbanBridge {
  const board = options.board ?? makeBoard([makeTask()])
  const detail = options.detail ?? makeDetail()
  const page = {
    items: options.taskPage?.items ?? board.columns.flatMap((column) => column.tasks),
    total: options.taskPage?.total ?? 1,
    nextCursor: options.taskPage?.nextCursor ?? null
  }
  const handlers: Record<string, (request: unknown) => unknown> = {
    'project.get': () => makeProjectRow({ status: options.archived === true ? 'archived' : 'active' }),
    'kanban.columns.list': () => COLUMNS,
    'kanban.views.board': () => board,
    'kanban.views.list': () => page,
    'kanban.views.blocked': () => ({
      ...page,
      items: page.items.filter((task) => task.blocked)
    }),
    'kanban.views.week': () => ({
      ...page,
      items: page.items.filter((task) => task.weekPlan !== null)
    }),
    'kanban.views.archive': () => ({
      ...page,
      items: page.items.filter((task) => task.archivedAt !== null)
    }),
    'kanban.views.inbox': () => page,
    'kanban.tasks.get': () => detail,
    'kanban.tasks.create': (request) => {
      const input = request as { title: string; column?: string }
      return {
        ...makeTask({ id: `new-${input.title}`, title: input.title, column: input.column ?? 'Backlog' })
      }
    },
    'kanban.tasks.update': (request) => {
      const input = request as { patch: Record<string, unknown> }
      return { ...detail, ...input.patch, updatedAt: NOW }
    },
    'kanban.tasks.move': (request) => {
      const input = request as { id: string; column: string }
      return { ...detail, id: input.id, column: input.column }
    },
    'kanban.tasks.archive': () => ({ ...detail, archivedAt: NOW }),
    'kanban.tasks.unarchive': () => ({ ...detail, archivedAt: null }),
    'kanban.tasks.delete': () => ({ deleted: true }),
    'kanban.tasks.restore': () => detail,
    'kanban.tasks.complete': () => detail,
    'kanban.checklist.list': () => detail.checklist,
    'kanban.checklist.add': (request) => {
      const input = request as { text: string }
      return makeChecklist({ id: 'c-new', text: input.text, sortOrder: detail.checklist.length })
    },
    'kanban.checklist.update': (request) => {
      const input = request as { itemId: string; text: string }
      return makeChecklist({ id: input.itemId, text: input.text })
    },
    'kanban.checklist.setDone': (request) => {
      const input = request as { itemId: string; done: boolean }
      return makeChecklist({ id: input.itemId, done: input.done })
    },
    'kanban.checklist.remove': () => ({ deleted: true }),
    'kanban.checklist.reorder': () => detail.checklist,
    'kanban.labels.list': () => [],
    'kanban.links.add': (request) => {
      const input = request as {
        relation: TaskDetail['links'][number]['relation']
        targetType: TaskDetail['links'][number]['dstType']
        targetId: string
      }
      return {
        relationId: 'r-new',
        relation: input.relation,
        dstType: input.targetType,
        dstId: input.targetId,
        dstTitle: 'Linked target',
        targetState: 'alive'
      }
    },
    'kanban.links.remove': () => ({ removed: true }),
    'literature.papers.list': () => ({ items: [], total: 0, limit: 20, offset: 0 }),
    'notes.list': () => ({ items: [], total: 0, limit: 20, offset: 0 }),
    'ideas.list': () => ({ items: [], total: 0, limit: 20, offset: 0 }),
    'experiments.list': () => [],
    'experiments.runs.list': () => []
  }
  const merged = { ...handlers, ...options.handlers }
  const fake = installBridge({ ...options, handlers: merged })
  return { fake, queryClient: createQueryClient() }
}

export interface RenderOptions {
  readonly queryClient: QueryClient
  readonly route?: string
}

export function renderPage(node: ReactNode, options: RenderOptions): ReturnType<typeof render> {
  return render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={options.queryClient}>
        <MemoryRouter initialEntries={[options.route ?? '/p/p1/kanban']}>{node}</MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>
  )
}

export function renderDetail(node: ReactNode, queryClient: QueryClient): ReturnType<typeof render> {
  return render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
    </I18nProvider>
  )
}

/** selectProject primes the shell store so `useCurrentProject` resolves p1. */
export function selectProject(projectId = 'p1'): void {
  useShellStore.setState({ currentProjectId: projectId })
}

export function resetShell(): void {
  useShellStore.setState({
    sidebarCollapsed: false,
    paletteOpen: false,
    currentProjectId: null,
    mcpPendingConfirmations: 0
  })
}

export function cardElement(taskId: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`[data-task-id="${taskId}"]`)
  if (element === null) throw new Error(`card ${taskId} is not rendered`)
  return element
}

export function columnElement(name: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`[data-column="${name}"]`)
  if (element === null) throw new Error(`column ${name} is not rendered`)
  return element
}

export { makeProject }