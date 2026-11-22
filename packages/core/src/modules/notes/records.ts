import { rmSync } from 'node:fs'

import type { DbRecord, FilterNode, SortKey } from '@researchos/shared'
import { isUuid } from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import { newId } from '../../ids'
import type { Actor, EntityType, Relation } from '../../schema'
import { emitEvent, emitNoteUpdated, linkRelation, restoreEntity, softDelete, unlinkRelation } from './activity'
import {
  createNoteFile,
  loadNoteRow,
  rewriteNoteMeta,
  type NoteRow
} from './notefile'
import {
  checkPermutation,
  listFieldRows,
  loadFieldRow,
  loadTitleField,
  parseFieldConfig,
  type FieldRow
} from './database'
import {
  compileFilter,
  compileSorts,
  matchesFilter,
  sortRows,
  type SortRow
} from './filter'
import { requireLiveTag } from './tags'
import {
  conflict,
  entityDeleted,
  invalid,
  isUniqueViolation,
  notFound,
  parseJsonObject,
  type Json,
  type NotesContext
} from './support'

export interface RecordListQuery {
  databaseId: string
  q?: string
  filter?: FilterNode
  sorts?: SortKey[]
  limit?: number
  offset?: number
  includeDeleted?: boolean
}

export interface RecordCreateRequest {
  databaseId: string
  values?: Json
  title?: string
  noteId?: string
  actor?: Actor
}

export interface RecordUpdateRequest {
  recordId: string
  values: Json
  title?: string
  expectedUpdatedAt?: string
  actor?: Actor
}

interface RecordRow {
  id: string
  databaseId: string
  noteId: string | null
  title: string
  data: string
  sortOrder: number
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

const RECORD_COLUMNS = `id, database_id, note_id, title, data, sort_order, created_at, updated_at, deleted_at`

function mapRecordRow(row: Record<string, unknown>): RecordRow {
  return {
    id: String(row.id),
    databaseId: String(row.database_id),
    noteId: row.note_id === null ? null : String(row.note_id),
    title: String(row.title ?? ''),
    data: String(row.data ?? '{}'),
    sortOrder: Number(row.sort_order),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    deletedAt: row.deleted_at === null ? null : String(row.deleted_at)
  }
}

export function recordToDto(row: RecordRow): DbRecord {
  return {
    id: row.id,
    databaseId: row.databaseId,
    noteId: row.noteId,
    title: row.title,
    data: parseJsonObject(row.data),
    sortOrder: row.sortOrder,
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
    deletedAt: row.deletedAt
  }
}

export function loadRecordRow(ctx: NotesContext, recordId: string): RecordRow | null {
  const row = ctx.db.prepare(`SELECT ${RECORD_COLUMNS} FROM db_records WHERE id = ?`).get(recordId) as
    | Record<string, unknown>
    | undefined
  return row === undefined ? null : mapRecordRow(row)
}

export function getRecord(ctx: NotesContext, recordId: string): DbRecord {
  const row = loadRecordRow(ctx, recordId)
  if (row === null) throw notFound(`record ${recordId} not found`)
  return recordToDto(row)
}

function requireLiveRecord(ctx: NotesContext, recordId: string): RecordRow {
  const row = loadRecordRow(ctx, recordId)
  if (row === null) throw notFound(`record ${recordId} not found`)
  if (row.deletedAt !== null) throw entityDeleted('db_record', recordId)
  return row
}

function requireLiveDatabase(ctx: NotesContext, databaseId: string): void {
  const row = ctx.db.prepare(`SELECT deleted_at FROM research_databases WHERE id = ?`).get(databaseId) as
    | { deleted_at: string | null }
    | undefined
  if (row === undefined) throw notFound(`database ${databaseId} not found`)
  if (row.deleted_at !== null) throw entityDeleted('research_database', databaseId)
}

export function listRecords(ctx: NotesContext, query: RecordListQuery): {
  items: DbRecord[]
  total: number
  limit: number
  offset: number
} {
  requireLiveDatabase(ctx, query.databaseId)
  const fields = listFieldRows(ctx, query.databaseId)
  const fieldMap = new Map(fields.map((field) => [field.id, field]))
  const predicate = compileFilter(fieldMap, query.filter)
  const specs = compileSorts(fieldMap, query.sorts)
  const rows = ctx.db
    .prepare(
      `SELECT ${RECORD_COLUMNS} FROM db_records WHERE database_id = ?
        ${query.includeDeleted === true ? '' : 'AND deleted_at IS NULL'} ORDER BY sort_order, id`
    )
    .all(query.databaseId) as Array<Record<string, unknown>>
  const needle = query.q?.trim().toLowerCase() ?? ''
  const candidates: Array<{ record: RecordRow; sort: SortRow }> = []
  for (const raw of rows) {
    const record = mapRecordRow(raw)
    if (needle !== '' && !record.title.toLowerCase().includes(needle)) continue
    const values = parseJsonObject(record.data)
    if (!matchesFilter(predicate, values)) continue
    candidates.push({
      record,
      sort: {
        id: record.id,
        title: record.title,
        createdAt: record.createdAt,
        updatedAt: record.updatedAt,
        values
      }
    })
  }
  const ordered = specs.length === 0 ? candidates.map((entry) => entry.record) : sortRows(candidates.map((entry) => entry.sort), specs).map((entry) => candidates.find((candidate) => candidate.record.id === entry.id)?.record).filter((entry): entry is RecordRow => entry !== undefined)
  const total = ordered.length
  const limit = query.limit === undefined || query.limit <= 0 ? 100 : Math.min(Math.trunc(query.limit), 500)
  const offset = query.offset === undefined || query.offset < 0 ? 0 : Math.trunc(query.offset)
  const page = ordered.slice(offset, offset + limit)
  const items: DbRecord[] = page.map(recordToDto)
  return { items, total, limit, offset }
}

/* --------------------------------------------------------- value typing */

function normalizeFieldKey(fields: FieldRow[], key: string): FieldRow {
  const byId = fields.find((field) => field.id === key)
  if (byId !== undefined) return byId
  const byName = fields.find((field) => field.name.toLowerCase() === key.toLowerCase())
  if (byName !== undefined) return byName
  throw invalid(`field ${JSON.stringify(key)} does not exist or was deleted in this database`, { field: key })
}

interface RelationRef {
  type: string
  id: string
}

const RELATION_TYPES = ['paper', 'note', 'idea', 'experiment', 'db_record'] as const

function targetAlive(ctx: NotesContext, ref: RelationRef): boolean {
  if (ref.type === 'paper') {
    if (ctx.globalDb === null) return false
    const row = ctx.globalDb.prepare(`SELECT 1 AS one FROM papers WHERE id = ?`).get(ref.id)
    return row !== undefined
  }
  const table =
    ref.type === 'note' ? 'notes' : ref.type === 'db_record' ? 'db_records' : ref.type === 'idea' ? 'ideas' : ref.type === 'experiment' ? 'experiments' : null
  if (table === null) return false
  const row = ctx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(ref.id) as
    | { deleted_at: string | null }
    | undefined
  return row !== undefined && row.deleted_at === null
}

function relationRefs(value: unknown, field: FieldRow, targets: readonly string[]): RelationRef[] {
  if (!Array.isArray(value)) throw invalid(`field ${JSON.stringify(field.name)} needs an array of {type,id} targets`)
  const refs: RelationRef[] = []
  for (const entry of value) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) {
      throw invalid(`field ${JSON.stringify(field.name)} needs {type,id} targets`)
    }
    const type = 'type' in entry ? entry.type : undefined
    const id = 'id' in entry ? entry.id : undefined
    if (typeof type !== 'string' || !(RELATION_TYPES as readonly string[]).includes(type)) {
      throw invalid(`field ${JSON.stringify(field.name)} has an unsupported target type`)
    }
    if (typeof id !== 'string' || !isUuid(id)) {
      throw invalid(`field ${JSON.stringify(field.name)} has an invalid target id`)
    }
    if (!targets.includes(type)) throw invalid(`field ${JSON.stringify(field.name)} does not allow targets of type ${type}`)
    if (!refs.some((ref) => ref.type === type && ref.id === id)) refs.push({ type, id })
  }
  return refs
}

function canonicalValue(ctx: NotesContext, field: FieldRow, value: unknown): unknown {
  if (value === null) return null
  const config = parseFieldConfig(field.config)
  switch (field.type) {
    case 'text':
    case 'url':
      if (typeof value !== 'string') throw invalid(`field ${JSON.stringify(field.name)} needs a string`, { field: field.id })
      return value
    case 'number':
      if (typeof value !== 'number' || !Number.isFinite(value)) {
        throw invalid(`field ${JSON.stringify(field.name)} needs a number`, { field: field.id })
      }
      return value
    case 'date':
      if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/u.test(value) || Number.isNaN(Date.parse(`${value}T00:00:00Z`))) {
        throw invalid(`field ${JSON.stringify(field.name)} needs a YYYY-MM-DD date`, { field: field.id })
      }
      return value
    case 'checkbox':
      if (typeof value !== 'boolean') throw invalid(`field ${JSON.stringify(field.name)} needs a boolean`, { field: field.id })
      return value
    case 'select': {
      const options = config.options ?? []
      if (typeof value !== 'string' || value === '') {
        throw invalid(`field ${JSON.stringify(field.name)} needs an option id`, { field: field.id })
      }
      const match = options.find((option) => option.id === value || option.name === value)
      if (match === undefined) throw invalid(`value ${JSON.stringify(value)} is not an option of field ${JSON.stringify(field.name)}`, { field: field.id })
      return match.id
    }
    case 'multi_select': {
      if (!Array.isArray(value) || !value.every((entry) => typeof entry === 'string')) {
        throw invalid(`field ${JSON.stringify(field.name)} needs an array of option ids`, { field: field.id })
      }
      const options = config.options ?? []
      const selected: string[] = []
      for (const entry of value) {
        const match = options.find((option) => option.id === entry || option.name === entry)
        if (match === undefined) {
          throw invalid(`value ${JSON.stringify(entry)} is not an option of field ${JSON.stringify(field.name)}`, { field: field.id })
        }
        if (selected.includes(match.id)) {
          throw invalid(`field ${JSON.stringify(field.name)} contains a duplicate option`, { field: field.id })
        }
        selected.push(match.id)
      }
      return selected
    }
    case 'tags': {
      if (!Array.isArray(value) || !value.every((entry) => typeof entry === 'string')) {
        throw invalid(`field ${JSON.stringify(field.name)} needs an array of tag ids`, { field: field.id })
      }
      const tagIds: string[] = []
      for (const entry of value) {
        let tagId = entry
        try {
          tagId = requireLiveTag(ctx, entry).id
        } catch {
          const byName = ctx.db.prepare(`SELECT id FROM tags WHERE lower(name) = lower(?) AND deleted_at IS NULL`).get(entry) as
            | { id: string }
            | undefined
          if (byName === undefined) throw invalid(`field ${JSON.stringify(field.name)} references a missing tag`, { field: field.id })
          tagId = byName.id
        }
        if (tagIds.includes(tagId)) throw invalid(`field ${JSON.stringify(field.name)} contains the duplicate tag`, { field: field.id })
        tagIds.push(tagId)
      }
      return tagIds
    }
    case 'relation': {
      const refs = relationRefs(value, field, config.targets ?? [...RELATION_TYPES])
      for (const ref of refs) {
        if (!targetAlive(ctx, ref)) {
          throw invalid(`relation target ${ref.type}/${ref.id} is missing or deleted`, { field: field.id })
        }
      }
      return refs
    }
    default:
      throw invalid(`field ${JSON.stringify(field.name)} has an unsupported type`)
  }
}

interface NormalizedValues {
  byId: Map<string, unknown>
}

function normalizeValues(ctx: NotesContext, fields: FieldRow[], values: Json): NormalizedValues {
  const byId = new Map<string, unknown>()
  for (const [key, value] of Object.entries(values)) {
    const field = normalizeFieldKey(fields, key)
    byId.set(field.id, canonicalValue(ctx, field, value))
  }
  return { byId }
}

function titleValue(data: Readonly<Record<string, unknown>>, titleFieldId: string): string {
  const value = data[titleFieldId]
  return typeof value === 'string' ? value : ''
}

function relationForTarget(target: string): Relation {
  return target === 'paper' || target === 'note' ? 'references' : 'related_to'
}

function refsOf(value: unknown): RelationRef[] {
  if (!Array.isArray(value)) return []
  const refs: RelationRef[] = []
  for (const entry of value) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
    const type = 'type' in entry ? entry.type : undefined
    const id = 'id' in entry ? entry.id : undefined
    if (typeof type === 'string' && typeof id === 'string') refs.push({ type, id })
  }
  return refs
}

function applyRelationDiff(
  ctx: NotesContext,
  noteId: string,
  recordId: string,
  field: FieldRow,
  before: unknown,
  after: unknown,
  actor: Actor
): void {
  const oldRefs = refsOf(before)
  const newRefs = refsOf(after)
  for (const ref of oldRefs) {
    if (newRefs.some((entry) => entry.type === ref.type && entry.id === ref.id)) continue
    unlinkRelation(
      ctx,
      {
        srcType: 'note',
        srcId: noteId,
        relation: relationForTarget(ref.type),
        dstType: ref.type as EntityType,
        dstId: ref.id
      },
      actor
    )
  }
  for (const ref of newRefs) {
    if (oldRefs.some((entry) => entry.type === ref.type && entry.id === ref.id)) continue
    if (ref.type === 'db_record' && ref.id === recordId) throw invalid('a record cannot relate to itself', { field: field.id })
    linkRelation(
      ctx,
      {
        srcType: 'note',
        srcId: noteId,
        relation: relationForTarget(ref.type),
        dstType: ref.type as EntityType,
        dstId: ref.id
      },
      actor
    )
  }
}

function applyTagFieldDiff(ctx: NotesContext, recordId: string, before: unknown, after: unknown, now: string): void {
  const oldIds = Array.isArray(before) ? before.filter((entry): entry is string => typeof entry === 'string') : []
  const newIds = Array.isArray(after) ? after.filter((entry): entry is string => typeof entry === 'string') : []
  for (const tagId of oldIds) {
    if (newIds.includes(tagId)) continue
    ctx.db
      .prepare(`DELETE FROM entity_tags WHERE tag_id = ? AND entity_type = 'db_record' AND entity_id = ?`)
      .run(tagId, recordId)
  }
  for (const tagId of newIds) {
    if (oldIds.includes(tagId)) continue
    ctx.db
      .prepare(`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'db_record', ?, ?)`)
      .run(tagId, recordId, now)
  }
}

function syncNoteTitle(ctx: NotesContext, note: NoteRow, title: string, actor: Actor): void {
  if (note.title === title) return
  const now = formatTimestamp(ctx.clock.now())
  ctx.db.prepare(`UPDATE notes SET title = ?, updated_at = ? WHERE id = ?`).run(title, now, note.id)
  rewriteNoteMeta(ctx, note.id)
  emitNoteUpdated(ctx, { noteId: note.id, summary: '更新记录标题', actor, at: ctx.clock.now() })
}

export function createRecord(ctx: NotesContext, request: RecordCreateRequest): DbRecord {
  const actor = request.actor ?? 'user'
  requireLiveDatabase(ctx, request.databaseId)
  const fields = listFieldRows(ctx, request.databaseId)
  const titleField = loadTitleField(ctx, request.databaseId)
  if (titleField === null) throw invalid(`database ${request.databaseId} has no title field`)
  const supplied = request.values === undefined ? {} : request.values
  return ctx.inTx(() => {
    const normalized = normalizeValues(ctx, fields, supplied)
    const data = new Map<string, unknown>(normalized.byId)
    if (request.title !== undefined && !data.has(titleField.id)) data.set(titleField.id, request.title)
    const title = titleValue(Object.fromEntries(data), titleField.id)
    let note: NoteRow | null = null
    let createdFilePath: string | null = null
    if (request.noteId !== undefined && request.noteId !== '') {
      note = loadNoteRow(ctx, request.noteId)
      if (note === null) throw notFound(`note ${request.noteId} not found`)
      if (note.deletedAt !== null) throw entityDeleted('note', request.noteId)
      const existing = ctx.db
        .prepare(`SELECT id FROM db_records WHERE note_id = ? AND deleted_at IS NULL`)
        .get(request.noteId) as { id: string } | undefined
      if (existing !== undefined) throw invalid(`note ${request.noteId} already has a database record`)
      if (title === '' && note.title !== '') data.set(titleField.id, note.title)
    } else {
      const created = createNoteFile(ctx, {
        kind: 'db_record',
        title: title === '' ? '' : title,
        body: '',
        actor
      })
      createdFilePath = created.filePath
      note = loadNoteRow(ctx, created.noteId)
    }
    if (!data.has(titleField.id)) data.set(titleField.id, '')
    const dataObject = Object.fromEntries(data)
    const finalTitle = titleValue(dataObject, titleField.id)
    const recordId = newId(ctx.clock)
    const now = formatTimestamp(ctx.clock.now())
    const positionRow = ctx.db
      .prepare(`SELECT COALESCE(MAX(sort_order) + 1, 0) AS n FROM db_records WHERE database_id = ?`)
      .get(request.databaseId) as { n: number }
    try {
      ctx.db
        .prepare(
          `INSERT INTO db_records(id, database_id, note_id, title, data, sort_order, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(recordId, request.databaseId, note?.id ?? null, finalTitle, JSON.stringify(dataObject), positionRow.n, now, now)
      if (note !== null) {
        for (const field of fields) {
          if (!data.has(field.id)) continue
          if (field.type === 'relation') {
            applyRelationDiff(ctx, note.id, recordId, field, undefined, data.get(field.id), actor)
          } else if (field.type === 'tags') {
            applyTagFieldDiff(ctx, recordId, undefined, data.get(field.id), now)
          }
        }
        syncNoteTitle(ctx, note, finalTitle, actor)
      }
      emitEvent(ctx, {
        type: 'entity.created',
        entityType: 'db_record',
        entityId: recordId,
        summary: '创建记录',
        actor,
        at: ctx.clock.now()
      })
    } catch (error) {
      if (createdFilePath !== null) rmSync(ctx.paths.abs(createdFilePath), { force: true })
      if (isUniqueViolation(error)) throw invalid('note already has a database record')
      throw error
    }
    const row = loadRecordRow(ctx, recordId)
    if (row === null) throw new Error('record insert did not produce a row')
    return recordToDto(row)
  })
}

export function updateRecord(ctx: NotesContext, request: RecordUpdateRequest): DbRecord {
  const actor = request.actor ?? 'user'
  return ctx.inTx(() => {
    const record = requireLiveRecord(ctx, request.recordId)
    if (request.expectedUpdatedAt !== undefined && request.expectedUpdatedAt !== record.updatedAt) {
      throw conflict('record was modified by another writer', {
        expectedUpdatedAt: request.expectedUpdatedAt,
        currentUpdatedAt: record.updatedAt,
        current: recordToDto(record)
      })
    }
    const fields = listFieldRows(ctx, record.databaseId)
    const titleField = loadTitleField(ctx, record.databaseId)
    if (titleField === null) throw invalid(`database ${record.databaseId} has no title field`)
    const normalized = normalizeValues(ctx, fields, request.values)
    const before = parseJsonObject(record.data)
    const merged: Json = { ...before }
    for (const [fieldId, value] of normalized.byId) {
      if (value === null) delete merged[fieldId]
      else merged[fieldId] = value
    }
    if (request.title !== undefined) merged[titleField.id] = request.title
    if (!(titleField.id in merged)) merged[titleField.id] = ''
    const title = titleValue(merged, titleField.id)
    const now = formatTimestamp(ctx.clock.now())
    const sql =
      request.expectedUpdatedAt === undefined
        ? `UPDATE db_records SET data = ?, title = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
        : `UPDATE db_records SET data = ?, title = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND updated_at = ?`
    const args: unknown[] = [JSON.stringify(merged), title, now, record.id]
    if (request.expectedUpdatedAt !== undefined) args.push(request.expectedUpdatedAt)
    if (ctx.db.prepare(sql).run(...args).changes === 0) {
      const current = loadRecordRow(ctx, record.id)
      throw conflict('record was modified by another writer', {
        expectedUpdatedAt: request.expectedUpdatedAt,
        currentUpdatedAt: current?.updatedAt ?? null,
        current: current === null ? null : recordToDto(current)
      })
    }
    for (const field of fields) {
      if (!normalized.byId.has(field.id)) continue
      if (field.type === 'relation') {
        if (record.noteId === null) throw invalid(`relation field ${JSON.stringify(field.name)} needs a backing note`)
        applyRelationDiff(ctx, record.noteId, record.id, field, before[field.id], normalized.byId.get(field.id), actor)
      } else if (field.type === 'tags') {
        applyTagFieldDiff(ctx, record.id, before[field.id], normalized.byId.get(field.id), now)
      }
    }
    if (record.noteId !== null) {
      const note = loadNoteRow(ctx, record.noteId)
      if (note !== null && note.deletedAt === null) {
        syncNoteTitle(ctx, note, title, actor)
        emitNoteUpdated(ctx, { noteId: note.id, summary: '更新记录字段', actor, at: ctx.clock.now() })
      }
    } else {
      emitEvent(ctx, {
        type: 'entity.updated',
        entityType: 'db_record',
        entityId: record.id,
        summary: '更新记录字段',
        actor,
        at: ctx.clock.now()
      })
    }
    const updated = loadRecordRow(ctx, record.id)
    if (updated === null) throw notFound(`record ${record.id} not found`)
    return recordToDto(updated)
  })
}

export function deleteRecord(
  ctx: NotesContext,
  recordId: string,
  deleteNoteToo = false,
  actor: Actor = 'user'
): { deleted: boolean } {
  return ctx.inTx(() => {
    const record = requireLiveRecord(ctx, recordId)
    softDelete(ctx, 'db_record', recordId, actor)
    if (deleteNoteToo && record.noteId !== null) softDelete(ctx, 'note', record.noteId, actor)
    return { deleted: true }
  })
}

export function restoreRecord(ctx: NotesContext, recordId: string, actor: Actor = 'user'): DbRecord {
  return ctx.inTx(() => {
    const record = loadRecordRow(ctx, recordId)
    if (record === null) throw notFound(`record ${recordId} not found`)
    restoreEntity(ctx, 'db_record', recordId, actor)
    if (record.noteId !== null) {
      const note = loadNoteRow(ctx, record.noteId)
      if (note !== null && note.deletedAt !== null) restoreEntity(ctx, 'note', note.id, actor)
    }
    const restored = loadRecordRow(ctx, recordId)
    if (restored === null) throw notFound(`record ${recordId} not found`)
    return recordToDto(restored)
  })
}

export function reorderRecords(ctx: NotesContext, databaseId: string, orderedRecordIds: string[]): DbRecord[] {
  requireLiveDatabase(ctx, databaseId)
  const rows = ctx.db
    .prepare(`SELECT id FROM db_records WHERE database_id = ? AND deleted_at IS NULL ORDER BY sort_order, id`)
    .all(databaseId) as Array<{ id: string }>
  checkPermutation('records', orderedRecordIds, rows.map((row) => row.id))
  return ctx.inTx(() => {
    const now = formatTimestamp(ctx.clock.now())
    for (const [position, recordId] of orderedRecordIds.entries()) {
      ctx.db
        .prepare(`UPDATE db_records SET sort_order = ?, updated_at = ? WHERE id = ? AND database_id = ?`)
        .run(position, now, recordId, databaseId)
    }
    return listRecords(ctx, { databaseId, limit: 500 }).items
  })
}

