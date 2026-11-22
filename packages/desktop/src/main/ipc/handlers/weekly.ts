import {
  AppError,
  AppErrorCode,
  type Period,
  type WeeklySkillInput,
  type WeeklySkillSet,
  type WeeklyTemplate,
  type WeeklyValidationReport
} from '@researchos/shared'

import type { CommandRouter } from '../router'
import type {
  ConfirmInput,
  CreateReportInput,
  DigestView,
  ExportInput,
  ExportResult,
  GenerateOutcomeView,
  GeneratePeriodInput,
  ListReportsInput,
  PeriodEntryView,
  ReportPageView,
  ReportView,
  SectionRevisionInput,
  SectionRevisionOutcome,
  UpdateContentInput,
  Violation,
  WeeklyPeriod
} from '@researchos/core/modules/weekly/index'
import { weeklyValidators } from '@researchos/core/validation/weekly'

/** One running weekly job; the main job manager owns the row and events. */
export interface WeeklyIpcJob {
  jobId: string
}

export interface WeeklyIpcJobContext {
  signal?: AbortSignal
  progress?: (percent: number, stage: string) => void
}

export interface WeeklyIpcJobRequest {
  channel: string
  projectId: string
  run: (context: WeeklyIpcJobContext) => Promise<unknown>
}

/** The service surface the IPC adapters call; `WeeklyService` satisfies it. */
export interface WeeklyIpcService {
  currentPeriod(): WeeklyPeriod
  listPeriods(limit?: number): PeriodEntryView[]
  digest(period?: WeeklyPeriod, maxEvents?: number): DigestView
  listReports(input: ListReportsInput): ReportPageView
  getReport(reportId: string): ReportView
  createReport(input: CreateReportInput): ReportView
  generateForPeriod(input: GeneratePeriodInput): Promise<GenerateOutcomeView>
  updateContent(input: UpdateContentInput): ReportView
  validate(reportId: string): Violation[]
  regenerateSection(input: SectionRevisionInput): Promise<SectionRevisionOutcome>
  confirm(input: ConfirmInput): ReportView
  revertToDraft(input: { reportId: string; actor?: 'user' }): ReportView
  deleteReport(reportId: string, actor?: 'user' | 'mcp'): void
  restoreReport(reportId: string, actor?: 'user' | 'mcp'): void
  export(input: ExportInput): ExportResult
}

export interface WeeklyWorkspaceSurface {
  template(): WeeklyTemplate
  skills(): WeeklySkillSet
  saveSkill(input: WeeklySkillInput): WeeklySkillSet
  deleteSkill(id: string): WeeklySkillSet
  setDefaultSkill(id: string): WeeklySkillSet
}

export interface WeeklyIpcDeps {
  /** Resolves the project-scoped service; the caller owns the wiring. */
  project(projectId: string): WeeklyIpcService
  /** Workspace-scoped template and skills, independent of a project. */
  workspace(): WeeklyWorkspaceSurface
  /** Starts a background job and returns its id. */
  submitJob(request: WeeklyIpcJobRequest): Promise<WeeklyIpcJob>
  /** Archived gate for every write channel. */
  assertWritable?(projectId: string): void
  now?(): Date
}

/**
 * Weekly review IPC handlers. Each is a thin adapter: validate with the zod
 * schema from `@researchos/core/validation/weekly`, dispatch to the service,
 * and let the router map `AppError` codes. Read channels work on archived
 * projects; writes are gated before dispatch.
 */
export function registerWeeklyHandlers(router: CommandRouter, deps: WeeklyIpcDeps): void {
  const now = (): Date => deps.now?.() ?? new Date()

  const writable = (projectId: string): WeeklyIpcService => {
    deps.assertWritable?.(projectId)
    return deps.project(projectId)
  }

  const toPeriod = (service: WeeklyIpcService, period?: Period): WeeklyPeriod | undefined => {
    if (period === undefined) return undefined
    return {
      startDate: period.start,
      endExclusiveDate: addOneDay(period.end),
      timeZone: service.currentPeriod().timeZone
    }
  }

  router.registerHandler('weekly.periods.list', weeklyValidators['weekly.periods.list'], (request) =>
    deps.project(request.projectId).listPeriods(request.limit)
  )

  router.registerHandler('weekly.digest.get', weeklyValidators['weekly.digest.get'], (request) => {
    const service = deps.project(request.projectId)
    return service.digest(toPeriod(service, request.period), request.maxEvents)
  })

  router.registerHandler('weekly.template.get', weeklyValidators['weekly.template.get'], () =>
    deps.workspace().template()
  )

  router.registerHandler('weekly.skills.list', weeklyValidators['weekly.skills.list'], () =>
    deps.workspace().skills()
  )

  router.registerHandler('weekly.skills.save', weeklyValidators['weekly.skills.save'], (request) =>
    deps.workspace().saveSkill(request.skill)
  )

  router.registerHandler('weekly.skills.delete', weeklyValidators['weekly.skills.delete'], (request) =>
    deps.workspace().deleteSkill(request.id)
  )

  router.registerHandler('weekly.skills.setDefault', weeklyValidators['weekly.skills.setDefault'], (request) =>
    deps.workspace().setDefaultSkill(request.id)
  )

  router.registerHandler('weekly.reports.list', weeklyValidators['weekly.reports.list'], (request) => {
    const { projectId, ...query } = request
    return deps.project(projectId).listReports(query)
  })

  router.registerHandler('weekly.reports.get', weeklyValidators['weekly.reports.get'], (request) =>
    deps.project(request.projectId).getReport(request.reportId)
  )

  router.registerHandler('weekly.reports.create', weeklyValidators['weekly.reports.create'], (request) => {
    const service = writable(request.projectId)
    return service.createReport({
      period: toPeriod(service, request.period),
      skeleton: request.skeleton,
      force: request.force,
      actor: 'user'
    })
  })

  router.registerHandler(
    'weekly.reports.generate',
    weeklyValidators['weekly.reports.generate'],
    async (request) => {
      const service = writable(request.projectId)
      const period = toPeriod(service, request.period)
      if (period === undefined) {
        throw new AppError(AppErrorCode.InvalidArgument, 'period is required', { field: 'period' })
      }
      const job = await deps.submitJob({
        channel: 'weekly.reports.generate',
        projectId: request.projectId,
        run: async (jobContext) => {
          const outcome = await service.generateForPeriod({
            period,
            includeAI: request.includeAI !== false,
            replace: request.replace === true,
            skillId: request.skillId,
            actor: 'user',
            progress: (done, total, section) => {
              if (total <= 0) return
              jobContext.progress?.(
                10 + Math.round((done / total) * 80),
                section === '' ? '写入草稿' : `生成分区 ${section}`
              )
            }
          })
          jobContext.progress?.(100, '已生成')
          return {
            reportId: outcome.report.reportId,
            status: outcome.report.status,
            updatedAt: outcome.report.updatedAt
          }
        }
      })
      return { jobId: job.jobId }
    }
  )

  router.registerHandler('weekly.reports.update', weeklyValidators['weekly.reports.update'], (request) => {
    const service = writable(request.projectId)
    return service.updateContent({
      reportId: request.reportId,
      contentMd: request.contentMd,
      expectedHash: request.expectedHash,
      expectedUpdatedAt: request.expectedUpdatedAt,
      actor: 'user'
    })
  })

  router.registerHandler(
    'weekly.reports.validate',
    weeklyValidators['weekly.reports.validate'],
    (request): WeeklyValidationReport => {
      const service = deps.project(request.projectId)
      const violations: Violation[] = service.validate(request.reportId)
      return {
        reportId: request.reportId,
        ok: violations.every((violation) => violation.ack !== undefined),
        violations,
        checkedAt: now().toISOString(),
        truncated: violations.length >= 200
      }
    }
  )

  router.registerHandler('weekly.reports.revise', weeklyValidators['weekly.reports.revise'], async (request) => {
    const service = writable(request.projectId)
    const outcome = await service.regenerateSection({
      reportId: request.reportId,
      section: request.section,
      ...(request.focusText === undefined ? {} : { focusText: request.focusText }),
      ...(request.instructions === undefined ? {} : { instructions: request.instructions }),
      ...(request.expectedHash === undefined ? {} : { expectedHash: request.expectedHash }),
      ...(request.skillId === undefined ? {} : { skillId: request.skillId }),
      actor: 'user'
    })
    return {
      report: service.getReport(outcome.report.id),
      section: outcome.section,
      markdown: outcome.markdown,
      violations: outcome.violations
    }
  })

  router.registerHandler('weekly.reports.confirm', weeklyValidators['weekly.reports.confirm'], (request) => {
    const service = writable(request.projectId)
    return service.confirm({
      reportId: request.reportId,
      actor: 'user',
      ackFingerprints: request.ackFingerprints,
      ackNotes: request.ackNotes
    })
  })

  router.registerHandler('weekly.reports.revert', weeklyValidators['weekly.reports.revert'], (request) => {
    const service = writable(request.projectId)
    return service.revertToDraft({ reportId: request.reportId, actor: 'user' })
  })

  router.registerHandler('weekly.reports.delete', weeklyValidators['weekly.reports.delete'], (request) => {
    writable(request.projectId).deleteReport(request.reportId, 'user')
    return { deleted: true as const }
  })

  router.registerHandler('weekly.reports.restore', weeklyValidators['weekly.reports.restore'], (request) => {
    const service = writable(request.projectId)
    service.restoreReport(request.reportId, 'user')
    return service.getReport(request.reportId)
  })

  router.registerHandler('weekly.reports.export', weeklyValidators['weekly.reports.export'], (request) => {
    const service = deps.project(request.projectId)
    return service.export({
      reportId: request.reportId,
      format: request.format,
      destDir: request.destDir,
      overwrite: request.overwrite,
      frontMatter: request.frontMatter,
      actor: 'user'
    })
  })
}

function addOneDay(date: string): string {
  const [year, month, day] = date.split('-').map(Number)
  return new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1) + 86_400_000)
    .toISOString()
    .slice(0, 10)
}