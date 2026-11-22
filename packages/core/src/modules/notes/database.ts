import type { DbField, DbFieldType, ResearchDatabase } from '@researchos/shared'
import { DB_FIELD_TYPES } from '@researchos/shared'

import { formatTimestamp } from '../../clock'
import { newId } from '../../ids'
import type { Actor, EntityType } from '../../schema'
import { emitEvent, requireLiveRow, restoreEntity, softDelete } from './activity'
import { conflict, entityDeleted, invalid, isUniqueViolation, notFound, type Json, type NotesContext } from './support'

export const RELATION_TARGETS: readonly EntityType[] = ['paper', 'note', 'idea', 'experiment', 'db_record']

export interface SelectOption {
  id: string
  name: string
  color: string
}

export interface FieldConfig {
  options?: SelectOption[]
  targets?: string[]
}

export interface FieldInput {
  name: string
  type: DbFieldType
  config?: unknown
  options?: unknown
  targets?: unknown
  role?: 'title' | 'plain'
}

export interface FieldPatchInput {
  name?: string
  config?: unknown
  options?: unknown
  targets?: unknown
  role?: 'title' | 'plain'
}

export interface DatabaseCreateInput {
  name: string
  description?: string
  fields?: FieldInput[]
  actor?: Actor
}

export interface FieldRow {
  id: string
  databaseId: string
  name: string
  type: DbFieldType
  config: string
  sortOrder: number
  role: 'title' | 'plain'
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

const FIELD_COLUMNS = `id, database_id, name, type, config, sort_order, role, created_at, updated_at, deleted_at`

function asRecord(value: unknown): Json {
  if (value === null || value === undefined) return {}
  if (typeof value !== 'object' || Array.isArray(value)) {
    throw invalid('field config must be a JSON object')
  }
  return value as Json
}

function optionCandidates(input: FieldInput | FieldPatchInput, config: Json): unknown {
  if (input.options !== undefined) return input.options
  if (config.options !== undefined) return config.options
  if (Array.isArray(config)) return config
  return undefined
}

function targetCandidates(input: FieldInput | FieldPatchInput, config: Json): unknown {
  if (input.targets !== undefined) return input.targets
  if (config.targets !== undefined) return config.targets
  if (Array.isArray(config)) return config
  return undefined
}

/**
 * Accepts every historical option shape (`string[]`, `{name,color}[]`,
 * `config.options`, top-level `options`) and returns canonical options with
 * server-generated ids. Existing ids stay stable across an update.
 */
export function normalizeFieldConfig(
  ctx: NotesContext,
  type: DbFieldType,
  input: FieldInput | FieldPatchInput,
  existingOptionIds: readonly string[] = [],
  omittedConfig?: string
): string {
  const rawConfig = input.config
  const config: Json = Array.isArray(rawConfig) ? {} : asRecord(rawConfig)
  const optionsObject =
    input.options !== undefined && typeof input.options === 'object' && !Array.isArray(input.options)
      ? asRecord(input.options)
      : null
  switch (type) {
    case 'select':
    case 'multi_select': {
      const candidates = input.options !== undefined
        ? Array.isArray(input.options)
          ? input.options
          : optionsObject?.options
        : Array.isArray(rawConfig)
          ? rawConfig
          : optionCandidates(input, config)
      if (candidates === undefined) {
        if (omittedConfig !== undefined) return omittedConfig
        return JSON.stringify({ options: [] })
      }
      if (!Array.isArray(candidates)) throw invalid('options must be an array', { field: 'options' })
      const known = new Set(existingOptionIds)
      const seen = new Set<string>()
      const options: SelectOption[] = []
      for (const [index, candidate] of candidates.entries()) {
        let name: string
        let color = ''
        let id = ''
        if (typeof candidate === 'string') {
          name = candidate.trim()
        } else if (typeof candidate === 'object' && candidate !== null && !Array.isArray(candidate)) {
          const entry = candidate as Json
          name = typeof entry.name === 'string' ? entry.name.trim() : ''
          color = typeof entry.color === 'string' ? entry.color.trim() : ''
          id = typeof entry.id === 'string' ? entry.id : ''
        } else {
          throw invalid(`option at index ${index} must be a string or {name,color}`, { field: `options.${index}` })
        }
        if (name === '') throw invalid(`option at index ${index} needs a non-empty name`, { field: `options.${index}.name` })
        if (id === '' || !known.has(id) || seen.has(id)) id = newId(ctx.clock)
        seen.add(id)
        options.push({ id, name, color })
      }
      return JSON.stringify({ options })
    }
    case 'relation': {
      const candidates = input.targets !== undefined
        ? input.targets
        : input.options !== undefined && !Array.isArray(input.options)
          ? optionsObject?.targets
          : Array.isArray(rawConfig)
            ? rawConfig
            : targetCandidates(input, config)
      const raw = candidates === undefined ? RELATION_TARGETS : candidates
      if (!Array.isArray(raw)) throw invalid('relation targets must be an array', { field: 'targets' })
      const targets: string[] = []
      for (const candidate of raw) {
        if (typeof candidate !== 'string') throw invalid('relation targets must be strings', { field: 'targets' })
        const target = candidate.trim() as EntityType
        if (!RELATION_TARGETS.includes(target)) {
          throw invalid(`relation target ${JSON.stringify(candidate)} is not allowed`, { field: 'targets' })
        }
        if (!targets.includes(target)) targets.push(target)
      }
      if (targets.length === 0) {
        // Defect 3: an omitted/empty target list means every supported target.
        targets.push(...RELATION_TARGETS)
      }
      return JSON.stringify({ targets })
    }
    default: {
      const filtered = Object.keys(config).filter((key) => key !== 'options' && key !== 'targets')
      if (filtered.length > 0) {
        throw invalid(`field type ${JSON.stringify(type)} takes no config`, { field: 'config' })
      }
      return '{}'
    }
  }
}

export function validateFieldType(type: string): DbFieldType {
  if (!(DB_FIELD_TYPES as readonly string[]).includes(type)) {
    throw invalid(`unknown field type ${JSON.stringify(type)}`, { field: 'type' })
  }
  return type as DbFieldType
}

export function validateFieldName(name: string): string {
  const trimmed = name.trim()
  if (trimmed === '') throw invalid('field name must not be empty', { field: 'name' })
  if (Array.from(trimmed).length > 64) throw invalid('field name must be at most 64 characters', { field: 'name' })
  return trimmed
}

export function parseFieldConfig(text: string): FieldConfig {
  try {
    const parsed: unknown = JSON.parse(text === '' ? '{}' : text)
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const record = parsed as Json
    const options = Array.isArray(record.options)
      ? record.options
          .filter((entry): entry is Json => typeof entry === 'object' && entry !== null && !Array.isArray(entry))
          .map((entry) => ({
            id: typeof entry.id === 'string' ? entry.id : '',
            name: typeof entry.name === 'string' ? entry.name : '',
            color: typeof entry.color === 'string' ? entry.color : ''
          }))
      : []
    const targets = Array.isArray(record.targets)
      ? record.targets.filter((entry): entry is string => typeof entry === 'string')
      : []
    return { options, targets }
  } catch {
    return {}
  }
}

export function fieldToDto(row: FieldRow): DbField {
  return {
    id: row.id,
    databaseId: row.databaseId,
    name: row.name,
    type: row.type,
    config: JSON.parse(row.config === '' ? '{}' : row.config) as Json,
    sortOrder: row.sortOrder,
    role: row.role,
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
    deletedAt: row.deletedAt
  }
}

export function loadFieldRow(ctx: NotesContext, fieldId: string): FieldRow | null {
  const row = ctx.db.prepare(`SELECT ${FIELD_COLUMNS} FROM db_fields WHERE id = ?`).get(fieldId) as
    | Record<string, unknown>
    | undefined
  if (row === undefined) return null
  return {
    id: String(row.id),
    databaseId: String(row.database_id),
    name: String(row.name),
    type: String(row.type) as DbFieldType,
    config: String(row.config ?? '{}'),
    sortOrder: Number(row.sort_order),
    role: String(row.role) as 'title' | 'plain',
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    deletedAt: row.deleted_at === null ? null : String(row.deleted_at)
  }
}

export function listFieldRows(ctx: NotesContext, databaseId: string, includeDeleted = false): FieldRow[] {
  const rows = ctx.db
    .prepare(
      `SELECT ${FIELD_COLUMNS} FROM db_fields WHERE database_id = ?
        ${includeDeleted ? '' : 'AND deleted_at IS NULL'} ORDER BY sort_order, id`
    )
    .all(databaseId) as Array<Record<string, unknown>>
  return rows.map((row) => ({
    id: String(row.id),
    databaseId: String(row.database_id),
    name: String(row.name),
    type: String(row.type) as DbFieldType,
    config: String(row.config ?? '{}'),
    sortOrder: Number(row.sort_order),
    role: String(row.role) as 'title' | 'plain',
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    deletedAt: row.deleted_at === null ? null : String(row.deleted_at)
  }))
}

export function loadTitleField(ctx: NotesContext, databaseId: string): FieldRow | null {
  const row = ctx.db
    .prepare(
      `SELECT ${FIELD_COLUMNS} FROM db_fields
        WHERE database_id = ? AND role = 'title' AND deleted_at IS NULL`
    )
    .get(databaseId) as Record<string, unknown> | undefined
  return row === undefined ? null : loadFieldRow(ctx, String(row.id))
}

interface DatabaseRow {
  id: string
  name: string
  description: string
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

function loadDatabaseRow(ctx: NotesContext, databaseId: string): DatabaseRow | null {
  const row = ctx.db
    .prepare(`SELECT id, name, description, created_at, updated_at, deleted_at FROM research_databases WHERE id = ?`)
    .get(databaseId) as Record<string, unknown> | undefined
  if (row === undefined) return null
  return {
    id: String(row.id),
    name: String(row.name),
    description: String(row.description ?? ''),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    deletedAt: row.deleted_at === null ? null : String(row.deleted_at)
  }
}

export function databaseToDto(row: DatabaseRow): ResearchDatabase {
  return {
    id: row.id,
    name: row.name,
    description: row.description,
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
    deletedAt: row.deletedAt
  }
}

export function getDatabase(ctx: NotesContext, databaseId: string): ResearchDatabase {
  const row = loadDatabaseRow(ctx, databaseId)
  if (row === null) throw notFound(`database ${databaseId} not found`)
  return databaseToDto(row)
}

export function listDatabases(ctx: NotesContext, includeDeleted = false): ResearchDatabase[] {
  const rows = ctx.db
    .prepare(
      `SELECT id, name, description, created_at, updated_at, deleted_at FROM research_databases
        ${includeDeleted ? '' : 'WHERE deleted_at IS NULL'} ORDER BY lower(name), id`
    )
    .all() as Array<Record<string, unknown>>
  return rows.map((row) =>
    databaseToDto({
      id: String(row.id),
      name: String(row.name),
      description: String(row.description ?? ''),
      createdAt: String(row.created_at),
      updatedAt: String(row.updated_at),
      deletedAt: row.deleted_at === null ? null : String(row.deleted_at)
    })
  )
}

interface PreparedField {
  name: string
  type: DbFieldType
  config: string
  role: 'title' | 'plain'
}

/**
 * Validates every initial field before the transaction opens, so an invalid
 * field can never leave a half-created database behind (defects 3 and 4).
 */
function prepareInitialFields(ctx: NotesContext, fields: FieldInput[]): PreparedField[] {
  const prepared: PreparedField[] = []
  const names = new Set<string>()
  let titleIndex = -1
  for (const [index, field] of fields.entries()) {
    const name = validateFieldName(field.name)
    const folded = name.toLowerCase()
    if (names.has(folded)) {
      throw invalid(`duplicate field name ${JSON.stringify(name)}`, { field: `fields.${index}.name` })
    }
    names.add(folded)
    const type = validateFieldType(field.type)
    const config = normalizeFieldConfig(ctx, type, field)
    const role = field.role === 'title' ? 'title' : 'plain'
    if (role === 'title') {
      if (titleIndex !== -1) throw invalid('a database can have only one title field', { field: `fields.${index}.role` })
      titleIndex = index
    }
    prepared.push({ name, type, config, role })
  }
  if (titleIndex === -1) {
    if (names.has('name')) {
      throw invalid('field name "Name" is reserved for the mandatory title field', { field: 'fields' })
    }
    prepared.unshift({ name: 'Name', type: 'text', config: '{}', role: 'title' })
  }
  return prepared
}

export function createDatabase(ctx: NotesContext, input: DatabaseCreateInput): {
  database: ResearchDatabase
  fields: DbField[]
} {
  const name = input.name.trim()
  if (name === '') throw invalid('database name must not be empty', { field: 'name' })
  if (Array.from(name).length > 200) throw invalid('database name must be at most 200 characters', { field: 'name' })
  const description = input.description ?? ''
  const prepared = prepareInitialFields(ctx, input.fields ?? [])
  const databaseId = newId(ctx.clock)
  return ctx.inTx(() => {
    const now = formatTimestamp(ctx.clock.now())
    ctx.db
      .prepare(
        `INSERT INTO research_databases(id, name, description, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?)`
      )
      .run(databaseId, name, description, now, now)
    const fields: DbField[] = []
    for (const [position, field] of prepared.entries()) {
      const fieldId = newId(ctx.clock)
      ctx.db
        .prepare(
          `INSERT INTO db_fields(id, database_id, name, type, config, sort_order, role, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(fieldId, databaseId, field.name, field.type, field.config, position, field.role, now, now)
      const row = loadFieldRow(ctx, fieldId)
      if (row !== null) fields.push(fieldToDto(row))
    }
    emitEvent(ctx, {
      type: 'entity.created',
      entityType: 'research_database',
      entityId: databaseId,
      summary: `创建数据库：${name}`,
      actor: input.actor ?? 'user',
      at: ctx.clock.now()
    })
    const database = loadDatabaseRow(ctx, databaseId)
    if (database === null) throw new Error('database insert did not produce a row')
    return { database: databaseToDto(database), fields }
  })
}

export function updateDatabase(
  ctx: NotesContext,
  databaseId: string,
  patch: { name?: string; description?: string },
  expectedUpdatedAt?: string,
  actor: Actor = 'user'
): ResearchDatabase {
  return ctx.inTx(() => {
    const row = loadDatabaseRow(ctx, databaseId)
    if (row === null) throw notFound(`database ${databaseId} not found`)
    if (row.deletedAt !== null) throw entityDeleted('research_database', databaseId)
    const name = patch.name === undefined ? row.name : patch.name.trim()
    if (name === '') throw invalid('database name must not be empty', { field: 'name' })
    if (Array.from(name).length > 200) throw invalid('database name must be at most 200 characters', { field: 'name' })
    const description = patch.description === undefined ? row.description : patch.description
    if (name === row.name && description === row.description) return databaseToDto(row)
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== row.updatedAt) {
      throw conflict('database was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: row.updatedAt,
        current: databaseToDto(row)
      })
    }
    const sql =
      expectedUpdatedAt === undefined
        ? `UPDATE research_databases SET name = ?, description = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
        : `UPDATE research_databases SET name = ?, description = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND updated_at = ?`
    const args: unknown[] = [name, description, formatTimestamp(ctx.clock.now()), databaseId]
    if (expectedUpdatedAt !== undefined) args.push(expectedUpdatedAt)
    if (ctx.db.prepare(sql).run(...args).changes === 0) {
      throw conflict('database was modified by another writer', { expectedUpdatedAt })
    }
    emitEvent(ctx, {
      type: 'entity.updated',
      entityType: 'research_database',
      entityId: databaseId,
      old: { name: row.name, description: row.description },
      new: { name, description },
      summary: '更新数据库',
      actor,
      at: ctx.clock.now()
    })
    return getDatabase(ctx, databaseId)
  })
}

export function deleteDatabase(ctx: NotesContext, databaseId: string, actor: Actor = 'user'): { deleted: boolean } {
  return ctx.inTx(() => {
    if (loadDatabaseRow(ctx, databaseId) === null) throw notFound(`database ${databaseId} not found`)
    softDelete(ctx, 'research_database', databaseId, actor)
    return { deleted: true }
  })
}

export function listFields(ctx: NotesContext, databaseId: string): DbField[] {
  if (loadDatabaseRow(ctx, databaseId) === null) throw notFound(`database ${databaseId} not found`)
  return listFieldRows(ctx, databaseId).map(fieldToDto)
}

function requireLiveDatabase(ctx: NotesContext, databaseId: string): DatabaseRow {
  const row = loadDatabaseRow(ctx, databaseId)
  if (row === null) throw notFound(`database ${databaseId} not found`)
  if (row.deletedAt !== null) throw entityDeleted('research_database', databaseId)
  return row
}

function requireFieldNameFree(ctx: NotesContext, databaseId: string, name: string, exceptId?: string): void {
  const clash = listFieldRows(ctx, databaseId).find(
    (field) => field.id !== exceptId && field.name.toLowerCase() === name.toLowerCase()
  )
  if (clash !== undefined) {
    throw invalid(`a field named ${JSON.stringify(name)} already exists in this database`, { field: 'name' })
  }
}

export function createField(
  ctx: NotesContext,
  databaseId: string,
  input: FieldInput,
  actor: Actor = 'user'
): DbField {
  const name = validateFieldName(input.name)
  const type = validateFieldType(input.type)
  const config = normalizeFieldConfig(ctx, type, input)
  requireLiveDatabase(ctx, databaseId)
  requireFieldNameFree(ctx, databaseId, name)
  const fieldId = newId(ctx.clock)
  const now = formatTimestamp(ctx.clock.now())
  return ctx.inTx(() => {
    const positionRow = ctx.db
      .prepare(`SELECT COALESCE(MAX(sort_order) + 1, 0) AS n FROM db_fields WHERE database_id = ?`)
      .get(databaseId) as { n: number }
    try {
      ctx.db
        .prepare(
          `INSERT INTO db_fields(id, database_id, name, type, config, sort_order, role, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, 'plain', ?, ?)`
        )
        .run(fieldId, databaseId, name, type, config, positionRow.n, now, now)
    } catch (error) {
      if (isUniqueViolation(error)) throw invalid(`a field named ${JSON.stringify(name)} already exists`, { field: 'name' })
      throw error
    }
    emitEvent(ctx, {
      type: 'entity.created',
      entityType: 'db_field',
      entityId: fieldId,
      summary: `新增字段：${name}`,
      actor,
      at: ctx.clock.now()
    })
    const row = loadFieldRow(ctx, fieldId)
    if (row === null) throw new Error('field insert did not produce a row')
    return fieldToDto(row)
  })
}

export function updateField(
  ctx: NotesContext,
  fieldId: string,
  patch: FieldPatchInput,
  expectedUpdatedAt?: string,
  actor: Actor = 'user'
): DbField {
  return ctx.inTx(() => {
    const row = loadFieldRow(ctx, fieldId)
    if (row === null) throw notFound(`field ${fieldId} not found`)
    if (row.deletedAt !== null) throw entityDeleted('db_field', fieldId)
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== row.updatedAt) {
      throw conflict('field was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: row.updatedAt,
        current: fieldToDto(row)
      })
    }
    const name = patch.name === undefined ? row.name : validateFieldName(patch.name)
    requireFieldNameFree(ctx, row.databaseId, name, fieldId)
    const existing = parseFieldConfig(row.config)
    const config =
      patch.name === undefined && patch.config === undefined && patch.options === undefined && patch.targets === undefined
        ? row.config
        : normalizeFieldConfig(
            ctx,
            row.type,
            patch,
            (existing.options ?? []).map((option) => option.id),
            row.config
          )
    let role = row.role
    if (patch.role !== undefined) {
      if (patch.role === 'title') role = 'title'
      else if (row.role === 'title') throw invalid('a database must keep exactly one title field', { field: 'role' })
      else role = 'plain'
    }
    const unchanged = name === row.name && config === row.config && role === row.role
    if (unchanged) return fieldToDto(row)
    const now = formatTimestamp(ctx.clock.now())
    if (role === 'title' && row.role !== 'title') {
      ctx.db
        .prepare(`UPDATE db_fields SET role = 'plain', updated_at = ? WHERE database_id = ? AND role = 'title' AND deleted_at IS NULL`)
        .run(now, row.databaseId)
    }
    const sql =
      expectedUpdatedAt === undefined
        ? `UPDATE db_fields SET name = ?, config = ?, role = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
        : `UPDATE db_fields SET name = ?, config = ?, role = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND updated_at = ?`
    const args: unknown[] = [name, config, role, now, fieldId]
    if (expectedUpdatedAt !== undefined) args.push(expectedUpdatedAt)
    if (ctx.db.prepare(sql).run(...args).changes === 0) {
      const current = loadFieldRow(ctx, fieldId)
      throw conflict('field was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: current?.updatedAt ?? null,
        current: current === null ? null : fieldToDto(current)
      })
    }
    const oldObject: Json = {}
    const newObject: Json = {}
    if (row.name !== name) {
      oldObject.name = row.name
      newObject.name = name
    }
    if (row.config !== config) {
      oldObject.config = parseFieldConfig(row.config)
      newObject.config = parseFieldConfig(config)
    }
    if (row.role !== role) {
      oldObject.role = row.role
      newObject.role = role
    }
    emitEvent(ctx, {
      type: 'entity.updated',
      entityType: 'db_field',
      entityId: fieldId,
      old: oldObject,
      new: newObject,
      summary: `修改字段：${name}`,
      actor,
      at: ctx.clock.now()
    })
    const updated = loadFieldRow(ctx, fieldId)
    if (updated === null) throw notFound(`field ${fieldId} not found`)
    return fieldToDto(updated)
  })
}

export function deleteField(
  ctx: NotesContext,
  fieldId: string,
  expectedUpdatedAt?: string,
  actor: Actor = 'user'
): { deleted: boolean } {
  return ctx.inTx(() => {
    const row = loadFieldRow(ctx, fieldId)
    if (row === null) throw notFound(`field ${fieldId} not found`)
    if (row.deletedAt !== null) throw entityDeleted('db_field', fieldId)
    if (row.role === 'title') {
      throw invalid('the title field cannot be deleted; delete the database instead')
    }
    if (expectedUpdatedAt !== undefined && expectedUpdatedAt !== row.updatedAt) {
      throw conflict('field was modified by another writer', {
        expectedUpdatedAt,
        currentUpdatedAt: row.updatedAt,
        current: fieldToDto(row)
      })
    }
    softDelete(ctx, 'db_field', fieldId, actor)
    return { deleted: true }
  })
}

export function restoreField(ctx: NotesContext, fieldId: string, actor: Actor = 'user'): DbField {
  return ctx.inTx(() => {
    const row = loadFieldRow(ctx, fieldId)
    if (row === null) throw notFound(`field ${fieldId} not found`)
    restoreEntity(ctx, 'db_field', fieldId, actor)
    const restored = loadFieldRow(ctx, fieldId)
    if (restored === null) throw notFound(`field ${fieldId} not found`)
    return fieldToDto(restored)
  })
}

export function reorderFields(ctx: NotesContext, databaseId: string, orderedFieldIds: string[]): DbField[] {
  requireLiveDatabase(ctx, databaseId)
  const live = listFieldRows(ctx, databaseId)
  checkPermutation('fields', orderedFieldIds, live.map((field) => field.id))
  return ctx.inTx(() => {
    const now = formatTimestamp(ctx.clock.now())
    for (const [position, fieldId] of orderedFieldIds.entries()) {
      ctx.db
        .prepare(`UPDATE db_fields SET sort_order = ?, updated_at = ? WHERE id = ? AND database_id = ?`)
        .run(position, now, fieldId, databaseId)
    }
    return listFields(ctx, databaseId)
  })
}

export function checkPermutation(kind: string, ordered: string[], live: string[]): void {
  if (ordered.length !== live.length) {
    throw invalid(`ordered ${kind} must contain all ${live.length} live ids, got ${ordered.length}`)
  }
  const known = new Set(live)
  const seen = new Set<string>()
  for (const id of ordered) {
    if (!known.has(id)) throw invalid(`${kind} id ${JSON.stringify(id)} is not a live row of this database`)
    if (seen.has(id)) throw invalid(`${kind} id ${JSON.stringify(id)} is repeated`)
    seen.add(id)
  }
}

export { requireLiveRow }