/**
 * pdf.js canvas + text layer for one page. The component owns document loading,
 * fit-width/fit-page scaling and selection geometry; it renders highlight
 * rects normalized to the page box so zooming never moves a saved highlight.
 * The PDF URL is the imported copy's file URL.
 */

import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react'
import {
  GlobalWorkerOptions,
  TextLayer,
  getDocument,
  type PDFDocumentProxy,
  type PDFPageProxy
} from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

import type { PaperAnnotation, Rect } from '@researchos/shared'

import styles from '../paper.module.css'

GlobalWorkerOptions.workerSrc = workerUrl

export interface PdfSelection {
  readonly pageNo: number
  readonly rects: Rect[]
  readonly quote: string
}

export type PdfFit = 'none' | 'width' | 'page'

export interface PdfCanvasProps {
  readonly url: string
  readonly pageNo: number
  readonly zoom: number
  readonly fit: PdfFit
  readonly annotations: readonly PaperAnnotation[]
  readonly focusAnnotationId: string | null
  readonly onPageCount: (count: number) => void
  readonly onScale: (scale: number) => void
  readonly onSelection: (selection: PdfSelection | null) => void
  readonly onError: (message: string) => void
  readonly onRendering: (rendering: boolean) => void
}

function clamp01(value: number): number {
  return Math.min(1, Math.max(0, value))
}

function isCancelled(error: unknown): boolean {
  if (typeof error !== 'object' || error === null || !('name' in error)) return false
  return error.name === 'RenderingCancelledException' || error.name === 'AbortException'
}

export default function PdfCanvas({
  url,
  pageNo,
  zoom,
  fit,
  annotations,
  focusAnnotationId,
  onPageCount,
  onScale,
  onSelection,
  onError,
  onRendering
}: PdfCanvasProps): JSX.Element {
  const containerRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const textLayerRef = useRef<HTMLDivElement>(null)
  const pageFrameRef = useRef<HTMLDivElement>(null)
  const versionRef = useRef(0)
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [page, setPage] = useState<PDFPageProxy | null>(null)
  const [container, setContainer] = useState({ width: 0, height: 0 })
  const [display, setDisplay] = useState({ width: 0, height: 0 })
  const [scale, setScale] = useState(1)

  useEffect(() => {
    let cancelled = false
    setDoc(null)
    setPage(null)
    const task = getDocument({
      url,
      // pdf.js 6 ships its JBIG2/OpenJPEG decoders and CMaps as separate static
      // files (served/copied by the pdfjsAssets Vite plugin). Both URLs must end
      // with "/" because pdf.js appends the file name verbatim.
      wasmUrl: new URL('pdfjs/wasm/', document.baseURI).href,
      cMapUrl: new URL('pdfjs/cmaps/', document.baseURI).href,
      cMapPacked: true
    })
    task.promise
      .then((loaded) => {
        if (cancelled) return
        setDoc(loaded)
        onPageCount(loaded.numPages)
      })
      .catch((cause: unknown) => {
        if (!cancelled) onError(cause instanceof Error ? cause.message : String(cause))
      })
    return () => {
      cancelled = true
      void task.destroy()
    }
  }, [url, onPageCount, onError])

  useEffect(() => {
    const element = containerRef.current
    if (element === null) return
    const measure = (): void => {
      setContainer({
        width: element.clientWidth || 800,
        height: element.clientHeight || 600
      })
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (doc === null) return
    let cancelled = false
    onRendering(true)
    doc
      .getPage(pageNo)
      .then((loaded) => {
        if (!cancelled) setPage(loaded)
      })
      .catch((cause: unknown) => {
        if (!cancelled && !isCancelled(cause)) {
          onError(cause instanceof Error ? cause.message : String(cause))
        }
      })
    return () => {
      cancelled = true
    }
  }, [doc, pageNo, onError, onRendering])

  useEffect(() => {
    if (page === null) return
    const version = ++versionRef.current
    let cancelled = false
    const base = page.getViewport({ scale: 1 })
    let scale = zoom
    if (fit === 'width' && base.width > 0) {
      scale = Math.max(0.25, (container.width - 48) / base.width)
    } else if (fit === 'page' && base.width > 0 && base.height > 0) {
      scale = Math.max(
        0.25,
        Math.min((container.width - 48) / base.width, (container.height - 48) / base.height)
      )
    }
    scale = Math.min(4, scale)
    setScale(scale)
    onScale(scale)
    const viewport = page.getViewport({ scale })
    const canvas = canvasRef.current
    const textLayer = textLayerRef.current
    if (canvas === null || textLayer === null) {
      return () => {
        versionRef.current += 1
      }
    }
    const ratio = window.devicePixelRatio || 1
    canvas.width = Math.floor(viewport.width * ratio)
    canvas.height = Math.floor(viewport.height * ratio)
    canvas.style.width = `${Math.floor(viewport.width)}px`
    canvas.style.height = `${Math.floor(viewport.height)}px`
    setDisplay({ width: Math.floor(viewport.width), height: Math.floor(viewport.height) })
    const context = canvas.getContext('2d')
    if (context === null) {
      onError('canvas context unavailable')
      return () => {
        versionRef.current += 1
      }
    }
    onRendering(true)
    const renderTask = page.render({
      canvasContext: context,
      canvas,
      viewport,
      transform: ratio === 1 ? undefined : [ratio, 0, 0, ratio, 0, 0]
    })
    renderTask.promise
      .then(async () => {
        if (cancelled || version !== versionRef.current) return
        textLayer.replaceChildren()
        const layer = new TextLayer({
          textContentSource: page.streamTextContent(),
          container: textLayer,
          viewport
        })
        await layer.render()
      })
      .catch((cause: unknown) => {
        if (!isCancelled(cause) && !cancelled) {
          onError(cause instanceof Error ? cause.message : String(cause))
        }
      })
      .finally(() => {
        if (!cancelled) onRendering(false)
      })
    return () => {
      cancelled = true
      versionRef.current += 1
      renderTask.cancel()
    }
  }, [page, zoom, fit, container.width, container.height, onError, onRendering, onScale])

  useEffect(() => {
    if (focusAnnotationId === null) return
    const frame = pageFrameRef.current
    if (frame === null) return
    const element = frame.querySelector<HTMLElement>(
      `[data-annotation-id="${CSS.escape(focusAnnotationId)}"]`
    )
    if (element === null) return
    element.scrollIntoView?.({ block: 'center' })
    const flash = styles.highlightFlash ?? 'highlight-flash'
    element.classList.add(flash)
    const timer = window.setTimeout(() => {
      element.classList.remove(flash)
    }, 1200)
    return () => window.clearTimeout(timer)
  }, [focusAnnotationId, annotations, pageNo, display.width])

  const captureSelection = useCallback((): void => {
    const frame = pageFrameRef.current
    if (frame === null) return
    const selection = window.getSelection()
    if (selection === null || selection.rangeCount === 0) {
      onSelection(null)
      return
    }
    const quote = selection.toString().replace(/\s+/gu, ' ').trim()
    if (quote === '') {
      onSelection(null)
      return
    }
    const range = selection.getRangeAt(0)
    const pageRect = frame.getBoundingClientRect()
    const width = pageRect.width > 0 ? pageRect.width : display.width
    const height = pageRect.height > 0 ? pageRect.height : display.height
    if (width <= 0 || height <= 0) return
    const rects: Rect[] = []
    for (const rect of Array.from(range.getClientRects())) {
      if (rect.width < 1 || rect.height < 1) continue
      rects.push({
        x: clamp01((rect.left - pageRect.left) / width),
        y: clamp01((rect.top - pageRect.top) / height),
        width: clamp01(rect.width / width),
        height: clamp01(rect.height / height)
      })
    }
    if (rects.length === 0) {
      onSelection(null)
      return
    }
    onSelection({ pageNo, rects, quote: quote.slice(0, 20_000) })
  }, [display.height, display.width, onSelection, pageNo])

  const pageStyle: CSSProperties | undefined =
    display.width === 0
      ? undefined
      : ({
          width: display.width,
          height: display.height,
          '--scale-factor': String(scale),
          '--total-scale-factor': String(scale),
          '--scale-round-x': '1px',
          '--scale-round-y': '1px',
          '--user-unit': '1'
        } as CSSProperties)

  return (
    <div ref={containerRef} className={styles.canvasArea} onMouseUp={captureSelection}>
      {page !== null && (
        <div ref={pageFrameRef} className={styles.pageFrame} style={pageStyle}>
          <canvas ref={canvasRef} className={styles.pageCanvas} />
          <div className={styles.highlightLayer} aria-hidden="true">
            {annotations.map((annotation) => (
              <div key={annotation.id}>
                {annotation.rects.map((rect, index) => (
                  <span
                    key={`${annotation.id}-${index}`}
                    data-annotation-id={annotation.id}
                    className={styles.highlight}
                    style={{
                      left: `${rect.x * 100}%`,
                      top: `${rect.y * 100}%`,
                      width: `${rect.width * 100}%`,
                      height: `${rect.height * 100}%`,
                      background: annotation.color ?? 'rgba(255, 214, 0, 0.55)'
                    }}
                  />
                ))}
              </div>
            ))}
          </div>
          <div ref={textLayerRef} className={styles.textLayer} />
        </div>
      )}
    </div>
  )
}