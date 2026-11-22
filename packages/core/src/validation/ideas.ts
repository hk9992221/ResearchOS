/**
 * Zod validation for the `ideas.*` IPC commands (plan §3.5). Schemas are the
 * only input gate at the IPC boundary; the service re-checks rune budgets
 * because zod counts UTF-16 units, not code points.
 */
import { z } from 'zod'

import type { CommandRequest, CommandValidator } from '@researchos/shared'

import { IDEA_LIMITS } from '../modules/ideas/limits'

const idSchema = z.uuidv7()
const timestampSchema = z.string().regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/, 'expected an RFC3339 UTC timestamp')

export const ideaStatusSchema = z.enum([
  'New',
  'Researching',
  'Validating',
  'Supported',
  'PartiallySupported',
  'Inconclusive',
  'Rejected',
  'OnHold'
])

export const evidenceDirectionSchema = z.enum(['support', 'oppose'])
export const evidenceSourceTypeSchema = z.enum(['experiment', 'run', 'note', 'paper'])
export const extractionSourceTypeSchema = z.enum(['experiment', 'run', 'note'])
export const ideaLinkTargetTypeSchema = z.enum(['paper', 'note', 'experiment'])
export const ideaHistoryKindSchema = z.enum([
  'created',
  'status',
  'relation',
  'evidence',
  'deleted',
  'restored',
  'ai',
  'derived'
])

const locatorSchema = z
  .strictObject({
    pageNo: z.number().int().positive().optional(),
    blockNo: z.number().int().nonnegative().optional(),
    charStart: z.number().int().nonnegative().optional(),
    charEnd: z.number().int().nonnegative().optional(),
    heading: z.string().max(500).optional(),
    fieldId: z.string().optional(),
    rects: z
      .array(z.strictObject({ x: z.number(), y: z.number(), width: z.number(), height: z.number() }))
      .max(200)
      .optional()
  })
  .optional()

const titleSchema = z.string().trim().min(1).max(IDEA_LIMITS.titleRunes)
const descriptionSchema = z.string().max(IDEA_LIMITS.descriptionRunes)
const rejectionReasonSchema = z.string().max(IDEA_LIMITS.rejectionReasonRunes)
const evidenceContentSchema = z.string().trim().min(1).max(IDEA_LIMITS.evidenceContentRunes)
const limitSchema = z.number().int().min(1).max(200)
const cursorSchema = z.string().min(1).max(1024)

const projectId = { projectId: idSchema }
const pageQuery = { limit: limitSchema.optional(), cursor: cursorSchema.optional() }

const listSchema = z.strictObject({
  ...projectId,
  status: z.array(ideaStatusSchema).max(8).optional(),
  q: z.string().max(200).optional(),
  includeDeleted: z.boolean().optional(),
  ...pageQuery
})

const getSchema = z.strictObject({ ...projectId, ideaId: idSchema })

const createSchema = z.strictObject({
  ...projectId,
  title: titleSchema,
  description: descriptionSchema.optional()
})

const updateSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  patch: z
    .strictObject({ title: titleSchema.optional(), description: descriptionSchema.optional() })
    .refine((patch) => patch.title !== undefined || patch.description !== undefined, {
      message: 'patch must change title or description'
    }),
  expectedUpdatedAt: timestampSchema.optional()
})

const changeStatusSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  status: ideaStatusSchema,
  rejectionReason: rejectionReasonSchema.optional(),
  note: z.string().max(2_000).optional(),
  expectedUpdatedAt: timestampSchema.optional()
})

const deleteSchema = z.strictObject({ ...projectId, ideaId: idSchema })
const restoreSchema = z.strictObject({ ...projectId, ideaId: idSchema })

const historySchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  kinds: z.array(ideaHistoryKindSchema).max(8).optional(),
  ...pageQuery
})

const deriveSchema = z.strictObject({
  ...projectId,
  parentIdeaId: idSchema,
  title: titleSchema,
  description: descriptionSchema.optional()
})

const evidenceListSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema.optional(),
  direction: evidenceDirectionSchema.optional(),
  confirmed: z.boolean().optional(),
  includeDeleted: z.boolean().optional(),
  ...pageQuery
})

const evidenceCreateSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  direction: evidenceDirectionSchema,
  content: evidenceContentSchema,
  source: z.strictObject({
    type: evidenceSourceTypeSchema,
    id: idSchema,
    locator: locatorSchema
  })
})

const evidenceUpdateSchema = z.strictObject({
  ...projectId,
  evidenceId: idSchema,
  patch: z
    .strictObject({ direction: evidenceDirectionSchema.optional(), content: evidenceContentSchema.optional() })
    .refine((patch) => patch.direction !== undefined || patch.content !== undefined, {
      message: 'patch must change direction or content'
    }),
  expectedUpdatedAt: timestampSchema.optional()
})

const evidenceDeleteSchema = z.strictObject({ ...projectId, evidenceId: idSchema })
const evidenceConfirmSchema = z.strictObject({ ...projectId, evidenceId: idSchema })
const evidenceDiscardSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  evidenceId: idSchema
})

const extractionSourceRefSchema = z.strictObject({
  type: extractionSourceTypeSchema,
  id: idSchema
})

const extractSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  sourceRefs: z.array(extractionSourceRefSchema).min(1).max(IDEA_LIMITS.maxDraftSourceRefs),
  instruction: z.string().max(IDEA_LIMITS.instructionRunes).optional(),
  maxDrafts: z.number().int().min(1).max(IDEA_LIMITS.maxDrafts).optional()
})

const extractPreviewSchema = z.strictObject({ jobId: idSchema })

const linkSchema = z.strictObject({
  ...projectId,
  ideaId: idSchema,
  target: z.strictObject({ type: ideaLinkTargetTypeSchema, id: idSchema })
})

/**
 * One validator per `ideas.*` channel. The mapped type keeps every channel in
 * `CommandMap` covered and makes a stale key a compile error.
 */
export const ideasCommandValidators: { [C in IdeasChannel]: CommandValidator<CommandRequest<C>> } = {
  'ideas.list': listSchema,
  'ideas.get': getSchema,
  'ideas.create': createSchema,
  'ideas.update': updateSchema,
  'ideas.changeStatus': changeStatusSchema,
  'ideas.delete': deleteSchema,
  'ideas.restore': restoreSchema,
  'ideas.history': historySchema,
  'ideas.derive': deriveSchema,
  'ideas.evidence.list': evidenceListSchema,
  'ideas.evidence.create': evidenceCreateSchema,
  'ideas.evidence.update': evidenceUpdateSchema,
  'ideas.evidence.delete': evidenceDeleteSchema,
  'ideas.evidence.confirm': evidenceConfirmSchema,
  'ideas.evidence.discard': evidenceDiscardSchema,
  'ideas.evidence.extract': extractSchema,
  'ideas.evidence.extractPreview': extractPreviewSchema,
  'ideas.links.add': linkSchema,
  'ideas.links.remove': linkSchema
}

type IdeasChannel =
  | 'ideas.list'
  | 'ideas.get'
  | 'ideas.create'
  | 'ideas.update'
  | 'ideas.changeStatus'
  | 'ideas.delete'
  | 'ideas.restore'
  | 'ideas.history'
  | 'ideas.derive'
  | 'ideas.evidence.list'
  | 'ideas.evidence.create'
  | 'ideas.evidence.update'
  | 'ideas.evidence.delete'
  | 'ideas.evidence.confirm'
  | 'ideas.evidence.discard'
  | 'ideas.evidence.extract'
  | 'ideas.evidence.extractPreview'
  | 'ideas.links.add'
  | 'ideas.links.remove'

export type IdeaIpcChannel = IdeasChannel