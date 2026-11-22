import { render, type RenderResult } from '@testing-library/react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'

import type {
  OverviewSnapshot,
  ProvenanceLineageNode,
  ProvenanceReport,
  ProjectActivityItem
} from '@researchos/shared'
import { OVERVIEW_WIDGET_ORDER } from '@researchos/shared'

import { I18nProvider } from '../../i18n'
import { createQueryClient } from '../../lib/query'
import { installBridge, type FakeBridge } from '../../app/testing'
import { useShellStore } from '../../state/shell'
import OverviewPage from './OverviewPage'

export const NOW = '2026-11-18T09:00:00.000Z'

export function makeOverview(overrides: Partial<OverviewSnapshot> = {}): OverviewSnapshot {
  return {
    project: {
      id: 'p1',
      name: 'Alpha',
      summary: 'Studies contrastive learning',
      goals: 'Publish one paper',
      status: 'active',
      createdAt: NOW,
      archived: false
    },
    writable: true,
    currentFocus: { goalsExcerpt: '', focusMd: '**Focus** on the baseline', focusEditable: true, topIdeas: [] },
    activeIdeas: [{ id: 'i1', title: 'Contrastive baseline', status: 'Researching', updatedAt: NOW }],
    recentExperiments: [
      { id: 'e1', title: 'Baseline run', lastRunStatus: 'finished', runCount: 2, updatedAt: NOW }
    ],
    myTasks: {
      weekPlanned: false,
      items: [
        {
          id: 't1',
          title: 'Write report',
          column: 'Todo',
          priority: 2,
          deadline: null,
          blocked: false,
          updatedAt: NOW
        }
      ]
    },
    recentNotes: [{ id: 'n1', title: 'Old note', kind: 'scratchpad', updatedAt: NOW }],
    literature: [{ id: 'paper-1', title: 'SimCLR', authors: 'Chen et al.', year: 2020, missing: false }],
    blockedTasks: {
      items: [
        {
          id: 't2',
          title: 'Waiting on data',
          column: 'Todo',
          priority: 3,
          deadline: null,
          blocked: true,
          updatedAt: NOW
        }
      ]
    },
    weeklyProgress: {
      reportId: null,
      excerpt: '',
      confirmed: false,
      activityCounts: { note: 4, task: 2 }
    },
    widgets: OVERVIEW_WIDGET_ORDER.map((kind) => ({
      kind,
      available: true,
      degraded: false,
      reason: null,
      pendingConfirmation: false
    })),
    generatedAt: NOW,
    ...overrides
  }
}

export function makeLineageNode(
  id: string,
  title: string,
  overrides: Partial<ProvenanceLineageNode> = {}
): ProvenanceLineageNode {
  return {
    id,
    title,
    status: 'Researching',
    rejectionReason: '',
    supportCount: 2,
    opposeCount: 0,
    papers: [],
    notes: [],
    experiments: [],
    children: [],
    createdAt: NOW,
    updatedAt: NOW,
    ...overrides
  }
}

export function makeProvenance(overrides: Partial<ProvenanceReport> = {}): ProvenanceReport {
  const root = makeLineageNode('i1', 'Alpha idea', {
    children: [makeLineageNode('i2', 'Beta idea')]
  })
  return {
    projectId: 'p1',
    projectName: 'Alpha',
    from: null,
    to: null,
    focusIdeaId: null,
    milestones: false,
    nodes: [
      {
        id: 'node-1',
        at: '2026-11-18T08:30:00.000Z',
        kind: 'idea_proposed',
        entity: { type: 'idea', id: 'i1' },
        title: 'Alpha idea',
        summary: 'Proposed from the literature review',
        badge: '',
        linkedNodes: [],
        routine: false
      }
    ],
    sections: [
      {
        key: '2026-W47',
        label: '2026-W47',
        at: '2026-11-18T08:30:00.000Z',
        nodes: [
          {
            id: 'node-1',
            at: '2026-11-18T08:30:00.000Z',
            kind: 'idea_proposed',
            entity: { type: 'idea', id: 'i1' },
            title: 'Alpha idea',
            summary: 'Proposed from the literature review',
            badge: '',
            linkedNodes: [],
            routine: false
          }
        ]
      }
    ],
    lineage: { roots: [root] },
    focusedLineage: { roots: [root] },
    markdown: null,
    nodeCount: 1,
    generatedAt: NOW,
    ...overrides
  }
}

export function makeFeedItem(id: string): ProjectActivityItem {
  return {
    id,
    eventType: 'note.created',
    entityType: 'note',
    entityId: 'n1',
    summary: `Note ${id}`,
    actor: 'user',
    createdAt: NOW,
    entityGone: false,
    route: { kind: 'note', projectId: 'p1', entityId: 'n1' }
  }
}

function RouteProbe(): JSX.Element {
  const location = useLocation()
  return <div data-testid="route-probe">{`${location.pathname}${location.search}`}</div>
}

export interface RenderedOverview {
  readonly result: RenderResult
  readonly queryClient: QueryClient
}

export function renderOverview(entry = '/p/p1/overview'): RenderedOverview {
  useShellStore.setState({ currentProjectId: 'p1' })
  const queryClient = createQueryClient()
  const result = render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path="/p/:projectId/overview" element={<OverviewPage />} />
            <Route path="*" element={<RouteProbe />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>
  )
  return { result, queryClient }
}

export function installOverviewBridge(
  handlers: Record<string, (request: unknown) => unknown>
): FakeBridge {
  return installBridge({ handlers })
}

/** React Flow measures its container; jsdom has no ResizeObserver. */
export function installLayoutStubs(): void {
  if (typeof globalThis.ResizeObserver !== 'undefined') return
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver
}