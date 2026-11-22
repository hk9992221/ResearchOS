/**
 * Zotero source selection: prefer the local API, fall back to
 * the read-only SQLite reader. The decision is made per call, never cached
 * forever; a failing API call is retried once against SQLite.
 */
import type { Clock } from '../../../clock'
import { appError, AppErrorCode } from '../../../errors'
import type {
  ItemPage,
  ListItemsOptions,
  MetadataSource,
  ResolvedFile,
  ZoteroAttachmentRecord,
  ZoteroItemRecord,
  ZoteroReader,
  ZoteroSource
} from '../types'
import type { ZoteroConnection } from './connection'
import { LocalApiReader } from './localapi'
import { createResolver } from './resolve'
import type { SqliteReader } from './sqlite'

export interface ZoteroSourceOptions {
  connection: ZoteroConnection
  clock: Clock
  fetch?: (input: string, init?: RequestInit) => Promise<Response>
}

function unavailable(message: string, cause?: unknown) {
  return appError(AppErrorCode.ExternalUnavailable, message, cause === undefined ? {} : { cause })
}

function resolverFor(connection: ZoteroConnection) {
  const dataDir = connection.effectiveDataDir()
  if (dataDir === null) throw unavailable('未找到 Zotero 数据目录')
  return createResolver({
    dataDir: dataDir.path,
    linkedBaseDir: connection.linkedAttachmentBaseDir()
  })
}

class FallbackReader implements ZoteroReader {
  private readonly owner: ZoteroSourceService
  private readonly primary: ZoteroReader
  private readonly fallback: SqliteReader

  constructor(owner: ZoteroSourceService, primary: ZoteroReader, fallback: SqliteReader) {
    this.owner = owner
    this.primary = primary
    this.fallback = fallback
  }

  source(): MetadataSource {
    return this.primary.source()
  }

  private async run<T>(call: (reader: ZoteroReader) => Promise<T>): Promise<T> {
    try {
      const value = await call(this.primary)
      this.owner.noteSuccess()
      return value
    } catch (error) {
      this.owner.noteFailure(error instanceof Error ? error.message : String(error))
      return call(this.fallback)
    }
  }

  listLibraries(): Promise<Array<{ id: string; name: string }>> {
    return this.run((reader) => reader.listLibraries())
  }

  listCollections(
    libraryId: string
  ): Promise<Array<{ key: string; parentKey: string; name: string; itemCount: number }>> {
    return this.run((reader) => reader.listCollections(libraryId))
  }

  listItems(libraryId: string, collectionKey: string, options: ListItemsOptions): Promise<ItemPage> {
    return this.run((reader) => reader.listItems(libraryId, collectionKey, options))
  }

  getItem(libraryId: string, key: string): Promise<ZoteroItemRecord> {
    return this.run((reader) => reader.getItem(libraryId, key))
  }

  listAttachments(libraryId: string, parentKey: string): Promise<ZoteroAttachmentRecord[]> {
    return this.run((reader) => reader.listAttachments(libraryId, parentKey))
  }

  resolveAttachment(attachment: ZoteroAttachmentRecord): Promise<ResolvedFile> {
    return this.run((reader) => reader.resolveAttachment(attachment))
  }
}

export class ZoteroSourceService implements ZoteroSource {
  private readonly connection: ZoteroConnection
  private readonly fetchImpl: ((input: string, init?: RequestInit) => Promise<Response>) | undefined
  private lastReason = ''

  constructor(options: ZoteroSourceOptions) {
    this.connection = options.connection
    this.fetchImpl = options.fetch
  }

  async reader(): Promise<ZoteroReader> {
    const probe = await this.connection.probeLocalApi()
    const sqlite = this.connection.createSqliteReader()
    if (probe.available) {
      const primary = new LocalApiReader({
        baseUrl: this.connection.localApiBaseUrl(),
        ...(this.fetchImpl === undefined ? {} : { fetch: this.fetchImpl }),
        resolver: resolverFor(this.connection)
      })
      this.lastReason = ''
      return sqlite === null ? primary : new FallbackReader(this, primary, sqlite)
    }
    if (sqlite !== null) {
      this.lastReason = probe.error || '本地 API 不可用'
      return sqlite
    }
    this.lastReason = probe.error || 'Zotero 未运行'
    throw unavailable(`Zotero 不可用：${this.lastReason}`)
  }

  async status(): Promise<{ mode: MetadataSource | 'unavailable'; reason: string }> {
    const probe = await this.connection.probeLocalApi()
    if (probe.available) return { mode: 'local_api', reason: this.lastReason }
    const dataDir = this.connection.effectiveDataDir()
    if (dataDir !== null && dataDir.valid) {
      return { mode: 'sqlite', reason: probe.error || this.lastReason }
    }
    return { mode: 'unavailable', reason: probe.error || this.lastReason }
  }

  noteFailure(reason: string): void {
    this.lastReason = reason
  }

  noteSuccess(): void {
    this.lastReason = ''
  }

  lastReasonText(): string {
    return this.lastReason
  }
}