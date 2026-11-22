import type { EntityId, IsoDateTime, JsonObject } from './types/common'

/**
 * Entity contracts from docs/schema-catalog.md §C and 
 * Canonical 14 entity types; DTOs are camelCase projections of the SQL rows.
 */

export const ENTITY_TYPES = [
  'project',
  'paper',
  'note',
  'research_database',
  'db_field',
  'db_record',
  'idea',
  'evidence',
  'experiment',
  'run',
  'snapshot',
  'task',
  'weekly_report',
  'entity_relation'
] as const

export type EntityType = (typeof ENTITY_TYPES)[number]

const ENTITY_TYPE_LOOKUP: Readonly<Record<string, true>> = Object.fromEntries(
  ENTITY_TYPES.map((type) => [type, true])
)

export function isEntityType(value: string): value is EntityType {
  return ENTITY_TYPE_LOOKUP[value] === true
}

/* ---------------------------------------------------------------- projects */

export type ProjectStatus = 'active' | 'archived'

export interface Project {
  id: EntityId
  name: string
  summary: string
  goals: string
  focusMd: string
  status: ProjectStatus
  archivedAt: IsoDateTime | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
}

/* ------------------------------------------------------------------ papers */

export type PaperFileState = 'ok' | 'missing'
export type PaperSourceState = 'ok' | 'missing' | 'unreadable'

export interface Paper {
  id: EntityId
  zoteroLibraryId: string
  zoteroKey: string
  attachmentKey: string | null
  metadata: JsonObject
  filePath: string | null
  fileHash: string | null
  fileSize: number | null
  fileState: PaperFileState
  sourceState: PaperSourceState
  lastSyncedAt: IsoDateTime | null
  importedAt: IsoDateTime
  updatedAt: IsoDateTime
}

/* ------------------------------------------------------------------- notes */

export const NOTE_KINDS = ['scratchpad', 'paper_note', 'db_record', 'overview'] as const
export type NoteKind = (typeof NOTE_KINDS)[number]
export type NoteFileState = 'ok' | 'missing'

export interface Note {
  id: EntityId
  title: string
  filePath: string
  kind: NoteKind
  description: string
  paperId: EntityId | null
  metaJson: JsonObject | null
  /** Live tag names assigned via entity_tags (always present from notes IPC). */
  tags?: string[]
  fileHash: string | null
  fileMtime: string | null
  fileState: NoteFileState
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* -------------------------------------------------------------------- tags */

export interface Tag {
  id: EntityId
  name: string
  color: string
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* --------------------------------------------------- research databases */

export const DB_FIELD_TYPES = [
  'text',
  'number',
  'select',
  'multi_select',
  'date',
  'checkbox',
  'url',
  'relation',
  'tags'
] as const
export type DbFieldType = (typeof DB_FIELD_TYPES)[number]
export type DbFieldRole = 'title' | 'plain'

export interface ResearchDatabase {
  id: EntityId
  name: string
  description: string
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export interface DbField {
  id: EntityId
  databaseId: EntityId
  name: string
  type: DbFieldType
  config: JsonObject
  sortOrder: number
  role: DbFieldRole
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export interface DbRecord {
  id: EntityId
  databaseId: EntityId
  noteId: EntityId | null
  title: string
  data: JsonObject
  sortOrder: number
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* ------------------------------------------------------------------- ideas */

export const IDEA_STATUSES = [
  'New',
  'Researching',
  'Validating',
  'Supported',
  'PartiallySupported',
  'Inconclusive',
  'Rejected',
  'OnHold'
] as const

export type IdeaStatus = (typeof IDEA_STATUSES)[number]

export interface Idea {
  id: EntityId
  title: string
  description: string
  status: IdeaStatus
  rejectionReason: string | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export type EvidenceDirection = 'support' | 'oppose'
export type EvidenceSourceType = 'experiment' | 'run' | 'note' | 'paper'

export interface Evidence {
  id: EntityId
  ideaId: EntityId
  direction: EvidenceDirection
  content: string
  sourceType: EvidenceSourceType
  sourceId: EntityId | null
  aiGenerated: boolean
  confirmed: boolean
  aiOutputId: EntityId | null
  provenance: JsonObject | null
  contentHash: string | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* ------------------------------------------------------------- experiments */

export interface Experiment {
  id: EntityId
  title: string
  purpose: string
  dirRef: string | null
  scanRules: JsonObject
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export const RUN_STATUSES = ['unknown', 'running', 'finished', 'failed'] as const
export type RunStatus = (typeof RUN_STATUSES)[number]

export interface Run {
  id: EntityId
  experimentId: EntityId
  title: string
  dirRef: string | null
  params: JsonObject
  gitInfo: JsonObject | null
  status: RunStatus
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

export type SnapshotTargetType = 'experiment' | 'run'

export interface Snapshot {
  id: EntityId
  targetType: SnapshotTargetType
  targetId: EntityId
  fileManifest: JsonObject
  gitInfo: JsonObject | null
  metrics: JsonObject
  aiAnalysis: string | null
  aiOutputId: EntityId | null
  modelInfo: JsonObject | null
  createdAt: IsoDateTime
}

/* ------------------------------------------------------------------- tasks */

/** Tasks.priority: NULL is normalized to `none` on read. */
export const TASK_PRIORITIES = ['high', 'medium', 'low', 'none'] as const
export type TaskPriority = (typeof TASK_PRIORITIES)[number]

export interface Task {
  id: EntityId
  title: string
  /** `task_columns.name`; a rename cascades every task row. */
  column: string
  /** Two levels only (service rejects a third, TASK-06). */
  parentTaskId: EntityId | null
  descriptionMd: string
  priority: TaskPriority
  /** Local calendar date `YYYY-MM-DD`, no time zone (TASK-05). */
  deadline: string | null
  blocked: boolean
  labels: string[]
  /** ISO week `YYYY-Www` (Monday start); null = not planned. */
  weekPlan: string | null
  sortOrder: number
  completedAt: IsoDateTime | null
  archivedAt: IsoDateTime | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* ----------------------------------------------------------- weekly reports */

export type WeeklyReportStatus = 'draft' | 'confirmed'

export interface WeeklyReport {
  id: EntityId
  periodStart: string
  periodEnd: string
  contentMd: string
  status: WeeklyReportStatus
  meta: JsonObject | null
  confirmedAt: IsoDateTime | null
  aiOutputId: EntityId | null
  createdAt: IsoDateTime
  updatedAt: IsoDateTime
  deletedAt: IsoDateTime | null
}

/* -------------------------------------------------------------- relations */

export const RELATION_TYPES = [
  'references',
  'derived_from',
  'validates',
  'deliverable_of',
  'related_to'
] as const

export type RelationType = (typeof RELATION_TYPES)[number]

export interface EntityRelation {
  id: EntityId
  srcType: EntityType
  srcId: EntityId
  relation: RelationType
  dstType: EntityType
  dstId: EntityId
  createdAt: IsoDateTime
}