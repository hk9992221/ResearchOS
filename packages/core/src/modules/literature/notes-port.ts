/** Adapts the notes service surface to the narrow port literature consumes. */
import type { Note } from '@researchos/shared'

import type { Actor } from '../../schema'
import type { NotesModule } from '../notes/index'
import type { LiteratureNote, LiteratureNotesPort } from './deps'

function toLiteratureNote(note: Note): LiteratureNote {
  return {
    id: note.id,
    title: note.title,
    kind: note.kind,
    paperId: note.paperId,
    description: note.description,
    filePath: note.filePath,
    fileHash: note.fileHash,
    fileMtime: note.fileMtime,
    fileState: note.fileState,
    createdAt: note.createdAt,
    updatedAt: note.updatedAt,
    deletedAt: note.deletedAt
  }
}

export function literatureNotesPort(module: NotesModule): LiteratureNotesPort {
  return {
    createNote(request) {
      return toLiteratureNote(
        module.createNote({
          kind: request.kind,
          title: request.title,
          ...(request.bodyMd === undefined ? {} : { bodyMd: request.bodyMd }),
          paperId: request.paperId ?? null,
          actor: request.actor
        })
      )
    },
    getNote(noteId) {
      return toLiteratureNote(module.getNote(noteId))
    },
    updateTitle(noteId: string, title: string, actor: Actor) {
      return toLiteratureNote(module.updateNoteTitle(noteId, title, actor))
    },
    loadBody(noteId) {
      const body = module.loadBody(noteId)
      return { markdown: body.markdown, hash: body.hash, state: body.state }
    },
    saveBody(request) {
      const saved = module.saveBody(
        request.noteId,
        request.markdown,
        request.baseHash,
        request.actor
      )
      return { fileHash: saved.fileHash, savedAt: saved.savedAt }
    },
    deleteNote(noteId, actor) {
      module.deleteNote(noteId, actor)
    }
  }
}