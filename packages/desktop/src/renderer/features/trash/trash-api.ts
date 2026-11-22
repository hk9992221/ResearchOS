import type { TrashEntry, TrashEntityType, TrashPlan, TrashRef } from '@researchos/shared'
import { ipc } from '../../lib/ipc'

/**
 * Recycle-bin adapter.
 *
 * The shared contract does not cover trash yet (reported as a gap): the main
 * process has no `trash.*` handlers. The UI is built against this small
 * interface so the flow, the two-tier confirmation and the tests are real;
 * swapping these calls for typed channels when `@researchos/shared` lands is a
 * one-file change.
 */

export interface TrashApi {
  list(projectId: string): Promise<TrashEntry[]>
  restore(projectId: string, items: readonly TrashRef[]): Promise<{ restored: number }>
  planPurge(projectId: string, items: readonly TrashRef[]): Promise<TrashPlan>
  confirmPurge(token: string): Promise<{ purged: number }>
  planEmpty(projectId: string): Promise<TrashPlan>
  confirmEmpty(token: string): Promise<{ emptied: number }>
}

export type { TrashEntry, TrashEntityType, TrashPlan, TrashRef }

export const trashApi: TrashApi = {
  list: (projectId) => ipc.invoke('trash.list', { projectId }),
  restore: (projectId, items) =>
    ipc.invoke('trash.restore', { projectId, items: [...items] }),
  planPurge: (projectId, items) =>
    ipc.invoke('trash.purge.plan', { projectId, items: [...items] }),
  confirmPurge: (token) => ipc.invoke('trash.purge.confirm', { token }),
  planEmpty: (projectId) => ipc.invoke('trash.empty.plan', { projectId }),
  confirmEmpty: (token) => ipc.invoke('trash.empty.confirm', { token })
}