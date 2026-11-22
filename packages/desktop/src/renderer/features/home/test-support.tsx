import { render, type RenderResult } from '@testing-library/react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import type {
  DashboardProjectCard,
  DashboardSnapshot,
  ProjectActivityItem,
  ProjectStats
} from '@researchos/shared'

import { I18nProvider } from '../../i18n'
import { createQueryClient } from '../../lib/query'
import { installBridge, makeProject, type FakeBridge } from '../../app/testing'
import { useShellStore } from '../../state/shell'
import HomePage from './HomePage'

export { makeProject }

export const NOW = '2026-11-18T09:00:00.000Z'

export function makeStats(overrides: Partial<ProjectStats> = {}): ProjectStats {
  return {
    notes: 5,
    ideasActive: 2,
    experiments: 1,
    papers: 4,
    tasksOpen: 3,
    tasksBlocked: 0,
    trashed: 0,
    dirBytes: 1024,
    lastActivityAt: NOW,
    ...overrides
  }
}

export function makeDashboardCard(
  id: string,
  name: string,
  overrides: Partial<DashboardProjectCard> = {}
): DashboardProjectCard {
  return {
    project: makeProject(id, name),
    stats: makeStats(),
    lastActivityAt: NOW,
    archived: false,
    degraded: false,
    ...overrides
  }
}

export function makeActivity(
  id: string,
  projectId: string,
  createdAt: string,
  overrides: Partial<ProjectActivityItem> = {}
): ProjectActivityItem {
  return {
    id,
    eventType: 'note.created',
    entityType: 'note',
    entityId: `n-${id}`,
    summary: `Note ${id}`,
    actor: 'user',
    createdAt,
    entityGone: false,
    route: { kind: 'note', projectId, entityId: `n-${id}` },
    ...overrides
  }
}

export function makeDashboard(overrides: Partial<DashboardSnapshot> = {}): DashboardSnapshot {
  const projects = overrides.projects ?? [makeDashboardCard('p1', 'Alpha')]
  return {
    projects,
    currentProjectId: 'p1',
    workspace: {
      root: 'F:/Workspace',
      projectCount: projects.length,
      archivedCount: 0,
      openProjects: 1,
      vectorAvailable: true
    },
    activity: [],
    totals: { active: 1, archived: 0, notes: 5, papers: 4, openTasks: 3 },
    degradedProjects: [],
    generatedAt: NOW,
    ...overrides
  }
}

export interface RenderedHome {
  readonly result: RenderResult
  readonly queryClient: QueryClient
}

export function renderHome(): RenderedHome {
  useShellStore.setState({ currentProjectId: 'p1' })
  const queryClient = createQueryClient()
  const result = render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/']}>
          <HomePage />
        </MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>
  )
  return { result, queryClient }
}

export function installHomeBridge(
  handlers: Record<string, (request: unknown) => unknown>
): FakeBridge {
  return installBridge({ handlers })
}