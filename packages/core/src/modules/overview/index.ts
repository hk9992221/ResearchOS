import type {
  OverviewBlockedWidget,
  OverviewExperimentCard,
  OverviewFocusWidget,
  OverviewIdeaCard,
  OverviewNoteCard,
  OverviewPaperCard,
  OverviewProjectHeader,
  OverviewSnapshot,
  OverviewTaskCard,
  OverviewTaskWidget,
  OverviewWeeklyWidget,
  OverviewWidgetKind,
  OverviewWidgetReason,
  OverviewWidgetState
} from '@researchos/shared'
import { OVERVIEW_WIDGET_ORDER } from '@researchos/shared'

import { formatTimestamp, parseTimestamp, type Clock } from '../../clock'
import { AppErrorCode } from '@researchos/shared'
import { appError } from '../../errors'
import type { Db } from '../../db/open'
import type { WorkspaceManager } from '../../workspace'
import { parsePaperMetadata, runeCount, taskTerminalScope } from '../project/service'
import type { NoteCreatorPort, TaskCompleterPort } from '../project/ports'

/**
 * Project Overview: the eight fixed widgets of §3.2, their
 * per-widget budget with per-widget degradation, the 15 s snapshot cache and the
 * two quick actions that run through the notes/kanban ports.
 *
 * The module reads global.db.projects itself (self-contained header) and only
 * ever reads project.db. Every write belongs to the module that owns the entity.
 */

const DEFAULT_CACHE_TTL_MS = 15_000
const DEFAULT_WIDGET_BUDGET_MS = 500

const FOCUS_TOP_IDEAS = 3
const WIDGET_LIMIT = 5
const MY_TASKS_LIMIT = 8
const GOALS_EXCERPT_RUNES = 200
const REPORT_EXCERPT_RUNES = 400
const NO_RUN_PLACEHOLDER = '—'

/** One linked paper as the literature widget needs it. */
export interface OverviewPaperIndexPort {
  listProjectPapers(projectId: string, limit: number): OverviewPaperCard[]
}

export interface OverviewOptions {
  workspace: WorkspaceManager
  clock?: Clock
  papers?: OverviewPaperIndexPort
  notes?: NoteCreatorPort
  tasks?: TaskCompleterPort
  /** Overrides the §5.8 read-only guard of the quick actions. */
  checkWritable?: (projectId: string) => void
  cacheTtlMs?: number
  widgetBudgetMs?: number
  /** Test seam: runs inside every widget load before its query. */
  beforeWidget?: (kind: OverviewWidgetKind) => void
}

interface CacheEntry {
  snapshot: OverviewSnapshot
  storedAt: number
}

interface HeaderRow {
  header: OverviewProjectHeader
  goals: string
  focusMd: string
  writable: boolean
}

interface WidgetOutcome {
  state: OverviewWidgetState
  focus?: OverviewFocusWidget
  ideas?: OverviewIdeaCard[]
  experiments?: OverviewExperimentCard[]
  myTasks?: OverviewTaskWidget
  notes?: OverviewNoteCard[]
  literature?: OverviewPaperCard[]
  blocked?: OverviewBlockedWidget
  weekly?: OverviewWeeklyWidget
}

export class OverviewService {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock
  private readonly papers: OverviewPaperIndexPort | undefined
  private readonly notes: NoteCreatorPort | undefined
  private readonly tasks: TaskCompleterPort | undefined
  private readonly checkWritable: ((projectId: string) => void) | undefined
  private readonly cacheTtlMs: number
  private readonly widgetBudgetMs: number
  private readonly beforeWidget: ((kind: OverviewWidgetKind) => void) | undefined
  private readonly cache = new Map<string, CacheEntry>()

  constructor(options: OverviewOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? options.workspace.clock
    this.papers = options.papers
    this.notes = options.notes
    this.tasks = options.tasks
    this.checkWritable = options.checkWritable
    this.cacheTtlMs =
      options.cacheTtlMs !== undefined && options.cacheTtlMs > 0 ? options.cacheTtlMs : DEFAULT_CACHE_TTL_MS
    this.widgetBudgetMs =
      options.widgetBudgetMs !== undefined && options.widgetBudgetMs > 0
        ? options.widgetBudgetMs
        : DEFAULT_WIDGET_BUDGET_MS
    this.beforeWidget = options.beforeWidget
  }

  /** The Overview snapshot of one project, or the cached copy while fresh. */
  load(projectId: string, options: { refresh?: OverviewWidgetKind } = {}): OverviewSnapshot {
    const header = this.header(projectId)
    if (options.refresh !== undefined) return this.refresh(projectId, options.refresh)
    const cached = this.cache.get(projectId)
    if (cached !== undefined && this.clock.now().getTime() - cached.storedAt < this.cacheTtlMs) {
      return cached.snapshot
    }
    const previous = cached?.snapshot ?? null
    const outcomes = this.runWidgets(header, [...OVERVIEW_WIDGET_ORDER], previous)
    const snapshot = assemble(header, outcomes, formatTimestamp(this.clock.now()))
    this.put(projectId, snapshot)
    return snapshot
  }

  /** Recomputes one widget; the other seven reuse the cached payload. */
  refresh(projectId: string, kind: OverviewWidgetKind): OverviewSnapshot {
    if (!OVERVIEW_WIDGET_ORDER.includes(kind)) {
      throw appError(AppErrorCode.InvalidArgument, `overview: unknown widget ${kind}`, {
        details: { field: 'refresh', widget: kind }
      })
    }
    const header = this.header(projectId)
    const previous = this.cache.get(projectId)?.snapshot ?? null
    if (previous === null) return this.load(projectId)
    const outcome = this.runWidgets(header, [kind], previous)[kind]!
    const snapshot = cloneSnapshot(previous)
    snapshot.project = header.header
    snapshot.writable = header.writable
    snapshot.currentFocus.focusEditable = header.writable
    snapshot.widgets[indexOf(kind)] = outcome.state
    applyWidget(snapshot, kind, outcome)
    snapshot.generatedAt = formatTimestamp(this.clock.now())
    this.put(projectId, snapshot)
    return snapshot
  }

  /** Drops the cached snapshot of one project. */
  invalidate(projectId: string): void {
    this.cache.delete(projectId)
  }

  /* ------------------------------------------------------- quick actions */

  /** Adds a scratchpad note through the notes port (actor=user). */
  quickAddNote(projectId: string, input: { title?: string; bodyMd?: string }): { noteId: string } {
    this.guardWritable(projectId)
    if (this.notes === undefined) {
      throw appError(AppErrorCode.Unsupported, '笔记模块未就绪', { details: { feature: 'notes' } })
    }
    const title = (input.title ?? '').trim() || `Quick note ${formatTimestamp(this.clock.now()).slice(0, 16).replace('T', ' ')}`
    const created = this.notes.createScratchpad({
      projectId,
      title,
      bodyMd: input.bodyMd ?? '',
      actor: 'user'
    })
    this.invalidate(projectId)
    return { noteId: created.id }
  }

  /** Ticks a task through the kanban port (actor=user). */
  completeTask(projectId: string, taskId: string): void {
    this.guardWritable(projectId)
    if (this.tasks === undefined) {
      throw appError(AppErrorCode.Unsupported, '看板模块未就绪', { details: { feature: 'kanban' } })
    }
    this.tasks.completeTask({ projectId, taskId, actor: 'user' })
    this.invalidate(projectId)
  }

  private guardWritable(projectId: string): void {
    if (this.checkWritable !== undefined) {
      this.checkWritable(projectId)
      return
    }
    const header = this.header(projectId)
    if (header.header.archived) throw archivedError(projectId)
  }

  /* ------------------------------------------------------------- widgets */

  private runWidgets(
    header: HeaderRow,
    wanted: OverviewWidgetKind[],
    previous: OverviewSnapshot | null
  ): Partial<Record<OverviewWidgetKind, WidgetOutcome>> {
    const results: Partial<Record<OverviewWidgetKind, WidgetOutcome>> = {}
    const loader = new WidgetLoader(this.workspace, this.clock, header, this.papers)
    for (const kind of wanted) {
      const started = performance.now()
      try {
        this.beforeWidget?.(kind)
        const outcome = loader.load(kind)
        const elapsed = performance.now() - started
        if (elapsed > this.widgetBudgetMs) {
          results[kind] = degrade(kind, previous, 'timeout')
        } else {
          results[kind] = outcome
        }
      } catch (error) {
        results[kind] = degrade(kind, previous, classify(error))
      }
    }
    return results
  }

  private header(projectId: string): HeaderRow {
    const row = this.workspace.global.db
      .prepare(
        `SELECT id, name, summary, goals, focus_md, status, created_at FROM projects WHERE id = ?`
      )
      .get(projectId) as
      | {
          id: string
          name: string
          summary: string
          goals: string
          focus_md: string
          status: string
          created_at: string
        }
      | undefined
    if (row === undefined) {
      throw appError(AppErrorCode.NotFound, `项目不存在：${projectId}`, { details: { projectId } })
    }
    return {
      header: {
        id: row.id,
        name: row.name,
        summary: row.summary,
        goals: row.goals,
        status: row.status === 'archived' ? 'archived' : 'active',
        createdAt: row.created_at,
        archived: row.status === 'archived'
      },
      goals: row.goals,
      focusMd: row.focus_md,
      writable: row.status !== 'archived'
    }
  }

  private put(projectId: string, snapshot: OverviewSnapshot): void {
    this.cache.set(projectId, { snapshot, storedAt: this.clock.now().getTime() })
  }
}

class WidgetLoader {
  constructor(
    private readonly workspace: WorkspaceManager,
    private readonly clock: Clock,
    private readonly header: HeaderRow,
    private readonly papers: OverviewPaperIndexPort | undefined
  ) {}

  load(kind: OverviewWidgetKind): WidgetOutcome {
    const db = this.projectDb()
    switch (kind) {
      case 'current_focus':
        return this.currentFocus(db)
      case 'active_ideas':
        return this.activeIdeas(db)
      case 'recent_experiments':
        return this.recentExperiments(db)
      case 'my_tasks':
        return this.myTasks(db)
      case 'recent_notes':
        return this.recentNotes(db)
      case 'literature':
        return this.literature(db)
      case 'blocked_tasks':
        return this.blockedTasks(db)
      case 'weekly_progress':
        return this.weeklyProgress(db)
    }
  }

  private projectDb(): Db {
    return this.workspace.project(this.header.header.id).db
  }

  private currentFocus(db: Db): WidgetOutcome {
    return {
      state: okState('current_focus'),
      focus: {
        goalsExcerpt: truncateRunes(this.header.goals, GOALS_EXCERPT_RUNES),
        focusMd: this.header.focusMd,
        focusEditable: this.header.writable,
        topIdeas: this.ideas(db, FOCUS_TOP_IDEAS)
      }
    }
  }

  private activeIdeas(db: Db): WidgetOutcome {
    return { state: okState('active_ideas'), ideas: this.ideas(db, WIDGET_LIMIT) }
  }

  private ideas(db: Db, limit: number): OverviewIdeaCard[] {
    return (
      db
        .prepare(
          `SELECT id, title, status, updated_at FROM ideas
            WHERE status IN ('Researching','Validating') AND deleted_at IS NULL
            ORDER BY updated_at DESC, id LIMIT ?`
        )
        .all(limit) as Array<{ id: string; title: string; status: string; updated_at: string }>
    ).map((row) => ({ id: row.id, title: row.title, status: row.status, updatedAt: row.updated_at }))
  }

  private recentExperiments(db: Db): WidgetOutcome {
    const cards = (
      db
        .prepare(
          `SELECT e.id, e.title, e.updated_at,
                  (SELECT r.status FROM runs r
                    WHERE r.experiment_id = e.id AND r.deleted_at IS NULL
                    ORDER BY r.updated_at DESC, r.created_at DESC, r.id LIMIT 1) AS latest,
                  (SELECT count(*) FROM runs r
                    WHERE r.experiment_id = e.id AND r.deleted_at IS NULL) AS run_count
             FROM experiments e
            WHERE e.deleted_at IS NULL
            ORDER BY e.updated_at DESC, e.id LIMIT ?`
        )
        .all(WIDGET_LIMIT) as Array<{
        id: string
        title: string
        updated_at: string
        latest: string | null
        run_count: number
      }>
    ).map<OverviewExperimentCard>((row) => ({
      id: row.id,
      title: row.title,
      lastRunStatus: row.latest === null || row.latest === '' ? NO_RUN_PLACEHOLDER : row.latest,
      runCount: Number(row.run_count),
      updatedAt: row.updated_at
    }))
    const runs = db.prepare(`SELECT count(*) AS n FROM runs WHERE deleted_at IS NULL`).get() as { n: number }
    const state = Number(runs.n) === 0 ? pendingState('recent_experiments') : okState('recent_experiments')
    return { state, experiments: cards }
  }

  private myTasks(db: Db): WidgetOutcome {
    const items = this.taskRows(
      db,
      `deleted_at IS NULL`,
      ` ORDER BY (deadline IS NULL OR deadline = ''), deadline, ${PRIORITY_RANK_SQL} DESC, updated_at DESC, id`,
      MY_TASKS_LIMIT
    )
    return { state: pendingState('my_tasks'), myTasks: { weekPlanned: false, items } }
  }

  private blockedTasks(db: Db): WidgetOutcome {
    const items = this.taskRows(db, `blocked = 1 AND deleted_at IS NULL`, ` ORDER BY updated_at DESC, id`, WIDGET_LIMIT)
    return { state: pendingState('blocked_tasks'), blocked: { items } }
  }

  private taskRows(db: Db, where: string, order: string, limit: number): OverviewTaskCard[] {
    const rows = db
      .prepare(
        `SELECT id, title, "column" AS column_name, priority, deadline, blocked, updated_at
           FROM tasks WHERE ${where}${taskTerminalScope(db)}${order} LIMIT ?`
      )
      .all(limit) as Array<{
      id: string
      title: string
      column_name: string
      priority: string | null
      deadline: string | null
      blocked: number
      updated_at: string
    }>
    return rows.map((row) => ({
      id: row.id,
      title: row.title,
      column: row.column_name,
      priority: priorityRank(row.priority),
      deadline: parseDeadline(row.deadline),
      blocked: row.blocked !== 0,
      updatedAt: row.updated_at
    }))
  }

  private recentNotes(db: Db): WidgetOutcome {
    const notes = (
      db
        .prepare(
          `SELECT id, title, kind, updated_at FROM notes WHERE deleted_at IS NULL
            ORDER BY updated_at DESC, id LIMIT ?`
        )
        .all(WIDGET_LIMIT) as Array<{ id: string; title: string; kind: string; updated_at: string }>
    ).map<OverviewNoteCard>((row) => ({
      id: row.id,
      title: row.title,
      kind: row.kind as OverviewNoteCard['kind'],
      updatedAt: row.updated_at
    }))
    return { state: okState('recent_notes'), notes }
  }

  private literature(db: Db): WidgetOutcome {
    const projectId = this.header.header.id
    if (this.papers !== undefined) {
      try {
        return { state: okState('literature'), literature: this.papers.listProjectPapers(projectId, WIDGET_LIMIT) }
      } catch (error) {
        throw new Error(`literature port: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
    return { state: okState('literature'), literature: this.literatureFallback(db) }
  }

  private literatureFallback(db: Db): OverviewPaperCard[] {
    const links = db
      .prepare(`SELECT paper_id FROM project_papers ORDER BY linked_at DESC, paper_id LIMIT ?`)
      .all(WIDGET_LIMIT) as Array<{ paper_id: string }>
    return links.map((link) => {
      const row = this.workspace.global.db
        .prepare(`SELECT metadata, file_path, file_state FROM papers WHERE id = ?`)
        .get(link.paper_id) as
        | { metadata: string; file_path: string | null; file_state: string }
        | undefined
      if (row === undefined) {
        return { id: link.paper_id, title: link.paper_id.slice(0, 8), authors: '', year: 0, missing: true }
      }
      const metadata = parsePaperMetadata(row.metadata)
      let missing = row.file_state !== 'ok' || row.file_path === null
      if (!missing && row.file_path !== null) {
        try {
          this.workspace.paths.abs(row.file_path)
        } catch {
          missing = true
        }
      }
      return {
        id: link.paper_id,
        title: metadata.title,
        authors: metadata.authors,
        year: metadata.year,
        missing
      }
    })
  }

  private weeklyProgress(db: Db): WidgetOutcome {
    const { start, end } = weekWindow(this.clock.now())
    const firstDay = isoDay(start)
    const lastDay = isoDay(new Date(end.getTime() - 24 * 60 * 60 * 1000))
    const report = db
      .prepare(
        `SELECT id, content_md FROM weekly_reports
          WHERE status = 'confirmed' AND deleted_at IS NULL
            AND period_start <= ? AND period_end >= ?
          ORDER BY period_start DESC, updated_at DESC, id DESC LIMIT 1`
      )
      .get(lastDay, firstDay) as { id: string; content_md: string } | undefined
    if (report !== undefined) {
      return {
        state: okState('weekly_progress'),
        weekly: {
          reportId: report.id,
          excerpt: truncateRunes(report.content_md, REPORT_EXCERPT_RUNES),
          confirmed: true,
          activityCounts: {}
        }
      }
    }
    const counts: Record<string, number> = {}
    const rows = db
      .prepare(
        `SELECT entity_type, count(*) AS n FROM activities
          WHERE created_at >= ? AND created_at < ?
          GROUP BY entity_type ORDER BY count(*) DESC, entity_type LIMIT 6`
      )
      .all(formatTimestamp(start), formatTimestamp(end)) as Array<{ entity_type: string; n: number }>
    for (const row of rows) counts[row.entity_type] = Number(row.n)
    return {
      state: pendingState('weekly_progress'),
      weekly: { reportId: null, excerpt: '', confirmed: false, activityCounts: counts }
    }
  }
}

const PRIORITY_RANK_SQL = `CASE lower(COALESCE(priority, ''))
  WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END`

function priorityRank(raw: string | null): number {
  switch ((raw ?? '').trim().toLowerCase()) {
    case 'high':
      return 3
    case 'medium':
      return 2
    case 'low':
      return 1
    default:
      return 0
  }
}

/** Accepts the canonical timestamp and a bare natural day. */
function parseDeadline(raw: string | null): string | null {
  const value = (raw ?? '').trim()
  if (value === '') return null
  try {
    parseTimestamp(value)
    return value
  } catch {
    return /^\d{4}-\d{2}-\d{2}$/.test(value) ? value : null
  }
}

/* --------------------------------------------------------------- shaping */

function okState(kind: OverviewWidgetKind): OverviewWidgetState {
  return { kind, available: true, degraded: false, reason: null, pendingConfirmation: false }
}

function pendingState(kind: OverviewWidgetKind): OverviewWidgetState {
  return { kind, available: true, degraded: false, reason: 'pending_confirmation', pendingConfirmation: true }
}

function classify(error: unknown): OverviewWidgetReason {
  if (error instanceof Error && /budget exceeded/.test(error.message)) return 'timeout'
  if (error instanceof Error && error.name === 'AbortError') return 'canceled'
  return 'query_error'
}

/** A failed widget keeps the previous payload (or the zero value) and says why. */
function degrade(
  kind: OverviewWidgetKind,
  previous: OverviewSnapshot | null,
  reason: OverviewWidgetReason
): WidgetOutcome {
  const outcome = payloadOf(previous, kind)
  const state: OverviewWidgetState = {
    kind,
    available: true,
    degraded: true,
    reason,
    pendingConfirmation: kind === 'my_tasks' || kind === 'blocked_tasks'
  }
  const prior = previous?.widgets.find((widget) => widget.kind === kind)
  if (prior !== undefined) {
    state.available = prior.available
    state.pendingConfirmation = prior.pendingConfirmation
  }
  outcome.state = state
  return outcome
}

function payloadOf(previous: OverviewSnapshot | null, kind: OverviewWidgetKind): WidgetOutcome {
  const outcome: WidgetOutcome = {
    state: { kind, available: false, degraded: true, reason: null, pendingConfirmation: false }
  }
  if (previous === null) return outcome
  switch (kind) {
    case 'current_focus':
      outcome.focus = { ...previous.currentFocus, topIdeas: [...previous.currentFocus.topIdeas] }
      break
    case 'active_ideas':
      outcome.ideas = [...previous.activeIdeas]
      break
    case 'recent_experiments':
      outcome.experiments = [...previous.recentExperiments]
      break
    case 'my_tasks':
      outcome.myTasks = { ...previous.myTasks, items: [...previous.myTasks.items] }
      break
    case 'recent_notes':
      outcome.notes = [...previous.recentNotes]
      break
    case 'literature':
      outcome.literature = [...previous.literature]
      break
    case 'blocked_tasks':
      outcome.blocked = { items: [...previous.blockedTasks.items] }
      break
    case 'weekly_progress':
      outcome.weekly = { ...previous.weeklyProgress, activityCounts: { ...previous.weeklyProgress.activityCounts } }
      break
  }
  return outcome
}

function applyWidget(snapshot: OverviewSnapshot, kind: OverviewWidgetKind, outcome: WidgetOutcome): void {
  switch (kind) {
    case 'current_focus':
      if (outcome.focus !== undefined) snapshot.currentFocus = outcome.focus
      break
    case 'active_ideas':
      snapshot.activeIdeas = outcome.ideas ?? []
      break
    case 'recent_experiments':
      snapshot.recentExperiments = outcome.experiments ?? []
      break
    case 'my_tasks':
      if (outcome.myTasks !== undefined) snapshot.myTasks = outcome.myTasks
      break
    case 'recent_notes':
      snapshot.recentNotes = outcome.notes ?? []
      break
    case 'literature':
      snapshot.literature = outcome.literature ?? []
      break
    case 'blocked_tasks':
      if (outcome.blocked !== undefined) snapshot.blockedTasks = outcome.blocked
      break
    case 'weekly_progress':
      if (outcome.weekly !== undefined) snapshot.weeklyProgress = outcome.weekly
      break
  }
}

function assemble(
  header: HeaderRow,
  outcomes: Partial<Record<OverviewWidgetKind, WidgetOutcome>>,
  generatedAt: string
): OverviewSnapshot {
  const snapshot: OverviewSnapshot = {
    project: header.header,
    writable: header.writable,
    currentFocus: { goalsExcerpt: '', focusMd: '', focusEditable: header.writable, topIdeas: [] },
    activeIdeas: [],
    recentExperiments: [],
    myTasks: { weekPlanned: false, items: [] },
    recentNotes: [],
    literature: [],
    blockedTasks: { items: [] },
    weeklyProgress: { reportId: null, excerpt: '', confirmed: false, activityCounts: {} },
    widgets: OVERVIEW_WIDGET_ORDER.map(okState),
    generatedAt
  }
  for (const kind of OVERVIEW_WIDGET_ORDER) {
    const outcome = outcomes[kind]
    if (outcome === undefined) {
      snapshot.widgets[indexOf(kind)] = {
        kind,
        available: false,
        degraded: true,
        reason: 'query_error',
        pendingConfirmation: false
      }
      continue
    }
    snapshot.widgets[indexOf(kind)] = outcome.state
    applyWidget(snapshot, kind, outcome)
  }
  return snapshot
}

function indexOf(kind: OverviewWidgetKind): number {
  return OVERVIEW_WIDGET_ORDER.indexOf(kind)
}

function cloneSnapshot(snapshot: OverviewSnapshot): OverviewSnapshot {
  return {
    ...snapshot,
    project: { ...snapshot.project },
    currentFocus: { ...snapshot.currentFocus, topIdeas: [...snapshot.currentFocus.topIdeas] },
    activeIdeas: [...snapshot.activeIdeas],
    recentExperiments: [...snapshot.recentExperiments],
    myTasks: { ...snapshot.myTasks, items: [...snapshot.myTasks.items] },
    recentNotes: [...snapshot.recentNotes],
    literature: [...snapshot.literature],
    blockedTasks: { items: [...snapshot.blockedTasks.items] },
    weeklyProgress: {
      ...snapshot.weeklyProgress,
      activityCounts: { ...snapshot.weeklyProgress.activityCounts }
    },
    widgets: snapshot.widgets.map((widget) => ({ ...widget }))
  }
}

function truncateRunes(value: string, max: number): string {
  if (runeCount(value) <= max) return value
  const characters = [...value].slice(0, Math.max(1, max - 1))
  return `${characters.join('').replace(/[ \t\n]+$/, '')}…`
}

function isoDay(date: Date): string {
  return formatTimestamp(date).slice(0, 10)
}

/** Half-open [Monday 00:00, next Monday 00:00) in the clock's local zone. */
function weekWindow(now: Date): { start: Date; end: Date } {
  const local = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const offset = (local.getDay() + 6) % 7
  const start = new Date(local.getFullYear(), local.getMonth(), local.getDate() - offset)
  return { start, end: new Date(start.getFullYear(), start.getMonth(), start.getDate() + 7) }
}

function archivedError(projectId: string) {
  return appError(AppErrorCode.ArchivedReadOnly, '项目已归档，只读', { details: { projectId } })
}