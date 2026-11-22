/**
 * Literature service facade: Zotero browsing, import, project papers, Paper
 * Notes, Literature Overview, manual sync and PDF annotations. IPC and MCP both
 * call this class; neither reaches into the libraries directly.
 */
import type {
  ImportPreview,
  ImportReport,
  ImportSelection,
  LiteratureOverview,
  OverviewComparisonRow,
  PaperAnnotation,
  PaperNoteFields,
  PaperNoteView,
  PaperPage,
  PaperView,
  ReferenceResolution,
  SyncApplyResult,
  SyncDecision,
  SyncReport,
  ZoteroCollection,
  ZoteroItem,
  ZoteroItemPage,
  ZoteroLibrary,
  ZoteroStatus
} from '@researchos/shared'

import type { Actor } from '../../schema'
import { appError, AppErrorCode } from '../../errors'
import type { LiteratureContextOptions, ProjectInfo } from './context'
import { LiteratureContext } from './context'
import {
  createAnnotation,
  deleteAnnotation,
  listAnnotations,
  updateAnnotation,
  type AnnotationPatch,
  type CreateAnnotationInput
} from './annotations'
import {
  findPaperNoteRow,
  findPaperNoteRowById,
  getPaperNote,
  createPaperNote,
  savePaperNoteBody,
  updatePaperNote,
  type PaperNotePatch
} from './library/papernote'
import {
  comparison,
  getOverview,
  resolveReferences,
  saveOverview
} from './library/overview'
import {
  getPaper,
  getPaperRow,
  linkPaper,
  listGlobalPapers,
  listPaperProjects,
  projectPapers,
  reconcilePaper,
  unlinkPaper
} from './library/query'
import {
  previewImport,
  runImport,
  type ImportSelectionInternal
} from './library/import'
import { refreshMetadata, syncApply, syncCheck, type MetadataRefreshReport } from './library/sync'
import { LiteratureJobStore } from './jobs'
import { authorsOf, type ZoteroAttachmentRecord, type ZoteroItemRecord, type ZoteroReader } from './types'
import { toPaperMetadata } from './zotero/metadata'
import type { ZoteroConnection } from './zotero/connection'

export interface LiteratureModuleOptions extends LiteratureContextOptions {
  connection?: ZoteroConnection
  jobs?: LiteratureJobStore
}

function unavailableStatus(message: string): ZoteroStatus {
  return {
    available: false,
    mode: 'unavailable',
    dataDir: null,
    localApiUrl: 'http://127.0.0.1:23119',
    profileDir: null,
    databasePath: null,
    storageDir: null,
    lastError: message,
    diagnostics: [message]
  }
}

function toPublicSelection(selection: ImportSelection): ImportSelectionInternal {
  if (selection.kind === 'items') {
    return {
      libraryId: selection.libraryId,
      collectionKeys: [],
      includeSubcollections: false,
      itemKeys: [...selection.itemKeys],
      attachmentChoices: {}
    }
  }
  return {
    libraryId: selection.libraryId,
    collectionKeys: [selection.collectionKey],
    includeSubcollections: selection.includeSubcollections === true,
    itemKeys: [],
    attachmentChoices: {}
  }
}

function itemView(
  item: ZoteroItemRecord,
  attachments: readonly ZoteroAttachmentRecord[]
): ZoteroItem {
  const meta = toPaperMetadata(item)
  return {
    libraryId: item.libraryId,
    key: item.key,
    itemType: item.itemType,
    title: item.title,
    creators: item.creators.map((creator) => authorsOf([creator])),
    year: meta.year === 0 ? null : String(meta.year),
    doi: item.doi === '' ? null : item.doi,
    collections: [...item.collections],
    attachments: attachments.map((attachment) => ({
      key: attachment.key,
      title: attachment.filename,
      contentType: attachment.contentType === '' ? null : attachment.contentType,
      filename: attachment.filename === '' ? null : attachment.filename,
      resolvable: attachment.size > 0
    })),
    metadata: item.raw
  }
}

export class LiteratureModule {
  readonly ctx: LiteratureContext
  readonly jobs: LiteratureJobStore
  private readonly connection: ZoteroConnection | undefined

  constructor(options: LiteratureModuleOptions) {
    this.ctx = new LiteratureContext(options)
    this.connection = options.connection
    this.jobs = options.jobs ?? new LiteratureJobStore(this.ctx.clock)
  }

  context(): LiteratureContext {
    return this.ctx
  }

  /* ------------------------------------------------------------- Zotero */

  async zoteroStatus(): Promise<ZoteroStatus> {
    if (this.connection === undefined) {
      return unavailableStatus('Zotero 未配置')
    }
    return this.connection.status()
  }

  private async reader(): Promise<ZoteroReader> {
    return this.ctx.requireZotero().reader()
  }

  async zoteroLibraries(): Promise<ZoteroLibrary[]> {
    const reader = await this.reader()
    const libraries = await reader.listLibraries()
    return libraries.map((library) => ({
      id: library.id,
      name: library.name,
      type: 'user' as const,
      itemCount: null
    }))
  }

  async zoteroCollections(libraryId: string): Promise<ZoteroCollection[]> {
    const reader = await this.reader()
    const collections = await reader.listCollections(libraryId)
    return collections.map((collection) => ({
      libraryId: libraryId === '' ? '0' : libraryId,
      key: collection.key,
      name: collection.name,
      parentKey: collection.parentKey === '' ? null : collection.parentKey,
      itemCount: collection.itemCount
    }))
  }

  async zoteroItems(input: {
    libraryId: string
    collectionKey?: string
    query?: string
    limit?: number
    offset?: number
  }): Promise<ZoteroItemPage> {
    const reader = await this.reader()
    const limit = Math.min(Math.max(1, Math.trunc(input.limit ?? 50)), 100)
    const offset = Math.max(0, Math.trunc(input.offset ?? 0))
    const page = await reader.listItems(input.libraryId, input.collectionKey ?? '', {
      offset,
      limit,
      query: input.query ?? ''
    })
    const items: ZoteroItem[] = []
    for (const item of page.items) {
      const attachments = await reader.listAttachments(input.libraryId, item.key)
      items.push(itemView(item, attachments))
    }
    const next = offset + page.items.length
    return {
      items,
      total: page.total,
      nextCursor: page.total > next ? String(next) : null
    }
  }

  /* ------------------------------------------------------------- import */

  previewImport(projectId: string, selection: ImportSelection): Promise<ImportPreview> {
    return previewImport(this.ctx, projectId, toPublicSelection(selection))
  }

  startImport(projectId: string, selection: ImportSelection, actor: Actor = 'user'): { jobId: string } {
    const internal = toPublicSelection(selection)
    const jobId = this.jobs.startImport((progress) =>
      runImport(this.ctx, projectId, internal, actor, progress)
    )
    return { jobId }
  }

  importReport(jobId: string): ImportReport {
    return this.jobs.report(jobId)
  }

  /** MCP import shape: item keys and collections may be combined. */
  startMcpImport(
    projectId: string,
    input: {
      libraryId: string
      collectionKeys: string[]
      includeSubcollections: boolean
      itemKeys: string[]
      attachmentChoices: Record<string, string>
    },
    actor: Actor = 'mcp'
  ): { jobId: string } {
    const selection: ImportSelectionInternal = {
      libraryId: input.libraryId === '' ? '0' : input.libraryId,
      collectionKeys: input.collectionKeys,
      includeSubcollections: input.includeSubcollections,
      itemKeys: input.itemKeys,
      attachmentChoices: input.attachmentChoices
    }
    const jobId = this.jobs.startImport((progress) =>
      runImport(this.ctx, projectId, selection, actor, progress)
    )
    return { jobId }
  }

  /* ------------------------------------------------------------- papers */

  listPapers(query: {
    projectId: string
    q?: string
    fileState?: 'ok' | 'missing'
    sourceState?: 'ok' | 'missing' | 'unreadable'
    hasNote?: boolean
    limit?: number
    offset?: number
  }): PaperPage {
    return projectPapers(this.ctx, query)
  }

  listGlobalPapers(query: {
    q?: string
    importedAfter?: string
    limit?: number
    cursor?: string | null
  }): { items: PaperView[]; nextCursor: string | null } {
    return listGlobalPapers(this.ctx, query)
  }

  getPaper(paperId: string, projectId?: string): PaperView {
    return getPaper(this.ctx, paperId, projectId)
  }

  linkPaper(projectId: string, paperId: string, actor: Actor = 'user'): PaperView {
    return linkPaper(this.ctx, projectId, paperId, actor)
  }

  unlinkPaper(projectId: string, paperId: string): { unlinked: boolean; detachedNoteIds: string[] } {
    return unlinkPaper(this.ctx, projectId, paperId)
  }

  isPaperLinked(projectId: string, paperId: string): boolean {
    const row = this.ctx.workspace
      .project(projectId)
      .db.prepare(`SELECT 1 AS one FROM project_papers WHERE project_id = ? AND paper_id = ?`)
      .get(projectId, paperId) as { one: number } | undefined
    return row !== undefined
  }

  paperProjects(paperId: string): Array<{ projectId: string; name: string; status: string }> {
    return listPaperProjects(this.ctx, paperId)
  }

  reconcilePaper(paperId: string) {
    return reconcilePaper(this.ctx, paperId)
  }

  /* --------------------------------------------------------- Paper Note */

  getPaperNote(projectId: string, paperId: string): PaperNoteView | null {
    return getPaperNote(this.ctx, projectId, paperId)
  }

  createPaperNote(projectId: string, paperId: string, actor: Actor = 'user'): PaperNoteView {
    return createPaperNote(this.ctx, projectId, paperId, actor)
  }

  savePaperNoteFields(
    projectId: string,
    paperId: string,
    patch: PaperNotePatch,
    expectedUpdatedAt: string | undefined,
    actor: Actor = 'user'
  ): PaperNoteView {
    const row = findPaperNoteRow(this.ctx, projectId, paperId)
    if (row === null) {
      throw appError(AppErrorCode.NotFound, '该论文还没有 Paper Note', {
        details: { paper_id: paperId }
      })
    }
    return updatePaperNote(this.ctx, projectId, row.id, patch, expectedUpdatedAt, actor)
  }

  savePaperNoteBody(
    projectId: string,
    paperId: string,
    markdown: string,
    baseHash: string,
    actor: Actor = 'user'
  ): { fileHash: string; savedAt: string; note: PaperNoteView | null } {
    const row = findPaperNoteRow(this.ctx, projectId, paperId)
    if (row === null) {
      throw appError(AppErrorCode.NotFound, '该论文还没有 Paper Note', {
        details: { paper_id: paperId }
      })
    }
    return savePaperNoteBody(this.ctx, projectId, row.id, markdown, baseHash, actor)
  }

  /** Locates the project holding a Paper Note; MCP tools carry only note_id. */
  projectForNote(noteId: string): string {
    for (const projectId of this.ctx.listProjectIds()) {
      try {
        if (findPaperNoteRowById(this.ctx, projectId, noteId) !== null) return projectId
      } catch {
        continue
      }
    }
    throw appError(AppErrorCode.NotFound, `Paper Note ${noteId} 不存在`, {
      details: { note_id: noteId }
    })
  }

  paperNoteByNoteId(noteId: string): { projectId: string; view: PaperNoteView } {
    const projectId = this.projectForNote(noteId)
    const row = findPaperNoteRowById(this.ctx, projectId, noteId)
    if (row === null) {
      throw appError(AppErrorCode.NotFound, `Paper Note ${noteId} 不存在`, {
        details: { note_id: noteId }
      })
    }
    const view = this.getPaperNote(projectId, row.paper_id ?? '')
    if (view === null) {
      throw appError(AppErrorCode.NotFound, `Paper Note ${noteId} 不存在`, {
        details: { note_id: noteId }
      })
    }
    return { projectId, view }
  }

  updatePaperNoteByNoteId(
    noteId: string,
    patch: PaperNotePatch,
    expectedUpdatedAt: string | undefined,
    actor: Actor = 'mcp'
  ): { projectId: string; view: PaperNoteView } {
    const projectId = this.projectForNote(noteId)
    const view = updatePaperNote(this.ctx, projectId, noteId, patch, expectedUpdatedAt, actor)
    return { projectId, view }
  }

  savePaperNoteBodyByNoteId(
    noteId: string,
    markdown: string,
    baseHash: string,
    actor: Actor = 'mcp'
  ): { projectId: string; fileHash: string; savedAt: string; view: PaperNoteView | null } {
    const projectId = this.projectForNote(noteId)
    const result = savePaperNoteBody(this.ctx, projectId, noteId, markdown, baseHash, actor)
    return { projectId, view: result.note, fileHash: result.fileHash, savedAt: result.savedAt }
  }

  /* ----------------------------------------------------------- overview */

  getOverview(projectId: string): LiteratureOverview | null {
    return getOverview(this.ctx, projectId)
  }

  saveOverview(
    projectId: string,
    markdown: string,
    baseHash: string | undefined,
    actor: Actor = 'user'
  ): { fileHash: string; savedAt: string; overview: LiteratureOverview } {
    return saveOverview(this.ctx, projectId, markdown, baseHash, actor)
  }

  comparison(projectId: string, paperIds?: string[]): OverviewComparisonRow[] {
    return comparison(this.ctx, projectId, paperIds)
  }

  resolveReferences(projectId: string, markdown: string): ReferenceResolution[] {
    return resolveReferences(this.ctx, projectId, markdown)
  }

  /* --------------------------------------------------------------- sync */

  syncCheck(scope: { projectId?: string; paperIds?: string[] }): Promise<SyncReport> {
    return syncCheck(this.ctx, scope)
  }

  syncApply(projectId: string, decisions: SyncDecision[], actor: Actor = 'user'): Promise<SyncApplyResult> {
    return syncApply(this.ctx, projectId, decisions, actor)
  }

  /** MCP confirmation executor: re-checks the hash stored in the confirmation. */
  applyConfirmedOverwrite(
    projectId: string,
    paperId: string,
    expectedNewHash: string
  ): Promise<SyncApplyResult> {
    return syncApply(
      this.ctx,
      projectId,
      [{ paperId, action: 'overwrite' }],
      'user',
      { [paperId]: expectedNewHash }
    )
  }

  startRefreshMetadata(scope: { projectId?: string; paperIds?: string[] }): { jobId: string } {
    const jobId = this.jobs.startRun('refresh_metadata', () =>
      refreshMetadata(this.ctx, scope)
    )
    return { jobId }
  }

  refreshMetadata(scope: { projectId?: string; paperIds?: string[] }): Promise<MetadataRefreshReport> {
    return refreshMetadata(this.ctx, scope)
  }

  /* --------------------------------------------------------- annotations */

  createAnnotation(
    projectId: string,
    paperId: string,
    input: CreateAnnotationInput,
    actor: Actor = 'user'
  ): PaperAnnotation {
    return createAnnotation(this.ctx, projectId, paperId, input)
  }

  listAnnotations(projectId: string, paperId: string): PaperAnnotation[] {
    return listAnnotations(this.ctx, projectId, paperId)
  }

  updateAnnotation(projectId: string, annotationId: string, patch: AnnotationPatch): PaperAnnotation {
    return updateAnnotation(this.ctx, projectId, annotationId, patch)
  }

  deleteAnnotation(projectId: string, annotationId: string): { deleted: boolean } {
    return deleteAnnotation(this.ctx, projectId, annotationId)
  }

  /* ------------------------------------------------------------ helpers */

  projectInfo(projectId: string): ProjectInfo {
    return this.ctx.projectInfo(projectId)
  }

  paperRow(paperId: string) {
    return getPaperRow(this.ctx, paperId)
  }
}