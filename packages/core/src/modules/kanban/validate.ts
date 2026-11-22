import { AppErrorCode, AppError } from '@researchos/shared'

import { isValidId } from '../../ids'
import { LIMITS } from './types'

/** AppError helpers with the kanban sentinel semantics of the Go module. */
export function invalid(message: string, details?: Record<string, unknown>): AppError {
  return new AppError(AppErrorCode.InvalidArgument, message, details)
}

export function notFound(message: string, details?: Record<string, unknown>): AppError {
  return new AppError(AppErrorCode.NotFound, message, details)
}

export function conflict(message: string, details?: Record<string, unknown>): AppError {
  return new AppError(AppErrorCode.Conflict, message, details)
}

export function archivedReadOnly(message = 'Project is archived and read-only'): AppError {
  return new AppError(AppErrorCode.ArchivedReadOnly, message)
}

export function limitExceeded(message: string, details?: Record<string, unknown>): AppError {
  return new AppError(AppErrorCode.LimitExceeded, message, details)
}

/** Code-point length; mirrors Go's `utf8.RuneCountInString`. */
export function runeLength(value: string): number {
  return Array.from(value).length
}

export function assertTaskId(value: unknown, field = 'task id'): string {
  if (typeof value !== 'string' || !isValidId(value)) {
    throw invalid(`${field} is not a lowercase UUIDv7: ${String(value)}`, { field })
  }
  return value
}

/** Title rules: trimmed, non-empty, ≤200 code points (TASK-01). */
export function validateTitle(title: string): string {
  const trimmed = title.trim()
  if (trimmed === '') throw invalid('title is empty', { field: 'title' })
  if (runeLength(trimmed) > LIMITS.titleChars) {
    throw invalid(`title exceeds ${LIMITS.titleChars} characters`, { field: 'title' })
  }
  return trimmed
}

export function validateDescription(markdown: string): void {
  if (runeLength(markdown) > LIMITS.descriptionChars) {
    throw invalid(`description exceeds ${LIMITS.descriptionChars} characters`, { field: 'descriptionMd' })
  }
}

const PRIORITIES: Readonly<Record<string, true>> = { high: true, medium: true, low: true, none: true }

export function validatePriority(value: string): void {
  if (!PRIORITIES[value]) throw invalid(`unknown priority: ${value}`, { field: 'priority' })
}

const DEADLINE_PATTERN = /^(\d{4})-(\d{2})-(\d{2})$/

/** Accepts a real calendar date `YYYY-MM-DD`; past dates are legal (TASK-05). */
export function validateDeadline(value: string): void {
  const match = DEADLINE_PATTERN.exec(value)
  if (match === null) throw invalid(`deadline ${value} is not YYYY-MM-DD`, { field: 'deadline' })
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const date = new Date(Date.UTC(year, month - 1, day))
  const roundTrip =
    date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day
  if (!roundTrip) throw invalid(`deadline ${value} is not a real date`, { field: 'deadline' })
}

const WEEK_PATTERN = /^(\d{4})-W(\d{2})$/

/** `YYYY-Www`, Monday start; week 53 only in long ISO years. */
export function validateWeekPlan(value: string): void {
  const match = WEEK_PATTERN.exec(value)
  if (match === null) throw invalid(`week_plan ${value} is not YYYY-Www`, { field: 'weekPlan' })
  const year = Number(match[1])
  const week = Number(match[2])
  if (week < 1 || week > isoWeeksInYear(year)) {
    throw invalid(`week_plan ${value} is not a real ISO week`, { field: 'weekPlan' })
  }
}

export function isoWeeksInYear(year: number): number {
  const jan1 = new Date(Date.UTC(year, 0, 1)).getUTCDay()
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  return jan1 === 4 || (jan1 === 3 && leap) ? 53 : 52
}

/** Current ISO week for the injected clock (My Week default). */
export function currentIsoWeek(now: Date): string {
  const date = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()))
  const day = date.getUTCDay() || 7
  date.setUTCDate(date.getUTCDate() + 4 - day)
  const isoYear = date.getUTCFullYear()
  const yearStart = new Date(Date.UTC(isoYear, 0, 1))
  const week = Math.ceil(((date.getTime() - yearStart.getTime()) / 86_400_000 + 1) / 7)
  return `${isoYear}-W${String(week).padStart(2, '0')}`
}

/** Trims, rejects empty/oversized members, dedupes in order and caps the group. */
export function sanitizeLabels(labels: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of labels) {
    const label = raw.trim()
    if (label === '') throw invalid('label is empty after trim', { field: 'labels' })
    if (runeLength(label) > LIMITS.labelChars) {
      throw invalid(`label ${label} exceeds ${LIMITS.labelChars} characters`, { field: 'labels' })
    }
    if (seen.has(label)) continue
    seen.add(label)
    out.push(label)
  }
  if (out.length > LIMITS.labelsPerTask) {
    throw invalid(`more than ${LIMITS.labelsPerTask} labels`, { field: 'labels' })
  }
  return out
}

export function validateLabel(label: string): string {
  const trimmed = label.trim()
  if (trimmed === '') throw invalid('label is empty', { field: 'label' })
  if (runeLength(trimmed) > LIMITS.labelChars) {
    throw invalid(`label ${trimmed} exceeds ${LIMITS.labelChars} characters`, { field: 'label' })
  }
  return trimmed
}

/** `tasks.labels` JSON; corrupt JSON degrades to the empty group. */
export function decodeLabels(raw: string): string[] {
  if (raw.trim() === '') return []
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((entry): entry is string => typeof entry === 'string')
  } catch {
    return []
  }
}

export function validateChecklistText(text: string): string {
  const trimmed = text.trim()
  if (trimmed === '') throw invalid('checklist text is empty', { field: 'text' })
  if (runeLength(trimmed) > LIMITS.checklistChars) {
    throw invalid(`checklist text exceeds ${LIMITS.checklistChars} characters`, { field: 'text' })
  }
  return trimmed
}

export const COLUMN_NAME_MAX = 40

/** Column names are the primary key: trimmed, non-empty, ≤40 (MCP contract). */
export function validateColumnName(name: string): string {
  const trimmed = name.trim()
  if (trimmed === '') throw invalid('column name is empty', { field: 'name' })
  if (runeLength(trimmed) > COLUMN_NAME_MAX) {
    throw invalid(`column name exceeds ${COLUMN_NAME_MAX} characters`, { field: 'name' })
  }
  return trimmed
}

/**
 * Plan §4.7: move anchors are validated inside the target column **and** the
 * mover's parent domain. The message names both, so a client that sent an
 * anchor from another column/parent gets an actionable error (defect 10).
 */
export function anchorError(anchorId: string, column: string, parentId: string | null): AppError {
  return invalid(
    `move anchor ${anchorId} is not in column "${column}" of the same parent domain ` +
      `(parent: ${parentId ?? 'root'})`,
    { anchorId, column, parentId, reason: 'anchor-outside-parent-domain' }
  )
}