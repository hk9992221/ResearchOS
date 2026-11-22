import type { NoteCreator, NotePort } from '../../ports'
import { parseTimestamp } from '../../clock'
import type { NotesModule } from './index'

/**
 * The notes-side implementation of the foundation cross-module ports
 * (`packages/core/src/ports.ts`). Literature creates Paper Notes through
 * `createPaperNote`; context-graph reads a quote through `readNote`.
 */
export interface NotesPortResolver {
  (projectId: string): NotesModule
}

export function createNotesPort(resolve: NotesPortResolver): NotePort {
  return {
    async createScratchpad(projectId, title, bodyMd, actor) {
      const note = resolve(projectId).createNote({
        kind: 'scratchpad',
        title,
        bodyMd,
        actor
      })
      return { type: 'note', id: note.id }
    },
    async createPaperNote(projectId, paperId, title, bodyMd, actor) {
      const note = resolve(projectId).createNote({
        kind: 'paper_note',
        title,
        bodyMd,
        paperId,
        actor
      })
      return { type: 'note', id: note.id }
    },
    async readNote(projectId, noteId) {
      const module = resolve(projectId)
      const note = module.getNote(noteId)
      if (note.deletedAt !== null) return undefined
      const body = module.loadBody(noteId)
      return {
        id: note.id,
        title: note.title,
        bodyMd: body.markdown,
        updatedAt: parseTimestamp(note.updatedAt)
      }
    }
  }
}

/** The narrow `NoteCreator` slice, for consumers that only create notes. */
export function createNoteCreator(resolve: NotesPortResolver): NoteCreator {
  const port = createNotesPort(resolve)
  return { createScratchpad: port.createScratchpad }
}