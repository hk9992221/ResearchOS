import type { DbField, DbFieldType, DbRecord, Note, NoteKind, ResearchDatabase, Tag } from '../entities'
import type { FilterNode, IsoDateTime, JsonObject, Page, PageQuery, SortKey } from '../types/common'

export type { Tag }

export interface NoteBodyView {
  noteId: string
  markdown: string
  hash: string
  state: 'ok' | 'missing'
}

export interface NotePage extends Page<Note> {}

export interface NoteCreateInput {
  projectId: string
  title?: string
  kind?: NoteKind
  description?: string
  bodyMd?: string
  tags?: string[]
  paperId?: string | null
}

export interface NoteMetaPatch {
  title?: string
  description?: string
}

export interface SaveBodyRequest {
  projectId: string
  noteId: string
  markdown: string
  baseHash: string
}

export interface SaveResult {
  noteId: string
  fileHash: string
  fileMtime: IsoDateTime
  savedAt: IsoDateTime
}

export interface OutboundLink {
  href: string
  type: string | null
  targetId: string | null
  pageNo: number | null
  resolved: boolean
}

export interface DatabaseCreateInput {
  projectId: string
  name: string
  description?: string
  fields?: FieldCreateInput[]
}

export interface FieldCreateInput {
  name: string
  type: DbFieldType
  config?: JsonObject | unknown[]
  role?: 'title' | 'plain'
  /** Defect 3 compatibility: options may be top-level or under config. */
  options?: string[] | Array<{ name: string; color?: string; id?: string }>
  /** Defect 3 compatibility: relation targets may be top-level or under config. */
  targets?: string[]
}

export interface FieldPatch {
  name?: string
  config?: JsonObject | unknown[]
  role?: 'title' | 'plain'
  options?: string[] | Array<{ name: string; color?: string; id?: string }>
  targets?: string[]
}

export interface FieldReorder {
  fieldIds: string[]
}

export interface RecordCreateInput {
  projectId: string
  databaseId: string
  values?: JsonObject
  title?: string
  noteId?: string
}

export interface RecordPatch {
  values?: JsonObject
  title?: string
}

export interface RecordQuery extends PageQuery {
  projectId: string
  databaseId: string
  /** Case-insensitive title search within the database. */
  q?: string
  filter?: FilterNode
  sorts?: SortKey[]
}

export interface RecordPage extends Page<DbRecord> {}

export interface RecordReorder {
  recordIds: string[]
}

export interface AttachmentInfo {
  id: string
  noteId: string
  fileName: string
  relativePath: string
  bytes: number
  sha256: string
  contentType: string | null
  createdAt: IsoDateTime
}

export interface ReconcileOrphan {
  relPath: string
  size: number
  modTime: IsoDateTime
}

export interface ReconcileMissing {
  noteId: string
  filePath: string
  title: string
}

export interface ReconcileModified {
  noteId: string
  filePath: string
  dbHash: string
  diskHash: string
}

/**
 * Canonical note-file reconcile report (shared with the foundation
 * `ReconcileService`). Literature's separate report type is retired in favour
 * of this one; notes reconcile is the only owner of the name.
 */
export interface ReconcileReport {
  projectId: string
  scanned: number
  orphans: ReconcileOrphan[]
  missing: ReconcileMissing[]
  modified: ReconcileModified[]
  generatedAt: IsoDateTime
}

export type ReconcileActionKind =
  | 'adopt'
  | 'accept_external_edit'
  | 'mark_broken'
  | 'reattach'
  | 'recreate'

export interface ReconcileAction {
  kind: ReconcileActionKind
  /** `adopt`: workspace-relative file to register. */
  relPath?: string
  /** Every action except `adopt`. */
  noteId?: string
  /** `reattach`: the recovered file. */
  newRelPath?: string
  /** `adopt`: explicit title; otherwise derived from frontmatter/heading/name. */
  title?: string
  /** `adopt`: defaults to scratchpad. */
  noteKind?: NoteKind
  actor: 'user' | 'mcp' | 'ai'
}

export interface ReconcileActionResult {
  action: ReconcileAction
  noteId?: string
  /** Serialized failure; other actions still run. */
  error?: { code: string; message: string } | null
}

export interface ReconcileApplyResult {
  items: ReconcileActionResult[]
}

export interface NotesCommands {
  'notes.list': {
    req: { projectId: string; kind?: NoteKind; q?: string; tags?: string[]; limit?: number; offset?: number }
    res: NotePage
  }
  'notes.get': { req: { projectId: string; noteId: string }; res: Note }
  'notes.body.get': { req: { projectId: string; noteId: string }; res: NoteBodyView }
  'notes.create': { req: NoteCreateInput; res: Note }
  'notes.updateMeta': {
    req: { projectId: string; noteId: string; patch: NoteMetaPatch; expectedUpdatedAt?: IsoDateTime }
    res: Note
  }
  'notes.saveBody': { req: SaveBodyRequest; res: SaveResult }
  'notes.delete': { req: { projectId: string; noteId: string }; res: { deleted: boolean } }
  'notes.restore': { req: { projectId: string; noteId: string }; res: Note }

  'notes.tags.add': { req: { projectId: string; noteId: string; tags: string[] }; res: string[] }
  'notes.tags.list': { req: { projectId: string }; res: Tag[] }
  'notes.tags.remove': { req: { projectId: string; noteId: string; tags: string[] }; res: string[] }
  'notes.links.outbound': { req: { projectId: string; noteId: string }; res: OutboundLink[] }
  'notes.promote': { req: { projectId: string; noteId: string; databaseId: string }; res: DbRecord }
  'notes.detach': {
    req: { projectId: string; recordId: string; deleteNote?: boolean }
    res: { deleted: boolean }
  }

  'notes.databases.list': { req: { projectId: string }; res: ResearchDatabase[] }
  'notes.databases.create': { req: DatabaseCreateInput; res: ResearchDatabase }
  'notes.databases.get': { req: { projectId: string; databaseId: string }; res: ResearchDatabase }
  'notes.databases.update': {
    req: { projectId: string; databaseId: string; name?: string; description?: string; expectedUpdatedAt?: IsoDateTime }
    res: ResearchDatabase
  }
  'notes.databases.delete': { req: { projectId: string; databaseId: string }; res: { deleted: boolean } }
  'notes.databases.fields.list': { req: { projectId: string; databaseId: string }; res: DbField[] }
  'notes.databases.fields.create': {
    req: { projectId: string; databaseId: string; field: FieldCreateInput }
    res: DbField
  }
  'notes.databases.fields.update': {
    req: {
      projectId: string
      databaseId: string
      fieldId: string
      patch: FieldPatch
      expectedUpdatedAt?: IsoDateTime
    }
    res: DbField
  }
  'notes.databases.fields.delete': {
    req: { projectId: string; databaseId: string; fieldId: string; expectedUpdatedAt?: IsoDateTime }
    res: { deleted: boolean }
  }
  'notes.databases.fields.reorder': {
    req: { projectId: string; databaseId: string; order: FieldReorder }
    res: DbField[]
  }

  'notes.databases.records.list': { req: RecordQuery; res: RecordPage }
  'notes.databases.records.get': {
    req: { projectId: string; databaseId: string; recordId: string }
    res: DbRecord
  }
  'notes.databases.records.create': { req: RecordCreateInput; res: DbRecord }
  'notes.databases.records.update': {
    req: { projectId: string; databaseId: string; recordId: string; patch: RecordPatch; expectedUpdatedAt?: IsoDateTime }
    res: DbRecord
  }
  'notes.databases.records.delete': {
    req: { projectId: string; databaseId: string; recordId: string; deleteNote?: boolean }
    res: { deleted: boolean }
  }
  'notes.databases.records.reorder': {
    req: { projectId: string; databaseId: string; order: RecordReorder }
    res: DbRecord[]
  }

  'notes.attachments.list': { req: { projectId: string; noteId: string }; res: AttachmentInfo[] }
  'notes.attachments.add': {
    req: { projectId: string; noteId: string; sourcePath: string; fileName?: string }
    res: AttachmentInfo
  }
  'notes.attachments.remove': {
    req: { projectId: string; noteId: string; attachmentId: string }
    res: { deleted: boolean }
  }

  'notes.reconcile.scan': { req: { projectId: string }; res: ReconcileReport }
  'notes.reconcile.apply': {
    req: { projectId: string; actions: ReconcileAction[] }
    res: ReconcileApplyResult
  }
}