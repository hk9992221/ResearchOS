import type { CommandRouter } from '../router'
import type { ExportService } from '@researchos/core/modules/export'
import { exportValidators } from '@researchos/core/validation/export'

export interface ExportIpcDeps {
  /** Workspace-scoped service; the composition root creates it once. */
  service(): ExportService
}

/**
 * Export/import IPC adapters (§4.11). Each channel validates with the export
 * zod schema, dispatches to the service and returns the shared result shape;
 * the router maps typed AppErrors and never leaks an INTERNAL for a domain
 * failure. Long-running work returns `{ jobId }`, progress arrives as events.
 */
export function registerExportHandlers(router: CommandRouter, deps: ExportIpcDeps): void {
  router.registerHandler('export.project.plan', exportValidators['export.project.plan'], (request) =>
    deps.service().plan({
      projectId: request.projectId,
      format: request.format,
      ...(request.destDir === undefined ? {} : { destDir: request.destDir })
    })
  )

  router.registerHandler('export.project.start', exportValidators['export.project.start'], (request) =>
    deps.service().start(
      {
        projectId: request.projectId,
        format: request.format,
        ...(request.destDir === undefined ? {} : { destDir: request.destDir }),
        ...(request.overwrite === undefined ? {} : { overwrite: request.overwrite })
      },
      'user'
    )
  )

  router.registerHandler('export.project.status', exportValidators['export.project.status'], (request) =>
    deps.service().exportStatus(request.jobId)
  )

  router.registerHandler('export.cancel', exportValidators['export.cancel'], (request) => {
    const service = deps.service()
    // A job id is unique across both kinds; try export then import.
    const cancelled = service.cancelExport(request.jobId) || service.cancelImport(request.jobId)
    return { cancelled }
  })

  router.registerHandler('export.import.inspect', exportValidators['export.import.inspect'], (request) =>
    deps.service().inspect(request.sourcePath)
  )

  router.registerHandler('export.import.start', exportValidators['export.import.start'], (request) =>
    deps.service().startImport(request.planId, {
      ...(request.newIdentity === undefined ? {} : { newIdentity: request.newIdentity }),
      ...(request.name === undefined ? {} : { name: request.name })
    })
  )

  router.registerHandler('export.import.status', exportValidators['export.import.status'], (request) =>
    deps.service().importStatus(request.jobId)
  )
}