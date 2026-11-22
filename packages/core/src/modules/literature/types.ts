/**
 * Internal literature types. The public shapes live in
 * `@researchos/shared/contracts/literature`; these are the reader/service
 * boundaries that never cross IPC.
 */
import type { JsonObject } from '@researchos/shared'

export type MetadataSource = 'local_api' | 'sqlite'

export interface ZoteroCreator {
  firstName: string
  lastName: string
  name?: string
  creatorType: string
}

export interface ZoteroTag {
  tag: string
  type: number
}

export interface ZoteroItemRecord {
  key: string
  libraryId: string
  version: number
  itemType: string
  title: string
  creators: ZoteroCreator[]
  date: string
  doi: string
  url: string
  abstractNote: string
  publicationTitle: string
  tags: ZoteroTag[]
  collections: string[]
  language: string
  extra: string
  deleted: boolean
  /** Unknown Zotero fields, preserved verbatim. */
  raw: JsonObject
}

export interface ZoteroAttachmentRecord {
  key: string
  parentKey: string
  libraryId: string
  filename: string
  contentType: string
  /** `imported_file | imported_url | linked_file | linked_url`. */
  linkMode: string
  /** Zotero's recorded path; may carry a `storage:` / `attachments:` prefix. */
  path: string
  size: number
}

export interface ResolvedFile {
  path: string
  size: number
  modTime: Date
}

export interface ListItemsOptions {
  offset: number
  /** 1..100. */
  limit: number
  query: string
}

export interface ItemPage {
  items: ZoteroItemRecord[]
  total: number
  source: MetadataSource
}

/** Read-only Zotero metadata reader (local API or sqlite). */
export interface ZoteroReader {
  source(): MetadataSource
  listLibraries(): Promise<Array<{ id: string; name: string }>>
  listCollections(
    libraryId: string
  ): Promise<Array<{ key: string; parentKey: string; name: string; itemCount: number }>>
  listItems(libraryId: string, collectionKey: string, options: ListItemsOptions): Promise<ItemPage>
  getItem(libraryId: string, key: string): Promise<ZoteroItemRecord>
  listAttachments(libraryId: string, parentKey: string): Promise<ZoteroAttachmentRecord[]>
  resolveAttachment(attachment: ZoteroAttachmentRecord): Promise<ResolvedFile>
}

/** Chooses the reader for one call: local API first, sqlite fallback. */
export interface ZoteroSource {
  reader(): Promise<ZoteroReader>
  /** Current mode and last fallback reason, for diagnostics. */
  status(): Promise<{ mode: MetadataSource | 'unavailable'; reason: string }>
}

export interface PaperMetadata {
  key: string
  version: number
  itemType: string
  title: string
  creators: ZoteroCreator[]
  date: string
  publicationTitle: string
  doi: string
  url: string
  abstractNote: string
  tags: ZoteroTag[]
  collections: string[]
  language: string
  extra: string
  year: number
  authors: string
}

export interface ImportProgressEvent {
  index: number
  total: number
  key: string
  status: string
  message: string
}

export type ImportProgress = (event: ImportProgressEvent) => void

export interface PaperRow {
  id: string
  zoteroLibraryId: string
  zoteroKey: string
  attachmentKey: string | null
  metadata: string
  filePath: string | null
  fileHash: string | null
  fileSize: number | null
  fileState: 'ok' | 'missing'
  sourceState: 'ok' | 'missing' | 'unreadable'
  lastSyncedAt: string | null
  importedAt: string
  updatedAt: string
}

export interface ProjectPaperRow extends PaperRow {
  linkedAt: string
}

export function paperMetadataOf(row: PaperRow): PaperMetadata {
  let parsed: Partial<PaperMetadata> = {}
  if (row.metadata.trim() !== '') {
    try {
      const value: unknown = JSON.parse(row.metadata)
      if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
        parsed = value as Partial<PaperMetadata>
      }
    } catch {
      parsed = {}
    }
  }
  const date = typeof parsed.date === 'string' ? parsed.date : ''
  const creators = Array.isArray(parsed.creators) ? parsed.creators : []
  // Older rows stored Zotero's own `DOI` key; read it without fabricating a shape.
  const legacyDoi = 'DOI' in parsed && typeof parsed.DOI === 'string' ? parsed.DOI : ''
  return {
    key: typeof parsed.key === 'string' ? parsed.key : row.zoteroKey,
    version: typeof parsed.version === 'number' ? parsed.version : 0,
    itemType: typeof parsed.itemType === 'string' ? parsed.itemType : '',
    title: typeof parsed.title === 'string' ? parsed.title : '',
    creators,
    date,
    publicationTitle: typeof parsed.publicationTitle === 'string' ? parsed.publicationTitle : '',
    doi: typeof parsed.doi === 'string' ? parsed.doi : legacyDoi,
    url: typeof parsed.url === 'string' ? parsed.url : '',
    abstractNote: typeof parsed.abstractNote === 'string' ? parsed.abstractNote : '',
    tags: Array.isArray(parsed.tags) ? parsed.tags : [],
    collections: Array.isArray(parsed.collections) ? parsed.collections : [],
    language: typeof parsed.language === 'string' ? parsed.language : '',
    extra: typeof parsed.extra === 'string' ? parsed.extra : '',
    year: yearOf(date),
    authors: authorsOf(creators)
  }
}

const FOUR_DIGITS = /[0-9]{4}/

/** First four-digit year in a Zotero date string. */
export function yearOf(date: string): number {
  const match = FOUR_DIGITS.exec(date)
  if (match === null) return 0
  const year = Number.parseInt(match[0], 10)
  return year >= 1000 && year <= 2999 ? year : 0
}

/** `Last, First` in creator order; single-field creators use their name. */
export function authorsOf(creators: readonly ZoteroCreator[]): string {
  const parts: string[] = []
  for (const creator of creators) {
    const last = (creator.lastName ?? '').trim()
    const first = (creator.firstName ?? '').trim()
    const name = (creator.name ?? '').trim()
    if (last !== '') parts.push(first === '' ? last : `${last}, ${first}`)
    else if (name !== '') parts.push(name)
    else if (first !== '') parts.push(first)
  }
  return parts.join(', ')
}