/**
 * Injectable time source and the single storage timestamp format
 * Every TEXT timestamp column stores the output of
 * `formatTimestamp`: UTC, millisecond precision, fixed width, so lexicographic
 * order equals chronological order.
 */

/** The one and only storage timestamp layout. */
export const TIMESTAMP_LAYOUT = 'YYYY-MM-DDTHH:mm:ss.SSSZ'

const TIMESTAMP_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/

export interface Clock {
  now(): Date
}

/** Production clock: wall time, exposed in UTC by the formatter. */
export class SystemClock implements Clock {
  now(): Date {
    return new Date()
  }
}

/** Test clock. `advance` moves it forward and is safe to share. */
export class FixedClock implements Clock {
  private current: Date

  constructor(start: Date | number | string = 0) {
    this.current = start instanceof Date ? new Date(start.getTime()) : new Date(start)
    if (Number.isNaN(this.current.getTime())) throw new TypeError('FixedClock: invalid start time')
  }

  now(): Date {
    return new Date(this.current.getTime())
  }

  advance(milliseconds: number): this {
    this.current = new Date(this.current.getTime() + milliseconds)
    return this
  }

  set(value: Date | number | string): this {
    this.current = value instanceof Date ? new Date(value.getTime()) : new Date(value)
    return this
  }
}

function pad(value: number, width = 2): string {
  return String(value).padStart(width, '0')
}

/** Serializes a time in UTC with millisecond precision. */
export function formatTimestamp(value: Date | number = Date.now()): string {
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.getTime())) throw new RangeError('formatTimestamp: invalid time')
  return (
    `${pad(date.getUTCFullYear(), 4)}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}` +
    `T${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}` +
    `.${pad(date.getUTCMilliseconds(), 3)}Z`
  )
}

/**
 * Strict parser for `formatTimestamp` output. Second or nanosecond precision,
 * missing milliseconds and non-UTC offsets are rejected so hand-edited files
 * cannot smuggle in a different ordering.
 */
export function parseTimestamp(text: string): Date {
  if (!TIMESTAMP_PATTERN.test(text)) {
    throw new RangeError(`parseTimestamp: ${JSON.stringify(text)} is not a UTC millisecond timestamp`)
  }
  const date = new Date(text)
  if (Number.isNaN(date.getTime())) {
    throw new RangeError(`parseTimestamp: ${JSON.stringify(text)} is not a valid date`)
  }
  // Reject calendar overflow such as 2026-02-31.
  if (formatTimestamp(date) !== text) {
    throw new RangeError(`parseTimestamp: ${JSON.stringify(text)} is not a valid date`)
  }
  return date
}

export function isTimestamp(text: string): boolean {
  try {
    parseTimestamp(text)
    return true
  } catch {
    return false
  }
}

/** The UTC calendar day (`YYYY-MM-DD`) used by weekly reports and merge keys. */
export function dayStamp(value: Date | number = Date.now()): string {
  return formatTimestamp(value).slice(0, 10)
}