import type { SearchHitView } from '@researchos/shared'

import { routeHref, type RouteKind } from '../routes'

/**
 * Where a search hit jumps to.
 *
 * The shell navigates with one-shot query parameters that the destination
 * feature reads once on mount: `page` for the PDF viewer, `heading` and
 * `offset` (character offset) for the note editor, `record` for a database
 * record. Keeping them in the URL means a reload or a back/forward step lands
 * on the same place, and no extra IPC read is needed to reproduce the jump.
 */

const ENTITY_ROUTE: Readonly<Record<string, RouteKind>> = {
  note: 'note',
  paper: 'paper',
  idea: 'idea',
  experiment: 'experiment',
  db_record: 'notes'
}

export function searchJumpHref(hit: SearchHitView): string | undefined {
  const projectId = hit.target.projectId
  const entityId = hit.target.entityId
  if (projectId === '' || entityId === '') return undefined

  const isPdf = hit.target.source === 'paper_fulltext' || hit.source === 'paper_fulltext'
  const routeKind: RouteKind | undefined = isPdf
    ? 'pdf'
    : (ENTITY_ROUTE[hit.target.kind] ?? ENTITY_ROUTE[hit.entityType])
  if (routeKind === undefined) return undefined

  const href = routeHref(routeKind, { projectId, entityId })
  if (href === undefined) return undefined

  const params = new URLSearchParams()
  const locator = hit.target.locator ?? hit.locator
  if (isPdf && locator.pageNo !== undefined) params.set('page', String(locator.pageNo))
  if (locator.heading !== undefined && locator.heading !== '') {
    params.set('heading', locator.heading)
  }
  if (locator.charStart !== undefined) params.set('offset', String(locator.charStart))
  if (hit.target.kind === 'db_record') params.set('record', entityId)

  const query = params.toString()
  return query === '' ? href : `${href}?${query}`
}