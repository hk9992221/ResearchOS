/**
 * Zotero 7 local HTTP API reader (plan §4.3). GET only, serial pagination,
 * 1..100 page size, a 3 s read deadline. The user library is `/api/users/0`;
 * v1 addresses only the personal library.
 */
import * as path from 'node:path'

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
import type { AttachmentResolver } from './resolve'

export const DEFAULT_LOCAL_API_BASE_URL = 'http://127.0.0.1:23119'
export const LOCAL_API_PROBE_TIMEOUT_MS = 1000
export const LOCAL_API_READ_TIMEOUT_MS = 3000
const PAGE_LIMIT = 100

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>

interface ApiObject {
  key?: unknown
  version?: unknown
  data?: unknown
  links?: unknown
  meta?: unknown
}

export interface LocalApiOptions {
  baseUrl?: string
  fetch?: FetchLike
  resolver: AttachmentResolver
  timeoutMs?: number
}

/** Appends `/api/users/0` when the base stops at the port or `/api`. */
export function normalizeUsersBase(baseUrl: string): string {
  let base = (baseUrl || DEFAULT_LOCAL_API_BASE_URL).trim().replace(/\/+$/, '')
  if (base === '') base = DEFAULT_LOCAL_API_BASE_URL
  const lower = base.toLowerCase()
  if (lower.includes('/api/users/')) return base
  if (lower.endsWith('/api/users')) return `${base}/0`
  if (lower.endsWith('/api')) return `${base}/users/0`
  return `${base}/api/users/0`
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function integer(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : 0
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((entry): entry is string => typeof entry === 'string')
}

function creators(value: unknown): ZoteroCreator[] {
  if (!Array.isArray(value)) return []
  const out: ZoteroCreator[] = []
  for (const entry of value) {
    const data = record(entry)
    out.push({
      firstName: text(data['firstName']),
      lastName: text(data['lastName']),
      ...(text(data['name']) === '' ? {} : { name: text(data['name']) }),
      creatorType: text(data['creatorType'])
    })
  }
  return out
}

function tagNames(value: unknown): ZoteroTag[] {
  if (!Array.isArray(value)) return []
  const out: ZoteroTag[] = []
  for (const entry of value) {
    if (typeof entry === 'string') {
      out.push({ tag: entry, type: 0 })
      continue
    }
    const data = record(entry)
    const name = text(data['tag'])
    if (name === '') continue
    out.push({ tag: name, type: integer(data['type']) })
  }
  return out
}

function linkModeName(value: unknown): string {
  if (typeof value === 'string') return value
  switch (integer(value)) {
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

function isPdf(contentType: string, filename: string, filePath: string): boolean {
  if (contentType.trim().toLowerCase() === 'application/pdf') return true
  for (const candidate of [filename, filePath]) {
    if (candidate.trim().toLowerCase().endsWith('.pdf')) return true
  }
  return false
}

/** Recovers the local location from Zotero 7's root `links` map. */
function attachmentHref(links: unknown): string {
  const map = record(links)
  for (const name of ['enclosure', 'linkedFile', 'importedFile']) {
    const link = record(map[name])
    const href = text(link['href']).trim()
    if (href !== '') return localPathFromHref(href)
  }
  return ''
}

function localPathFromHref(href: string): string {
  if (!href.toLowerCase().startsWith('file:')) return href
  try {
    const url = new URL(href)
    if (url.host !== '' && url.host.toLowerCase() !== 'localhost') return href
    let value = decodeURIComponent(url.pathname)
    if (/^\/[A-Za-z]:/.test(value)) value = value.slice(1)
    return value
  } catch {
    return href
  }
}

function itemFromApi(value: unknown, fallbackKey: string, libraryId: string): ZoteroItemRecord {
  const object = record(value)
  const data = record(object['data'])
  const key = text(data['key']) || text(object['key']) || fallbackKey
  const raw: Record<string, unknown> = { ...data }
  return {
    key,
    libraryId,
    version: integer(data['version']) || integer(object['version']),
    itemType: text(data['itemType']),
    title: text(data['title']),
    creators: creators(data['creators']),
    date: text(data['date']),
    doi: text(data['DOI']),
    url: text(data['url']),
    abstractNote: text(data['abstractNote']),
    publicationTitle: text(data['publicationTitle']),
    tags: tagNames(data['tags']),
    collections: stringList(data['collections']),
    language: text(data['language']),
    extra: text(data['extra']),
    deleted: data['deleted'] === true,
    raw
  }
}

function timeoutSignal(signal: AbortSignal | undefined, ms: number): AbortSignal {
  const timeout = AbortSignal.timeout(ms)
  return signal === undefined ? timeout : AbortSignal.any([signal, timeout])
}

export class LocalApiReader implements ZoteroReader {
  private readonly base: string
  private readonly fetchImpl: FetchLike
  private readonly resolver: AttachmentResolver
  private readonly timeoutMs: number

  constructor(options: LocalApiOptions) {
    this.base = normalizeUsersBase(options.baseUrl ?? DEFAULT_LOCAL_API_BASE_URL)
    this.fetchImpl = options.fetch ?? ((input, init) => fetch(input, init))
    this.resolver = options.resolver
    this.timeoutMs = options.timeoutMs ?? LOCAL_API_READ_TIMEOUT_MS
  }

  baseUrl(): string {
    return this.base
  }

  source(): MetadataSource {
    return 'local_api'
  }

  async listLibraries(): Promise<Array<{ id: string; name: string }>> {
    return [{ id: '0', name: '我的文库' }]
  }

  async listCollections(
    libraryId: string
  ): Promise<Array<{ key: string; parentKey: string; name: string; itemCount: number }>> {
    assertPersonalLibrary(libraryId)
    const out: Array<{ key: string; parentKey: string; name: string; itemCount: number }> = []
    let start = 0
    for (let page = 0; page < 1000; page += 1) {
      const { body, total } = await this.getJson(
        `/collections?${pageQuery(start, PAGE_LIMIT)}`
      )
      const objects = Array.isArray(body) ? body : []
      for (const object of objects) {
        const data = record(record(object)['data'])
        const key = text(data['key']) || text(record(object)['key'])
        if (key === '') continue
        out.push({
          key,
          parentKey: text(data['parentCollection']),
          name: text(data['name']),
          itemCount: integer(record(record(object)['meta'])['numItems'])
        })
      }
      if (objects.length < PAGE_LIMIT) break
      start += objects.length
      if (total !== null && start >= total) break
    }
    return out
  }

  async listItems(
    libraryId: string,
    collectionKey: string,
    options: ListItemsOptions
  ): Promise<ItemPage> {
    assertPersonalLibrary(libraryId)
    const limit = options.limit <= 0 || options.limit > PAGE_LIMIT ? PAGE_LIMIT : options.limit
    const offset = Math.max(0, options.offset)
    const prefix =
      collectionKey.trim() === ''
        ? '/items/top'
        : `/collections/${encodeURIComponent(collectionKey.trim())}/items/top`
    const { body, total } = await this.getJson(
      `${prefix}?${pageQuery(offset, limit, options.query)}`
    )
    const objects = Array.isArray(body) ? body : []
    return {
      items: objects.map((object) => itemFromApi(object, '', '0')),
      total: total ?? offset + objects.length,
      source: 'local_api'
    }
  }

  async getItem(libraryId: string, key: string): Promise<ZoteroItemRecord> {
    assertPersonalLibrary(libraryId)
    const { body } = await this.getJson(`/items/${encodeURIComponent(key)}`)
    return itemFromApi(body, key, '0')
  }

  async listAttachments(libraryId: string, parentKey: string): Promise<ZoteroAttachmentRecord[]> {
    assertPersonalLibrary(libraryId)
    const out: ZoteroAttachmentRecord[] = []
    let start = 0
    for (let page = 0; page < 1000; page += 1) {
      const { body, total } = await this.getJson(
        `/items/${encodeURIComponent(parentKey)}/children?${pageQuery(start, PAGE_LIMIT)}`
      )
      const objects = Array.isArray(body) ? body : []
      for (const object of objects) {
        const map = record(object)
        const data = record(map['data'])
        if (text(data['itemType']) !== 'attachment') continue
        let filename = text(data['filename'])
        if (filename === '') filename = text(data['title'])
        let filePath = text(data['path']).trim()
        if (filePath === '') filePath = attachmentHref(map['links'])
        const contentType = text(data['contentType'])
        if (!isPdf(contentType, filename, filePath)) continue
        const key = text(data['key']) || text(map['key'])
        if (key === '') continue
        const attachment: ZoteroAttachmentRecord = {
          key,
          parentKey: text(data['parentItem']) || parentKey,
          libraryId: '0',
          filename,
          contentType,
          linkMode: linkModeName(data['linkMode']),
          path: filePath,
          size: integer(data['size'])
        }
        try {
          attachment.size = this.resolver.resolve(attachment).size
        } catch {
          // Unresolvable attachments stay in the list; the caller reports them.
        }
        out.push(attachment)
      }
      if (objects.length < PAGE_LIMIT) break
      start += objects.length
      if (total !== null && start >= total) break
    }
    return out
  }

  async resolveAttachment(attachment: ZoteroAttachmentRecord): Promise<ResolvedFile> {
    return this.resolver.resolve(attachment)
  }

  private async getJson(
    pathAndQuery: string
  ): Promise<{ body: unknown; total: number | null }> {
    let response: Response
    try {
      response = await this.fetchImpl(`${this.base}${pathAndQuery}`, {
        method: 'GET',
        headers: { Accept: 'application/json' },
        signal: timeoutSignal(undefined, this.timeoutMs)
      })
    } catch (cause) {
      throw appError(AppErrorCode.ExternalUnavailable, `Zotero 本地 API 不可用：${String(cause)}`, {
        cause
      })
    }
    if (response.status === 403) {
      throw appError(
        AppErrorCode.ExternalUnavailable,
        '本地 API 未启用：请在 Zotero 设置中允许其他应用通信'
      )
    }
    if (response.status === 404) {
      throw appError(AppErrorCode.NotFound, 'Zotero 条目不存在')
    }
    if (response.status !== 200) {
      throw appError(
        AppErrorCode.ExternalUnavailable,
        `Zotero 本地 API 返回 HTTP ${response.status}`
      )
    }
    const rawTotal = response.headers.get('total-results')
    const total = rawTotal === null ? null : Number.parseInt(rawTotal, 10)
    return {
      body: await response.json(),
      total: total !== null && Number.isFinite(total) && total >= 0 ? total : null
    }
  }
}

function pageQuery(start: number, limit: number, query = ''): string {
  const params = new URLSearchParams({ start: String(start), limit: String(limit) })
  if (query.trim() !== '') params.set('q', query.trim())
  return params.toString()
}

export function assertPersonalLibrary(libraryId: string): void {
  const id = libraryId.trim()
  if (id !== '' && id !== '0') {
    throw appError(AppErrorCode.InvalidArgument, `v1 仅支持个人库（library_id="0"），收到 ${libraryId}`, {
      details: { field: 'library_id' }
    })
  }
}

/** Filename recovered from a Zotero path value. */
export function attachmentFilename(filePath: string): string {
  let value = filePath.trim()
  const lower = value.toLowerCase()
  if (lower.startsWith('storage:')) value = value.slice('storage:'.length)
  else if (lower.startsWith('attachments:')) value = value.slice('attachments:'.length)
  value = value.trim()
  return value === '' ? '' : path.basename(value)
}