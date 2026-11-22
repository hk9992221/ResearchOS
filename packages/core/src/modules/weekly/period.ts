import { AppError, AppErrorCode } from '@researchos/shared'

import { addDays } from './template'
import type { WeeklyPeriod } from './types'

/**
 * Report-window arithmetic in the configured timezone.
 *
 * The window is half-open `[startDate 00:00, endExclusiveDate 00:00)` in the
 * report timezone. Calendar dates are the only durable representation; a
 * `Date` is produced only to compare against the stored UTC activity stamps.
 */

export const DEFAULT_WEEK_START_DAY = 1 // Go weekday numbering: Monday
export const DEFAULT_MAX_EVENTS = 2000
export const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/

const MAX_PERIOD_DAYS = 366

export function systemTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

/** Validates an IANA zone name; an unknown zone falls back to the system one. */
export function resolveTimeZone(raw: string | undefined): string {
  if (raw === undefined || raw.trim() === '') return systemTimeZone()
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: raw.trim() })
    return raw.trim()
  } catch {
    return systemTimeZone()
  }
}

export function isCalendarDate(value: string): boolean {
  if (!DATE_PATTERN.test(value)) return false
  const [year, month, day] = value.split('-').map(Number)
  if (year === undefined || month === undefined || day === undefined) return false
  const date = new Date(Date.UTC(year, month - 1, day))
  return (
    date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
  )
}

/** The calendar date of an instant in a timezone. */
export function localDate(instant: Date, timeZone: string): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  }).formatToParts(instant)
  const get = (type: string): string => parts.find((part) => part.type === type)?.value ?? ''
  return `${get('year')}-${get('month')}-${get('day')}`
}

/** Offset of `timeZone` at `instant`, in milliseconds east of UTC. */
function zoneOffsetMs(timeZone: string, instant: Date): number {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hour12: false,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  }).formatToParts(instant)
  const get = (type: string): number => Number(parts.find((part) => part.type === type)?.value)
  const hour = get('hour') === 24 ? 0 : get('hour')
  const asUtc = Date.UTC(get('year'), get('month') - 1, get('day'), hour, get('minute'), get('second'))
  return asUtc - instant.getTime()
}

/**
 * The UTC instant of local midnight on `date`. Two offset probes settle DST
 * transitions, where the first probe can land on the wrong side of the change.
 */
export function zonedMidnightUtc(date: string, timeZone: string): Date {
  const naive = Date.parse(`${date}T00:00:00Z`)
  let instant = naive - zoneOffsetMs(timeZone, new Date(naive))
  const second = naive - zoneOffsetMs(timeZone, new Date(instant))
  if (second !== instant) instant = second
  return new Date(instant)
}

/** ISO-8601 week label of a calendar date: `2026-W46`. */
export function isoWeek(date: string): string {
  const [year, month, day] = date.split('-').map(Number)
  const target = new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1))
  const dayNumber = (target.getUTCDay() + 6) % 7 // Monday = 0
  target.setUTCDate(target.getUTCDate() - dayNumber + 3) // Thursday of this week
  const firstThursday = new Date(Date.UTC(target.getUTCFullYear(), 0, 4))
  firstThursday.setUTCDate(firstThursday.getUTCDate() - ((firstThursday.getUTCDay() + 6) % 7) + 3)
  const week = 1 + Math.round((target.getTime() - firstThursday.getTime()) / (7 * 86_400_000))
  return `${target.getUTCFullYear()}-W${String(week).padStart(2, '0')}`
}

/** Most recent `startDay` (Go numbering, 0 = Sunday) on or before `date`. */
export function weekStartDate(date: string, startDay: number): string {
  const [year, month, day] = date.split('-').map(Number)
  const target = new Date(Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1))
  const delta = (target.getUTCDay() - startDay + 7) % 7
  return addDays(date, -delta)
}

export interface WeekSettings {
  weekStartDay: number
  timeZone: string
  defaultPeriod: 'current' | 'previous'
}

export function defaultPeriod(now: Date, settings: WeekSettings): WeeklyPeriod {
  const today = localDate(now, settings.timeZone)
  let start = weekStartDate(today, settings.weekStartDay)
  if (settings.defaultPeriod === 'previous') start = addDays(start, -7)
  return {
    startDate: start,
    endExclusiveDate: addDays(start, 7),
    timeZone: settings.timeZone
  }
}

/** Builds a period from inclusive calendar dates (IPC and MCP shape). */
export function periodFromDates(
  start: string,
  endInclusive: string,
  timeZone: string
): WeeklyPeriod {
  const period = {
    startDate: start,
    endExclusiveDate: addDays(endInclusive, 1),
    timeZone
  }
  validatePeriod(period)
  return period
}

/** Builds a period from a stored `[start, end]` pair without a settings read. */
export function storedPeriod(start: string, endInclusive: string, timeZone: string): WeeklyPeriod {
  return { startDate: start, endExclusiveDate: addDays(endInclusive, 1), timeZone }
}

export function validatePeriod(period: WeeklyPeriod): void {
  const invalid = (message: string): never => {
    throw new AppError(AppErrorCode.InvalidArgument, message, { reason: 'report.invalid_period' })
  }
  if (!isCalendarDate(period.startDate) || !isCalendarDate(period.endExclusiveDate)) {
    invalid('period dates must be YYYY-MM-DD calendar dates')
  }
  if (period.endExclusiveDate <= period.startDate) {
    invalid('period end must be after its start')
  }
  const days = periodDays(period)
  if (days < 1) invalid('period must be at least one day')
  if (days > MAX_PERIOD_DAYS) invalid('period must be at most 366 days')
}

/** Number of calendar days in the half-open window. */
export function periodDays(period: WeeklyPeriod): number {
  const start = Date.parse(`${period.startDate}T00:00:00Z`)
  const end = Date.parse(`${period.endExclusiveDate}T00:00:00Z`)
  return Math.round((end - start) / 86_400_000)
}

/** Half-open UTC bounds of the window, for `created_at` comparisons. */
export function utcBounds(period: WeeklyPeriod): { since: string; until: string } {
  return {
    since: zonedMidnightUtc(period.startDate, period.timeZone).toISOString(),
    until: zonedMidnightUtc(period.endExclusiveDate, period.timeZone).toISOString()
  }
}

/** The next ISO week's start date (used for planned-task candidates). */
export function followingPeriod(period: WeeklyPeriod): WeeklyPeriod {
  return {
    startDate: period.endExclusiveDate,
    endExclusiveDate: addDays(period.endExclusiveDate, periodDays(period)),
    timeZone: period.timeZone
  }
}