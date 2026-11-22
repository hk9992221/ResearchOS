import { render, type RenderResult } from '@testing-library/react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import type { Project } from '@researchos/shared'

import { I18nProvider } from '../../i18n'
import { createQueryClient } from '../../lib/query'
import { installBridge, makeProject, type FakeBridge } from '../../app/testing'
import { useShellStore } from '../../state/shell'
import ProjectsPage from './ProjectsPage'

export { makeProject }

export interface RenderedProjects {
  readonly result: RenderResult
  readonly queryClient: QueryClient
}

export function renderProjects(): RenderedProjects {
  const queryClient = createQueryClient()
  const result = render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/projects']}>
          <ProjectsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>
  )
  return { result, queryClient }
}

export function installProjectBridge(
  handlers: Record<string, (request: unknown) => unknown>
): FakeBridge {
  useShellStore.setState({ currentProjectId: null })
  return installBridge({ handlers })
}

export function projectRow(overrides: Partial<Project> = {}): Project {
  return { ...makeProject('p1', 'Alpha'), summary: 'Studies contrastive learning', ...overrides }
}