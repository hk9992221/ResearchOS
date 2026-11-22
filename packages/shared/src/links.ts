import { isEntityType, type EntityType } from './entities'
import type { EntityId } from './types/common'

/**
 * Internal deep links: `ros://<entity-type>/<uuid>` with optional
 * `?project=<uuid>` and optional `#page=N` for PDF jump-back (plan §2.4).
 */
export interface RosLink {
  type: EntityType
  id: EntityId
  projectId?: EntityId
  /** 1-based PDF page number. */
  pageNo?: number
}

export type RosLinkError =
  | 'not-a-ros-link'
  | 'unknown-entity-type'
  | 'invalid-id'
  | 'invalid-project-id'
  | 'unexpected-path'
  | 'unexpected-query-parameter'
  | 'unexpected-fragment'
  | 'invalid-page'

export type ParseRosLinkResult =
  | { ok: true; link: RosLink }
  | { ok: false; error: RosLinkError; input: string }

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const PAGE_FRAGMENT_PATTERN = /^#page=(\d+)$/

export function isUuid(value: string): boolean {
  return UUID_PATTERN.test(value)
}

export function parseRosLink(input: string): ParseRosLinkResult {
  const fail = (error: RosLinkError): ParseRosLinkResult => ({ ok: false, error, input })

  if (typeof input !== 'string' || !/^ros:\/\//i.test(input)) return fail('not-a-ros-link')

  let url: URL
  try {
    url = new URL(input)
  } catch {
    return fail('not-a-ros-link')
  }

  if (url.protocol !== 'ros:') return fail('not-a-ros-link')
  if (url.port !== '' || url.username !== '' || url.password !== '') return fail('not-a-ros-link')

  const type = url.hostname
  if (type === '' || !isEntityType(type)) return fail('unknown-entity-type')

  const segments = url.pathname.split('/').filter((segment) => segment !== '')
  if (segments.length !== 1) return fail('unexpected-path')

  const id = segments[0] as string
  if (!isUuid(id)) return fail('invalid-id')

  let pageNo: number | undefined
  if (url.hash !== '') {
    const match = PAGE_FRAGMENT_PATTERN.exec(url.hash)
    if (match === null) return fail('unexpected-fragment')

    const page = Number(match[1])
    if (!Number.isSafeInteger(page) || page < 1) return fail('invalid-page')
    pageNo = page
  }

  const projectValues = url.searchParams.getAll('project')
  for (const key of url.searchParams.keys()) {
    if (key !== 'project') return fail('unexpected-query-parameter')
  }
  if (projectValues.length > 1) return fail('unexpected-query-parameter')

  const projectId = projectValues[0]
  if (projectId !== undefined && !isUuid(projectId)) return fail('invalid-project-id')

  const link: RosLink = { type, id: id.toLowerCase() }
  if (projectId !== undefined) link.projectId = projectId.toLowerCase()
  if (pageNo !== undefined) link.pageNo = pageNo
  return { ok: true, link }
}

export function formatRosLink(link: RosLink): string {
  if (!isEntityType(link.type)) throw new TypeError(`unknown entity type: ${String(link.type)}`)
  if (!isUuid(link.id)) throw new TypeError(`invalid entity id: ${link.id}`)
  if (link.projectId !== undefined && !isUuid(link.projectId)) {
    throw new TypeError(`invalid project id: ${link.projectId}`)
  }
  if (link.pageNo !== undefined && (!Number.isSafeInteger(link.pageNo) || link.pageNo < 1)) {
    throw new TypeError(`invalid page number: ${link.pageNo}`)
  }

  const base = `ros://${link.type}/${link.id.toLowerCase()}`
  const query = link.projectId === undefined ? '' : `?project=${link.projectId.toLowerCase()}`
  const fragment = link.pageNo === undefined ? '' : `#page=${link.pageNo}`
  return `${base}${query}${fragment}`
}

export function tryParseRosLink(input: string): RosLink | null {
  const result = parseRosLink(input)
  return result.ok ? result.link : null
}