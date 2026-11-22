import { z } from 'zod'
import { DB_FIELD_TYPES, NOTE_KINDS } from '@researchos/shared'
import type { FilterNode } from '@researchos/shared'

import { AppErrorCode } from '../errors'
import { appError } from '../errors'

/**
 * Zod schemas for every notes command and MCP input. Services re-validate
 * semantics (field configs, filter limits, uniqueness); these schemas pin the
 * wire shape so an unknown or missing key is INVALID_ARGUMENT, never INTERNAL.
 */

export const idSchema = z.string().min(1).max(64)
export const projectIdSchema = z.string().min(1).max(64)
const optionalReason = z.string().max(200).optional()

const noteKindSchema = z.enum(NOTE_KINDS)
const fieldTypeSchema = z.enum(DB_FIELD_TYPES)

const optionSchema = z.union([
  z.string().min(1).max(200),
  z
    .object({
      id: z.string().min(1).max(64).optional(),
      name: z.string().min(1).max(200),
      color: z.string().max(64).optional()
    })
    .strict()
])

const optionsSchema = z.union([z.array(optionSchema), z.record(z.string(), z.unknown())])
const targetsSchema = z.array(z.string().min(1).max(32))

export const fieldCreateSchema = z
  .object({
    name: z.string().min(1).max(64),
    type: fieldTypeSchema,
    config: z.unknown().optional(),
    options: optionsSchema.optional(),
    targets: targetsSchema.optional(),
    role: z.enum(['title', 'plain']).optional()
  })
  .strict()

export const fieldPatchSchema = z
  .object({
    name: z.string().min(1).max(64).optional(),
    config: z.unknown().optional(),
    options: optionsSchema.optional(),
    targets: targetsSchema.optional(),
    role: z.enum(['title', 'plain']).optional()
  })
  .strict()

export const noteListSchema = z
  .object({
    projectId: projectIdSchema,
    kind: noteKindSchema.optional(),
    q: z.string().max(500).optional(),
    tags: z.array(z.string().min(1)).max(50).optional(),
    includeDeleted: z.boolean().optional(),
    limit: z.number().int().min(1).max(500).optional(),
    offset: z.number().int().min(0).optional()
  })
  .strict()

export const noteGetSchema = z.object({ projectId: projectIdSchema, noteId: idSchema }).strict()

export const noteBodyGetSchema = z.object({ projectId: projectIdSchema, noteId: idSchema }).strict()

export const tagListSchema = z.object({ projectId: projectIdSchema }).strict()

export const noteCreateSchema = z
  .object({
    projectId: projectIdSchema,
    title: z.string().max(200).optional(),
    kind: noteKindSchema.optional(),
    description: z.string().max(2000).optional(),
    bodyMd: z.string().max(5 * 1024 * 1024).optional(),
    tags: z.array(z.string().min(1)).max(50).optional(),
    paperId: z.string().min(1).max(64).nullable().optional()
  })
  .strict()

export const noteUpdateMetaSchema = z
  .object({
    projectId: projectIdSchema,
    noteId: idSchema,
    patch: z
      .object({
        title: z.string().min(1).max(200).optional(),
        description: z.string().max(4000).optional()
      })
      .strict(),
    expectedUpdatedAt: z.string().min(1).optional()
  })
  .strict()

export const noteSaveBodySchema = z
  .object({
    projectId: projectIdSchema,
    noteId: idSchema,
    markdown: z.string().max(5 * 1024 * 1024),
    baseHash: z.string().max(128)
  })
  .strict()

export const noteIdSchema = z.object({ projectId: projectIdSchema, noteId: idSchema }).strict()

export const noteTagsSchema = z
  .object({
    projectId: projectIdSchema,
    noteId: idSchema,
    tags: z.array(z.string().min(1)).max(100)
  })
  .strict()

export const notePromoteSchema = z
  .object({ projectId: projectIdSchema, noteId: idSchema, databaseId: idSchema })
  .strict()

export const noteDetachSchema = z
  .object({
    projectId: projectIdSchema,
    recordId: idSchema,
    deleteNote: z.boolean().optional()
  })
  .strict()

export const databaseCreateSchema = z
  .object({
    projectId: projectIdSchema,
    name: z.string().min(1).max(200),
    description: z.string().max(2000).optional(),
    fields: z.array(fieldCreateSchema).max(100).optional()
  })
  .strict()

export const databaseUpdateSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    name: z.string().min(1).max(200).optional(),
    description: z.string().max(2000).optional(),
    expectedUpdatedAt: z.string().min(1).optional()
  })
  .strict()

export const databaseIdSchema = z.object({ projectId: projectIdSchema, databaseId: idSchema }).strict()

export const fieldCreateCommandSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    field: fieldCreateSchema
  })
  .strict()

export const fieldUpdateSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    fieldId: idSchema,
    patch: fieldPatchSchema,
    expectedUpdatedAt: z.string().min(1).optional()
  })
  .strict()

export const fieldDeleteSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    fieldId: idSchema,
    expectedUpdatedAt: z.string().min(1).optional()
  })
  .strict()

export const fieldReorderSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    order: z.object({ fieldIds: z.array(idSchema).max(500) }).strict()
  })
  .strict()

export const recordValuesSchema = z.record(z.string(), z.unknown())

export const recordCreateSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    values: recordValuesSchema.optional(),
    title: z.string().max(200).optional(),
    noteId: idSchema.optional()
  })
  .strict()

export const recordUpdateSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    recordId: idSchema,
    patch: z
      .object({
        values: recordValuesSchema.optional(),
        title: z.string().max(200).optional()
      })
      .strict(),
    expectedUpdatedAt: z.string().min(1).optional()
  })
  .strict()

export const recordListSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    q: z.string().max(500).optional(),
    filter: z.custom<FilterNode>().optional(),
    sorts: z
      .array(z.object({ field: z.string().min(1), direction: z.enum(['asc', 'desc']) }).strict())
      .max(20)
      .optional(),
    includeDeleted: z.boolean().optional(),
    limit: z.number().int().min(1).max(500).optional(),
    offset: z.number().int().min(0).optional()
  })
  .strict()

export const recordIdSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    recordId: idSchema
  })
  .strict()

export const recordDeleteSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    recordId: idSchema,
    deleteNote: z.boolean().optional()
  })
  .strict()

export const recordReorderSchema = z
  .object({
    projectId: projectIdSchema,
    databaseId: idSchema,
    order: z.object({ recordIds: z.array(idSchema).max(10000) }).strict()
  })
  .strict()

export const attachmentListSchema = z.object({ projectId: projectIdSchema, noteId: idSchema }).strict()

export const attachmentAddSchema = z
  .object({
    projectId: projectIdSchema,
    noteId: idSchema,
    sourcePath: z.string().min(1),
    fileName: z.string().min(1).max(255).optional()
  })
  .strict()

export const attachmentRemoveSchema = z
  .object({
    projectId: projectIdSchema,
    noteId: idSchema,
    attachmentId: z.string().min(1).max(255)
  })
  .strict()

export const reconcileScanSchema = z.object({ projectId: projectIdSchema }).strict()

const reconcileActionSchema = z
  .object({
    kind: z.enum(['adopt', 'accept_external_edit', 'mark_broken', 'reattach', 'recreate']),
    relPath: z.string().min(1).optional(),
    noteId: idSchema.optional(),
    newRelPath: z.string().min(1).optional(),
    title: z.string().max(200).optional(),
    noteKind: noteKindSchema.optional(),
    actor: z.enum(['user', 'mcp', 'ai'])
  })
  .strict()

export const reconcileApplySchema = z
  .object({
    projectId: projectIdSchema,
    actions: z.array(reconcileActionSchema).max(1000)
  })
  .strict()

/** Parses with zod and converts failures into a typed domain error. */
export function parseInput<T extends z.ZodType>(schema: T, input: unknown): z.infer<T> {
  const result = schema.safeParse(input)
  if (result.success) return result.data
  const issues = result.error.issues.map((issue) => ({
    path: issue.path.join('.'),
    message: issue.message,
    code: issue.code
  }))
  const first = issues[0]
  throw appError(AppErrorCode.InvalidArgument, first === undefined ? 'invalid input' : `${first.path}: ${first.message}`, {
    details: { issues, field: first?.path }
  })
}

export { optionalReason }