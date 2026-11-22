import type { ActivityActor } from '../activity'
import type { EntityType, Idea, NoteKind, Project, ProjectStatus, RunStatus, Task } from '../entities'
import type { EntityId, IsoDateTime } from '../types/common'

/**
 * Project-domain contracts: lifecycle/statistics/resources/activity,
 * dashboard, §5.6 overview,  provenance,
 * context graph and §5.5 readiness. Every DTO is the camelCase projection of
 * the Go service result; field order mirrors the Go structs.
 */

/* ----------------------------------------------------------------- project */

export interface ProjectPatch {
  name?: string
  summary?: string
  goals?: string
  focusMd?: string
}

/** Per-project aggregate (Go `project.Stats`). */
export interface ProjectStats {
  notes: number
  ideasActive: number
  experiments: number
  papers: number
  tasksOpen: number
  tasksBlocked: number
  trashed: number
  dirBytes: number
  lastActivityAt: IsoDateTime | null
}

/** The single active project session (opening is navigation, never an event). */
export interface ProjectSession {
  project: Project
  readOnly: boolean
  openedAt: IsoDateTime
}

export interface ResourcePaperRef {
  id: EntityId
  title: string
  authors: string
  year: number
  missing: boolean
}

export interface ResourceNoteRef {
  id: EntityId
  title: string
  kind: NoteKind
  updatedAt: IsoDateTime
}

export interface ResourceExperimentRef {
  id: EntityId
  title: string
  lastRunStatus: RunStatus | '—'
  runCount: number
  updatedAt: IsoDateTime
}

export interface ResourceTaskRef {
  id: EntityId
  title: string
  column: string
  priority: number
  deadline: string | null
  blocked: boolean
  updatedAt: IsoDateTime
}

/** Resources page projection (Go `project.Resources`). */
export interface ProjectResources {
  papers: ResourcePaperRef[]
  noteCounts: Record<string, number>
  notes: ResourceNoteRef[]
  experiments: ResourceExperimentRef[]
  taskBuckets: Record<string, number>
  tasks: ResourceTaskRef[]
}

/* ---------------------------------------------------------------- activity */

export type ActivityRouteKind =
  | 'project'
  | 'note'
  | 'idea'
  | 'experiment'
  | 'run'
  | 'task'
  | 'paper'
  | 'weekly_report'
  | 'trash'

export interface ActivityRoute {
  kind: ActivityRouteKind
  projectId: EntityId
  entityId?: EntityId
  params?: Record<string, string>
}

/**
 * One timeline entry with its navigation target. `entityGone` is true for a
 * soft-deleted entity or for every item of an archived project.
 */
export interface ProjectActivityItem {
  id: EntityId
  eventType: string
  entityType: EntityType
  entityId: EntityId
  summary: string
  actor: ActivityActor
  createdAt: IsoDateTime
  entityGone: boolean
  route: ActivityRoute
}

export interface ActivityFilter {
  /** Inclusive lower bound. Defaults to `until - 30 days`. */
  from?: IsoDateTime
  /** Inclusive upper bound. Defaults to now. */
  to?: IsoDateTime
  entityTypes?: string[]
  eventTypes?: string[]
  actors?: ActivityActor[]
  cursor?: string
  /** Default 50, capped at 200. */
  limit?: number
}

export interface ActivityPage {
  items: ProjectActivityItem[]
  nextCursor: string | null
}

/* --------------------------------------------------------------- dashboard */

export interface DashboardProjectCard {
  project: Project
  stats: ProjectStats
  lastActivityAt: IsoDateTime | null
  archived: boolean
  /** Statistics could not be read inside the per-project budget. */
  degraded: boolean
}

export interface DashboardTotals {
  active: number
  archived: number
  notes: number
  papers: number
  openTasks: number
}

export interface DashboardWorkspaceSummary {
  root: string
  projectCount: number
  archivedCount: number
  openProjects: number
  vectorAvailable: boolean
}

/** One Dashboard read (Go `dashboard.Snapshot`). */
export interface DashboardSnapshot {
  projects: DashboardProjectCard[]
  currentProjectId: EntityId | null
  workspace: DashboardWorkspaceSummary | null
  activity: ProjectActivityItem[]
  totals: DashboardTotals
  degradedProjects: EntityId[]
  generatedAt: IsoDateTime
}

/* ---------------------------------------------------------------- overview */

export type OverviewWidgetKind =
  | 'current_focus'
  | 'active_ideas'
  | 'recent_experiments'
  | 'my_tasks'
  | 'recent_notes'
  | 'literature'
  | 'blocked_tasks'
  | 'weekly_progress'

/** The eight widgets in §3.2 display order. */
export const OVERVIEW_WIDGET_ORDER: readonly OverviewWidgetKind[] = [
  'current_focus',
  'active_ideas',
  'recent_experiments',
  'my_tasks',
  'recent_notes',
  'literature',
  'blocked_tasks',
  'weekly_progress'
] as const

export type OverviewWidgetReason =
  | 'module_not_registered'
  | 'pending_confirmation'
  | 'timeout'
  | 'query_error'
  | 'canceled'

export interface OverviewWidgetState {
  kind: OverviewWidgetKind
  available: boolean
  degraded: boolean
  reason: OverviewWidgetReason | null
  pendingConfirmation: boolean
}

export interface OverviewProjectHeader {
  id: EntityId
  name: string
  summary: string
  goals: string
  status: ProjectStatus
  createdAt: IsoDateTime
  archived: boolean
}

export interface OverviewIdeaCard {
  id: EntityId
  title: string
  status: string
  updatedAt: IsoDateTime
}

export interface OverviewExperimentCard {
  id: EntityId
  title: string
  /** Run status, or the em dash placeholder when the experiment has no run. */
  lastRunStatus: string
  runCount: number
  updatedAt: IsoDateTime
}

export interface OverviewTaskCard {
  id: EntityId
  title: string
  column: string
  /** high=3, medium=2, low=1, anything else 0. */
  priority: number
  deadline: string | null
  blocked: boolean
  updatedAt: IsoDateTime
}

export interface OverviewNoteCard {
  id: EntityId
  title: string
  kind: NoteKind
  updatedAt: IsoDateTime
}

export interface OverviewPaperCard {
  id: EntityId
  title: string
  authors: string
  year: number
  missing: boolean
}

export interface OverviewFocusWidget {
  /** goals truncated to 200 runes with a trailing ellipsis. */
  goalsExcerpt: string
  focusMd: string
  focusEditable: boolean
  topIdeas: OverviewIdeaCard[]
}

export interface OverviewTaskWidget {
  weekPlanned: boolean
  items: OverviewTaskCard[]
}

export interface OverviewBlockedWidget {
  items: OverviewTaskCard[]
}

export interface OverviewWeeklyWidget {
  reportId: EntityId | null
  excerpt: string
  confirmed: boolean
  activityCounts: Record<string, number>
}

/** One Overview read (Go `overview.Snapshot`). */
export interface OverviewSnapshot {
  project: OverviewProjectHeader
  writable: boolean
  currentFocus: OverviewFocusWidget
  activeIdeas: OverviewIdeaCard[]
  recentExperiments: OverviewExperimentCard[]
  myTasks: OverviewTaskWidget
  recentNotes: OverviewNoteCard[]
  literature: OverviewPaperCard[]
  blockedTasks: OverviewBlockedWidget
  weeklyProgress: OverviewWeeklyWidget
  /** Always eight entries, in `OVERVIEW_WIDGET_ORDER` order. */
  widgets: OverviewWidgetState[]
  generatedAt: IsoDateTime
}

/* -------------------------------------------------------------- provenance */

/** The eight core milestones. */
export type ProvenanceMilestoneKind =
  | 'paper_imported'
  | 'note_created'
  | 'idea_proposed'
  | 'idea_derived'
  | 'idea_status_changed'
  | 'experiment_analyzed'
  | 'evidence_recorded'
  | 'weekly_confirmed'

export const PROVENANCE_MILESTONE_ORDER: readonly ProvenanceMilestoneKind[] = [
  'paper_imported',
  'note_created',
  'idea_proposed',
  'idea_derived',
  'idea_status_changed',
  'experiment_analyzed',
  'evidence_recorded',
  'weekly_confirmed'
] as const

export interface ProvenanceEntityRef {
  type: string
  id: EntityId
}

export interface ProvenanceNode {
  id: string
  at: IsoDateTime
  kind: ProvenanceMilestoneKind
  entity: ProvenanceEntityRef
  title: string
  summary: string
  badge: string
  linkedNodes: ProvenanceEntityRef[]
  /** Routine edit: note.updated / entity.updated. Dropped by the milestone view. */
  routine: boolean
}

export interface ProvenanceSection {
  /** `YYYY-Www` (week) or `YYYY-MM` (month). */
  key: string
  label: string
  at: IsoDateTime
  nodes: ProvenanceNode[]
}

export interface ProvenanceLineageRef {
  id: EntityId
  title: string
  badge: string
}

export interface ProvenanceLineageNode {
  id: EntityId
  title: string
  status: string
  rejectionReason: string
  supportCount: number
  opposeCount: number
  papers: ProvenanceLineageRef[]
  notes: ProvenanceLineageRef[]
  experiments: ProvenanceLineageRef[]
  children: ProvenanceLineageNode[]
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
}

export interface ProvenanceLineageTree {
  roots: ProvenanceLineageNode[]
}

export type ProvenanceGranularity = 'week' | 'month'
export type ProvenanceFormat = 'json' | 'markdown'

export interface ProvenanceFilter {
  projectId: EntityId
  from?: IsoDateTime
  to?: IsoDateTime
  /** Only replay the ancestry/descendant chain of one idea. */
  focusIdeaId?: EntityId
  /** Drop routine edits (note.updated / entity.updated). */
  milestones?: boolean
  granularity?: ProvenanceGranularity
}

/** One provenance read. `markdown` is filled when format=markdown. */
export interface ProvenanceReport {
  projectId: EntityId
  projectName: string
  from: IsoDateTime | null
  to: IsoDateTime | null
  focusIdeaId: EntityId | null
  milestones: boolean
  nodes: ProvenanceNode[]
  sections: ProvenanceSection[]
  lineage: ProvenanceLineageTree
  focusedLineage: ProvenanceLineageTree
  markdown: string | null
  nodeCount: number
  generatedAt: IsoDateTime
}

/* ------------------------------------------------------------ context graph */

export const CONTEXT_ENTITY_KINDS = [
  'note',
  'paper',
  'idea',
  'experiment',
  'run',
  'task',
  'db_record',
  'evidence'
] as const

export type ContextEntityKind = (typeof CONTEXT_ENTITY_KINDS)[number]

export interface ContextBacklink {
  noteId: EntityId
  noteTitle: string
  noteKind: NoteKind
  /** At most 160 characters around the mention. */
  snippet: string
  viaRelation: boolean
  updatedAt: IsoDateTime
}

export interface ContextIdeaItem {
  id: EntityId
  title: string
  status: string
  rejectionReason: string
  derivedFrom: EntityId | null
  self: boolean
  updatedAt: IsoDateTime
}

export interface ContextExperimentItem {
  id: EntityId
  title: string
  runCount: number
  latestRunId: EntityId | null
  latestStatus: string
  /** `key=value` digest of the newest snapshot, empty when none exists. */
  metric: string
  self: boolean
  updatedAt: IsoDateTime
}

export interface ContextEvidenceItem {
  id: EntityId
  ideaId: EntityId
  direction: 'support' | 'oppose'
  content: string
  confirmed: boolean
  source: ProvenanceEntityRef
  createdAt: IsoDateTime
}

export interface ContextPaperItem {
  id: EntityId
  title: string
  year: string
  hasCopy: boolean
}

export interface ContextTaskItem {
  id: EntityId
  title: string
  column: string
  blocked: boolean
  done: boolean
  relation: string
  updatedAt: IsoDateTime
}

export interface EntityContext {
  target: ProvenanceEntityRef
  title: string
  backlinks: ContextBacklink[]
  ideas: ContextIdeaItem[]
  experiments: ContextExperimentItem[]
  evidences: ContextEvidenceItem[]
  papers: ContextPaperItem[]
  tasks: ContextTaskItem[]
  loadedAt: IsoDateTime
}

export interface ContextGraphNode {
  type: string
  id: EntityId
  title: string
  kind: string
  center: boolean
  /** Hop distance from the inspected entity: 0 center, 1, 2. */
  depth: number
}

export interface ContextGraphEdge {
  from: ProvenanceEntityRef
  to: ProvenanceEntityRef
  relation: string
}

export interface ContextGraph {
  center: ProvenanceEntityRef
  nodes: ContextGraphNode[]
  edges: ContextGraphEdge[]
  /** True when the 24-node cap truncated the graph. */
  truncated: boolean
}

/* --------------------------------------------------------------- readiness */

export type ReadinessRuleId = 'R-01' | 'R-02' | 'R-03' | 'R-04' | 'R-05' | 'R-06'

export type TaskPriorityLevel = 'high' | 'medium' | 'low'

export interface ReadinessCheckItem {
  rule: ReadinessRuleId
  ideaId: EntityId | null
  ideaTitle: string
  experimentId: EntityId | null
  experimentTitle: string
  summary: string
  suggestion: string
  priority: TaskPriorityLevel
  targetEntity: ProvenanceEntityRef
}

export interface IdeaReadiness {
  ideaId: EntityId
  title: string
  status: string
  checked: boolean
  passed: number
  total: number
  items: ReadinessCheckItem[]
}

export interface ReadinessReport {
  projectId: EntityId
  generatedAt: IsoDateTime
  ideaId: EntityId | null
  checkedIdeas: number
  completeIdeas: number
  ideas: IdeaReadiness[]
  items: ReadinessCheckItem[]
}

/** A deterministic task suggestion. Creating it stays a user action. */
export interface TaskSuggestion {
  rule: ReadinessRuleId | 'gap' | 'limitation' | 'todo' | 'idea'
  title: string
  reason: string
  priority: TaskPriorityLevel
  targetEntity: ProvenanceEntityRef
}

/* ------------------------------------------------------------------ delete */

export interface DeleteProjectBlocker {
  code: string
  message: string
}

export interface DeleteProjectPlan {
  projectId: EntityId
  projectName: string
  entityCounts: Record<string, number>
  trashedCount: number
  dirBytes: number
  paperIds: EntityId[]
  sharedPaperIds: EntityId[]
  exclusivePaperIds: EntityId[]
  /** The paper reference set could not be read; retention is unknown. */
  paperImpactUnknown: boolean
  lastExportedAt: IsoDateTime | null
  exportRecommended: boolean
  blockers: DeleteProjectBlocker[]
  preparedAt: IsoDateTime
  /** One-time in-app confirmation token; null for an MCP dry run. */
  confirmationToken: string | null
}

export interface DeleteProjectResult {
  projectId: EntityId
  removedPapers: EntityId[]
  retainedPapers: EntityId[]
  warnings: string[]
  durationMs: number
}

/* --------------------------------------------------------------- commands */

export interface ProjectCommands {
  'project.list': {
    req: { includeArchived?: boolean; query?: string; limit?: number; offset?: number }
    res: Project[]
  }
  'project.get': { req: { id: EntityId }; res: Project }
  'project.create': {
    req: { name: string; summary?: string; goals?: string }
    res: Project
  }
  'project.update': {
    req: { id: EntityId; patch: ProjectPatch; expectedUpdatedAt?: IsoDateTime }
    res: Project
  }
  'project.archive': { req: { id: EntityId }; res: Project }
  'project.unarchive': { req: { id: EntityId }; res: Project }

  'project.delete.plan': { req: { id: EntityId }; res: DeleteProjectPlan }
  'project.delete.confirm': {
    req: { id: EntityId; confirmName: string; token: string }
    res: DeleteProjectResult
  }

  'project.session': { req: { id: EntityId }; res: ProjectSession }
  'project.stats': { req: { id: EntityId }; res: ProjectStats }
  'project.resources': { req: { id: EntityId }; res: ProjectResources }
  'project.activity': { req: { projectId: EntityId; filter?: ActivityFilter }; res: ActivityPage }

  'project.overview': {
    req: { projectId: EntityId; refresh?: OverviewWidgetKind }
    res: OverviewSnapshot
  }
  'project.dashboard': { req: { refresh?: boolean }; res: DashboardSnapshot }
  'project.provenance': { req: ProvenanceFilter & { format?: ProvenanceFormat }; res: ProvenanceReport }
  'project.context': {
    req: {
      projectId: EntityId
      entityType: ContextEntityKind
      entityId: EntityId
      includeGraph?: boolean
      depth?: 1 | 2
    }
    res: EntityContext & { graph?: ContextGraph }
  }
  'project.readiness': { req: { projectId: EntityId; ideaId?: EntityId }; res: ReadinessReport }
  'project.taskSuggestions': {
    req: { projectId: EntityId; ideaId?: EntityId }
    res: TaskSuggestion[]
  }

  'project.tasks.quickAdd': {
    req: { projectId: EntityId; title: string; column?: string; weekPlan?: string }
    res: Task
  }
  'project.notes.quickAdd': {
    req: { projectId: EntityId; title?: string; bodyMd?: string }
    res: { noteId: EntityId }
  }
  'project.ideas.quickAdd': { req: { projectId: EntityId; title: string }; res: Idea }
}
