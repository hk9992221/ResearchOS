/**
 * Weekly review IPC adapter and its query keys.
 *
 * One seam over the typed command map: components never name a channel and
 * tests replace the host through the fake bridge, not this module. Every key
 * lives under the `['weekly', projectId, …]` root so the shell's
 * `entity.changed` invalidation and our own post-write invalidation agree.
 */

import type {
  JobRef,
  McpJob,
  OpenPathResult,
  Period,
  WeeklyDigest,
  WeeklyExportResult,
  WeeklyReportPage,
  WeeklyReportStatus,
  WeeklyReportView,
  WeeklyReportsListQuery,
  WeeklyRevisionResult,
  WeeklySectionKey,
  WeeklySkillSet,
  WeeklyTemplate,
  WeeklyValidationReport
} from '@researchos/shared'
import type { AppPreferences } from '@researchos/shared'

import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'

export const weeklyKeys = {
  reports: (projectId: string, query: unknown = {}) =>
    queryKeys.weekly.reports(projectId, query as Record<string, unknown>),
  reportsRoot: (projectId: string) =>
    [...queryKeys.weekly.all(projectId), 'reports'] as const,
  report: (projectId: string, reportId: string) =>
    [...queryKeys.weekly.all(projectId), 'report', reportId] as const,
  validation: (projectId: string, reportId: string) =>
    [...queryKeys.weekly.all(projectId), 'validation', reportId] as const,
  digest: (projectId: string, period: Period | undefined) =>
    [...queryKeys.weekly.all(projectId), 'digest', period ?? null] as const,
  periods: (projectId: string) => [...queryKeys.weekly.all(projectId), 'periods'] as const,
  template: ['weekly', 'template'] as const,
  skills: queryKeys.weeklySkills,
  preferences: ['app', 'preferences'] as const
}

export interface CreateReportRequest {
  readonly projectId: string
  readonly period: Period
  readonly skeleton?: boolean
  readonly force?: boolean
}

export interface ReviseReportRequest {
  readonly projectId: string
  readonly reportId: string
  readonly section: WeeklySectionKey
  readonly focusText?: string
  readonly instructions?: string
  readonly expectedHash?: string
  readonly skillId?: string
}

export interface UpdateReportRequest {
  readonly projectId: string
  readonly reportId: string
  readonly contentMd: string
  readonly expectedHash: string
}

export interface ConfirmReportRequest {
  readonly projectId: string
  readonly reportId: string
  readonly ackFingerprints: readonly string[]
  readonly ackNotes: Readonly<Record<string, string>>
}

export interface ExportReportRequest {
  readonly projectId: string
  readonly reportId: string
  readonly format: 'md' | 'html'
  readonly destDir?: string
  readonly overwrite?: boolean
  readonly frontMatter?: boolean
}

export const weeklyApi = {
  reports: (query: WeeklyReportsListQuery): Promise<WeeklyReportPage> =>
    ipc.invoke('weekly.reports.list', query),
  report: (projectId: string, reportId: string): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.get', { projectId, reportId }),
  periods: (projectId: string) =>
    ipc.invoke('weekly.periods.list', { projectId, limit: 26 }),
  create: (request: CreateReportRequest): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.create', request),
  generate: (request: {
    projectId: string
    period: Period
    skillId?: string
    includeAI?: boolean
    replace?: boolean
  }): Promise<JobRef> => ipc.invoke('weekly.reports.generate', request),
  job: (jobId: string): Promise<McpJob> => ipc.invoke('jobs.get', { jobId }),
  update: (request: UpdateReportRequest): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.update', request),
  validate: (projectId: string, reportId: string): Promise<WeeklyValidationReport> =>
    ipc.invoke('weekly.reports.validate', { projectId, reportId }),
  revise: (request: ReviseReportRequest): Promise<WeeklyRevisionResult> =>
    ipc.invoke('weekly.reports.revise', {
      projectId: request.projectId,
      reportId: request.reportId,
      section: request.section,
      ...(request.focusText === undefined ? {} : { focusText: request.focusText }),
      ...(request.instructions === undefined ? {} : { instructions: request.instructions }),
      ...(request.expectedHash === undefined ? {} : { expectedHash: request.expectedHash }),
      ...(request.skillId === undefined ? {} : { skillId: request.skillId })
    }),
  confirm: (request: ConfirmReportRequest): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.confirm', {
      projectId: request.projectId,
      reportId: request.reportId,
      ackFingerprints: [...request.ackFingerprints],
      ackNotes: { ...request.ackNotes }
    }),
  revert: (projectId: string, reportId: string): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.revert', { projectId, reportId }),
  remove: (projectId: string, reportId: string): Promise<{ deleted: boolean }> =>
    ipc.invoke('weekly.reports.delete', { projectId, reportId }),
  restore: (projectId: string, reportId: string): Promise<WeeklyReportView> =>
    ipc.invoke('weekly.reports.restore', { projectId, reportId }),
  export: (request: ExportReportRequest): Promise<WeeklyExportResult> =>
    ipc.invoke('weekly.reports.export', request),
  digest: (projectId: string, period?: Period): Promise<WeeklyDigest> =>
    ipc.invoke('weekly.digest.get', period === undefined ? { projectId } : { projectId, period }),
  template: (): Promise<WeeklyTemplate> => ipc.invoke('weekly.template.get'),
  skills: (): Promise<WeeklySkillSet> => ipc.invoke('weekly.skills.list'),
  preferences: (): Promise<AppPreferences> => ipc.invoke('app.preferences.get'),
  pickFolder: (title: string, defaultPath?: string): Promise<OpenPathResult> =>
    ipc.invoke(
      'app.pickFolder',
      defaultPath === undefined ? { title } : { title, defaultPath }
    )
}
