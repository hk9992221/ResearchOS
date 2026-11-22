/**
 * Consumer-side ports for the literature module. Paper Notes and the
 * Literature Overview are ordinary `notes` rows: the notes service owns their
 * body files, frontmatter and `note.updated` merging, and literature never
 * duplicates that logic.
 */
import type { Actor } from '../../schema'

export interface LiteratureNote {
  id: string
  title: string
  kind: string
  paperId: string | null
  description: string
  filePath: string
  fileHash: string | null
  fileMtime: string | null
  fileState: 'ok' | 'missing'
  createdAt: string
  updatedAt: string
  deletedAt: string | null
}

export interface LiteratureNoteBody {
  markdown: string
  hash: string
  state: 'ok' | 'missing'
}

export interface NoteCreateRequest {
  kind: 'paper_note' | 'overview'
  title: string
  bodyMd?: string
  paperId?: string | null
  actor: Actor
}

export interface NoteSaveRequest {
  noteId: string
  markdown: string
  baseHash: string
  actor: Actor
}

/** The slice of the notes service literature consumes. */
export interface LiteratureNotesPort {
  createNote(request: NoteCreateRequest): LiteratureNote
  getNote(noteId: string): LiteratureNote
  updateTitle(noteId: string, title: string, actor: Actor): LiteratureNote
  loadBody(noteId: string): LiteratureNoteBody
  saveBody(request: NoteSaveRequest): { fileHash: string; savedAt: string }
  deleteNote(noteId: string, actor: Actor): void
}

/** Post-commit literature index hook (search module). Failure never fails a write. */
export interface LiteratureIndexHook {
  onPaperChanged(paperId: string, eventType: string, summary: string): void
}