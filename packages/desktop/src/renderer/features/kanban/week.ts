/**
 * ISO week helpers for the My Week view (Monday start, matching the Go
 * isoWeekNow/isoWeekMonday/shiftISOWeek/isoWeekRange pair).
 */

const WEEK_PATTERN = /^(\d{4})-W(\d{2})$/

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

/** isoWeek formats a local date as YYYY-Www. */
export function isoWeek(date: Date): string {
  const thursday = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const day = thursday.getDay() === 0 ? 7 : thursday.getDay()
  thursday.setDate(thursday.getDate() + 4 - day)
  const yearStart = new Date(thursday.getFullYear(), 0, 1)
  const week = Math.ceil(((thursday.getTime() - yearStart.getTime()) / 86_400_000 + 1) / 7)
  return `${thursday.getFullYear()}-W${pad(week)}`
}

export function isoWeekNow(): string {
  return isoWeek(new Date())
}

/** mondayOf returns the Monday of the week containing date, local midnight. */
export function mondayOf(date: Date): Date {
  const copy = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const day = copy.getDay() === 0 ? 7 : copy.getDay()
  copy.setDate(copy.getDate() - (day - 1))
  return copy
}

export function parseIsoWeek(week: string): Date | undefined {
  const match = WEEK_PATTERN.exec(week)
  if (match === null) return undefined
  const year = Number(match[1])
  const number = Number(match[2])
  if (number < 1 || number > 53) return undefined
  // ISO week 1 contains January 4.
  const january4 = new Date(year, 0, 4)
  const firstMonday = mondayOf(january4)
  const monday = new Date(firstMonday.getFullYear(), firstMonday.getMonth(), firstMonday.getDate())
  monday.setDate(monday.getDate() + (number - 1) * 7)
  return monday
}

export function shiftIsoWeek(week: string, delta: number): string {
  const monday = parseIsoWeek(week)
  if (monday === undefined) return isoWeekNow()
  monday.setDate(monday.getDate() + delta * 7)
  return isoWeek(monday)
}

/** isoWeekRange is the `11-16 – 11-22` label beside the week number. */
export function isoWeekRange(week: string): string {
  const monday = parseIsoWeek(week)
  if (monday === undefined) return week
  const sunday = new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + 6)
  const left = `${pad(monday.getMonth() + 1)}-${pad(monday.getDate())}`
  const right = `${pad(sunday.getMonth() + 1)}-${pad(sunday.getDate())}`
  return `${left} – ${right}`
}

export function isValidWeekPlan(value: string): boolean {
  return value === '' || WEEK_PATTERN.test(value)
}