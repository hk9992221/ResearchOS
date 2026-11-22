import { AppError, AppErrorCode } from '@researchos/shared'

import { newId } from '../../ids'
import type { Actor as EventActor } from '../../schema'
import type { WeeklyContext } from './context'
import { defaultPeriod, validatePeriod } from './period'
import {
  assertContentSize,
  contentHash,
  insertReportRow,
  listReports,
  projectInfo,
  readWeeklySettings,
  requireReport,
  requireWritable,
  reportTitle,
  updateReportRow,
  type ListReportsOptions
} from './store'
import { buildSkeleton } from './template'
import type {
  ActivityDigest,
  ConfirmInput,
  Coverage,
  EntityRef,
  PeriodHistoryEntry,
  ReportPage,
  ReportStatus,
  UpdateContentInput,
  WeeklyActor,
  WeeklyPeriod,
  WeeklyReport
} from './types'
import { applyAckFingerprints, buildCheckState, mergeAcks, validateReport } from './validate'
import { collectActivities, coverageOf, digestRefs, type DigestContext } from './digest'
import type { WeeklySettings } from './context'

const MAX_PERIOD_HISTORY = 52

/** Maps the caller actor to the Activity actor; AI never writes a report. */
export function writeActor(actor: WeeklyActor | undefined): EventActor {
  switch (actor) {
    case undefined:
    case 'user':
      return 'user'
    case 'mcp':
      return 'mcp'
    case 'ai':
      throw new AppError(AppErrorCode.InvalidArgument, 'ai 不能直接写周报', {
        reason: 'permission.invalid_actor'
      })
    default:
      throw new AppError(AppErrorCode.InvalidArgument, `未知操作者 ${JSON.stringify(actor)}`, {
        reason: 'permission.invalid_actor'
      })
  }
}

export { readWeeklySettings, requireReport, requireWritable }

export function digestContext(context: WeeklyContext, settings?: WeeklySettings): DigestContext {
  const resolved = settings ?? readWeeklySettings(context)
  return {
    projectId: context.projectId,
    db: context.handle.db,
    globalDb: context.globalDb,
    root: context.root,
    clock: context.clock,
    timeZone: resolved.timeZone,
    maxEvents: resolved.maxEvents,
    logger: context.logger
  }
}

export function resolvePeriod(
  context: WeeklyContext,
  period: WeeklyPeriod | undefined,
  settings?: WeeklySettings
): WeeklyPeriod {
  const resolved = settings ?? readWeeklySettings(context)
  const chosen =
    period ??
    defaultPeriod(context.clock.now(), {
      weekStartDay: resolved.weekStartDay,
      timeZone: resolved.timeZone,
      defaultPeriod: resolved.defaultPeriod
    })
  const withZone: WeeklyPeriod =
    chosen.timeZone === '' ? { ...chosen, timeZone: resolved.timeZone } : chosen
  validatePeriod(withZone)
  return withZone
}

export function getReport(context: WeeklyContext, id: string): WeeklyReport {
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, id, settings.timeZone, { includeDeleted: true })
  report.title = reportTitle(projectInfo(context.globalDb, context.projectId).name, report.period)
  return report
}

export function createReport(
  context: WeeklyContext,
  input: {
    period?: WeeklyPeriod
    skeleton?: boolean
    force?: boolean
    actor?: WeeklyActor
  }
): WeeklyReport {
  const actor = writeActor(input.actor)
  const settings = readWeeklySettings(context)
  const period = resolvePeriod(context, input.period, settings)
  requireWritable(context.globalDb, context.projectId)
  const info = projectInfo(context.globalDb, context.projectId)
  const now = context.clock.now().toISOString()
  const report: WeeklyReport = {
    id: newId(context.clock),
    period,
    title: reportTitle(info.name, period),
    contentMd: input.skeleton === true ? buildSkeleton(info.name, period) : '',
    contentHash: '',
    status: 'draft',
    meta: {
      generation: {
        mode: 'manual',
        ai_status: 'skipped',
        template_id: 'default-7',
        template_version: 1,
        attempts: 0
      }
    },
    createdAt: now,
    updatedAt: now
  }
  report.contentHash = contentHash(report.contentMd)
  const startDate = period.startDate
  const endDate = periodEnd(period)

  context.handle.inTx(() => {
    if (input.force !== true) {
      const existing = context.handle.db
        .prepare(
          `SELECT id FROM weekly_reports
            WHERE period_start = ? AND period_end = ? AND deleted_at IS NULL
            ORDER BY created_at, id LIMIT 1`
        )
        .get(startDate, endDate) as { id: string } | undefined
      if (existing !== undefined) {
        throw new AppError(AppErrorCode.Conflict, `该周期已存在周报 ${existing.id}`, {
          reason: 'report.period_occupied',
          existingId: existing.id
        })
      }
    }
    insertReportRow(context.handle.db, report)
    context.events.emit({
      type: 'entity.created',
      entityType: 'weekly_report',
      entityId: report.id,
      summary: `创建周报 ${startDate}–${endDate}`,
      actor,
      at: context.clock.now()
    })
  })
  return getReport(context, report.id)
}

function periodEnd(period: WeeklyPeriod): string {
  const [year, month, day] = period.endExclusiveDate.split('-').map(Number)
  return new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1) - 86_400_000)
    .toISOString()
    .slice(0, 10)
}

export function listReportPage(context: WeeklyContext, options: ListReportsOptions = {}): ReportPage {
  const settings = readWeeklySettings(context)
  const info = projectInfo(context.globalDb, context.projectId)
  for (const status of options.status ?? []) {
    if (status !== 'draft' && status !== 'confirmed') {
      throw new AppError(AppErrorCode.InvalidArgument, `无效状态 ${JSON.stringify(status)}`, {
        field: 'status'
      })
    }
  }
  if (options.order !== undefined && !['period_start', '-period_start', 'updated_at'].includes(options.order)) {
    throw new AppError(AppErrorCode.InvalidArgument, `无效排序 ${JSON.stringify(options.order)}`, {
      field: 'order'
    })
  }
  return listReports(context.handle.db, info.name, settings.timeZone, options)
}

/** Report periods plus the current/previous placeholders the UI offers. */
export function listPeriods(
  context: WeeklyContext,
  limit = MAX_PERIOD_HISTORY
): PeriodHistoryEntry[] {
  const settings = readWeeklySettings(context)
  const current = defaultPeriod(context.clock.now(), {
    weekStartDay: settings.weekStartDay,
    timeZone: settings.timeZone,
    defaultPeriod: 'current'
  })
  const previous = defaultPeriod(context.clock.now(), {
    weekStartDay: settings.weekStartDay,
    timeZone: settings.timeZone,
    defaultPeriod: 'previous'
  })
  const rows = context.handle.db
    .prepare(
      `SELECT id, period_start, period_end, status FROM weekly_reports
        WHERE deleted_at IS NULL
        ORDER BY period_start DESC, created_at DESC, id DESC`
    )
    .all() as Array<{ id: string; period_start: string; period_end: string; status: ReportStatus }>

  const merged = new Map<string, PeriodHistoryEntry>()
  const placeholders: PeriodHistoryEntry[] = [
    { period: current, reportId: null, status: null },
    { period: previous, reportId: null, status: null }
  ]
  for (const entry of placeholders) {
    merged.set(entry.period.startDate, entry)
  }
  for (const row of rows) {
    const endExclusive = addDay(row.period_end)
    merged.set(row.period_start, {
      period: {
        startDate: row.period_start,
        endExclusiveDate: endExclusive,
        timeZone: settings.timeZone
      },
      reportId: row.id,
      status: row.status
    })
  }
  return [...merged.values()]
    .sort((left, right) =>
      left.period.startDate < right.period.startDate
        ? 1
        : left.period.startDate > right.period.startDate
          ? -1
          : 0
    )
    .slice(0, Math.max(1, Math.min(limit, MAX_PERIOD_HISTORY)))
}

function addDay(date: string): string {
  const [year, month, day] = date.split('-').map(Number)
  return new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1) + 86_400_000)
    .toISOString()
    .slice(0, 10)
}

export function updateContent(context: WeeklyContext, input: UpdateContentInput): WeeklyReport {
  writeActor(input.actor)
  assertContentSize(input.contentMd)
  if (input.expectedHash === undefined && input.expectedUpdatedAt === undefined) {
    throw new AppError(AppErrorCode.Conflict, '缺少并发令牌（expectedHash 或 expectedUpdatedAt）', {
      reason: 'report.content_conflict'
    })
  }
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  if (report.status === 'confirmed') {
    throw new AppError(AppErrorCode.Conflict, '已确认周报不可编辑；请先撤回为草稿', {
      reason: 'report.confirmed_read_only'
    })
  }
  if (input.expectedHash !== undefined && input.expectedHash !== report.contentHash) {
    throw conflict('内容已在别处修改', report)
  }
  if (input.expectedUpdatedAt !== undefined && input.expectedUpdatedAt !== report.updatedAt) {
    throw conflict('内容已在别处修改', report)
  }

  const digest = collectActivities(digestContext(context, settings), report.period)
  const violations = mergeAcks(validateReport(input.contentMd, digest), report.meta.checks)
  const now = context.clock.now().toISOString()

  context.handle.inTx(() => {
    const current = requireReport(context.handle.db, input.reportId, settings.timeZone)
    if (current.status === 'confirmed') {
      throw new AppError(AppErrorCode.Conflict, '已确认周报不可编辑', {
        reason: 'report.confirmed_read_only'
      })
    }
    if (input.expectedHash !== undefined && current.contentHash !== input.expectedHash) {
      throw conflict('内容已在别处修改', current)
    }
    if (input.expectedUpdatedAt !== undefined && current.updatedAt !== input.expectedUpdatedAt) {
      throw conflict('内容已在别处修改', current)
    }
    current.contentMd = input.contentMd
    current.updatedAt = now
    current.meta = {
      ...current.meta,
      checks: buildCheckState(violations, now),
      confirmed_hash: undefined
    }
    updateReportRow(context.handle.db, current)
  })
  return getReport(context, input.reportId)
}

function conflict(message: string, report: WeeklyReport): AppError {
  return new AppError(AppErrorCode.Conflict, message, {
    reason: 'report.content_conflict',
    updatedAt: report.updatedAt,
    contentHash: report.contentHash
  })
}

export function confirm(context: WeeklyContext, input: ConfirmInput): WeeklyReport {
  if (input.actor === 'mcp' || input.actor === 'ai') {
    throw new AppError(AppErrorCode.UserConfirmationRequired, '确认必须由应用内用户在确认队列中批准', {
      reason: 'confirmation.required'
    })
  }
  writeActor(input.actor)
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  if (report.status !== 'draft') {
    throw new AppError(AppErrorCode.InvalidArgument, '只有草稿可以确认', {
      reason: 'report.not_draft'
    })
  }
  const digest = collectActivities(digestContext(context, settings), report.period)
  const now = context.clock.now().toISOString()
  const violations = applyAckFingerprints(
    mergeAcks(validateReport(report.contentMd, digest), report.meta.checks),
    input.ackFingerprints,
    input.ackNotes,
    now
  )
  const unresolved = violations.filter((violation) => violation.ack === undefined)
  if (unresolved.length > 0) {
    throw new AppError(AppErrorCode.Conflict, '存在未解决的校验项，无法确认', {
      reason: 'report.unresolved_violations',
      violations: unresolved
    })
  }

  context.handle.inTx(() => {
    const current = requireReport(context.handle.db, input.reportId, settings.timeZone)
    if (current.status !== 'draft') {
      throw new AppError(AppErrorCode.InvalidArgument, '只有草稿可以确认', {
        reason: 'report.not_draft'
      })
    }
    if (current.contentHash !== report.contentHash) {
      throw conflict('报告内容在校验后发生变化，请重新确认', current)
    }
    const outputIds = sectionOutputIds(current)
    if (outputIds.length > 0) {
      if (context.ai === undefined) {
        throw new AppError(AppErrorCode.Internal, 'AI 输出服务未配置，无法在同一事务内确认', {
          traceId: 'weekly:confirm-output'
        })
      }
      for (const id of outputIds) context.ai.confirmOutput(id, 'user')
    }
    current.status = 'confirmed'
    current.confirmedAt = now
    current.updatedAt = now
    current.meta = {
      ...current.meta,
      confirmed_hash: contentHash(current.contentMd),
      checks: buildCheckState(violations, now)
    }
    updateReportRow(context.handle.db, current)
    context.events.emit({
      type: 'status.changed',
      entityType: 'weekly_report',
      entityId: current.id,
      summary: `确认周报 ${current.period.startDate}–${periodEnd(current.period)}`,
      actor: 'user',
      at: context.clock.now(),
      old: { status: 'draft' },
      new: { status: 'confirmed' }
    })
  })
  return getReport(context, input.reportId)
}

export function sectionOutputIds(report: WeeklyReport): string[] {
  const generation = report.meta.generation
  if (generation === undefined) return []
  if (generation.ai_output_ids !== undefined && generation.ai_output_ids.length > 0) {
    return generation.ai_output_ids
  }
  return generation.ai_output_id === undefined || generation.ai_output_id === ''
    ? []
    : [generation.ai_output_id]
}

export function revertToDraft(
  context: WeeklyContext,
  input: { reportId: string; actor?: WeeklyActor }
): WeeklyReport {
  if (input.actor === 'mcp' || input.actor === 'ai') {
    throw new AppError(AppErrorCode.UserConfirmationRequired, '撤回仅允许应用内用户操作', {
      reason: 'confirmation.required'
    })
  }
  writeActor(input.actor)
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  if (report.status !== 'confirmed') {
    throw new AppError(AppErrorCode.InvalidArgument, '只有已确认周报可以撤回', {
      reason: 'report.not_confirmed'
    })
  }
  const now = context.clock.now().toISOString()
  context.handle.inTx(() => {
    const current = requireReport(context.handle.db, input.reportId, settings.timeZone)
    if (current.status !== 'confirmed') {
      throw new AppError(AppErrorCode.InvalidArgument, '只有已确认周报可以撤回', {
        reason: 'report.not_confirmed'
      })
    }
    current.status = 'draft'
    current.confirmedAt = undefined
    current.updatedAt = now
    updateReportRow(context.handle.db, current)
    context.events.emit({
      type: 'status.changed',
      entityType: 'weekly_report',
      entityId: current.id,
      summary: `撤回周报 ${current.period.startDate}–${periodEnd(current.period)}`,
      actor: 'user',
      at: context.clock.now(),
      old: { status: 'confirmed' },
      new: { status: 'draft' }
    })
  })
  return getReport(context, input.reportId)
}

export function deleteReport(
  context: WeeklyContext,
  reportId: string,
  actorInput?: WeeklyActor
): void {
  const actor = writeActor(actorInput)
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  requireReport(context.handle.db, reportId, settings.timeZone)
  if (context.trash === undefined) {
    throw new AppError(AppErrorCode.Internal, '回收站服务未配置', { traceId: 'weekly:trash' })
  }
  context.handle.inTx(() => {
    context.trash?.softDelete({ type: 'weekly_report', id: reportId }, actor)
  })
}

export function restoreReport(
  context: WeeklyContext,
  reportId: string,
  actorInput?: WeeklyActor
): void {
  const actor = writeActor(actorInput)
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  requireReport(context.handle.db, reportId, settings.timeZone, { includeDeleted: true })
  if (context.trash === undefined) {
    throw new AppError(AppErrorCode.Internal, '回收站服务未配置', { traceId: 'weekly:trash' })
  }
  context.handle.inTx(() => {
    context.trash?.restore({ type: 'weekly_report', id: reportId }, actor)
  })
}

/** Digest for one period; read-only. */
export function digestFor(
  context: WeeklyContext,
  period: WeeklyPeriod | undefined,
  maxEvents?: number
) {
  const settings = readWeeklySettings(context)
  const resolved = resolvePeriod(context, period, settings)
  return collectActivities(digestContext(context, settings), resolved, maxEvents ?? settings.maxEvents)
}

/** The meta.coverage and meta.refs a stored draft carries. */
export function coverageMeta(digest: ActivityDigest): {
  coverage: Coverage
  refs: EntityRef[]
} {
  return { coverage: coverageOf(digest), refs: digestRefs(digest) }
}