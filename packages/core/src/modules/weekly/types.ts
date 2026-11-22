/**
 * Domain types of the weekly review module.
 *
 * Persistence follows the frozen schema: `weekly_reports.meta` is a JSON
 * document whose keys must stay identical to the Go implementation's
 * (`mode`, `ai_status`, `checked_at`, `confirmed_hash`, ...) so a workspace
 * written by either engine stays readable by the other. The service boundary
 * therefore keeps those snake_case names rather than renaming the document.
 */

export type ReportStatus = 'draft' | 'confirmed'

export type SectionKey =
  | 'work'
  | 'literature'
  | 'theory'
  | 'experiment'
  | 'idea'
  | 'problem'
  | 'next_week'

export const SECTION_KEYS: readonly SectionKey[] = [
  'work',
  'literature',
  'theory',
  'experiment',
  'idea',
  'problem',
  'next_week'
]

export type WeeklyActor = 'user' | 'mcp' | 'ai'

export interface EntityRef {
  type: string
  id: string
}

/**
 * One report window in the report timezone. `startDate` is inclusive and
 * `endExclusiveDate` is exclusive; both are calendar dates (`YYYY-MM-DD`).
 * The inclusive display end is `addDays(endExclusiveDate, -1)`.
 */
export interface WeeklyPeriod {
  startDate: string
  endExclusiveDate: string
  timeZone: string
}

export interface TemplateSection {
  key: SectionKey
  title: string
  num: number
}

export interface WeeklyTemplate {
  id: string
  version: number
  sections: TemplateSection[]
}

/* ------------------------------------------------------------------ digest */

export interface DigestItem {
  ref: EntityRef
  kind: string
  state: string
  summary: string
  detail: string
  old: string
  new: string
  actor: string
  /** UTC ISO instants. */
  firstAt: string
  lastAt: string
  eventIds: string[]
  unconfirmedSource: boolean
  broken: boolean
  /** Bounded excerpt of the entity's own text, never the title alone. */
  brief: string
}

export interface DigestSection {
  key: SectionKey
  title: string
  items: DigestItem[]
}

export interface BlockedTask {
  ref: EntityRef
  title: string
  columnName: string
  isDone: boolean
  reason: string
}

export type PlannedOrigin = 'week_plan' | 'deadline' | 'carry_over' | 'manual'

export interface PlannedTask {
  ref: EntityRef
  title: string
  columnName: string
  isDone: boolean
  deadline?: string
  weekPlan?: string
  origin: PlannedOrigin
}

export interface DigestStats {
  countsByType: Record<string, number>
  countsBySection: Partial<Record<SectionKey, number>>
  countsByActor: Record<string, number>
}

export interface ActivityDigest {
  period: WeeklyPeriod
  timeZone: string
  collectedAt: string
  sections: DigestSection[]
  blocked: BlockedTask[]
  planned: PlannedTask[]
  stats: DigestStats
  truncated: boolean
  warnings: string[]
  digestHash: string
  /** Filing actions: known refs only, never rendered or prompted. */
  housekeeping: DigestItem[]
}

/* -------------------------------------------------------------- validation */

export type Severity = 'error' | 'warning'

export interface Ack {
  actor: string
  at: string
  note?: string
}

export interface Violation {
  rule: string
  severity: Severity
  fingerprint: string
  section: SectionKey | ''
  ref?: EntityRef
  message: string
  ack?: Ack
}

export interface CheckState {
  checked_at: string
  violations: Violation[]
  truncated?: boolean
}

/* ---------------------------------------------------------------- metadata */

export interface GenerationMeta {
  mode: 'manual' | 'ai'
  ai_status: 'ok' | 'failed' | 'skipped'
  ai_output_id?: string
  ai_output_ids?: string[]
  provider_id?: string
  model_id?: string
  prompt_version?: string
  template_id: string
  template_version: number
  generated_at?: string
  digest_hash?: string
  attempts: number
  error?: string
  skill_id?: string
  skill_name?: string
  notes?: string
}

export interface Coverage {
  period_start: string
  period_end: string
  timezone: string
  event_count: number
  counts_by_type: Record<string, number>
  counts_by_section: Record<string, number>
  truncated: boolean
  digest_hash: string
  computed_at: string
}

export interface ReportMeta {
  generation?: GenerationMeta
  coverage?: Coverage
  checks?: CheckState
  confirmed_hash?: string
  refs?: EntityRef[]
}

/* ------------------------------------------------------------------ report */

export interface WeeklyReport {
  id: string
  period: WeeklyPeriod
  title: string
  contentMd: string
  contentHash: string
  status: ReportStatus
  meta: ReportMeta
  createdAt: string
  updatedAt: string
  confirmedAt?: string
  deletedAt?: string
  aiOutputId?: string
}

export interface ReportSummary {
  id: string
  title: string
  period: WeeklyPeriod
  status: ReportStatus
  excerpt: string
  createdAt: string
  updatedAt: string
  confirmedAt?: string
  deletedAt?: string
}

export interface ReportPage {
  items: ReportSummary[]
  total: number
  page: number
  pageSize: number
}

export interface PeriodHistoryEntry {
  period: WeeklyPeriod
  reportId: string | null
  status: ReportStatus | null
}

/* ------------------------------------------------------------------- inputs */

export interface CreateReportInput {
  period?: WeeklyPeriod
  skeleton?: boolean
  /** Bypasses the one-report-per-period rule. */
  force?: boolean
  actor?: WeeklyActor
}

export interface ListReportsInput {
  status?: ReportStatus[]
  from?: string
  to?: string
  includeDeleted?: boolean
  page?: number
  pageSize?: number
  order?: 'period_start' | '-period_start' | 'updated_at'
}

export interface CollectInput {
  period?: WeeklyPeriod
  timeZone?: string
  maxEvents?: number
}

export type SectionProgress = (done: number, total: number, section: SectionKey | '') => void
export type SectionDelta = (section: SectionKey, delta: string) => void

export interface GenerateInput {
  reportId: string
  replace?: boolean
  actor?: WeeklyActor
  skillId?: string
  progress?: SectionProgress
  delta?: SectionDelta
}

export interface GeneratePeriodInput {
  period: WeeklyPeriod
  includeAI?: boolean
  replace?: boolean
  actor?: WeeklyActor
  skillId?: string
  progress?: SectionProgress
  delta?: SectionDelta
}

export interface GenerateOutcome {
  report: WeeklyReport
  digest: ActivityDigest
  violations: Violation[]
}

export interface UpdateContentInput {
  reportId: string
  contentMd: string
  expectedHash?: string
  expectedUpdatedAt?: string
  actor?: WeeklyActor
}

export interface ConfirmInput {
  reportId: string
  actor?: WeeklyActor
  ackFingerprints?: string[]
  ackNotes?: Record<string, string>
}

export interface RevertInput {
  reportId: string
  actor?: WeeklyActor
}

export interface SectionRevisionInput {
  reportId: string
  section: SectionKey
  focusText?: string
  instructions?: string
  expectedHash?: string
  skillId?: string
  actor?: WeeklyActor
  progress?: SectionProgress
  delta?: SectionDelta
}

export interface SectionRevisionOutcome {
  report: WeeklyReport
  section: SectionKey
  markdown: string
  violations: Violation[]
  outputId: string
}

export interface ExportInput {
  reportId: string
  format?: 'md' | 'html'
  /** Absolute OS directory; may lie outside the workspace (defect 8). */
  destDir?: string
  overwrite?: boolean
  frontMatter?: boolean
  actor?: WeeklyActor
}

export interface ExportResult {
  path: string
  format: 'md' | 'html'
  bytes: number
  status: ReportStatus
}

/* ------------------------------------------------------------------- skills */

export interface WeeklySkill {
  id: string
  name: string
  prompt: string
  source: 'builtin' | 'custom' | 'plugin'
  /** Read-only skills cannot be overwritten or deleted. */
  builtin?: boolean
}

export interface SkillSet {
  skills: WeeklySkill[]
  default: string
}

export interface SkillInput {
  id?: string
  name: string
  prompt: string
}