import { AppError, AppErrorCode } from '@researchos/shared'
import type { AiRole } from '@researchos/shared'
import { createAiOutputService } from '@researchos/core/modules/ai/index'
import type { AiModule, AnalysisEvent } from '@researchos/core/modules/ai/index'
import type { Db } from '@researchos/core/db/open'

import { aiValidators } from '@researchos/core/validation/ai'
import type { CommandRouter } from '../router'

export interface AiProjectContext {
  db: Db
  /** Throws `ARCHIVED_READ_ONLY` for archived projects. */
  readOnly?: () => void
  emitAnalysis?: (event: AnalysisEvent) => void
}

export interface AiHandlerDeps {
  ai: AiModule
  /** Resolves the open project database of one project. */
  project: (projectId: string) => AiProjectContext
}

/**
 * Thin IPC adapter over the AI module. Validation comes from
 * `core/src/validation/ai.ts`; errors cross the bridge as AppError shapes.
 */
export function registerAiHandlers(router: CommandRouter, deps: AiHandlerDeps): void {
  const outputsFor = (projectId: string) => {
    const context = deps.project(projectId)
    return createAiOutputService(context.db, {
      projectId,
      ...(context.readOnly === undefined ? {} : { readOnly: context.readOnly }),
      ...(context.emitAnalysis === undefined ? {} : { emitAnalysis: context.emitAnalysis })
    })
  }

  router.registerHandler('ai.providers.list', aiValidators['ai.providers.list'], () => deps.ai.providers.list())
  router.registerHandler('ai.providers.create', aiValidators['ai.providers.create'], (request) =>
    deps.ai.providers.create(request.provider)
  )
  router.registerHandler('ai.providers.update', aiValidators['ai.providers.update'], (request) =>
    deps.ai.providers.update(request.providerId, request.patch)
  )
  router.registerHandler('ai.providers.delete', aiValidators['ai.providers.delete'], async (request) => {
    await deps.ai.providers.delete(request.providerId)
    return { deleted: true }
  })
  router.registerHandler('ai.providers.test', aiValidators['ai.providers.test'], (request) =>
    deps.ai.providers.test(request.providerId)
  )

  router.registerHandler('ai.models.list', aiValidators['ai.models.list'], (request) =>
    deps.ai.models.list({
      providerId: request.providerId,
      ...(request.capability === undefined ? {} : { capability: request.capability }),
      ...(request.includeUnavailable === undefined
        ? {}
        : { includeUnavailable: request.includeUnavailable })
    })
  )
  router.registerHandler('ai.models.addManual', aiValidators['ai.models.addManual'], (request) => {
    deps.ai.models.addManual(request.providerId, request.modelId, request.displayName)
  })

  router.registerHandler('ai.models.remove', aiValidators['ai.models.remove'], (request) => {
    deps.ai.models.removeManual(request.providerId, request.modelId)
  })

  router.registerHandler('ai.models.refresh', aiValidators['ai.models.refresh'], (request) =>
    deps.ai.models.sync(request.providerId)
  )

  router.registerHandler('ai.roles.list', aiValidators['ai.roles.list'], () => deps.ai.roles.list())
  router.registerHandler('ai.roles.set', aiValidators['ai.roles.set'], (request) =>
    deps.ai.roles.assign(request.role, request.providerId, request.modelId)
  )
  router.registerHandler('ai.roles.clear', aiValidators['ai.roles.clear'], (request) => {
    deps.ai.roles.clear(request.role)
  })

  router.registerHandler('ai.secrets.set', aiValidators['ai.secrets.set'], (request) =>
    deps.ai.secrets.set(request.providerId, request.apiKey)
  )
  router.registerHandler('ai.secrets.delete', aiValidators['ai.secrets.delete'], (request) =>
    deps.ai.secrets.delete(request.providerId)
  )
  router.registerHandler('ai.secrets.status', aiValidators['ai.secrets.status'], (request) =>
    deps.ai.secrets.status(request.providerId)
  )

  router.registerHandler('ai.probe', aiValidators['ai.probe'], async (request) =>
    deps.ai.gateway.probe(request.role ?? 'chat_main')
  )

  router.registerHandler('ai.chat', aiValidators['ai.chat'], (request) => {
    const role: AiRole = request.role ?? 'chat_main'
    return deps.ai.gateway.chat(role, {
      messages: request.messages,
      ...(request.temperature === undefined ? {} : { temperature: request.temperature }),
      ...(request.maxOutputTokens === undefined
        ? {}
        : { maxOutputTokens: request.maxOutputTokens })
    })
  })

  router.registerHandler('ai.availability', aiValidators['ai.availability'], async (request) => {
    if (request?.refresh === true) await deps.ai.gateway.probe('chat_main')
    return deps.ai.gateway.availability()
  })

  router.registerHandler('ai.outputs.list', aiValidators['ai.outputs.list'], (request) => {
    const { projectId, ...query } = request
    return outputsFor(projectId).list(query)
  })
  router.registerHandler('ai.outputs.get', aiValidators['ai.outputs.get'], (request) =>
    outputsFor(request.projectId).get(request.outputId)
  )
  router.registerHandler('ai.outputs.confirm', aiValidators['ai.outputs.confirm'], (request) => {
    const context = deps.project(request.projectId)
    const service = outputsFor(request.projectId)
    return context.db
      .transaction(() => {
        if (request.contentMd !== undefined) {
          service.updateContent(request.outputId, request.contentMd, 'user')
        }
        return service.confirmTx(request.outputId, 'user')
      })
      .immediate()
  })
  router.registerHandler('ai.outputs.update', aiValidators['ai.outputs.update'], (request) =>
    outputsFor(request.projectId).updateContent(request.outputId, request.contentMd, 'user')
  )
  router.registerHandler('ai.outputs.discard', aiValidators['ai.outputs.discard'], (request) =>
    outputsFor(request.projectId).discard(request.outputId)
  )
}

/** Standard archived-project gate for `AiProjectContext.readOnly`. */
export function archivedReadOnly(isArchived: () => boolean): () => void {
  return () => {
    if (isArchived()) {
      throw new AppError(
        AppErrorCode.ArchivedReadOnly,
        'the project is archived; AI generation and confirmation are read-only'
      )
    }
  }
}