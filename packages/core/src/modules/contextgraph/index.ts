import type {
  ContextBacklink,
  ContextEntityKind,
  ContextEvidenceItem,
  ContextExperimentItem,
  ContextGraph,
  ContextGraphEdge,
  ContextGraphNode,
  ContextIdeaItem,
  ContextPaperItem,
  ContextTaskItem,
  EntityContext,
  ProvenanceEntityRef
} from '@researchos/shared'
import { CONTEXT_ENTITY_KINDS } from '@researchos/shared'
import { AppErrorCode } from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import { appError } from '../../errors'
import type { Db } from '../../db/open'
import type { Paths } from '../../paths'
import type { WorkspaceManager } from '../../workspace'
import { projectNotFound } from '../project/errors'
import { readNoteBody } from '../project/note-body'
import type { NoteCreatorPort, TaskCreatorPort } from '../project/ports'
import { parsePaperMetadata } from '../project/service'
import { metricDigest, openingText, rosIds, shortId, snippetAround, truncate } from '../project/text'

/**
 * Read-only context aggregation behind the Inspector: the incoming
 * references of one entity, the ideas/evidence/experiments/papers/tasks around
 * it, and the depth-1..2 ego graph with the 24-node cap.
 *
 * No table is added and nothing is stored. The two write helpers the inspector's
 * buttons need create a note or task through the owning module's port, then
 * register the relation that expresses the link.
 */

const BACKLINK_LIMIT = 20
const IDEA_LIMIT = 20
const EVIDENCE_LIMIT = 12
const EXPERIMENT_CAP = 12
const PAPER_LIMIT = 12
const TASK_LIMIT = 12
const SNIPPET_RUNES = 160
const GRAPH_MAX_NODES = 24
const GRAPH_MAX_DEPTH = 2

const TABLES: Readonly<Record<string, string>> = {
  note: 'notes',
  idea: 'ideas',
  experiment: 'experiments',
  task: 'tasks',
  db_record: 'db_records',
  evidence: 'evidence'
}

export interface RelationWriterPort {
  link(input: {
    projectId: string
    srcType: string
    srcId: string
    relation: 'references' | 'related_to' | 'deliverable_of'
    dstType: string
    dstId: string
    actor: 'user'
  }): void
}

export interface TaskBoardPort {
  listColumns(projectId: string): Array<{ name: string; kind: string; isDone: boolean }>
}

export interface ContextGraphOptions {
  workspace: WorkspaceManager
  clock?: Clock
  notes?: NoteCreatorPort
  tasks?: TaskCreatorPort
  board?: TaskBoardPort
  relations?: RelationWriterPort
}

export function isSupportedContextKind(value: string): value is ContextEntityKind {
  return (CONTEXT_ENTITY_KINDS as readonly string[]).includes(value)
}

interface NoteRow {
  id: string
  title: string
  kind: string
  paperId: string
  body: string
  updatedAt: string
}

interface RelationRow {
  srcType: string
  srcId: string
  relation: string
  dstType: string
  dstId: string
}

export class ContextGraphService {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock
  private readonly paths: Paths
  private readonly notes: NoteCreatorPort | undefined
  private readonly tasks: TaskCreatorPort | undefined
  private readonly board: TaskBoardPort | undefined
  private readonly relations: RelationWriterPort | undefined

  constructor(options: ContextGraphOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? options.workspace.clock
    this.paths = options.workspace.paths
    this.notes = options.notes
    this.tasks = options.tasks
    this.board = options.board
    this.relations = options.relations
  }

  /** Everything the inspector shows about one entity. */
  load(projectId: string, target: ProvenanceEntityRef): EntityContext {
    const db = this.projectDb(projectId)
    this.assertTarget(db, target)
    const title = this.title(db, target)
    const notes = this.liveNotes(db)
    const relations = this.relationsTouching(db, target)

    const ideas = this.linkedIdeas(db, target, notes, relations)
    return {
      target,
      title,
      backlinks: this.backlinks(notes, relations, target),
      ideas,
      experiments: this.linkedExperiments(db, target, ideas, notes, relations),
      evidences: this.linkedEvidence(db, target, ideas),
      papers: this.linkedPapers(db, target, ideas, notes, relations),
      tasks: this.linkedTasks(db, target, relations),
      loadedAt: formatTimestamp(this.clock.now())
    }
  }

  /** The ego graph of the center entity out to 1–2 hops, capped at 24 nodes. */
  graph(projectId: string, center: ProvenanceEntityRef, depth = 1): ContextGraph {
    const db = this.projectDb(projectId)
    this.assertTarget(db, center)
    const hops = depth <= 0 ? 1 : Math.min(Math.floor(depth), GRAPH_MAX_DEPTH)

    const nodes = new Map<string, ContextGraphNode>()
    const edges: ContextGraphEdge[] = []
    const edgeKeys = new Set<string>()
    const key = (ref: ProvenanceEntityRef): string => `${ref.type}:${ref.id}`
    nodes.set(key(center), { type: center.type, id: center.id, title: '', kind: center.type, center: true, depth: 0 })
    let frontier: ProvenanceEntityRef[] = [center]
    let truncated = false

    for (let hop = 1; hop <= hops; hop += 1) {
      if (frontier.length === 0) break
      if (nodes.size >= GRAPH_MAX_NODES) {
        truncated = true
        break
      }
      const relations = this.relationsTouchingMany(db, frontier)
      const next: ProvenanceEntityRef[] = []
      for (const relation of relations) {
        const from: ProvenanceEntityRef = { type: relation.srcType, id: relation.srcId }
        const to: ProvenanceEntityRef = { type: relation.dstType, id: relation.dstId }
        if (!isSupportedContextKind(from.type) || !isSupportedContextKind(to.type)) continue
        if (from.id === to.id) continue
        for (const ref of [from, to]) {
          if (nodes.has(key(ref))) continue
          if (nodes.size >= GRAPH_MAX_NODES) {
            truncated = true
            continue
          }
          nodes.set(key(ref), { type: ref.type, id: ref.id, title: '', kind: ref.type, center: false, depth: hop })
          next.push(ref)
        }
        const edgeKey = `${key(from)}|${relation.relation}|${key(to)}`
        if (edgeKeys.has(edgeKey)) continue
        edgeKeys.add(edgeKey)
        edges.push({ from, to, relation: relation.relation })
      }
      frontier = next
    }

    const titles = this.titles(db, [...nodes.values()].map((node) => ({ type: node.type, id: node.id })))
    const result = [...nodes.values()].map((node) => ({
      ...node,
      title: titles.get(key(node)) ?? shortId(node.id)
    }))
    result.sort((left, right) => {
      if (left.depth !== right.depth) return left.depth - right.depth
      if (left.type !== right.type) return left.type < right.type ? -1 : 1
      return left.id < right.id ? -1 : 1
    })
    edges.sort((left, right) => {
      const from = key(left.from).localeCompare(key(right.from))
      if (from !== 0) return from
      if (left.relation !== right.relation) return left.relation.localeCompare(right.relation)
      return key(left.to).localeCompare(key(right.to))
    })
    return { center, nodes: result, edges, truncated }
  }

  /* ----------------------------------------------------------- write helpers */

  /**
   * Creates a scratchpad note whose body links the inspected entity and
   * registers the relation the link expresses (`references` for note/paper,
   * `related_to` otherwise).
   */
  createLinkedNote(projectId: string, target: ProvenanceEntityRef, title: string): ProvenanceEntityRef {
    if (this.notes === undefined) throw unavailable('notes')
    if (this.relations === undefined) throw unavailable('relations')
    this.assertTarget(this.projectDb(projectId), target)
    const targetTitle = this.title(this.projectDb(projectId), target)
    const created = this.notes.createScratchpad({
      projectId,
      title,
      bodyMd: `[${targetTitle}](ros://${target.type}/${target.id})\n`,
      actor: 'user'
    })
    this.relations.link({
      projectId,
      srcType: 'note',
      srcId: created.id,
      relation: target.type === 'note' || target.type === 'paper' ? 'references' : 'related_to',
      dstType: target.type,
      dstId: target.id,
      actor: 'user'
    })
    return { type: 'note', id: created.id }
  }

  /**
   * Creates a task in the board's first open column and links it to the
   * inspected entity (`deliverable_of` for a note/idea/experiment/run).
   */
  createDerivedTask(projectId: string, target: ProvenanceEntityRef, title: string): ProvenanceEntityRef {
    if (this.tasks === undefined) throw unavailable('kanban')
    if (this.relations === undefined) throw unavailable('relations')
    const column = this.board?.listColumns(projectId).find((candidate) => candidate.kind === 'todo' && !candidate.isDone)
    const created = this.tasks.createTask({
      projectId,
      title,
      ...(column === undefined ? {} : { column: column.name })
    })
    const relation =
      target.type === 'note' || target.type === 'idea' || target.type === 'experiment' || target.type === 'run'
        ? 'deliverable_of'
        : 'related_to'
    this.relations.link({
      projectId,
      srcType: 'task',
      srcId: created.id,
      relation,
      dstType: target.type,
      dstId: target.id,
      actor: 'user'
    })
    return { type: 'task', id: created.id }
  }

  /* ---------------------------------------------------------------- reads */

  private projectDb(projectId: string): Db {
    const row = this.workspace.global.db.prepare(`SELECT id FROM projects WHERE id = ?`).get(projectId)
    if (row === undefined) throw projectNotFound(projectId)
    return this.workspace.project(projectId).db
  }

  private assertTarget(db: Db, target: ProvenanceEntityRef): void {
    if (!isSupportedContextKind(target.type)) {
      throw appError(AppErrorCode.InvalidArgument, `不支持的实体类型：${target.type}`, {
        details: { field: 'entityType' }
      })
    }
    if (!/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(target.id)) {
      throw appError(AppErrorCode.InvalidArgument, `实体 ID 不是 UUIDv7：${target.id}`, {
        details: { field: 'entityId' }
      })
    }
    if (target.type === 'paper') {
      const exists = this.workspace.global.db.prepare(`SELECT 1 AS x FROM papers WHERE id = ?`).get(target.id)
      if (exists === undefined) {
        throw appError(AppErrorCode.NotFound, `文献不存在：${target.id}`, { details: { paperId: target.id } })
      }
      return
    }
    const table = TABLES[target.type]!
    const exists = db.prepare(`SELECT 1 AS x FROM ${table} WHERE id = ?`).get(target.id)
    if (exists === undefined) {
      throw appError(AppErrorCode.NotFound, `实体不存在：${target.type} ${target.id}`, {
        details: { entityType: target.type, entityId: target.id }
      })
    }
  }

  private title(db: Db, target: ProvenanceEntityRef): string {
    switch (target.type) {
      case 'note':
      case 'idea':
      case 'experiment':
      case 'task':
      case 'db_record': {
        const row = db.prepare(`SELECT title FROM ${TABLES[target.type]!} WHERE id = ?`).get(target.id) as
          | { title: string }
          | undefined
        return row?.title ?? shortId(target.id)
      }
      case 'evidence': {
        const row = db.prepare(`SELECT content FROM evidence WHERE id = ?`).get(target.id) as
          | { content: string }
          | undefined
        return truncate(row?.content ?? shortId(target.id), SNIPPET_RUNES)
      }
      case 'run': {
        const row = db
          .prepare(
            `SELECT r.id AS run_id, e.title AS experiment FROM runs r
               JOIN experiments e ON e.id = r.experiment_id WHERE r.id = ?`
          )
          .get(target.id) as { run_id: string; experiment: string } | undefined
        return row === undefined ? shortId(target.id) : `${row.experiment} · ${shortId(target.id)}`
      }
      case 'paper': {
        const row = this.workspace.global.db.prepare(`SELECT metadata FROM papers WHERE id = ?`).get(target.id) as
          | { metadata: string }
          | undefined
        const title = row === undefined ? '' : parsePaperMetadata(row.metadata).title
        return title === '' ? shortId(target.id) : title
      }
      default:
        return shortId(target.id)
    }
  }

  private titles(db: Db, refs: ProvenanceEntityRef[]): Map<string, string> {
    const out = new Map<string, string>()
    const byType = new Map<string, string[]>()
    for (const ref of refs) byType.set(ref.type, [...(byType.get(ref.type) ?? []), ref.id])
    for (const [type, ids] of byType) {
      const placeholders = ids.map(() => '?').join(',')
      if (type === 'paper') {
        const rows = this.workspace.global.db
          .prepare(`SELECT id, metadata FROM papers WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; metadata: string }>
        for (const row of rows) {
          const title = parsePaperMetadata(row.metadata).title
          out.set(`paper:${row.id}`, title === '' ? shortId(row.id) : title)
        }
        continue
      }
      const table = TABLES[type]
      if (table === undefined) continue
      if (type === 'evidence') {
        const rows = db
          .prepare(`SELECT id, content FROM evidence WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; content: string }>
        for (const row of rows) out.set(`evidence:${row.id}`, truncate(row.content, SNIPPET_RUNES))
        continue
      }
      if (type === 'run') {
        const rows = db
          .prepare(
            `SELECT r.id, e.title FROM runs r JOIN experiments e ON e.id = r.experiment_id
              WHERE r.id IN (${placeholders})`
          )
          .all(...ids) as Array<{ id: string; title: string }>
        for (const row of rows) out.set(`run:${row.id}`, `${row.title} · ${shortId(row.id)}`)
        continue
      }
      const rows = db.prepare(`SELECT id, title FROM ${table} WHERE id IN (${placeholders})`).all(...ids) as Array<{
        id: string
        title: string
      }>
      for (const row of rows) out.set(`${type}:${row.id}`, row.title)
    }
    return out
  }

  private liveNotes(db: Db): NoteRow[] {
    return (
      db
        .prepare(
          `SELECT id, title, kind, coalesce(paper_id, '') AS paper_id, file_path, updated_at
             FROM notes WHERE deleted_at IS NULL ORDER BY updated_at DESC, id`
        )
        .all() as Array<{
        id: string
        title: string
        kind: string
        paper_id: string
        file_path: string
        updated_at: string
      }>
    ).map((row) => ({
      id: row.id,
      title: row.title,
      kind: row.kind,
      paperId: row.paper_id,
      body: readNoteBody(this.paths, row.file_path),
      updatedAt: row.updated_at
    }))
  }

  private relationsTouching(db: Db, target: ProvenanceEntityRef): RelationRow[] {
    return this.relationsTouchingMany(db, [target])
  }

  private relationsTouchingMany(db: Db, refs: ProvenanceEntityRef[]): RelationRow[] {
    if (refs.length === 0) return []
    const where: string[] = []
    const params: unknown[] = []
    for (const ref of refs) {
      where.push('(src_type = ? AND src_id = ?)', '(dst_type = ? AND dst_id = ?)')
      params.push(ref.type, ref.id, ref.type, ref.id)
    }
    return (
      db
        .prepare(
          `SELECT src_type, src_id, relation, dst_type, dst_id FROM entity_relations
            WHERE ${where.join(' OR ')}`
        )
        .all(...params) as Array<{
        src_type: string
        src_id: string
        relation: string
        dst_type: string
        dst_id: string
      }>
    ).map((row) => ({
      srcType: row.src_type,
      srcId: row.src_id,
      relation: row.relation,
      dstType: row.dst_type,
      dstId: row.dst_id
    }))
  }

  private other(relation: RelationRow, target: ProvenanceEntityRef): ProvenanceEntityRef {
    if (relation.srcType === target.type && relation.srcId === target.id) {
      return { type: relation.dstType, id: relation.dstId }
    }
    return { type: relation.srcType, id: relation.srcId }
  }

  /**
   * Notes that mention the target: registered note→target relations first, then
   * `ros://` links in the Markdown body. A paper also lists its paper notes.
   */
  private backlinks(
    notes: NoteRow[],
    relations: RelationRow[],
    target: ProvenanceEntityRef
  ): ContextBacklink[] {
    const relationNotes = new Set<string>()
    for (const relation of relations) {
      const other = this.other(relation, target)
      if (other.type !== 'note') continue
      if (target.type === 'note' && other.id === target.id) continue
      relationNotes.add(other.id)
    }
    const token = `ros://${target.type}/${target.id}`
    const items: ContextBacklink[] = []
    for (const note of notes) {
      if (target.type === 'note' && note.id === target.id) continue
      const index = note.body.indexOf(token)
      const viaBody = index >= 0
      const viaRelation = relationNotes.has(note.id) || (target.type === 'paper' && note.paperId === target.id)
      const mentionsNote = target.type === 'note' && note.body.includes(`ros://note/${target.id}`)
      if (!viaBody && !viaRelation && !mentionsNote) continue
      items.push({
        noteId: note.id,
        noteTitle: note.title,
        noteKind: note.kind as ContextBacklink['noteKind'],
        snippet: viaBody ? snippetAround(note.body, index, SNIPPET_RUNES) : openingText(note.body, SNIPPET_RUNES),
        viaRelation,
        updatedAt: note.updatedAt
      })
    }
    items.sort((left, right) => {
      if (left.updatedAt !== right.updatedAt) return left.updatedAt > right.updatedAt ? -1 : 1
      return left.noteId < right.noteId ? -1 : 1
    })
    return items.slice(0, BACKLINK_LIMIT)
  }

  private linkedIdeas(
    db: Db,
    target: ProvenanceEntityRef,
    notes: NoteRow[],
    relations: RelationRow[]
  ): ContextIdeaItem[] {
    const ids = new Set<string>()
    if (target.type === 'idea') ids.add(target.id)
    for (const relation of relations) {
      const other = this.other(relation, target)
      if (other.type === 'idea') ids.add(other.id)
    }
    const targetNote = target.type === 'note' ? notes.find((note) => note.id === target.id) : undefined
    if (targetNote !== undefined) {
      for (const id of rosIds(targetNote.body, 'idea')) ids.add(id)
    }
    if (target.type === 'experiment' || target.type === 'run') {
      const experimentId = target.type === 'experiment' ? target.id : this.runExperiment(db, target.id)
      for (const relation of this.experimentIdeas(db, experimentId)) ids.add(relation)
    }
    if (target.type === 'paper') {
      const rows = db
        .prepare(
          `SELECT DISTINCT idea_id FROM evidence
            WHERE deleted_at IS NULL AND source_type = 'paper' AND source_id = ?`
        )
        .all(target.id) as Array<{ idea_id: string }>
      for (const row of rows) ids.add(row.idea_id)
    }
    if (ids.size === 0) return []

    const parents = this.ideaDerivations(db, [...ids])
    const list = [...ids]
    const rows = db
      .prepare(
        `SELECT id, title, status, rejection_reason, updated_at FROM ideas
          WHERE deleted_at IS NULL AND id IN (${list.map(() => '?').join(',')})`
      )
      .all(...list) as Array<{
      id: string
      title: string
      status: string
      rejection_reason: string
      updated_at: string
    }>
    const items = rows.map<ContextIdeaItem>((row) => ({
      id: row.id,
      title: row.title,
      status: row.status,
      rejectionReason: row.rejection_reason,
      derivedFrom: parents.get(row.id) ?? null,
      self: target.type === 'idea' && row.id === target.id,
      updatedAt: row.updated_at
    }))
    items.sort((left, right) => {
      if (left.self !== right.self) return left.self ? -1 : 1
      if (left.updatedAt !== right.updatedAt) return left.updatedAt > right.updatedAt ? -1 : 1
      return left.id < right.id ? -1 : 1
    })
    return items.slice(0, IDEA_LIMIT)
  }

  private runExperiment(db: Db, runId: string): string | null {
    const row = db.prepare(`SELECT experiment_id FROM runs WHERE id = ?`).get(runId) as
      | { experiment_id: string }
      | undefined
    return row?.experiment_id ?? null
  }

  private experimentIdeas(db: Db, experimentId: string | null): string[] {
    if (experimentId === null) return []
    return (
      db
        .prepare(
          `SELECT dst_id FROM entity_relations
            WHERE relation = 'validates' AND src_type = 'experiment' AND src_id = ? AND dst_type = 'idea'`
        )
        .all(experimentId) as Array<{ dst_id: string }>
    ).map((row) => row.dst_id)
  }

  private ideaDerivations(db: Db, ids: string[]): Map<string, string> {
    const out = new Map<string, string>()
    if (ids.length === 0) return out
    const placeholders = ids.map(() => '?').join(',')
    const rows = db
      .prepare(
        `SELECT src_id, dst_id FROM entity_relations
          WHERE relation = 'derived_from' AND src_type = 'idea' AND dst_type = 'idea'
            AND (src_id IN (${placeholders}) OR dst_id IN (${placeholders}))`
      )
      .all(...ids, ...ids) as Array<{ src_id: string; dst_id: string }>
    for (const row of rows) if (!out.has(row.src_id)) out.set(row.src_id, row.dst_id)
    return out
  }

  private linkedEvidence(db: Db, target: ProvenanceEntityRef, ideas: ContextIdeaItem[]): ContextEvidenceItem[] {
    const where: string[] = []
    const params: unknown[] = []
    if (ideas.length > 0) {
      where.push(`idea_id IN (${ideas.map(() => '?').join(',')})`)
      params.push(...ideas.map((idea) => idea.id))
    }
    where.push('(source_type = ? AND source_id = ?)')
    params.push(target.type, target.id)
    const rows = db
      .prepare(
        `SELECT id, idea_id, direction, content, confirmed, source_type, source_id, created_at
           FROM evidence WHERE deleted_at IS NULL AND (${where.join(' OR ')})
          ORDER BY created_at DESC, id DESC LIMIT ?`
      )
      .all(...params, EVIDENCE_LIMIT) as Array<{
      id: string
      idea_id: string
      direction: string
      content: string
      confirmed: number
      source_type: string
      source_id: string
      created_at: string
    }>
    return rows.map((row) => ({
      id: row.id,
      ideaId: row.idea_id,
      direction: row.direction === 'oppose' ? 'oppose' : 'support',
      content: row.content,
      confirmed: row.confirmed === 1,
      source: { type: row.source_type, id: row.source_id },
      createdAt: row.created_at
    }))
  }

  private linkedExperiments(
    db: Db,
    target: ProvenanceEntityRef,
    ideas: ContextIdeaItem[],
    notes: NoteRow[],
    relations: RelationRow[]
  ): ContextExperimentItem[] {
    const ids = new Set<string>()
    if (target.type === 'experiment') ids.add(target.id)
    const targetNote = target.type === 'note' ? notes.find((note) => note.id === target.id) : undefined
    if (targetNote !== undefined) for (const id of rosIds(targetNote.body, 'experiment')) ids.add(id)
    if (target.type === 'run') {
      const experimentId = this.runExperiment(db, target.id)
      if (experimentId !== null) ids.add(experimentId)
    }
    for (const relation of relations) {
      const other = this.other(relation, target)
      if (other.type === 'experiment') ids.add(other.id)
    }
    if (ideas.length > 0) {
      const rows = db
        .prepare(
          `SELECT src_id FROM entity_relations
            WHERE relation = 'validates' AND src_type = 'experiment' AND dst_type = 'idea'
              AND dst_id IN (${ideas.map(() => '?').join(',')})`
        )
        .all(...ideas.map((idea) => idea.id)) as Array<{ src_id: string }>
      for (const row of rows) ids.add(row.src_id)
    }
    const items: ContextExperimentItem[] = []
    for (const id of ids) {
      const item = this.experimentItem(db, id)
      if (item === null) continue
      item.self = target.type === 'experiment' && id === target.id
      items.push(item)
    }
    items.sort((left, right) => {
      if (left.self !== right.self) return left.self ? -1 : 1
      if (left.updatedAt !== right.updatedAt) return left.updatedAt > right.updatedAt ? -1 : 1
      return left.id < right.id ? -1 : 1
    })
    return items.slice(0, EXPERIMENT_CAP)
  }

  private experimentItem(db: Db, experimentId: string): ContextExperimentItem | null {
    const row = db
      .prepare(`SELECT id, title, updated_at FROM experiments WHERE id = ? AND deleted_at IS NULL`)
      .get(experimentId) as { id: string; title: string; updated_at: string } | undefined
    if (row === undefined) return null
    const runs = db
      .prepare(`SELECT count(*) AS n FROM runs WHERE experiment_id = ? AND deleted_at IS NULL`)
      .get(experimentId) as { n: number }
    const latest = db
      .prepare(
        `SELECT id, status FROM runs WHERE experiment_id = ? AND deleted_at IS NULL
          ORDER BY updated_at DESC, id DESC LIMIT 1`
      )
      .get(experimentId) as { id: string; status: string } | undefined
    const snapshot = db
      .prepare(
        `SELECT s.metrics FROM snapshots s
          WHERE (s.target_type = 'experiment' AND s.target_id = ?)
             OR (s.target_type = 'run' AND s.target_id IN
                   (SELECT r.id FROM runs r WHERE r.experiment_id = ? AND r.deleted_at IS NULL))
          ORDER BY s.created_at DESC, s.id DESC LIMIT 1`
      )
      .get(experimentId, experimentId) as { metrics: string } | undefined
    return {
      id: row.id,
      title: row.title,
      runCount: Number(runs.n),
      latestRunId: latest?.id ?? null,
      latestStatus: latest?.status ?? '',
      metric: snapshot === undefined ? '' : metricDigest(snapshot.metrics),
      self: false,
      updatedAt: row.updated_at
    }
  }

  private linkedPapers(
    db: Db,
    target: ProvenanceEntityRef,
    ideas: ContextIdeaItem[],
    notes: NoteRow[],
    relations: RelationRow[]
  ): ContextPaperItem[] {
    const ids = new Set<string>()
    if (target.type === 'paper') ids.add(target.id)
    const targetNote = target.type === 'note' ? notes.find((note) => note.id === target.id) : undefined
    if (targetNote !== undefined) for (const id of rosIds(targetNote.body, 'paper')) ids.add(id)
    for (const relation of relations) {
      const other = this.other(relation, target)
      if (other.type === 'paper') ids.add(other.id)
    }
    const token = `ros://${target.type}/${target.id}`
    for (const note of notes) {
      if (note.paperId === '') continue
      if (note.body.includes(token) || (target.type === 'note' && note.id === target.id)) ids.add(note.paperId)
    }
    if (ideas.length > 0) {
      const rows = db
        .prepare(
          `SELECT DISTINCT source_id FROM evidence
            WHERE deleted_at IS NULL AND source_type = 'paper'
              AND idea_id IN (${ideas.map(() => '?').join(',')})`
        )
        .all(...ideas.map((idea) => idea.id)) as Array<{ source_id: string }>
      for (const row of rows) ids.add(row.source_id)
    }
    const items: ContextPaperItem[] = []
    for (const id of ids) {
      const row = this.workspace.global.db
        .prepare(`SELECT metadata, file_path FROM papers WHERE id = ?`)
        .get(id) as { metadata: string; file_path: string | null } | undefined
      if (row === undefined) continue
      const metadata = parsePaperMetadata(row.metadata)
      const year = metadata.year === 0 ? '' : String(metadata.year)
      items.push({
        id,
        title: metadata.title === '' ? shortId(id) : metadata.title,
        year,
        hasCopy: row.file_path !== null && row.file_path !== ''
      })
    }
    items.sort((left, right) => {
      if (left.year !== right.year) return left.year > right.year ? -1 : 1
      return left.title < right.title ? -1 : 1
    })
    return items.slice(0, PAPER_LIMIT)
  }

  private linkedTasks(db: Db, target: ProvenanceEntityRef, relations: RelationRow[]): ContextTaskItem[] {
    const found = new Map<string, string>()
    for (const relation of relations) {
      const other = this.other(relation, target)
      if (other.type === 'task') found.set(other.id, relation.relation)
    }
    if (target.type === 'task') found.set(target.id, '')
    if (found.size === 0) return []
    const ids = [...found.keys()]
    const rows = db
      .prepare(
        `SELECT t.id, t.title, t."column" AS column_name, t.blocked, t.updated_at,
                coalesce(c.is_done, 0) AS column_done
           FROM tasks t LEFT JOIN task_columns c ON c.name = t."column"
          WHERE t.deleted_at IS NULL AND t.id IN (${ids.map(() => '?').join(',')})`
      )
      .all(...ids) as Array<{
      id: string
      title: string
      column_name: string
      blocked: number
      updated_at: string
      column_done: number
    }>
    const items = rows.map<ContextTaskItem>((row) => ({
      id: row.id,
      title: row.title,
      column: row.column_name,
      blocked: row.blocked !== 0,
      done: row.column_done === 1 || ['done', '完成'].includes(row.column_name.trim().toLowerCase()),
      relation: found.get(row.id) ?? '',
      updatedAt: row.updated_at
    }))
    items.sort((left, right) => {
      if (left.done !== right.done) return left.done ? 1 : -1
      if (left.updatedAt !== right.updatedAt) return left.updatedAt > right.updatedAt ? -1 : 1
      return left.id < right.id ? -1 : 1
    })
    return items.slice(0, TASK_LIMIT)
  }
}

function unavailable(feature: string) {
  return appError(AppErrorCode.Unsupported, `${feature} 模块未就绪`, { details: { feature } })
}
