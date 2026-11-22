import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import * as path from 'node:path'

import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import { DEFAULT_MAX_EVENTS, followingPeriod, isoWeek, utcBounds } from './period'
import { DEFAULT_TEMPLATE, addDays, periodEndDate, truncateRunes } from './template'
import type {
  ActivityDigest,
  BlockedTask,
  Coverage,
  DigestItem,
  DigestSection,
  EntityRef,
  PlannedOrigin,
  PlannedTask,
  SectionKey,
  WeeklyPeriod
} from './types'

export { DEFAULT_MAX_EVENTS }
export const MAX_SECTION_ITEMS = 80
export const MAX_SUMMARY_RUNES = 300
export const MAX_BRIEF_RUNES = 400
export const MAX_REFS = 500

const PAGE_SIZE = 500

export interface DigestContext {
  readonly projectId: string
  readonly db: Db
  readonly globalDb: Db
  readonly root: string
  readonly clock: Clock
  readonly timeZone: string
  readonly maxEvents: number
  readonly logger?: { warn(message: string, fields?: Record<string, unknown>): void }
}

interface StoredActivity {
  id: string
  eventType: string
  entityType: string
  entityId: string
  actor: string
  createdAt: string
  old?: Record<string, unknown>
  new?: Record<string, unknown>
  summary: string
  saveCount: number
  lastSavedAt: string
}

interface EntityBrief {
  title: string
  noteKind: string
  aiContentUnconfirmed: boolean
  broken: boolean
  abstract: string
}

interface ClassifiedItem {
  section: SectionKey
  item: DigestItem
}

/**
 * Builds the deterministic digest of one period. Read-only:
 * it never writes an Activity row, a report or a file.
 */
export function collectActivities(
  context: DigestContext,
  period: WeeklyPeriod,
  maxEvents = context.maxEvents
): ActivityDigest {
  const { since, until } = utcBounds(period)
  const rows = readActivities(context.db, since, until)
  const countsByType: Record<string, number> = {}
  const countsByActor: Record<string, number> = {}
  for (const row of rows) {
    countsByType[row.eventType] = (countsByType[row.eventType] ?? 0) + 1
    countsByActor[row.actor] = (countsByActor[row.actor] ?? 0) + 1
  }

  const limit = maxEvents > 0 ? maxEvents : DEFAULT_MAX_EVENTS
  let kept = rows
  let truncated = false
  if (rows.length > limit) {
    kept = truncateActivities(rows, limit)
    truncated = true
  }
  kept = [...kept].sort(compareActivity)

  const refs = collectRefs(kept)
  const briefs = resolveBriefs(context, refs)
  const warnings: string[] = []
  const warn = (message: string): void => {
    if (!warnings.includes(message)) warnings.push(message)
  }

  const sections = new Map<SectionKey, DigestItem[]>()
  const housekeeping: DigestItem[] = []
  for (const row of kept) {
    const classified = classifyActivity(row, briefs, warn)
    if (isHousekeeping(row)) {
      housekeeping.push(classified.item)
      continue
    }
    const list = sections.get(classified.section) ?? []
    list.push(classified.item)
    sections.set(classified.section, list)
  }

  const blocked = readBlockedTasks(context.db)
  const nextISO = isoWeek(period.endExclusiveDate)
  const currentISO = isoWeek(addDays(period.endExclusiveDate, -1))
  const planned = readPlannedTasks(context.db, period, nextISO, currentISO)

  const digest: ActivityDigest = {
    period,
    timeZone: period.timeZone,
    collectedAt: context.clock.now().toISOString(),
    sections: [],
    blocked,
    planned,
    stats: { countsByType, countsBySection: {}, countsByActor },
    truncated,
    warnings,
    digestHash: '',
    housekeeping: housekeeping.slice(0, MAX_SECTION_ITEMS)
  }

  for (const templateSection of DEFAULT_TEMPLATE.sections) {
    let items = collapseStatuses(sections.get(templateSection.key) ?? [])
    if (items.length > MAX_SECTION_ITEMS) items = items.slice(0, MAX_SECTION_ITEMS)
    const section: DigestSection = {
      key: templateSection.key,
      title: templateSection.title,
      items
    }
    digest.sections.push(section)
    digest.stats.countsBySection[templateSection.key] = items.length
  }

  if (truncated) {
    warnings.push(`活动条目超过上限 ${limit}，已按优先级截断`)
  }
  digest.digestHash = digestHash(digest)
  return digest
}

function readActivities(db: Db, since: string, until: string): StoredActivity[] {
  const out: StoredActivity[] = []
  let offset = 0
  for (;;) {
    const rows = db
      .prepare(
        `SELECT id, event_type, entity_type, entity_id, payload, actor, created_at
           FROM activities
          WHERE created_at >= ? AND created_at < ?
          ORDER BY created_at ASC, id ASC
          LIMIT ? OFFSET ?`
      )
      .all(since, until, PAGE_SIZE, offset) as Array<{
      id: string
      event_type: string
      entity_type: string
      entity_id: string
      payload: string
      actor: string
      created_at: string
    }>
    for (const row of rows) out.push(decodeActivity(row))
    if (rows.length < PAGE_SIZE) break
    offset += rows.length
  }
  return out
}

function decodeActivity(row: {
  id: string
  event_type: string
  entity_type: string
  entity_id: string
  payload: string
  actor: string
  created_at: string
}): StoredActivity {
  const payload = safeJson<Record<string, unknown>>(row.payload) ?? {}
  const activity: StoredActivity = {
    id: row.id,
    eventType: row.event_type,
    entityType: row.entity_type,
    entityId: row.entity_id,
    actor: row.actor,
    createdAt: row.created_at,
    summary: typeof payload.summary === 'string' ? payload.summary : '',
    saveCount: typeof payload.save_count === 'number' ? payload.save_count : 0,
    lastSavedAt: typeof payload.last_saved_at === 'string' ? payload.last_saved_at : ''
  }
  if (isObject(payload.old)) activity.old = payload.old
  if (isObject(payload.new)) activity.new = payload.new
  return activity
}

function safeJson<T>(raw: string): T | undefined {
  try {
    return JSON.parse(raw) as T
  } catch {
    return undefined
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function compareActivity(left: StoredActivity, right: StoredActivity): number {
  if (left.createdAt !== right.createdAt) return left.createdAt < right.createdAt ? -1 : 1
  return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
}

/** Higher priority means "kept when the digest is truncated". */
function eventPriority(row: StoredActivity): number {
  switch (row.eventType) {
    case 'status.changed':
      return 60
    case 'ai.analysis_completed':
      return 50
    case 'paper.imported':
    case 'paper.overwrite_confirmed':
      return 40
    case 'entity.created':
      return 30
    case 'relation.added':
    case 'relation.removed':
      return 20
    case 'note.updated':
      return 10
    default:
      return 5
  }
}

function truncateActivities(rows: StoredActivity[], limit: number): StoredActivity[] {
  const sorted = [...rows].sort((left, right) => {
    const priority = eventPriority(left) - eventPriority(right)
    if (priority !== 0) return -priority
    if (left.createdAt !== right.createdAt) return left.createdAt < right.createdAt ? 1 : -1
    return left.id < right.id ? 1 : left.id > right.id ? -1 : 0
  })
  return sorted.slice(0, limit).sort(compareActivity)
}

/** Filing actions are not research progress: they never reach body or prompt. */
function isHousekeeping(row: StoredActivity): boolean {
  switch (row.eventType) {
    case 'entity.created':
      // Recording evidence is a research judgement, not filing.
      return row.entityType !== 'evidence'
    case 'entity.deleted':
    case 'entity.restored':
    case 'paper.imported':
    case 'paper.overwrite_confirmed':
    case 'paper.metadata_updated':
    case 'report.generated':
    case 'project.exported':
      return true
    default:
      return false
  }
}

function classifyActivity(
  row: StoredActivity,
  briefs: Map<string, EntityBrief>,
  warn: (message: string) => void
): ClassifiedItem {
  const ref: EntityRef = { type: row.entityType, id: row.entityId }
  const brief = briefs.get(`${ref.type}:${ref.id}`) ?? {
    title: '',
    noteKind: '',
    aiContentUnconfirmed: false,
    broken: false,
    abstract: ''
  }
  const classified = classify(row, brief)
  if (classified.warning !== undefined) warn(classified.warning)

  let summary = row.summary.trim()
  if (summary === '' && row.eventType === 'status.changed' && row.entityType === 'task') {
    summary = taskChangeSummary(row.old, row.new, brief.title)
  }
  if (summary === '') summary = defaultSummary(row)
  if (row.eventType === 'note.updated' && summary === '更新笔记' && brief.title !== '') {
    summary = `更新笔记：${brief.title}`
  }
  if (summary === '') summary = brief.title
  if (summary === '') summary = `${row.entityType} ${row.entityId}`
  summary = truncateRunes(summary, MAX_SUMMARY_RUNES)

  let detail = ''
  let lastAt = row.createdAt
  if (row.eventType === 'note.updated') {
    detail = `${Math.max(row.saveCount, 1)} 次保存`
    if (row.lastSavedAt !== '') lastAt = row.lastSavedAt
  }
  if (row.eventType === 'relation.added' || row.eventType === 'relation.removed') {
    detail = relationText(row, row.eventType === 'relation.removed' ? '解除关联' : '关联')
  }
  if (brief.aiContentUnconfirmed) {
    detail = detail === '' ? 'AI 内容待确认' : `${detail}；AI 内容待确认`
  }
  if (brief.broken) summary += '（目标已删除）'

  return {
    section: classified.section,
    item: {
      ref,
      kind: itemKind(row.eventType),
      state: 'done',
      summary,
      detail,
      old: normalizeStatus(row).old,
      new: normalizeStatus(row).new,
      actor: row.actor,
      firstAt: row.createdAt,
      lastAt,
      eventIds: [row.id],
      unconfirmedSource: brief.aiContentUnconfirmed,
      broken: brief.broken,
      brief: brief.abstract
    }
  }
}

function classify(row: StoredActivity, brief: EntityBrief): { section: SectionKey; warning?: string } {
  switch (row.eventType) {
    case 'relation.added':
    case 'relation.removed':
      return { section: relationSection(row) }
    case 'paper.imported':
    case 'paper.overwrite_confirmed':
    case 'paper.metadata_updated':
      return { section: 'literature' }
    case 'report.generated':
    case 'project.exported':
      return { section: 'work' }
    case 'ai.analysis_completed':
      switch (row.entityType) {
        case 'snapshot':
        case 'experiment':
        case 'run':
          return { section: 'experiment' }
        case 'idea':
          return { section: 'idea' }
        case 'weekly_report':
          return { section: 'work' }
        default:
          return classifyEntity(row.entityType, brief)
      }
    case 'status.changed':
      if (row.entityType === 'task') return taskSection(row.old, row.new)
      return classifyEntity(row.entityType, brief)
    default:
      return classifyEntity(row.entityType, brief)
  }
}

function relationSection(row: StoredActivity): SectionKey {
  const object = row.eventType === 'relation.removed' ? row.old : row.new
  if (object === undefined) return 'work'
  const src = typeof object.src_type === 'string' ? object.src_type : ''
  const dst = typeof object.dst_type === 'string' ? object.dst_type : ''
  if (src === 'idea' || dst === 'idea') return 'idea'
  if (src === 'experiment' || dst === 'experiment' || src === 'run' || dst === 'run') return 'experiment'
  if ((src === 'note' && dst === 'paper') || (src === 'paper' && dst === 'note')) return 'literature'
  return 'work'
}

function taskSection(
  oldObject: Record<string, unknown> | undefined,
  newObject: Record<string, unknown> | undefined
): { section: SectionKey; warning?: string } {
  const oldBlocked = taskShape(oldObject).blocked
  const next = taskShape(newObject)
  if (next.archived === true) {
    return { section: 'work', warning: '任务归档事件的归类待确认，暂归「工作内容」' }
  }
  if (next.blocked !== undefined) {
    if (next.blocked && (oldBlocked === undefined || !oldBlocked)) {
      return { section: 'problem' }
    }
    if (!next.blocked && oldBlocked === true) return { section: 'problem' }
  }
  return { section: 'work' }
}

function taskChangeSummary(
  oldObject: Record<string, unknown> | undefined,
  newObject: Record<string, unknown> | undefined,
  title: string
): string {
  const name = title === '' ? '任务' : title
  const next = taskShape(newObject)
  const old = taskShape(oldObject)
  if (next.archived === true) return `归档任务：${name}`
  if (
    next.blocked !== undefined &&
    ((next.blocked && (old.blocked === undefined || !old.blocked)) ||
      (!next.blocked && old.blocked === true))
  ) {
    return next.blocked ? `${name} 标记受阻` : `${name} 解除受阻`
  }
  if (next.isDone && next.column !== '') return `完成任务：${name}（进入 ${next.column}）`
  if (next.column !== '' && next.column !== old.column && old.column !== '') {
    return `${name}：${old.column} → ${next.column}`
  }
  return ''
}

interface TaskShape {
  column: string
  isDone: boolean
  blocked?: boolean
  archived?: boolean
}

function taskShape(object: Record<string, unknown> | undefined): TaskShape {
  if (object === undefined) return { column: '', isDone: false }
  const column =
    typeof object.column_name === 'string' && object.column_name !== ''
      ? object.column_name
      : typeof object.column === 'string'
        ? object.column
        : ''
  return {
    column,
    isDone: object.is_done === true,
    blocked: typeof object.blocked === 'boolean' ? object.blocked : undefined,
    archived: typeof object.archived === 'boolean' ? object.archived : undefined
  }
}

function classifyEntity(entity: string, brief: EntityBrief): { section: SectionKey; warning?: string } {
  switch (entity) {
    case 'note':
      return {
        section:
          brief.noteKind === 'paper_note' || brief.noteKind === 'overview' ? 'literature' : 'theory'
      }
    case 'db_record':
      return { section: 'theory' }
    case 'paper':
      return { section: 'literature' }
    case 'idea':
    case 'evidence':
      return { section: 'idea' }
    case 'experiment':
    case 'run':
    case 'snapshot':
      return { section: 'experiment' }
    case 'task':
    case 'weekly_report':
    case 'project':
      return { section: 'work' }
    default:
      return {
        section: 'work',
        warning: `未识别的实体类型 ${JSON.stringify(entity)}，已归入「工作内容」`
      }
  }
}

function itemKind(eventType: string): string {
  switch (eventType) {
    case 'entity.created':
      return 'created'
    case 'entity.deleted':
      return 'deleted'
    case 'entity.restored':
      return 'restored'
    case 'status.changed':
      return 'status'
    case 'note.updated':
    case 'entity.updated':
      return 'updated'
    case 'ai.analysis_completed':
      return 'ai_done'
    case 'relation.added':
    case 'relation.removed':
      return 'relation'
    case 'paper.imported':
    case 'paper.overwrite_confirmed':
    case 'paper.metadata_updated':
      return 'import'
    case 'report.generated':
      return 'report'
    case 'project.exported':
      return 'export'
    default:
      return 'state'
  }
}

function defaultSummary(row: StoredActivity): string {
  switch (row.eventType) {
    case 'entity.created':
      return `创建 ${row.entityType}`
    case 'entity.deleted':
      return '移入回收站'
    case 'entity.restored':
      return '已从回收站恢复'
    case 'status.changed':
      return '状态变更'
    case 'note.updated':
      return '更新笔记'
    case 'ai.analysis_completed':
      return 'AI 分析完成'
    case 'relation.added':
      return '新增关联'
    case 'relation.removed':
      return '解除关联'
    default:
      return ''
  }
}

function normalizeStatus(row: StoredActivity): { old: string; new: string } {
  if (row.eventType !== 'status.changed' && row.eventType !== 'paper.metadata_updated') {
    return { old: '', new: '' }
  }
  if (row.entityType === 'task') {
    return { old: taskStatusText(row.old), new: taskStatusText(row.new) }
  }
  return {
    old: typeof row.old?.status === 'string' ? row.old.status : '',
    new: typeof row.new?.status === 'string' ? row.new.status : ''
  }
}

function taskStatusText(object: Record<string, unknown> | undefined): string {
  if (object === undefined) return ''
  const shape = taskShape(object)
  const parts: string[] = []
  if (shape.column !== '') parts.push(`列：${shape.column}`)
  if (typeof object.is_done === 'boolean') parts.push(shape.isDone ? '已完成' : '未完成')
  if (shape.blocked !== undefined) parts.push(shape.blocked ? '受阻' : '未受阻')
  if (shape.archived !== undefined) parts.push(shape.archived ? '已归档' : '未归档')
  return parts.join('，')
}

/**
 * Keeps at most three consecutive status transitions per entity and folds the
 * rest into one summary item, preserving the first dropped position.
 */
function collapseStatuses(items: DigestItem[]): DigestItem[] {
  const maxRun = 3
  const out: DigestItem[] = []
  let index = 0
  while (index < items.length) {
    const current = items[index]
    if (current === undefined) break
    if (current.kind !== 'status') {
      out.push(current)
      index += 1
      continue
    }
    let end = index
    while (
      end < items.length &&
      items[end]?.kind === 'status' &&
      items[end]?.ref.type === current.ref.type &&
      items[end]?.ref.id === current.ref.id
    ) {
      end += 1
    }
    const run = items.slice(index, end)
    if (run.length <= maxRun) {
      out.push(...run)
      index = end
      continue
    }
    out.push(...run.slice(0, maxRun))
    const folded = run[maxRun]
    if (folded !== undefined) {
      out.push({
        ...folded,
        summary: truncateRunes(`[状态变更折叠] ${folded.summary}`, MAX_SUMMARY_RUNES),
        detail: `另有 ${run.length - maxRun} 次状态变更`,
        eventIds: run.slice(maxRun).flatMap((item) => item.eventIds),
        lastAt: run[run.length - 1]?.lastAt ?? folded.lastAt
      })
    }
    index = end
  }
  return out
}

function relationText(row: StoredActivity, prefix: string): string {
  const object = row.eventType === 'relation.removed' ? row.old : row.new
  if (object === undefined) return prefix
  const fields = ['src_type', 'src_id', 'relation', 'dst_type', 'dst_id'].map((key) =>
    typeof object[key] === 'string' ? (object[key] as string) : ''
  )
  const [srcType, srcId, relation, dstType, dstId] = fields
  return `${prefix}：${srcType}/${srcId} —${relation}→ ${dstType}/${dstId}`
}

/* -------------------------------------------------------------------- refs */

function collectRefs(rows: StoredActivity[]): EntityRef[] {
  const seen = new Set<string>()
  const out: EntityRef[] = []
  const add = (ref: EntityRef): void => {
    if (ref.type === '' || ref.id === '') return
    const key = `${ref.type}:${ref.id}`
    if (seen.has(key)) return
    seen.add(key)
    out.push(ref)
  }
  for (const row of rows) {
    add({ type: row.entityType, id: row.entityId })
    if (row.eventType === 'relation.added' || row.eventType === 'relation.removed') {
      const object = row.new ?? row.old
      if (object !== undefined) {
        add({ type: String(object.src_type ?? ''), id: String(object.src_id ?? '') })
        add({ type: String(object.dst_type ?? ''), id: String(object.dst_id ?? '') })
      }
    }
    if (row.eventType === 'ai.analysis_completed' && row.new !== undefined) {
      add({ type: String(row.new.target_type ?? ''), id: String(row.new.target_id ?? '') })
    }
  }
  return out
}

/* ----------------------------------------------------------------- briefs */

function resolveBriefs(context: DigestContext, refs: EntityRef[]): Map<string, EntityBrief> {
  const out = new Map<string, EntityBrief>()
  const groups = new Map<string, string[]>()
  for (const ref of refs) {
    const list = groups.get(ref.type) ?? []
    list.push(ref.id)
    groups.set(ref.type, list)
  }
  const db = context.db
  for (const [type, ids] of groups) {
    const placeholders = ids.map(() => '?').join(',')
    switch (type) {
      case 'note': {
        const rows = db
          .prepare(`SELECT id, title, kind, file_path, deleted_at FROM notes WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; title: string; kind: string; file_path: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`note:${row.id}`, {
            title: row.title,
            noteKind: row.kind,
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: readNoteExcerpt(context, row.file_path)
          })
        }
        break
      }
      case 'db_record': {
        const rows = db
          .prepare(`SELECT id, title, deleted_at FROM db_records WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; title: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`db_record:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: ''
          })
        }
        break
      }
      case 'idea': {
        const rows = db
          .prepare(`SELECT id, title, description, status, deleted_at FROM ideas WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; title: string; description: string; status: string; deleted_at: string | null }>
        for (const row of rows) {
          const statement = row.description.trim()
          out.set(`idea:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: statement === '' ? `状态：${row.status}` : `状态：${row.status}；${statement}`
          })
        }
        const evidence = db
          .prepare(
            `SELECT idea_id, direction, COUNT(*) AS n FROM evidence
              WHERE deleted_at IS NULL AND idea_id IN (${placeholders})
              GROUP BY idea_id, direction`
          )
          .all(...ids) as Array<{ idea_id: string; direction: string; n: number }>
        const balance = new Map<string, { support: number; oppose: number }>()
        for (const row of evidence) {
          const entry = balance.get(row.idea_id) ?? { support: 0, oppose: 0 }
          if (row.direction === 'oppose') entry.oppose += row.n
          else entry.support += row.n
          balance.set(row.idea_id, entry)
        }
        for (const [id, entry] of balance) {
          const brief = out.get(`idea:${id}`)
          if (brief === undefined) continue
          brief.abstract += `；证据 ${entry.support} 条支持 / ${entry.oppose} 条反驳`
        }
        break
      }
      case 'evidence': {
        const rows = db
          .prepare(
            `SELECT id, substr(content,1,80), content, direction, deleted_at, ai_generated, confirmed
               FROM evidence WHERE id IN (${placeholders})`
          )
          .all(...ids) as Array<{
          id: string
          content: string
          direction: string
          deleted_at: string | null
          ai_generated: number
          confirmed: number
        }>
        for (const row of rows) {
          out.set(`evidence:${row.id}`, {
            title: row.content,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: row.ai_generated === 1 && row.confirmed === 0,
            abstract: `${row.direction === 'oppose' ? '反驳：' : '支持：'}${row.content}`
          })
        }
        break
      }
      case 'experiment': {
        const rows = db
          .prepare(`SELECT id, title, purpose, deleted_at FROM experiments WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; title: string; purpose: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`experiment:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: row.purpose
          })
        }
        break
      }
      case 'run': {
        const rows = db
          .prepare(
            `SELECT id, COALESCE(NULLIF(TRIM(title), ''), 'Run ' || substr(id, 1, 8)) AS title,
                    status, deleted_at
               FROM runs WHERE id IN (${placeholders})`
          )
          .all(...ids) as Array<{ id: string; title: string; status: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`run:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: `运行状态：${row.status}`
          })
        }
        break
      }
      case 'snapshot': {
        const rows = db
          .prepare(
            `SELECT s.id AS id, s.ai_output_id AS ai_output_id, o.status AS status
               FROM snapshots s LEFT JOIN ai_outputs o ON o.id = s.ai_output_id
              WHERE s.id IN (${placeholders})`
          )
          .all(...ids) as Array<{ id: string; ai_output_id: string | null; status: string | null }>
        for (const row of rows) {
          out.set(`snapshot:${row.id}`, {
            title: row.id,
            noteKind: '',
            broken: false,
            aiContentUnconfirmed: row.ai_output_id !== null && row.status === 'draft',
            abstract: ''
          })
        }
        break
      }
      case 'task': {
        const rows = db
          .prepare(
            `SELECT id, title, description_md, deleted_at FROM tasks WHERE id IN (${placeholders})`
          )
          .all(...ids) as Array<{ id: string; title: string; description_md: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`task:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: row.description_md
          })
        }
        break
      }
      case 'weekly_report': {
        const rows = db
          .prepare(
            `SELECT id, period_start || ' ~ ' || period_end AS title, deleted_at
               FROM weekly_reports WHERE id IN (${placeholders})`
          )
          .all(...ids) as Array<{ id: string; title: string; deleted_at: string | null }>
        for (const row of rows) {
          out.set(`weekly_report:${row.id}`, {
            title: row.title,
            noteKind: '',
            broken: row.deleted_at !== null,
            aiContentUnconfirmed: false,
            abstract: ''
          })
        }
        break
      }
      case 'paper': {
        const rows = context.globalDb
          .prepare(`SELECT id, metadata, file_state FROM papers WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; metadata: string; file_state: string }>
        for (const row of rows) {
          out.set(`paper:${row.id}`, {
            title: jsonTitle(row.metadata),
            noteKind: '',
            broken: row.file_state === 'missing',
            aiContentUnconfirmed: false,
            abstract: jsonAbstract(row.metadata)
          })
        }
        break
      }
      case 'project': {
        const rows = context.globalDb
          .prepare(`SELECT id, name FROM projects WHERE id IN (${placeholders})`)
          .all(...ids) as Array<{ id: string; name: string }>
        for (const row of rows) {
          out.set(`project:${row.id}`, {
            title: row.name,
            noteKind: '',
            broken: false,
            aiContentUnconfirmed: false,
            abstract: ''
          })
        }
        break
      }
      default:
        break
    }
  }
  for (const brief of out.values()) {
    brief.abstract = truncateRunes(brief.abstract.trim(), MAX_BRIEF_RUNES)
  }
  return out
}

/**
 * Bounded, frontmatter-free preview of a note body. The body lives in the
 * workspace file, not the row; an unreadable body yields "" and never fails
 * the digest.
 */
function readNoteExcerpt(context: DigestContext, relativeFile: string): string {
  if (relativeFile === '') return ''
  let raw: string
  try {
    raw = readFileSync(path.join(context.root, ...relativeFile.split('/')), 'utf8')
  } catch {
    return ''
  }
  return compactExcerpt(stripFrontMatter(raw), MAX_BRIEF_RUNES)
}

function stripFrontMatter(raw: string): string {
  const normalized = raw.replaceAll('\r\n', '\n')
  if (!normalized.startsWith('---\n')) return normalized
  const closing = normalized.indexOf('\n---\n', 4)
  if (closing < 0) return normalized
  return normalized.slice(closing + 5)
}

function compactExcerpt(text: string, limit: number): string {
  const lines: string[] = []
  for (const line of text.split('\n')) {
    const compact = line.trim().split(/\s+/).join(' ')
    if (compact !== '') lines.push(compact)
  }
  return truncateRunes(lines.join(' / '), limit)
}

function jsonTitle(metadata: string): string {
  const parsed = safeJson<Record<string, unknown>>(metadata)
  if (parsed === undefined) return ''
  for (const key of ['title', 'Title', 'name']) {
    const value = parsed[key]
    if (typeof value === 'string' && value !== '') return value
  }
  return ''
}

function jsonAbstract(metadata: string): string {
  const parsed = safeJson<Record<string, unknown>>(metadata)
  if (parsed === undefined) return ''
  for (const key of ['abstractNote', 'abstract', 'Abstract']) {
    const value = parsed[key]
    if (typeof value === 'string' && value.trim() !== '') return value
  }
  return ''
}

/* --------------------------------------------------------- tasks (blocked) */

function readBlockedTasks(db: Db): BlockedTask[] {
  const rows = db
    .prepare(
      `SELECT t.id, t.title, t."column" AS column_name, COALESCE(c.is_done, 0) AS is_done
         FROM tasks t LEFT JOIN task_columns c ON c.name = t."column"
        WHERE t.blocked = 1 AND t.deleted_at IS NULL AND t.archived_at IS NULL
        ORDER BY t.deadline IS NULL, t.deadline, t.id`
    )
    .all() as Array<{ id: string; title: string; column_name: string; is_done: number }>
  return rows.map((row) => ({
    ref: { type: 'task', id: row.id },
    title: row.title,
    columnName: row.column_name,
    isDone: row.is_done !== 0,
    reason: ''
  }))
}

function readPlannedTasks(
  db: Db,
  period: WeeklyPeriod,
  nextISO: string,
  currentISO: string
): PlannedTask[] {
  const next = followingPeriod(period)
  const rows = db
    .prepare(
      `SELECT t.id, t.title, t."column" AS column_name, COALESCE(c.is_done, 0) AS is_done,
              t.deadline, t.week_plan
         FROM tasks t LEFT JOIN task_columns c ON c.name = t."column"
        WHERE t.deleted_at IS NULL AND t.archived_at IS NULL
          AND COALESCE(c.is_done, 0) = 0 AND t.completed_at IS NULL
          AND (t.week_plan = ?
               OR (t.week_plan IS NULL AND t.deadline IS NOT NULL
                   AND t.deadline >= ? AND t.deadline < ?)
               OR t.week_plan = ?)
        ORDER BY t.id`
    )
    .all(nextISO, next.startDate, next.endExclusiveDate, currentISO) as Array<{
    id: string
    title: string
    column_name: string
    is_done: number
    deadline: string | null
    week_plan: string | null
  }>

  return rows.map((row) => {
    let origin: PlannedOrigin
    if (row.week_plan === nextISO) origin = 'week_plan'
    else if (
      row.week_plan === null &&
      row.deadline !== null &&
      row.deadline >= next.startDate &&
      row.deadline < next.endExclusiveDate
    ) {
      origin = 'deadline'
    } else if (row.week_plan === currentISO) origin = 'carry_over'
    else origin = 'manual'
    const task: PlannedTask = {
      ref: { type: 'task', id: row.id },
      title: row.title,
      columnName: row.column_name,
      isDone: row.is_done !== 0,
      origin
    }
    if (row.deadline !== null) task.deadline = row.deadline
    if (row.week_plan !== null) task.weekPlan = row.week_plan
    return task
  })
}

/* -------------------------------------------------------------------- hash */

function stableStringify(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`
  const object = value as Record<string, unknown>
  const keys = Object.keys(object).sort()
  return `{${keys.map((key) => `${JSON.stringify(key)}:${stableStringify(object[key])}`).join(',')}}`
}

/** `sha256:` + canonical digest bytes. */
export function digestHash(digest: ActivityDigest): string {
  const canonical = {
    period_start: digest.period.startDate,
    period_end: periodEndDate(digest.period),
    timezone: digest.timeZone,
    sections: digest.sections.map((section) => ({
      key: section.key,
      items: section.items.map((item) => ({
        type: item.ref.type,
        id: item.ref.id,
        kind: item.kind,
        state: item.state,
        summary: item.summary,
        detail: item.detail,
        old: item.old,
        new: item.new,
        actor: item.actor,
        first_at: item.firstAt,
        last_at: item.lastAt,
        event_ids: item.eventIds,
        unconfirmed_source: item.unconfirmedSource,
        broken: item.broken
      }))
    })),
    blocked: digest.blocked.map((task) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      column_name: task.columnName,
      is_done: task.isDone,
      reason: task.reason
    })),
    planned: digest.planned.map((task) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      column_name: task.columnName,
      is_done: task.isDone,
      deadline: task.deadline ?? '',
      week_plan: task.weekPlan ?? '',
      origin: task.origin
    })),
    counts_by_type: digest.stats.countsByType,
    truncated: digest.truncated,
    warnings: digest.warnings
  }
  return `sha256:${createHash('sha256').update(stableStringify(canonical)).digest('hex')}`
}

export function totalEvents(counts: Record<string, number>): number {
  return Object.values(counts).reduce((sum, value) => sum + value, 0)
}

export function digestRefs(digest: ActivityDigest, limit = MAX_REFS): EntityRef[] {
  const seen = new Set<string>()
  const out: EntityRef[] = []
  for (const section of digest.sections) {
    for (const item of section.items) {
      const key = `${item.ref.type}:${item.ref.id}`
      if (seen.has(key)) continue
      seen.add(key)
      out.push(item.ref)
      if (out.length >= limit) return out
    }
  }
  return out
}

export function coverageOf(digest: ActivityDigest): Coverage {
  return {
    period_start: digest.period.startDate,
    period_end: periodEndDate(digest.period),
    timezone: digest.timeZone,
    event_count: totalEvents(digest.stats.countsByType),
    counts_by_type: digest.stats.countsByType,
    counts_by_section: Object.fromEntries(
      Object.entries(digest.stats.countsBySection).map(([key, value]) => [key, value ?? 0])
    ),
    truncated: digest.truncated,
    digest_hash: digest.digestHash,
    computed_at: digest.collectedAt
  }
}

/** Sections whose facts are empty (the work section always runs). */
export function sectionIsEmpty(digest: ActivityDigest, key: SectionKey): boolean {
  if (key === 'work') return false
  if ((digest.sections.find((section) => section.key === key)?.items.length ?? 0) > 0) return false
  if (key === 'problem') return digest.blocked.length === 0
  if (key === 'next_week') return digest.planned.length === 0
  return true
}

export function sectionOf(digest: ActivityDigest, key: SectionKey): DigestSection | undefined {
  return digest.sections.find((section) => section.key === key)
}
