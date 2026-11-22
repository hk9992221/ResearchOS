/**
 * Read-only Zotero 7 SQLite reader (plan §4.3). The connection is opened with
 * `readonly: true`, a 3 s busy timeout and no `immutable` flag: Zotero may hold
 * the live database open. Nothing in this file writes to the data directory.
 */
import Database from 'better-sqlite3'

import { appError, AppErrorCode } from '../../../errors'
import type {
  ItemPage,
  ListItemsOptions,
  MetadataSource,
  ResolvedFile,
  ZoteroAttachmentRecord,
  ZoteroCreator,
  ZoteroItemRecord,
  ZoteroReader,
  ZoteroTag
} from '../types'
import { attachmentFilename, assertPersonalLibrary } from './localapi'
import { createResolver, type AttachmentResolver } from './resolve'

export interface SqliteOptions {
  dataDir: string
  linkedBaseDir?: string
  busyTimeoutMs?: number
}

interface ItemHeadRow {
  itemID: number
  key: string
  version: number
  typeName: string
}

interface FieldRow {
  itemID: number
  fieldName: string
  value: string
}

interface CreatorRow {
  itemID: number
  firstName: string
  lastName: string
  fieldMode: number
  creatorType: string
}

interface TagRow {
  itemID: number
  name: string
  type: number
}

interface CollectionKeyRow {
  itemID: number
  key: string
}

interface AttachmentRow {
  itemID: number
  key: string
  libraryID: number
  parentItemID: number
  linkMode: number
  contentType: string
  path: string
}

/** `0` is Zotero's personal library outside the database; it stores as 1. */
export function libraryNumber(libraryId: string): number {
  const id = libraryId.trim()
  if (id === '' || id === '0') return 1
  const number = Number.parseInt(id, 10)
  if (!Number.isFinite(number) || number < 0) {
    throw appError(AppErrorCode.InvalidArgument, `非法的 library ID ${JSON.stringify(libraryId)}`)
  }
  return number
}

function linkModeName(value: number): string {
  switch (value) {
    case 0:
      return 'imported_file'
    case 1:
      return 'imported_url'
    case 2:
      return 'linked_file'
    case 3:
      return 'linked_url'
    default:
      return ''
  }
}

function placeholders(count: number): string {
  return new Array(count).fill('?').join(',')
}

export class SqliteReader implements ZoteroReader {
  private readonly db: Database.Database
  private readonly dataDir: string
  private readonly resolver: AttachmentResolver

  constructor(options: SqliteOptions) {
    this.dataDir = options.dataDir
    this.resolver = createResolver({
      dataDir: options.dataDir,
      linkedBaseDir: options.linkedBaseDir
    })
    const databasePath = `${options.dataDir}/zotero.sqlite`
    try {
      this.db = new Database(databasePath, { readonly: true, fileMustExist: true })
      this.db.pragma(`busy_timeout = ${options.busyTimeoutMs ?? 3000}`)
    } catch (cause) {
      throw appError(
        AppErrorCode.ExternalUnavailable,
        `无法以只读方式打开 Zotero 数据库：${databasePath}`,
        { cause, details: { path: databasePath } }
      )
    }
  }

  source(): MetadataSource {
    return 'sqlite'
  }

  close(): void {
    if (this.db.open) this.db.close()
  }

  async listLibraries(): Promise<Array<{ id: string; name: string }>> {
    return [{ id: '0', name: '我的文库' }]
  }

  async listCollections(
    libraryId: string
  ): Promise<Array<{ key: string; parentKey: string; name: string; itemCount: number }>> {
    assertPersonalLibrary(libraryId)
    const library = libraryNumber(libraryId)
    const rows = this.db
      .prepare(
        `SELECT c.key, c.collectionName AS name, COALESCE(pc.key, '') AS parentKey,
                (SELECT COUNT(*)
                   FROM collectionItems ci
                   JOIN items i ON i.itemID = ci.itemID
                   JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
                   LEFT JOIN deletedItems d ON d.itemID = i.itemID
                  WHERE ci.collectionID = c.collectionID
                    AND d.itemID IS NULL
                    AND it.typeName NOT IN ('attachment', 'note')) AS itemCount
           FROM collections c
           LEFT JOIN collections pc ON pc.collectionID = c.parentCollectionID
          WHERE c.libraryID = ?
          ORDER BY c.collectionName, c.key`
      )
      .all(library) as Array<{ key: string; name: string; parentKey: string; itemCount: number }>
    return rows.map((row) => ({
      key: row.key,
      name: row.name,
      parentKey: row.parentKey,
      itemCount: row.itemCount
    }))
  }

  async listItems(
    libraryId: string,
    collectionKey: string,
    options: ListItemsOptions
  ): Promise<ItemPage> {
    assertPersonalLibrary(libraryId)
    const library = libraryNumber(libraryId)
    const limit = options.limit <= 0 || options.limit > 100 ? 100 : options.limit
    const offset = Math.max(0, options.offset)
    const conditions = [
      'i.libraryID = ?',
      'd.itemID IS NULL',
      "it.typeName NOT IN ('attachment', 'note')"
    ]
    const args: unknown[] = [library]
    if (collectionKey.trim() !== '') {
      conditions.push(
        `i.itemID IN (SELECT ci.itemID FROM collectionItems ci
                        JOIN collections c ON c.collectionID = ci.collectionID
                       WHERE c.key = ?)`
      )
      args.push(collectionKey.trim())
    }
    if (options.query.trim() !== '') {
      conditions.push(
        `i.itemID IN (SELECT idv.itemID FROM itemData idv
                        JOIN fields f ON f.fieldID = idv.fieldID
                        JOIN itemDataValues v ON v.valueID = idv.valueID
                       WHERE f.fieldName = 'title'
                         AND instr(lower(v.value), lower(?)) > 0)`
      )
      args.push(options.query.trim())
    }
    const where = ` WHERE ${conditions.join(' AND ')}`
    const total = (
      this.db
        .prepare(
          `SELECT COUNT(*) AS n FROM items i
             JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
             LEFT JOIN deletedItems d ON d.itemID = i.itemID${where}`
        )
        .get(...args) as { n: number }
    ).n
    const heads = this.db
      .prepare(
        `SELECT i.itemID AS itemID, i.key AS key, i.version AS version, it.typeName AS typeName
           FROM items i
           JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
           LEFT JOIN deletedItems d ON d.itemID = i.itemID${where}
          ORDER BY i.itemID LIMIT ? OFFSET ?`
      )
      .all(...args, limit, offset) as ItemHeadRow[]
    const loaded = await this.loadItems(heads.map((head) => head.itemID))
    const items: ZoteroItemRecord[] = heads.map((head) => {
      const base = loaded.get(head.itemID) ?? this.emptyItem()
      return {
        ...base,
        key: head.key,
        version: head.version,
        itemType: head.typeName,
        libraryId: String(library)
      }
    })
    return { items, total, source: 'sqlite' }
  }

  async getItem(libraryId: string, key: string): Promise<ZoteroItemRecord> {
    assertPersonalLibrary(libraryId)
    const library = libraryNumber(libraryId)
    const head = this.db
      .prepare(
        `SELECT i.itemID AS itemID, i.key AS key, i.version AS version, it.typeName AS typeName
           FROM items i
           JOIN itemTypes it ON it.itemTypeID = i.itemTypeID
           LEFT JOIN deletedItems d ON d.itemID = i.itemID
          WHERE i.key = ? AND i.libraryID = ? AND d.itemID IS NULL
            AND it.typeName NOT IN ('attachment', 'note')
          LIMIT 1`
      )
      .get(key, library) as ItemHeadRow | undefined
    if (head === undefined) {
      throw appError(AppErrorCode.NotFound, `Zotero 条目 ${key} 不存在`)
    }
    const loaded = await this.loadItems([head.itemID])
    return {
      ...(loaded.get(head.itemID) ?? this.emptyItem()),
      key: head.key,
      version: head.version,
      itemType: head.typeName,
      libraryId: String(library)
    }
  }

  async listAttachments(libraryId: string, parentKey: string): Promise<ZoteroAttachmentRecord[]> {
    assertPersonalLibrary(libraryId)
    const library = libraryNumber(libraryId)
    const parent = this.db
      .prepare(`SELECT itemID FROM items WHERE key = ? AND libraryID = ? LIMIT 1`)
      .get(parentKey, library) as { itemID: number } | undefined
    if (parent === undefined) {
      throw appError(AppErrorCode.NotFound, `Zotero 条目 ${parentKey} 不存在`)
    }
    const rows = this.db
      .prepare(
        `SELECT i.itemID AS itemID, i.key AS key, i.libraryID AS libraryID,
                ia.parentItemID AS parentItemID,
                COALESCE(ia.linkMode, 0) AS linkMode,
                COALESCE(ia.contentType, '') AS contentType,
                COALESCE(ia.path, '') AS path
           FROM itemAttachments ia
           JOIN items i ON i.itemID = ia.itemID
           LEFT JOIN deletedItems d ON d.itemID = i.itemID
          WHERE ia.parentItemID = ? AND d.itemID IS NULL
          ORDER BY i.itemID`
      )
      .all(parent.itemID) as AttachmentRow[]
    return rows.map((row) => {
      const attachment: ZoteroAttachmentRecord = {
        key: row.key,
        parentKey,
        libraryId: String(row.libraryID),
        filename: attachmentFilename(row.path),
        contentType: row.contentType,
        linkMode: linkModeName(row.linkMode),
        path: row.path,
        size: 0
      }
      try {
        attachment.size = this.resolver.resolve(attachment).size
      } catch {
        // Unresolvable attachments are reported by the caller, not dropped.
      }
      return attachment
    })
  }

  async resolveAttachment(attachment: ZoteroAttachmentRecord): Promise<ResolvedFile> {
    return this.resolver.resolve(attachment)
  }

  private emptyItem(): ZoteroItemRecord {
    return {
      key: '',
      libraryId: '0',
      version: 0,
      itemType: '',
      title: '',
      creators: [],
      date: '',
      doi: '',
      url: '',
      abstractNote: '',
      publicationTitle: '',
      tags: [],
      collections: [],
      language: '',
      extra: '',
      deleted: false,
      raw: {}
    }
  }

  private async loadItems(ids: number[]): Promise<Map<number, ZoteroItemRecord>> {
    const out = new Map<number, ZoteroItemRecord>()
    if (ids.length === 0) return out
    for (const id of ids) out.set(id, this.emptyItem())
    const marks = placeholders(ids.length)

    const fields = this.db
      .prepare(
        `SELECT id.itemID AS itemID, f.fieldName AS fieldName, v.value AS value
           FROM itemData id
           JOIN fields f ON f.fieldID = id.fieldID
           JOIN itemDataValues v ON v.valueID = id.valueID
          WHERE id.itemID IN (${marks})`
      )
      .all(...ids) as FieldRow[]
    const tagTypes = new Map<number, ZoteroTag[]>()
    for (const row of fields) {
      const item = out.get(row.itemID)
      if (item === undefined) continue
      item.raw[row.fieldName] = row.value
      switch (row.fieldName) {
        case 'title':
          item.title = row.value
          break
        case 'date':
          item.date = row.value
          break
        case 'DOI':
          item.doi = row.value
          break
        case 'url':
          item.url = row.value
          break
        case 'abstractNote':
          item.abstractNote = row.value
          break
        case 'publicationTitle':
          item.publicationTitle = row.value
          break
        case 'language':
          item.language = row.value
          break
        case 'extra':
          item.extra = row.value
          break
        default:
          break
      }
    }

    const creators = this.db
      .prepare(
        `SELECT ic.itemID AS itemID, c.firstName AS firstName, c.lastName AS lastName,
                c.fieldMode AS fieldMode, ct.creatorType AS creatorType
           FROM itemCreators ic
           JOIN creators c ON c.creatorID = ic.creatorID
           JOIN creatorTypes ct ON ct.creatorTypeID = ic.creatorTypeID
          WHERE ic.itemID IN (${marks})
          ORDER BY ic.itemID, ic.orderIndex`
      )
      .all(...ids) as CreatorRow[]
    for (const row of creators) {
      const item = out.get(row.itemID)
      if (item === undefined) continue
      const creator: ZoteroCreator =
        row.fieldMode === 1
          ? { firstName: '', lastName: row.lastName, name: row.lastName, creatorType: row.creatorType }
          : {
              firstName: row.firstName,
              lastName: row.lastName,
              creatorType: row.creatorType
            }
      item.creators.push(creator)
    }

    const tags = this.db
      .prepare(
        `SELECT it.itemID AS itemID, t.name AS name, it.type AS type
           FROM itemTags it
           JOIN tags t ON t.tagID = it.tagID
          WHERE it.itemID IN (${marks})
          ORDER BY it.itemID, t.name`
      )
      .all(...ids) as TagRow[]
    for (const row of tags) {
      const item = out.get(row.itemID)
      if (item === undefined) continue
      item.tags.push({ tag: row.name, type: row.type })
      const bucket = tagTypes.get(row.itemID) ?? []
      bucket.push({ tag: row.name, type: row.type })
      tagTypes.set(row.itemID, bucket)
    }
    for (const [id, list] of tagTypes) {
      const item = out.get(id)
      if (item !== undefined) item.raw['tags'] = list.map((tag) => ({ ...tag }))
    }

    const collections = this.db
      .prepare(
        `SELECT ci.itemID AS itemID, c.key AS key
           FROM collectionItems ci
           JOIN collections c ON c.collectionID = ci.collectionID
          WHERE ci.itemID IN (${marks})
          ORDER BY ci.itemID, c.key`
      )
      .all(...ids) as CollectionKeyRow[]
    for (const row of collections) {
      const item = out.get(row.itemID)
      if (item === undefined) continue
      item.collections.push(row.key)
      item.raw['collections'] = [...item.collections]
    }
    return out
  }
}

/** `PRAGMA user_version`, falling back to Zotero's legacy `version` table. */
export function readZoteroSchemaVersion(databasePath: string): { version: number; known: boolean } {
  let db: Database.Database | undefined
  try {
    db = new Database(databasePath, { readonly: true, fileMustExist: true })
    db.pragma('busy_timeout = 1000')
    const row = db.prepare('PRAGMA user_version').get() as { user_version?: number } | undefined
    if (row !== undefined && typeof row.user_version === 'number' && row.user_version > 0) {
      return { version: row.user_version, known: true }
    }
    const legacy = db.prepare('SELECT version FROM version LIMIT 1').get() as
      | { version?: number | string }
      | undefined
    if (legacy?.version !== undefined) {
      const value = Number.parseInt(String(legacy.version), 10)
      if (Number.isFinite(value)) return { version: value, known: true }
    }
    return { version: 0, known: false }
  } catch {
    return { version: 0, known: false }
  } finally {
    if (db !== undefined && db.open) db.close()
  }
}