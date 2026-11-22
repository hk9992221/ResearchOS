import type { Db } from '../../db/open'
import { parseTimestamp } from '../../clock'
import { AppErrorCode, appError } from '../../errors'
import { tablesFor, type IndexPartition, type IndexTables } from '../../index-store'
import { isEntityType, isSourceType, paperFulltextDocId, paperMetadataDocId, type SearchFilters } from './types'

export interface SearchTarget {
  /** project id, or "global" / "global:<project>" for the literature partition. */
  key: string
  projectId: string
  name: string
  archived: boolean
  /** Resolved lazily so a global search is not broken by the project LRU. */
  readonly db: Db
  partition: IndexPartition
  tables: IndexTables
  /** Project scope: the global paper documents linked to the project. */
  restrictDocIds?: string[]
}

export interface DocFilter {
  allowed?: string[]
  restricted: boolean
  excluded: string[]
}

export interface DocMeta {
  docId: string
  source: string
  entityType: string
  entityId: string
  title: string
  lang: string
  updatedAt: Date
  status: string
}

export function makeTarget(
  key: string,
  projectId: string,
  name: string,
  archived: boolean,
  openDb: () => Db,
  partition: IndexPartition,
  restrictDocIds?: string[]
): SearchTarget {
  return {
    key,
    projectId,
    name,
    archived,
    get db() {
      return openDb()
    },
    partition,
    tables: tablesFor(partition),
    ...(restrictDocIds === undefined ? {} : { restrictDocIds })
  }
}

function placeholders(count: number): string {
  return new Array(count).fill('?').join(',')
}

/**
 * Pushes structured filters down to the document table so a top-k cut can
 * never silently drop a filter-matching hit.
 */
export function resolveDocFilter(
  target: SearchTarget,
  filters: SearchFilters,
  includeDeleted: boolean,
  globalDb: Db
): DocFilter {
  const conditions: string[] = []
  const args: string[] = []
  if (target.partition === 'project') {
    conditions.push('project_id = ?')
    args.push(target.projectId)
  }
  if (filters.sources !== undefined && filters.sources.length > 0) {
    for (const source of filters.sources) {
      if (!isSourceType(source)) {
        throw appError(AppErrorCode.InvalidArgument, `unknown search source ${JSON.stringify(source)}`, {
          details: { source }
        })
      }
    }
    conditions.push(`source IN (${placeholders(filters.sources.length)})`)
    args.push(...filters.sources)
  }
  if (filters.entityTypes !== undefined && filters.entityTypes.length > 0) {
    for (const entityType of filters.entityTypes) {
      if (!isEntityType(entityType)) {
        throw appError(AppErrorCode.InvalidArgument, `unknown entity type ${JSON.stringify(entityType)}`, {
          details: { entityType }
        })
      }
    }
    conditions.push(`entity_type IN (${placeholders(filters.entityTypes.length)})`)
    args.push(...filters.entityTypes)
  }
  if (filters.updatedAfter !== undefined) {
    try {
      parseTimestamp(filters.updatedAfter)
    } catch (cause) {
      throw appError(AppErrorCode.InvalidArgument, 'updatedAfter must be a UTC millisecond timestamp', {
        cause
      })
    }
    conditions.push('updated_at >= ?')
    args.push(filters.updatedAfter)
  }

  let restricted = target.restrictDocIds !== undefined
  let allowed = target.restrictDocIds
  if (conditions.length > 0) {
    const rows = target.db
      .prepare(`SELECT doc_id AS id FROM ${target.tables.docs} WHERE ${conditions.join(' AND ')} ORDER BY doc_id`)
      .all(...args) as Array<{ id: string }>
    const fromSql = rows.map((row) => row.id)
    allowed = restricted ? intersect(fromSql, allowed ?? []) : fromSql
    restricted = true
  }

  const paperScope = paperScopeIds(globalDb, filters)
  if (paperScope !== undefined) {
    const scoped = paperDocIds(target, paperScope)
    allowed = restricted ? intersect(allowed ?? [], scoped) : scoped
    restricted = true
  }

  return {
    allowed: restricted ? (allowed ?? []) : undefined,
    restricted,
    excluded: includeDeleted ? [] : deletedDocIds(target)
  }
}

function intersect(left: readonly string[], right: readonly string[]): string[] {
  const set = new Set(right)
  return left.filter((value) => set.has(value))
}

function deletedDocIds(target: SearchTarget): string[] {
  const conditions = ['index_status = ?']
  const args: string[] = ['deleted']
  if (target.partition === 'project') {
    conditions.push('project_id = ?')
    args.push(target.projectId)
  }
  const rows = target.db
    .prepare(`SELECT doc_id AS id FROM ${target.tables.docs} WHERE ${conditions.join(' AND ')}`)
    .all(...args) as Array<{ id: string }>
  return rows.map((row) => row.id)
}

/* ------------------------------------------------------------- paper filters */

interface PaperRow {
  id: string
  metadata: string
}

/** Resolves the paper-id restriction of a filter; undefined means unrestricted. */
function paperScopeIds(globalDb: Db, filters: SearchFilters): string[] | undefined {
  const hasYears = filters.yearMin !== undefined || filters.yearMax !== undefined
  const hasAuthors = filters.authors !== undefined && filters.authors.length > 0
  if ((filters.paperIds === undefined || filters.paperIds.length === 0) && !hasYears && !hasAuthors) {
    return undefined
  }
  const candidates = new Set(filters.paperIds ?? [])
  if (!hasYears && !hasAuthors) return [...candidates]

  const rows = globalDb.prepare(`SELECT id, metadata FROM papers ORDER BY id`).all() as PaperRow[]
  const out: string[] = []
  for (const row of rows) {
    if (candidates.size > 0 && !candidates.has(row.id)) continue
    let metadata: {
      date?: string
      creators?: Array<{ firstName?: string; lastName?: string; name?: string }>
    } = {}
    try {
      metadata = JSON.parse(row.metadata) as typeof metadata
    } catch {
      metadata = {}
    }
    const year = metadataYear(metadata.date)
    if (filters.yearMin !== undefined && (year === 0 || year < filters.yearMin)) continue
    if (filters.yearMax !== undefined && (year === 0 || year > filters.yearMax)) continue
    if (hasAuthors) {
      const names = (metadata.creators ?? []).map((creator) =>
        `${creator.lastName ?? ''} ${creator.firstName ?? ''}`.trim() || (creator.name ?? '').trim()
      )
      const wanted = (filters.authors ?? []).map((author) => author.toLowerCase().trim()).filter((name) => name !== '')
      if (!names.some((name) => wanted.some((needle) => name.toLowerCase().includes(needle)))) continue
    }
    out.push(row.id)
  }
  return out
}

function metadataYear(date: string | undefined): number {
  for (const field of (date ?? '').split(/[^0-9]+/)) {
    if (field.length === 4) {
      const year = Number(field)
      if (year >= 1000 && year <= 2999) return year
    }
  }
  return 0
}

/**
 * Maps paper ids to the documents of one target: both global documents, or the
 * project's paper notes for a project partition.
 */
function paperDocIds(target: SearchTarget, paperIds: readonly string[]): string[] {
  const out: string[] = []
  if (target.partition === 'global') {
    for (const id of paperIds) out.push(paperMetadataDocId(id), paperFulltextDocId(id))
    return out
  }
  if (paperIds.length === 0) return out
  const rows = target.db
    .prepare(`SELECT id FROM notes WHERE paper_id IN (${placeholders(paperIds.length)})`)
    .all(...paperIds) as Array<{ id: string }>
  for (const row of rows) out.push(`note:${row.id}`)
  return out
}

/** Global paper documents linked to one project (project_papers is authoritative). */
export function linkedPaperDocIds(projectDb: Db): string[] {
  const ids = projectDb.prepare(`SELECT paper_id FROM project_papers ORDER BY paper_id`).pluck().all() as string[]
  const out: string[] = []
  for (const id of ids.map(String)) out.push(paperMetadataDocId(id), paperFulltextDocId(id))
  return out
}

/* ---------------------------------------------------------------- doc metadata */

export function loadDocMeta(target: SearchTarget, docIds: readonly string[]): Map<string, DocMeta> {
  const out = new Map<string, DocMeta>()
  if (docIds.length === 0) return out
  const rows = target.db
    .prepare(
      `SELECT doc_id, source, entity_type, entity_id, title, lang, updated_at, index_status
       FROM ${target.tables.docs} WHERE doc_id IN (${placeholders(docIds.length)})`
    )
    .all(...docIds) as Array<{
    doc_id: string
    source: string
    entity_type: string
    entity_id: string
    title: string
    lang: string
    updated_at: string
    index_status: string
  }>
  for (const row of rows) {
    let updatedAt = new Date(0)
    try {
      updatedAt = parseTimestamp(row.updated_at)
    } catch {
      updatedAt = new Date(0)
    }
    out.set(row.doc_id, {
      docId: row.doc_id,
      source: row.source,
      entityType: row.entity_type,
      entityId: row.entity_id,
      title: row.title,
      lang: row.lang,
      updatedAt,
      status: row.index_status
    })
  }
  return out
}
