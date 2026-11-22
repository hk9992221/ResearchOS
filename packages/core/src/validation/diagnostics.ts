import { z } from 'zod'

import { AppErrorCode } from '../errors'
import { appError } from '../errors'

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

const noPayload = { parse: () => undefined }

export const diagnosticsValidators = {
  'diagnostics.integrity': validatorOf(
    z
      .object({
        projectIds: z.array(z.string().min(1).max(64)).max(100).optional(),
        includeFileScan: z.boolean().optional()
      })
      .strict()
  ),
  'diagnostics.storage': noPayload,
  'diagnostics.externalRefs': validatorOf(
    z.object({ projectId: z.string().min(1).max(64), refIds: z.array(z.string().min(1).max(64)).max(500).optional() }).strict()
  )
} as const
