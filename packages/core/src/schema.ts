/**
 * The single source of truth for the entity, event, actor, relation and enum
 * registries. Every DDL CHECK constraint must agree with these
 * values; `check-schema-parity` enforces it against the applied migrations.
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

export const EVENT_TYPES = [
  'entity.created',
  'entity.deleted',
  'entity.restored',
  'entity.updated',
  'entity.purged',
  'entity.relocated',
  'status.changed',
  'relation.added',
  'relation.removed',
  'note.updated',
  'ai.analysis_completed',
  'paper.imported',
  'paper.overwrite_confirmed',
  'paper.metadata_updated',
  'report.generated',
  'project.exported'
] as const

export type EventType = (typeof EVENT_TYPES)[number]

export const ACTORS = ['user', 'mcp', 'ai'] as const

export type Actor = (typeof ACTORS)[number]

export const RELATIONS = ['references', 'derived_from', 'validates', 'deliverable_of', 'related_to'] as const

export type Relation = (typeof RELATIONS)[number]

export interface RelationRule {
  /** Empty means "any entity type". */
  readonly src: readonly EntityType[]
  readonly dst: readonly EntityType[]
}

/** Direction table of the foundation spec */
export const RELATION_RULES: Readonly<Record<Relation, RelationRule>> = {
  references: { src: ['note'], dst: ['note', 'paper'] },
  derived_from: { src: ['idea'], dst: ['idea'] },
  validates: { src: ['experiment'], dst: ['idea'] },
  deliverable_of: {
    src: ['task'],
    dst: ['note', 'idea', 'experiment', 'run', 'weekly_report']
  },
  related_to: { src: [], dst: [] }
}

export const NOTE_KINDS = ['scratchpad', 'paper_note', 'db_record', 'overview'] as const

export type NoteKind = (typeof NOTE_KINDS)[number]

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

export const PROJECT_STATUSES = ['active', 'archived'] as const

export type ProjectStatus = (typeof PROJECT_STATUSES)[number]

export const REPORT_STATUSES = ['draft', 'confirmed'] as const

export type ReportStatus = (typeof REPORT_STATUSES)[number]

export const EVIDENCE_DIRECTIONS = ['support', 'oppose'] as const

export type EvidenceDirection = (typeof EVIDENCE_DIRECTIONS)[number]

export const PAPER_FILE_STATES = ['ok', 'missing'] as const

export type PaperFileState = (typeof PAPER_FILE_STATES)[number]

export const PAPER_SOURCE_STATES = ['ok', 'missing', 'unreadable'] as const

export type PaperSourceState = (typeof PAPER_SOURCE_STATES)[number]

function includes<T extends string>(values: readonly T[], value: unknown): value is T {
  return typeof value === 'string' && (values as readonly string[]).includes(value)
}

export function isEntityType(value: unknown): value is EntityType {
  return includes(ENTITY_TYPES, value)
}

export function isEventType(value: unknown): value is EventType {
  return includes(EVENT_TYPES, value)
}

export function isActor(value: unknown): value is Actor {
  return includes(ACTORS, value)
}

export function isRelation(value: unknown): value is Relation {
  return includes(RELATIONS, value)
}

export function isNoteKind(value: unknown): value is NoteKind {
  return includes(NOTE_KINDS, value)
}

export function isIdeaStatus(value: unknown): value is IdeaStatus {
  return includes(IDEA_STATUSES, value)
}

export function isProjectStatus(value: unknown): value is ProjectStatus {
  return includes(PROJECT_STATUSES, value)
}

export function isReportStatus(value: unknown): value is ReportStatus {
  return includes(REPORT_STATUSES, value)
}

export function isEvidenceDirection(value: unknown): value is EvidenceDirection {
  return includes(EVIDENCE_DIRECTIONS, value)
}

/** True when `src --relation--> dst` is in the direction registry. */
export function isRelationAllowed(src: EntityType, relation: Relation, dst: EntityType): boolean {
  const rule = RELATION_RULES[relation]
  if (rule.src.length > 0 && !rule.src.includes(src)) return false
  return rule.dst.length === 0 || rule.dst.includes(dst)
}