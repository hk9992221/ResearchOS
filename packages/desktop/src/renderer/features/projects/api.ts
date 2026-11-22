/**
 * Project-domain IPC adapter. Channel names stay here so the dialogs and the
 * page never spell them.
 */

import type {
  DeleteProjectPlan,
  DeleteProjectResult,
  Project,
  ProjectPatch
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export const projectsApi = {
  /** One list read; archived rows are included so the tabs split locally. */
  list: (query: string): Promise<Project[]> =>
    ipc.invoke('project.list', {
      includeArchived: true,
      ...(query.trim() === '' ? {} : { query: query.trim() })
    }),

  create: (input: { name: string; summary?: string; goals?: string }): Promise<Project> =>
    ipc.invoke('project.create', input),

  update: (id: string, patch: ProjectPatch, expectedUpdatedAt?: string): Promise<Project> =>
    ipc.invoke('project.update', {
      id,
      patch,
      ...(expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt })
    }),

  archive: (id: string): Promise<Project> => ipc.invoke('project.archive', { id }),
  unarchive: (id: string): Promise<Project> => ipc.invoke('project.unarchive', { id }),

  /** Two-phase delete: the plan carries the one-shot confirmation token. */
  planDelete: (id: string): Promise<DeleteProjectPlan> =>
    ipc.invoke('project.delete.plan', { id }),

  confirmDelete: (id: string, confirmName: string, token: string): Promise<DeleteProjectResult> =>
    ipc.invoke('project.delete.confirm', { id, confirmName, token })
}