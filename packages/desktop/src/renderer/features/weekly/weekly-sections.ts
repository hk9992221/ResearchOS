/**
 * Section slicing for the report editor.
 *
 * The stored body is one Markdown document: a title line, then the seven
 * `## N. Title` sections the validator recognizes. The editor
 * writes per section, so this module maps the document to its sections and
 * writes one section's body back, preserving every other byte — the same
 * contract as the Go `weeklySectionAt` / section region helpers.
 */

import type { WeeklySectionKey, WeeklyTemplate } from '@researchos/shared'

export interface WeeklySectionSlice {
  readonly key: WeeklySectionKey
  readonly num: number
  readonly title: string
  /** Body lines joined with `\n`; empty when the section is missing. */
  readonly body: string
  /** False when the document has no heading for this section. */
  readonly present: boolean
}

interface Region {
  /** Line index just after the heading. */
  readonly start: number
  /** Line index of the next heading that ends this section, or lines.length. */
  readonly end: number
}

/** normalizeTitle is the validator's title normalization, without the `##`. */
function normalizeTitle(text: string): string {
  let value = text.trim()
  value = value.replace(/^[0-9]+/, '').trim()
  value = value.replace(/^[.、,，:：)）\]】]+/, '').trim()
  return value.replaceAll('（', '(').replaceAll('）', ')')
}

/** headingKey normalizes a heading the way the validator does. */
export function headingKey(line: string): string {
  const trimmed = line.trim()
  if (!trimmed.startsWith('##') || trimmed.startsWith('###')) return ''
  return normalizeTitle(trimmed.slice(2).trim())
}

function isHeading(line: string): boolean {
  const trimmed = line.trim()
  return trimmed.startsWith('##') && !trimmed.startsWith('###')
}

function sectionRegions(content: string, template: WeeklyTemplate): Map<WeeklySectionKey, Region> {
  const lines = content.split('\n')
  const keys = new Map(
    template.sections.map((section) => [normalizeTitle(section.title), section.key])
  )
  const heads: Array<{ key: WeeklySectionKey; line: number }> = []
  lines.forEach((line, index) => {
    if (!isHeading(line)) return
    const key = keys.get(headingKey(line))
    if (key !== undefined) heads.push({ key, line: index })
  })
  const regions = new Map<WeeklySectionKey, Region>()
  heads.forEach((head, index) => {
    if (regions.has(head.key)) return
    const next = heads[index + 1]
    regions.set(head.key, { start: head.line + 1, end: next?.line ?? lines.length })
  })
  return regions
}

/** splitReport returns every template section in template order. */
export function splitReport(content: string, template: WeeklyTemplate): WeeklySectionSlice[] {
  const lines = content.split('\n')
  const regions = sectionRegions(content, template)
  return template.sections.map((section) => {
    const region = regions.get(section.key)
    if (region === undefined) {
      return { key: section.key, num: section.num, title: section.title, body: '', present: false }
    }
    const body = lines
      .slice(region.start, region.end)
      .join('\n')
      .replace(/^\n+/, '')
      .replace(/\n+$/, '')
    return { key: section.key, num: section.num, title: section.title, body, present: true }
  })
}

/**
 * replaceSectionBody writes one section's body and leaves the rest untouched.
 * A section that is missing from the document is appended with its canonical
 * heading; an empty body keeps the heading so the validator can still see the
 * seven-section structure.
 */
export function replaceSectionBody(
  content: string,
  template: WeeklyTemplate,
  key: WeeklySectionKey,
  body: string
): string {
  const section = template.sections.find((candidate) => candidate.key === key)
  if (section === undefined) return content
  const lines = content.split('\n')
  const region = sectionRegions(content, template).get(key)
  const bodyLines = body.replace(/\n+$/, '').split('\n')
  if (bodyLines.length === 1 && bodyLines[0] === '') bodyLines.length = 0

  if (region === undefined) {
    const base = content.replace(/\n+$/, '')
    const block = `## ${section.num}. ${section.title}\n${bodyLines.join('\n')}`
    return base === '' ? `${block}\n` : `${base}\n\n${block}\n`
  }

  const tail = lines.slice(region.end)
  const head = lines.slice(0, region.start)
  const needsSeparator = tail.length > 0 && bodyLines.length > 0
  const next = [...head, ...bodyLines, ...(needsSeparator ? [''] : []), ...tail]
  return next.join('\n')
}

/** unresolvedCount is how many findings still lack an acknowledgement. */
export function unresolvedCount(
  violations: readonly { readonly fingerprint: string; readonly ack?: unknown }[],
  localAcks: ReadonlySet<string>
): number {
  return violations.filter(
    (violation) => violation.ack === undefined && !localAcks.has(violation.fingerprint)
  ).length
}
