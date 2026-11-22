import { z } from 'zod'

import type { CommandValidator } from '@researchos/shared'

/**
 * Input validation of the search commands and the MCP tool arguments. Shared
 * contracts are dependency-free, so runtime schemas live here.
 */

const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

const entityId = z.string().regex(UUID_V7, 'expected a lowercase UUIDv7')
const scopeKind = z.enum(['project', 'global'])
const rerankPolicy = z.union([z.enum(['auto', 'off', 'force']), z.boolean()])
const sourceKind = z.enum([
  'paper_metadata',
  'paper_fulltext',
  'paper_note',
  'scratchpad',
  'db_record',
  'idea',
  'experiment_outline',
  'experiment_results'
])
const entityKind = z.enum(['paper', 'note', 'db_record', 'idea', 'experiment'])

const isoMillis = z
  .string()
  .regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, 'expected a UTC millisecond timestamp')

const filters = z
  .object({
    sources: z.array(sourceKind).max(16).optional(),
    entityTypes: z.array(entityKind).max(8).optional(),
    yearMin: z.number().int().min(1000).max(2999).optional(),
    yearMax: z.number().int().min(1000).max(2999).optional(),
    authors: z.array(z.string().min(1).max(200)).max(50).optional(),
    paperIds: z.array(entityId).max(200).optional(),
    updatedAfter: isoMillis.optional()
  })
  .strict()

function scopePair(value: { scope?: 'project' | 'global'; projectId?: string }, context: z.RefinementCtx): void {
  if (value.scope === 'project' && (value.projectId === undefined || value.projectId === '')) {
    context.addIssue({ code: 'custom', path: ['projectId'], message: 'project scope requires projectId' })
  }
  if (value.scope === 'global' && value.projectId !== undefined && value.projectId !== '') {
    context.addIssue({ code: 'custom', path: ['projectId'], message: 'global scope does not accept projectId' })
  }
}

export const searchQuerySchema = z
  .object({
    scope: scopeKind,
    projectId: entityId.optional(),
    query: z
      .string()
      .refine((value) => [...value].length <= 512, 'query is longer than 512 characters'),
    kinds: z.array(entityKind).max(8).optional(),
    filters: filters.optional(),
    limit: z.number().int().min(1).max(100).optional(),
    offset: z.number().int().min(0).max(100_000).optional(),
    rerank: rerankPolicy.optional(),
    includeDeleted: z.boolean().optional(),
    embeddingProfileId: z.string().min(1).max(200).optional()
  })
  .strict()
  .superRefine(scopePair)

export const searchIndexStatusSchema = z
  .object({
    scope: scopeKind.optional(),
    projectId: entityId.optional()
  })
  .strict()
  .superRefine((value, context) => {
    // A bare projectId is the IPC shorthand; an explicit scope must agree.
    if (value.scope === 'project' && value.projectId === undefined) {
      context.addIssue({ code: 'custom', path: ['projectId'], message: 'project scope requires projectId' })
    }
    if (value.projectId !== undefined && value.scope === 'global') {
      context.addIssue({ code: 'custom', path: ['scope'], message: 'global scope does not accept projectId' })
    }
  })

export const searchReindexSchema = z
  .object({
    scope: scopeKind,
    projectId: entityId.optional(),
    reparsePdf: z.boolean().optional(),
    rebuildVector: z.boolean().optional()
  })
  .strict()
  .superRefine(scopePair)

export const searchCancelSchema = z.object({ jobId: z.string().min(1).max(64) }).strict()

/** Command-validator adapter for the IPC router. */
export function zodValidator<T>(schema: z.ZodType<T>): CommandValidator<T> {
  return {
    parse(input: unknown): T {
      return schema.parse(input)
    }
  }
}

export const searchQueryValidator = zodValidator(searchQuerySchema)
export const searchIndexStatusValidator = zodValidator(searchIndexStatusSchema)
export const searchReindexValidator = zodValidator(searchReindexSchema)
export const searchCancelValidator = zodValidator(searchCancelSchema)

export type SearchQueryInput = z.infer<typeof searchQuerySchema>
export type SearchIndexStatusInput = z.infer<typeof searchIndexStatusSchema>
export type SearchReindexInput = z.infer<typeof searchReindexSchema>
export type SearchCancelInput = z.infer<typeof searchCancelSchema>
