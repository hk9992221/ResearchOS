import { readFileSync } from 'node:fs'

import type { Paths } from '../../paths'

/**
 * Minimal body reader for read-only aggregations (provenance, context graph).
 * A note file is Markdown with an optional leading YAML frontmatter block; the
 * body is everything after the closing `---`. A missing or unreadable file
 * yields an empty body: the metadata row still lists the note.
 */
export function splitFrontmatter(raw: string): { frontmatter: string; body: string } {
  const normalized = raw.replace(/^\uFEFF/, '')
  if (!normalized.startsWith('---')) return { frontmatter: '', body: normalized }
  const firstLineEnd = normalized.indexOf('\n')
  if (firstLineEnd < 0) return { frontmatter: '', body: normalized }
  const closing = normalized.indexOf('\n---', firstLineEnd)
  if (closing < 0) return { frontmatter: '', body: normalized }
  const after = normalized.indexOf('\n', closing + 4)
  const bodyStart = after < 0 ? normalized.length : after + 1
  return {
    frontmatter: normalized.slice(firstLineEnd + 1, closing),
    body: normalized.slice(bodyStart)
  }
}

export function readNoteBody(paths: Paths, relativePath: string): string {
  if (relativePath === '') return ''
  try {
    return splitFrontmatter(readFileSync(paths.abs(relativePath), 'utf8')).body
  } catch {
    return ''
  }
}