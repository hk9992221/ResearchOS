import type { Note, Paper, PaperFileState, PaperSourceState } from '../entities'
import type { IsoDateTime, JsonObject, Locator, Page, Rect } from '../types/common'

/* --------------------------------------------------------------- Zotero */

export interface ZoteroStatus {
  available: boolean
  mode: 'local_api' | 'sqlite' | 'unavailable'
  /** Effective data directory, even when invalid (the UI explains why). */
  dataDir: string | null
  localApiUrl: string
  profileDir: string | null
  databasePath: string | null
  storageDir: string | null
  lastError: string | null
  diagnostics: string[]
}

export interface ZoteroLibrary {
  id: string
  name: string
  type: 'user' | 'group'
  itemCount: number | null
}

export interface ZoteroCollection {
  libraryId: string
  key: string
  name: string
  parentKey: string | null
  itemCount: number
}

export interface ZoteroAttachment {
  key: string
  title: string
  contentType: string | null
  filename: string | null
  resolvable: boolean
}

export interface ZoteroCreator {
  firstName: string
  lastName: string
  name?: string
  creatorType: string
}

export interface ZoteroItem {
  libraryId: string
  key: string
  itemType: string
  title: string
  creators: string[]
  year: string | null
  doi: string | null
  collections: string[]
  attachments: ZoteroAttachment[]
  metadata: JsonObject
}

export interface ZoteroItemPage {
  items: ZoteroItem[]
  total: number
  nextCursor: string | null
}

/* --------------------------------------------------------------- import */

export type ImportSelection =
  | { kind: 'items'; libraryId: string; itemKeys: string[] }
  | { kind: 'collection'; libraryId: string; collectionKey: string; includeSubcollections?: boolean }

export interface ImportPreviewEntry {
  libraryId: string
  key: string
  title: string
  creators: string[]
  year: number | null
  alreadyImported: boolean
  pdfFound: boolean
  attachmentKey: string | null
  warnings: string[]
}

export interface ImportPreview {
  entries: ImportPreviewEntry[]
  importable: number
  alreadyPresent: number
  missingPdf: number
  errors: Array<{ key: string; message: string }>
}

export type ImportOutcomeStatus = 'imported' | 'reused' | 'skipped' | 'needs_attachment' | 'failed'

export interface ImportOutcome {
  libraryId: string
  key: string
  paperId: string | null
  status: ImportOutcomeStatus
  warnings: string[]
  error: string | null
}

export interface ImportReport {
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  progress: number
  outcomes: ImportOutcome[]
  imported: number
  reused: number
  skipped: number
  failed: number
  warnings: string[]
  error: string | null
}

/* ------------------------------------------------------------- papers */

export interface PaperListQuery {
  projectId: string
  q?: string
  fileState?: PaperFileState
  sourceState?: PaperSourceState
  /** Only project queries: filter by the presence of a live Paper Note. */
  hasNote?: boolean
  limit?: number
  offset?: number
}

export interface ProjectPaper extends Paper {
  linkedAt: IsoDateTime
  noteId: string | null
  broken: boolean
}

export interface PaperPage extends Page<ProjectPaper> {}

export interface PaperView extends Paper {
  title: string
  authors: string
  year: number | null
  doi: string | null
  noteId: string | null
  linkedAt: IsoDateTime | null
  broken: boolean
}

/* --------------------------------------------------------- paper note */

export interface PaperNoteFields {
  summary: string
  problem: string
  method: string
  contribution: string
  limitations: string
  understanding: string
}

export const EMPTY_PAPER_NOTE_FIELDS: PaperNoteFields = {
  summary: '',
  problem: '',
  method: '',
  contribution: '',
  limitations: '',
  understanding: ''
}

export interface PaperNoteView {
  note: Note
  fields: PaperNoteFields
  bodyMd: string
  bodyHash: string | null
}

export interface PaperNoteRef {
  projectId: string
  noteId: string
  title: string
  updatedAt: IsoDateTime
  deleted: boolean
}

/* ----------------------------------------------------------- overview */

export interface LiteratureOverview {
  note: Note
  markdown: string
  bodyHash: string | null
  referencedNoteIds: string[]
}

export interface OverviewComparisonRow {
  paperId: string
  title: string
  year: number | null
  venue: string
  authors: string
  sourceState: PaperSourceState
  noteId: string | null
  fields: PaperNoteFields
}

export interface ReferenceResolution {
  raw: string
  entityType: string
  entityId: string
  title: string
  exists: boolean
}

/* --------------------------------------------------------------- sync */

export interface SyncItem {
  paperId: string
  title: string
  zoteroKey: string
  localHash: string | null
  sourceHash: string | null
  localSize: number | null
  sourceSize: number | null
  changed: boolean
  missing: boolean
  error: string | null
  warnings: string[]
}

export interface SyncReport {
  checked: number
  items: SyncItem[]
  source: 'local_api' | 'sqlite'
  checkedAt: IsoDateTime
}

export interface SyncDecision {
  paperId: string
  action: 'overwrite' | 'skip'
}

export interface SyncApplyResult {
  applied: number
  skipped: number
  conflicted: string[]
  failures: Array<{ paperId: string; error: string }>
}

export interface MetadataRefreshResult {
  jobId: string
}

/* ----------------------------------------------------------- annotations */

export interface PaperAnnotation {
  id: string
  paperId: string
  projectId: string
  noteId: string | null
  evidenceId: string | null
  pageNo: number
  rects: Rect[]
  quote: string
  color: string | null
  locator: Locator | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
}

export interface AnnotationInput {
  pageNo: number
  rects: Rect[]
  quote: string
  color?: string | null
  noteId?: string | null
  evidenceId?: string | null
  locator?: Locator | null
}

/* ------------------------------------------------------------- commands */

export interface LiteratureCommands {
  'literature.zotero.status': { req: { refresh?: boolean }; res: ZoteroStatus }
  'literature.zotero.libraries': { req: void; res: ZoteroLibrary[] }
  'literature.zotero.collections': { req: { libraryId: string }; res: ZoteroCollection[] }
  'literature.zotero.items': {
    req: {
      libraryId: string
      collectionKey?: string
      query?: string
      limit?: number
      offset?: number
    }
    res: ZoteroItemPage
  }

  'literature.import.preview': {
    req: { projectId: string; selection: ImportSelection }
    res: ImportPreview
  }
  'literature.import.start': {
    req: { projectId: string; selection: ImportSelection }
    res: { jobId: string }
  }
  'literature.import.report': { req: { jobId: string }; res: ImportReport }

  'literature.papers.list': { req: PaperListQuery; res: PaperPage }
  'literature.papers.get': { req: { projectId: string; paperId: string }; res: PaperView }
  'literature.papers.link': { req: { projectId: string; paperId: string }; res: PaperView }
  'literature.papers.unlink': {
    req: { projectId: string; paperId: string }
    res: { unlinked: boolean; detachedNoteIds: string[] }
  }

  'literature.paperNote.get': {
    req: { projectId: string; paperId: string }
    res: PaperNoteView | null
  }
  'literature.paperNote.create': { req: { projectId: string; paperId: string }; res: PaperNoteView }
  'literature.paperNote.saveFields': {
    req: {
      projectId: string
      paperId: string
      fields: PaperNoteFields
      title?: string
      expectedUpdatedAt?: IsoDateTime
    }
    res: PaperNoteView
  }
  'literature.paperNote.saveBody': {
    req: { projectId: string; paperId: string; markdown: string; baseHash: string }
    res: { fileHash: string; savedAt: IsoDateTime }
  }

  'literature.overview.get': { req: { projectId: string }; res: LiteratureOverview | null }
  'literature.overview.save': {
    req: { projectId: string; markdown: string; baseHash?: string }
    res: { fileHash: string; savedAt: IsoDateTime }
  }

  'literature.sync.check': {
    req: { projectId?: string; paperIds?: string[] }
    res: SyncReport
  }
  'literature.sync.apply': {
    req: { projectId: string; decisions: SyncDecision[] }
    res: SyncApplyResult
  }
  'literature.sync.refreshMetadata': {
    req: { projectId?: string; paperIds?: string[] }
    res: MetadataRefreshResult
  }

  'literature.annotations.create': {
    req: { projectId: string; paperId: string; annotation: AnnotationInput }
    res: PaperAnnotation
  }
  'literature.annotations.list': {
    req: { projectId: string; paperId: string }
    res: PaperAnnotation[]
  }
  'literature.annotations.update': {
    req: {
      projectId: string
      annotationId: string
      quote?: string
      color?: string | null
      noteId?: string | null
      evidenceId?: string | null
    }
    res: PaperAnnotation
  }
  'literature.annotations.delete': {
    req: { projectId: string; annotationId: string }
    res: { deleted: boolean }
  }
}