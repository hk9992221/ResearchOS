import type { WeeklyReportStatus } from '../entities'
import type { IsoDateTime, JobRef, Period } from '../types/common'

/**
 * Weekly review IPC contract.
 *
 * `Period.end` is the INCLUSIVE last calendar day, matching
 * `WeeklyReport.periodEnd` and the MCP `period_end` argument. The service
 * converts it to the half-open `[start, end + 1)` window internally.
 */

export type WeeklySectionKey =
  | 'work'
  | 'literature'
  | 'theory'
  | 'experiment'
  | 'idea'
  | 'problem'
  | 'next_week'

export type WeeklySkillSource = 'builtin' | 'custom' | 'plugin'

export interface WeeklyDigestItem {
  type: string
  id: string
  kind: string
  summary: string
  detail: string
  old: string
  new: string
  state: string
  actor: string
  firstAt: IsoDateTime
  lastAt: IsoDateTime
  unconfirmedSource: boolean
  broken: boolean
}

export interface WeeklyDigestSection {
  key: WeeklySectionKey
  title: string
  items: WeeklyDigestItem[]
}

export interface WeeklyDigest {
  periodStart: string
  periodEnd: string
  timeZone: string
  eventCount: number
  countsByType: Record<string, number>
  countsBySection: Record<string, number>
  truncated: boolean
  warnings: string[]
  digestHash: string
  collectedAt: IsoDateTime
  sections: WeeklyDigestSection[]
  blocked: Array<{
    type: string
    id: string
    title: string
    columnName: string
    isDone: boolean
    reason: string
  }>
  planned: Array<{
    type: string
    id: string
    title: string
    columnName: string
    isDone: boolean
    deadline: string | null
    weekPlan: string | null
    origin: string
  }>
}

export interface WeeklyTemplateSection {
  key: WeeklySectionKey
  title: string
  num: number
}

export interface WeeklyTemplate {
  id: string
  version: number
  sections: WeeklyTemplateSection[]
}

export interface WeeklySkill {
  id: string
  name: string
  prompt: string
  source: WeeklySkillSource
  builtin?: boolean
}

export interface WeeklySkillSet {
  skills: WeeklySkill[]
  /** Empty means the builtin skill. */
  default: string
}

export interface WeeklySkillInput {
  id?: string
  name: string
  prompt: string
}

export type WeeklyViolationSeverity = 'error' | 'warning'

export interface WeeklyViolation {
  rule: string
  severity: WeeklyViolationSeverity
  fingerprint: string
  section: WeeklySectionKey | ''
  ref?: { type: string; id: string }
  message: string
  ack?: { actor: string; at: IsoDateTime; note?: string }
}

export interface WeeklyValidationReport {
  reportId: string
  ok: boolean
  violations: WeeklyViolation[]
  checkedAt: IsoDateTime
  truncated: boolean
}

/** One section rewritten from a selection, temp instructions and the digest. */
export interface WeeklyRevisionResult {
  report: WeeklyReportView
  section: WeeklySectionKey
  markdown: string
  violations: WeeklyViolation[]
}

export interface WeeklyGenerationMeta {
  mode: 'manual' | 'ai'
  ai_status: 'ok' | 'failed' | 'skipped'
  ai_output_id?: string
  ai_output_ids?: string[]
  provider_id?: string
  model_id?: string
  prompt_version?: string
  template_id: string
  template_version: number
  generated_at?: IsoDateTime
  digest_hash?: string
  attempts: number
  error?: string
  skill_id?: string
  skill_name?: string
  notes?: string
}

export interface WeeklyCoverage {
  period_start: string
  period_end: string
  timezone: string
  event_count: number
  counts_by_type: Record<string, number>
  counts_by_section: Record<string, number>
  truncated: boolean
  digest_hash: string
  computed_at: IsoDateTime
}

export interface WeeklyCheckState {
  checked_at: IsoDateTime
  violations: WeeklyViolation[]
  truncated?: boolean
}

/** The `weekly_reports.meta` document, identical to the Go JSON shape. */
export interface WeeklyReportMeta {
  generation?: WeeklyGenerationMeta
  coverage?: WeeklyCoverage
  checks?: WeeklyCheckState
  confirmed_hash?: string
  refs?: Array<{ type: string; id: string }>
}

export interface WeeklyReportView {
  reportId: string
  periodStart: string
  periodEnd: string
  title: string
  contentMd: string
  contentHash: string
  status: WeeklyReportStatus
  meta: WeeklyReportMeta
  confirmedAt: IsoDateTime | null
  deletedAt: IsoDateTime | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
}

export interface WeeklyReportSummary {
  reportId: string
  title: string
  periodStart: string
  periodEnd: string
  status: WeeklyReportStatus
  excerpt: string
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  confirmedAt: IsoDateTime | null
  deletedAt: IsoDateTime | null
}

export interface WeeklyReportPage {
  items: WeeklyReportSummary[]
  total: number
  page: number
  pageSize: number
}

export interface WeeklyPeriodEntry {
  periodStart: string
  periodEnd: string
  timeZone: string
  reportId: string | null
  status: WeeklyReportStatus | null
}

export interface WeeklyGenerateOutcome {
  report: WeeklyReportView
  digest: WeeklyDigest
  violations: WeeklyViolation[]
}

export interface WeeklyExportResult {
  path: string
  bytes: number
  format: 'md' | 'html'
  status: WeeklyReportStatus
}

export interface WeeklyReportsListQuery {
  projectId: string
  status?: WeeklyReportStatus[]
  from?: string
  to?: string
  includeDeleted?: boolean
  page?: number
  pageSize?: number
  order?: 'period_start' | '-period_start' | 'updated_at'
}

export interface WeeklyCommands {
  'weekly.periods.list': {
    req: { projectId: string; limit?: number }
    res: WeeklyPeriodEntry[]
  }
  'weekly.digest.get': {
    req: { projectId: string; period?: Period; maxEvents?: number }
    res: WeeklyDigest
  }
  'weekly.template.get': { req: void; res: WeeklyTemplate }
  'weekly.skills.list': { req: void; res: WeeklySkillSet }
  'weekly.skills.save': { req: { skill: WeeklySkillInput }; res: WeeklySkillSet }
  'weekly.skills.delete': { req: { id: string }; res: WeeklySkillSet }
  'weekly.skills.setDefault': { req: { id: string }; res: WeeklySkillSet }

  'weekly.reports.list': { req: WeeklyReportsListQuery; res: WeeklyReportPage }
  'weekly.reports.get': { req: { projectId: string; reportId: string }; res: WeeklyReportView }
  'weekly.reports.create': {
    req: { projectId: string; period: Period; skeleton?: boolean; force?: boolean }
    res: WeeklyReportView
  }
  'weekly.reports.generate': {
    req: {
      projectId: string
      period: Period
      skillId?: string
      includeAI?: boolean
      replace?: boolean
    }
    res: JobRef
  }
  'weekly.reports.update': {
    req: {
      projectId: string
      reportId: string
      contentMd: string
      /** Optimistic token; one of the two is required. */
      expectedHash?: string
      expectedUpdatedAt?: IsoDateTime
    }
    res: WeeklyReportView
  }
  'weekly.reports.validate': {
    req: { projectId: string; reportId: string }
    res: WeeklyValidationReport
  }
  'weekly.reports.revise': {
    req: {
      projectId: string
      reportId: string
      section: WeeklySectionKey
      /** The selected fragment this revision must focus on (optional). */
      focusText?: string
      /** One-shot instructions; never stored in the skill or report meta. */
      instructions?: string
      expectedHash?: string
      skillId?: string
    }
    res: WeeklyRevisionResult
  }
  'weekly.reports.confirm': {
    req: {
      projectId: string
      reportId: string
      ackFingerprints?: string[]
      ackNotes?: Record<string, string>
    }
    res: WeeklyReportView
  }
  'weekly.reports.revert': { req: { projectId: string; reportId: string }; res: WeeklyReportView }
  'weekly.reports.delete': { req: { projectId: string; reportId: string }; res: { deleted: boolean } }
  'weekly.reports.restore': { req: { projectId: string; reportId: string }; res: WeeklyReportView }
  'weekly.reports.export': {
    req: {
      projectId: string
      reportId: string
      format: 'md' | 'html'
      /** Absolute OS directory; outside the workspace is allowed. */
      destDir?: string
      overwrite?: boolean
      frontMatter?: boolean
    }
    res: WeeklyExportResult
  }
}