import { existsSync, rmSync } from 'node:fs'
import * as path from 'node:path'

import type {
  ActivityActor,
  ActivityFilter,
  ActivityPage,
  ActivityRoute,
  ActivityRouteKind,
  DeleteProjectPlan,
  DeleteProjectResult,
  Idea,
  Project,
  ProjectActivityItem,
  ProjectPatch,
  ProjectResources,
  ProjectSession,
  ProjectStats,
  ResourceNoteRef,
  ResourcePaperRef,
  ResourceTaskRef,
  Task
} from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp, parseTimestamp, type Clock, SystemClock } from '../../clock'
import { appError, isAppError } from '../../errors'
import { newId } from '../../ids'
import { SettingsStore } from '../../settings'
import type { Db } from '../../db/open'
import type { ProjectHandle } from '../../workspace/handle'
import type { WorkspaceManager } from '../../workspace'
import {
  assertProjectActor,
  eventMissing,
  featureUnavailable,
  invalidField,
  projectArchived,
  projectConflict,
  projectNotFound
} from './errors'
import { executePermanentDelete, prepareDeletePlan } from './delete'
import { directoryBytes } from './util'
import type {
  ActivityEmitter,
  BusyProbe,
  ConfirmationPort,
  IdeaCreatorPort,
  NoteCreatorPort,
  TaskCompleterPort,
  TaskCreatorPort
} from './ports'

/** Limits. Rune counts of the stored text. */
export const PROJECT_LIMITS = {
  name: 200,
  summary: 5000,
  goals: 100_000,
  focus: 20_000
} as const

/** Browse state: the last opened project (navigation, never an Activity). */
const LAST_PROJECT_KEY = 'app.last_project_id'

const ACTIVITY_DEFAULT_LIMIT = 50
const ACTIVITY_MAX_LIMIT = 200
const ACTIVITY_DEFAULT_DAYS = 30
const RESOURCE_LIMIT = 20

export interface ProjectServiceOptions {
  workspace: WorkspaceManager
  clock?: Clock
  /** Foundation EventBus. Without it no Activity row is written. */
  events?: ActivityEmitter
  /** Foundation confirmation tokens for the permanent-delete step. */
  confirmations?: ConfirmationPort
  notes?: NoteCreatorPort
  tasks?: TaskCompleterPort & TaskCreatorPort
  ideas?: IdeaCreatorPort
  /** Export-job registry probe used by the delete plan. */
  busy?: BusyProbe
  /** Called after a committed project change; caches drop their snapshot. */
  onChanged?: (projectId: string) => void
}

export interface CreateProjectInput {
  name: string
  summary?: string
  goals?: string
}

export interface UpdateProjectInput {
  id: string
  patch: ProjectPatch
  /** Optimistic token from the last read row. */
  expectedUpdatedAt?: string
  actor?: ActivityActor
}

export interface ListProjectsQuery {
  includeArchived?: boolean
  query?: string
  limit?: number
  offset?: number
}

interface ProjectRow {
  id: string
  name: string
  summary: string
  goals: string
  focus_md: string
  status: string
  archived_at: string | null
  created_at: string
  updated_at: string
}

/** Counts every rune (not UTF-16 unit) the way Go's utf8.RuneCountInString does. */
export function runeCount(value: string): number {
  let count = 0
  for (const _ of value) count += 1
  return count
}

function validateText(field: string, value: string, max: number): void {
  const count = runeCount(value)
  if (count > max) throw invalidField(field, `must be at most ${max} runes, got ${count}`)
}

/** Trims and validates a project name: 1..200 runes, no control characters. */
export function normalizeProjectName(raw: string): string {
  const name = raw.trim()
  if (name === '') throw invalidField('name', 'must not be empty')
  const count = runeCount(name)
  if (count > PROJECT_LIMITS.name) {
    throw invalidField('name', `must be at most ${PROJECT_LIMITS.name} runes, got ${count}`)
  }
  for (const character of name) {
    if (character.codePointAt(0)! < 0x20) {
      throw invalidField('name', `must not contain control characters (found ${JSON.stringify(character)})`)
    }
  }
  return name
}

function rowToProject(row: ProjectRow): Project {
  return {
    id: row.id,
    name: row.name,
    summary: row.summary,
    goals: row.goals,
    focusMd: row.focus_md,
    status: row.status === 'archived' ? 'archived' : 'active',
    archivedAt: row.archived_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

/** Reads PRAGMA table_info once per call; the table name is never user input. */
export function tableColumns(db: Db, table: string): Set<string> {
  const rows = db.prepare(`PRAGMA table_info(${table})`).all() as Array<{ name: string }>
  return new Set(rows.map((row) => row.name))
}

/** SQL fragment excluding done/archived tasks when this schema has the columns. */
export function taskTerminalScope(db: Db): string {
  const columns = tableColumns(db, 'tasks')
  let scope = ''
  if (columns.has('completed_at')) scope += ' AND completed_at IS NULL'
  if (columns.has('archived_at')) scope += ' AND archived_at IS NULL'
  return scope
}

function countRows(db: Db, sql: string, ...args: unknown[]): number {
  const row = db.prepare(sql).get(...args) as { n: number } | undefined
  return row === undefined ? 0 : Number(row.n)
}

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

const ROUTE_KIND: Readonly<Record<string, ActivityRouteKind>> = {
  note: 'note',
  idea: 'idea',
  experiment: 'experiment',
  run: 'run',
  task: 'task',
  paper: 'paper',
  weekly_report: 'weekly_report'
}

const ENTITY_ROUTE_KIND: Readonly<Record<string, string>> = {
  note: 'note',
  idea: 'idea',
  experiment: 'experiment',
  run: 'run',
  task: 'task',
  paper: 'paper',
  weekly_report: 'weekly_report'
}

function escapeLike(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('%', '\\%').replaceAll('_', '\\_')
}

interface ActivityCursor {
  offset: number
  lastId: string
}

function encodeCursor(cursor: ActivityCursor): string {
  return Buffer.from(JSON.stringify({ o: cursor.offset, l: cursor.lastId }), 'utf8').toString('base64url')
}

function decodeCursor(raw: string | undefined): ActivityCursor | null {
  if (raw === undefined || raw === '') return null
  try {
    const parsed = JSON.parse(Buffer.from(raw, 'base64url').toString('utf8')) as { o?: unknown; l?: unknown }
    if (typeof parsed.o !== 'number' || typeof parsed.l !== 'string' || parsed.o < 0) return null
    return { offset: parsed.o, lastId: parsed.l }
  } catch {
    return null
  }
}

/**
 * The project service: registration CRUD over
 * global.db.projects, the §5.4 creation pipeline, the single in-process
 * session, the archive read-only gate, statistics, resources and the activity
 * timeline.
 *
 * Cross-database invariant: global.db holds the registration row while
 * project.db holds the Activity event. Writes follow "directory/database →
 * registration → event"; an event failure keeps the business change and
 * surfaces `EVENT_MISSING` instead of rolling back.
 */
export class ProjectService {
  readonly workspace: WorkspaceManager
  readonly clock: Clock
  readonly events: ActivityEmitter | undefined
  readonly confirmations: ConfirmationPort | undefined
  readonly notes: NoteCreatorPort | undefined
  readonly tasks: (TaskCompleterPort & TaskCreatorPort) | undefined
  readonly ideas: IdeaCreatorPort | undefined
  private readonly busy: BusyProbe | undefined
  private readonly onChanged: ((projectId: string) => void) | undefined
  private session: ProjectSession | null = null

  constructor(options: ProjectServiceOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? options.workspace.clock ?? new SystemClock()
    this.events = options.events
    this.confirmations = options.confirmations
    this.notes = options.notes
    this.tasks = options.tasks
    this.ideas = options.ideas
    this.busy = options.busy
    this.onChanged = options.onChanged
  }

  /* -------------------------------------------------------------- reads */

  get(id: string): Project {
    const row = this.workspace.global.db
      .prepare(
        `SELECT id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at
           FROM projects WHERE id = ?`
      )
      .get(id) as ProjectRow | undefined
    if (row === undefined) throw projectNotFound(id)
    return rowToProject(row)
  }

  /**
   * Active projects by default; archived projects follow when requested. Each
   * group is created_at DESC with the id as tie-break. Query is a
   * case-insensitive substring of name or summary (sorted in memory).
   */
  list(query: ListProjectsQuery = {}): Project[] {
    const rows = this.workspace.global.db
      .prepare(
        `SELECT id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at
           FROM projects`
      )
      .all() as ProjectRow[]
    const needle = (query.query ?? '').trim().toLowerCase()
    const projects = rows
      .map(rowToProject)
      .filter((project) => query.includeArchived === true || project.status !== 'archived')
      .filter(
        (project) =>
          needle === '' ||
          project.name.toLowerCase().includes(needle) ||
          project.summary.toLowerCase().includes(needle)
      )
      .sort((left, right) => {
        const leftArchived = left.status === 'archived'
        const rightArchived = right.status === 'archived'
        if (leftArchived !== rightArchived) return leftArchived ? 1 : -1
        if (left.createdAt !== right.createdAt) return left.createdAt > right.createdAt ? -1 : 1
        return left.id > right.id ? -1 : left.id < right.id ? 1 : 0
      })
    if (query.offset !== undefined && query.limit !== undefined) {
      return projects.slice(query.offset, query.offset + query.limit)
    }
    if (query.offset !== undefined) return projects.slice(query.offset)
    if (query.limit !== undefined) return projects.slice(0, query.limit)
    return projects
  }

  stats(id: string): ProjectStats {
    this.get(id)
    const db = this.projectDb(id)
    const scope = taskTerminalScope(db)
    const last = db.prepare(`SELECT max(created_at) AS last FROM activities`).get() as { last: string | null }
    return {
      notes: countRows(db, `SELECT count(*) AS n FROM notes WHERE deleted_at IS NULL`),
      ideasActive: countRows(
        db,
        `SELECT count(*) AS n FROM ideas WHERE status IN ('Researching','Validating') AND deleted_at IS NULL`
      ),
      experiments: countRows(db, `SELECT count(*) AS n FROM experiments WHERE deleted_at IS NULL`),
      papers: countRows(db, `SELECT count(*) AS n FROM project_papers`),
      tasksOpen: countRows(db, `SELECT count(*) AS n FROM tasks WHERE deleted_at IS NULL${scope}`),
      tasksBlocked: countRows(
        db,
        `SELECT count(*) AS n FROM tasks WHERE blocked = 1 AND deleted_at IS NULL${scope}`
      ),
      trashed: countRows(db, `SELECT count(*) AS n FROM v_trash`),
      dirBytes: directoryBytes(this.projectDir(id)),
      lastActivityAt: last?.last ?? null
    }
  }

  /* ------------------------------------------------------------- writes */

  /**
   * §5.4 creation pipeline: directory + project.db → global registration row →
   * entity.created event. A registration failure rolls the directory and the
   * row back; an event failure keeps the project and reports the gap.
   */
  create(input: CreateProjectInput, actor: ActivityActor = 'user'): Project {
    const author = assertProjectActor(actor)
    const name = normalizeProjectName(input.name)
    const summary = input.summary ?? ''
    const goals = input.goals ?? ''
    validateText('summary', summary, PROJECT_LIMITS.summary)
    validateText('goals', goals, PROJECT_LIMITS.goals)

    const id = newId(this.clock)
    try {
      this.workspace.createProjectData(id)
    } catch (error) {
      this.rollbackCreate(id)
      throw error
    }

    const now = formatTimestamp(this.clock.now())
    try {
      this.workspace.global.inTx(() => {
        this.workspace.global.db
          .prepare(
            `INSERT INTO projects(id, name, summary, goals, focus_md, status, archived_at, created_at, updated_at)
             VALUES (?, ?, ?, ?, '', 'active', NULL, ?, ?)`
          )
          .run(id, name, summary, goals, now, now)
      })
    } catch (error) {
      this.rollbackCreate(id)
      throw error
    }

    let created: Project
    try {
      created = this.get(id)
    } catch (error) {
      this.rollbackCreate(id)
      throw error
    }

    let eventError: unknown
    try {
      this.emit(id, {
        type: 'entity.created',
        entityType: 'project',
        entityId: id,
        summary: `创建项目「${name}」`,
        actor: author
      })
    } catch (error) {
      eventError = error
    }
    this.changed(id)
    if (eventError !== undefined) throw eventMissing(id, eventError)
    return created
  }

  /**
   * §5.3 update: only fields whose value actually changes are written and
   * reported. A stale `expectedUpdatedAt` returns CONFLICT with the current
   * row; a no-op returns the unchanged project without an event.
   */
  update(input: UpdateProjectInput): Project {
    const current = this.assertWritable(input.id)
    const actor = assertProjectActor(input.actor ?? 'user')
    if (input.expectedUpdatedAt !== undefined && input.expectedUpdatedAt !== current.updatedAt) {
      throw projectConflict(current)
    }

    const patch = input.patch
    const changes: Record<string, { from: string; to: string }> = {}
    const assignments: Array<[string, string]> = []

    if (patch.name !== undefined) {
      const name = normalizeProjectName(patch.name)
      if (name !== current.name) {
        changes.name = { from: current.name, to: name }
        assignments.push(['name', name])
      }
    }
    if (patch.summary !== undefined) {
      validateText('summary', patch.summary, PROJECT_LIMITS.summary)
      if (patch.summary !== current.summary) {
        changes.summary = { from: current.summary, to: patch.summary }
        assignments.push(['summary', patch.summary])
      }
    }
    if (patch.goals !== undefined) {
      validateText('goals', patch.goals, PROJECT_LIMITS.goals)
      if (patch.goals !== current.goals) {
        changes.goals = { from: current.goals, to: patch.goals }
        assignments.push(['goals', patch.goals])
      }
    }
    if (patch.focusMd !== undefined) {
      validateText('focus_md', patch.focusMd, PROJECT_LIMITS.focus)
      if (patch.focusMd !== current.focusMd) {
        changes.focus_md = { from: current.focusMd, to: patch.focusMd }
        assignments.push(['focus_md', patch.focusMd])
      }
    }
    if (assignments.length === 0) return current

    const now = formatTimestamp(this.clock.now())
    const setSql = assignments.map(([column]) => `${column} = ?`).join(', ')
    const params = [...assignments.map(([, value]) => value), now, input.id, current.updatedAt]
    let updated = 1
    this.workspace.global.inTx(() => {
      const result = this.workspace.global.db
        .prepare(
          `UPDATE projects SET ${setSql}, updated_at = ? WHERE id = ? AND updated_at = ?`
        )
        .run(...params)
      updated = result.changes
    })
    if (updated === 0) {
      // Another writer committed between the read and this update.
      throw projectConflict(this.get(input.id))
    }

    const next = this.get(input.id)
    const oldObject: Record<string, unknown> = {}
    const newObject: Record<string, unknown> = {}
    for (const [field, change] of Object.entries(changes)) {
      oldObject[field] = change.from
      newObject[field] = change.to
    }
    let eventError: unknown
    try {
      this.emit(input.id, {
        type: 'entity.updated',
        entityType: 'project',
        entityId: input.id,
        summary: `编辑项目「${next.name}」`,
        actor,
        old: oldObject,
        new: newObject
      })
    } catch (error) {
      eventError = error
    }
    this.changed(input.id)
    if (eventError !== undefined) throw eventMissing(input.id, eventError)
    return next
  }

  archive(id: string, actor: ActivityActor = 'user'): Project {
    return this.setStatus(id, 'archived', actor)
  }

  unarchive(id: string, actor: ActivityActor = 'user'): Project {
    return this.setStatus(id, 'active', actor)
  }

  private setStatus(id: string, target: 'active' | 'archived', actor: ActivityActor): Project {
    const author = assertProjectActor(actor)
    const current = this.get(id)
    if (current.status === target) {
      this.refreshSession(id)
      return current
    }
    const now = formatTimestamp(this.clock.now())
    const archivedAt = target === 'archived' ? now : null
    this.workspace.global.inTx(() => {
      this.workspace.global.db
        .prepare(`UPDATE projects SET status = ?, archived_at = ?, updated_at = ? WHERE id = ?`)
        .run(target, archivedAt, now, id)
    })

    const next = this.get(id)
    let eventError: unknown
    try {
      this.emit(id, {
        type: 'status.changed',
        entityType: 'project',
        entityId: id,
        summary: target === 'archived' ? `归档项目「${next.name}」` : `取消归档项目「${next.name}」`,
        actor: author,
        old: { status: current.status },
        new: { status: target }
      })
    } catch (error) {
      eventError = error
    }
    this.refreshSession(id)
    this.changed(id)
    if (eventError !== undefined) throw eventMissing(id, eventError)
    return next
  }

  /** §5.8 read-only guard: only the archived status blocks a write. */
  assertWritable(id: string): Project {
    const project = this.get(id)
    if (project.status === 'archived') throw projectArchived(id)
    return project
  }

  /* ------------------------------------------------------------ session */

  /** The single active session; opening is navigation and writes no Activity. */
  open(id: string): ProjectSession {
    const project = this.get(id)
    if (this.session !== null && this.session.project.id !== id) this.close()
    const session: ProjectSession = {
      project,
      readOnly: project.status === 'archived',
      openedAt: formatTimestamp(this.clock.now())
    }
    this.session = session
    try {
      new SettingsStore(this.workspace.global.db, 'workspace', this.clock).set(LAST_PROJECT_KEY, id)
    } catch (error) {
      this.session = null
      throw isAppError(error)
        ? error
        : appError(AppErrorCode.Internal, `project: persist ${LAST_PROJECT_KEY}`, { cause: error })
    }
    return session
  }

  current(): ProjectSession | null {
    return this.session === null ? null : { ...this.session, project: { ...this.session.project } }
  }

  close(id?: string): void {
    if (this.session === null) return
    if (id !== undefined && this.session.project.id !== id) return
    this.workspace.closeProject(this.session.project.id)
    this.session = null
  }

  private refreshSession(id: string): void {
    if (this.session === null || this.session.project.id !== id) return
    try {
      const project = this.get(id)
      this.session = { ...this.session, project, readOnly: project.status === 'archived' }
    } catch {
      // A vanished row is the caller's problem; keep the session untouched.
    }
  }

  /* ----------------------------------------------------------- activity */

  activity(projectId: string, filter: ActivityFilter = {}): ActivityPage {
    const project = this.get(projectId)
    const db = this.projectDb(projectId)
    const limit = Math.max(1, Math.min(filter.limit ?? ACTIVITY_DEFAULT_LIMIT, ACTIVITY_MAX_LIMIT))

    const to = filter.to ?? formatTimestamp(this.clock.now())
    const from =
      filter.from ??
      formatTimestamp(new Date(Date.parse(to) - ACTIVITY_DEFAULT_DAYS * 24 * 60 * 60 * 1000))

    const where: string[] = ['created_at >= ?', 'created_at <= ?']
    const params: unknown[] = [from, to]
    if (filter.entityTypes !== undefined && filter.entityTypes.length > 0) {
      where.push(`entity_type IN (${filter.entityTypes.map(() => '?').join(',')})`)
      params.push(...filter.entityTypes)
    }
    if (filter.eventTypes !== undefined && filter.eventTypes.length > 0) {
      where.push(`event_type IN (${filter.eventTypes.map(() => '?').join(',')})`)
      params.push(...filter.eventTypes)
    }
    if (filter.actors !== undefined && filter.actors.length > 0) {
      where.push(`actor IN (${filter.actors.map(() => '?').join(',')})`)
      params.push(...filter.actors)
    }

    let offset = 0
    const cursor = decodeCursor(filter.cursor)
    if (cursor !== null && cursor.offset > 0) {
      const probe = db
        .prepare(
          `SELECT id FROM activities WHERE ${where.join(' AND ')} ORDER BY created_at DESC, id DESC LIMIT 1 OFFSET ?`
        )
        .get(...params, cursor.offset - 1) as { id: string } | undefined
      if (probe !== undefined && probe.id === cursor.lastId) offset = cursor.offset
    }

    const rows = db
      .prepare(
        `SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
           FROM activities WHERE ${where.join(' AND ')}
          ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
      )
      .all(...params, limit + 1, offset) as Array<{
      id: string
      event_type: string
      entity_type: string
      entity_id: string
      payload: string
      actor: string
      created_at: string
    }>

    const hasMore = rows.length > limit
    const window = hasMore ? rows.slice(0, limit) : rows

    const trashed = new Set<string>()
    const trashRows = db.prepare(`SELECT entity_type, id FROM v_trash`).all() as Array<{
      entity_type: string
      id: string
    }>
    for (const row of trashRows) trashed.add(`${row.entity_type}\u0000${row.id}`)

    const archived = project.status === 'archived'
    const items: ProjectActivityItem[] = window.map((row) => {
      const payload = JSON.parse(row.payload) as { summary?: unknown }
      const summary = typeof payload.summary === 'string' ? payload.summary : ''
      const gone = archived || trashed.has(`${row.entity_type}\u0000${row.entity_id}`)
      return {
        id: row.id,
        eventType: row.event_type,
        entityType: row.entity_type as ProjectActivityItem['entityType'],
        entityId: row.entity_id,
        summary,
        actor: row.actor as ActivityActor,
        createdAt: row.created_at,
        entityGone: gone,
        route: activityRoute(projectId, row.entity_type, row.entity_id, gone)
      }
    })

    return {
      items,
      nextCursor:
        hasMore && items.length > 0
          ? encodeCursor({ offset: offset + limit, lastId: items[items.length - 1]!.id })
          : null
    }
  }

  /* ---------------------------------------------------------- resources */

  resources(projectId: string): ProjectResources {
    this.get(projectId)
    const db = this.projectDb(projectId)
    const scope = taskTerminalScope(db)

    const links = db
      .prepare(`SELECT paper_id FROM project_papers ORDER BY linked_at DESC, paper_id LIMIT ?`)
      .all(RESOURCE_LIMIT) as Array<{ paper_id: string }>
    const papers: ResourcePaperRef[] = links.map((link) => this.paperRef(link.paper_id))

    const noteCounts: Record<string, number> = {}
    for (const row of db
      .prepare(`SELECT kind, count(*) AS n FROM notes WHERE deleted_at IS NULL GROUP BY kind`)
      .all() as Array<{ kind: string; n: number }>) {
      noteCounts[row.kind] = Number(row.n)
    }
    const notes = (
      db
        .prepare(
          `SELECT id, title, kind, updated_at FROM notes WHERE deleted_at IS NULL
            ORDER BY updated_at DESC, id LIMIT ?`
        )
        .all(RESOURCE_LIMIT) as Array<{ id: string; title: string; kind: string; updated_at: string }>
    ).map<ResourceNoteRef>((row) => ({
      id: row.id,
      title: row.title,
      kind: row.kind as ResourceNoteRef['kind'],
      updatedAt: row.updated_at
    }))

    const experiments = (
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
        .all(RESOURCE_LIMIT) as Array<{
        id: string
        title: string
        updated_at: string
        latest: string | null
        run_count: number
      }>
    ).map((row) => ({
      id: row.id,
      title: row.title,
      lastRunStatus: (row.latest ?? '—') as ProjectResources['experiments'][number]['lastRunStatus'],
      runCount: Number(row.run_count),
      updatedAt: row.updated_at
    }))

    const taskBuckets: Record<string, number> = {}
    for (const row of db
      .prepare(
        `SELECT "column" AS column_name, count(*) AS n FROM tasks
          WHERE deleted_at IS NULL${scope} GROUP BY "column" ORDER BY "column"`
      )
      .all() as Array<{ column_name: string; n: number }>) {
      taskBuckets[row.column_name] = Number(row.n)
    }
    const tasks = (
      db
        .prepare(
          `SELECT id, title, "column" AS column_name, priority, deadline, blocked, updated_at
             FROM tasks WHERE deleted_at IS NULL${scope}
            ORDER BY (deadline IS NULL OR deadline = ''), deadline, "column", updated_at DESC, id
            LIMIT ?`
        )
        .all(RESOURCE_LIMIT) as Array<{
        id: string
        title: string
        column_name: string
        priority: string | null
        deadline: string | null
        blocked: number
        updated_at: string
      }>
    ).map<ResourceTaskRef>((row) => ({
      id: row.id,
      title: row.title,
      column: row.column_name,
      priority: priorityRank(row.priority),
      deadline: parseDeadline(row.deadline),
      blocked: row.blocked !== 0,
      updatedAt: row.updated_at
    }))

    return { papers, noteCounts, notes, experiments, taskBuckets, tasks }
  }

  private paperRef(paperId: string): ResourcePaperRef {
    const row = this.workspace.global.db
      .prepare(`SELECT metadata, file_path, file_state FROM papers WHERE id = ?`)
      .get(paperId) as { metadata: string; file_path: string | null; file_state: string } | undefined
    if (row === undefined) {
      return { id: paperId, title: paperId.slice(0, 8), authors: '', year: 0, missing: true }
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
    return { id: paperId, title: metadata.title, authors: metadata.authors, year: metadata.year, missing }
  }

  /* ------------------------------------------------------- quick actions */

  quickAddNote(projectId: string, input: { title?: string; bodyMd?: string }): { noteId: string } {
    this.assertWritable(projectId)
    if (this.notes === undefined) throw featureUnavailable('notes')
    const title = (input.title ?? '').trim() || `Quick note ${formatTimestamp(this.clock.now()).slice(0, 16).replace('T', ' ')}`
    const created = this.notes.createScratchpad({
      projectId,
      title,
      bodyMd: input.bodyMd ?? '',
      actor: 'user'
    })
    this.changed(projectId)
    return { noteId: created.id }
  }

  completeTask(projectId: string, taskId: string): void {
    this.assertWritable(projectId)
    if (this.tasks === undefined) throw featureUnavailable('kanban')
    this.tasks.completeTask({ projectId, taskId, actor: 'user' })
    this.changed(projectId)
  }

  quickAddTask(projectId: string, input: { title: string; column?: string; weekPlan?: string }): Task {
    this.assertWritable(projectId)
    if (this.tasks === undefined) throw featureUnavailable('kanban')
    const created = this.tasks.createTask({
      projectId,
      title: input.title,
      ...(input.column === undefined ? {} : { column: input.column }),
      ...(input.weekPlan === undefined ? {} : { weekPlan: input.weekPlan })
    })
    this.changed(projectId)
    return created
  }

  quickAddIdea(projectId: string, title: string): Idea {
    this.assertWritable(projectId)
    if (this.ideas === undefined) throw featureUnavailable('ideas')
    const created = this.ideas.createIdea({ projectId, title })
    this.changed(projectId)
    return created
  }

  /* ------------------------------------------------------------ helpers */

  /** Opens (and caches) the project database through the workspace. */
  projectDb(projectId: string): Db {
    return this.workspace.project(projectId).db
  }

  projectHandle(projectId: string): ProjectHandle {
    return this.workspace.project(projectId)
  }

  projectDir(projectId: string): string {
    return this.workspace.paths.abs(this.workspace.paths.projectDir(projectId))
  }

  /** Emits one Activity row inside the project database's own transaction. */
  emit(projectId: string, event: Parameters<ActivityEmitter['emit']>[1]): void {
    if (this.events === undefined) return
    const handle = this.workspace.project(projectId)
    handle.inTx(() => {
      this.events!.emit(handle.db, event)
    })
  }

  private changed(projectId: string): void {
    this.onChanged?.(projectId)
  }

  /**
   * Undoes a partially created project. Nothing else can observe it yet, so
   * deleting the registration row and the directory is safe (§5.4). A broken
   * cleanup names the orphan for startup reconcile.
   */
  private rollbackCreate(id: string): void {
    const cleanup: string[] = []
    try {
      this.workspace.closeProject(id)
    } catch (error) {
      cleanup.push(`close handle: ${messageOf(error)}`)
    }
    try {
      this.workspace.global.inTx(() => {
        this.workspace.global.db.prepare(`DELETE FROM projects WHERE id = ?`).run(id)
      })
    } catch (error) {
      cleanup.push(`remove registration: ${messageOf(error)}`)
    }
    try {
      const directory = this.projectDir(id)
      if (existsSync(directory)) rmSync(directory, { recursive: true, force: true })
    } catch (error) {
      cleanup.push(`remove orphan directory: ${messageOf(error)}`)
    }
    if (cleanup.length > 0) {
      throw appError(
        AppErrorCode.Internal,
        `project ${id} creation failed and the rollback is incomplete: ${cleanup.join('; ')}`,
        { details: { projectId: id, incompleteRollback: cleanup } }
      )
    }
  }

  /* ------------------------------------------------------ delete support */

  prepareDelete(projectId: string, options: { issueToken: boolean }): DeleteProjectPlan {
    return prepareDeletePlan(this, projectId, options)
  }

  permanentDelete(request: {
    projectId: string
    confirmName: string
    token: string
  }): DeleteProjectResult {
    return executePermanentDelete(this, request)
  }

  busyCode(projectId: string): string | null {
    return this.busy === undefined ? null : this.busy(projectId)
  }
}

/** Reads a Zotero metadata blob; unknown shapes yield zero values. */
export function parsePaperMetadata(raw: string): { title: string; authors: string; year: number } {
  let doc: unknown
  try {
    doc = JSON.parse(raw)
  } catch {
    return { title: '', authors: '', year: 0 }
  }
  if (typeof doc !== 'object' || doc === null || Array.isArray(doc)) {
    return { title: '', authors: '', year: 0 }
  }
  const record = doc as Record<string, unknown>
  const title = typeof record.title === 'string' ? record.title : ''
  const names: string[] = []
  const creators = Array.isArray(record.creators) ? record.creators : []
  for (const entry of creators) {
    if (typeof entry !== 'object' || entry === null) continue
    const creator = entry as Record<string, unknown>
    const name = personName(creator.firstName, creator.lastName, creator.name, '')
    if (name !== '') names.push(name)
  }
  if (names.length === 0 && Array.isArray(record.author)) {
    for (const entry of record.author) {
      if (typeof entry !== 'object' || entry === null) continue
      const author = entry as Record<string, unknown>
      const name = personName(author.firstName, author.lastName, author.name, author.literal)
      if (name !== '') names.push(name)
    }
  }
  let year = 0
  const issued = record.issued
  if (typeof issued === 'object' && issued !== null) {
    const parts = (issued as Record<string, unknown>)['date-parts']
    if (Array.isArray(parts) && Array.isArray(parts[0]) && typeof parts[0][0] === 'number') {
      year = parts[0][0]
    }
  }
  if (year === 0 && typeof record.date === 'string') year = leadingYear(record.date)
  if (year === 0 && typeof record.year === 'number') year = record.year
  return { title, authors: names.join(', '), year }
}

function personName(
  first: unknown,
  last: unknown,
  name: unknown,
  literal: unknown
): string {
  const institutional = typeof name === 'string' ? name.trim() : ''
  if (institutional !== '') return institutional
  const single = typeof literal === 'string' ? literal.trim() : ''
  if (single !== '') return single
  const given = typeof first === 'string' ? first.trim() : ''
  const family = typeof last === 'string' ? last.trim() : ''
  if (given !== '' && family !== '') return `${given} ${family}`
  return family !== '' ? family : given
}

function leadingYear(value: string): number {
  const match = /\d{4}/.exec(value)
  if (match === null) return 0
  const year = Number(match[0])
  return year >= 1000 ? year : 0
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function activityRoute(projectId: string, entityType: string, entityId: string, deleted: boolean): ActivityRoute {
  if (deleted) {
    return { kind: 'trash', projectId, entityId, params: { entity_type: entityType } }
  }
  const kind = ROUTE_KIND[ENTITY_ROUTE_KIND[entityType] ?? ''] ?? 'project'
  return { kind, projectId, entityId }
}

/** Overview/dashboard share this so a widget never renders a deleted entity. */
export function isTrashed(db: Db): Set<string> {
  const rows = db.prepare(`SELECT entity_type, id FROM v_trash`).all() as Array<{
    entity_type: string
    id: string
  }>
  return new Set(rows.map((row) => `${row.entity_type}\u0000${row.id}`))
}
