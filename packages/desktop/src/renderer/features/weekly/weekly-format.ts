/**
 * Calendar math for the weekly review pages.
 *
 * The report period is an inclusive `[start, end]` day pair;
 * a "week" is the configured `weekStartsOn` day plus six more. Everything here
 * works on `YYYY-MM-DD` calendar strings in UTC arithmetic so a DST shift can
 * never move a day, and the current day is resolved in the workspace timezone
 * with `Intl` — the same rule the Go shell uses for `DefaultPeriod`.
 */

import type { Period } from '@researchos/shared'

export const WEEKDAY_KEYS = ['week_start_sunday', 'week_start_monday'] as const

const DAY_MS = 86_400_000

export function isCalendarDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false
  return parseDate(value) !== null
}

function parseDate(value: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (match === null) return null
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const date = new Date(Date.UTC(year, month - 1, day))
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  ) {
    return null
  }
  return date
}

export function addDays(date: string, days: number): string {
  const parsed = parseDate(date)
  if (parsed === null) throw new Error(`invalid calendar date: ${date}`)
  return new Date(parsed.getTime() + days * DAY_MS).toISOString().slice(0, 10)
}

/** weekDayOf is 0 for Sunday … 6 for Saturday. */
export function weekDayOf(date: string): number {
  const parsed = parseDate(date)
  if (parsed === null) throw new Error(`invalid calendar date: ${date}`)
  return parsed.getUTCDay()
}

/** startOfWeek moves a date back to the configured first day of its week. */
export function startOfWeek(date: string, weekStartsOn: 0 | 1): string {
  const offset = (weekDayOf(date) - weekStartsOn + 7) % 7
  return addDays(date, -offset)
}

/** periodAround builds the inclusive seven-day window that contains a day. */
export function periodAround(date: string, weekStartsOn: 0 | 1): Period {
  const start = startOfWeek(date, weekStartsOn)
  return { start, end: addDays(start, 6) }
}

export function previousPeriod(period: Period): Period {
  const start = addDays(period.start, -7)
  return { start, end: addDays(start, 6) }
}

/** periodDays is the inclusive length of a day window. */
export function periodDays(period: Period): number {
  const start = Date.parse(`${period.start}T00:00:00Z`)
  const end = Date.parse(`${period.end}T00:00:00Z`)
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return 0
  return Math.round((end - start) / DAY_MS) + 1
}

/** todayInZone is the calendar day of `now` in an IANA timezone. */
export function todayInZone(timeZone: string, now: Date = new Date()): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  }).formatToParts(now)
  const get = (type: string): string => parts.find((part) => part.type === type)?.value ?? ''
  const value = `${get('year')}-${get('month')}-${get('day')}`
  return isCalendarDate(value) ? value : now.toISOString().slice(0, 10)
}

/**
 * periodLabel renders `YYYY-Www`.
 *
 * A Monday week is numbered by ISO-8601 (week 1 is the week with the first
 * Thursday). A Sunday week uses the US convention: week 1 is the week that
 * contains January 1. Both are read-only labels; the stored period stays the
 * authoritative `[start, end]` pair.
 */
export function periodLabel(start: string, weekStartsOn: 0 | 1): string {
  return weekStartsOn === 1 ? isoWeekLabel(start) : sundayWeekLabel(start)
}

function isoWeekLabel(date: string): string {
  const parsed = parseDate(date)
  if (parsed === null) throw new Error(`invalid calendar date: ${date}`)
  // Move to the Thursday of the ISO week; its year is the label's year.
  const thursday = new Date(parsed.getTime())
  const isoDay = (thursday.getUTCDay() + 6) % 7
  thursday.setUTCDate(thursday.getUTCDate() - isoDay + 3)
  const year = thursday.getUTCFullYear()
  const firstThursday = new Date(Date.UTC(year, 0, 4))
  const firstIsoDay = (firstThursday.getUTCDay() + 6) % 7
  firstThursday.setUTCDate(firstThursday.getUTCDate() - firstIsoDay + 3)
  const week = 1 + Math.round((thursday.getTime() - firstThursday.getTime()) / (7 * DAY_MS))
  return `${year}-W${String(week).padStart(2, '0')}`
}

function sundayWeekLabel(date: string): string {
  const parsed = parseDate(date)
  if (parsed === null) throw new Error(`invalid calendar date: ${date}`)
  // The label year is the year of the day itself, not of the Sunday that may
  // fall in the previous year (the week of 2026-01-01 starts 2025-12-28).
  const year = parsed.getUTCFullYear()
  const start = parseDate(startOfWeek(date, 0))
  if (start === null) throw new Error(`invalid calendar date: ${date}`)
  const januaryFirst = Date.UTC(year, 0, 1)
  const januaryFirstDay = new Date(januaryFirst).getUTCDay()
  // Week 1 is the week that contains January 1: it starts on the Sunday on or
  // before January 1.
  const weekOneStart = januaryFirst - januaryFirstDay * DAY_MS
  const week = Math.round((start.getTime() - weekOneStart) / (7 * DAY_MS)) + 1
  return `${year}-W${String(week).padStart(2, '0')}`
}

/** formatDay renders a calendar day in the UI language. */
export function formatDay(date: string, locale: string): string {
  const parsed = parseDate(date)
  if (parsed === null) return date
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    timeZone: 'UTC'
  }).format(parsed)
}

/** formatInstant renders an ISO instant in the UI language and timezone. */
export function formatInstant(instant: string, locale: string, timeZone?: string): string {
  const date = new Date(instant)
  if (Number.isNaN(date.getTime())) return instant
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    timeZone
  }).format(date)
}

