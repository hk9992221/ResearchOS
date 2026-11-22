import { z } from 'zod'

import type { CommandMap, CommandValidator } from '@researchos/shared'

/**
 * Validation schemas for the workspace lifecycle and application config
 * commands. The router calls `parse`; a zod
 * failure becomes INVALID_ARGUMENT at the IPC boundary.
 */

const rootPath = z.string().min(1).max(4096).describe('absolute workspace root')

export const workspaceCreateSchema = z.strictObject({
  root: rootPath,
  name: z.string().min(1).max(200).optional()
})

export const workspaceOpenSchema = z.strictObject({ root: rootPath })

export const workspaceScanSchema = z.strictObject({
  root: rootPath.optional()
})

export const workspaceMigratePlanSchema = z.strictObject({
  targetRoot: rootPath
})

export const workspaceMigrateStartSchema = z.strictObject({
  targetRoot: rootPath
})

export const workspaceMigrateProgressSchema = z.strictObject({
  jobId: z.string().min(1).max(128)
})

export const workspaceMigrateCancelSchema = z.strictObject({
  jobId: z.string().min(1).max(128)
})

export const workspaceProjectsListSchema = z.strictObject({
  includeArchived: z.boolean().optional()
})

export const workspaceProjectSchema = z.strictObject({
  projectId: z.string().min(1).max(64)
})

export const appConfigUpdateSchema = z.strictObject({
  patch: z
    .strictObject({
      workspaceRoot: z.string().max(4096).nullable().optional(),
      zoteroDataDir: z.string().max(4096).nullable().optional(),
      language: z.enum(['en', 'zh']).optional()
    })
    .refine((patch) => Object.keys(patch).length > 0, { message: 'patch must name at least one field' })
})

function validator<T>(schema: z.ZodType<T>): CommandValidator<T> {
  return {
    parse: (input) => schema.parse(input)
  }
}

const voidValidator: CommandValidator<void> = { parse: () => undefined }

/** One validator per workspace/app-config channel this handler registers. */
export const workspaceValidators: {
  [C in
    | 'workspace.status'
    | 'workspace.create'
    | 'workspace.open'
    | 'workspace.close'
    | 'workspace.scan'
    | 'workspace.migrate.plan'
    | 'workspace.migrate.start'
    | 'workspace.migrate.progress'
    | 'workspace.migrate.cancel'
    | 'workspace.projects.list'
    | 'workspace.projects.open'
    | 'workspace.projects.close'
    | 'app.config.get'
    | 'app.config.update']: CommandValidator<CommandMap[C]['req']>
} = {
  'workspace.status': voidValidator,
  'workspace.create': validator(workspaceCreateSchema),
  'workspace.open': validator(workspaceOpenSchema),
  'workspace.close': voidValidator,
  'workspace.scan': validator(workspaceScanSchema),
  'workspace.migrate.plan': validator(workspaceMigratePlanSchema),
  'workspace.migrate.start': validator(workspaceMigrateStartSchema),
  'workspace.migrate.progress': validator(workspaceMigrateProgressSchema),
  'workspace.migrate.cancel': validator(workspaceMigrateCancelSchema),
  'workspace.projects.list': validator(workspaceProjectsListSchema),
  'workspace.projects.open': validator(workspaceProjectSchema),
  'workspace.projects.close': validator(workspaceProjectSchema),
  'app.config.get': voidValidator,
  'app.config.update': validator(appConfigUpdateSchema)
}