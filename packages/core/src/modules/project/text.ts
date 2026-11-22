import type { ProvenanceEntityRef } from '@researchos/shared'

/** Text/metric helpers shared by the read-only aggregations (provenance, context). */

export function collapse(value: string): string {
  return value.split(/\s+/).filter(Boolean).join(' ')
}

/** Cuts to at most `max` runes, marking the cut with one ellipsis. */
export function truncate(value: string, max: number): string {
  const characters = [...value]
  if (characters.length <= max) return value
  if (max <= 1) return '…'
  return `${characters
    .slice(0, max - 1)
    .join('')
    .replace(/[ \t\n]+$/, '')}…`
}

/** Deterministic first-three-metric digest: `key=value · key=value`. */
export function metricDigest(metrics: string): string {
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(metrics) as Record<string, unknown>
  } catch {
    return ''
  }
  const parts: string[] = []
  for (const key of Object.keys(parsed).sort()) {
    if (parts.length === 3) break
    parts.push(`${key}=${formatMetric(parsed[key])}`)
  }
  return parts.join(' · ')
}

export function formatMetric(value: unknown): string {
  if (typeof value === 'number') {
    return Number.isInteger(value) ? String(value) : String(Number(value.toPrecision(4)))
  }
  if (typeof value === 'string') return value
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  try {
    return JSON.stringify(value)
  } catch {
    return ''
  }
}

/** Extracts every `ros://<type>/<uuid>` target from one Markdown body. */
export function rosLinks(body: string): ProvenanceEntityRef[] {
  const out: ProvenanceEntityRef[] = []
  for (const type of ['note', 'paper', 'idea', 'experiment']) {
    const needle = `ros://${type}/`
    let index = 0
    for (;;) {
      const found = body.indexOf(needle, index)
      if (found < 0) break
      const start = found + needle.length
      let end = start
      while (end < body.length && /[0-9a-z-]/.test(body[end]!)) end += 1
      const id = body.slice(start, end)
      if (id.length === 36) out.push({ type, id })
      index = end
    }
  }
  return dedupeRefs(out)
}

export function dedupeRefs(refs: ProvenanceEntityRef[]): ProvenanceEntityRef[] {
  const seen = new Set<string>()
  const out: ProvenanceEntityRef[] = []
  for (const ref of refs) {
    const key = `${ref.type}:${ref.id}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(ref)
  }
  return out
}

/** IDs linked as `ros://<kind>/<id>` in one body. */
export function rosIds(body: string, kind: string): string[] {
  const needle = `ros://${kind}/`
  const out: string[] = []
  let index = 0
  for (;;) {
    const found = body.indexOf(needle, index)
    if (found < 0) break
    const start = found + needle.length
    let end = start
    while (end < body.length && /[0-9a-z-]/.test(body[end]!)) end += 1
    out.push(body.slice(start, end))
    index = end
  }
  return out
}

/** At most `max` runes around a mention, whitespace-collapsed and ellipsized. */
export function snippetAround(text: string, byteOffset: number, max: number): string {
  if (byteOffset < 0 || byteOffset >= text.length) return ''
  const characters = [...text]
  const prefixRunes = [...text.slice(0, byteOffset)].length
  const half = Math.floor(max / 2)
  let start = prefixRunes - half
  if (start < 0) start = 0
  let end = start + max
  if (end > characters.length) {
    end = characters.length
    start = Math.max(0, end - max)
  }
  let out = collapse(characters.slice(start, end).join(''))
  if (start > 0) out = `…${out}`
  if (end < characters.length) out = `${out}…`
  return out
}

/** First ~`max` runes of a body, for a relation-only backlink. */
export function openingText(body: string, max: number): string {
  const collapsed = collapse(body)
  const characters = [...collapsed]
  if (characters.length <= max) return collapsed
  return `${characters
    .slice(0, max - 1)
    .join('')
    .replace(/[ ,.;，。；]+$/, '')}…`
}

export function shortId(id: string): string {
  return id.length <= 8 ? id : id.slice(0, 8)
}

/** First four-digit year in a Zotero free-form date. */
export function yearOf(value: string): string {
  let digits = 0
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index]!
    if (character >= '0' && character <= '9') {
      digits += 1
      if (digits === 4) return value.slice(index - 3, index + 1)
      continue
    }
    digits = 0
  }
  return ''
}