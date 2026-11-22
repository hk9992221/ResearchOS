import { z } from 'zod'

import type { ExportCommands } from '@researchos/shared'

/**
 * Zod schemas for the six export/import IPC commands. The router parses before
 * dispatch; `.strict()` rejects unknown fields so a renderer bug cannot widen
 * the request surface (for example by smuggling a path into a plan call).
 */

const projectId = z.string().min(1).max(64)
const jobId = z.string().min(1).max(64)
const planId = z.string().min(1).max(64)
const exportFormat = z.enum(['folder', 'zip', 'tar.gz'])
const destDir = z.string().min(1).max(4096)

export const exportProjectPlanSchema = z
  .object({
    projectId,
    format: exportFormat,
    destDir: destDir.optional()
  })
  .strict()

export const exportProjectStartSchema = z
  .object({
    projectId,
    format: exportFormat,
    destDir: destDir.optional(),
    overwrite: z.boolean().optional()
  })
  .strict()

export const exportJobSchema = z.object({ jobId }).strict()

export const exportImportInspectSchema = z
  .object({ sourcePath: z.string().min(1).max(4096) })
  .strict()

export const exportImportStartSchema = z
  .object({
    planId,
    newIdentity: z.boolean().optional(),
    name: z.string().max(200).optional()
  })
  .strict()

function validatorOf<T extends z.ZodType>(schema: T): { parse(input: unknown): z.infer<T> } {
  return {
    parse: (input: unknown): z.infer<T> => schema.parse(input)
  }
}

export const exportCancelSchema = z.object({ jobId: z.string().min(1).max(64) }).strict()

export const exportValidators = {
  'export.project.plan': validatorOf(exportProjectPlanSchema),
  'export.project.start': validatorOf(exportProjectStartSchema),
  'export.project.status': validatorOf(exportJobSchema),
  'export.cancel': validatorOf(exportCancelSchema),
  'export.import.inspect': validatorOf(exportImportInspectSchema),
  'export.import.start': validatorOf(exportImportStartSchema),
  'export.import.status': validatorOf(exportJobSchema)
} satisfies Record<keyof ExportCommands, { parse(input: unknown): unknown }>