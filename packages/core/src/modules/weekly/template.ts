import type {
  ActivityDigest,
  DigestItem,
  PlannedOrigin,
  SectionKey,
  TemplateSection,
  WeeklyPeriod,
  WeeklyTemplate
} from './types'

/** The fixed default-7 structure. Code, never a setting. */
export const DEFAULT_TEMPLATE: WeeklyTemplate = {
  id: 'default-7',
  version: 1,
  sections: [
    { key: 'work', title: '本周工作内容', num: 1 },
    { key: 'literature', title: '文献调研进展', num: 2 },
    { key: 'theory', title: '理论研究进展', num: 3 },
    { key: 'experiment', title: '实验工作与结果', num: 4 },
    { key: 'idea', title: 'Idea 进展', num: 5 },
    { key: 'problem', title: '遇到的问题', num: 6 },
    { key: 'next_week', title: '下周计划', num: 7 }
  ]
}

export const REPORT_TEMPLATE_ID = DEFAULT_TEMPLATE.id
export const REPORT_TEMPLATE_VERSION = DEFAULT_TEMPLATE.version

const BY_KEY: ReadonlyMap<SectionKey, TemplateSection> = new Map(
  DEFAULT_TEMPLATE.sections.map((section) => [section.key, section])
)

export function templateSection(key: SectionKey): TemplateSection | undefined {
  return BY_KEY.get(key)
}

export function sectionTitle(key: SectionKey): string {
  return BY_KEY.get(key)?.title ?? key
}

export function isSectionKey(value: string): value is SectionKey {
  return BY_KEY.has(value as SectionKey)
}

/** Inclusive display end date of the report window. */
export function periodEndDate(period: WeeklyPeriod): string {
  return addDays(period.endExclusiveDate, -1)
}

export function addDays(date: string, days: number): string {
  const [year, month, day] = date.split('-').map(Number)
  const base = Date.UTC(year ?? 1970, (month ?? 1) - 1, day ?? 1)
  const shifted = new Date(base + days * 86_400_000)
  return shifted.toISOString().slice(0, 10)
}

export function sectionItems(digest: ActivityDigest, key: SectionKey): DigestItem[] {
  return digest.sections.find((section) => section.key === key)?.items ?? []
}

/**
 * Renders the deterministic seven-section report. Entries come only from
 * digest facts and planned candidates; a missing digest yields an empty
 * skeleton, never invented content.
 */
export function buildSkeleton(
  projectName: string,
  period: WeeklyPeriod,
  digest?: ActivityDigest
): string {
  const lead = `# Weekly Research Report — ${projectName}（${period.startDate} – ${periodEndDate(period)}）\n\n`
  return lead + assembleSections((key) => skeletonLines(key, digest))
}

/** The seven fixed headings with a body per section. */
export function assembleReport(
  projectName: string,
  period: WeeklyPeriod,
  bodies: ReadonlyMap<SectionKey, string[]>
): string {
  return (
    `# Weekly Research Report — ${projectName}（${period.startDate} – ${periodEndDate(period)}）\n\n` +
    assembleSections((key) => {
      const lines = bodies.get(key)
      return lines === undefined || lines.length === 0 ? ['- 本周期无记录'] : lines
    })
  )
}

function assembleSections(body: (key: SectionKey) => string[]): string {
  const parts: string[] = []
  const sections = DEFAULT_TEMPLATE.sections
  sections.forEach((section, index) => {
    const lines = body(section.key)
    const text = lines.length === 0 ? ['- 本周期无记录'] : lines
    let block = `## ${section.num}. ${section.title}\n`
    for (const line of text) block += `${line}\n`
    if (index !== sections.length - 1) block += '\n'
    parts.push(block)
  })
  return parts.join('')
}

export function skeletonLines(key: SectionKey, digest?: ActivityDigest): string[] {
  if (digest === undefined) return []
  switch (key) {
    case 'problem': {
      const lines: string[] = []
      for (const task of digest.blocked) {
        lines.push(
          `- 受阻任务：${task.title}（列：${task.columnName}；原因未记录）[[ref:task:${task.ref.id}]]`
        )
      }
      for (const item of sectionItems(digest, 'problem')) lines.push(renderDigestItem(item, digest.timeZone))
      return lines
    }
    case 'next_week':
      return digest.planned.map(
        (task) =>
          `- ${task.title}（来源：${originLabel(task.origin)}）[[ref:task:${task.ref.id}]]`
      )
    default:
      return sectionItems(digest, key).map((item) => renderDigestItem(item, digest.timeZone))
  }
}

export function renderDigestItem(item: DigestItem, timeZone: string): string {
  const summary = item.summary.trim() === '' ? '活动记录' : item.summary.trim()
  const when = formatLocalMinute(item.firstAt, timeZone)
  let line =
    item.detail !== '' && item.detail !== summary
      ? `- ${summary}（${when}；${item.detail}）`
      : `- ${summary}（${when}）`
  if (item.ref.type !== '' && item.ref.id !== '') line += `[[ref:${item.ref.type}:${item.ref.id}]]`
  return line
}

/** `MM-DD HH:mm` in a timezone, for skeleton entries. */
export function formatLocalMinute(instant: string, timeZone: string): string {
  const date = new Date(instant)
  if (Number.isNaN(date.getTime())) return ''
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  }).formatToParts(date)
  const get = (type: string): string => parts.find((part) => part.type === type)?.value ?? ''
  const hour = get('hour') === '24' ? '00' : get('hour')
  return `${get('month')}-${get('day')} ${hour}:${get('minute')}`
}

export function originLabel(origin: PlannedOrigin): string {
  switch (origin) {
    case 'week_plan':
      return '周计划'
    case 'deadline':
      return '截止日'
    case 'carry_over':
      return '本周计划顺延'
    default:
      return origin
  }
}

/**
 * Recognizes `## 1. 本周工作内容`, `## 1、本周工作内容`, `## 本周工作内容`
 * and the full-width bracket variants a hand edit may produce.
 */
export function normalizeHeading(line: string): SectionKey | undefined {
  let text = line.trim()
  if (!text.startsWith('##') || text.startsWith('###')) return undefined
  text = text.slice(2).trim()
  text = text.replace(/^[0-9]+/, '').trim()
  text = text.replace(/^[.、,，:：)）\]】]+/, '').trim()
  text = text.replaceAll('（', '(').replaceAll('）', ')')
  for (const section of DEFAULT_TEMPLATE.sections) {
    if (text === section.title) return section.key
  }
  return undefined
}

export interface ParsedReport {
  order: SectionKey[]
  sections: Map<SectionKey, string[]>
  unknown: string[]
}

/** Splits a report body into the recognized sections, in document order. */
export function parseReport(content: string): ParsedReport {
  const parsed: ParsedReport = { order: [], sections: new Map(), unknown: [] }
  let current: SectionKey | undefined
  for (const line of content.split('\n')) {
    const trimmed = line.trim()
    if (trimmed.startsWith('##') && !trimmed.startsWith('###')) {
      const key = normalizeHeading(trimmed)
      if (key === undefined) {
        parsed.unknown.push(trimmed.replace(/^##+/, '').trim())
        current = undefined
        continue
      }
      if (!parsed.order.includes(key)) parsed.order.push(key)
      current = key
      if (!parsed.sections.has(key)) parsed.sections.set(key, [])
      continue
    }
    if (current !== undefined) {
      const lines = parsed.sections.get(current)
      if (lines !== undefined) lines.push(line)
    }
  }
  return parsed
}

/** Splits content into lines without the empty element a final newline adds. */
export function splitLines(content: string): string[] {
  const lines = content.split('\n')
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

/**
 * Finds the line range of one section's body: after the heading up to the next
 * `##` heading, or the end of the document.
 */
export function sectionRegion(
  lines: string[],
  key: SectionKey
): { start: number; end: number } | undefined {
  let start = -1
  for (let index = 0; index < lines.length; index += 1) {
    const trimmed = (lines[index] ?? '').trim()
    if (!trimmed.startsWith('##') || trimmed.startsWith('###')) continue
    const heading = normalizeHeading(trimmed)
    if (heading === key && start < 0) {
      start = index
      continue
    }
    if (start >= 0) return { start, end: index }
  }
  return start < 0 ? undefined : { start, end: lines.length }
}

export function sectionBody(content: string, key: SectionKey): string | undefined {
  const lines = splitLines(content)
  const region = sectionRegion(lines, key)
  if (region === undefined) return undefined
  return lines.slice(region.start + 1, region.end).join('\n').trim()
}

/**
 * Swaps one section body and leaves every other byte alone, keeping the blank
 * lines that closed the old body so the layout does not shift.
 */
export function replaceSectionBody(
  content: string,
  key: SectionKey,
  bodyLines: string[]
): string | undefined {
  const doc = splitLines(content)
  const region = sectionRegion(doc, key)
  if (region === undefined) return undefined
  let trailing = 0
  for (let index = region.end - 1; index > region.start; index -= 1) {
    if ((doc[index] ?? '').trim() !== '') break
    trailing += 1
  }
  const out = [
    ...doc.slice(0, region.start + 1),
    ...bodyLines,
    ...Array.from({ length: trailing }, () => ''),
    ...doc.slice(region.end)
  ]
  let joined = out.join('\n')
  if (content.endsWith('\n')) joined += '\n'
  return joined
}

/** The excerpt shown in report lists: first real content line, ≤120 runes. */
export function excerptOf(content: string): string {
  for (const line of content.split('\n')) {
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#')) continue
    return truncateRunes(trimmed, 120)
  }
  return ''
}

/** Caps a string at `limit` runes, appending an ellipsis. */
export function truncateRunes(value: string, limit: number): string {
  const runes = Array.from(value)
  if (runes.length <= limit) return value
  return `${runes.slice(0, limit).join('')}…`
}
