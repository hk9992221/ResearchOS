/**
 * Fuzzy matching for the command palette.
 *
 * A command matches when its title or keywords contain the query as a
 * subsequence. A contiguous run and a word start score higher, so typing
 * "sett" ranks "Settings" above a command that merely contains s…e…t…t.
 * Matching is case-insensitive and diacritic-free: the catalogs are ASCII or
 * CJK, where case folding is a no-op.
 */

export interface FuzzyResult {
  /** Higher is better; only compared among matches of the same query. */
  readonly score: number
  /** Index of the first matched character, used as the tie-breaker. */
  readonly first: number
}

const WORD_BOUNDARY = /[\s·/:_-]/

export function fuzzyMatch(query: string, text: string): FuzzyResult | null {
  const needle = query.trim().toLowerCase()
  if (needle === '') return { score: 0, first: 0 }
  const haystack = text.toLowerCase()

  const direct = haystack.indexOf(needle)
  if (direct >= 0) {
    const atWordStart = direct === 0 || WORD_BOUNDARY.test(haystack[direct - 1] ?? '')
    return { score: 1000 - direct + (atWordStart ? 200 : 0), first: direct }
  }

  let score = 0
  let first = -1
  let cursor = 0
  let previous = -2
  for (const char of needle) {
    const found = haystack.indexOf(char, cursor)
    if (found < 0) return null
    if (first < 0) first = found
    const atWordStart = found === 0 || WORD_BOUNDARY.test(haystack[found - 1] ?? '')
    score += 1 + (found === previous + 1 ? 2 : 0) + (atWordStart ? 2 : 0)
    previous = found
    cursor = found + 1
  }
  return { score, first }
}

/**
 * rankCommands filters and orders candidates. `keywords` are extra searchable
 * text (the Go shell keeps translated keywords per command for this).
 */
export function rankByQuery<T>(
  query: string,
  items: readonly T[],
  fields: (item: T) => readonly string[]
): T[] {
  if (query.trim() === '') return [...items]
  const scored: Array<{ item: T; score: number; first: number; order: number }> = []
  items.forEach((item, order) => {
    let best: FuzzyResult | null = null
    for (const field of fields(item)) {
      const result = fuzzyMatch(query, field)
      if (result === null) continue
      if (best === null || result.score > best.score) best = result
    }
    if (best !== null) scored.push({ item, score: best.score, first: best.first, order })
  })
  scored.sort((a, b) => b.score - a.score || a.first - b.first || a.order - b.order)
  return scored.map((entry) => entry.item)
}