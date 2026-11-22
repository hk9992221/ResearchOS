/**
 * Typed domain errors of the Ideas module. The stable `reason`
 * string is the Go error code; the `AppErrorCode` is the coarse MCP/IPC code
 * the taxonomy allows. Nothing in this module maps a known domain error to
 * `INTERNAL` (plan §5 defect 1).
 */
import { AppError, AppErrorCode, isAppError } from '../../errors'

export const IDEA_ERROR = {
  ideaNotFound: 'idea.not_found',
  ideaDeleted: 'idea.deleted',
  evidenceNotFound: 'evidence.not_found',
  evidenceDeleted: 'evidence.deleted',
  evidenceConfirmed: 'evidence.confirmed',
  evidenceDirectionLocked: 'evidence.direction_locked',
  evidenceNotAIDraft: 'evidence.not_ai_draft',
  invalidStatus: 'idea.invalid_status',
  rejectionReasonRequired: 'idea.rejection_reason_required',
  notRejected: 'idea.not_rejected',
  invalidSource: 'evidence.invalid_source',
  sourceUnavailable: 'evidence.source_unavailable',
  sourceEmpty: 'evidence.source_empty',
  projectReadOnly: 'project.read_only',
  projectNotFound: 'project.not_found',
  invalidActor: 'permission.invalid_actor',
  aiDisabled: 'ai.disabled',
  aiUnavailable: 'ai.unavailable',
  aiOutputInvalid: 'ai.output_invalid',
  confirmationRequired: 'confirmation.required',
  lineageTooDeep: 'idea.lineage_too_deep',
  invalidInput: 'input.invalid',
  targetNotFound: 'relation.target_not_found',
  targetDeleted: 'relation.target_deleted',
  jobUnavailable: 'job.unavailable',
  jobNotFound: 'job.not_found',
  stale: 'idea.stale'
} as const

export type IdeaErrorReason = (typeof IDEA_ERROR)[keyof typeof IDEA_ERROR]

/** Builds an AppError carrying the stable reason plus caller details. */
export function ideaError(
  reason: string,
  code: AppErrorCode,
  message: string,
  details?: Record<string, unknown>
): AppError {
  return new AppError(code, message, { reason, ...details })
}

export function invalidInput(message: string, details?: Record<string, unknown>): AppError {
  return ideaError(IDEA_ERROR.invalidInput, AppErrorCode.InvalidArgument, message, details)
}

export function ideaNotFound(message: string, details?: Record<string, unknown>): AppError {
  return ideaError(IDEA_ERROR.ideaNotFound, AppErrorCode.NotFound, message, details)
}

export function ideaDeleted(message: string, details?: Record<string, unknown>): AppError {
  return ideaError(IDEA_ERROR.ideaDeleted, AppErrorCode.EntityDeleted, message, details)
}

export function evidenceNotFound(message: string, details?: Record<string, unknown>): AppError {
  return ideaError(IDEA_ERROR.evidenceNotFound, AppErrorCode.NotFound, message, details)
}

export function evidenceDeleted(message: string, details?: Record<string, unknown>): AppError {
  return ideaError(IDEA_ERROR.evidenceDeleted, AppErrorCode.EntityDeleted, message, details)
}

export function rejectionReasonRequired(): AppError {
  return ideaError(
    IDEA_ERROR.rejectionReasonRequired,
    AppErrorCode.InvalidArgument,
    'a rejection reason is required'
  )
}

/**
 * Re-throws a known `AppError` unchanged and wraps anything else in the given
 * code. Used at the AI boundary so gateway failures keep their typed code
 * (`AI_UNAVAILABLE`, `AI_OUTPUT_TRUNCATED`, …) instead of collapsing.
 */
export function typedError(error: unknown, code: AppErrorCode, reason: string): AppError {
  if (isAppError(error)) return error
  const message = error instanceof Error ? error.message : String(error)
  return ideaError(reason, code, message)
}