import type { Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { EventType, EntityType, Actor as EventActor } from '../../schema'
import type { SettingsStore } from '../../settings'
import type { SkillRegistry } from './skills'
import type { ReportMeta, ReportStatus, SectionKey, Violation, WeeklySkill } from './types'

/** The subset of `ProjectHandle` this module needs. */
export interface WeeklyProjectHandle {
  readonly id: string
  readonly db: Db
  inTx<T>(fn: () => T): T
}

/** One Activity row to append. Emitted only inside `handle.inTx`. */
export interface WeeklyActivityEvent {
  type: EventType
  entityType: EntityType
  entityId: string
  summary: string
  actor: EventActor
  at: Date
  old?: Record<string, unknown>
  new?: Record<string, unknown>
}

/**
 * Activity sink. The foundation event bus satisfies this shape; the emitter
 * must participate in the caller's open transaction so a failed write leaves
 * no event behind.
 */
export interface WeeklyEventSink {
  emit(event: WeeklyActivityEvent): void
}

export interface WeeklyTrashSink {
  softDelete(ref: { type: 'weekly_report'; id: string }, actor: EventActor): void
  restore(ref: { type: 'weekly_report'; id: string }, actor: EventActor): void
}

export interface WeeklyAiMessage {
  role: 'system' | 'user'
  content: string
}

export interface WeeklyAiRequest {
  taskKind: 'weekly_report'
  promptVersion: string
  messages: WeeklyAiMessage[]
  sourceRefs: Array<{ entityType: string; entityId: string }>
  entity: { type: 'weekly_report'; id: string }
  maxOutputTokens: number
  signal?: AbortSignal
  onDelta?: (text: string) => void
}

export interface WeeklyAiResult {
  outputId: string
  contentMd: string
  attempts: number
  provenance: {
    providerId?: string
    modelId?: string
    promptVersion?: string
    generatedAt?: string
  }
}

/**
 * Generation port (W3 `AiGateway`). `generate` writes one `ai_outputs`
 * draft row and returns its id; `confirmOutput` flips that row inside the
 * caller's transaction. There is no fallback implementation here: when the
 * port is absent the module degrades to deterministic skeletons.
 */
export interface WeeklyAiPort {
  generate(request: WeeklyAiRequest): Promise<WeeklyAiResult>
  confirmOutput(outputId: string, actor: 'user'): void
}

export interface WeeklyLog {
  warn(message: string, fields?: Record<string, unknown>): void
  error?(message: string, fields?: Record<string, unknown>): void
}

export interface WeeklySettings {
  weekStartDay: number
  timeZone: string
  defaultPeriod: 'current' | 'previous'
  maxEvents: number
}

export interface WeeklyContext {
  readonly projectId: string
  readonly handle: WeeklyProjectHandle
  readonly globalDb: Db
  readonly root: string
  readonly clock: Clock
  readonly settings: SettingsStore
  readonly projectSettings: SettingsStore
  readonly events: WeeklyEventSink
  readonly trash?: WeeklyTrashSink
  readonly ai?: WeeklyAiPort
  readonly skills: SkillRegistry
  readonly logger?: WeeklyLog
  readonly fileSystem: WeeklyFileSystem
}

/** Report view handed to IPC/MCP; dates are inclusive calendar dates. */
export interface ReportView {
  reportId: string
  periodStart: string
  periodEnd: string
  title: string
  contentMd: string
  contentHash: string
  status: ReportStatus
  meta: ReportMeta
  confirmedAt: string | null
  deletedAt: string | null
  createdAt: string
  updatedAt: string
}

export interface ReportSummaryView {
  reportId: string
  title: string
  periodStart: string
  periodEnd: string
  status: ReportStatus
  excerpt: string
  createdAt: string
  updatedAt: string
  confirmedAt: string | null
  deletedAt: string | null
}

export interface ReportPageView {
  items: ReportSummaryView[]
  total: number
  page: number
  pageSize: number
}

export interface PeriodEntryView {
  periodStart: string
  periodEnd: string
  timeZone: string
  reportId: string | null
  status: ReportStatus | null
}

export interface DigestItemView {
  type: string
  id: string
  kind: string
  summary: string
  detail: string
  old: string
  new: string
  state: string
  actor: string
  firstAt: string
  lastAt: string
  unconfirmedSource: boolean
  broken: boolean
}

export interface DigestSectionView {
  key: SectionKey
  title: string
  items: DigestItemView[]
}

export interface DigestView {
  periodStart: string
  periodEnd: string
  timeZone: string
  eventCount: number
  countsByType: Record<string, number>
  countsBySection: Record<string, number>
  truncated: boolean
  warnings: string[]
  digestHash: string
  collectedAt: string
  sections: DigestSectionView[]
  blocked: Array<{ type: string; id: string; title: string; columnName: string; isDone: boolean; reason: string }>
  planned: Array<{
    type: string
    id: string
    title: string
    columnName: string
    isDone: boolean
    deadline: string | null
    weekPlan: string | null
    origin: string
  }>
}

export interface GenerateOutcomeView {
  report: ReportView
  digest: DigestView
  violations: Violation[]
}

export interface WeeklyServiceOptions {
  projectId: string
  handle: WeeklyProjectHandle
  globalDb: Db
  /** Workspace root; also used to read note bodies and default exports. */
  root: string
  clock?: Clock
  events: WeeklyEventSink
  trash?: WeeklyTrashSink
  ai?: WeeklyAiPort
  settings?: SettingsStore
  projectSettings?: SettingsStore
  /** Test seam for permission/disk failures (default: node:fs). */
  fileSystem?: WeeklyFileSystem
  logger?: WeeklyLog
  pluginSkills?: WeeklySkill[]
}

/** Filesystem operations used by export; injectable to prove error mapping. */
export interface WeeklyFileSystem {
  stat(target: string): { isDirectory(): boolean; isFile(): boolean }
  access(target: string, mode: number): void
  mkdir(target: string, options: { recursive: boolean }): void
  writeFile(target: string, data: Buffer, options: { exclusive: boolean }): void
  rename(from: string, to: string): void
  unlink(target: string): void
  exists(target: string): boolean
}