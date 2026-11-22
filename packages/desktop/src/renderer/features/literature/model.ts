/**
 * Pure literature helpers: the display projection of a paper row, hash/size
 * formatting, `ros://` reference parsing and the destination mapping shared by
 * the overview, the paper page and the PDF viewer.
 */

import type { McpJob, Paper, PaperAnnotation } from '@researchos/shared'

import { routeHref, type RouteKind } from '../../routes'

export interface PaperDisplay {
  readonly title: string
  readonly authors: string
  readonly year: number | null
  readonly venue: string
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function creatorName(value: unknown): string {
  if (typeof value !== 'object' || value === null) return ''
  const creator = value as Record<string, unknown>
  const name = text(creator['name'])
  if (name !== '') return name
  const last = text(creator['lastName'])
  const first = text(creator['firstName'])
  if (last === '' && first === '') return ''
  if (last === '') return first
  if (first === '') return last
  return `${last} ${first}`
}

export function authorsOf(creators: unknown): string {
  if (!Array.isArray(creators)) return ''
  return creators
    .map(creatorName)
    .filter((name) => name !== '')
    .join(', ')
}

export function yearOfDate(value: string): number | null {
  const match = /(\d{4})/u.exec(value)
  if (match === null) return null
  const year = Number.parseInt(match[1] ?? '', 10)
  return Number.isFinite(year) && year > 0 ? year : null
}

/**
 * paperDisplay projects a paper row to the table/citation values. `PaperView`
 * carries them pre-computed; a list row only carries `metadata`, so both paths
 * read the same keys and a raw Zotero field still renders.
 */
export function paperDisplay(paper: Paper | (Paper & Record<string, unknown>)): PaperDisplay {
  const meta = paper.metadata ?? {}
  const view = paper as Paper & { title?: unknown; authors?: unknown; year?: unknown }
  const title = text(view.title) !== '' ? text(view.title) : text(meta['title'])
  const authors = text(view.authors) !== '' ? text(view.authors) : authorsOf(meta['creators'])
  const viewYear = view.year
  const year =
    typeof viewYear === 'number' && Number.isFinite(viewYear)
      ? viewYear
      : yearOfDate(text(meta['date']))
  const venue = text(meta['publicationTitle']) || text(meta['conferenceName'])
  return {
    title: title === '' ? '' : title,
    authors,
    year,
    venue
  }
}

/** shortHash renders a stable prefix for a diff row; empty hashes stay honest. */
export function shortHash(hash: string | null | undefined, length = 10): string {
  if (hash === null || hash === undefined || hash === '') return ''
  return hash.slice(0, length)
}

export function formatBytes(size: number | null | undefined): string {
  if (size === null || size === undefined || !Number.isFinite(size)) return ''
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

export function formatSavedAt(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (part: number): string => String(part).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

/** fileUrl turns an absolute workspace path into something pdf.js can fetch. */
/** Workspace-relative path as a fetchable ros-asset:// URL (sandbox-safe). */
export function rosAssetUrl(path: string): string {
  const normalized = path.replace(/\\/gu, '/')
  let relative = normalized
  const marker = ['/library/', '/projects/']
    .map((prefix) => normalized.indexOf(prefix))
    .filter((index) => index >= 0)
    .sort((left, right) => left - right)[0]
  if (marker !== undefined) relative = normalized.slice(marker + 1)
  else relative = normalized.replace(/^\/+/, '')
  return `ros-asset://workspace/${relative
    .split('/')
    .filter((segment) => segment !== '')
    .map((segment) => encodeURIComponent(segment))
    .join('/')}`
}

export function fileUrl(path: string): string {
  if (/^[a-z]+:\/\//iu.test(path)) return path
  const normalized = path.replace(/\\/gu, '/')
  if (normalized.startsWith('/')) return `file://${encodeURI(normalized)}`
  return `file:///${encodeURI(normalized)}`
}

/* --------------------------------------------------------- ros:// links */

export interface RosRef {
  readonly type: string
  readonly id: string
  readonly page: number | null
  readonly heading: string | null
}

const ROS_REF = /^ros:\/\/([a-z_]+)\/([^#?\s]+)(?:\?[^#]*)?(?:#(.*))?$/iu

export function parseRosRef(href: string): RosRef | null {
  const match = ROS_REF.exec(href.trim())
  if (match === null) return null
  const [, rawType, rawId, fragment] = match
  if (rawType === undefined || rawId === undefined || rawId === '') return null
  const type = rawType.toLowerCase()
  let page: number | null = null
  let heading: string | null = null
  if (fragment !== undefined && fragment !== '') {
    const pageMatch = /(?:^|&)page=(\d+)/u.exec(fragment)
    if (pageMatch !== null) {
      const parsed = Number.parseInt(pageMatch[1] ?? '', 10)
      if (Number.isFinite(parsed) && parsed > 0) page = parsed
    }
    const headingMatch = /(?:^|&)heading=([^&]+)/u.exec(fragment)
    if (headingMatch !== null) heading = decodeURIComponent(headingMatch[1] ?? '')
  }
  return { type, id: decodeURIComponent(rawId), page, heading }
}

const KIND_BY_REF: Readonly<Record<string, RouteKind | undefined>> = {
  paper: 'paper',
  note: 'note',
  idea: 'idea',
  experiment: 'experiment',
  run: 'run',
  task: 'task',
  weekly_report: 'weekly_report'
}

/**
 * rosDestination maps a reference to its route. A paper reference with a page
 * hint opens the PDF viewer at that page, so a excerpt link is one click back
 * into the highlighted text.
 */
export function rosDestination(href: string, projectId: string): string | undefined {
  const ref = parseRosRef(href)
  if (ref === null) return undefined
  const kind = KIND_BY_REF[ref.type]
  if (kind === undefined) return undefined
  if (ref.type === 'paper' && ref.page !== null) {
    const pdf = routeHref('pdf', { projectId, entityId: ref.id })
    return pdf === undefined ? undefined : `${pdf}?page=${ref.page}`
  }
  const hrefPath = routeHref(kind, { projectId, entityId: ref.id })
  if (hrefPath === undefined) return undefined
  if (ref.heading !== null && ref.heading !== '') {
    return `${hrefPath}?heading=${encodeURIComponent(ref.heading)}`
  }
  return hrefPath
}

/** rosHrefs lists the distinct references a Markdown body carries. */
export function rosHrefs(markdown: string): string[] {
  const found = new Set<string>()
  const pattern = /ros:\/\/[^\s)\]}>,]+/gu
  for (const match of markdown.matchAll(pattern)) {
    const href = match[0]
    if (href !== undefined) found.add(href)
  }
  return [...found]
}

/* --------------------------------------------------------------- jobs */

const TERMINAL_JOB_STATES: ReadonlySet<string> = new Set(['succeeded', 'failed', 'cancelled'])

/**
 * waitForJob polls `jobs.get` until the job settles. Import has its own report
 * stream; this covers the metadata refresh, which has no report channel.
 */
export async function waitForJob(
  jobId: string,
  get: (jobId: string) => Promise<McpJob>,
  options: { intervalMs?: number; timeoutMs?: number } = {}
): Promise<McpJob> {
  const interval = options.intervalMs ?? 400
  const deadline = Date.now() + (options.timeoutMs ?? 30_000)
  for (;;) {
    const job = await get(jobId)
    if (TERMINAL_JOB_STATES.has(job.state)) return job
    if (Date.now() > deadline) return job
    await new Promise((resolve) => setTimeout(resolve, interval))
  }
}

/* --------------------------------------------------------- annotations */

export function annotationsForPage(
  annotations: readonly PaperAnnotation[],
  pageNo: number
): PaperAnnotation[] {
  return annotations.filter((annotation) => annotation.pageNo === pageNo)
}

export function annotationColor(annotation: PaperAnnotation): string {
  return annotation.color ?? 'var(--accent-subtle)'
}