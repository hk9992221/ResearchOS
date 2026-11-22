import { AppError, AppErrorCode, type McpJob } from '@researchos/shared'
import type { Jobs } from '@researchos/core/modules/mcp/jobs'
import { jobsValidators } from '@researchos/core/validation/jobs'

import type { CommandRouter } from '../router'

export interface JobsHandlerDeps {
  /** Null while the workspace/MCP runtime is closed. */
  jobs: () => Jobs | null
}

const DESKTOP_PRINCIPAL = { clientId: 'desktop', clientName: 'Desktop', transport: 'http' as const }

export function registerJobsHandlers(router: CommandRouter, deps: JobsHandlerDeps): void {
  router.registerHandler('jobs.list', jobsValidators['jobs.list'], (request): McpJob[] => {
    const jobs = deps.jobs()
    if (jobs === null) return []
    return jobs.list(DESKTOP_PRINCIPAL, {
      ...(request.projectId === undefined ? {} : { projectId: request.projectId }),
      ...(request.state === undefined ? {} : { state: request.state }),
      ...(request.limit === undefined ? {} : { limit: request.limit })
    })
  })

  router.registerHandler('jobs.get', jobsValidators['jobs.get'], (request): McpJob => {
    const jobs = deps.jobs()
    if (jobs === null) {
      throw new AppError(AppErrorCode.NotWorkspace, 'job runtime is not open')
    }
    return jobs.get(DESKTOP_PRINCIPAL, request.jobId)
  })
}