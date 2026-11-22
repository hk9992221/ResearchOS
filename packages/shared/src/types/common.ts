export type EntityId = string
export type IsoDateTime = string
export type JsonObject = Record<string, unknown>

export interface PageQuery {
  limit?: number
  offset?: number
}

export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface JobRef {
  jobId: string
}

export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

/** Chunk/annotation locator used by search hits and evidence provenance. */
export interface Locator {
  pageNo?: number
  blockNo?: number
  charStart?: number
  charEnd?: number
  heading?: string
  fieldId?: string
  rects?: Rect[]
}

/** Half-open ISO-week period `[start, end)`. */
export interface Period {
  start: string
  end: string
}

export interface TargetRef {
  kind: 'experiment' | 'run'
  id: EntityId
}

export type SortDirection = 'asc' | 'desc'

export interface SortKey {
  field: string
  direction: SortDirection
}

export type FilterOperator =
  | 'eq'
  | 'neq'
  | 'contains'
  | 'startsWith'
  | 'gt'
  | 'gte'
  | 'lt'
  | 'lte'
  | 'isEmpty'
  | 'isNotEmpty'

export interface FilterLeaf {
  kind: 'leaf'
  fieldId: string
  operator: FilterOperator
  value?: unknown
}

export interface FilterGroup {
  kind: 'group'
  op: 'and' | 'or'
  children: FilterNode[]
}

/** Depth ≤ 3, ≤ 30 leaves. */
export type FilterNode = FilterLeaf | FilterGroup

export interface OptimisticUpdate {
  /** ISO timestamp of the row the caller last read. */
  expectedUpdatedAt?: IsoDateTime
}