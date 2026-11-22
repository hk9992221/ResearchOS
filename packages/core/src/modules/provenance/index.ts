import type {
  ProvenanceEntityRef,
  ProvenanceFilter,
  ProvenanceLineageNode,
  ProvenanceLineageRef,
  ProvenanceLineageTree,
  ProvenanceMilestoneKind,
  ProvenanceNode,
  ProvenanceReport,
  ProvenanceSection
} from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { Paths } from '../../paths'
import type { WorkspaceManager } from '../../workspace'
import { projectNotFound } from '../project/errors'
import { readNoteBody } from '../project/note-body'
import { parsePaperMetadata } from '../project/service'
import { collapse, metricDigest, truncate } from '../project/text'
import { dedupeRefs as dedupe, rosLinks } from '../project/text'

/**
 * Read-only provenance aggregation: the milestone story line of a
 * project and the `derived_from` forest of its ideas.
 *
 * It adds no table. Activities, ideas, evidence, experiments with their newest
 * snapshots, entity_relations, notes, the project's papers and confirmed weekly
 * reports are woven into one chronological view. Markdown export is a pure
 * rendering of the same report.
 */

const ACTIVITY_PAGE = 500
const SNIPPET_RUNES = 200
const NOTE_SUMMARY_RUNES = 160

interface IdeaRow {
  id: string
  title: string
  description: string
  status: string
  rejectionReason: string
  createdAt: string
  updatedAt: string
}

interface EvidenceRow {
  id: string
  ideaId: string
  direction: string
  content: string
  sourceType: string
  sourceId: string
  confirmed: boolean
  createdAt: string
}

interface SnapshotRow {
  id: string
  targetType: string
  targetId: string
  metrics: string
  gitInfo: string
  createdAt: string
}

interface ExperimentRow {
  id: string
  title: string
  createdAt: string
  updatedAt: string
  runId: string | null
  runStatus: string
  snapshot: SnapshotRow | null
}

interface NoteRow {
  id: string
  title: string
  paperId: string
  body: string
  createdAt: string
  updatedAt: string
}

interface PaperRow {
  id: string
  title: string
  importedAt: string
}

interface RelationRow {
  srcType: string
  srcId: string
  relation: string
  dstType: string
  dstId: string
  at: string
}

interface WeeklyRow {
  id: string
  start: string
  end: string
  at: string
}

interface ActivityRow {
  id: string
  eventType: string
  entityType: string
  entityId: string
  payload: { summary?: unknown; new?: unknown }
  createdAt: string
}

interface SourceData {
  ideas: Map<string, IdeaRow>
  ideaOrder: string[]
  derivedFrom: Map<string, string>
  derivedAt: Map<string, string>
  relatedTo: RelationRow[]
  validates: RelationRow[]
  evidence: EvidenceRow[]
  experiments: ExperimentRow[]
  notes: NoteRow[]
  papers: Map<string, PaperRow>
  activities: ActivityRow[]
  weekly: WeeklyRow[]
}

export interface ProvenanceServiceOptions {
  workspace: WorkspaceManager
  clock?: Clock
}

export class ProvenanceService {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock
  private readonly paths: Paths

  constructor(options: ProvenanceServiceOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? options.workspace.clock
    this.paths = options.workspace.paths
  }

  /** Builds the timeline of one project. An empty project is not an error. */
  load(filter: ProvenanceFilter): ProvenanceReport {
    const projectId = filter.projectId
    const global = this.workspace.global.db
    const nameRow = global.prepare(`SELECT name FROM projects WHERE id = ?`).get(projectId) as
      | { name: string }
      | undefined
    if (nameRow === undefined) throw projectNotFound(projectId)
    const db = this.workspace.project(projectId).db

    const data = this.read(db, global, projectId, filter)
    let nodes = this.nodes(data, filter)
    let lineage = buildLineage(data)
    let focused = lineage
    if (filter.focusIdeaId !== undefined && filter.focusIdeaId !== '') {
      const family = ideaFamily(filter.focusIdeaId, data)
      focused = filterLineage(lineage, family)
      nodes = filterNodes(nodes, family, data)
    }
    const granularity = filter.granularity ?? 'week'
    const report: ProvenanceReport = {
      projectId,
      projectName: nameRow.name,
      from: filter.from ?? null,
      to: filter.to ?? null,
      focusIdeaId: filter.focusIdeaId ?? null,
      milestones: filter.milestones === true,
      nodes,
      sections: groupSections(nodes, granularity === 'month'),
      lineage,
      focusedLineage: focused,
      markdown: null,
      nodeCount: nodes.length,
      generatedAt: formatTimestamp(this.clock.now())
    }
    return report
  }

  /** Renders the narrative summary; the export path and tests share this. */
  markdown(report: ProvenanceReport): string {
    return renderMarkdown(report)
  }

  /* -------------------------------------------------------------- reading */

  private read(db: Db, global: Db, projectId: string, filter: ProvenanceFilter): SourceData {
    const data: SourceData = {
      ideas: new Map(),
      ideaOrder: [],
      derivedFrom: new Map(),
      derivedAt: new Map(),
      relatedTo: [],
      validates: [],
      evidence: [],
      experiments: [],
      notes: [],
      papers: new Map(),
      activities: [],
      weekly: []
    }

    for (const row of db
      .prepare(
        `SELECT id, title, description, status, rejection_reason, created_at, updated_at
           FROM ideas WHERE deleted_at IS NULL ORDER BY created_at, id`
      )
      .all() as Array<{
      id: string
      title: string
      description: string
      status: string
      rejection_reason: string
      created_at: string
      updated_at: string
    }>) {
      data.ideas.set(row.id, {
        id: row.id,
        title: row.title,
        description: row.description,
        status: row.status,
        rejectionReason: row.rejection_reason,
        createdAt: row.created_at,
        updatedAt: row.updated_at
      })
      data.ideaOrder.push(row.id)
    }

    for (const row of db
      .prepare(
        `SELECT src_type, src_id, relation, dst_type, dst_id, created_at FROM entity_relations
          WHERE relation IN ('derived_from','validates','related_to') ORDER BY created_at, id`
      )
      .all() as Array<{
      src_type: string
      src_id: string
      relation: string
      dst_type: string
      dst_id: string
      created_at: string
    }>) {
      const relation: RelationRow = {
        srcType: row.src_type,
        srcId: row.src_id,
        relation: row.relation,
        dstType: row.dst_type,
        dstId: row.dst_id,
        at: row.created_at
      }
      if (row.relation === 'derived_from') {
        if (row.src_type === 'idea' && row.dst_type === 'idea' && !data.derivedFrom.has(row.src_id)) {
          data.derivedFrom.set(row.src_id, row.dst_id)
          data.derivedAt.set(row.src_id, row.created_at)
        }
      } else if (row.relation === 'validates') {
        if (row.src_type === 'experiment' && row.dst_type === 'idea') data.validates.push(relation)
      } else {
        data.relatedTo.push(relation)
      }
    }

    for (const row of db
      .prepare(
        `SELECT id, idea_id, direction, content, source_type, source_id, confirmed, created_at
           FROM evidence WHERE deleted_at IS NULL ORDER BY created_at, id`
      )
      .all() as Array<{
      id: string
      idea_id: string
      direction: string
      content: string
      source_type: string
      source_id: string
      confirmed: number
      created_at: string
    }>) {
      data.evidence.push({
        id: row.id,
        ideaId: row.idea_id,
        direction: row.direction,
        content: row.content,
        sourceType: row.source_type,
        sourceId: row.source_id,
        confirmed: row.confirmed === 1,
        createdAt: row.created_at
      })
    }

    const experiments = new Map<string, ExperimentRow>()
    const experimentOrder: string[] = []
    for (const row of db
      .prepare(
        `SELECT id, title, created_at, updated_at FROM experiments
          WHERE deleted_at IS NULL ORDER BY created_at, id`
      )
      .all() as Array<{ id: string; title: string; created_at: string; updated_at: string }>) {
      experiments.set(row.id, {
        id: row.id,
        title: row.title,
        createdAt: row.created_at,
        updatedAt: row.updated_at,
        runId: null,
        runStatus: '',
        snapshot: null
      })
      experimentOrder.push(row.id)
    }
    for (const row of db
      .prepare(
        `SELECT experiment_id, id, status FROM runs
          WHERE deleted_at IS NULL ORDER BY updated_at DESC, created_at DESC, id`
      )
      .all() as Array<{ experiment_id: string; id: string; status: string }>) {
      const experiment = experiments.get(row.experiment_id)
      if (experiment !== undefined && experiment.runId === null) {
        experiment.runId = row.id
        experiment.runStatus = row.status
      }
    }
    const runToExperiment = new Map<string, string>()
    for (const id of experimentOrder) {
      const runId = experiments.get(id)?.runId
      if (runId !== undefined && runId !== null) runToExperiment.set(runId, id)
    }
    for (const row of db
      .prepare(
        `SELECT id, target_type, target_id, metrics, git_info, created_at FROM snapshots
          ORDER BY created_at DESC, id DESC`
      )
      .all() as Array<{
      id: string
      target_type: string
      target_id: string
      metrics: string
      git_info: string
      created_at: string
    }>) {
      const experimentId = row.target_type === 'run' ? runToExperiment.get(row.target_id) : row.target_id
      if (experimentId === undefined) continue
      const experiment = experiments.get(experimentId)
      if (experiment === undefined || experiment.snapshot !== null) continue
      experiment.snapshot = {
        id: row.id,
        targetType: row.target_type,
        targetId: row.target_id,
        metrics: row.metrics,
        gitInfo: row.git_info,
        createdAt: row.created_at
      }
    }
    for (const id of experimentOrder) data.experiments.push(experiments.get(id)!)

    for (const row of db
      .prepare(
        `SELECT id, title, coalesce(paper_id, '') AS paper_id, file_path, created_at, updated_at
           FROM notes WHERE deleted_at IS NULL ORDER BY created_at, id`
      )
      .all() as Array<{
      id: string
      title: string
      paper_id: string
      file_path: string
      created_at: string
      updated_at: string
    }>) {
      data.notes.push({
        id: row.id,
        title: row.title,
        paperId: row.paper_id,
        body: readNoteBody(this.paths, row.file_path),
        createdAt: row.created_at,
        updatedAt: row.updated_at
      })
    }

    const paperLinks = db
      .prepare(`SELECT paper_id, linked_at FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`)
      .all(projectId) as Array<{ paper_id: string; linked_at: string }>
    const paperStatement = global.prepare(`SELECT metadata FROM papers WHERE id = ?`)
    for (const link of paperLinks) {
      const row = paperStatement.get(link.paper_id) as { metadata: string } | undefined
      data.papers.set(link.paper_id, {
        id: link.paper_id,
        title: row === undefined ? link.paper_id.slice(0, 8) : parsePaperMetadata(row.metadata).title,
        importedAt: link.linked_at
      })
    }

    for (const row of db
      .prepare(
        `SELECT id, period_start, period_end, coalesce(confirmed_at, updated_at) AS at
           FROM weekly_reports WHERE deleted_at IS NULL AND status = 'confirmed'
          ORDER BY period_start, id`
      )
      .all() as Array<{ id: string; period_start: string; period_end: string; at: string }>) {
      data.weekly.push({ id: row.id, start: row.period_start, end: row.period_end, at: row.at })
    }

    const where = ['1 = 1']
    const params: unknown[] = []
    if (filter.from !== undefined) {
      where.push('created_at >= ?')
      params.push(filter.from)
    }
    if (filter.to !== undefined) {
      where.push('created_at <= ?')
      params.push(filter.to)
    }
    for (let offset = 0; ; offset += ACTIVITY_PAGE) {
      const page = db
        .prepare(
          `SELECT id, event_type, entity_type, entity_id, payload, created_at FROM activities
            WHERE ${where.join(' AND ')} ORDER BY created_at, id LIMIT ? OFFSET ?`
        )
        .all(...params, ACTIVITY_PAGE, offset) as Array<{
        id: string
        event_type: string
        entity_type: string
        entity_id: string
        payload: string
        created_at: string
      }>
      for (const row of page) {
        let payload: { summary?: unknown; new?: unknown } = {}
        try {
          payload = JSON.parse(row.payload) as { summary?: unknown; new?: unknown }
        } catch {
          payload = {}
        }
        data.activities.push({
          id: row.id,
          eventType: row.event_type,
          entityType: row.entity_type,
          entityId: row.entity_id,
          payload,
          createdAt: row.created_at
        })
      }
      if (page.length < ACTIVITY_PAGE) break
    }
    return data
  }

  /* --------------------------------------------------------------- nodes */

  private nodes(data: SourceData, filter: ProvenanceFilter): ProvenanceNode[] {
    const out: ProvenanceNode[] = []
    const seen = new Set<string>()
    const add = (node: ProvenanceNode): void => {
      if (!Number.isFinite(Date.parse(node.at))) return
      if (filter.from !== undefined && Date.parse(node.at) < Date.parse(filter.from)) return
      if (filter.to !== undefined && Date.parse(node.at) > Date.parse(filter.to)) return
      if (filter.milestones === true && node.routine) return
      if (seen.has(node.id)) return
      seen.add(node.id)
      out.push(node)
    }

    for (const id of data.ideaOrder) {
      const idea = data.ideas.get(id)!
      add({
        id: `idea:${idea.id}`,
        at: idea.createdAt,
        kind: 'idea_proposed',
        entity: { type: 'idea', id: idea.id },
        title: idea.title,
        summary: truncate(idea.description, SNIPPET_RUNES),
        badge: idea.status,
        linkedNodes: [],
        routine: false
      })
    }
    for (const [child, parent] of data.derivedFrom) {
      const idea = data.ideas.get(child)
      if (idea === undefined) continue
      const from = data.ideas.get(parent)
      add({
        id: `derived:${child}:${parent}`,
        at: data.derivedAt.get(child) ?? idea.createdAt,
        kind: 'idea_derived',
        entity: { type: 'idea', id: child },
        title: idea.title,
        summary: from === undefined ? '' : `派生自「${from.title}」`,
        badge: 'derived_from',
        linkedNodes: [{ type: 'idea', id: parent }],
        routine: false
      })
    }
    for (const evidence of data.evidence) {
      const idea = data.ideas.get(evidence.ideaId)
      add({
        id: `evidence:${evidence.id}`,
        at: evidence.createdAt,
        kind: 'evidence_recorded',
        entity: { type: 'evidence', id: evidence.id },
        title: idea?.title ?? '证据',
        summary: truncate(evidence.content, SNIPPET_RUNES),
        badge: evidence.confirmed ? evidence.direction : `${evidence.direction} · draft`,
        linkedNodes: [
          { type: 'idea', id: evidence.ideaId },
          { type: evidence.sourceType, id: evidence.sourceId }
        ],
        routine: false
      })
    }
    for (const experiment of data.experiments) {
      if (experiment.snapshot === null) continue
      add({
        id: `experiment:${experiment.id}`,
        at: experiment.snapshot.createdAt,
        kind: 'experiment_analyzed',
        entity: { type: 'experiment', id: experiment.id },
        title: experiment.title,
        summary: metricDigest(experiment.snapshot.metrics),
        badge: shortSha(experiment.snapshot.gitInfo),
        linkedNodes: experimentLinks(data, experiment),
        routine: false
      })
    }
    for (const note of data.notes) {
      add({
        id: `note:${note.id}`,
        at: note.createdAt,
        kind: 'note_created',
        entity: { type: 'note', id: note.id },
        title: note.title,
        summary: truncate(collapse(note.body), NOTE_SUMMARY_RUNES),
        badge: '',
        linkedNodes: rosLinks(note.body),
        routine: false
      })
    }
    for (const paper of data.papers.values()) {
      add({
        id: `paper:${paper.id}`,
        at: paper.importedAt,
        kind: 'paper_imported',
        entity: { type: 'paper', id: paper.id },
        title: paper.title,
        summary: '',
        badge: '',
        linkedNodes: [],
        routine: false
      })
    }
    for (const weekly of data.weekly) {
      add({
        id: `weekly:${weekly.id}`,
        at: weekly.at,
        kind: 'weekly_confirmed',
        entity: { type: 'weekly_report', id: weekly.id },
        title: `${weekly.start} ~ ${weekly.end}`,
        summary: '',
        badge: 'confirmed',
        linkedNodes: [],
        routine: false
      })
    }

    for (const activity of data.activities) {
      switch (activity.eventType) {
        case 'status.changed': {
          if (activity.entityType !== 'idea') continue
          const idea = data.ideas.get(activity.entityId)
          const summary = typeof activity.payload.summary === 'string' ? activity.payload.summary : ''
          add({
            id: `status:${activity.id}`,
            at: activity.createdAt,
            kind: 'idea_status_changed',
            entity: { type: 'idea', id: activity.entityId },
            title: idea?.title ?? summary,
            summary: idea?.rejectionReason !== undefined && idea.rejectionReason !== '' ? idea.rejectionReason : summary,
            badge: statusOf(activity),
            linkedNodes: [
              ...evidenceRefs(data, activity.entityId),
              ...validatingExperiments(data, activity.entityId)
            ],
            routine: false
          })
          break
        }
        case 'note.updated': {
          if (filter.milestones === true) continue
          add({
            id: `edit:${activity.id}`,
            at: activity.createdAt,
            kind: 'note_created',
            entity: { type: 'note', id: activity.entityId },
            title: typeof activity.payload.summary === 'string' ? activity.payload.summary : '',
            summary: '',
            badge: 'updated',
            linkedNodes: [],
            routine: true
          })
          break
        }
        case 'entity.updated': {
          if (filter.milestones === true) continue
          add({
            id: `edited:${activity.id}`,
            at: activity.createdAt,
            kind: routineKind(activity.entityType),
            entity: { type: activity.entityType, id: activity.entityId },
            title: typeof activity.payload.summary === 'string' ? activity.payload.summary : '',
            summary: '',
            badge: 'updated',
            linkedNodes: [],
            routine: true
          })
          break
        }
        default:
          break
      }
    }

    out.sort((left, right) => {
      const at = Date.parse(left.at) - Date.parse(right.at)
      if (at !== 0) return at
      return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
    })
    return out
  }
}

/* ------------------------------------------------------------- helpers */

function statusOf(activity: ActivityRow): string {
  const next = activity.payload.new
  if (typeof next !== 'object' || next === null) return ''
  const status = (next as Record<string, unknown>).status
  return typeof status === 'string' ? status : ''
}

function routineKind(entityType: string): ProvenanceMilestoneKind {
  switch (entityType) {
    case 'idea':
      return 'idea_status_changed'
    case 'experiment':
    case 'run':
    case 'snapshot':
      return 'experiment_analyzed'
    case 'evidence':
      return 'evidence_recorded'
    case 'paper':
      return 'paper_imported'
    case 'weekly_report':
      return 'weekly_confirmed'
    default:
      return 'note_created'
  }
}

function evidenceRefs(data: SourceData, ideaId: string): ProvenanceEntityRef[] {
  return data.evidence
    .filter((evidence) => evidence.ideaId === ideaId)
    .map((evidence) => ({ type: 'evidence', id: evidence.id }))
}

function validatingExperiments(data: SourceData, ideaId: string): ProvenanceEntityRef[] {
  return dedupe(
    data.validates
      .filter((relation) => relation.dstId === ideaId)
      .map((relation) => ({ type: 'experiment', id: relation.srcId }))
  )
}

function experimentLinks(data: SourceData, experiment: ExperimentRow): ProvenanceEntityRef[] {
  const links: ProvenanceEntityRef[] = []
  for (const relation of data.validates) {
    if (relation.srcId !== experiment.id) continue
    links.push({ type: 'experiment', id: relation.srcId }, { type: 'idea', id: relation.dstId })
  }
  if (experiment.runId !== null) links.push({ type: 'run', id: experiment.runId })
  if (experiment.snapshot !== null) links.push({ type: 'snapshot', id: experiment.snapshot.id })
  return dedupe(links)
}

function shortSha(gitInfo: string): string {
  let info: Record<string, unknown>
  try {
    info = JSON.parse(gitInfo) as Record<string, unknown>
  } catch {
    return ''
  }
  let sha = typeof info.short_sha === 'string' ? info.short_sha : ''
  if (sha === '' && typeof info.commit === 'string') sha = info.commit
  if (sha.length > 8) sha = sha.slice(0, 8)
  if (info.dirty === true) sha += '*'
  return sha
}

/* -------------------------------------------------------------- sections */

export function groupSections(nodes: ProvenanceNode[], byMonth: boolean): ProvenanceSection[] {
  const index = new Map<string, number>()
  const out: ProvenanceSection[] = []
  for (const node of nodes) {
    const date = new Date(node.at)
    const key = byMonth ? monthKey(date) : weekKey(date)
    const existing = index.get(key)
    if (existing === undefined) {
      index.set(key, out.length)
      out.push({ key, label: key, at: node.at, nodes: [node] })
    } else {
      out[existing]!.nodes.push(node)
    }
  }
  out.sort((left, right) => Date.parse(left.at) - Date.parse(right.at))
  return out
}

/** ISO week key `YYYY-Www`, computed in UTC so the grouping is tag-stable. */
export function weekKey(date: Date): string {
  const target = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()))
  const day = target.getUTCDay() === 0 ? 7 : target.getUTCDay()
  target.setUTCDate(target.getUTCDate() + 4 - day)
  const yearStart = new Date(Date.UTC(target.getUTCFullYear(), 0, 1))
  const week = Math.ceil(((target.getTime() - yearStart.getTime()) / 86_400_000 + 1) / 7)
  return `${String(target.getUTCFullYear()).padStart(4, '0')}-W${String(week).padStart(2, '0')}`
}

function monthKey(date: Date): string {
  return `${String(date.getUTCFullYear()).padStart(4, '0')}-${String(date.getUTCMonth() + 1).padStart(2, '0')}`
}

/* -------------------------------------------------------------- lineage */

export function buildLineage(data: SourceData): ProvenanceLineageTree {
  const nodes = new Map<string, ProvenanceLineageNode>()
  const order = [...data.ideaOrder].sort((left, right) => {
    const a = data.ideas.get(left)!
    const b = data.ideas.get(right)!
    if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1
    return left < right ? -1 : 1
  })

  const support = new Map<string, number>()
  const oppose = new Map<string, number>()
  for (const evidence of data.evidence) {
    if (evidence.direction === 'support') support.set(evidence.ideaId, (support.get(evidence.ideaId) ?? 0) + 1)
    if (evidence.direction === 'oppose') oppose.set(evidence.ideaId, (oppose.get(evidence.ideaId) ?? 0) + 1)
  }

  const experimentsOf = new Map<string, ExperimentRow[]>()
  for (const relation of data.validates) {
    const experiment = data.experiments.find((candidate) => candidate.id === relation.srcId)
    if (experiment === undefined) continue
    experimentsOf.set(relation.dstId, [...(experimentsOf.get(relation.dstId) ?? []), experiment])
  }

  for (const id of order) {
    const idea = data.ideas.get(id)!
    const node: ProvenanceLineageNode = {
      id: idea.id,
      title: idea.title,
      status: idea.status,
      rejectionReason: idea.rejectionReason,
      supportCount: support.get(id) ?? 0,
      opposeCount: oppose.get(id) ?? 0,
      papers: [],
      notes: [],
      experiments: [],
      children: [],
      createdAt: idea.createdAt,
      updatedAt: idea.updatedAt
    }
    for (const experiment of experimentsOf.get(id) ?? []) {
      node.experiments.push({
        id: experiment.id,
        title: experiment.title,
        badge: experiment.runStatus === '' ? 'no_run' : experiment.runStatus
      })
    }
    const noteIds = new Set<string>()
    for (const note of data.notes) {
      if (!note.body.includes(`ros://idea/${id}`)) continue
      noteIds.add(note.id)
      node.notes.push({ id: note.id, title: note.title, badge: '' })
      if (note.paperId !== '') node.papers.push(paperRef(data, note.paperId))
    }
    for (const relation of data.relatedTo) {
      if (relation.srcType === 'idea' && relation.srcId === id && relation.dstType === 'note') {
        if (noteIds.has(relation.dstId)) continue
        noteIds.add(relation.dstId)
        const note = data.notes.find((candidate) => candidate.id === relation.dstId)
        if (note !== undefined) {
          node.notes.push({ id: note.id, title: note.title, badge: '' })
          if (note.paperId !== '') node.papers.push(paperRef(data, note.paperId))
        }
      } else if (relation.dstType === 'idea' && relation.dstId === id && relation.srcType === 'note') {
        if (noteIds.has(relation.srcId)) continue
        noteIds.add(relation.srcId)
        const note = data.notes.find((candidate) => candidate.id === relation.srcId)
        if (note !== undefined) {
          node.notes.push({ id: note.id, title: note.title, badge: '' })
          if (note.paperId !== '') node.papers.push(paperRef(data, note.paperId))
        }
      } else if (relation.srcType === 'idea' && relation.srcId === id && relation.dstType === 'paper') {
        node.papers.push(paperRef(data, relation.dstId))
      } else if (relation.dstType === 'idea' && relation.dstId === id && relation.srcType === 'paper') {
        node.papers.push(paperRef(data, relation.srcId))
      }
    }
    for (const evidence of data.evidence) {
      if (evidence.ideaId === id && evidence.sourceType === 'paper') node.papers.push(paperRef(data, evidence.sourceId))
    }
    node.notes = dedupeLineage(node.notes)
    node.papers = dedupeLineage(node.papers)
    node.experiments = dedupeLineage(node.experiments)
    node.notes.sort(compareRefs)
    node.papers.sort(compareRefs)
    node.experiments.sort(compareRefs)
    nodes.set(id, node)
  }

  for (const id of order) {
    const parentId = data.derivedFrom.get(id)
    if (parentId === undefined) continue
    const parent = nodes.get(parentId)
    const child = nodes.get(id)
    if (parent === undefined || child === undefined || parentId === id) continue
    parent.children.push(child)
  }

  const tree: ProvenanceLineageTree = { roots: [] }
  const reachable = new Set<string>()
  const mark = (node: ProvenanceLineageNode): void => {
    if (reachable.has(node.id)) return
    reachable.add(node.id)
    for (const child of node.children) mark(child)
  }
  for (const id of order) {
    const parentId = data.derivedFrom.get(id)
    if (parentId !== undefined && nodes.has(parentId)) continue
    const node = nodes.get(id)!
    tree.roots.push(node)
    mark(node)
  }
  for (const id of order) {
    if (reachable.has(id)) continue
    const node = nodes.get(id)!
    tree.roots.push(node)
    mark(node)
  }
  sortRoots(tree.roots)
  return tree
}

function paperRef(data: SourceData, id: string): ProvenanceLineageRef {
  const paper = data.papers.get(id)
  return { id, title: paper === undefined || paper.title === '' ? id : paper.title, badge: '' }
}

function dedupeLineage(refs: ProvenanceLineageRef[]): ProvenanceLineageRef[] {
  const seen = new Set<string>()
  const out: ProvenanceLineageRef[] = []
  for (const ref of refs) {
    if (seen.has(ref.id)) continue
    seen.add(ref.id)
    out.push(ref)
  }
  return out
}

function compareRefs(left: ProvenanceLineageRef, right: ProvenanceLineageRef): number {
  if (left.title !== right.title) return left.title < right.title ? -1 : 1
  return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
}

function sortRoots(nodes: ProvenanceLineageNode[]): void {
  nodes.sort((left, right) => {
    if (left.createdAt !== right.createdAt) return left.createdAt < right.createdAt ? -1 : 1
    return left.id < right.id ? -1 : 1
  })
  for (const node of nodes) sortRoots(node.children)
}

/* --------------------------------------------------------- focus filter */

/** Walks derived_from in both directions from `focus` (inclusive). */
export function ideaFamily(focus: string, data: SourceData): Set<string> {
  const family = new Set<string>([focus])
  const children = new Map<string, string[]>()
  for (const [child, parent] of data.derivedFrom) {
    children.set(parent, [...(children.get(parent) ?? []), child])
  }
  const stack = [focus]
  while (stack.length > 0) {
    const id = stack.pop()!
    const parent = data.derivedFrom.get(id)
    if (parent !== undefined && !family.has(parent)) {
      family.add(parent)
      stack.push(parent)
    }
    for (const child of children.get(id) ?? []) {
      if (family.has(child)) continue
      family.add(child)
      stack.push(child)
    }
  }
  return family
}

function filterNodes(nodes: ProvenanceNode[], family: Set<string>, data: SourceData): ProvenanceNode[] {
  const keep = new Set<string>()
  for (const id of family) keep.add(`idea:${id}`)
  for (const evidence of data.evidence) if (family.has(evidence.ideaId)) keep.add(`evidence:${evidence.id}`)
  for (const relation of data.validates) if (family.has(relation.dstId)) keep.add(`experiment:${relation.srcId}`)
  for (const note of data.notes) {
    if (family.size === 0) continue
    const mentions = [...family].some((id) => note.body.includes(`ros://idea/${id}`))
    if (mentions) {
      keep.add(`note:${note.id}`)
      if (note.paperId !== '') keep.add(`paper:${note.paperId}`)
    }
  }
  for (const evidence of data.evidence) {
    if (family.has(evidence.ideaId) && evidence.sourceType === 'paper') keep.add(`paper:${evidence.sourceId}`)
  }
  return nodes.filter((node) => {
    if (node.kind === 'idea_derived') {
      const linked = node.linkedNodes.find((ref) => ref.type === 'idea')?.id ?? ''
      return family.has(node.entity.id) && family.has(linked)
    }
    if (node.kind === 'weekly_confirmed') return false
    if (node.entity.type === 'idea') return family.has(node.entity.id)
    return keep.has(`${node.entity.type}:${node.entity.id}`)
  })
}

function filterLineage(tree: ProvenanceLineageTree, family: Set<string>): ProvenanceLineageTree {
  const prune = (nodes: ProvenanceLineageNode[]): ProvenanceLineageNode[] =>
    nodes
      .map((node) => ({ ...node, children: prune(node.children) }))
      .filter((node) => family.has(node.id))
  return { roots: prune(tree.roots) }
}

/* -------------------------------------------------------------- markdown */

const KIND_TITLE: Readonly<Record<ProvenanceMilestoneKind, string>> = {
  paper_imported: '引入文献',
  note_created: '记录笔记',
  idea_proposed: '提出想法',
  idea_derived: '衍生想法',
  idea_status_changed: '想法状态',
  experiment_analyzed: '实验分析',
  evidence_recorded: '记录证据',
  weekly_confirmed: '确认周报'
}

function renderMarkdown(report: ProvenanceReport): string {
  const lines: string[] = []
  const name = report.projectName === '' ? report.projectId : report.projectName
  lines.push(`# 科研脉络 · ${name}`, '')
  lines.push(`生成时间：${report.generatedAt.replace('T', ' ').replace('.000Z', ' UTC')}`, '')
  let scope = '全部时间'
  if (report.from !== null && report.to !== null) {
    scope = `${report.from.slice(0, 10)} ~ ${report.to.slice(0, 10)}`
  } else if (report.from !== null) {
    scope = `${report.from.slice(0, 10)} 起`
  } else if (report.to !== null) {
    scope = `截至 ${report.to.slice(0, 10)}`
  }
  scope += report.milestones ? ' · 仅核心里程碑' : ' · 全部活动'
  if (report.focusIdeaId !== null) scope += ` · 聚焦想法 ${report.focusIdeaId}`
  lines.push(`范围：${scope}`, '')

  lines.push('## 假设演化', '')
  const tree = report.focusedLineage.roots.length > 0 ? report.focusedLineage : report.lineage
  if (tree.roots.length === 0) {
    lines.push('（尚无可回放的 Idea 演化）', '')
  } else {
    for (const root of tree.roots) writeLineage(lines, root, 0)
    lines.push('')
  }

  lines.push('## 里程碑', '')
  if (report.nodes.length === 0) {
    lines.push('（该范围内没有活动）', '')
  } else {
    for (const section of groupSections(report.nodes, false)) {
      lines.push(`### ${section.label}`, '')
      for (const node of section.nodes) writeNode(lines, node)
      lines.push('')
    }
  }

  lines.push('## 失败原因', '')
  const ideas = flattenTree(report.focusedLineage, report.lineage)
  let failures = 0
  for (const idea of ideas) {
    if (idea.status !== 'Rejected' && idea.rejectionReason === '') continue
    failures += 1
    lines.push(`- **${idea.title}**（${idea.status}）：${idea.rejectionReason === '' ? '未记录原因' : idea.rejectionReason}`)
  }
  if (failures === 0) lines.push('（没有被否决的假设）')
  lines.push('')

  lines.push('## 实验依据', '')
  let basis = 0
  for (const idea of ideas) {
    for (const experiment of idea.experiments) {
      basis += 1
      const summary = experimentSummary(report, experiment.id)
      lines.push(
        `- ${experiment.title}（${experiment.badge}）· 支撑「${idea.title}」${summary === '' ? '' : `：${summary}`}`
      )
    }
  }
  if (basis === 0) lines.push('（没有关联的验证实验）')
  return lines.join('\n')
}

function writeLineage(lines: string[], node: ProvenanceLineageNode, depth: number): void {
  const indent = '  '.repeat(depth)
  const extras: string[] = []
  if (node.experiments.length > 0) extras.push(`验证实验 ${node.experiments.length}`)
  lines.push(
    `${indent}- **${node.title}** — ${node.status}（证据 +${node.supportCount} / -${node.opposeCount}${
      extras.length === 0 ? '' : `，${extras.join('，')}`
    }）`
  )
  if (node.rejectionReason !== '') lines.push(`${indent}  - 失败原因：${node.rejectionReason}`)
  if (node.papers.length > 0) lines.push(`${indent}  - 启发文献：${joinTitles(node.papers)}`)
  if (node.notes.length > 0) lines.push(`${indent}  - 推导笔记：${joinTitles(node.notes)}`)
  if (node.experiments.length > 0) lines.push(`${indent}  - 验证实验：${joinTitles(node.experiments)}`)
  for (const child of node.children) writeLineage(lines, child, depth + 1)
}

function writeNode(lines: string[], node: ProvenanceNode): void {
  let line = `- ${node.at.slice(0, 16).replace('T', ' ')} · ${KIND_TITLE[node.kind]} · ${node.title}`
  if (node.badge !== '') line += ` — \`${node.badge}\``
  lines.push(line)
  if (node.summary !== '') lines.push(`  - ${collapse(node.summary)}`)
  if (node.linkedNodes.length > 0) {
    lines.push(`  - 关联：${node.linkedNodes.map((ref) => `${ref.type}/${ref.id.slice(0, 8)}`).join('、')}`)
  }
}

function experimentSummary(report: ProvenanceReport, experimentId: string): string {
  const node = report.nodes.find(
    (candidate) => candidate.kind === 'experiment_analyzed' && candidate.entity.id === experimentId
  )
  return node?.summary ?? ''
}

function flattenTree(focused: ProvenanceLineageTree, full: ProvenanceLineageTree): ProvenanceLineageNode[] {
  const roots = focused.roots.length > 0 ? focused.roots : full.roots
  const out: ProvenanceLineageNode[] = []
  const walk = (nodes: ProvenanceLineageNode[]): void => {
    for (const node of nodes) {
      out.push(node)
      walk(node.children)
    }
  }
  walk(roots)
  return out
}

function joinTitles(refs: ProvenanceLineageRef[]): string {
  return refs.map((ref) => ref.title).join('、')
}
