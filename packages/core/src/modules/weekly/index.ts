/**
 * Weekly review module: digest, seven-section template, validation,
 * AI/skeleton generation, skills, revisions, lifecycle and export.
 *
 * The service is created per project with a `ProjectHandle`-shaped object plus
 * the workspace global connection. Collaborators the foundation still owns
 * (events, trash, AI) arrive as narrow ports; when the AI port is absent the
 * module degrades to the deterministic skeleton and persists the cause.
 */

export * from './types'
export { WeeklyService, toDigestView, toOutcomeView, toReportView, reportWithDigest } from './service'
export type {
  DigestView,
  GenerateOutcomeView,
  PeriodEntryView,
  ReportPageView,
  ReportView,
  ReportSummaryView,
  WeeklyActivityEvent,
  WeeklyAiPort,
  WeeklyAiRequest,
  WeeklyAiResult,
  WeeklyContext,
  WeeklyEventSink,
  WeeklyFileSystem,
  WeeklyProjectHandle,
  WeeklyServiceOptions,
  WeeklySettings,
  WeeklyTrashSink
} from './context'
export { DEFAULT_TEMPLATE, buildSkeleton, assembleReport, parseReport, normalizeHeading } from './template'
export {
  collectActivities,
  coverageOf,
  digestHash,
  digestRefs,
  MAX_SECTION_ITEMS,
  MAX_SUMMARY_RUNES,
  DEFAULT_MAX_EVENTS
} from './digest'
export {
  applyAckFingerprints,
  buildCheckState,
  capViolations,
  mergeAcks,
  validateReport,
  MAX_VIOLATIONS
} from './validate'
export {
  DEFAULT_WEEK_START_DAY,
  isoWeek,
  periodFromDates,
  validatePeriod,
  weekStartDate,
  zonedMidnightUtc
} from './period'
export {
  BUILTIN_SKILL,
  BUILTIN_SKILL_ID,
  SkillRegistry,
  DEFAULT_SKILL_KEY,
  SKILLS_KEY
} from './skills'
export { generateDraft, generateForPeriod, hasManualTrace, PROMPT_VERSION } from './generate'
export { createWeeklyAiPort, type WeeklyAiAdapterDeps } from './ai-adapter'
export { regenerateSection } from './revision'
export { exportReport, mapFsError, renderMarkdown, renderHtml, NODE_FILE_SYSTEM } from './export'
export { weeklyTools, WEEKLY_TOOL_NAMES, WEEKLY_RUNTIME_KEY, reportView } from './mcp'
export type {
  WeeklyConfirmationPort,
  WeeklyConfirmationRequest,
  WeeklyJobPort,
  WeeklyJobSubmitRequest,
  WeeklyMcpRuntime,
  WeeklyToolService
} from './mcp'
export { weeklySchemaByChannel, weeklyValidators } from '../../validation/weekly'