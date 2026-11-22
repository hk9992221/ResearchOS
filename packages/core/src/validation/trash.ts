import { z } from 'zod'

import { AppErrorCode } from '../errors'
import { appError } from '../errors'
import { TRASH_ENTITY_TYPES } from '@researchos/shared'

const projectId = z.string().min(1).max(64)
const ref = z
  .object({
    type: z.enum(TRASH_ENTITY_TYPES),
    id: z.string().min(1).max(64)
  })
  .strict()

const token = z.object({ token: z.string().min(1).max(200) }).strict()

function validatorOf<T extends z.ZodType>(schema: T): { parse(input: unknown): z.infer<T> } {
  return {
    parse(input: unknown): z.infer<T> {
      const result = schema.safeParse(input)
      if (result.success) return result.data
      const issue = result.error.issues[0]
      throw appError(AppErrorCode.InvalidArgument, issue === undefined ? 'invalid input' : issue.message, {
        details: { path: issue?.path.join('.') ?? '' }
      })
    }
  }
}

export const trashValidators = {
  'trash.list': validatorOf(z.object({ projectId }).strict()),
  'trash.restore': validatorOf(
    z.object({ projectId, items: z.array(ref).min(1).max(500) }).strict()
  ),
  'trash.purge.plan': validatorOf(
    z.object({ projectId, items: z.array(ref).min(1).max(500) }).strict()
  ),
  'trash.purge.confirm': validatorOf(token),
  'trash.empty.plan': validatorOf(z.object({ projectId }).strict()),
  'trash.empty.confirm': validatorOf(token)
} as const