/**
 * Overview IPC adapter: the eight-widget snapshot, one-widget refresh, the
 * provenance timeline and the context ego graph.
 */

import type {
  ActivityPage,
  ContextEntityKind,
  ContextGraph,
  EntityContext,
  OverviewSnapshot,
  OverviewWidgetKind,
  ProvenanceGranularity,
  ProvenanceReport
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'

export const overviewKeys = {
  snapshot: (projectId: string) => [...queryKeys.project(projectId).all, 'overview'] as const,
  provenance: (
    projectId: string,
    filter: { milestones: boolean; granularity: ProvenanceGranularity; focusIdeaId: string | null }
  ) => [...queryKeys.project(projectId).all, 'provenance', filter] as const,
  context: (projectId: string, type: string, id: string, depth: 1 | 2) =>
    [...queryKeys.project(projectId).all, 'context', type, id, depth] as const
}

/** The entity kinds `project.context` accepts. */
export const CONTEXT_KINDS: readonly ContextEntityKind[] = [
  'note',
  'paper',
  'idea',
  'experiment',
  'run',
  'task',
  'db_record',
  'evidence'
]

export function isContextKind(type: string): type is ContextEntityKind {
  return (CONTEXT_KINDS as readonly string[]).includes(type)
}

export const overviewApi = {
  snapshot: (projectId: string): Promise<OverviewSnapshot> =>
    ipc.invoke('project.overview', { projectId }),

  /** One degraded card, grafted onto the service's cached snapshot. */
  refreshWidget: (projectId: string, kind: OverviewWidgetKind): Promise<OverviewSnapshot> =>
    ipc.invoke('project.overview', { projectId, refresh: kind }),

  provenance: (
    projectId: string,
    filter: { milestones: boolean; granularity: ProvenanceGranularity; focusIdeaId: string | null }
  ): Promise<ProvenanceReport> =>
    ipc.invoke('project.provenance', {
      projectId,
      milestones: filter.milestones,
      granularity: filter.granularity,
      ...(filter.focusIdeaId === null ? {} : { focusIdeaId: filter.focusIdeaId })
    }),

  context: (
    projectId: string,
    type: ContextEntityKind,
    id: string,
    depth: 1 | 2
  ): Promise<EntityContext & { graph?: ContextGraph }> =>
    ipc.invoke('project.context', {
      projectId,
      entityType: type,
      entityId: id,
      includeGraph: true,
      depth
    }),

  /** The project's own activity timeline. */
  activity: (projectId: string, limit: number): Promise<ActivityPage> =>
    ipc.invoke('project.activity', { projectId, filter: { limit } }),

  quickNote: (projectId: string, title: string): Promise<{ noteId: string }> =>
    ipc.invoke('project.notes.quickAdd', { projectId, title }),

  completeTask: (projectId: string, taskId: string): Promise<unknown> =>
    ipc.invoke('kanban.tasks.complete', { projectId, id: taskId, done: true }),

  updateFocus: (projectId: string, focusMd: string): Promise<unknown> =>
    ipc.invoke('project.update', { id: projectId, patch: { focusMd } }),

  setArchive: (projectId: string): Promise<unknown> =>
    ipc.invoke('project.archive', { id: projectId }),

  setUnarchive: (projectId: string): Promise<unknown> =>
    ipc.invoke('project.unarchive', { id: projectId })
}
