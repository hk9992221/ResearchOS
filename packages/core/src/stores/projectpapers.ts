/**
 * The project ↔ global-Paper association in project.db.project_papers
 * This table is the only place that links
 * a project to a paper: `entity_relations` must never carry that relation.
 */
import { type Clock, SystemClock, formatTimestamp, parseTimestamp } from '../clock'
import { AppErrorCode, appError } from '../errors'
import type { EventBus, EventTx } from '../events'
import { isValidId } from '../ids'
import type { Actor } from '../schema'
import type { ProjectHandle } from '../workspace/handle'

export interface ProjectPaperLink {
  projectId: string
  paperId: string
  linkedAt: Date
  /** `user` or `mcp` only. */
  linkedBy: Actor
}

export interface LinkInput {
  projectId: string
  paperId: string
  linkedAt?: Date
  linkedBy: Actor
}

const SELECT_COLUMNS = 'SELECT project_id, paper_id, linked_at, linked_by FROM project_papers'

interface LinkRow {
  project_id: string
  paper_id: string
  linked_at: string
  linked_by: Actor
}

export class ProjectPaperStore {
  private readonly project: ProjectHandle
  private readonly events: EventBus
  private readonly clock: Clock

  constructor(project: ProjectHandle, events: EventBus, clock: Clock = new SystemClock()) {
    this.project = project
    this.events = events
    this.clock = clock
  }

  /** Associates a paper. A duplicate is idempotent: `false`, no write, no event. */
  link(tx: EventTx, input: LinkInput): boolean {
    return this.writeLink(tx, input, true)
  }

  /**
   * Associates exactly like `link` but never emits `relation.added`. The
   * literature import pipeline uses it because that flow records
   * `paper.imported` instead.
   */
  linkQuiet(tx: EventTx, input: LinkInput): boolean {
    return this.writeLink(tx, input, false)
  }

  /** Removes the association. A missing row is a no-op (`false`). */
  unlink(tx: EventTx, projectId: string, paperId: string, actor: Actor): boolean {
    if (projectId !== this.project.id) {
      throw appError(
        AppErrorCode.InvalidArgument,
        `projectpapers: unlink project ${JSON.stringify(projectId)} does not match the store project ${this.project.id}`
      )
    }
    const result = tx.db
      .prepare('DELETE FROM project_papers WHERE project_id = ? AND paper_id = ?')
      .run(projectId, paperId)
    if (result.changes === 0) return false
    this.events.emit(tx, {
      type: 'relation.removed',
      entityType: 'paper',
      entityId: paperId,
      old: tuple(projectId, paperId),
      summary: '解除文献关联',
      actor
    })
    return true
  }

  byProject(projectId: string): ProjectPaperLink[] {
    const rows = this.project.db
      .prepare(`${SELECT_COLUMNS} WHERE project_id = ? ORDER BY linked_at, paper_id`)
      .all(projectId) as LinkRow[]
    return rows.map(decode)
  }

  byPaper(paperId: string): ProjectPaperLink[] {
    const rows = this.project.db
      .prepare(`${SELECT_COLUMNS} WHERE paper_id = ? ORDER BY linked_at, project_id`)
      .all(paperId) as LinkRow[]
    return rows.map(decode)
  }

  /** Every referenced paper id: the reference-count candidate set. */
  paperIds(): string[] {
    return this.project.db
      .prepare('SELECT DISTINCT paper_id FROM project_papers ORDER BY paper_id')
      .pluck()
      .all() as string[]
  }

  /**
   * How many associations this project has for `paperId`. `readable: false`
   * means the association table is structurally broken, which must abort the
   * shared-paper GC rather than delete live copies.
   */
  countRefs(paperId: string): { count: number; readable: boolean } {
    try {
      const row = this.project.db
        .prepare('SELECT count(*) AS n FROM project_papers WHERE paper_id = ?')
        .get(paperId) as { n: number }
      return { count: row.n, readable: true }
    } catch {
      return { count: 0, readable: false }
    }
  }

  private writeLink(tx: EventTx, input: LinkInput, emitEvent: boolean): boolean {
    if (input.projectId !== this.project.id) {
      throw appError(
        AppErrorCode.InvalidArgument,
        `projectpapers: link project ${JSON.stringify(input.projectId)} does not match the store project ${this.project.id}`
      )
    }
    if (!isValidId(input.projectId) || !isValidId(input.paperId)) {
      throw appError(
        AppErrorCode.BadEntityId,
        `project ${JSON.stringify(input.projectId)} paper ${JSON.stringify(input.paperId)}`
      )
    }
    if (input.linkedBy !== 'user' && input.linkedBy !== 'mcp') {
      throw appError(AppErrorCode.BadActor, `linked_by ${JSON.stringify(input.linkedBy)} must be user or mcp`)
    }
    const at = input.linkedAt ?? this.clock.now()
    const result = tx.db
      .prepare(
        `INSERT INTO project_papers(project_id, paper_id, linked_at, linked_by)
         VALUES (?, ?, ?, ?)
         ON CONFLICT(project_id, paper_id) DO NOTHING`
      )
      .run(input.projectId, input.paperId, formatTimestamp(at), input.linkedBy)
    if (result.changes === 0) return false
    if (emitEvent) {
      this.events.emit(tx, {
        type: 'relation.added',
        entityType: 'paper',
        entityId: input.paperId,
        new: tuple(input.projectId, input.paperId),
        summary: '关联文献',
        actor: input.linkedBy
      })
    }
    return true
  }
}

function decode(row: LinkRow): ProjectPaperLink {
  return {
    projectId: row.project_id,
    paperId: row.paper_id,
    linkedAt: parseTimestamp(row.linked_at),
    linkedBy: row.linked_by
  }
}

/** The 5-key relation payload shared with `relation.*` events. */
function tuple(projectId: string, paperId: string): Record<string, string> {
  return {
    src_type: 'project',
    src_id: projectId,
    relation: 'related_to',
    dst_type: 'paper',
    dst_id: paperId
  }
}