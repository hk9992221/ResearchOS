/**
 * Query cache, key taxonomy and app-event wiring.
 *
 * Query key taxonomy (all keys are arrays; the first element is the root):
 *
 *   ['workspace', 'status']                       workspace.status
 *   ['projects', 'list', params]                  project.list, any filter
 *   ['projects', 'detail', projectId]             one project row
 *   ['project', projectId, 'overview', refresh]   project.overview
 *   ['project', projectId, 'stats']               project.stats
 *   ['notes', projectId, 'list', params]          notes.list
 *   ['notes', projectId, 'detail', noteId]        notes.get
 *   ['literature', projectId, 'papers', params]   literature.papers.list
 *   ['ideas', projectId, 'list', params]          ideas.list
 *   ['experiments', projectId, 'list', params]    experiments.list
 *   ['kanban', projectId, 'board']                kanban board
 *   ['weekly', projectId, 'reports', params]      weekly reports
 *   ['search', request]                           search.query
 *   ['mcp', 'status'] / ['mcp', 'confirmations', state]
 *   ['plugins', 'list']
 *   ['plugins', 'market', criteria]               plugins.market.search
 *   ['plugins', 'market', 'detail', pluginId]     plugins.market.detail
 *   ['plugins', 'registries']                     plugins.registries.list
 *   ['plugins', 'market', 'settings']             plugins.market.settings.get
 *
 * Invariant: a project-scoped entity key puts `projectId` immediately after
 * its root. That is what makes `entity.changed` invalidation a one-step
 * prefix match — `['notes', projectA]` can never touch project B or another
 * module, because neither uses that prefix.
 *
 * Domain data lives here (or in the main process); zustand holds only chrome
 * state (sidebar, palette, theme, language, job strip).
 */

import { QueryClient, type QueryKey } from '@tanstack/react-query'

import type {
  AppEventPayload,
  AppEventType,
  SearchQueryRequest
} from '@researchos/shared'

import { ipc } from './ipc'
import { translate } from '../i18n'
import { pushToast } from '../state/toasts'
import { useShellStore } from '../state/shell'
import { useTasksStore } from '../state/tasks'
import { useThemeStore } from '../state/theme'

export type { QueryKey }

export interface ProjectListParams {
  readonly includeArchived?: boolean
  readonly query?: string
}

/** The local filter set of `plugins.market.search`. */
export interface PluginMarketCriteria {
  readonly query?: string
  readonly tags?: readonly string[]
  readonly verifiedOnly?: boolean
  readonly registryId?: string
}

export const queryKeys = {
  workspace: {
    all: ['workspace'] as const,
    status: () => ['workspace', 'status'] as const
  },
  projects: {
    all: ['projects'] as const,
    list: (params: ProjectListParams = {}) => ['projects', 'list', params] as const,
    detail: (projectId: string) => ['projects', 'detail', projectId] as const
  },
  project: (projectId: string) => ({
    all: ['project', projectId] as const,
    overview: (refresh = false) => ['project', projectId, 'overview', refresh] as const,
    stats: () => ['project', projectId, 'stats'] as const
  }),
  notes: {
    all: (projectId: string) => ['notes', projectId] as const,
    list: (projectId: string, params: Record<string, unknown> = {}) =>
      ['notes', projectId, 'list', params] as const,
    detail: (projectId: string, noteId: string) =>
      ['notes', projectId, 'detail', noteId] as const,
    reconcile: (projectId: string) => ['notes', projectId, 'reconcile'] as const
  },
  literature: {
    all: (projectId: string) => ['literature', projectId] as const,
    papers: (projectId: string, params: Record<string, unknown> = {}) =>
      ['literature', projectId, 'papers', params] as const,
    paper: (projectId: string, paperId: string) =>
      ['literature', projectId, 'paper', paperId] as const
  },
  ideas: {
    all: (projectId: string) => ['ideas', projectId] as const,
    list: (projectId: string, params: Record<string, unknown> = {}) =>
      ['ideas', projectId, 'list', params] as const,
    detail: (projectId: string, ideaId: string) => ['ideas', projectId, 'detail', ideaId] as const
  },
  experiments: {
    all: (projectId: string) => ['experiments', projectId] as const,
    list: (projectId: string, params: Record<string, unknown> = {}) =>
      ['experiments', projectId, 'list', params] as const,
    detail: (projectId: string, experimentId: string) =>
      ['experiments', projectId, 'detail', experimentId] as const
  },
  kanban: {
    all: (projectId: string) => ['kanban', projectId] as const,
    board: (projectId: string) => ['kanban', projectId, 'board'] as const
  },
  weekly: {
    all: (projectId: string) => ['weekly', projectId] as const,
    reports: (projectId: string, params: Record<string, unknown> = {}) =>
      ['weekly', projectId, 'reports', params] as const
  },
  search: {
    all: ['search'] as const,
    query: (request: SearchQueryRequest) => ['search', request] as const
  },
  mcp: {
    all: ['mcp'] as const,
    status: ['mcp', 'status'] as const,
    confirmations: (state?: string) => ['mcp', 'confirmations', state ?? null] as const
  },
  plugins: {
    all: ['plugins'] as const,
    list: ['plugins', 'list'] as const,
    market: (criteria: PluginMarketCriteria = {}) => ['plugins', 'market', criteria] as const,
    detail: (pluginId: string) => ['plugins', 'market', 'detail', pluginId] as const,
    registries: ['plugins', 'registries'] as const,
    settings: ['plugins', 'market', 'settings'] as const
  },
  trash: {
    all: (projectId: string) => ['trash', projectId] as const,
    list: (projectId: string) => ['trash', projectId, 'list'] as const
  },
  diagnostics: {
    all: ['diagnostics'] as const,
    report: ['diagnostics', 'report'] as const,
    nativeSmoke: ['diagnostics', 'native'] as const
  },
  searchIndex: {
    all: ['search', 'index'] as const,
    status: (scope: 'project' | 'global', projectId?: string) =>
      ['search', 'index', scope, projectId ?? null] as const
  },
  ai: {
    all: ['ai'] as const,
    providers: ['ai', 'providers'] as const,
    roles: ['ai', 'roles'] as const,
    availability: ['ai', 'availability'] as const,
    models: (providerId: string) => ['ai', 'models', providerId] as const
  },
  weeklySkills: ['weekly', 'skills'] as const,
  config: ['app', 'config'] as const,
  exportJob: (jobId: string) => ['export', 'job', jobId] as const,
  importJob: (jobId: string) => ['import', 'job', jobId] as const
}

/**
 * Which project-scoped query roots an entity change invalidates. `projects`
 * has no project segment: a project row edit invalidates every project list
 * and detail, while the `project` root scopes to the project that changed.
 */
const ENTITY_QUERY_ROOTS: Readonly<Record<string, readonly string[]>> = {
  project: ['projects', 'project'],
  paper: ['literature', 'project'],
  note: ['notes', 'project'],
  research_database: ['notes', 'project'],
  db_field: ['notes', 'project'],
  db_record: ['notes', 'project'],
  idea: ['ideas', 'project'],
  evidence: ['ideas', 'project'],
  experiment: ['experiments', 'project'],
  run: ['experiments', 'project'],
  snapshot: ['experiments'],
  task: ['kanban', 'project'],
  weekly_report: ['weekly', 'project'],
  // A relation edit changes the relation lists of both endpoints; the safest
  // project-scoped answer is every module that renders relations.
  entity_relation: ['notes', 'ideas', 'experiments', 'literature', 'kanban', 'project']
}

export function invalidationKeysFor(entityType: string, projectId: string): QueryKey[] {
  const roots = ENTITY_QUERY_ROOTS[entityType] ?? ['project']
  return roots.map((root) => (root === 'projects' ? ['projects'] : [root, projectId]))
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Local IPC: an error is deterministic and surfaces as ErrorState with
        // an explicit retry, so automatic retries would only hide it.
        retry: 0,
        staleTime: 15_000,
        gcTime: 5 * 60_000,
        refetchOnWindowFocus: false
      }
    }
  })
}

/* ------------------------------------------------------------ flush hooks */

export type FlushReason = 'close' | 'switch-workspace'
export type FlushHandler = (reason: FlushReason) => void | Promise<void>

const flushHandlers = new Set<FlushHandler>()

/**
 * registerFlushHandler lets an editor flush its pending save before the window
 * closes or the workspace switches. Returns the unsubscribe function.
 */
export function registerFlushHandler(handler: FlushHandler): () => void {
  flushHandlers.add(handler)
  return () => {
    flushHandlers.delete(handler)
  }
}

/* ------------------------------------------------------------ event wiring */

/** The subset of the IPC client the wiring needs; a test source can replace it. */
export interface AppEventSource {
  on<E extends AppEventType>(
    event: E,
    handler: (payload: AppEventPayload<E>) => void
  ): () => void
}

/**
 * wireAppEvents connects main-process events to the cache and the stores.
 * Returns a function that detaches every subscription.
 */
export function wireAppEvents(client: QueryClient, source: AppEventSource = ipc): () => void {
  const detach: Array<() => void> = []

  detach.push(
    source.on('entity.changed', (event) => {
      for (const queryKey of invalidationKeysFor(event.entityType, event.projectId)) {
        void client.invalidateQueries({ queryKey })
      }
    })
  )

  detach.push(
    source.on('job.progress', (event) => {
      useTasksStore.getState().progress(event.jobId, event.percent, event.stage)
    })
  )

  detach.push(
    source.on('job.done', (event) => {
      useTasksStore.getState().finish(event.jobId, event.state, event.error?.message)
    })
  )

  detach.push(
    source.on('mcp.status.changed', (event) => {
      client.setQueryData(queryKeys.mcp.status, event.status)
      useShellStore.getState().setMcpPendingConfirmations(event.status.pendingConfirmations)
    })
  )

  detach.push(
    source.on('mcp.confirmations.changed', () => {
      void client.invalidateQueries({ queryKey: queryKeys.mcp.all })
      // The count lives on the status; refresh it so the badge tracks the
      // confirmations list the user is acting on.
      if (!ipc.available) return
      void client
        .fetchQuery({ queryKey: queryKeys.mcp.status, queryFn: () => ipc.invoke('mcp.status') })
        .then((status) => {
          useShellStore.getState().setMcpPendingConfirmations(status.pendingConfirmations)
        })
        .catch(() => {
          // A failed badge refresh must not break the shell; the next status
          // event corrects it.
        })
    })
  )

  detach.push(
    source.on('plugins.changed', () => {
      void client.invalidateQueries({ queryKey: queryKeys.plugins.all })
    })
  )

  detach.push(
    source.on('plugins.updated', (event) => {
      pushToast({
        tone: 'success',
        title: translate('pages.plugins.updated_toast', {
          name: event.name,
          version: event.version
        })
      })
      void client.invalidateQueries({ queryKey: queryKeys.plugins.all })
    })
  )

  detach.push(
    source.on('theme.system.changed', (event) => {
      useThemeStore.getState().setSystemDark(event.dark)
    })
  )

  detach.push(
    source.on('prefers.reduced-motion.changed', (event) => {
      useThemeStore.getState().setReducedMotion(event.reduced)
    })
  )

  detach.push(
    source.on('app.flush', (event) => {
      void (async () => {
        try {
          await Promise.all([...flushHandlers].map((handler) => handler(event.reason)))
        } finally {
          // Acknowledge the close handshake after every editor save settled.
          try {
            await ipc.invoke('app.flush', { reason: event.reason })
          } catch {
            // The window is closing; the main timeout is the backstop.
          }
        }
      })()
    })
  )

  return () => {
    for (const off of detach) off()
  }
}