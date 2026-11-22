/**
 * Literature IPC adapter. Every channel the feature uses is named here so the
 * components never carry channel strings and the request shapes stay the
 * shared contract's.
 */

import type {
  AnnotationInput,
  McpJob,
  ImportPreview,
  ImportReport,
  ImportSelection,
  LiteratureOverview,
  PaperAnnotation,
  PaperListQuery,
  PaperNoteFields,
  PaperNoteView,
  PaperPage,
  PaperView,
  SyncApplyResult,
  SyncDecision,
  SyncReport,
  ZoteroCollection,
  ZoteroItemPage,
  ZoteroLibrary,
  ZoteroStatus
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export interface ZoteroItemsRequest {
  readonly libraryId: string
  readonly collectionKey?: string
  readonly query?: string
  readonly limit?: number
  readonly offset?: number
}

export const literatureApi = {
  zoteroStatus: (refresh = false): Promise<ZoteroStatus> =>
    ipc.invoke('literature.zotero.status', { refresh }),

  zoteroLibraries: (): Promise<ZoteroLibrary[]> => ipc.invoke('literature.zotero.libraries'),

  zoteroCollections: (libraryId: string): Promise<ZoteroCollection[]> =>
    ipc.invoke('literature.zotero.collections', { libraryId }),

  zoteroItems: (request: ZoteroItemsRequest): Promise<ZoteroItemPage> =>
    ipc.invoke('literature.zotero.items', {
      libraryId: request.libraryId,
      ...(request.collectionKey === undefined ? {} : { collectionKey: request.collectionKey }),
      ...(request.query === undefined ? {} : { query: request.query }),
      ...(request.limit === undefined ? {} : { limit: request.limit }),
      ...(request.offset === undefined ? {} : { offset: request.offset })
    }),

  previewImport: (projectId: string, selection: ImportSelection): Promise<ImportPreview> =>
    ipc.invoke('literature.import.preview', { projectId, selection }),

  startImport: (projectId: string, selection: ImportSelection): Promise<{ jobId: string }> =>
    ipc.invoke('literature.import.start', { projectId, selection }),

  importReport: (jobId: string): Promise<ImportReport> =>
    ipc.invoke('literature.import.report', { jobId }),

  papers: (query: PaperListQuery): Promise<PaperPage> =>
    ipc.invoke('literature.papers.list', query),

  paper: (projectId: string, paperId: string): Promise<PaperView> =>
    ipc.invoke('literature.papers.get', { projectId, paperId }),

  unlink: (projectId: string, paperId: string): Promise<{ unlinked: boolean; detachedNoteIds: string[] }> =>
    ipc.invoke('literature.papers.unlink', { projectId, paperId }),

  paperNote: (projectId: string, paperId: string): Promise<PaperNoteView | null> =>
    ipc.invoke('literature.paperNote.get', { projectId, paperId }),

  createPaperNote: (projectId: string, paperId: string): Promise<PaperNoteView> =>
    ipc.invoke('literature.paperNote.create', { projectId, paperId }),

  savePaperNoteFields: (
    projectId: string,
    paperId: string,
    fields: PaperNoteFields,
    expectedUpdatedAt?: string
  ): Promise<PaperNoteView> =>
    ipc.invoke('literature.paperNote.saveFields', {
      projectId,
      paperId,
      fields,
      ...(expectedUpdatedAt === undefined ? {} : { expectedUpdatedAt })
    }),

  savePaperNoteBody: (
    projectId: string,
    paperId: string,
    markdown: string,
    baseHash: string
  ): Promise<{ fileHash: string; savedAt: string }> =>
    ipc.invoke('literature.paperNote.saveBody', { projectId, paperId, markdown, baseHash }),

  overview: (projectId: string): Promise<LiteratureOverview | null> =>
    ipc.invoke('literature.overview.get', { projectId }),

  saveOverview: (
    projectId: string,
    markdown: string,
    baseHash?: string
  ): Promise<{ fileHash: string; savedAt: string }> =>
    ipc.invoke('literature.overview.save', {
      projectId,
      markdown,
      ...(baseHash === undefined || baseHash === '' ? {} : { baseHash })
    }),

  syncCheck: (projectId: string): Promise<SyncReport> =>
    ipc.invoke('literature.sync.check', { projectId }),

  syncApply: (projectId: string, decisions: SyncDecision[]): Promise<SyncApplyResult> =>
    ipc.invoke('literature.sync.apply', { projectId, decisions }),

  refreshMetadata: (projectId: string): Promise<{ jobId: string }> =>
    ipc.invoke('literature.sync.refreshMetadata', { projectId }),

  annotations: (projectId: string, paperId: string): Promise<PaperAnnotation[]> =>
    ipc.invoke('literature.annotations.list', { projectId, paperId }),

  createAnnotation: (
    projectId: string,
    paperId: string,
    annotation: AnnotationInput
  ): Promise<PaperAnnotation> =>
    ipc.invoke('literature.annotations.create', { projectId, paperId, annotation }),

  updateAnnotation: (
    projectId: string,
    annotationId: string,
    patch: { noteId?: string | null; evidenceId?: string | null }
  ): Promise<PaperAnnotation> =>
    ipc.invoke('literature.annotations.update', { projectId, annotationId, ...patch }),

  deleteAnnotation: (projectId: string, annotationId: string): Promise<{ deleted: boolean }> =>
    ipc.invoke('literature.annotations.delete', { projectId, annotationId }),

  /** Generic job inspection, used by the metadata refresh poll. */
  job: (jobId: string): Promise<McpJob> => ipc.invoke('jobs.get', { jobId })
}
