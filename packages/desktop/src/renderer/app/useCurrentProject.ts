import { useQuery } from '@tanstack/react-query'

import type { Project } from '@researchos/shared'

import { ipc } from '../lib/ipc'
import { queryKeys } from '../lib/query'
import { useShellStore } from '../state/shell'

export interface CurrentProject {
  readonly projectId: string | null
  readonly project: Project | null
  readonly archived: boolean
  readonly isLoading: boolean
  readonly error: Error | null
}

/**
 * useCurrentProject resolves the shell's open project to its stored row. Pages
 * that write project data use it for the archived gate and the read-only
 * banner; a project route without a loaded row is not writable either.
 */
export function useCurrentProject(): CurrentProject {
  const projectId = useShellStore((state) => state.currentProjectId)
  const query = useQuery({
    queryKey: queryKeys.projects.detail(projectId ?? 'none'),
    queryFn: () => ipc.invoke('project.get', { id: projectId as string }),
    enabled: projectId !== null
  })

  return {
    projectId,
    project: query.data ?? null,
    archived: query.data?.status === 'archived',
    isLoading: projectId !== null && query.isPending,
    error: query.error
  }
}