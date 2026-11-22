/**
 * Home formatting helpers: the relative time, the entity icon and the route
 * of one activity row. They mirror internal/ui/shell/home.go so the dashboard
 * reads like the rest of the product.
 */

import type { ActivityRouteKind, ProjectActivityItem } from '@researchos/shared'

import type { IconName } from '../../app/icons'
import { routeHref, type RouteKind } from '../../routes'

const ACTIVITY_ICON: Readonly<Record<string, IconName>> = {
  project: 'projects',
  paper: 'literature',
  note: 'note',
  idea: 'ideas',
  evidence: 'ideas',
  experiment: 'experiments',
  run: 'experiments',
  snapshot: 'experiments',
  task: 'kanban',
  weekly_report: 'weeklyReview',
  research_database: 'note',
  db_field: 'note',
  db_record: 'note',
  entity_relation: 'link'
}

export function activityIcon(entityType: string): IconName {
  return ACTIVITY_ICON[entityType] ?? 'fileText'
}

const ACTIVITY_ROUTE: Readonly<Record<ActivityRouteKind, RouteKind>> = {
  project: 'overview',
  note: 'note',
  idea: 'idea',
  experiment: 'experiment',
  run: 'run',
  task: 'task',
  paper: 'paper',
  weekly_report: 'weekly_report',
  trash: 'trash'
}

/** activityHref maps an event to its page; undefined for a gone target. */
export function activityHref(item: ProjectActivityItem): string | undefined {
  if (item.entityGone) return undefined
  const kind = ACTIVITY_ROUTE[item.route.kind]
  if (kind === undefined) return undefined
  const base = routeHref(kind, {
    projectId: item.route.projectId,
    entityId: item.route.entityId
  })
  if (base === undefined) return undefined
  const params = item.route.params
  if (params === undefined) return base
  const search = new URLSearchParams(params)
  const query = search.toString()
  return query === '' ? base : `${base}?${query}`
}

export interface RelativeTime {
  /** `shell.time.*` key, or an absolute date. */
  readonly key: string
  readonly vars?: { readonly n: number }
}

/** relativeTime follows Go: minutes, hours, days, then the absolute date. */
export function relativeTime(iso: string, now: number = Date.now()): RelativeTime {
  const at = Date.parse(iso)
  if (Number.isNaN(at)) return { key: iso }
  const diff = Math.max(0, now - at)
  const minute = 60_000
  const hour = 60 * minute
  const day = 24 * hour
  if (diff < minute) return { key: 'shell.time.just_now' }
  if (diff < hour) return { key: 'shell.time.minutes', vars: { n: Math.floor(diff / minute) } }
  if (diff < day) return { key: 'shell.time.hours', vars: { n: Math.floor(diff / hour) } }
  if (diff < 7 * day) return { key: 'shell.time.days', vars: { n: Math.floor(diff / day) } }
  return { key: iso.slice(0, 10) }
}