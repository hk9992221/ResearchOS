/**
 * Zotero item → `papers.metadata` JSON. Unknown Zotero fields
 * survive because the writer starts from the reader's raw object; derived
 * display values (`year`, `authors`) are never written back.
 */
import type { JsonObject } from '@researchos/shared'

import type { PaperMetadata, ZoteroItemRecord, ZoteroTag } from '../types'
import { authorsOf, yearOf } from '../types'

const INTERNAL_KEYS: Readonly<Record<string, true>> = {
  Raw: true,
  raw: true,
  libraryID: true,
  libraryId: true,
  itemID: true,
  deleted: true,
  Deleted: true,
  attachmentKey: true,
  source: true,
  sourceState: true,
  fileState: true,
  filePath: true
}

export function toPaperMetadata(item: ZoteroItemRecord): PaperMetadata {
  const creators = item.creators.map((creator) => ({ ...creator }))
  return {
    key: item.key,
    version: item.version,
    itemType: item.itemType,
    title: item.title,
    creators,
    date: item.date,
    publicationTitle: item.publicationTitle,
    doi: item.doi,
    url: item.url,
    abstractNote: item.abstractNote,
    tags: item.tags.map((tag) => ({ ...tag })),
    collections: [...item.collections],
    language: item.language,
    extra: item.extra,
    year: yearOf(item.date),
    authors: authorsOf(creators)
  }
}

/** Compact JSON for `papers.metadata`; unknown Zotero fields are preserved. */
export function metadataJson(item: ZoteroItemRecord): string {
  const raw: JsonObject = {}
  for (const [key, value] of Object.entries(item.raw)) {
    if (INTERNAL_KEYS[key] !== true) raw[key] = value
  }
  const meta = toPaperMetadata(item)
  raw['key'] = meta.key
  raw['version'] = meta.version
  raw['itemType'] = meta.itemType
  raw['title'] = meta.title
  raw['creators'] = meta.creators
  raw['date'] = meta.date
  raw['publicationTitle'] = meta.publicationTitle
  raw['DOI'] = meta.doi
  raw['url'] = meta.url
  raw['abstractNote'] = meta.abstractNote
  raw['tags'] = meta.tags
  raw['collections'] = meta.collections
  raw['language'] = meta.language
  raw['extra'] = meta.extra
  return JSON.stringify(raw)
}

/** Tags win when the reader provided normalized names; raw objects fill gaps. */
export function normalizeTags(item: ZoteroItemRecord): ZoteroTag[] {
  if (item.tags.length > 0) return item.tags.map((tag) => ({ ...tag }))
  const raw = item.raw['tags']
  if (!Array.isArray(raw)) return []
  const out: ZoteroTag[] = []
  for (const entry of raw) {
    if (typeof entry === 'string') {
      out.push({ tag: entry, type: 0 })
      continue
    }
    if (entry !== null && typeof entry === 'object' && 'tag' in entry) {
      const name = entry.tag
      if (typeof name !== 'string' || name === '') continue
      const type = 'type' in entry && typeof entry.type === 'number' ? entry.type : 0
      out.push({ tag: name, type })
    }
  }
  return out
}