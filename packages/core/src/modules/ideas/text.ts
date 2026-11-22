/**
 * Text, hashing, cursor and SQL-LIKE helpers shared by the Ideas service.
 * Hashing is: Unicode NFC plus collapsed whitespace, SHA-256
 * lowercase hex.
 */
import { createHash } from 'node:crypto'

import { IDEA_LIMITS, runeCount } from './limits'
import { invalidInput, rejectionReasonRequired } from './errors'

export interface ListCursor {
  sort: string
  key: string
  id: string
}

const CURSOR_VERSION = 1

/** NFC-normalizes and collapses every whitespace run to one space. */
export function normalizeContentText(value: string): string {
  return value.normalize('NFC').split(/\s+/u).filter((part) => part !== '').join(' ')
}

/** SHA-256 of the normalized content, lowercase hex. */
export function hashContent(value: string): string {
  return createHash('sha256').update(normalizeContentText(value), 'utf8').digest('hex')
}

export function encodeCursor(cursor: ListCursor): string {
  return Buffer.from(JSON.stringify({ v: CURSOR_VERSION, ...cursor }), 'utf8').toString('base64url')
}

export function decodeCursor(raw: string | undefined): ListCursor | undefined {
  if (raw === undefined || raw === '') return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(Buffer.from(raw, 'base64url').toString('utf8'))
  } catch {
    throw invalidInput('invalid cursor')
  }
  if (typeof parsed !== 'object' || parsed === null) throw invalidInput('invalid cursor')
  const record = parsed as Record<string, unknown>
  if (record.v !== CURSOR_VERSION) throw invalidInput('invalid cursor')
  if (typeof record.sort !== 'string' || typeof record.key !== 'string' || typeof record.id !== 'string') {
    throw invalidInput('invalid cursor')
  }
  return { sort: record.sort, key: record.key, id: record.id }
}

/** Escapes `%`, `_` and `\` for a LIKE pattern used with `ESCAPE '\'`. */
export function escapeLike(value: string): string {
  return value.replace(/[\\%_]/g, (match) => `\\${match}`)
}

/** Cuts text at a paragraph boundary within `budget` code points. */
export function truncateParagraphs(text: string, budget: number): { text: string; truncated: boolean } {
  const runes = [...text]
  if (budget <= 0) return { text: '', truncated: runes.length > 0 }
  if (runeCount(text) <= budget) return { text, truncated: false }

  const paragraphs = text.split('\n\n')
  let out = ''
  let used = 0
  for (const paragraph of paragraphs) {
    const add = runeCount(paragraph) + (out === '' ? 0 : 2)
    if (used + add > budget) break
    out = out === '' ? paragraph : `${out}\n\n${paragraph}`
    used += add
  }
  if (out === '') return { text: runes.slice(0, budget).join(''), truncated: true }
  return { text: out, truncated: true }
}

/** Display title of a run: the experiment title plus the run-id prefix. */
export function runTitle(experimentTitle: string, runId: string): string {
  const short = runId.slice(0, 8)
  if (experimentTitle.trim() === '') return `Run ${short}`
  return `${experimentTitle} · Run ${short}`
}

/** Trims a title and enforces the 1..200 code-point budget. */
export function normalizeTitle(raw: string): string {
  const title = raw.trim()
  if (title === '') throw invalidInput('title must not be empty')
  if (runeCount(title) > IDEA_LIMITS.titleRunes) {
    throw invalidInput(`title must be at most ${IDEA_LIMITS.titleRunes} characters`)
  }
  return title
}

export function normalizeDescription(raw: string): string {
  if (runeCount(raw) > IDEA_LIMITS.descriptionRunes) {
    throw invalidInput(`description must be at most ${IDEA_LIMITS.descriptionRunes} characters`)
  }
  return raw
}

export function normalizeRejectionReason(raw: string): string {
  const reason = raw.trim()
  if (reason === '') throw rejectionReasonRequired()
  if (runeCount(reason) > IDEA_LIMITS.rejectionReasonRunes) {
    throw invalidInput(`rejection reason must be at most ${IDEA_LIMITS.rejectionReasonRunes} characters`)
  }
  return reason
}

export function normalizeEvidenceContent(raw: string): string {
  const content = raw.trim()
  if (content === '') throw invalidInput('evidence content must not be empty')
  if (runeCount(content) > IDEA_LIMITS.evidenceContentRunes) {
    throw invalidInput(`evidence content must be at most ${IDEA_LIMITS.evidenceContentRunes} characters`)
  }
  return content
}