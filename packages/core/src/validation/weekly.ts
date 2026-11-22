import { z } from 'zod'

import type { WeeklyCommands } from '@researchos/shared'

/**
 * Zod schemas for the weekly review IPC commands. The router validates with
 * these before dispatch; the same shapes are the source for the MCP tool
 * inputs and for service-level tests.
 */

const projectId = z.string().min(1).max(64)
const reportId = z.string().min(1).max(64)
const calendarDate = z
  .string()
  .regex(/^\d{4}-\d{2}-\d{2}$/, 'must be YYYY-MM-DD')
  .refine(isCalendarDate, 'must be a real calendar date')

export const periodSchema = z
  .object({
    start: calendarDate,
    end: calendarDate
  })
  .strict()
  .refine((value) => value.end >= value.start, 'end must not be before start')
  .refine((value) => daysBetween(value.start, value.end) <= 365, 'period must be at most 366 days')

export const weeklyPeriodsListSchema = z
  .object({
    projectId,
    limit: z.number().int().min(1).max(52).optional()
  })
  .strict()

export const weeklyDigestGetSchema = z
  .object({
    projectId,
    period: periodSchema.optional(),
    maxEvents: z.number().int().min(1).max(10_000).optional()
  })
  .strict()

export const weeklyReportsListSchema = z
  .object({
    projectId,
    status: z.array(z.enum(['draft', 'confirmed'])).max(2).optional(),
    from: calendarDate.optional(),
    to: calendarDate.optional(),
    includeDeleted: z.boolean().optional(),
    page: z.number().int().min(1).max(100_000).optional(),
    pageSize: z.number().int().min(1).max(100).optional(),
    order: z.enum(['period_start', '-period_start', 'updated_at']).optional()
  })
  .strict()

export const weeklyReportsGetSchema = z.object({ projectId, reportId }).strict()

export const weeklyReportsCreateSchema = z
  .object({
    projectId,
    period: periodSchema,
    skeleton: z.boolean().optional(),
    force: z.boolean().optional()
  })
  .strict()

export const weeklyReportsGenerateSchema = z
  .object({
    projectId,
    period: periodSchema,
    skillId: z.string().min(1).max(128).optional(),
    includeAI: z.boolean().optional(),
    replace: z.boolean().optional()
  })
  .strict()

export const weeklyReportsUpdateSchema = z
  .object({
    projectId,
    reportId,
    contentMd: z.string().max(512 * 1024),
    expectedHash: z.string().min(1).max(128).optional(),
    expectedUpdatedAt: z.string().min(1).max(64).optional()
  })
  .strict()
  .refine(
    (value) => value.expectedHash !== undefined || value.expectedUpdatedAt !== undefined,
    'expectedHash or expectedUpdatedAt is required'
  )

export const weeklyReportsValidateSchema = z.object({ projectId, reportId }).strict()

/** Selection-driven revision stays inside one of the seven canonical sections. */
const weeklySectionKey = z.enum([
  'work',
  'literature',
  'theory',
  'experiment',
  'idea',
  'problem',
  'next_week'
])

export const weeklyReportsReviseSchema = z
  .object({
    projectId,
    reportId,
    section: weeklySectionKey,
    focusText: z.string().max(20_000).optional(),
    instructions: z.string().max(20_000).optional(),
    expectedHash: z.string().min(1).max(128).optional(),
    skillId: z.string().min(1).max(128).optional()
  })
  .strict()
  .refine(
    (value) => (value.focusText ?? '').trim() !== '' || (value.instructions ?? '').trim() !== '',
    'focusText or instructions is required'
  )

export const weeklyReportsConfirmSchema = z
  .object({
    projectId,
    reportId,
    ackFingerprints: z.array(z.string().min(1).max(128)).max(200).optional(),
    ackNotes: z.record(z.string().min(1).max(128), z.string().max(2000)).optional()
  })
  .strict()

export const weeklyReportsRevertSchema = z.object({ projectId, reportId }).strict()

export const weeklyReportsDeleteSchema = z.object({ projectId, reportId }).strict()

export const weeklySkillsSaveSchema = z
  .object({
    skill: z
      .object({
        id: z.string().min(1).max(128).optional(),
        name: z.string().min(1).max(200),
        prompt: z.string().min(1).max(20000)
      })
      .strict()
  })
  .strict()

export const weeklySkillsDeleteSchema = z.object({ id: z.string().min(1).max(128) }).strict()

export const weeklyReportsExportSchema = z
  .object({
    projectId,
    reportId,
    format: z.enum(['md', 'html']),
    destDir: z.string().min(1).max(4096).optional(),
    overwrite: z.boolean().optional(),
    frontMatter: z.boolean().optional()
  })
  .strict()

export const weeklySchemaByChannel = {
  'weekly.periods.list': weeklyPeriodsListSchema,
  'weekly.digest.get': weeklyDigestGetSchema,
  'weekly.template.get': null,
  'weekly.skills.list': null,
  'weekly.skills.save': weeklySkillsSaveSchema,
  'weekly.skills.delete': weeklySkillsDeleteSchema,
  'weekly.skills.setDefault': weeklySkillsDeleteSchema,
  'weekly.reports.list': weeklyReportsListSchema,
  'weekly.reports.get': weeklyReportsGetSchema,
  'weekly.reports.create': weeklyReportsCreateSchema,
  'weekly.reports.generate': weeklyReportsGenerateSchema,
  'weekly.reports.update': weeklyReportsUpdateSchema,
  'weekly.reports.validate': weeklyReportsValidateSchema,
  'weekly.reports.revise': weeklyReportsReviseSchema,
  'weekly.reports.confirm': weeklyReportsConfirmSchema,
  'weekly.reports.revert': weeklyReportsRevertSchema,
  'weekly.reports.delete': weeklyReportsDeleteSchema,
  'weekly.reports.restore': weeklyReportsDeleteSchema,
  'weekly.reports.export': weeklyReportsExportSchema
} as const

/** Adapts a zod schema to the router's structural validator contract. */
function validatorOf<T extends z.ZodType>(schema: T): { parse(input: unknown): z.infer<T> } {
  return {
    parse: (input: unknown): z.infer<T> => schema.parse(input)
  }
}

const noPayload = {
  parse: (_input?: unknown): undefined => undefined
}

export const weeklyValidators = {
  'weekly.periods.list': validatorOf(weeklyPeriodsListSchema),
  'weekly.digest.get': validatorOf(weeklyDigestGetSchema),
  'weekly.template.get': noPayload,
  'weekly.skills.list': noPayload,
  'weekly.skills.save': validatorOf(weeklySkillsSaveSchema),
  'weekly.skills.delete': validatorOf(weeklySkillsDeleteSchema),
  'weekly.skills.setDefault': validatorOf(weeklySkillsDeleteSchema),
  'weekly.reports.list': validatorOf(weeklyReportsListSchema),
  'weekly.reports.get': validatorOf(weeklyReportsGetSchema),
  'weekly.reports.create': validatorOf(weeklyReportsCreateSchema),
  'weekly.reports.generate': validatorOf(weeklyReportsGenerateSchema),
  'weekly.reports.update': validatorOf(weeklyReportsUpdateSchema),
  'weekly.reports.validate': validatorOf(weeklyReportsValidateSchema),
  'weekly.reports.revise': validatorOf(weeklyReportsReviseSchema),
  'weekly.reports.confirm': validatorOf(weeklyReportsConfirmSchema),
  'weekly.reports.revert': validatorOf(weeklyReportsRevertSchema),
  'weekly.reports.delete': validatorOf(weeklyReportsDeleteSchema),
  'weekly.reports.restore': validatorOf(weeklyReportsDeleteSchema),
  'weekly.reports.export': validatorOf(weeklyReportsExportSchema)
} satisfies Record<keyof WeeklyCommands, { parse(input: unknown): unknown }>

function isCalendarDate(value: string): boolean {
  const [year, month, day] = value.split('-').map(Number)
  if (year === undefined || month === undefined || day === undefined) return false
  const date = new Date(Date.UTC(year, month - 1, day))
  return (
    date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
  )
}

function daysBetween(start: string, end: string): number {
  return Math.round(
    (Date.parse(`${end}T00:00:00Z`) - Date.parse(`${start}T00:00:00Z`)) / 86_400_000
  )
}