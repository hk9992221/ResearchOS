import { formatTimestamp } from '../../clock'
import { newId } from '../../ids'
import type { Actor } from '../../schema'
import { emitNoteUpdated } from './activity'
import { rewriteNoteMeta } from './notefile'
import { invalid, notFound, type NotesContext } from './support'

function formatNow(ctx: NotesContext): string {
  return formatTimestamp(ctx.clock.now())
}

export interface Tag {
  id: string
  name: string
  color: string
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

interface TagRow {
  id: string
  name: string
  color: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
}

function mapTag(row: TagRow): Tag {
  return {
    id: row.id,
    name: row.name,
    color: row.color ?? '',
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    deletedAt: row.deleted_at
  }
}

export function validateTagName(name: string): string {
  const trimmed = name.trim()
  if (trimmed === '') throw invalid('tag name must not be empty', { field: 'name' })
  if (Array.from(trimmed).length > 64) throw invalid('tag name must be at most 64 characters', { field: 'name' })
  for (const character of trimmed) {
    if (/\p{Cc}/u.test(character)) throw invalid('tag name contains control characters', { field: 'name' })
  }
  return trimmed
}

export function validateTagColor(color: string): string {
  const trimmed = color.trim()
  if (trimmed === '') return ''
  if (trimmed.startsWith('#')) {
    if (!/^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/u.test(trimmed)) {
      throw invalid(`color ${JSON.stringify(color)} is not #RGB or #RRGGBB`, { field: 'color' })
    }
    return trimmed
  }
  if (!/^[A-Za-z]{1,32}$/u.test(trimmed)) {
    throw invalid(`color ${JSON.stringify(color)} is not a hex code or letter preset`, { field: 'color' })
  }
  return trimmed
}

export function listTags(ctx: NotesContext, includeDeleted = false): Tag[] {
  const rows = ctx.db
    .prepare(
      `SELECT id, name, color, created_at, updated_at, deleted_at FROM tags
        ${includeDeleted ? '' : 'WHERE deleted_at IS NULL'}`
    )
    .all() as TagRow[]
  return rows
    .map(mapTag)
    .sort((left, right) => {
      const leftName = left.name.toLowerCase()
      const rightName = right.name.toLowerCase()
      if (leftName !== rightName) return leftName < rightName ? -1 : 1
      return left.id < right.id ? -1 : 1
    })
}

export function getTag(ctx: NotesContext, tagId: string): Tag | null {
  const row = ctx.db
    .prepare(`SELECT id, name, color, created_at, updated_at, deleted_at FROM tags WHERE id = ?`)
    .get(tagId) as TagRow | undefined
  return row === undefined ? null : mapTag(row)
}

export function requireLiveTag(ctx: NotesContext, tagId: string): Tag {
  const tag = getTag(ctx, tagId)
  if (tag === null) throw invalid(`tag ${tagId} does not exist or was deleted`)
  if (tag.deletedAt !== null) throw invalid(`tag ${tagId} does not exist or was deleted`)
  return tag
}

/** Creates a tag; an existing live tag with the same folded name is reused. */
export function createTag(ctx: NotesContext, name: string, color = ''): Tag {
  const validatedName = validateTagName(name)
  const validatedColor = validateTagColor(color)
  const existing = listTags(ctx).find((tag) => tag.name.toLowerCase() === validatedName.toLowerCase())
  if (existing !== undefined) return existing
  const id = newId(ctx.clock)
  const now = formatNow(ctx)
  ctx.db
    .prepare(`INSERT INTO tags(id, name, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`)
    .run(id, validatedName, validatedColor, now, now)
  const created = getTag(ctx, id)
  if (created === null) throw new Error('tag insert did not produce a row')
  return created
}

export function renameTag(ctx: NotesContext, tagId: string, name: string, color = ''): Tag {
  const tag = getTag(ctx, tagId)
  if (tag === null) throw invalid(`tag ${tagId} does not exist or was deleted`)
  if (tag.deletedAt !== null) throw invalid(`tag ${tagId} is deleted`)
  const validatedName = validateTagName(name)
  const validatedColor = color === '' ? tag.color : validateTagColor(color)
  const clash = listTags(ctx).find(
    (other) => other.id !== tagId && other.name.toLowerCase() === validatedName.toLowerCase()
  )
  if (clash !== undefined) throw invalid(`tag name ${JSON.stringify(validatedName)} already exists`)
  ctx.db
    .prepare(`UPDATE tags SET name = ?, color = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`)
    .run(validatedName, validatedColor, formatNow(ctx), tagId)
  const updated = getTag(ctx, tagId)
  if (updated === null) throw notFound(`tag ${tagId} not found`)
  return updated
}

export function softDeleteTag(ctx: NotesContext, tagId: string): void {
  const tag = getTag(ctx, tagId)
  if (tag === null) throw invalid(`tag ${tagId} does not exist`)
  if (tag.deletedAt !== null) return
  const now = formatNow(ctx)
  ctx.db.prepare(`UPDATE tags SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`).run(now, now, tagId)
}

export function restoreTag(ctx: NotesContext, tagId: string): void {
  const tag = getTag(ctx, tagId)
  if (tag === null) throw invalid(`tag ${tagId} does not exist`)
  if (tag.deletedAt === null) return
  const clash = listTags(ctx).find(
    (other) => other.id !== tagId && other.name.toLowerCase() === tag.name.toLowerCase()
  )
  if (clash !== undefined) throw invalid(`live tag ${JSON.stringify(clash.name)} already uses the name`)
  ctx.db.prepare(`UPDATE tags SET deleted_at = NULL, updated_at = ? WHERE id = ?`).run(formatNow(ctx), tagId)
}

/* --------------------------------------------------------------- refs */

export type TagTargetType = 'note' | 'db_record'

function targetNoteId(ctx: NotesContext, type: TagTargetType, id: string): string | null {
  if (type === 'note') return id
  const row = ctx.db.prepare(`SELECT note_id FROM db_records WHERE id = ?`).get(id) as
    | { note_id: string | null }
    | undefined
  return row?.note_id ?? null
}

function assertTargetLive(ctx: NotesContext, type: TagTargetType, id: string): void {
  const table = type === 'note' ? 'notes' : 'db_records'
  const row = ctx.db.prepare(`SELECT deleted_at FROM ${table} WHERE id = ?`).get(id) as
    | { deleted_at: string | null }
    | undefined
  if (row === undefined) throw invalid(`no such ${type} ${id}`)
  if (row.deleted_at !== null) throw invalid(`${type} ${id} is deleted`)
}

function resolveTagByNameOrId(ctx: NotesContext, value: string): Tag {
  const trimmed = value.trim()
  if (trimmed === '') throw invalid('tag must not be empty')
  const byId = getTag(ctx, trimmed)
  if (byId !== null) {
    if (byId.deletedAt !== null) throw invalid(`tag ${trimmed} is deleted`)
    return byId
  }
  return createTag(ctx, trimmed)
}

/**
 * Adds names/ids to a note or record. Adding an already assigned tag is a
 * no-op; every actual change emits one merged note.updated and refreshes the
 * note frontmatter tags.
 */
export function addTags(
  ctx: NotesContext,
  type: TagTargetType,
  entityId: string,
  tags: string[],
  actor: Actor = 'user'
): string[] {
  assertTargetLive(ctx, type, entityId)
  let changed = false
  for (const value of tags) {
    const tag = resolveTagByNameOrId(ctx, value)
    const result = ctx.db
      .prepare(
        `INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at)
         VALUES (?, ?, ?, ?) ON CONFLICT(tag_id, entity_type, entity_id) DO NOTHING`
      )
      .run(tag.id, type, entityId, formatNow(ctx))
    if (result.changes > 0) changed = true
  }
  if (changed) {
    if (type === 'db_record') syncRecordTagsData(ctx, entityId)
    const noteId = targetNoteId(ctx, type, entityId)
    if (noteId !== null) {
      if (type === 'note') rewriteNoteMeta(ctx, noteId)
      emitNoteUpdated(ctx, { noteId, summary: '更新标签', actor, at: ctx.clock.now() })
    }
  }
  return tagNamesFor(ctx, type, entityId)
}

export function removeTags(
  ctx: NotesContext,
  type: TagTargetType,
  entityId: string,
  tags: string[],
  actor: Actor = 'user'
): string[] {
  assertTargetLive(ctx, type, entityId)
  let changed = false
  for (const value of tags) {
    const tag = resolveExistingTag(ctx, value)
    if (tag === null) continue
    const result = ctx.db
      .prepare(`DELETE FROM entity_tags WHERE tag_id = ? AND entity_type = ? AND entity_id = ?`)
      .run(tag.id, type, entityId)
    if (result.changes > 0) changed = true
  }
  if (changed) {
    if (type === 'db_record') syncRecordTagsData(ctx, entityId)
    const noteId = targetNoteId(ctx, type, entityId)
    if (noteId !== null) {
      if (type === 'note') rewriteNoteMeta(ctx, noteId)
      emitNoteUpdated(ctx, { noteId, summary: '更新标签', actor, at: ctx.clock.now() })
    }
  }
  return tagNamesFor(ctx, type, entityId)
}

function resolveExistingTag(ctx: NotesContext, value: string): Tag | null {
  const byId = getTag(ctx, value.trim())
  if (byId !== null) return byId.deletedAt === null ? byId : null
  return (
    listTags(ctx).find((tag) => tag.name.toLowerCase() === value.trim().toLowerCase()) ?? null
  )
}

export function tagNamesFor(ctx: NotesContext, type: TagTargetType, entityId: string): string[] {
  const rows = ctx.db
    .prepare(
      `SELECT t.name FROM entity_tags et JOIN tags t ON t.id = et.tag_id
        WHERE et.entity_type = ? AND et.entity_id = ? AND t.deleted_at IS NULL
        ORDER BY t.name`
    )
    .all(type, entityId) as Array<{ name: string }>
  return rows.map((row) => row.name)
}

/** Replaces a record's tag set by tag ids (used by the tags field type). */
export function setRecordTagIds(ctx: NotesContext, recordId: string, tagIds: string[], now: string): void {
  const unique: string[] = []
  for (const tagId of tagIds) {
    requireLiveTag(ctx, tagId)
    if (!unique.includes(tagId)) unique.push(tagId)
  }
  ctx.db.prepare(`DELETE FROM entity_tags WHERE entity_type = 'db_record' AND entity_id = ?`).run(recordId)
  for (const tagId of unique) {
    ctx.db
      .prepare(`INSERT INTO entity_tags(tag_id, entity_type, entity_id, created_at) VALUES (?, 'db_record', ?, ?)`)
      .run(tagId, recordId, now)
  }
  syncRecordTagsData(ctx, recordId)
}

/**
 * Mirrors entity_tags into every tags-typed field of the record so filters,
 * sorts and table cells agree with the tag dictionary.
 */
export function syncRecordTagsData(ctx: NotesContext, recordId: string): void {
  const record = ctx.db.prepare(`SELECT database_id, data FROM db_records WHERE id = ?`).get(recordId) as
    | { database_id: string; data: string }
    | undefined
  if (record === undefined) return
  const fields = ctx.db
    .prepare(`SELECT id FROM db_fields WHERE database_id = ? AND type = 'tags' AND deleted_at IS NULL`)
    .all(record.database_id) as Array<{ id: string }>
  if (fields.length === 0) return
  const tagRows = ctx.db
    .prepare(`SELECT tag_id FROM entity_tags WHERE entity_type = 'db_record' AND entity_id = ? ORDER BY tag_id`)
    .all(recordId) as Array<{ tag_id: string }>
  const tagIds = tagRows.map((row) => row.tag_id)
  const values: Record<string, unknown> = {}
  try {
    const parsed: unknown = JSON.parse(record.data === '' ? '{}' : record.data)
    if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
      Object.assign(values, parsed)
    }
  } catch {
    // Corrupt data is replaced by the projected tag arrays only.
  }
  for (const field of fields) values[field.id] = tagIds
  ctx.db
    .prepare(`UPDATE db_records SET data = ?, updated_at = ? WHERE id = ?`)
    .run(JSON.stringify(values), formatNow(ctx), recordId)
}