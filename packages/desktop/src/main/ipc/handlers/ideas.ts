/**
 * IPC handlers for the `ideas.*` commands (plan §3.5). Thin adapters: the
 * router validates with the core zod schemas, the service enforces the archived
 * read-only gate, and no handler catches a domain error (the router maps
 * `AppError` to the `IpcResult` envelope).
 */
import type { IdeasService } from '@researchos/core/modules/ideas/index'
import { ideasCommandValidators } from '@researchos/core/validation/ideas'

import type { CommandRouter } from '../router'

export interface IdeasHandlerDeps {
  service: IdeasService
}

export function registerIdeasHandlers(router: CommandRouter, deps: IdeasHandlerDeps): void {
  const { service } = deps
  const validators = ideasCommandValidators

  router.registerHandler('ideas.list', validators['ideas.list'], (request) =>
    service.listIdeas(request.projectId, {
      statuses: request.status,
      text: request.q,
      includeDeleted: request.includeDeleted,
      limit: request.limit,
      cursor: request.cursor
    })
  )

  router.registerHandler('ideas.get', validators['ideas.get'], (request) =>
    service.getIdea(request.projectId, request.ideaId)
  )

  router.registerHandler('ideas.create', validators['ideas.create'], (request) =>
    service.createIdea(request.projectId, {
      title: request.title,
      description: request.description,
      actor: 'user'
    })
  )

  router.registerHandler('ideas.update', validators['ideas.update'], (request) =>
    service.updateIdea(request.projectId, request.ideaId, request.patch, {
      actor: 'user',
      expectedUpdatedAt: request.expectedUpdatedAt
    })
  )

  router.registerHandler('ideas.changeStatus', validators['ideas.changeStatus'], (request) =>
    service.changeStatus(request.projectId, request.ideaId, {
      status: request.status,
      rejectionReason: request.rejectionReason,
      note: request.note,
      expectedUpdatedAt: request.expectedUpdatedAt,
      actor: 'user'
    })
  )

  router.registerHandler('ideas.delete', validators['ideas.delete'], (request) =>
    service.deleteIdea(request.projectId, request.ideaId, 'user')
  )

  router.registerHandler('ideas.restore', validators['ideas.restore'], (request) =>
    service.restoreIdea(request.projectId, request.ideaId, 'user')
  )

  router.registerHandler('ideas.history', validators['ideas.history'], (request) =>
    service.history(request.projectId, {
      ideaId: request.ideaId,
      kinds: request.kinds,
      limit: request.limit,
      cursor: request.cursor
    })
  )

  router.registerHandler('ideas.derive', validators['ideas.derive'], (request) =>
    service.deriveIdea(
      request.projectId,
      request.parentIdeaId,
      { title: request.title, description: request.description },
      'user'
    )
  )

  router.registerHandler('ideas.evidence.list', validators['ideas.evidence.list'], (request) =>
    service.listEvidence(request.projectId, {
      ideaId: request.ideaId,
      direction: request.direction,
      confirmed: request.confirmed,
      includeDeleted: request.includeDeleted,
      limit: request.limit,
      cursor: request.cursor
    })
  )

  router.registerHandler('ideas.evidence.create', validators['ideas.evidence.create'], (request) =>
    service.addEvidence(request.projectId, {
      ideaId: request.ideaId,
      direction: request.direction,
      content: request.content,
      source: { type: request.source.type, id: request.source.id },
      actor: 'user'
    })
  )

  router.registerHandler('ideas.evidence.update', validators['ideas.evidence.update'], (request) =>
    service.updateEvidence(request.projectId, {
      evidenceId: request.evidenceId,
      direction: request.patch.direction,
      content: request.patch.content,
      expectedUpdatedAt: request.expectedUpdatedAt,
      actor: 'user'
    })
  )

  router.registerHandler('ideas.evidence.delete', validators['ideas.evidence.delete'], (request) =>
    service.deleteEvidence(request.projectId, request.evidenceId, 'user')
  )

  router.registerHandler('ideas.evidence.confirm', validators['ideas.evidence.confirm'], (request) =>
    service.confirmEvidenceItem(request.projectId, request.evidenceId, 'user')
  )

  router.registerHandler('ideas.evidence.discard', validators['ideas.evidence.discard'], (request) =>
    service.discardEvidenceItem(request.projectId, request.ideaId, request.evidenceId)
  )

  router.registerHandler('ideas.evidence.extract', validators['ideas.evidence.extract'], (request) =>
    service.enqueueExtraction(request.projectId, {
      ideaId: request.ideaId,
      sourceRefs: request.sourceRefs,
      instruction: request.instruction,
      maxDrafts: request.maxDrafts,
      actor: 'user'
    })
  )

  router.registerHandler(
    'ideas.evidence.extractPreview',
    validators['ideas.evidence.extractPreview'],
    (request) => service.extractionPreview(request.jobId)
  )

  router.registerHandler('ideas.links.add', validators['ideas.links.add'], (request) =>
    service.linkEntity(request.projectId, request.ideaId, request.target, 'user')
  )

  router.registerHandler('ideas.links.remove', validators['ideas.links.remove'], (request) =>
    service.unlinkEntity(request.projectId, request.ideaId, request.target, 'user')
  )
}