import { createHash } from 'node:crypto'

import type { Chunk, PdfBlock } from './types'

const MARKDOWN_MAX_TOKENS = 500
const MARKDOWN_OVERLAP_TOKENS = 50
const PDF_MAX_TOKENS = 500
/** Rune window of an oversized paragraph/PDF block; ~1800 runes of mostly Latin. */
const SPLIT_WINDOW = 1800
const SPLIT_OVERLAP = 180

interface MarkdownSegment {
  start: number
  end: number
  text: string
  heading: string
  block: number
}

export function hashText(text: string): string {
  return createHash('sha256').update(text, 'utf8').digest('hex')
}

export function chunkId(docId: string, ordinal: number): string {
  return `${docId}#${ordinal}`
}

export function isCjk(codePoint: string): boolean {
  return /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u.test(codePoint)
}

/**
 * Approximate token count: one per CJK rune, one per four other non-space
 * runes. It only sizes chunks; retrieval quality never depends on a tokenizer.
 */
export function estimateTokens(text: string): number {
  let cjk = 0
  let other = 0
  for (const rune of text) {
    if (isCjk(rune)) cjk += 1
    else if (!/\s/u.test(rune)) other += 1
  }
  return cjk + Math.ceil(other / 4)
}

/** Effective heading path of one segment, joined with " > ". */
function headingPath(headings: readonly string[]): string {
  return headings.filter((value) => value !== '').join(' > ')
}

function isHeadingLine(trimmed: string): boolean {
  return parseHeadingLevel(trimmed).level > 0
}

function parseHeadingLevel(trimmed: string): { level: number; rest: string } {
  let level = 0
  while (level < trimmed.length && trimmed[level] === '#') level += 1
  if (level === 0 || level > 6) return { level: 0, rest: trimmed }
  const rest = trimmed.slice(level)
  if (rest !== '' && rest[0] !== ' ' && rest[0] !== '\t') return { level: 0, rest: trimmed }
  return { level, rest }
}

/**
 * Splits Markdown into heading/paragraph segments with rune offsets and the
 * effective heading path. Fenced code blocks stay one segment.
 */
export function splitMarkdownSegments(runes: readonly string[]): MarkdownSegment[] {
  const segments: MarkdownSegment[] = []
  const headings = ['', '', '', '', '', '']
  let block = 0
  let lineStart = 0
  let inFence = false
  let paragraphStart = -1

  const flushParagraph = (end: number): void => {
    if (paragraphStart < 0) return
    const text = runes.slice(paragraphStart, end).join('').trim()
    if (text !== '') {
      segments.push({ start: paragraphStart, end, text, heading: headingPath(headings), block })
      block += 1
    }
    paragraphStart = -1
  }

  for (;;) {
    let lineEnd = lineStart
    while (lineEnd < runes.length && runes[lineEnd] !== '\n') lineEnd += 1
    const line = runes.slice(lineStart, lineEnd).join('')
    const trimmed = line.trim()
    const fence = trimmed.startsWith('```') || trimmed.startsWith('~~~')
    if (inFence) {
      if (fence) {
        inFence = false
        flushParagraph(lineEnd)
      }
    } else if (fence) {
      flushParagraph(lineStart)
      inFence = true
      paragraphStart = lineStart
    } else if (trimmed === '') {
      flushParagraph(lineStart)
    } else if (isHeadingLine(trimmed)) {
      flushParagraph(lineStart)
      const { level, rest } = parseHeadingLevel(trimmed)
      headings[level - 1] = rest.trim()
      for (let index = level; index < headings.length; index += 1) headings[index] = ''
      segments.push({
        start: lineStart,
        end: lineEnd,
        text: trimmed,
        heading: headingPath(headings),
        block
      })
      block += 1
    } else if (paragraphStart < 0) {
      paragraphStart = lineStart
    }
    if (lineEnd >= runes.length) break
    lineStart = lineEnd + 1
  }
  flushParagraph(runes.length)
  if (inFence && paragraphStart >= 0) flushParagraph(runes.length)
  return segments
}

/**
 * Chunks Markdown on heading/paragraph boundaries: about 500-token chunks with
 * about 50 tokens of overlap, offsets in the original document.
 */
export function chunkMarkdown(docId: string, markdown: string): Chunk[] {
  const runes = [...markdown]
  const segments = splitMarkdownSegments(runes)
  if (segments.length === 0) return []
  const chunks: Chunk[] = []
  const add = (start: number, end: number, segment: MarkdownSegment): void => {
    chunks.push({
      chunkId: chunkId(docId, chunks.length),
      docId,
      ordinal: chunks.length,
      text: runes.slice(start, end).join('').trim(),
      contentHash: hashText(runes.slice(start, end).join('')),
      locator: { blockNo: segment.block, charStart: start, charEnd: end, heading: segment.heading || undefined }
    })
  }

  for (let index = 0; index < segments.length; ) {
    const segment = segments[index]
    if (segment === undefined) break
    if (estimateTokens(segment.text) > MARKDOWN_MAX_TOKENS) {
      for (let start = segment.start; start < segment.end; ) {
        const end = Math.min(start + SPLIT_WINDOW, segment.end)
        chunks.push({
          chunkId: chunkId(docId, chunks.length),
          docId,
          ordinal: chunks.length,
          text: runes.slice(start, end).join('').trim(),
          contentHash: hashText(runes.slice(start, end).join('')),
          locator: { blockNo: segment.block, charStart: start, charEnd: end, heading: segment.heading || undefined }
        })
        if (end === segment.end) break
        start = end - SPLIT_OVERLAP
      }
      index += 1
      continue
    }
    let next = index
    let tokens = 0
    while (next < segments.length) {
      // A heading starts a new section: never merge across it, so the heading
      // path on a chunk stays exact.
      const nextSegment = segments[next]
      if (nextSegment === undefined) break
      if (next > index && nextSegment.text.trim().startsWith('#')) break
      const segmentTokens = estimateTokens(nextSegment.text)
      if (next > index && tokens + segmentTokens > MARKDOWN_MAX_TOKENS) break
      tokens += segmentTokens
      next += 1
      if (tokens >= MARKDOWN_MAX_TOKENS) break
    }
    const lastSegment = segments[next - 1]
    if (lastSegment === undefined) break
    add(segment.start, lastSegment.end, segment)

    let advance = next
    const advanceSegment = segments[advance]
    if (advanceSegment !== undefined && !advanceSegment.text.trim().startsWith('#')) {
      let overlap = 0
      for (let back = next - 1; back > index; back -= 1) {
        overlap += estimateTokens(segments[back]?.text ?? '')
        if (overlap >= MARKDOWN_OVERLAP_TOKENS) {
          advance = back
          break
        }
      }
    }
    index = advance <= index ? index + 1 : advance
  }
  return chunks
}

/**
 * Turns parsed PDF blocks into chunks. Blocks never span pages; a block longer
 * than the token cap is split inside its page, keeping page-local offsets.
 */
export function chunkPdfBlocks(docId: string, blocks: readonly PdfBlock[]): Chunk[] {
  const chunks: Chunk[] = []
  for (const block of blocks) {
    const runes = [...block.text]
    if (runes.length === 0) continue
    if (estimateTokens(block.text) <= PDF_MAX_TOKENS) {
      chunks.push(pdfChunk(docId, chunks.length, block.text, block, 0, runes.length))
      continue
    }
    for (let start = 0; start < runes.length; ) {
      const end = Math.min(start + SPLIT_WINDOW, runes.length)
      chunks.push(pdfChunk(docId, chunks.length, runes.slice(start, end).join(''), block, start, end))
      if (end === runes.length) break
      start = end - SPLIT_OVERLAP
    }
  }
  return chunks
}

function pdfChunk(
  docId: string,
  ordinal: number,
  text: string,
  block: PdfBlock,
  start: number,
  end: number
): Chunk {
  return {
    chunkId: chunkId(docId, ordinal),
    docId,
    ordinal,
    text: text.trim(),
    contentHash: hashText(text),
    locator: {
      pageNo: block.pageNo,
      blockNo: block.blockNo,
      charStart: block.charStart + start,
      charEnd: block.charStart + end,
      ...(block.rects === undefined ? {} : { rects: block.rects })
    }
  }
}

/** Lowercases rune by rune, preserving rune count and offsets. */
export function foldRunes(text: string): string {
  return [...text].map((rune) => rune.toLowerCase()).join('')
}

export function truncateRunes(text: string, length: number): string {
  const runes = [...text.trim()]
  return runes.length <= length ? runes.join('') : `${runes.slice(0, length).join('')}…`
}
