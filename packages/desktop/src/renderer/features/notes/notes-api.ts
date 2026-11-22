/**
 * Notes IPC adapter.
 *
 * Every call is the typed client. The body read and tag catalog are the
 * editor's only reads beyond the note row; both return canonical shared DTOs.
 */

import type {
  AttachmentInfo,
  DatabaseCreateInput,
  DbField,
  DbRecord,
  FieldCreateInput,
  FieldPatch,
  FilterNode,
  Note,
  NoteCreateInput,
  NoteBodyView,
  NoteKind,
  NoteMetaPatch,
  OutboundLink,
  RecordCreateInput,
  RecordPage,
  RecordPatch,
  RecordQuery,
  ReconcileReport,
  ResearchDatabase,
  SaveResult,
  Tag,
  SortKey
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export type { NoteBodyView as NoteBody, Tag }

export interface NoteListParams {
  readonly kind?: NoteKind
  readonly q?: string
  readonly tags?: string[]
  readonly limit?: number
  readonly offset?: number
}

export interface RecordListParams {
  readonly databaseId: string
  readonly filter?: FilterNode
  readonly sorts?: SortKey[]
  readonly limit?: number
  readonly offset?: number
}

export const notesApi = {
  list(projectId: string, params: NoteListParams = {}): Promise<{ items: Note[]; total: number }> {
    return ipc.invoke('notes.list', { projectId, ...params })
  },
  get(projectId: string, noteId: string): Promise<Note> {
    return ipc.invoke('notes.get', { projectId, noteId })
  },
  body(projectId: string, noteId: string): Promise<NoteBodyView> {
    return ipc.invoke('notes.body.get', { projectId, noteId })
  },
  create(projectId: string, input: Omit<NoteCreateInput, 'projectId'>): Promise<Note> {
    return ipc.invoke('notes.create', { projectId, ...input })
  },
  updateMeta(
    projectId: string,
    noteId: string,
    patch: NoteMetaPatch,
    expectedUpdatedAt?: string
  ): Promise<Note> {
    return ipc.invoke('notes.updateMeta', { projectId, noteId, patch, expectedUpdatedAt })
  },
  saveBody(projectId: string, noteId: string, markdown: string, baseHash: string): Promise<SaveResult> {
    return ipc.invoke('notes.saveBody', { projectId, noteId, markdown, baseHash })
  },
  remove(projectId: string, noteId: string): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.delete', { projectId, noteId })
  },
  restore(projectId: string, noteId: string): Promise<Note> {
    return ipc.invoke('notes.restore', { projectId, noteId })
  },
  addTags(projectId: string, noteId: string, tags: string[]): Promise<string[]> {
    return ipc.invoke('notes.tags.add', { projectId, noteId, tags })
  },
  removeTags(projectId: string, noteId: string, tags: string[]): Promise<string[]> {
    return ipc.invoke('notes.tags.remove', { projectId, noteId, tags })
  },
  listTags(projectId: string): Promise<Tag[]> {
    return ipc.invoke('notes.tags.list', { projectId })
  },
  outbound(projectId: string, noteId: string): Promise<OutboundLink[]> {
    return ipc.invoke('notes.links.outbound', { projectId, noteId })
  },
  promote(projectId: string, noteId: string, databaseId: string): Promise<DbRecord> {
    return ipc.invoke('notes.promote', { projectId, noteId, databaseId })
  },
  detach(projectId: string, recordId: string, deleteNote = false): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.detach', { projectId, recordId, deleteNote })
  },

  databases(projectId: string): Promise<ResearchDatabase[]> {
    return ipc.invoke('notes.databases.list', { projectId })
  },
  createDatabase(projectId: string, input: Omit<DatabaseCreateInput, 'projectId'>): Promise<ResearchDatabase> {
    return ipc.invoke('notes.databases.create', { projectId, ...input })
  },
  database(projectId: string, databaseId: string): Promise<ResearchDatabase> {
    return ipc.invoke('notes.databases.get', { projectId, databaseId })
  },
  updateDatabase(
    projectId: string,
    databaseId: string,
    patch: { name?: string; description?: string },
    expectedUpdatedAt?: string
  ): Promise<ResearchDatabase> {
    return ipc.invoke('notes.databases.update', { projectId, databaseId, ...patch, expectedUpdatedAt })
  },
  deleteDatabase(projectId: string, databaseId: string): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.databases.delete', { projectId, databaseId })
  },

  fields(projectId: string, databaseId: string): Promise<DbField[]> {
    return ipc.invoke('notes.databases.fields.list', { projectId, databaseId })
  },
  createField(projectId: string, databaseId: string, field: FieldCreateInput): Promise<DbField> {
    return ipc.invoke('notes.databases.fields.create', { projectId, databaseId, field })
  },
  updateField(
    projectId: string,
    databaseId: string,
    fieldId: string,
    patch: FieldPatch,
    expectedUpdatedAt?: string
  ): Promise<DbField> {
    return ipc.invoke('notes.databases.fields.update', {
      projectId,
      databaseId,
      fieldId,
      patch,
      expectedUpdatedAt
    })
  },
  deleteField(
    projectId: string,
    databaseId: string,
    fieldId: string,
    expectedUpdatedAt?: string
  ): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.databases.fields.delete', {
      projectId,
      databaseId,
      fieldId,
      expectedUpdatedAt
    })
  },
  reorderFields(projectId: string, databaseId: string, fieldIds: string[]): Promise<DbField[]> {
    return ipc.invoke('notes.databases.fields.reorder', {
      projectId,
      databaseId,
      order: { fieldIds }
    })
  },

  records(projectId: string, params: RecordListParams): Promise<RecordPage> {
    return ipc.invoke('notes.databases.records.list', { projectId, ...params } satisfies RecordQuery)
  },
  createRecord(
    projectId: string,
    input: { databaseId: string; values?: Record<string, unknown>; title?: string; noteId?: string }
  ): Promise<DbRecord> {
    const request: RecordCreateInput = { projectId, ...input }
    return ipc.invoke('notes.databases.records.create', request)
  },
  updateRecord(
    projectId: string,
    databaseId: string,
    recordId: string,
    patch: RecordPatch,
    expectedUpdatedAt?: string
  ): Promise<DbRecord> {
    return ipc.invoke('notes.databases.records.update', {
      projectId,
      databaseId,
      recordId,
      patch,
      expectedUpdatedAt
    })
  },
  deleteRecord(
    projectId: string,
    databaseId: string,
    recordId: string,
    deleteNote = false
  ): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.databases.records.delete', {
      projectId,
      databaseId,
      recordId,
      deleteNote
    })
  },
  reorderRecords(projectId: string, databaseId: string, recordIds: string[]): Promise<DbRecord[]> {
    return ipc.invoke('notes.databases.records.reorder', {
      projectId,
      databaseId,
      order: { recordIds }
    })
  },

  attachments(projectId: string, noteId: string): Promise<AttachmentInfo[]> {
    return ipc.invoke('notes.attachments.list', { projectId, noteId })
  },
  addAttachment(projectId: string, noteId: string, sourcePath: string): Promise<AttachmentInfo> {
    return ipc.invoke('notes.attachments.add', { projectId, noteId, sourcePath })
  },
  removeAttachment(projectId: string, noteId: string, attachmentId: string): Promise<{ deleted: boolean }> {
    return ipc.invoke('notes.attachments.remove', { projectId, noteId, attachmentId })
  },
  reconcileScan(projectId: string): Promise<ReconcileReport> {
    return ipc.invoke('notes.reconcile.scan', { projectId })
  }
}

/** isConflict narrows an AppError to the optimistic-token failure. */
export function isConflict(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && error.code === 'CONFLICT'
}

function errorDetails(error: unknown): Record<string, unknown> | undefined {
  if (error === null || typeof error !== 'object' || !('details' in error)) return undefined
  const details = error.details
  if (details === null || typeof details !== 'object') return undefined
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(details)) out[key] = value
  return out
}

/** conflictHashes reads the on-disk token out of a save CONFLICT. */
export function conflictHashes(error: unknown): { diskHash: string; expectedHash: string } {
  const details = errorDetails(error)
  const disk = details?.['diskHash']
  const expected = details?.['expectedHash']
  return {
    diskHash: typeof disk === 'string' ? disk : '',
    expectedHash: typeof expected === 'string' ? expected : ''
  }
}