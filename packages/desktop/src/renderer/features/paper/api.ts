/**
 * Paper page and PDF viewer IPC adapter. Paper Note reads/writes go through the
 * literature commands; the related-note/idea panel reads `project.context`,
 * which already projects backlinks for a paper; evidence creation uses the
 * ideas command with a paper source locator.
 */

import type {
  AnnotationInput,
  EntityContext,
  EvidenceCreateInput,
  EvidenceView,
  IdeaPage,
  PaperAnnotation,
  PaperNoteFields,
  PaperNoteView,
  PaperView
} from '@researchos/shared'

import { ipc } from '../../lib/ipc'

export const paperApi = {
  get: (projectId: string, paperId: string): Promise<PaperView> =>
    ipc.invoke('literature.papers.get', { projectId, paperId }),

  note: (projectId: string, paperId: string): Promise<PaperNoteView | null> =>
    ipc.invoke('literature.paperNote.get', { projectId, paperId }),

  createNote: (projectId: string, paperId: string): Promise<PaperNoteView> =>
    ipc.invoke('literature.paperNote.create', { projectId, paperId }),

  saveFields: (
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

  saveBody: (
    projectId: string,
    paperId: string,
    markdown: string,
    baseHash: string
  ): Promise<{ fileHash: string; savedAt: string }> =>
    ipc.invoke('literature.paperNote.saveBody', { projectId, paperId, markdown, baseHash }),

  context: (projectId: string, paperId: string): Promise<EntityContext> =>
    ipc.invoke('project.context', { projectId, entityType: 'paper', entityId: paperId }),

  ideas: (projectId: string): Promise<IdeaPage> =>
    ipc.invoke('ideas.list', { projectId, limit: 100 }),

  createEvidence: (input: EvidenceCreateInput): Promise<EvidenceView> =>
    ipc.invoke('ideas.evidence.create', input),

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
    ipc.invoke('literature.annotations.delete', { projectId, annotationId })
}