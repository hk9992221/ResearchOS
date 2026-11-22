import { z } from 'zod'

/**
 * IPC input schemas for the experiments commands (rewrite plan §3.5). The
 * router parses with these before dispatch, so a missing or extra field is
 * `INVALID_ARGUMENT` and the handler never sees a half-shaped request.
 */

export const scanRulesSchema = z
  .object({
    include: z.array(z.string()).optional(),
    exclude: z.array(z.string()).optional(),
    maxFileBytes: z.number().int().positive().optional(),
    maxTotalBytes: z.number().int().positive().optional(),
    maxFiles: z.number().int().positive().optional(),
    hashMaxBytes: z.number().int().positive().optional(),
    followSymlinks: z.boolean().optional(),
    includeImagesInAi: z.boolean().optional()
  })
  .strict()

export const targetRefSchema = z
  .object({
    kind: z.enum(['experiment', 'run']),
    id: z.string().min(1)
  })
  .strict()

const paramValueSchema = z
  .object({
    value: z.string(),
    source: z
      .object({
        path: z.string().min(1),
        pointer: z.string().nullable().optional(),
        sha256: z.string().nullable().optional()
      })
      .strict()
      .nullable()
      .optional(),
    confirmed: z.boolean().optional()
  })
  .strict()

export const runParamsSchema = z.record(z.string().min(1), paramValueSchema)

const expectedUpdatedAtSchema = z.string().optional()

export const experimentListSchema = z
  .object({
    projectId: z.string().min(1),
    q: z.string().optional(),
    includeDeleted: z.boolean().optional(),
    limit: z.number().int().min(1).max(500).optional(),
    offset: z.number().int().min(0).optional()
  })
  .strict()

export const experimentIdSchema = z
  .object({
    projectId: z.string().min(1),
    experimentId: z.string().min(1)
  })
  .strict()

export const experimentCreateSchema = z
  .object({
    projectId: z.string().min(1),
    title: z.string().min(1).max(200),
    purpose: z.string().optional(),
    dirRef: z.string().min(1).optional(),
    scanRules: scanRulesSchema.optional()
  })
  .strict()

export const experimentUpdateSchema = z
  .object({
    projectId: z.string().min(1),
    experimentId: z.string().min(1),
    patch: z
      .object({
        title: z.string().min(1).max(200).optional(),
        purpose: z.string().optional(),
        dirRef: z.string().nullable().optional(),
        scanRules: scanRulesSchema.optional()
      })
      .strict(),
    expectedUpdatedAt: expectedUpdatedAtSchema
  })
  .strict()

export const scanPreviewSchema = z
  .object({
    projectId: z.string().min(1),
    target: targetRefSchema,
    override: scanRulesSchema.optional()
  })
  .strict()

export const scanStartSchema = scanPreviewSchema

export const runListSchema = experimentIdSchema

export const runCreateSchema = z
  .object({
    projectId: z.string().min(1),
    experimentId: z.string().min(1),
    title: z.string().min(1).max(200).optional(),
    dirRef: z.string().nullable().optional(),
    params: runParamsSchema.optional(),
    status: z.enum(['unknown', 'running', 'finished', 'failed']).optional()
  })
  .strict()

export const runUpdateSchema = z
  .object({
    projectId: z.string().min(1),
    runId: z.string().min(1),
    patch: z
      .object({
        title: z.string().min(1).max(200).optional(),
        dirRef: z.string().nullable().optional(),
        params: runParamsSchema.optional(),
        status: z.enum(['unknown', 'running', 'finished', 'failed']).optional(),
        ack: z.boolean().optional()
      })
      .strict(),
    expectedUpdatedAt: expectedUpdatedAtSchema
  })
  .strict()

export const runIdSchema = z
  .object({
    projectId: z.string().min(1),
    runId: z.string().min(1)
  })
  .strict()

export const snapshotListSchema = z
  .object({
    projectId: z.string().min(1),
    target: targetRefSchema,
    kind: z.enum(['outline', 'results']).optional(),
    limit: z.number().int().min(1).max(500).optional(),
    offset: z.number().int().min(0).optional()
  })
  .strict()

export const snapshotGetSchema = z
  .object({
    projectId: z.string().min(1),
    snapshotId: z.string().min(1)
  })
  .strict()

export const snapshotCreateSchema = z
  .object({
    projectId: z.string().min(1),
    target: targetRefSchema,
    metrics: z.record(z.string(), z.unknown()).optional(),
    aiAnalysis: z.string().optional(),
    aiOutputId: z.string().optional(),
    kind: z.enum(['outline', 'results']).optional()
  })
  .strict()

export const metricsExtractSchema = z
  .object({
    projectId: z.string().min(1),
    target: targetRefSchema
  })
  .strict()

export const gitInfoSchema = metricsExtractSchema

export const compareSchema = z
  .object({
    projectId: z.string().min(1),
    experimentId: z.string().min(1).optional(),
    runIds: z.array(z.string().min(1)).optional(),
    baselineId: z.string().min(1).optional(),
    diffParams: z.boolean().optional(),
    directions: z.record(z.string(), z.enum(['higher', 'lower'])).optional(),
    table: z
      .object({
        format: z.enum(['latex_booktabs', 'markdown', 'csv']).optional(),
        caption: z.string().optional(),
        label: z.string().optional(),
        includeParams: z.array(z.string()).optional(),
        includeMetrics: z.array(z.string()).optional(),
        precision: z.number().int().min(0).max(10).optional(),
        highlightBest: z.boolean().optional(),
        showDelta: z.boolean().optional()
      })
      .strict()
      .optional()
  })
  .strict()

export const aiOutlineSchema = z
  .object({
    projectId: z.string().min(1),
    experimentId: z.string().min(1),
    extraInstructions: z.string().optional()
  })
  .strict()

export const aiResultsSchema = z
  .object({
    projectId: z.string().min(1),
    target: targetRefSchema,
    extraInstructions: z.string().optional()
  })
  .strict()