import { createHash } from 'node:crypto'

import { DEFAULT_TEMPLATE, parseReport, sectionTitle, type ParsedReport } from './template'
import type { ActivityDigest, EntityRef, SectionKey, Violation, WeeklyTemplate } from './types'

/** Reference token written by the generator: `[[ref:type:uuid]]`. */
export const REF_PATTERN = /\[\[ref:([a-z_]+):([0-9a-f-]{36})\]\]/g
const NUMBER_PATTERN = /[0-9]+(?:\.[0-9]+)?%?/g
const CLAIM_WORDS = [
  '表明',
  '证明',
  '发现',
  '显示',
  '提升',
  '改进',
  '显著',
  '成功',
  '有效',
  '证实',
  '结果表明'
] as const

export const MAX_VIOLATIONS = 200

/**
 * Deterministic validation of one report body. Pure: the only
 * inputs are the content, the template and the digest, so a stored draft
 * re-validates to the same findings on any machine.
 */
export function validateReport(
  contentMd: string,
  digest: ActivityDigest | undefined,
  template: WeeklyTemplate = DEFAULT_TEMPLATE
): Violation[] {
  const parsed = parseReport(contentMd)
  const out: Violation[] = [...structureViolations(parsed, template)]

  for (const section of template.sections) {
    if (!parsed.order.includes(section.key)) {
      out.push({
        rule: 'V6',
        severity: 'warning',
        fingerprint: fingerprint('V6', section.key, '', 'missing'),
        section: section.key,
        message: `缺少章节「${section.num}. ${section.title}」`
      })
    }
  }

  if (digest !== undefined) {
    out.push(...plannedAsDone(parsed, digest))
    out.push(...unconfirmedClaims(parsed, digest))
    out.push(...unplannedNextWeek(parsed, digest))
    out.push(...unresolvedRefs(parsed, digest))
    out.push(...inventedNumbers(parsed, digest))
  }
  return out
}

function structureViolations(parsed: ParsedReport, template: WeeklyTemplate): Violation[] {
  const out: Violation[] = []
  const expected = new Map(template.sections.map((section, index) => [section.key, index]))
  let last = -1
  for (const key of parsed.order) {
    const index = expected.get(key)
    if (index === undefined) continue
    if (index < last) {
      out.push({
        rule: 'V6',
        severity: 'warning',
        fingerprint: fingerprint('V6', key, '', 'order'),
        section: key,
        message: '章节顺序与默认七节结构不一致'
      })
      break
    }
    last = index
  }
  for (const name of parsed.unknown) {
    out.push({
      rule: 'V6',
      severity: 'warning',
      fingerprint: fingerprint('V6', '', '', name),
      section: '',
      message: `未识别的章节标题「${name}」`
    })
  }
  return out
}

/** V1: a planned task must never be described as work already done. */
function plannedAsDone(parsed: ParsedReport, digest: ActivityDigest): Violation[] {
  const out: Violation[] = []
  for (const task of digest.planned) {
    const title = normalizeForMatch(task.title)
    if (Array.from(title).length < 4) continue
    for (const section of parsed.order) {
      if (section === 'next_week') continue
      for (const line of parsed.sections.get(section) ?? []) {
        if (!normalizeForMatch(line).includes(title)) continue
        out.push({
          rule: 'V1',
          severity: 'error',
          fingerprint: fingerprint('V1', section, task.ref.id, title),
          section,
          ref: task.ref,
          message: `计划任务「${task.title}」出现在「${sectionTitle(section)}」中`
        })
        break
      }
    }
  }
  return out
}

/** V2: every numeric token must exist in the digest facts. */
function inventedNumbers(parsed: ParsedReport, digest: ActivityDigest): Violation[] {
  const allowed = allowedNumbers(digest)
  const out: Violation[] = []
  for (const section of parsed.order) {
    const reported = new Set<string>()
    for (const line of parsed.sections.get(section) ?? []) {
      if (line.trim().startsWith('##')) continue
      // Reference tokens carry UUIDs; identifiers are not measurements.
      const scanned = line.replace(REF_PATTERN, '')
      for (const token of scanned.match(NUMBER_PATTERN) ?? []) {
        const canonical = canonNumber(token)
        if (allowed.has(canonical) || reported.has(canonical) || ignorableNumber(token)) continue
        reported.add(canonical)
        out.push({
          rule: 'V2',
          severity: 'error',
          fingerprint: fingerprint('V2', section, '', token),
          section,
          message: `数值「${token}」在活动汇总中无来源`
        })
      }
    }
  }
  return out
}

function allowedNumbers(digest: ActivityDigest): Set<string> {
  const allowed = new Set<string>()
  const add = (value: string | undefined): void => {
    if (value === undefined) return
    for (const token of value.match(NUMBER_PATTERN) ?? []) allowed.add(canonNumber(token))
  }
  add(digest.period.startDate)
  add(digest.period.endExclusiveDate)
  for (const section of digest.sections) {
    for (const item of section.items) {
      add(item.summary)
      add(item.detail)
      add(item.brief)
      add(item.old)
      add(item.new)
    }
  }
  for (const task of digest.blocked) {
    add(task.title)
    add(task.columnName)
  }
  for (const task of digest.planned) {
    add(task.title)
    add(task.columnName)
    add(task.deadline)
    add(task.weekPlan)
  }
  return allowed
}

/** `08`, `8` and `8%` compare equal; `1.50` and `1.5` do too. */
export function canonNumber(token: string): string {
  let raw = token.endsWith('%') ? token.slice(0, -1) : token
  if (raw.includes('.')) raw = raw.replace(/0+$/, '').replace(/\.$/, '')
  raw = raw.replace(/^0+/, '')
  return raw === '' ? '0' : raw
}

function ignorableNumber(token: string): boolean {
  const raw = token.endsWith('%') ? token.slice(0, -1) : token
  if (raw === token) {
    const value = Number(raw)
    if (Number.isInteger(value) && value >= 1 && value <= 7) return true // section/list numbering
  }
  if (raw.length === 4) {
    const value = Number(raw)
    if (Number.isInteger(value) && value >= 1900 && value <= 2100) return true // years
  }
  return false
}

/** V3: AI conclusions must be marked unconfirmed; claims need a source. */
function unconfirmedClaims(parsed: ParsedReport, digest: ActivityDigest): Violation[] {
  const unconfirmed = new Set<string>()
  for (const section of digest.sections) {
    for (const item of section.items) {
      if (item.unconfirmedSource) unconfirmed.add(`${item.ref.type}:${item.ref.id}`)
    }
  }
  const out: Violation[] = []
  for (const section of ['literature', 'theory', 'experiment', 'idea'] as const) {
    for (const line of parsed.sections.get(section) ?? []) {
      const trimmed = line.trim()
      if (!trimmed.startsWith('-')) continue
      if (trimmed.includes('待确认')) continue
      const refs = collectRefs(trimmed)
      const citesUnconfirmed = refs.some((ref) => unconfirmed.has(`${ref.type}:${ref.id}`))
      if (citesUnconfirmed) {
        out.push({
          rule: 'V3',
          severity: 'error',
          fingerprint: fingerprint('V3', section, '', normalizeForMatch(trimmed)),
          section,
          ref: refs[0],
          message: '引用了未确认的 AI 结论，但未标注「（待确认）」'
        })
        continue
      }
      if (refs.length === 0 && CLAIM_WORDS.some((word) => trimmed.includes(word))) {
        out.push({
          rule: 'V3',
          severity: 'error',
          fingerprint: fingerprint('V3', section, '', normalizeForMatch(trimmed)),
          section,
          message: '成果性表述缺少来源引用，且未标注「（待确认）」'
        })
      }
    }
  }
  return out
}

/** V4: a `[[ref:...]]` outside this week's digest is likely stale. */
function unresolvedRefs(parsed: ParsedReport, digest: ActivityDigest): Violation[] {
  const known = new Set<string>()
  for (const item of digest.sections.flatMap((section) => section.items)) {
    known.add(`${item.ref.type}:${item.ref.id}`)
  }
  for (const task of digest.blocked) known.add(`${task.ref.type}:${task.ref.id}`)
  for (const task of digest.planned) known.add(`${task.ref.type}:${task.ref.id}`)
  // Filing actions stay out of the body, but citing the paper they recorded
  // is still a known reference.
  for (const item of digest.housekeeping) known.add(`${item.ref.type}:${item.ref.id}`)

  const out: Violation[] = []
  for (const section of parsed.order) {
    for (const line of parsed.sections.get(section) ?? []) {
      for (const ref of collectRefs(line)) {
        if (known.has(`${ref.type}:${ref.id}`)) continue
        out.push({
          rule: 'V4',
          severity: 'warning',
          fingerprint: fingerprint('V4', section, ref.id, ref.type),
          section,
          ref,
          message: '引用不在本周期活动汇总中，可能已失效'
        })
      }
    }
  }
  return out
}

/** V5: next-week entries must come from the candidate tasks. */
function unplannedNextWeek(parsed: ParsedReport, digest: ActivityDigest): Violation[] {
  const planned = new Set<string>()
  const titles: string[] = []
  for (const task of digest.planned) {
    planned.add(task.ref.id)
    const title = normalizeForMatch(task.title)
    if (Array.from(title).length >= 4) titles.push(title)
  }
  const out: Violation[] = []
  for (const line of parsed.sections.get('next_week') ?? []) {
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('#') || trimmed.includes('本周期无记录')) continue
    const refs = collectRefs(trimmed)
    if (refs.length === 0) {
      const normalized = normalizeForMatch(trimmed)
      if (titles.some((title) => normalized.includes(title))) continue
      out.push({
        rule: 'V5',
        severity: 'error',
        fingerprint: fingerprint('V5', 'next_week', '', normalized),
        section: 'next_week',
        message: '下周计划条目缺少候选任务引用'
      })
      continue
    }
    if (refs.some((ref) => ref.type === 'task' && planned.has(ref.id))) continue
    out.push({
      rule: 'V5',
      severity: 'error',
      fingerprint: fingerprint('V5', 'next_week', firstTaskRef(refs), normalizeForMatch(trimmed)),
      section: 'next_week',
      ref: refs[0],
      message: '下周计划包含不在候选中的任务'
    })
  }
  return out
}

function firstTaskRef(refs: EntityRef[]): string {
  return refs.find((ref) => ref.type === 'task')?.id ?? ''
}

function collectRefs(line: string): EntityRef[] {
  const out: EntityRef[] = []
  const pattern = new RegExp(REF_PATTERN.source, 'g')
  for (const match of line.matchAll(pattern)) {
    const type = match[1]
    const id = match[2]
    if (type !== undefined && id !== undefined) out.push({ type, id })
  }
  return out
}

export function fingerprint(rule: string, section: SectionKey | '', refId: string, text: string): string {
  const sum = createHash('sha256').update(`${rule}|${section}|${refId}|${text}`).digest('hex')
  return `sha256:${sum}`
}

/** Case- and whitespace-insensitive comparison, with CJK punctuation folded. */
export function normalizeForMatch(value: string): string {
  return value
    .toLowerCase()
    .replace(/\s+/g, '')
    .replaceAll('（', '(')
    .replaceAll('）', ')')
    .replaceAll('，', ',')
    .replaceAll('。', '.')
    .replaceAll('：', ':')
}

/**
 * Snapshot of a validation run. Errors come first and the list is capped at
 * 200 entries, with `truncated` recording that more findings existed.
 */
export function buildCheckState(
  violations: Violation[],
  checkedAt: string
): { checked_at: string; violations: Violation[]; truncated?: boolean } {
  const state: { checked_at: string; violations: Violation[]; truncated?: boolean } = {
    checked_at: checkedAt,
    violations: capViolations(violations)
  }
  if (violations.length > MAX_VIOLATIONS) state.truncated = true
  return state
}

export function capViolations(violations: Violation[]): Violation[] {
  if (violations.length <= MAX_VIOLATIONS) return violations
  const errors = violations.filter((violation) => violation.severity === 'error')
  const warnings = violations.filter((violation) => violation.severity !== 'error')
  return [...errors, ...warnings].slice(0, MAX_VIOLATIONS)
}

/** Re-attaches acks recorded by a previous check run, matching fingerprints. */
export function mergeAcks(violations: Violation[], previous?: { violations: Violation[] }): Violation[] {
  if (previous === undefined) return violations
  const acks = new Map<string, NonNullable<Violation['ack']>>()
  for (const violation of previous.violations) {
    if (violation.ack !== undefined) acks.set(violation.fingerprint, violation.ack)
  }
  return violations.map((violation) => {
    const ack = acks.get(violation.fingerprint)
    return ack === undefined ? violation : { ...violation, ack }
  })
}

export function applyAckFingerprints(
  violations: Violation[],
  fingerprints: readonly string[] | undefined,
  notes: Record<string, string> | undefined,
  at: string
): Violation[] {
  if (fingerprints === undefined || fingerprints.length === 0) return violations
  const wanted = new Set(fingerprints)
  return violations.map((violation) => {
    if (!wanted.has(violation.fingerprint) || violation.ack !== undefined) return violation
    const note = notes?.[violation.fingerprint]
    return {
      ...violation,
      ack: note === undefined ? { actor: 'user', at } : { actor: 'user', at, note }
    }
  })
}
