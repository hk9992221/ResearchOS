import type { Rect } from '@researchos/shared'

import type { PdfBlock, PdfDocument, PdfParseOptions, PdfParser } from './types'

/** The desktop package owns pdfjs-dist; core resolves it through this specifier. */
const PDFJS_SPECIFIER = 'pdfjs-dist/legacy/build/pdf.mjs'

/** A PDF that parses but carries no text layer (scan-only); indexed as `no_text`. */
export class PdfNoTextLayerError extends Error {
  constructor(message = 'PDF has no text layer') {
    super(message)
    this.name = 'PdfNoTextLayerError'
  }
}

/** The PDF could not be opened or decoded. */
export class PdfUnreadableError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options)
    this.name = 'PdfUnreadableError'
  }
}

/** pdfjs-dist could not be resolved (not installed/linked for core). */
export class PdfParserUnavailableError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options)
    this.name = 'PdfParserUnavailableError'
  }
}

interface PdfjsTextItem {
  str?: string
  transform?: number[]
  width?: number
  height?: number
}

interface PdfjsPage {
  getViewport(params: { scale: number }): { width: number; height: number }
  getTextContent(): Promise<{ items: PdfjsTextItem[] }>
}

interface PdfjsDocument {
  numPages: number
  getPage(pageNumber: number): Promise<PdfjsPage>
  destroy(): Promise<void>
}

export interface PdfjsModule {
  getDocument(params: {
    data: Uint8Array
    isEvalSupported?: boolean
    disableFontFace?: boolean
    useSystemFonts?: boolean
  }): { promise: Promise<PdfjsDocument>; destroy(): Promise<void> }
}

interface GlyphLine {
  y: number
  size: number
  text: string
  x0: number
  x1: number
}

/**
 * Default PDF parser: `pdfjs-dist/legacy/build/pdf.mjs`, loaded lazily because
 * the desktop package owns the dependency. When resolution fails the parser is
 * unavailable and PDF full text is left with an explicit issue; Markdown
 * sources keep indexing.
 */
export class PdfJsParser implements PdfParser {
  private readonly loader: () => Promise<PdfjsModule>
  private module: Promise<PdfjsModule> | undefined

  constructor(options: { loadModule?: () => Promise<PdfjsModule> } = {}) {
    this.loader =
      options.loadModule ??
      (() => import(/* @vite-ignore */ PDFJS_SPECIFIER) as Promise<PdfjsModule>)
  }

  private load(): Promise<PdfjsModule> {
    if (this.module === undefined) {
      this.module = this.loader().catch((cause: unknown) => {
        throw new PdfParserUnavailableError(
          'pdfjs-dist is not resolvable from @researchos/core; the main process must inject a PDF parser',
          { cause }
        )
      })
    }
    return this.module
  }

  async parse(buffer: Uint8Array, options: PdfParseOptions = {}): Promise<PdfDocument> {
    const pdfjs = await this.load()
    let document: PdfjsDocument
    const loadingTask = pdfjs.getDocument({
        data: new Uint8Array(buffer.buffer, buffer.byteOffset, buffer.byteLength),
        isEvalSupported: false,
        disableFontFace: true,
        useSystemFonts: false
      })
    try {
      document = await loadingTask.promise
    } catch (cause) {
      await loadingTask.destroy().catch(() => undefined)
      throw new PdfUnreadableError('PDF cannot be opened', { cause })
    }

    try {
      const pageCount = document.numPages
      if (pageCount <= 0) throw new PdfNoTextLayerError('PDF has no readable pages')
      const start = Math.max(1, options.startPage ?? 1)
      if (start > pageCount) return { pageCount, blocks: [] }
      const end = options.maxPage !== undefined && options.maxPage > 0
        ? Math.min(pageCount, start + options.maxPage - 1)
        : pageCount
      const blocks: PdfBlock[] = []
      let nonEmpty = false
      for (let pageNo = start; pageNo <= end; pageNo += 1) {
        const pageBlocks = await parsePage(document, pageNo)
        if (pageBlocks.length > 0) nonEmpty = true
        blocks.push(...pageBlocks)
      }
      if (!nonEmpty) throw new PdfNoTextLayerError('PDF has no text layer (possibly a scan)')
      return { pageCount, blocks }
    } finally {
      await loadingTask.destroy().catch(() => undefined)
    }
  }
}

/** One decoded glyph with its PDF user-space position and font size. */
interface PageGlyph {
  x: number
  y: number
  size: number
  text: string
  width: number
}

/**
 * Extracts one page's text blocks. Lines are reconstructed from glyph
 * positions (baseline tolerance 2), paragraphs break on a vertical gap larger
 * than 1.9 line heights; no block crosses a page boundary.
 */
async function parsePage(document: PdfjsDocument, pageNo: number): Promise<PdfBlock[]> {
  let page: PdfjsPage
  try {
    page = await document.getPage(pageNo)
    const viewport = page.getViewport({ scale: 1 })
    const content = await page.getTextContent()
    const glyphs: PageGlyph[] = []
    for (const item of content.items) {
      if (typeof item.str !== 'string' || item.str === '' || item.transform === undefined) continue
      const transform = item.transform
      glyphs.push({
        x: transform[4] ?? 0,
        y: transform[5] ?? 0,
        size: Math.abs(transform[3] ?? 0) || item.height || 12,
        text: item.str,
        width: item.width ?? 0
      })
    }
    if (glyphs.length === 0) return []

    glyphs.sort((left, right) => {
      if (Math.abs(left.y - right.y) > 2) return right.y - left.y
      return left.x - right.x
    })

    const lines: GlyphLine[] = []
    let current: PageGlyph[] = []
    const flush = (): void => {
      if (current.length === 0) return
      current.sort((left, right) => left.x - right.x)
      let text = ''
      let previousEnd = 0
      for (let index = 0; index < current.length; index += 1) {
        const glyph = current[index]
        if (glyph === undefined) continue
        if (index > 0 && glyph.x - previousEnd > Math.max(glyph.size, 4) * 0.28) {
          if (!text.endsWith(' ')) text += ' '
        }
        text += glyph.text
        previousEnd = glyph.x + glyph.size * 0.5
      }
      const trimmed = text.trim()
      const firstGlyph = current[0]
      if (trimmed !== '' && firstGlyph !== undefined) {
        lines.push({
          y: firstGlyph.y,
          size: firstGlyph.size,
          text: trimmed,
          x0: firstGlyph.x,
          x1: Math.max(...current.map((glyph) => glyph.x + glyph.width))
        })
      }
      current = []
    }
    for (const glyph of glyphs) {
      const firstGlyph = current[0]
      if (firstGlyph !== undefined && Math.abs(glyph.y - firstGlyph.y) > 2) flush()
      current.push(glyph)
    }
    flush()
    if (lines.length === 0) return []

    const blocks: PdfBlock[] = []
    let paragraph: GlyphLine[] = []
    let offset = 0
    const emit = (): void => {
      if (paragraph.length === 0) return
      const linesInBlock = paragraph
      paragraph = []
      const text = linesInBlock.map((line) => line.text).join('\n').trim()
      if (text === '') return
      const runes = [...text]
      const left = Math.min(...linesInBlock.map((line) => line.x0))
      const right = Math.max(...linesInBlock.map((line) => line.x1))
      const top = Math.max(...linesInBlock.map((line) => line.y + line.size))
      const bottom = Math.min(...linesInBlock.map((line) => line.y))
      const rect: Rect = {
        x: clamp01(left / viewport.width),
        y: clamp01(1 - top / viewport.height),
        width: clamp01((right - left) / viewport.width),
        height: clamp01((top - bottom) / viewport.height)
      }
      blocks.push({
        pageNo,
        blockNo: blocks.length,
        text,
        charStart: offset,
        charEnd: offset + runes.length,
        rects: [rect]
      })
      offset += runes.length + 1
    }

    for (const line of lines) {
      if (paragraph.length > 0) {
        const lastLine = paragraph[paragraph.length - 1]
        const firstLine = paragraph[0]
        const gap = (lastLine?.y ?? line.y) - line.y
        const threshold = Math.max(firstLine?.size ?? 1, 1) * 1.9
        if (gap > threshold) emit()
      }
      paragraph.push(line)
    }
    emit()
    return blocks
  } catch (cause) {
    if (cause instanceof PdfNoTextLayerError) throw cause
    throw new PdfUnreadableError(`PDF page ${pageNo} could not be parsed`, { cause })
  }
}

function clamp01(value: number): number {
  return Math.min(1, Math.max(0, value))
}
