import type { DiagnosticsIntegrityReport, DiagnosticsStorageReport, ExternalRefProbe } from '@researchos/shared'
import { AppError, AppErrorCode } from '@researchos/shared'
import { runDiagnostics, type DiagReport } from '@researchos/core/diag'
import { workspaceFootprint, workspaceInfo } from '@researchos/core/workspace/info'
import { WorkspaceManager } from '@researchos/core/workspace/index'

import { diagnosticsValidators } from '@researchos/core/validation/diagnostics'

import type { CommandRouter } from '../router'

export interface DiagnosticsHandlerDeps {
  workspace(): WorkspaceManager | null
  /** Live probes of external references for one project (read-only). */
  externalRefs(projectId: string, refIds?: readonly string[]): RefProbeView[]
}

export interface RefProbeView {
  id: string
  ownerType: string
  ownerId: string
  field: string
  kind: 'directory' | 'file' | 'git_repository'
  path: string
  status: 'ok' | 'broken'
  detail: string | null
}

function requireWorkspace(deps: DiagnosticsHandlerDeps): WorkspaceManager {
  const workspace = deps.workspace()
  if (workspace === null) throw new AppError(AppErrorCode.NotWorkspace, 'workspace is not open')
  return workspace
}

function dbReport(report: DiagReport['global']): DiagnosticsIntegrityReport['global'] {
  return {
    path: report.path,
    schemaVersion: report.schemaVersion,
    targetVersion: report.targetVersion,
    integrityOk: report.integrityOk,
    ...(report.integrityError === undefined ? {} : { integrityError: report.integrityError }),
    foreignKeyViolations: report.foreignKeyViolations,
    sizeBytes: report.sizeBytes,
    walBytes: report.walBytes
  }
}

function toView(report: DiagReport): DiagnosticsIntegrityReport {
  return {
    generatedAt: report.generatedAt.toISOString(),
    global: dbReport(report.global),
    projects: report.projects.map((project) => ({
      projectId: project.projectId,
      ...(project.db === undefined ? {} : { db: dbReport(project.db) }),
      dirExists: project.dirExists,
      notesTotal: project.notesTotal,
      notesMissing: project.notesMissing,
      brokenRefs: project.brokenRefs
    })),
    config: {
      path: report.config.path,
      exists: report.config.exists,
      ...(report.config.corruptBackup === undefined ? {} : { corruptBackup: report.config.corruptBackup }),
      workspaceRoot: report.config.workspaceRoot,
      rootReachable: report.config.rootReachable,
      zoteroDataDir: report.config.zoteroDataDir
    },
    warnings: report.warnings
  }
}

export function registerDiagnosticsHandlers(router: CommandRouter, deps: DiagnosticsHandlerDeps): void {
  router.registerHandler('diagnostics.integrity', diagnosticsValidators['diagnostics.integrity'], (request): DiagnosticsIntegrityReport =>
    toView(
      runDiagnostics(requireWorkspace(deps), {
        ...(request.projectIds === undefined ? {} : { projectIds: request.projectIds }),
        ...(request.includeFileScan === undefined ? {} : { includeFileScan: request.includeFileScan })
      })
    )
  )

  router.registerHandler('diagnostics.storage', diagnosticsValidators['diagnostics.storage'], async (): Promise<DiagnosticsStorageReport> => {
    const workspace = requireWorkspace(deps)
    const [footprint] = await Promise.all([workspaceFootprint(workspace)])
    const info = workspaceInfo(workspace)
    return {
      info: {
        root: info.root,
        freeBytes: info.freeBytes,
        totalBytes: info.totalBytes,
        dbBytes: info.dbBytes,
        libraryBytes: info.libraryBytes,
        exportsBytes: info.exportsBytes,
        projectCount: info.projectCount,
        archivedCount: info.archivedCount
      },
      footprint: {
        projects: footprint.projects.map((project) => ({ ...project })),
        libraryBytes: footprint.libraryBytes,
        exportsBytes: footprint.exportsBytes,
        totalBytes: footprint.totalBytes
      }
    }
  })

  router.registerHandler('diagnostics.externalRefs', diagnosticsValidators['diagnostics.externalRefs'], (request): ExternalRefProbe[] =>
    deps.externalRefs(request.projectId, request.refIds).map((probe) => ({
      id: probe.id,
      ownerType: probe.ownerType,
      ownerId: probe.ownerId,
      field: probe.field,
      kind: probe.kind,
      path: probe.path,
      status: probe.status,
      detail: probe.detail
    }))
  )
}
