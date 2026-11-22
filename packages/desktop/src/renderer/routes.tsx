/**
 * The route registry.
 *
 * `RouteKind` mirrors the destinations of the Go shell
 * (internal/ports/ports.go plus internal/ui/app/app.go); every entry keeps the
 * Go title key, icon and sidebar group, so the sidebar, breadcrumb and command
 * palette cannot drift from the reference UI.
 *
 * Two destinations are additions of the Electron port:
 *   - `plugin_market` was a hidden RouteKind in the Go shell and is now the
 *     real `/plugins` route.
 *   - `pdf` is the paper viewer detail route (plan §3.4).
 *
 * A navigation is not an Activity: nothing here writes to the audit log. The
 * `feature` slot is where a wave-5 feature module registers its lazy element;
 * until then the shared outlet renders the honest shell empty state.
 */

import { lazy, type ComponentType, type LazyExoticComponent } from 'react'
import { matchPath } from 'react-router-dom'

import type { IconName } from './app/icons'

export type RouteKind =
  | 'home'
  | 'projects'
  | 'inbox'
  | 'plugin_market'
  | 'overview'
  | 'literature'
  | 'notes'
  | 'ideas'
  | 'experiments'
  | 'kanban'
  | 'weekly_review'
  | 'export'
  | 'settings'
  | 'trash'
  | 'diagnostics'
  | 'migration'
  | 'consistency'
  | 'help'
  | 'note'
  | 'paper'
  | 'pdf'
  | 'idea'
  | 'experiment'
  | 'run'
  | 'task'
  | 'weekly_report'
  | 'plugin_project'
  | 'plugin_workspace'

export type RouteGroup = 'workspace' | 'project' | 'system' | 'detail'

/** Whether a destination needs an open project. */
export type RouteScope = 'global' | 'project'

export interface RouteFeatureProps {
  readonly projectId?: string
  readonly entityId?: string
  readonly pluginId?: string
  readonly entry?: string
}

/** A wave-5 feature element; `lazy` keeps its chunk out of the shell bundle. */
export type RouteFeature = LazyExoticComponent<ComponentType<RouteFeatureProps>>

export interface RouteEntry {
  readonly kind: RouteKind
  /** Absolute hash path; `:projectId` / `:entityId` are the only params. */
  readonly path: string
  /** The single source of the destination's name (internal/i18n convention). */
  readonly titleKey: string
  readonly icon: IconName
  readonly group: RouteGroup
  readonly scope: RouteScope
  /** Shown in the sidebar. Hidden kinds stay reachable from the palette. */
  readonly nav: boolean
  /** List route a detail route belongs to, for sidebar active state. */
  readonly parent?: RouteKind
  /** The module named by the shell's placeholder, when it is not built yet. */
  readonly downstreamKey?: string
  readonly feature?: RouteFeature
}

const SettingsFeature = featureRoute(() => import('./features/settings/SettingsPage'))
const TrashFeature = featureRoute(() => import('./features/trash/TrashPage'))
const ConsistencyFeature = featureRoute(() => import('./features/consistency/ConsistencyPage'))
const DiagnosticsFeature = featureRoute(() => import('./features/diagnostics/DiagnosticsPage'))
const MigrationFeature = featureRoute(() => import('./features/migration/MigrationPage'))
const ExportFeature = featureRoute(() => import('./features/export/ExportPage'))
const ExperimentsFeature = featureRoute(() => import('./features/experiments/ExperimentsPage'))
const ExperimentDetailFeature = featureRoute(
  () => import('./features/experiments/ExperimentDetailPage')
)
const RunDetailFeature = featureRoute(() => import('./features/experiments/RunDetailPage'))
const HomeFeature = featureRoute(() => import('./features/home/HomePage'))
const ProjectsFeature = featureRoute(() => import('./features/projects/ProjectsPage'))
const OverviewFeature = featureRoute(() => import('./features/overview/OverviewPage'))
const IdeasFeature = featureRoute(() => import('./features/ideas/IdeasPage'))
const IdeaDetailFeature = featureRoute(() => import('./features/ideas/IdeaDetailPage'))
const NotesFeature = featureRoute(() => import('./features/notes/NotesPage'))
const NoteEditorFeature = featureRoute(() => import('./features/notes/NoteEditorPage'))
const LiteratureFeature = featureRoute(() => import('./features/literature/LiteratureEntry'))
const PaperFeature = featureRoute(() => import('./features/paper/PaperPage'))
const PdfFeature = featureRoute(() => import('./features/paper/pdf/PdfPage'))
const PluginsFeature = featureRoute(
  () => import('./features/plugins/PluginManagerPage').then((module) => ({ default: module.PluginManagerPage }))
)
const PluginHostFeature = featureRoute(
  () => import('./features/plugins/PluginHostPage').then((module) => ({ default: module.PluginHostPage }))
)

export const ROUTE_REGISTRY: readonly RouteEntry[] = [
  {
    kind: 'home',
    path: '/',
    titleKey: 'shell.page.home',
    icon: 'home',
    group: 'workspace',
    scope: 'global',
    nav: true,
    downstreamKey: 'shell.downstream.projects',
    feature: HomeFeature
  },
  {
    kind: 'projects',
    path: '/projects',
    titleKey: 'shell.page.projects',
    icon: 'projects',
    group: 'workspace',
    scope: 'global',
    nav: true,
    downstreamKey: 'shell.downstream.projects',
    feature: ProjectsFeature
  },
  // Hidden in the Go sidebar; the route and its palette entry remain.
  {
    kind: 'inbox',
    path: '/inbox',
    titleKey: 'shell.page.inbox',
    icon: 'inbox',
    group: 'workspace',
    scope: 'global',
    nav: false,
    downstreamKey: 'shell.downstream.inbox'
  },
  {
    kind: 'overview',
    path: '/p/:projectId/overview',
    titleKey: 'shell.page.overview',
    icon: 'overview',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.projects',
    feature: OverviewFeature
  },
  {
    kind: 'literature',
    path: '/p/:projectId/literature',
    titleKey: 'shell.page.literature',
    icon: 'literature',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.literature',
    feature: LiteratureFeature
  },
  {
    kind: 'notes',
    path: '/p/:projectId/notes',
    titleKey: 'shell.page.notes',
    icon: 'note',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.notes',
    feature: NotesFeature
  },
  {
    kind: 'ideas',
    path: '/p/:projectId/ideas',
    titleKey: 'shell.page.ideas',
    icon: 'ideas',
    group: 'project',
    scope: 'project',
    nav: true,
    feature: IdeasFeature
  },
  {
    kind: 'experiments',
    path: '/p/:projectId/experiments',
    titleKey: 'shell.page.experiments',
    icon: 'experiments',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.experiments',
    feature: ExperimentsFeature
  },
  {
    kind: 'kanban',
    path: '/p/:projectId/kanban',
    titleKey: 'shell.page.kanban',
    icon: 'kanban',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.kanban',
    feature: featureRoute(() => import('./features/kanban/KanbanPage'))
  },
  {
    kind: 'weekly_review',
    path: '/p/:projectId/weekly',
    titleKey: 'shell.page.weekly_review',
    icon: 'weeklyReview',
    group: 'project',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.weekly_review',
    feature: featureRoute(() => import('./features/weekly/WeeklyReviewPage'))
  },
  // Reached from a project row and the overview header, not the sidebar.
  {
    kind: 'export',
    path: '/p/:projectId/export',
    titleKey: 'shell.page.export',
    icon: 'externalLink',
    group: 'project',
    scope: 'project',
    nav: false,
    downstreamKey: 'shell.downstream.default',
    feature: ExportFeature
  },

  {
    kind: 'plugin_workspace',
    path: '/plugins/:pluginId/:entry',
    titleKey: 'pages.plugins.title',
    icon: 'pluginMarket',
    group: 'detail',
    scope: 'global',
    nav: false,
    feature: PluginHostFeature
  },
  {
    kind: 'plugin_project',
    path: '/projects/:projectId/plugins/:pluginId/:entry',
    titleKey: 'pages.plugins.title',
    icon: 'pluginMarket',
    group: 'detail',
    scope: 'project',
    nav: false,
    feature: PluginHostFeature
  },
  {
    kind: 'plugin_market',
    path: '/plugins',
    titleKey: 'pages.plugins.title',
    icon: 'pluginMarket',
    group: 'system',
    scope: 'global',
    nav: true,
    feature: PluginsFeature
  },
  {
    kind: 'trash',
    path: '/p/:projectId/trash',
    titleKey: 'shell.page.trash',
    icon: 'trash',
    group: 'system',
    scope: 'project',
    nav: true,
    downstreamKey: 'shell.downstream.system_pages',
    feature: TrashFeature
  },
  {
    kind: 'settings',
    path: '/settings',
    titleKey: 'shell.page.settings',
    icon: 'settings',
    group: 'system',
    scope: 'global',
    nav: true,
    downstreamKey: 'shell.downstream.system_pages',
    feature: SettingsFeature
  },
  // Reached from Settings and the command palette (Go keeps them out of the
  // sidebar).
  {
    kind: 'diagnostics',
    path: '/diagnostics',
    titleKey: 'shell.page.diagnostics',
    icon: 'diagnostics',
    group: 'system',
    scope: 'global',
    nav: false,
    downstreamKey: 'shell.downstream.system_pages',
    feature: DiagnosticsFeature
  },
  {
    kind: 'migration',
    path: '/migration',
    titleKey: 'pages.migration.title',
    icon: 'settings',
    group: 'system',
    scope: 'global',
    nav: false,
    feature: MigrationFeature
  },
  {
    kind: 'consistency',
    path: '/p/:projectId/consistency',
    titleKey: 'shell.page.consistency',
    icon: 'scan',
    group: 'system',
    scope: 'project',
    nav: false,
    downstreamKey: 'shell.downstream.system_pages',
    feature: ConsistencyFeature
  },
  {
    kind: 'help',
    path: '/help',
    titleKey: 'shell.page.help',
    icon: 'help',
    group: 'system',
    scope: 'global',
    nav: true,
    downstreamKey: 'shell.downstream.default'
  },

  // Detail destinations. Each one is a wave-16 feature; the shell keeps the
  // route (and the parent's sidebar selection) ready for it.
  {
    kind: 'note',
    path: '/p/:projectId/note/:entityId',
    titleKey: 'shell.page.note',
    icon: 'note',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'notes',
    downstreamKey: 'shell.downstream.notes',
    feature: NoteEditorFeature
  },
  {
    kind: 'paper',
    path: '/p/:projectId/paper/:entityId',
    titleKey: 'shell.page.paper',
    icon: 'literature',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'literature',
    downstreamKey: 'shell.downstream.literature',
    feature: PaperFeature
  },
  {
    kind: 'pdf',
    path: '/p/:projectId/paper/:entityId/pdf',
    titleKey: 'pages.pdf.title',
    icon: 'fileText',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'literature',
    downstreamKey: 'shell.downstream.literature',
    feature: PdfFeature
  },
  {
    kind: 'idea',
    path: '/p/:projectId/idea/:entityId',
    titleKey: 'shell.page.idea',
    icon: 'ideas',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'ideas',
    feature: IdeaDetailFeature
  },
  {
    kind: 'experiment',
    path: '/p/:projectId/experiment/:entityId',
    titleKey: 'shell.page.experiment',
    icon: 'experiments',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'experiments',
    downstreamKey: 'shell.downstream.experiments',
    feature: ExperimentDetailFeature
  },
  {
    kind: 'run',
    path: '/p/:projectId/run/:entityId',
    titleKey: 'shell.page.run',
    icon: 'loader',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'experiments',
    downstreamKey: 'shell.downstream.experiments',
    feature: RunDetailFeature
  },
  {
    kind: 'task',
    path: '/p/:projectId/task/:entityId',
    titleKey: 'shell.page.task',
    icon: 'check',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'kanban',
    downstreamKey: 'shell.downstream.kanban',
    feature: featureRoute(() => import('./features/kanban/TaskDetail'))
  },
  {
    kind: 'weekly_report',
    path: '/p/:projectId/weekly-report/:entityId',
    titleKey: 'shell.page.weekly_report',
    icon: 'weeklyReview',
    group: 'detail',
    scope: 'project',
    nav: false,
    parent: 'weekly_review',
    downstreamKey: 'shell.downstream.weekly_review',
    feature: featureRoute(() => import('./features/weekly/WeeklyReportPage'))
  }
]

/** The sidebar groups, in design order (§6.3). */
export const NAV_GROUPS: readonly RouteGroup[] = ['workspace', 'project', 'system']

export function routeEntry(kind: RouteKind): RouteEntry {
  const entry = ROUTE_REGISTRY.find((candidate) => candidate.kind === kind)
  if (entry === undefined) throw new Error(`unknown route kind: ${kind}`)
  return entry
}

/** routeHref fills a route's params; undefined when a required one is missing. */
export function routeHref(
  kind: RouteKind,
  params?: { projectId?: string; entityId?: string }
): string | undefined {
  const entry = routeEntry(kind)
  let href = entry.path
  if (entry.scope === 'project') {
    if (params?.projectId === undefined) return undefined
    href = href.replace(':projectId', encodeURIComponent(params.projectId))
  }
  if (href.includes(':entityId')) {
    if (params?.entityId === undefined) return undefined
    href = href.replace(':entityId', encodeURIComponent(params.entityId))
  }
  return href
}

export interface ResolvedRoute {
  readonly entry: RouteEntry
  readonly params: Readonly<Record<string, string | undefined>>
}

/** resolveRoute maps a hash pathname to its registry entry. */
export function resolveRoute(pathname: string): ResolvedRoute | undefined {
  for (const entry of ROUTE_REGISTRY) {
    const match = matchPath({ path: entry.path, end: true }, pathname)
    if (match !== null) return { entry, params: match.params }
  }
  return undefined
}

/**
 * featureRoute is the wave-5 registration seam: a feature module passes its
 * lazy import and the registry entry gains its element.
 */
export function featureRoute(
  loader: () => Promise<{ default: ComponentType<RouteFeatureProps> }>
): RouteFeature {
  return lazy(loader)
}