import type { Locator, Snippet } from './types'

export const MAX_QUERY_RUNES = 512

export interface NormalizedQuery {
  raw: string
  /** Retrieval text: NFKC, width-folded, lowercased, quotes removed. */
  text: string
  /** Quoted substrings; all must occur in a chunk (case-folded must-conditions). */
  phrases: string[]
  /** Highlight tokens: at least two runes, deduplicated, lowercased. */
  terms: string[]
  lang: string
}

const OPEN_QUOTES = new Set(['"', "'", '“', '‘', '「'])

function closesPhrase(opener: string, rune: string): boolean {
  if (opener === '“') return rune === '”'
  if (opener === '‘') return rune === '’'
  if (opener === '「') return rune === '」'
  return rune === opener
}

/** Removes quoted fragments and returns them as must-conditions. */
export function splitPhrases(input: string): { text: string; phrases: string[] } {
  const runes = [...input]
  const phrases: string[] = []
  let text = ''
  let opener: string | undefined
  let start = 0
  for (let index = 0; index < runes.length; index += 1) {
    const rune = runes[index] ?? ''
    if (opener === undefined) {
      if (OPEN_QUOTES.has(rune)) {
        opener = rune
        start = index + 1
        text += ' '
        continue
      }
      text += rune
      continue
    }
    if (closesPhrase(opener, rune)) {
      const phrase = runes.slice(start, index).join('').trim()
      if (phrase !== '') phrases.push(phrase)
      opener = undefined
      text += ' '
    }
  }
  if (opener !== undefined) text += runes.slice(start).join('')
  return { text, phrases }
}

/** Classifies text by script share; 20% CJK already defeats word analysis. */
export function detectLang(text: string): string {
  let cjk = 0
  let latin = 0
  for (const rune of text) {
    if (/[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u.test(rune)) cjk += 1
    else if (/\p{L}/u.test(rune)) latin += 1
  }
  if (cjk === 0 && latin === 0) return ''
  if (cjk > 0 && latin === 0) return 'zh'
  return cjk * 4 >= latin ? 'zh' : 'en'
}

/** Highlight tokens of the normalized query. */
export function highlightTerms(text: string, phrases: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const candidate of [...phrases, ...text.split(/[^\p{L}\p{N}]+/u)]) {
    const value = candidate.trim()
    if ([...value].length < 2 || seen.has(value)) continue
    seen.add(value)
    out.push(value)
  }
  return out
}

/** Normalizes a query; throws `RangeError` beyond the rune cap. */
export function normalizeQuery(raw: string): NormalizedQuery {
  const trimmed = raw.trim()
  if (trimmed === '') return { raw: '', text: '', phrases: [], terms: [], lang: '' }
  const runeCount = [...trimmed].length
  if (runeCount > MAX_QUERY_RUNES) {
    throw new RangeError(`search query is ${runeCount} runes (limit ${MAX_QUERY_RUNES})`)
  }
  const folded = trimmed.normalize('NFKC').toLowerCase()
  const split = splitPhrases(folded)
  const text = split.text.split(/\s+/u).filter((part) => part !== '').join(' ')
  const lang = detectLang(`${text} ${split.phrases.join(' ')}`)
  return { raw: trimmed, text, phrases: split.phrases, terms: highlightTerms(text, split.phrases), lang }
}

/** Text sent to keyword/vector recall; phrases only when no plain text is left. */
export function retrievalText(query: NormalizedQuery): string {
  return query.text !== '' ? query.text : query.phrases.join(' ')
}

export function containsPhrases(foldedText: string, phrases: readonly string[]): boolean {
  return phrases.every((phrase) => foldedText.includes(phrase))
}

function indexOfRunes(haystack: readonly string[], needle: readonly string[]): number {
  if (needle.length === 0 || needle.length > haystack.length) return -1
  outer: for (let index = 0; index + needle.length <= haystack.length; index += 1) {
    for (let offset = 0; offset < needle.length; offset += 1) {
      if (haystack[index + offset] !== needle[offset]) continue outer
    }
    return index
  }
  return -1
}

function mergeRanges(ranges: Array<{ start: number; end: number }>): Array<{ start: number; end: number }> {
  if (ranges.length === 0) return []
  const first = ranges[0]
  if (first === undefined) return []
  const out = [first]
  for (const range of ranges.slice(1)) {
    const last = out[out.length - 1]
    if (last === undefined) break
    if (range.start <= last.end) {
      if (range.end > last.end) last.end = range.end
      continue
    }
    out.push(range)
  }
  return out
}

/**
 * Builds a snippet of at most `maxRunes` runes around the first term match and
 * the matching highlight ranges.
 */
export function buildSnippet(text: string, terms: readonly string[], maxRunes: number, locator: Locator): Snippet {
  const runes = [...text]
  if (runes.length === 0) return { text: '', highlights: [], locator }
  const folded = runes.map((rune) => rune.toLowerCase())
  let first = -1
  for (const term of terms) {
    const needle = [...term.toLowerCase()]
    if (needle.length === 0) continue
    const index = indexOfRunes(folded, needle)
    if (index >= 0 && (first < 0 || index < first)) first = index
  }
  let start = first > maxRunes / 4 ? first - Math.floor(maxRunes / 4) : 0
  const end = Math.min(start + maxRunes, runes.length)
  if (end - start < maxRunes && start > 0) start = Math.max(0, end - maxRunes)
  const window = runes.slice(start, end)
  const windowFolded = folded.slice(start, end)
  const highlights: Array<{ start: number; end: number }> = []
  for (const term of terms) {
    const needle = [...term.toLowerCase()]
    if (needle.length === 0) continue
    for (let offset = 0; offset + needle.length <= windowFolded.length; ) {
      const index = indexOfRunes(windowFolded.slice(offset), needle)
      if (index < 0) break
      const begin = offset + index
      highlights.push({ start: begin, end: begin + needle.length })
      offset = begin + needle.length
    }
  }
  highlights.sort((left, right) => (left.start !== right.start ? left.start - right.start : left.end - right.end))
  return { text: window.join(''), highlights: mergeRanges(highlights), locator }
}
