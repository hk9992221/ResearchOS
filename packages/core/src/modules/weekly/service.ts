import { SystemClock, type Clock } from '../../clock'
import { SettingsStore } from '../../settings'
import type {
  DigestView,
  GenerateOutcomeView,
  PeriodEntryView,
  ReportPageView,
  ReportView,
  WeeklyContext,
  WeeklyServiceOptions
} from './context'
import { NODE_FILE_SYSTEM, exportReport } from './export'
import { generateDraft, generateForPeriod } from './generate'
import { defaultPeriod } from './period'
import { collectActivities, coverageOf, type DigestContext } from './digest'
import {
  confirm as confirmReport,
  createReport,
  deleteReport,
  digestFor,
  getReport,
  listPeriods,
  listReportPage,
  readWeeklySettings,
  requireReport,
  restoreReport,
  revertToDraft,
  updateContent
} from './reports'
import { regenerateSection } from './revision'
import { SkillRegistry } from './skills'
import { periodEndDate } from './template'
import type {
  ActivityDigest,
  ConfirmInput,
  CreateReportInput,
  ExportInput,
  ExportResult,
  GenerateInput,
  GeneratePeriodInput,
  ListReportsInput,
  SectionRevisionInput,
  SectionRevisionOutcome,
  SkillInput,
  SkillSet,
  UpdateContentInput,
  Violation,
  WeeklyPeriod,
  WeeklyReport,
  WeeklySkill,
  WeeklyTemplate
} from './types'
import { DEFAULT_TEMPLATE } from './template'
import { validateReport } from './validate'

export type {
  DigestView,
  GenerateOutcomeView,
  PeriodEntryView,
  ReportPageView,
  ReportView,
  WeeklyServiceOptions
} from './context'

/** The module surface shared by IPC handlers and MCP tools. */
export class WeeklyService {
  private readonly context: WeeklyContext

  constructor(options: WeeklyServiceOptions) {
    const clock: Clock = options.clock ?? new SystemClock()
    const settings = options.settings ?? new SettingsStore(options.globalDb, 'workspace', clock)
    const projectSettings =
      options.projectSettings ?? new SettingsStore(options.handle.db, 'project', clock)
    this.context = {
      projectId: options.projectId,
      handle: options.handle,
      globalDb: options.globalDb,
      root: options.root,
      clock,
      settings,
      projectSettings,
      events: options.events,
      trash: options.trash,
      ai: options.ai,
      logger: options.logger,
      fileSystem: options.fileSystem ?? NODE_FILE_SYSTEM,
      skills: new SkillRegistry({
        settings,
        clock,
        pluginSkills: options.pluginSkills
      })
    }
  }

  /** The configured current (or previous) report week. */
  currentPeriod(): WeeklyPeriod {
    const settings = readWeeklySettings(this.context)
    return defaultPeriod(this.context.clock.now(), {
      weekStartDay: settings.weekStartDay,
      timeZone: settings.timeZone,
      defaultPeriod: settings.defaultPeriod
    })
  }

  createReport(input: CreateReportInput = {}): ReportView {
    return toReportView(createReport(this.context, input))
  }

  getReport(reportId: string): ReportView {
    return toReportView(getReport(this.context, reportId))
  }

  listReports(input: ListReportsInput = {}): ReportPageView {
    const page = listReportPage(this.context, input)
    return {
      total: page.total,
      page: page.page,
      pageSize: page.pageSize,
      items: page.items.map((item) => ({
        reportId: item.id,
        title: item.title,
        periodStart: item.period.startDate,
        periodEnd: periodEndDate(item.period),
        status: item.status,
        excerpt: item.excerpt,
        createdAt: item.createdAt,
        updatedAt: item.updatedAt,
        confirmedAt: item.confirmedAt ?? null,
        deletedAt: item.deletedAt ?? null
      }))
    }
  }

  listPeriods(limit?: number): PeriodEntryView[] {
    return listPeriods(this.context, limit).map((entry) => ({
      periodStart: entry.period.startDate,
      periodEnd: periodEndDate(entry.period),
      timeZone: entry.period.timeZone,
      reportId: entry.reportId,
      status: entry.status
    }))
  }

  digest(period?: WeeklyPeriod, maxEvents?: number): DigestView {
    return toDigestView(digestFor(this.context, period, maxEvents))
  }

  template(): WeeklyTemplate {
    return DEFAULT_TEMPLATE
  }

  async generate(input: GenerateInput): Promise<GenerateOutcomeView> {
    const outcome = await generateDraft(this.context, input)
    return toOutcomeView(outcome)
  }

  async generateForPeriod(input: GeneratePeriodInput): Promise<GenerateOutcomeView> {
    const outcome = await generateForPeriod(this.context, input)
    return toOutcomeView(outcome)
  }

  async regenerateSection(input: SectionRevisionInput): Promise<SectionRevisionOutcome> {
    const outcome = await regenerateSection(this.context, input)
    return {
      ...outcome,
      report: { ...outcome.report, title: getReport(this.context, outcome.report.id).title }
    }
  }

  updateContent(input: UpdateContentInput): ReportView {
    return toReportView(updateContent(this.context, input))
  }

  confirm(input: ConfirmInput): ReportView {
    return toReportView(confirmReport(this.context, { ...input, actor: input.actor ?? 'user' }))
  }

  revertToDraft(input: { reportId: string; actor?: 'user' }): ReportView {
    return toReportView(revertToDraft(this.context, input))
  }

  deleteReport(reportId: string, actor?: 'user' | 'mcp'): void {
    deleteReport(this.context, reportId, actor)
  }

  restoreReport(reportId: string, actor?: 'user' | 'mcp'): void {
    restoreReport(this.context, reportId, actor)
  }

  export(input: ExportInput): ExportResult {
    return exportReport(this.context, input)
  }

  skills(): SkillSet {
    return this.context.skills.list()
  }

  saveSkill(input: SkillInput): SkillSet {
    return this.context.skills.save(input)
  }

  deleteSkill(id: string): SkillSet {
    return this.context.skills.remove(id)
  }

  setDefaultSkill(id: string): SkillSet {
    return this.context.skills.setDefault(id)
  }

  /** Plugin host hook: `plugin:<plugin-id>:<file>` skills are read-only here. */
  registerSkill(skill: WeeklySkill): void {
    this.context.skills.registerSkill(skill)
  }

  unregisterSkill(id: string): void {
    this.context.skills.unregisterPluginSkill(id)
  }

  /** Re-validates a stored draft with the current digest. */
  validate(reportId: string): Violation[] {
    const settings = readWeeklySettings(this.context)
    const report = requireReport(this.context.handle.db, reportId, settings.timeZone)
    const digest = collectActivities(digestContextFor(this.context, settings), report.period)
    return validateReport(report.contentMd, digest)
  }
}

function digestContextFor(context: WeeklyContext, settings: ReturnType<typeof readWeeklySettings>): DigestContext {
  return {
    projectId: context.projectId,
    db: context.handle.db,
    globalDb: context.globalDb,
    root: context.root,
    clock: context.clock,
    timeZone: settings.timeZone,
    maxEvents: settings.maxEvents,
    logger: context.logger
  }
}

export function toReportView(report: WeeklyReport): ReportView {
  return {
    reportId: report.id,
    periodStart: report.period.startDate,
    periodEnd: periodEndDate(report.period),
    title: report.title,
    contentMd: report.contentMd,
    contentHash: report.contentHash,
    status: report.status,
    meta: report.meta,
    confirmedAt: report.confirmedAt ?? null,
    deletedAt: report.deletedAt ?? null,
    createdAt: report.createdAt,
    updatedAt: report.updatedAt
  }
}

export function toDigestView(digest: ActivityDigest): DigestView {
  const coverage = coverageOf(digest)
  return {
    periodStart: digest.period.startDate,
    periodEnd: periodEndDate(digest.period),
    timeZone: digest.timeZone,
    eventCount: coverage.event_count,
    countsByType: digest.stats.countsByType,
    countsBySection: Object.fromEntries(
      Object.entries(digest.stats.countsBySection).map(([key, value]) => [key, value ?? 0])
    ),
    truncated: digest.truncated,
    warnings: digest.warnings,
    digestHash: digest.digestHash,
    collectedAt: digest.collectedAt,
    sections: digest.sections.map((section) => ({
      key: section.key,
      title: section.title,
      items: section.items.map((item) => ({
        type: item.ref.type,
        id: item.ref.id,
        kind: item.kind,
        summary: item.summary,
        detail: item.detail,
        old: item.old,
        new: item.new,
        state: item.state,
        actor: item.actor,
        firstAt: item.firstAt,
        lastAt: item.lastAt,
        unconfirmedSource: item.unconfirmedSource,
        broken: item.broken
      }))
    })),
    blocked: digest.blocked.map((task) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      columnName: task.columnName,
      isDone: task.isDone,
      reason: task.reason
    })),
    planned: digest.planned.map((task) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      columnName: task.columnName,
      isDone: task.isDone,
      deadline: task.deadline ?? null,
      weekPlan: task.weekPlan ?? null,
      origin: task.origin
    }))
  }
}

export function toOutcomeView(outcome: {
  report: WeeklyReport
  digest: ActivityDigest
  violations: Violation[]
}): GenerateOutcomeView {
  return {
    report: toReportView(outcome.report),
    digest: toDigestView(outcome.digest),
    violations: outcome.violations
  }
}

/** Report + digest in one read, for the MCP detail payload. */
export function reportWithDigest(context: WeeklyContext, reportId: string) {
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, reportId, settings.timeZone)
  const digest = collectActivities(digestContextFor(context, settings), report.period)
  return { report, digest }
}

export { periodEndDate }