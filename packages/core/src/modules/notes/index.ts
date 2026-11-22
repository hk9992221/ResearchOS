import type {
  DbField,
  DbRecord,
  Note,
  ResearchDatabase
} from '@researchos/shared'
import type {
  ReconcileAction,
  ReconcileApplyResult,
  ReconcileReport
} from '@researchos/shared/contracts/notes'

import type { Actor } from '../../schema'
import { createNotesContext, type NotesContext, type NotesContextOptions } from './support'
import {
  createNote,
  deleteNote,
  detachRecord,
  getNote,
  listNotes,
  loadNoteBody,
  noteToDto,
  promoteNote,
  restoreNote,
  saveNoteBody,
  updateNoteMeta,
  type NoteBody,
  type NoteCreateInput,
  type NoteListQuery,
  type NoteMetaPatch,
  type NoteSaveResult,
  type PromotedRecord
} from './notes'
import {
  createDatabase,
  createField,
  deleteDatabase,
  deleteField,
  getDatabase,
  listDatabases,
  listFields,
  reorderFields,
  restoreField,
  updateDatabase,
  updateField,
  type DatabaseCreateInput,
  type FieldInput,
  type FieldPatchInput
} from './database'
import {
  createRecord,
  deleteRecord,
  getRecord,
  listRecords,
  reorderRecords,
  restoreRecord,
  updateRecord,
  type RecordCreateRequest,
  type RecordListQuery,
  type RecordUpdateRequest
} from './records'
import {
  addTags,
  createTag,
  listTags,
  removeTags,
  renameTag,
  restoreTag,
  softDeleteTag,
  tagNamesFor,
  type Tag
} from './tags'
import { outboundLinks, resolveEntityLink, type LinkTargetView } from './links'
import {
  addAttachment,
  checkAttachmentReferences,
  listAttachments,
  removeAttachment,
  type AddAttachmentRequest,
  type AttachmentIssue
} from './attachments'
import { applyReconcile, scanNotes } from './reconcile'
import type { AttachmentInfo, OutboundLink } from '@researchos/shared'
import type { ProjectHandle } from '../../workspace/handle'

export interface NotesModuleOptions extends NotesContextOptions {}

/**
 * One project's notes surface. UI/IPC/MCP share this facade; each call runs in
 * its own immediate transaction and emits its activities in that transaction.
 */
export class NotesModule {
  readonly ctx: NotesContext

  constructor(ctx: NotesContext) {
    this.ctx = ctx
  }

  static forProject(handle: ProjectHandle, options: NotesModuleOptions = {}): NotesModule {
    return new NotesModule(createNotesContext(handle, options))
  }

  /* ------------------------------------------------------------- notes */

  listNotes(query: NoteListQuery = {}): { items: Note[]; total: number; limit: number; offset: number } {
    return listNotes(this.ctx, query)
  }

  getNote(noteId: string): Note {
    return getNote(this.ctx, noteId)
  }

  createNote(input: NoteCreateInput): Note {
    return createNote(this.ctx, input)
  }

  updateNoteMeta(
    noteId: string,
    patch: NoteMetaPatch,
    expectedUpdatedAt?: string,
    actor: Actor = 'user'
  ): Note {
    return updateNoteMeta(this.ctx, noteId, patch, expectedUpdatedAt, actor)
  }

  updateNoteTitle(noteId: string, title: string, actor: Actor = 'user'): Note {
    return updateNoteMeta(this.ctx, noteId, { title }, undefined, actor)
  }

  loadBody(noteId: string): NoteBody {
    return loadNoteBody(this.ctx, noteId)
  }

  saveBody(noteId: string, markdown: string, baseHash: string, actor: Actor = 'user'): NoteSaveResult {
    return saveNoteBody(this.ctx, noteId, markdown, baseHash, actor)
  }

  deleteNote(noteId: string, actor: Actor = 'user'): { deleted: boolean } {
    return deleteNote(this.ctx, noteId, actor)
  }

  restoreNote(noteId: string, actor: Actor = 'user'): Note {
    return restoreNote(this.ctx, noteId, actor)
  }

  addNoteTags(noteId: string, tags: string[], actor: Actor = 'user'): string[] {
    return this.ctx.inTx(() => addTags(this.ctx, 'note', noteId, tags, actor))
  }

  removeNoteTags(noteId: string, tags: string[], actor: Actor = 'user'): string[] {
    return this.ctx.inTx(() => removeTags(this.ctx, 'note', noteId, tags, actor))
  }

  noteTagNames(noteId: string): string[] {
    return tagNamesFor(this.ctx, 'note', noteId)
  }

  outboundLinks(noteId: string): OutboundLink[] {
    return outboundLinks(this.ctx, noteId)
  }

  resolveLink(type: string, id: string): LinkTargetView {
    return resolveEntityLink(this.ctx, type, id)
  }

  promote(noteId: string, databaseId: string, actor: Actor = 'user'): PromotedRecord {
    return promoteNote(this.ctx, noteId, databaseId, actor)
  }

  detach(recordId: string, deleteNoteToo = false, actor: Actor = 'user'): { deleted: boolean } {
    return detachRecord(this.ctx, recordId, deleteNoteToo, actor)
  }

  /* --------------------------------------------------------- databases */

  listDatabases(includeDeleted = false): ResearchDatabase[] {
    return listDatabases(this.ctx, includeDeleted)
  }

  getDatabase(databaseId: string): ResearchDatabase {
    return getDatabase(this.ctx, databaseId)
  }

  createDatabase(input: DatabaseCreateInput): { database: ResearchDatabase; fields: DbField[] } {
    return createDatabase(this.ctx, input)
  }

  updateDatabase(
    databaseId: string,
    patch: { name?: string; description?: string },
    expectedUpdatedAt?: string,
    actor: Actor = 'user'
  ): ResearchDatabase {
    return updateDatabase(this.ctx, databaseId, patch, expectedUpdatedAt, actor)
  }

  deleteDatabase(databaseId: string, actor: Actor = 'user'): { deleted: boolean } {
    return deleteDatabase(this.ctx, databaseId, actor)
  }

  listFields(databaseId: string): DbField[] {
    return listFields(this.ctx, databaseId)
  }

  createField(databaseId: string, field: FieldInput, actor: Actor = 'user'): DbField {
    return createField(this.ctx, databaseId, field, actor)
  }

  updateField(
    fieldId: string,
    patch: FieldPatchInput,
    expectedUpdatedAt?: string,
    actor: Actor = 'user'
  ): DbField {
    return updateField(this.ctx, fieldId, patch, expectedUpdatedAt, actor)
  }

  deleteField(fieldId: string, expectedUpdatedAt?: string, actor: Actor = 'user'): { deleted: boolean } {
    return deleteField(this.ctx, fieldId, expectedUpdatedAt, actor)
  }

  restoreField(fieldId: string, actor: Actor = 'user'): DbField {
    return restoreField(this.ctx, fieldId, actor)
  }

  reorderFields(databaseId: string, fieldIds: string[]): DbField[] {
    return reorderFields(this.ctx, databaseId, fieldIds)
  }

  /* ----------------------------------------------------------- records */

  listRecords(query: RecordListQuery): { items: DbRecord[]; total: number; limit: number; offset: number } {
    return listRecords(this.ctx, query)
  }

  getRecord(recordId: string): DbRecord {
    return getRecord(this.ctx, recordId)
  }

  createRecord(request: RecordCreateRequest): DbRecord {
    return createRecord(this.ctx, request)
  }

  updateRecord(request: RecordUpdateRequest): DbRecord {
    return updateRecord(this.ctx, request)
  }

  deleteRecord(recordId: string, deleteNoteToo = false, actor: Actor = 'user'): { deleted: boolean } {
    return deleteRecord(this.ctx, recordId, deleteNoteToo, actor)
  }

  restoreRecord(recordId: string, actor: Actor = 'user'): DbRecord {
    return restoreRecord(this.ctx, recordId, actor)
  }

  reorderRecords(databaseId: string, recordIds: string[]): DbRecord[] {
    return reorderRecords(this.ctx, databaseId, recordIds)
  }

  /* -------------------------------------------------------------- tags */

  listTags(includeDeleted = false): Tag[] {
    return listTags(this.ctx, includeDeleted)
  }

  createTag(name: string, color = ''): Tag {
    return this.ctx.inTx(() => createTag(this.ctx, name, color))
  }

  renameTag(tagId: string, name: string, color = ''): Tag {
    return this.ctx.inTx(() => renameTag(this.ctx, tagId, name, color))
  }

  deleteTag(tagId: string): void {
    this.ctx.inTx(() => softDeleteTag(this.ctx, tagId))
  }

  restoreTag(tagId: string): void {
    this.ctx.inTx(() => restoreTag(this.ctx, tagId))
  }

  /* ------------------------------------------------------- attachments */

  addAttachment(request: AddAttachmentRequest): AttachmentInfo {
    return addAttachment(this.ctx, request)
  }

  listAttachments(noteId: string): AttachmentInfo[] {
    return listAttachments(this.ctx, noteId)
  }

  removeAttachment(noteId: string, attachmentId: string): { deleted: boolean } {
    return removeAttachment(this.ctx, noteId, attachmentId)
  }

  checkAttachmentReferences(noteId: string): AttachmentIssue[] {
    return checkAttachmentReferences(this.ctx, noteId)
  }

  /* --------------------------------------------------------- reconcile */

  scanNotes(): ReconcileReport {
    return scanNotes(this.ctx)
  }

  applyReconcile(actions: ReconcileAction[]): ReconcileApplyResult {
    return applyReconcile(this.ctx, actions)
  }

  /** Convenience for callers that only need the DTO mapper. */
  toNote(row: Parameters<typeof noteToDto>[0]): Note {
    return noteToDto(row)
  }
}

export * from './support'
export * from './notefile'
export * from './notes'
export * from './database'
export * from './records'
export * from './filter'
export * from './tags'
export * from './links'
export * from './attachments'
export * from './reconcile'
export * from './activity'
export * from './ports'