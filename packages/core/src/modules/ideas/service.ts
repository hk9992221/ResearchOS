/**
 * The Ideas service: lifecycle, relations, evidence, history and the AI
 * extraction skill. One instance serves IPC and MCP; every write
 * runs in one `BEGIN IMMEDIATE` transaction and emits its activity in the same
 * transaction, so a failure leaves no partial state (plan §5 defect 4).
 */
import { IDEA_STATUSES } from '@researchos/shared'
import type {
  EvidenceDirection,
  EvidenceExtractionPreview,
  EvidencePage,
  EvidenceSkippedDraft,
  EvidenceView,
  Idea,
  IdeaDetail,
  IdeaEntityRef,
  IdeaHistoryEntry,
  IdeaHistoryPage,
  IdeaPage,
  IdeaRelationSet,
  IdeaStatus,
  IdeaSummary,
  JsonObject
} from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import { AppErrorCode, asAppError, isAppError } from '../../errors'
import { isValidId, newId } from '../../ids'
import type { ProjectHandle } from '../../workspace/handle'
import type { ActivityEvent } from '../../events'
import { isRelationAllowed, type EntityType, type Relation } from '../../schema'
import type { IdeasActor, IdeasServiceOptions } from './types'
import {
  IDEA_ERROR,
  evidenceNotFound,
  ideaError,
  invalidInput,
  typedError
} from './errors'
import {
  EVIDENCE_COLUMNS,
  IDEA_COLUMNS,
  evidenceCounts,
  type EvidenceRow,
  type IdeaRow,
  type RawEvidenceRow,
  mapEvidenceRow,
  mapIdeaRow,
  readEvidence,
  requireEvidence,
  requireIdea,
  resolveTarget,
  toEvidence,
  toIdea,
  validateEvidenceSource
} from './repo'
import {
  decodeCursor,
  encodeCursor,
  escapeLike,
  hashContent,
  normalizeEvidenceContent,
  normalizeRejectionReason,
  normalizeTitle
} from './text'
import { IDEA_LIMITS, runeCount } from './limits'
import { DatabaseSourceResolver } from './sources'
import {
  PROMPT_VERSION,
  SKILL_NAME,
  buildSkillRequest,
  budgetSources,
  parseAIOutput,
  validateAIItems,
  type AICandidate,
  type SkillSource
} from './extract'
import type {
  AddEvidenceInput,
  ChangeStatusInput,
  EvidenceBatchConfirmResult,
  EvidenceBatchDiscardResult,
  EvidenceBatchFailure,
  ExtractDraftsInput,
  ExtractDraftsResult,
  ExtractionJobResult,
  IdeasWorkspace,
  ListEvidenceInput,
  ListIdeasInput,
  UpdateEvidenceInput,
  UpdateIdeaPatch,
  WriteOptions
} from './types'

const STATUS_LABEL: Record<IdeaStatus, string> = {
  New: '新建',
  Researching: '研究中',
  Validating: '验证中',
  Supported: '已支持',
  PartiallySupported: '部分支持',
  Inconclusive: '不确定',
  Rejected: '已否定',
  OnHold: '搁置'
}

const DIRECTION_LABEL: Record<EvidenceDirection, string> = { support: '支持', oppose: '反对' }

const RELATION_LABEL: Record<string, string> = { paper: '论文', note: '笔记', experiment: '实验', run: 'Run' }

const DEFAULT_PAGE = 50
const MAX_PAGE = 200

interface HistoryCandidate {
  entry: IdeaHistoryEntry
  atText: string
  sortId: string
}

function pageSize(limit: number | undefined): number {
  if (limit === undefined || limit <= 0) return DEFAULT_PAGE
  return Math.min(limit, MAX_PAGE)
}

function isStatus(value: string): value is IdeaStatus {
  return (IDEA_STATUSES as readonly string[]).includes(value)
}

export class IdeasService {
  private readonly workspace: IdeasWorkspace
  private readonly events: IdeasServiceOptions['events']
  private readonly clock: IdeasServiceOptions['clock']
  private readonly ai: IdeasServiceOptions['ai']
  private readonly sources: NonNullable<IdeasServiceOptions['sources']>
  private readonly jobs: IdeasServiceOptions['jobs']

  constructor(options: IdeasServiceOptions) {
    this.workspace = options.workspace
    this.events = options.events
    this.clock = options.clock
    this.ai = options.ai
    this.sources = options.sources ?? new DatabaseSourceResolver(options.workspace)
    this.jobs = options.jobs
  }

  /* ------------------------------------------------------------ plumbing */

  /** Resolves a registered project; an unknown id is NOT_FOUND, not INTERNAL. */
  private handle(projectId: string): ProjectHandle {
    const row = this.workspace.global.db
      .prepare('SELECT status FROM projects WHERE id = ?')
      .get(projectId) as { status: string } | undefined
    if (row === undefined) {
      throw ideaError(IDEA_ERROR.projectNotFound, AppErrorCode.NotFound, `project ${projectId} does not exist`, {
        project_id: projectId
      })
    }
    return this.workspace.project(projectId)
  }

  /** Resolves the project and enforces the archived read-only rule. */
  private begin(projectId: string): ProjectHandle {
    const handle = this.handle(projectId)
    const row = this.workspace.global.db
      .prepare('SELECT status FROM projects WHERE id = ?')
      .get(projectId) as { status: string } | undefined
    if (row?.status === 'archived') {
      throw ideaError(IDEA_ERROR.projectReadOnly, AppErrorCode.ArchivedReadOnly, `project ${projectId} is archived`, {
        project_id: projectId
      })
    }
    return handle
  }

  private actorOf(actor: string): IdeasActor {
    if (actor === 'user' || actor === 'mcp') return actor
    throw ideaError(
      IDEA_ERROR.invalidActor,
      AppErrorCode.Forbidden,
      `actor ${JSON.stringify(actor)} may not perform this write`,
      { actor }
    )
  }

  private now(): Date {
    return this.clock.now()
  }

  private nextId(): string {
    return newId(this.clock)
  }

  private emit(handle: ProjectHandle, event: ActivityEvent): void {
    this.events.emit(handle, event)
  }

  /* --------------------------------------------------------------- ideas */

  createIdea(projectId: string, input: { title: string; description?: string; actor?: IdeasActor }): Idea {
    const actor = this.actorOf(input.actor ?? 'user')
    const title = normalizeTitle(input.title)
    const description = input.description ?? ''
    if (runeCount(description) > IDEA_LIMITS.descriptionRunes) {
      throw invalidInput(`description must be at most ${IDEA_LIMITS.descriptionRunes} characters`)
    }
    const handle = this.begin(projectId)
    const id = this.nextId()
    const at = this.now()
    const atText = formatTimestamp(at)

    handle.inTx(() => {
      handle.db
        .prepare(
          `INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at, deleted_at)
           VALUES (?, ?, ?, 'New', '', ?, ?, NULL)`
        )
        .run(id, title, description, atText, atText)
      this.emit(handle, {
        type: 'entity.created',
        entityType: 'idea',
        entityId: id,
        summary: `创建 Idea：${title}`,
        actor,
        at
      })
    })
    return toIdea(requireIdea(handle.db, id))
  }

  /** Edits title/description. No activity event. */
  updateIdea(projectId: string, ideaId: string, patch: UpdateIdeaPatch, options: WriteOptions): Idea {
    this.actorOf(options.actor)
    const handle = this.begin(projectId)
    let updated: IdeaRow | undefined
    handle.inTx(() => {
      const idea = requireIdea(handle.db, ideaId)
      const title = patch.title === undefined ? undefined : normalizeTitle(patch.title)
      const description = patch.description
      if (description !== undefined && runeCount(description) > IDEA_LIMITS.descriptionRunes) {
        throw invalidInput(`description must be at most ${IDEA_LIMITS.descriptionRunes} characters`)
      }
      if (title === undefined && description === undefined) {
        updated = idea
        return
      }
      if (options.expectedUpdatedAt !== undefined && options.expectedUpdatedAt !== idea.updatedAtText) {
        throw staleConflict(`idea ${ideaId} was modified`, { current_updated_at: idea.updatedAtText })
      }
      const atText = formatTimestamp(this.now())
      handle.db
        .prepare(
          `UPDATE ideas SET title = COALESCE(?, title), description = COALESCE(?, description), updated_at = ?
            WHERE id = ?${options.expectedUpdatedAt === undefined ? '' : ' AND updated_at = ?'}`
        )
        .run(
          ...(options.expectedUpdatedAt === undefined
            ? [title ?? null, description ?? null, atText, ideaId]
            : [title ?? null, description ?? null, atText, ideaId, options.expectedUpdatedAt])
        )
      updated = requireIdea(handle.db, ideaId)
    })
    return toIdea(updated as IdeaRow)
  }

  getIdea(projectId: string, ideaId: string): IdeaDetail {
    const handle = this.handle(projectId)
    const idea = requireIdea(handle.db, ideaId, true)
    return {
      ...toIdea(idea),
      evidence: evidenceCounts(handle.db, ideaId),
      relations: this.listRelations(projectId, ideaId),
      history: this.history(projectId, { ideaId, limit: 5 }).items
    }
  }

  listIdeas(projectId: string, input: ListIdeasInput = {}): IdeaPage {
    const handle = this.handle(projectId)
    const clauses = [input.includeDeleted === true ? 'i.deleted_at IS NOT NULL' : 'i.deleted_at IS NULL']
    const args: unknown[] = []
    if (input.statuses !== undefined && input.statuses.length > 0) {
      for (const status of input.statuses) {
        if (!isStatus(status)) {
          throw ideaError(
            IDEA_ERROR.invalidStatus,
            AppErrorCode.InvalidArgument,
            `status ${JSON.stringify(status)} is not registered`,
            { status }
          )
        }
      }
      clauses.push(`i.status IN (${input.statuses.map(() => '?').join(',')})`)
      args.push(...input.statuses)
    }
    const text = input.text?.trim() ?? ''
    if (text !== '') {
      const pattern = `%${escapeLike(text)}%`
      clauses.push("(i.title LIKE ? ESCAPE '\\' OR i.description LIKE ? ESCAPE '\\')")
      args.push(pattern, pattern)
    }
    const cursor = decodeCursor(input.cursor)
    if (cursor !== undefined) {
      clauses.push('(i.updated_at < ? OR (i.updated_at = ? AND i.id < ?))')
      args.push(cursor.key, cursor.key, cursor.id)
    }
    const where = ` WHERE ${clauses.join(' AND ')}`
    const total = (
      handle.db.prepare(`SELECT count(*) AS n FROM ideas i${where}`).get(...args) as { n: number }
    ).n
    const limit = pageSize(input.limit)
    const rows = handle.db
      .prepare(`SELECT ${IDEA_COLUMNS.split(', ')
        .map((column) => `i.${column.trim()}`)
        .join(', ')},
          (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.direction = 'support'
             AND e.confirmed = 1 AND e.deleted_at IS NULL) AS support_count,
          (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.direction = 'oppose'
             AND e.confirmed = 1 AND e.deleted_at IS NULL) AS oppose_count,
          (SELECT count(*) FROM evidence e WHERE e.idea_id = i.id AND e.ai_generated = 1
             AND e.confirmed = 0 AND e.deleted_at IS NULL) AS draft_count,
          (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
             AND r.relation = 'related_to' AND r.dst_type = 'paper') AS linked_papers,
          (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
             AND r.relation = 'related_to' AND r.dst_type = 'note') AS linked_notes,
          (SELECT count(*) FROM entity_relations r WHERE r.src_type = 'experiment'
             AND r.relation = 'validates' AND r.dst_type = 'idea' AND r.dst_id = i.id) AS linked_experiments,
          COALESCE((SELECT r.dst_id FROM entity_relations r WHERE r.src_type = 'idea' AND r.src_id = i.id
             AND r.relation = 'derived_from' ORDER BY r.created_at, r.id LIMIT 1), '') AS derived_from_id,
          COALESCE((SELECT i2.title FROM entity_relations r JOIN ideas i2 ON i2.id = r.dst_id
             WHERE r.src_type = 'idea' AND r.src_id = i.id AND r.relation = 'derived_from'
             ORDER BY r.created_at, r.id LIMIT 1), '') AS derived_from_title
         FROM ideas i${where} ORDER BY i.updated_at DESC, i.id DESC LIMIT ?`)
      .all(...args, limit + 1) as Array<
      Record<string, unknown> & {
        support_count: number
        oppose_count: number
        draft_count: number
        linked_papers: number
        linked_notes: number
        linked_experiments: number
        derived_from_id: string
        derived_from_title: string
      }
    >
    const mapped = rows.map((row) => {
      const idea = mapIdeaRow({
        id: String(row.id),
        title: String(row.title),
        description: String(row.description),
        status: String(row.status),
        rejection_reason: String(row.rejection_reason),
        created_at: String(row.created_at),
        updated_at: String(row.updated_at),
        deleted_at: row.deleted_at === null ? null : String(row.deleted_at)
      })
      const summary: IdeaSummary = {
        ...toIdea(idea),
        supportCount: row.support_count,
        opposeCount: row.oppose_count,
        draftCount: row.draft_count,
        linkedPapers: row.linked_papers,
        linkedNotes: row.linked_notes,
        linkedExperiments: row.linked_experiments,
        derivedFromId: row.derived_from_id === '' ? null : row.derived_from_id,
        derivedFromTitle: row.derived_from_title === '' ? null : row.derived_from_title
      }
      return { summary, row: idea }
    })
    const items = mapped.slice(0, limit)
    const last = items.at(-1)
    return {
      items: items.map((item) => item.summary),
      total,
      limit,
      offset: 0,
      nextCursor:
        mapped.length > limit && last !== undefined ? encodeCursor({ sort: 'updated_desc', key: last.row.updatedAtText, id: last.row.id }) : null
    }
  }

  /** Applies an explicit user/MCP status change (IDEA-03/08). */
  changeStatus(projectId: string, ideaId: string, input: ChangeStatusInput): Idea {
    const actor = this.actorOf(input.actor)
    if (!isStatus(input.status)) {
      throw ideaError(
        IDEA_ERROR.invalidStatus,
        AppErrorCode.InvalidArgument,
        `status ${JSON.stringify(input.status)} is not registered`,
        { status: input.status }
      )
    }
    const handle = this.begin(projectId)
    let updated: IdeaRow | undefined
    handle.inTx(() => {
      const idea = requireIdea(handle.db, ideaId)
      if (input.expectedUpdatedAt !== undefined && input.expectedUpdatedAt !== idea.updatedAtText) {
        throw staleConflict(`idea ${ideaId} was modified`, { current_updated_at: idea.updatedAtText })
      }
      if (idea.status === input.status) {
        updated = idea
        return
      }
      // Leaving Rejected keeps the historical reason; entering Rejected
      // Requires a new one.
      const reason = input.status === 'Rejected' ? normalizeRejectionReason(input.rejectionReason ?? '') : idea.rejectionReason
      const at = this.now()
      handle.db
        .prepare('UPDATE ideas SET status = ?, rejection_reason = ?, updated_at = ? WHERE id = ?')
        .run(input.status, reason, formatTimestamp(at), ideaId)
      let summary = `${STATUS_LABEL[idea.status]} → ${STATUS_LABEL[input.status]}`
      if (input.status === 'Rejected') summary += `：${reason}`
      if (input.note !== undefined && input.note.trim() !== '') summary += `（${input.note.trim()}）`
      this.emit(handle, {
        type: 'status.changed',
        entityType: 'idea',
        entityId: ideaId,
        old: { status: idea.status },
        new: { status: input.status },
        summary,
        actor,
        at
      })
      updated = requireIdea(handle.db, ideaId)
    })
    return toIdea(updated as IdeaRow)
  }

  /** Soft-deletes the Idea. Evidence and relations are untouched (AC-08). */
  deleteIdea(projectId: string, ideaId: string, actor: IdeasActor = 'user'): { deleted: boolean } {
    const who = this.actorOf(actor)
    const handle = this.begin(projectId)
    let deleted = false
    handle.inTx(() => {
      const idea = requireIdea(handle.db, ideaId, true)
      if (idea.deletedAt !== null) {
        deleted = false
        return
      }
      const at = this.now()
      handle.db.prepare('UPDATE ideas SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL').run(formatTimestamp(at), ideaId)
      this.emit(handle, {
        type: 'entity.deleted',
        entityType: 'idea',
        entityId: ideaId,
        summary: `删除 Idea：${idea.title}`,
        actor: who,
        at
      })
      deleted = true
    })
    return { deleted }
  }

  restoreIdea(projectId: string, ideaId: string, actor: IdeasActor = 'user'): Idea {
    const who = this.actorOf(actor)
    const handle = this.begin(projectId)
    handle.inTx(() => {
      const idea = requireIdea(handle.db, ideaId, true)
      if (idea.deletedAt === null) return
      const at = this.now()
      handle.db.prepare('UPDATE ideas SET deleted_at = NULL WHERE id = ?').run(ideaId)
      this.emit(handle, {
        type: 'entity.restored',
        entityType: 'idea',
        entityId: ideaId,
        summary: `恢复 Idea：${idea.title}`,
        actor: who,
        at
      })
    })
    return toIdea(requireIdea(handle.db, ideaId))
  }

  /** Creates a derived Idea plus the `derived_from` triple (§4.2.3). */
  deriveIdea(
    projectId: string,
    parentIdeaId: string,
    input: { title: string; description?: string },
    actor: IdeasActor = 'user'
  ): Idea {
    const who = this.actorOf(actor)
    const title = normalizeTitle(input.title)
    const description = input.description ?? ''
    if (runeCount(description) > IDEA_LIMITS.descriptionRunes) {
      throw invalidInput(`description must be at most ${IDEA_LIMITS.descriptionRunes} characters`)
    }
    const handle = this.begin(projectId)
    const id = this.nextId()
    const at = this.now()
    handle.inTx(() => {
      const parent = requireIdea(handle.db, parentIdeaId)
      this.assertLineageDepth(handle, parent.id)
      handle.db
        .prepare(
          `INSERT INTO ideas(id, title, description, status, rejection_reason, created_at, updated_at, deleted_at)
           VALUES (?, ?, ?, 'New', '', ?, ?, NULL)`
        )
        .run(id, title, description, formatTimestamp(at), formatTimestamp(at))
      this.emit(handle, {
        type: 'entity.created',
        entityType: 'idea',
        entityId: id,
        summary: `由「${parent.title}」衍生`,
        actor: who,
        at
      })
      this.linkTriple(
        handle,
        { srcType: 'idea', srcId: id, relation: 'derived_from', dstType: 'idea', dstId: parent.id },
        who,
        `derived_from → ${parent.title}`,
        at
      )
    })
    return toIdea(requireIdea(handle.db, id))
  }

  private assertLineageDepth(handle: ProjectHandle, ideaId: string): void {
    const seen = new Set<string>()
    let current = ideaId
    let depth = 0
    for (;;) {
      if (seen.has(current)) {
        throw ideaError(
          IDEA_ERROR.lineageTooDeep,
          AppErrorCode.Conflict,
          `derived_from cycle detected at idea ${current}`,
          { idea_id: current }
        )
      }
      seen.add(current)
      const row = handle.db
        .prepare(
          `SELECT dst_id FROM entity_relations
            WHERE src_type = 'idea' AND src_id = ? AND relation = 'derived_from' AND dst_type = 'idea'
            LIMIT 1`
        )
        .get(current) as { dst_id: string } | undefined
      if (row === undefined) return
      depth += 1
      if (depth >= IDEA_LIMITS.maxLineageDepth) {
        throw ideaError(
          IDEA_ERROR.lineageTooDeep,
          AppErrorCode.LimitExceeded,
          `derived_from chain exceeds ${IDEA_LIMITS.maxLineageDepth} levels`,
          { idea_id: ideaId, depth }
        )
      }
      current = row.dst_id
    }
  }

  /* ----------------------------------------------------------- relations */

  listRelations(projectId: string, ideaId: string): IdeaRelationSet {
    const handle = this.handle(projectId)
    requireIdea(handle.db, ideaId, true)
    const rows = handle.db
      .prepare(
        `SELECT src_type, src_id, relation, dst_type, dst_id FROM entity_relations
          WHERE (src_type = 'idea' AND src_id = ?) OR (dst_type = 'idea' AND dst_id = ?)
          ORDER BY created_at, id`
      )
      .all(ideaId, ideaId) as Array<{
      src_type: string
      src_id: string
      relation: string
      dst_type: string
      dst_id: string
    }>
    const out: IdeaRelationSet = { papers: [], notes: [], experiments: [], derivedFrom: [], derived: [] }
    const ref = (type: string, id: string): IdeaEntityRef => {
      const info = resolveTarget(handle.db, this.workspace.global.db, projectId, type, id)
      return { type, id, title: info.title, deleted: info.deleted, brokenRef: info.broken }
    }
    for (const row of rows) {
      if (row.src_type === 'idea' && row.src_id === ideaId && row.relation === 'related_to' && row.dst_type === 'paper') {
        out.papers.push(ref('paper', row.dst_id))
      } else if (
        row.src_type === 'idea' &&
        row.src_id === ideaId &&
        row.relation === 'related_to' &&
        row.dst_type === 'note'
      ) {
        out.notes.push(ref('note', row.dst_id))
      } else if (
        row.src_type === 'experiment' &&
        row.relation === 'validates' &&
        row.dst_type === 'idea' &&
        row.dst_id === ideaId
      ) {
        out.experiments.push(ref('experiment', row.src_id))
      } else if (row.src_type === 'idea' && row.src_id === ideaId && row.relation === 'derived_from') {
        out.derivedFrom.push(ref('idea', row.dst_id))
      } else if (
        row.src_type === 'idea' &&
        row.relation === 'derived_from' &&
        row.dst_type === 'idea' &&
        row.dst_id === ideaId
      ) {
        out.derived.push(ref('idea', row.src_id))
      }
    }
    return out
  }

  linkEntity(
    projectId: string,
    ideaId: string,
    target: { type: 'paper' | 'note' | 'experiment'; id: string },
    actor: IdeasActor = 'user'
  ): { added: boolean } {
    const who = this.actorOf(actor)
    const triple = relationTriple(ideaId, target)
    const handle = this.begin(projectId)
    let added = false
    handle.inTx(() => {
      requireIdea(handle.db, ideaId)
      if (this.findTriple(handle, triple) !== undefined) return
      const info = resolveTarget(handle.db, this.workspace.global.db, projectId, target.type, target.id)
      if (info.broken) {
        throw ideaError(
          IDEA_ERROR.targetNotFound,
          AppErrorCode.NotFound,
          `${target.type} ${target.id} does not exist or is not linked to the project`,
          { target_type: target.type, target_id: target.id }
        )
      }
      if (info.deleted) {
        throw ideaError(
          IDEA_ERROR.targetDeleted,
          AppErrorCode.EntityDeleted,
          `${target.type} ${target.id} is deleted`,
          { target_type: target.type, target_id: target.id }
        )
      }
      added = this.linkTriple(
        handle,
        triple,
        who,
        `关联${RELATION_LABEL[target.type] ?? target.type}：${info.title}`,
        this.now()
      )
    })
    return { added }
  }

  unlinkEntity(
    projectId: string,
    ideaId: string,
    target: { type: 'paper' | 'note' | 'experiment'; id: string },
    actor: IdeasActor = 'user'
  ): { removed: boolean } {
    const who = this.actorOf(actor)
    const triple = relationTriple(ideaId, target)
    const handle = this.begin(projectId)
    let removed = false
    handle.inTx(() => {
      requireIdea(handle.db, ideaId)
      const info = resolveTarget(handle.db, this.workspace.global.db, projectId, target.type, target.id)
      removed = this.unlinkTriple(
        handle,
        triple,
        who,
        `解除${RELATION_LABEL[target.type] ?? target.type}：${info.title}`,
        this.now()
      )
    })
    return { removed }
  }

  private findTriple(handle: ProjectHandle, triple: RelationTriple): string | undefined {
    const row = handle.db
      .prepare(
        `SELECT id FROM entity_relations
          WHERE src_type = ? AND src_id = ? AND relation = ? AND dst_type = ? AND dst_id = ?`
      )
      .get(triple.srcType, triple.srcId, triple.relation, triple.dstType, triple.dstId) as
      | { id: string }
      | undefined
    return row?.id
  }

  private linkTriple(
    handle: ProjectHandle,
    triple: RelationTriple,
    actor: IdeasActor,
    summary: string,
    at: Date
  ): boolean {
    if (
      !isRelationAllowed(triple.srcType as EntityType, triple.relation as Relation, triple.dstType as EntityType)
    ) {
      throw ideaError(
        IDEA_ERROR.invalidInput,
        AppErrorCode.InvalidArgument,
        `relation ${triple.srcType} → ${triple.relation} → ${triple.dstType} is not allowed`,
        { relation: triple.relation }
      )
    }
    if (this.findTriple(handle, triple) !== undefined) return false
    const id = this.nextId()
    handle.db
      .prepare(
        `INSERT INTO entity_relations(id, src_type, src_id, relation, dst_type, dst_id, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`
      )
      .run(id, triple.srcType, triple.srcId, triple.relation, triple.dstType, triple.dstId, formatTimestamp(at))
    const subject = triple.srcType === 'idea' ? triple.srcId : triple.dstId
    this.emit(handle, {
      type: 'relation.added',
      entityType: 'idea',
      entityId: subject,
      new: {
        src_type: triple.srcType,
        src_id: triple.srcId,
        relation: triple.relation,
        dst_type: triple.dstType,
        dst_id: triple.dstId
      },
      summary,
      actor,
      at
    })
    return true
  }

  private unlinkTriple(handle: ProjectHandle, triple: RelationTriple, actor: IdeasActor, summary: string, at: Date): boolean {
    const id = this.findTriple(handle, triple)
    if (id === undefined) return false
    handle.db.prepare('DELETE FROM entity_relations WHERE id = ?').run(id)
    const subject = triple.srcType === 'idea' ? triple.srcId : triple.dstId
    this.emit(handle, {
      type: 'relation.removed',
      entityType: 'idea',
      entityId: subject,
      old: {
        src_type: triple.srcType,
        src_id: triple.srcId,
        relation: triple.relation,
        dst_type: triple.dstType,
        dst_id: triple.dstId
      },
      summary,
      actor,
      at
    })
    return true
  }

  /* ------------------------------------------------------------- history */

  /**
   * Merged, newest-first history: the Idea's own events, its evidence events
   * and synthesized “衍生出 Idea” entries. Read-only; activities are never
   * rewritten, so earlier entries cannot be lost by later changes.
   */
  history(projectId: string, input: { ideaId: string; kinds?: string[]; limit?: number; cursor?: string }): IdeaHistoryPage {
    const handle = this.handle(projectId)
    const idea = requireIdea(handle.db, input.ideaId, true)
    const candidates = this.historyCandidates(handle, idea)
    const wanted = input.kinds === undefined || input.kinds.length === 0 ? undefined : new Set(input.kinds)
    const filtered =
      wanted === undefined ? candidates : candidates.filter((candidate) => wanted.has(candidate.entry.kind))
    filtered.sort((left, right) => {
      if (left.atText !== right.atText) return left.atText < right.atText ? 1 : -1
      return left.sortId < right.sortId ? 1 : -1
    })
    const total = filtered.length
    const cursor = decodeCursor(input.cursor)
    const remaining =
      cursor === undefined
        ? filtered
        : filtered.filter(
            (candidate) =>
              candidate.atText < cursor.key || (candidate.atText === cursor.key && candidate.sortId < cursor.id)
          )
    const limit = pageSize(input.limit)
    const items = remaining.slice(0, limit)
    const last = items.at(-1)
    return {
      items: items.map((candidate) => candidate.entry),
      total,
      nextCursor:
        remaining.length > limit && last !== undefined
          ? encodeCursor({ sort: 'history', key: last.atText, id: last.sortId })
          : null
    }
  }

  private historyCandidates(handle: ProjectHandle, idea: IdeaRow): HistoryCandidate[] {
    const rows = handle.db
      .prepare(
        `SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
           FROM activities
          WHERE (entity_type = 'idea' AND entity_id = ?)
             OR (entity_type = 'evidence' AND entity_id IN (SELECT id FROM evidence WHERE idea_id = ?))`
      )
      .all(idea.id, idea.id) as Array<{
      id: string
      event_type: string
      entity_type: string
      entity_id: string
      payload: string
      actor: string
      created_at: string
    }>
    const ideaRef: IdeaEntityRef = {
      type: 'idea',
      id: idea.id,
      title: idea.title,
      deleted: idea.deletedAt !== null,
      brokenRef: false
    }
    const candidates: HistoryCandidate[] = rows.map((row) => {
      let payload: Record<string, unknown> = {}
      try {
        payload = JSON.parse(row.payload) as Record<string, unknown>
      } catch {
        payload = {}
      }
      const ref =
        row.entity_type === 'idea'
          ? ideaRef
          : { type: 'evidence', id: row.entity_id, title: '', deleted: false, brokenRef: false }
      const entry: IdeaHistoryEntry = {
        at: row.created_at,
        activityId: row.id,
        kind: classifyEvent(row.event_type, row.entity_type),
        eventType: row.event_type,
        summary: typeof payload.summary === 'string' ? payload.summary : '',
        actor: row.actor as IdeaHistoryEntry['actor'],
        old: statusValue(payload.old),
        new: statusValue(payload.new),
        ref,
        synthesized: false
      }
      return { entry, atText: row.created_at, sortId: row.id }
    })

    const derived = handle.db
      .prepare(
        `SELECT i.id AS child_id, i.title AS child_title, i.deleted_at AS child_deleted, i.created_at AS child_created
           FROM entity_relations r
           JOIN ideas i ON i.id = r.src_id
          WHERE r.src_type = 'idea' AND r.relation = 'derived_from'
            AND r.dst_type = 'idea' AND r.dst_id = ?
          ORDER BY i.created_at, i.id`
      )
      .all(idea.id) as Array<{ child_id: string; child_title: string; child_deleted: string | null; child_created: string }>
    for (const row of derived) {
      candidates.push({
        entry: {
          at: row.child_created,
          activityId: null,
          kind: 'derived',
          eventType: '',
          summary: `衍生出 Idea：${row.child_title}`,
          actor: 'user',
          old: '',
          new: '',
          ref: {
            type: 'idea',
            id: row.child_id,
            title: row.child_title,
            deleted: row.child_deleted !== null,
            brokenRef: false
          },
          synthesized: true
        },
        atText: row.child_created,
        sortId: `derived:${row.child_id}`
      })
    }
    return candidates
  }

  /* ------------------------------------------------------------ evidence */

  addEvidence(projectId: string, input: AddEvidenceInput): EvidenceView {
    const actor = this.actorOf(input.actor)
    if (input.direction !== 'support' && input.direction !== 'oppose') {
      throw invalidInput(`direction ${JSON.stringify(input.direction)} must be support or oppose`)
    }
    const content = normalizeEvidenceContent(input.content)
    const handle = this.begin(projectId)
    const id = this.nextId()
    const at = this.now()
    let title = ''
    handle.inTx(() => {
      requireIdea(handle.db, input.ideaId)
      title = validateEvidenceSource(handle.db, this.workspace.global.db, projectId, input.source.type, input.source.id)
      handle.db
        .prepare(
          `INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
             ai_generated, confirmed, ai_output_id, provenance, content_hash, created_at, updated_at, deleted_at)
           VALUES (?, ?, ?, ?, ?, ?, 0, 1, NULL, '{}', ?, ?, ?, NULL)`
        )
        .run(
          id,
          input.ideaId,
          input.direction,
          content,
          input.source.type,
          input.source.id,
          hashContent(content),
          formatTimestamp(at),
          formatTimestamp(at)
        )
      this.emit(handle, {
        type: 'entity.created',
        entityType: 'evidence',
        entityId: id,
        summary: `添加${DIRECTION_LABEL[input.direction]}证据（来源：${title}）`,
        actor,
        at
      })
    })
    return this.evidenceView(projectId, handle, requireEvidence(handle.db, id))
  }

  updateEvidence(projectId: string, input: UpdateEvidenceInput): EvidenceView {
    this.actorOf(input.actor)
    const handle = this.begin(projectId)
    let updated: EvidenceRow | undefined
    handle.inTx(() => {
      const evidence = requireEvidence(handle.db, input.evidenceId)
      requireIdea(handle.db, evidence.ideaId)
      if (
        input.expectedUpdatedAt !== undefined &&
        input.expectedUpdatedAt !== evidence.updatedAtText
      ) {
        throw staleConflict(`evidence ${input.evidenceId} was modified`, {
          current_updated_at: evidence.updatedAtText
        })
      }
      let direction = evidence.direction
      if (input.direction !== undefined) {
        if (evidence.confirmed) {
          throw ideaError(
            IDEA_ERROR.evidenceDirectionLocked,
            AppErrorCode.Conflict,
            'direction of confirmed evidence cannot change; delete and recreate instead',
            { evidence_id: input.evidenceId }
          )
        }
        if (input.direction !== 'support' && input.direction !== 'oppose') {
          throw invalidInput(`direction ${JSON.stringify(input.direction)} must be support or oppose`)
        }
        direction = input.direction
      }
      let content = evidence.content
      let hash = evidence.contentHash
      if (input.content !== undefined) {
        content = normalizeEvidenceContent(input.content)
        hash = hashContent(content)
      }
      if (input.direction === undefined && input.content === undefined) {
        updated = evidence
        return
      }
      const atText = formatTimestamp(this.now())
      handle.db
        .prepare(
          `UPDATE evidence SET direction = ?, content = ?, content_hash = ?, updated_at = ?
            WHERE id = ?${input.expectedUpdatedAt === undefined ? '' : ' AND updated_at = ?'}`
        )
        .run(
          ...(input.expectedUpdatedAt === undefined
            ? [direction, content, hash, atText, input.evidenceId]
            : [direction, content, hash, atText, input.evidenceId, input.expectedUpdatedAt])
        )
      updated = requireEvidence(handle.db, input.evidenceId)
    })
    return this.evidenceView(projectId, handle, updated as EvidenceRow)
  }

  listEvidence(projectId: string, input: ListEvidenceInput = {}): EvidencePage {
    const handle = this.handle(projectId)
    if (input.ideaId !== undefined) requireIdea(handle.db, input.ideaId, true)
    const clauses: string[] = []
    const args: unknown[] = []
    if (input.ideaId !== undefined) {
      clauses.push('idea_id = ?')
      args.push(input.ideaId)
    } else {
      clauses.push("idea_id IN (SELECT id FROM ideas WHERE deleted_at IS NULL)")
    }
    clauses.push(input.includeDeleted === true ? 'deleted_at IS NOT NULL' : 'deleted_at IS NULL')
    if (input.direction !== undefined) {
      if (input.direction !== 'support' && input.direction !== 'oppose') {
        throw invalidInput(`direction ${JSON.stringify(input.direction)} must be support or oppose`)
      }
      clauses.push('direction = ?')
      args.push(input.direction)
    }
    if (input.confirmed !== undefined) {
      clauses.push('confirmed = ?')
      args.push(input.confirmed ? 1 : 0)
    }
    const cursor = decodeCursor(input.cursor)
    if (cursor !== undefined) {
      clauses.push('(created_at < ? OR (created_at = ? AND id < ?))')
      args.push(cursor.key, cursor.key, cursor.id)
    }
    const where = ` WHERE ${clauses.join(' AND ')}`
    const total = (handle.db.prepare(`SELECT count(*) AS n FROM evidence${where}`).get(...args) as { n: number }).n
    const limit = pageSize(input.limit)
    const rows = handle.db
      .prepare(`SELECT ${EVIDENCE_COLUMNS} FROM evidence${where} ORDER BY created_at DESC, id DESC LIMIT ?`)
      .all(...args, limit + 1) as RawEvidenceRow[]
    const mapped = rows.map((row) => mapEvidenceRow(row))
    const items = mapped.slice(0, limit).map((row) => this.evidenceView(projectId, handle, row))
    const last = mapped[limit - 1]
    return {
      items,
      total,
      limit,
      offset: 0,
      nextCursor:
        mapped.length > limit && last !== undefined
          ? encodeCursor({ sort: 'evidence', key: formatTimestamp(last.createdAt), id: last.id })
          : null
    }
  }

  /** Confirms drafts one by one, each in its own transaction (§5.6). */
  confirmEvidence(
    projectId: string,
    ideaId: string,
    evidenceIds: readonly string[],
    actor: IdeasActor = 'user'
  ): EvidenceBatchConfirmResult {
    const who = this.actorOf(actor)
    const handle = this.begin(projectId)
    requireIdea(handle.db, ideaId)
    const result: EvidenceBatchConfirmResult = { confirmed: [], failed: [] }
    for (const evidenceId of evidenceIds) {
      const failure = this.confirmOne(handle, ideaId, evidenceId, who)
      if (failure !== undefined) result.failed.push(failure)
      else result.confirmed.push(evidenceId)
    }
    return result
  }

  private confirmOne(
    handle: ProjectHandle,
    ideaId: string,
    evidenceId: string,
    actor: IdeasActor
  ): EvidenceBatchFailure | undefined {
    if (!isValidId(evidenceId)) {
      return { id: evidenceId, code: IDEA_ERROR.evidenceNotFound, message: 'invalid evidence id' }
    }
    try {
      handle.inTx(() => {
        const evidence = requireEvidence(handle.db, evidenceId)
        if (evidence.ideaId !== ideaId) {
          throw evidenceNotFound(`evidence ${evidenceId} does not belong to idea ${ideaId}`, {
            evidence_id: evidenceId,
            idea_id: ideaId
          })
        }
        requireIdea(handle.db, ideaId)
        if (!evidence.aiGenerated) {
          throw ideaError(
            IDEA_ERROR.evidenceNotAIDraft,
            AppErrorCode.InvalidArgument,
            `evidence ${evidenceId} is not an AI draft`,
            { evidence_id: evidenceId }
          )
        }
        if (evidence.confirmed) return
        const at = this.now()
        handle.db.prepare('UPDATE evidence SET confirmed = 1, updated_at = ? WHERE id = ?').run(formatTimestamp(at), evidenceId)
        this.emit(handle, {
          type: 'status.changed',
          entityType: 'evidence',
          entityId: evidenceId,
          old: { status: 'draft' },
          new: { status: 'confirmed' },
          summary: `确认${DIRECTION_LABEL[evidence.direction]}证据`,
          actor,
          at
        })
      })
      return undefined
    } catch (error) {
      const appError = asAppError(error, AppErrorCode.Internal)
      if (appError.code === AppErrorCode.Internal) throw appError
      return { id: evidenceId, code: reasonOf(appError), message: appError.message }
    }
  }

  /** Discards AI drafts (soft delete); confirmed evidence cannot be discarded. */
  discardEvidence(
    projectId: string,
    ideaId: string,
    evidenceIds: readonly string[],
    actor: IdeasActor = 'user'
  ): EvidenceBatchDiscardResult {
    const who = this.actorOf(actor)
    const handle = this.begin(projectId)
    requireIdea(handle.db, ideaId)
    const result: EvidenceBatchDiscardResult = { discarded: [], failed: [] }
    for (const evidenceId of evidenceIds) {
      if (!isValidId(evidenceId)) {
        result.failed.push({ id: evidenceId, code: IDEA_ERROR.evidenceNotFound, message: 'invalid evidence id' })
        continue
      }
      try {
        handle.inTx(() => {
          const evidence = requireEvidence(handle.db, evidenceId)
          if (evidence.ideaId !== ideaId) {
            throw evidenceNotFound(`evidence ${evidenceId} does not belong to idea ${ideaId}`, {
              evidence_id: evidenceId
            })
          }
          requireIdea(handle.db, ideaId)
          if (evidence.confirmed) {
            throw ideaError(
              IDEA_ERROR.evidenceConfirmed,
              AppErrorCode.Conflict,
              'confirmed evidence cannot be discarded; delete it instead',
              { evidence_id: evidenceId }
            )
          }
          const at = this.now()
          handle.db
            .prepare('UPDATE evidence SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL')
            .run(formatTimestamp(at), evidenceId)
          this.emit(handle, {
            type: 'entity.deleted',
            entityType: 'evidence',
            entityId: evidenceId,
            summary: '丢弃 AI 证据草稿',
            actor: who,
            at
          })
        })
        result.discarded.push(evidenceId)
      } catch (error) {
        const appError = asAppError(error, AppErrorCode.Internal)
        if (appError.code === AppErrorCode.Internal) throw appError
        result.failed.push({ id: evidenceId, code: reasonOf(appError), message: appError.message })
      }
    }
    return result
  }

  deleteEvidence(projectId: string, evidenceId: string, actor: IdeasActor = 'user'): { deleted: boolean } {
    const who = this.actorOf(actor)
    const handle = this.begin(projectId)
    let deleted = false
    handle.inTx(() => {
      const evidence = requireEvidence(handle.db, evidenceId, true)
      requireIdea(handle.db, evidence.ideaId)
      if (evidence.deletedAt !== null) return
      const at = this.now()
      handle.db.prepare('UPDATE evidence SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL').run(formatTimestamp(at), evidenceId)
      this.emit(handle, {
        type: 'entity.deleted',
        entityType: 'evidence',
        entityId: evidenceId,
        summary: `删除${DIRECTION_LABEL[evidence.direction]}证据`,
        actor: who,
        at
      })
      deleted = true
    })
    return { deleted }
  }

  /** Single-item confirm used by the IPC command. */
  confirmEvidenceItem(projectId: string, evidenceId: string, actor: IdeasActor = 'user'): EvidenceView {
    const handle = this.begin(projectId)
    const evidence = requireEvidence(handle.db, evidenceId)
    const result = this.confirmEvidence(projectId, evidence.ideaId, [evidenceId], actor)
    const failure = result.failed[0]
    if (failure !== undefined) {
      throw ideaError(
        IDEA_ERROR.invalidInput,
        AppErrorCode.InvalidArgument,
        failure.message,
        { evidence_id: failure.id, failure_code: failure.code }
      )
    }
    return this.evidenceView(projectId, handle, requireEvidence(handle.db, evidenceId))
  }

  discardEvidenceItem(projectId: string, ideaId: string, evidenceId: string): { discarded: boolean } {
    const result = this.discardEvidence(projectId, ideaId, [evidenceId])
    const failure = result.failed[0]
    if (failure !== undefined) {
      throw ideaError(
        IDEA_ERROR.invalidInput,
        AppErrorCode.InvalidArgument,
        failure.message,
        { evidence_id: failure.id, failure_code: failure.code }
      )
    }
    return { discarded: true }
  }

  evidenceById(projectId: string, evidenceId: string): EvidenceView {
    const handle = this.handle(projectId)
    return this.evidenceView(projectId, handle, requireEvidence(handle.db, evidenceId))
  }

  private evidenceView(projectId: string, handle: ProjectHandle, row: EvidenceRow): EvidenceView {
    const info = resolveTarget(handle.db, this.workspace.global.db, projectId, row.sourceType, row.sourceId)
    return {
      ...toEvidence(row),
      source: { type: row.sourceType, id: row.sourceId },
      sourceTitle: info.title,
      sourceDeleted: info.deleted,
      sourceBroken: info.broken
    }
  }

  /* ---------------------------------------------------------- AI extract */

  /** Runs the skill and persists drafts (`ai_generated=1, confirmed=0`). */
  async extractDrafts(projectId: string, input: ExtractDraftsInput): Promise<ExtractDraftsResult> {
    this.actorOf(input.actor)
    if (this.ai === undefined) {
      throw ideaError(IDEA_ERROR.aiDisabled, AppErrorCode.AiUnavailable, 'no chat provider is configured')
    }
    if (input.sourceRefs.length === 0 || input.sourceRefs.length > IDEA_LIMITS.maxDraftSourceRefs) {
      throw invalidInput(`sources must contain 1..${IDEA_LIMITS.maxDraftSourceRefs} entries`)
    }
    const instruction = input.instruction ?? ''
    if (runeCount(instruction) > IDEA_LIMITS.instructionRunes) {
      throw invalidInput(`instruction must be at most ${IDEA_LIMITS.instructionRunes} characters`)
    }
    const maxDrafts = input.maxDrafts ?? IDEA_LIMITS.defaultMaxDrafts
    if (maxDrafts < 1 || maxDrafts > IDEA_LIMITS.maxDrafts) {
      throw invalidInput(`max drafts must be 1..${IDEA_LIMITS.maxDrafts}`)
    }
    const handle = this.begin(projectId)
    const idea = requireIdea(handle.db, input.ideaId)

    const resolver = this.sources
    const seen = new Set<string>()
    const failed: Array<{ type: string; id: string; reason: string }> = []
    const resolved: Array<Omit<SkillSource, 'index' | 'truncated'>> = []
    for (const ref of input.sourceRefs) {
      if (ref.type !== 'experiment' && ref.type !== 'run' && ref.type !== 'note') {
        throw ideaError(
          IDEA_ERROR.invalidSource,
          AppErrorCode.InvalidArgument,
          `skill sources must be experiment, run or note, got ${JSON.stringify(ref.type)}`,
          { source_type: ref.type }
        )
      }
      const key = `${ref.type}:${ref.id}`
      if (seen.has(key)) continue
      seen.add(key)
      try {
        const source = await resolver.resolve(projectId, ref)
        if (source.deleted) {
          failed.push({ type: ref.type, id: ref.id, reason: 'target deleted' })
          continue
        }
        if (source.text.trim() === '') {
          failed.push({ type: ref.type, id: ref.id, reason: 'empty' })
          continue
        }
        resolved.push({ type: ref.type, id: ref.id, title: source.title, text: source.text, version: source.version })
      } catch (error) {
        failed.push({ type: ref.type, id: ref.id, reason: error instanceof Error ? error.message : String(error) })
      }
    }
    if (failed.length > 0) {
      throw ideaError(
        IDEA_ERROR.sourceUnavailable,
        AppErrorCode.ExternalUnavailable,
        'one or more sources could not be resolved',
        { failed }
      )
    }

    const sources = budgetSources(resolved)
    const request = buildSkillRequest(
      { title: idea.title, description: idea.description },
      sources,
      instruction,
      maxDrafts
    )
    let response
    try {
      response = await this.ai.chatStructured(
        'chat_main',
        {
          messages: [
            { role: 'system', content: request.system },
            { role: 'user', content: request.prompt }
          ],
          maxOutputTokens: request.maxOutputTokens,
          signal: input.signal
        },
        request.jsonSchema,
        { name: 'evidence', repairAttempts: 1 }
      )
    } catch (error) {
      throw typedError(error, AppErrorCode.AiUnavailable, IDEA_ERROR.aiUnavailable)
    }
    const items = parseAIOutput(response.json)
    const { candidates, skipped, schemaValid } = validateAIItems(items, sources, maxDrafts)
    if (items.length > 0 && schemaValid === 0) {
      throw ideaError(
        IDEA_ERROR.aiOutputInvalid,
        AppErrorCode.AiOutputInvalid,
        'the model returned no schema-valid evidence items'
      )
    }

    const runId = this.nextId()
    const generatedAt = this.now()
    const generatedAtText = formatTimestamp(generatedAt)
    const anyTruncated = sources.some((source) => source.truncated)
    const provenanceSources = sources.map((source) => ({
      type: source.type,
      id: source.id,
      version: source.version
    }))
    const draftIds: string[] = []
    const skippedDrafts: EvidenceSkippedDraft[] = skipped.map((skip) => ({
      type: skip.type,
      id: skip.id,
      reason: skip.reason,
      contentHash: skip.contentHash
    }))

    handle.inTx(() => {
      requireIdea(handle.db, input.ideaId)
      const batch = new Set<string>()
      const accepted: AICandidate[] = []
      for (const candidate of candidates) {
        const dedupeKey = `${candidate.sourceType}:${candidate.sourceId}:${candidate.hash}`
        if (batch.has(dedupeKey)) {
          skippedDrafts.push({
            type: candidate.sourceType,
            id: candidate.sourceId,
            reason: 'duplicate_batch',
            contentHash: candidate.hash
          })
          continue
        }
        batch.add(dedupeKey)
        const existing = handle.db
          .prepare(
            `SELECT count(*) AS n FROM evidence
              WHERE idea_id = ? AND source_type = ? AND source_id = ? AND content_hash = ?
                AND deleted_at IS NULL`
          )
          .get(input.ideaId, candidate.sourceType, candidate.sourceId, candidate.hash) as { n: number }
        if (existing.n > 0) {
          skippedDrafts.push({
            type: candidate.sourceType,
            id: candidate.sourceId,
            reason: 'duplicate_existing',
            contentHash: candidate.hash
          })
          continue
        }
        accepted.push(candidate)
      }
      for (const candidate of accepted) {
        const id = this.nextId()
        const provenance = {
          provider_id: response.providerId,
          model_id: response.modelId,
          prompt_version: PROMPT_VERSION,
          skill: SKILL_NAME,
          sources: provenanceSources,
          rationale: candidate.rationale,
          generated_at: generatedAtText,
          truncated: anyTruncated
        }
        handle.db
          .prepare(
            `INSERT INTO evidence(id, idea_id, direction, content, source_type, source_id,
               ai_generated, confirmed, ai_output_id, provenance, content_hash, created_at, updated_at, deleted_at)
             VALUES (?, ?, ?, ?, ?, ?, 1, 0, NULL, ?, ?, ?, ?, NULL)`
          )
          .run(
            id,
            input.ideaId,
            candidate.direction,
            candidate.content,
            candidate.sourceType,
            candidate.sourceId,
            JSON.stringify(provenance),
            candidate.hash,
            generatedAtText,
            generatedAtText
          )
        draftIds.push(id)
      }
      this.emit(handle, {
        type: 'ai.analysis_completed',
        entityType: 'idea',
        entityId: input.ideaId,
        summary: `Evidence 规范化：生成 ${draftIds.length} 条草稿（来源 ${sources.length} 项，跳过 ${skippedDrafts.length} 条）`,
        actor: 'ai',
        at: generatedAt
      })
    })

    return {
      runId,
      draftIds,
      skipped: skippedDrafts,
      modelInfo: { provider: response.providerId, model: response.modelId }
    }
  }

  /** Enqueues the extraction job; the manager stores typed failures. */
  async enqueueExtraction(projectId: string, input: ExtractDraftsInput): Promise<{ jobId: string }> {
    if (this.jobs === undefined) {
      throw ideaError(
        IDEA_ERROR.jobUnavailable,
        AppErrorCode.CapabilityInvalid,
        'the job manager is not configured'
      )
    }
    const handle = this.begin(projectId)
    requireIdea(handle.db, input.ideaId)
    const started = await this.jobs.enqueue({
      projectId,
      kind: 'ideas.evidence_extract',
      params: {
        idea_id: input.ideaId,
        source_refs: input.sourceRefs,
        instruction: input.instruction ?? '',
        max_drafts: input.maxDrafts ?? IDEA_LIMITS.defaultMaxDrafts
      },
      run: async (context) => {
        context.progress(10, 'resolve-sources')
        const result = await this.extractDrafts(projectId, {
          ...input,
          actor: 'mcp',
          signal: context.signal
        })
        context.progress(100, 'persist-drafts')
        const jobResult: ExtractionJobResult = {
          runId: result.runId,
          draftIds: result.draftIds,
          skipped: result.skipped,
          modelInfo: result.modelInfo
        }
        return jobResult
      }
    })
    return { jobId: started.jobId }
  }

  async extractionPreview(jobId: string): Promise<EvidenceExtractionPreview> {
    if (this.jobs === undefined) {
      throw ideaError(
        IDEA_ERROR.jobUnavailable,
        AppErrorCode.CapabilityInvalid,
        'the job manager is not configured'
      )
    }
    const status = await this.jobs.get(jobId)
    if (status === undefined) {
      throw ideaError(IDEA_ERROR.jobNotFound, AppErrorCode.NotFound, `job ${jobId} not found`, { job_id: jobId })
    }
    const result = status.result
    const drafts: EvidenceView[] = []
    let skipped: EvidenceSkippedDraft[] = []
    if (result !== undefined) {
      const handle = this.handle(status.projectId)
      for (const draftId of result.draftIds) {
        const row = readEvidence(handle.db, draftId)
        if (row !== undefined && row.deletedAt === null) drafts.push(this.evidenceView(status.projectId, handle, row))
      }
      skipped = result.skipped
    }
    return {
      jobId,
      state: status.state,
      drafts,
      skipped,
      modelInfo: result?.modelInfo ?? null,
      warnings: status.state === 'queued' || status.state === 'running' ? ['extraction is still running'] : [],
      ...(status.error === undefined ? {} : { error: status.error })
    }
  }

  /* ------------------------------------------------------- confirmations */

  /** Executor entry point used by the desktop confirmation UI (actor=user). */
  async executeEvidenceConfirm(
    projectId: string,
    ideaId: string,
    evidenceIds: readonly string[]
  ): Promise<JsonObject> {
    const result = this.confirmEvidence(projectId, ideaId, evidenceIds, 'user')
    return { confirmed: result.confirmed, failed: result.failed }
  }
}

/* ------------------------------------------------------------------ helpers */

interface RelationTriple {
  srcType: string
  srcId: string
  relation: string
  dstType: string
  dstId: string
}

function relationTriple(
  ideaId: string,
  target: { type: 'paper' | 'note' | 'experiment'; id: string }
): RelationTriple {
  switch (target.type) {
    case 'paper':
      return { srcType: 'idea', srcId: ideaId, relation: 'related_to', dstType: 'paper', dstId: target.id }
    case 'note':
      return { srcType: 'idea', srcId: ideaId, relation: 'related_to', dstType: 'note', dstId: target.id }
    case 'experiment':
      return { srcType: 'experiment', srcId: target.id, relation: 'validates', dstType: 'idea', dstId: ideaId }
    default:
      throw invalidInput(`target type ${JSON.stringify(target.type)} must be paper, note or experiment`)
  }
}

function staleConflict(message: string, details: Record<string, unknown>): never {
  throw ideaError(IDEA_ERROR.stale, AppErrorCode.Conflict, message, details)
}

function reasonOf(error: unknown): string {
  if (isAppError(error)) {
    const reason = error.details?.['reason']
    if (typeof reason === 'string') return reason
  }
  return ''
}

function statusValue(value: unknown): string {
  if (typeof value !== 'object' || value === null || !('status' in value)) return ''
  return typeof value.status === 'string' ? value.status : ''
}

function classifyEvent(eventType: string, entityType: string): IdeaHistoryEntry['kind'] {
  if (entityType === 'evidence') return 'evidence'
  switch (eventType) {
    case 'entity.created':
      return 'created'
    case 'status.changed':
      return 'status'
    case 'relation.added':
    case 'relation.removed':
      return 'relation'
    case 'entity.deleted':
      return 'deleted'
    case 'entity.restored':
      return 'restored'
    case 'ai.analysis_completed':
      return 'ai'
    default:
      return 'created'
  }
}

export type { IdeasWorkspace, IdeasServiceOptions }