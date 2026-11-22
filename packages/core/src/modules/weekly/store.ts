import { createHash } from 'node:crypto'

import { AppError, AppErrorCode } from '@researchos/shared'

import { formatTimestamp, parseTimestamp } from '../../clock'
import type { Db } from '../../db/open'
import type { ReportView, WeeklyContext, WeeklySettings } from './context'
import { DEFAULT_MAX_EVENTS, DEFAULT_WEEK_START_DAY, resolveTimeZone } from './period'
import { periodEndDate, excerptOf } from './template'
import type {
  ReportMeta,
  ReportPage,
  ReportStatus,
  ReportSummary,
  WeeklyPeriod,
  WeeklyReport
} from './types'

export const DEFAULT_PAGE_SIZE = 20
export const MAX_PAGE_SIZE = 100
export const MAX_CONTENT_BYTES = 512 * 1024

const REPORT_COLUMNS = `id, period_start, period_end, content_md, status, meta, confirmed_at,
  ai_output_id, created_at, updated_at, deleted_at`

interface ReportRow {
  id: string
  period_start: string
  period_end: string
  content_md: string
  status: string
  meta: string
  confirmed_at: string | null
  ai_output_id: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
}

export interface ProjectInfo {
  id: string
  name: string
  summary: string
  goals: string
}

/** Reads the `weekly.*` settings, applying the weekly-review defaults. */
export function readWeeklySettings(context: WeeklyContext): WeeklySettings {
  const weekStart = Number.parseInt(unquoteSetting(context.settings.get('weekly.week_start_day') ?? ''), 10)
  const maxEvents = Number.parseInt(unquoteSetting(context.settings.get('weekly.max_digest_events') ?? ''), 10)
  const defaultRaw = unquoteSetting(context.settings.get('weekly.default_period') ?? '')
  return {
    weekStartDay:
      Number.isInteger(weekStart) && weekStart >= 0 && weekStart <= 6 ? weekStart : DEFAULT_WEEK_START_DAY,
    timeZone: resolveTimeZone(unquoteSetting(context.settings.get('weekly.timezone') ?? '')),
    defaultPeriod: defaultRaw === 'previous' ? 'previous' : 'current',
    maxEvents: Number.isInteger(maxEvents) && maxEvents > 0 ? maxEvents : DEFAULT_MAX_EVENTS
  }
}

/** Settings may hold a JSON-quoted value; both forms are accepted. */
export function unquoteSetting(raw: string): string {
  const trimmed = raw.trim()
  if (trimmed.startsWith('"') && trimmed.endsWith('"')) {
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (typeof parsed === 'string') return parsed
    } catch {
      return trimmed
    }
  }
  return trimmed
}

export function projectInfo(globalDb: Db, projectId: string): ProjectInfo {
  const row = globalDb
    .prepare('SELECT id, name, summary, goals FROM projects WHERE id = ?')
    .get(projectId) as ProjectInfo | undefined
  if (row === undefined) {
    throw new AppError(AppErrorCode.NotFound, `项目 ${projectId} 不存在`, {
      reason: 'project.not_found'
    })
  }
  return row
}

/** Archived projects (and missing ones) refuse every write. */
export function requireWritable(globalDb: Db, projectId: string): void {
  const row = globalDb.prepare('SELECT status FROM projects WHERE id = ?').get(projectId) as
    | { status: string }
    | undefined
  if (row === undefined) {
    throw new AppError(AppErrorCode.NotFound, `项目 ${projectId} 不存在`, {
      reason: 'project.not_found'
    })
  }
  if (row.status === 'archived') {
    throw new AppError(AppErrorCode.ArchivedReadOnly, '项目已归档，周报为只读', {
      reason: 'project.archived'
    })
  }
}

export function contentHash(content: string): string {
  return createHash('sha256').update(content).digest('hex')
}

function decodeMeta(raw: string): ReportMeta {
  try {
    const parsed: unknown = JSON.parse(raw === '' ? '{}' : raw)
    return typeof parsed === 'object' && parsed !== null ? (parsed as ReportMeta) : {}
  } catch {
    return {}
  }
}

export function encodeMeta(meta: ReportMeta): string {
  return JSON.stringify(meta)
}

function scanReport(row: ReportRow, timeZone: string): WeeklyReport {
  const period: WeeklyPeriod = {
    startDate: row.period_start,
    endExclusiveDate: addOneDay(row.period_end),
    timeZone
  }
  const meta = decodeMeta(row.meta)
  const report: WeeklyReport = {
    id: row.id,
    period,
    title: '',
    contentMd: row.content_md,
    contentHash: contentHash(row.content_md),
    status: row.status === 'confirmed' ? 'confirmed' : 'draft',
    meta,
    createdAt: parseTimestamp(row.created_at).toISOString(),
    updatedAt: parseTimestamp(row.updated_at).toISOString()
  }
  if (row.confirmed_at !== null) report.confirmedAt = parseTimestamp(row.confirmed_at).toISOString()
  if (row.deleted_at !== null) report.deletedAt = parseTimestamp(row.deleted_at).toISOString()
  const aiOutputId = row.ai_output_id ?? meta.generation?.ai_output_id
  if (aiOutputId !== undefined && aiOutputId !== null && aiOutputId !== '') {
    report.aiOutputId = aiOutputId
    if (meta.generation !== undefined && meta.generation.ai_output_id === undefined) {
      meta.generation.ai_output_id = aiOutputId
    }
  }
  return report
}

function addOneDay(date: string): string {
  const [year, month, day] = date.split('-').map(Number)
  return new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1) + 86_400_000)
    .toISOString()
    .slice(0, 10)
}

export function readReport(
  db: Db,
  id: string,
  timeZone: string,
  options: { includeDeleted?: boolean } = {}
): WeeklyReport | undefined {
  const query =
    `SELECT ${REPORT_COLUMNS} FROM weekly_reports WHERE id = ?` +
    (options.includeDeleted === true ? '' : ' AND deleted_at IS NULL')
  const row = db.prepare(query).get(id) as ReportRow | undefined
  return row === undefined ? undefined : scanReport(row, timeZone)
}

export function requireReport(
  db: Db,
  id: string,
  timeZone: string,
  options: { includeDeleted?: boolean } = {}
): WeeklyReport {
  const report = readReport(db, id, timeZone, options)
  if (report === undefined) {
    throw new AppError(AppErrorCode.NotFound, `周报 ${id} 不存在`, { reason: 'report.not_found', reportId: id })
  }
  return report
}

/** Persists content, lifecycle, meta and the derived `ai_output_id` column. */
export function updateReportRow(db: Db, report: WeeklyReport): void {
  const aiOutputId = report.meta.generation?.ai_output_id ?? report.aiOutputId ?? null
  db.prepare(
    `UPDATE weekly_reports
        SET content_md = ?, status = ?, meta = ?, confirmed_at = ?, ai_output_id = ?,
            updated_at = ?, deleted_at = ?
      WHERE id = ?`
  ).run(
    report.contentMd,
    report.status,
    encodeMeta(report.meta),
    report.confirmedAt === undefined ? null : formatTimestamp(new Date(report.confirmedAt)),
    aiOutputId,
    formatTimestamp(new Date(report.updatedAt)),
    report.deletedAt === undefined ? null : formatTimestamp(new Date(report.deletedAt)),
    report.id
  )
  report.contentHash = contentHash(report.contentMd)
}

/** Inserts one report row; callers own the transaction. */
export function insertReportRow(db: Db, report: WeeklyReport): void {
  const aiOutputId = report.meta.generation?.ai_output_id ?? null
  db.prepare(
    `INSERT INTO weekly_reports(id, period_start, period_end, content_md, status, meta,
                                 confirmed_at, ai_output_id, created_at, updated_at, deleted_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  ).run(
    report.id,
    report.period.startDate,
    periodEndDate(report.period),
    report.contentMd,
    report.status,
    encodeMeta(report.meta),
    report.confirmedAt === undefined ? null : formatTimestamp(new Date(report.confirmedAt)),
    aiOutputId,
    formatTimestamp(new Date(report.createdAt)),
    formatTimestamp(new Date(report.updatedAt)),
    report.deletedAt === undefined ? null : formatTimestamp(new Date(report.deletedAt))
  )
}

export function reportTitle(projectName: string, period: WeeklyPeriod): string {
  return `${projectName} 周报（${period.startDate} – ${periodEndDate(period)}）`
}

export interface ListReportsOptions {
  status?: ReportStatus[]
  from?: string
  to?: string
  includeDeleted?: boolean
  page?: number
  pageSize?: number
  order?: 'period_start' | '-period_start' | 'updated_at'
}

export function listReports(
  db: Db,
  projectName: string,
  timeZone: string,
  options: ListReportsOptions = {}
): ReportPage {
  const page = options.page === undefined || options.page <= 0 ? 1 : options.page
  const pageSize = Math.min(
    options.pageSize === undefined || options.pageSize <= 0 ? DEFAULT_PAGE_SIZE : options.pageSize,
    MAX_PAGE_SIZE
  )
  const where: string[] = []
  const args: unknown[] = []
  if (options.includeDeleted !== true) where.push('deleted_at IS NULL')
  if (options.status !== undefined && options.status.length > 0) {
    where.push(`status IN (${options.status.map(() => '?').join(',')})`)
    args.push(...options.status)
  }
  if (options.from !== undefined) {
    where.push('period_start >= ?')
    args.push(options.from)
  }
  if (options.to !== undefined) {
    where.push('period_start <= ?')
    args.push(options.to)
  }
  const order =
    options.order === 'period_start'
      ? 'period_start ASC, created_at ASC, id ASC'
      : options.order === 'updated_at'
        ? 'updated_at DESC, id DESC'
        : 'period_start DESC, created_at DESC, id DESC'
  const clause = where.length > 0 ? ` WHERE ${where.join(' AND ')}` : ''
  const totalRow = db
    .prepare(`SELECT count(*) AS n FROM weekly_reports${clause}`)
    .get(...args) as { n: number } | undefined
  const total = totalRow?.n ?? 0
  const rows = db
    .prepare(
      `SELECT ${REPORT_COLUMNS} FROM weekly_reports${clause} ORDER BY ${order} LIMIT ? OFFSET ?`
    )
    .all(...args, pageSize, (page - 1) * pageSize) as ReportRow[]

  const items: ReportSummary[] = rows.map((row) => {
    const report = scanReport(row, timeZone)
    const summary: ReportSummary = {
      id: report.id,
      title: reportTitle(projectName, report.period),
      period: report.period,
      status: report.status,
      excerpt: excerptOf(report.contentMd),
      createdAt: report.createdAt,
      updatedAt: report.updatedAt
    }
    if (report.confirmedAt !== undefined) summary.confirmedAt = report.confirmedAt
    if (report.deletedAt !== undefined) summary.deletedAt = report.deletedAt
    return summary
  })
  return { items, total, page, pageSize }
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

export function assertContentSize(content: string): void {
  if (Buffer.byteLength(content, 'utf8') > MAX_CONTENT_BYTES) {
    throw new AppError(AppErrorCode.InvalidArgument, '正文超过 512 KiB 上限', {
      reason: 'report.content_too_large'
    })
  }
}
