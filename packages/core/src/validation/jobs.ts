import { z } from 'zod'

import { AppErrorCode } from '../errors'
import { appError } from '../errors'

const jobId = z.string().min(1).max(64)

export const jobsListSchema = z
  .object({
    projectId: z.string().min(1).max(64).optional(),
    state: z.enum(['queued', 'running', 'succeeded', 'failed', 'cancelled']).optional(),
    limit: z.number().int().min(1).max(200).optional()
  })
  .strict()

export const jobsGetSchema = z.object({ jobId }).strict()

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

export const jobsValidators = {
  'jobs.list': validatorOf(jobsListSchema),
  'jobs.get': validatorOf(jobsGetSchema)
} as const