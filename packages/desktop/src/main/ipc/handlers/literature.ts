import type { CommandMap, CommandValidator } from '@researchos/shared'
import type { LiteratureModule } from '@researchos/core/modules/literature/index'
import {
  literatureValidators,
  type LiteratureChannel
} from '@researchos/core/validation/literature'

import type { CommandRouter } from '../router'

export interface LiteratureHandlerDeps {
  /**
   * The workspace-scoped literature service. It is stateless per call; every
   * command names its project, and archived state is enforced by the service.
   */
  module(): LiteratureModule
}

/**
 * Registers all `literature.*` IPC commands. The router validates with the zod
 * schema from `@researchos/core/validation/literature`; AppError passes through
 * unchanged and unexpected errors get a trace id there.
 */
export function registerLiteratureHandlers(router: CommandRouter, deps: LiteratureHandlerDeps): void {
  function register<C extends LiteratureChannel>(
    channel: C,
    handler: (
      request: CommandMap[C]['req']
    ) => Promise<CommandMap[C]['res']> | CommandMap[C]['res']
  ): void {
    router.registerHandler(
      channel,
      literatureValidators[channel] as CommandValidator<CommandMap[C]['req']>,
      (request) => handler(request)
    )
  }

  register('literature.zotero.status', () => deps.module().zoteroStatus())
  register('literature.zotero.libraries', () => deps.module().zoteroLibraries())
  register('literature.zotero.collections', (request) =>
    deps.module().zoteroCollections(request.libraryId)
  )
  register('literature.zotero.items', (request) => deps.module().zoteroItems(request))

  register('literature.import.preview', (request) =>
    deps.module().previewImport(request.projectId, request.selection)
  )
  register('literature.import.start', (request) =>
    deps.module().startImport(request.projectId, request.selection)
  )
  register('literature.import.report', (request) => deps.module().importReport(request.jobId))

  register('literature.papers.list', (request) => deps.module().listPapers(request))
  register('literature.papers.get', (request) =>
    deps.module().getPaper(request.paperId, request.projectId)
  )
  register('literature.papers.link', (request) =>
    deps.module().linkPaper(request.projectId, request.paperId)
  )
  register('literature.papers.unlink', (request) =>
    deps.module().unlinkPaper(request.projectId, request.paperId)
  )

  register('literature.paperNote.get', (request) =>
    deps.module().getPaperNote(request.projectId, request.paperId)
  )
  register('literature.paperNote.create', (request) =>
    deps.module().createPaperNote(request.projectId, request.paperId)
  )
  register('literature.paperNote.saveFields', (request) =>
    deps.module().savePaperNoteFields(
      request.projectId,
      request.paperId,
      { fields: request.fields, ...(request.title === undefined ? {} : { title: request.title }) },
      request.expectedUpdatedAt
    )
  )
  register('literature.paperNote.saveBody', (request) => {
    const saved = deps
      .module()
      .savePaperNoteBody(
        request.projectId,
        request.paperId,
        request.markdown,
        request.baseHash
      )
    return { fileHash: saved.fileHash, savedAt: saved.savedAt }
  })

  register('literature.overview.get', (request) => deps.module().getOverview(request.projectId))
  register('literature.overview.save', (request) => {
    const saved = deps
      .module()
      .saveOverview(request.projectId, request.markdown, request.baseHash)
    return { fileHash: saved.fileHash, savedAt: saved.savedAt }
  })

  register('literature.sync.check', (request) =>
    deps.module().syncCheck({
      ...(request.projectId === undefined ? {} : { projectId: request.projectId }),
      ...(request.paperIds === undefined ? {} : { paperIds: request.paperIds })
    })
  )
  register('literature.sync.apply', (request) =>
    deps.module().syncApply(request.projectId, request.decisions)
  )
  register('literature.sync.refreshMetadata', (request) =>
    deps.module().startRefreshMetadata({
      ...(request.projectId === undefined ? {} : { projectId: request.projectId }),
      ...(request.paperIds === undefined ? {} : { paperIds: request.paperIds })
    })
  )

  register('literature.annotations.create', (request) =>
    deps.module().createAnnotation(request.projectId, request.paperId, request.annotation)
  )
  register('literature.annotations.list', (request) =>
    deps.module().listAnnotations(request.projectId, request.paperId)
  )
  register('literature.annotations.update', (request) =>
    deps.module().updateAnnotation(request.projectId, request.annotationId, {
      ...(request.quote === undefined ? {} : { quote: request.quote }),
      ...(request.color === undefined ? {} : { color: request.color }),
      ...(request.noteId === undefined ? {} : { noteId: request.noteId }),
      ...(request.evidenceId === undefined ? {} : { evidenceId: request.evidenceId })
    })
  )
  register('literature.annotations.delete', (request) =>
    deps.module().deleteAnnotation(request.projectId, request.annotationId)
  )
}