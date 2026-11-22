/**
 * Dashboard IPC adapter and the merged-feed bounds (§5.11).
 *
 * `project.dashboard` carries the first 20 cross-project events. "Load more"
 * reads the same window from each project's activity page and merges it here,
 * because no merged-feed page command exists yet; each per-project read asks
 * for the target size so the merged head is exact up to the 100-row cap.
 */

import type { DashboardSnapshot, ProjectActivityItem } from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export const DASHBOARD_FEED = {
  defaultLimit: 20,
  pageStep: 20,
  maxLimit: 100
} as const

export const homeApi = {
  dashboard: (): Promise<DashboardSnapshot> => ipc.invoke('project.dashboard', {}),

  /** A forced read: the service's 60 s snapshot cache is bypassed. */
  refresh: (): Promise<DashboardSnapshot> => ipc.invoke('project.dashboard', { refresh: true }),

  mergedActivity: async (
    projectIds: readonly string[],
    limit: number
  ): Promise<ProjectActivityItem[]> => {
    const capped = Math.min(Math.max(limit, 1), DASHBOARD_FEED.maxLimit)
    const pages = await Promise.all(
      projectIds.map((projectId) =>
        ipc.invoke('project.activity', { projectId, filter: { limit: capped } })
      )
    )
    return mergeActivity(
      pages.flatMap((page) => page.items),
      capped
    )
  }
}

/** Newest first, one row per event id, capped at `limit`. */
export function mergeActivity(
  items: readonly ProjectActivityItem[],
  limit: number
): ProjectActivityItem[] {
  const seen = new Set<string>()
  const unique: ProjectActivityItem[] = []
  for (const item of items) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    unique.push(item)
  }
  unique.sort((left, right) => {
    if (left.createdAt !== right.createdAt) return left.createdAt > right.createdAt ? -1 : 1
    return left.id > right.id ? -1 : left.id < right.id ? 1 : 0
  })
  return unique.slice(0, Math.min(limit, DASHBOARD_FEED.maxLimit))
}