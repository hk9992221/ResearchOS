import { AppError, AppErrorCode } from '@researchos/shared'

/**
 * Stable errors of the experiments module. Each maps to exactly
 * one shared `AppErrorCode`; MCP handlers never translate a known domain error
 * to `INTERNAL` (rewrite plan defect 1).
 */
export const ExperimentErrorCode = {
  NotFound: AppErrorCode.NotFound,
  InvalidInput: AppErrorCode.InvalidArgument,
  Conflict: AppErrorCode.Conflict,
  Archived: AppErrorCode.ArchivedReadOnly,
  DirUnavailable: AppErrorCode.InvalidArgument,
  DirGuard: AppErrorCode.InvalidArgument,
  ScanLimit: AppErrorCode.LimitExceeded,
  NoContent: AppErrorCode.InvalidArgument,
  AiUnavailable: AppErrorCode.AiUnavailable,
  AiOutputInvalid: AppErrorCode.AiOutputInvalid,
  AiOutputTruncated: AppErrorCode.AiOutputTruncated,
  SnapshotPersist: AppErrorCode.Internal,
  Unsupported: AppErrorCode.Unsupported
} as const

export function experimentError(
  code: (typeof ExperimentErrorCode)[keyof typeof ExperimentErrorCode],
  message: string,
  details?: Record<string, unknown>,
  cause?: unknown
): AppError {
  return new AppError(code, message, details, cause === undefined ? undefined : { cause })
}

export function notFound(message: string, details?: Record<string, unknown>): AppError {
  return experimentError(AppErrorCode.NotFound, message, details)
}

export function invalidInput(message: string, details?: Record<string, unknown>): AppError {
  return experimentError(AppErrorCode.InvalidArgument, message, details)
}

export function conflict(message: string, details?: Record<string, unknown>): AppError {
  return experimentError(AppErrorCode.Conflict, message, details)
}

export function dirUnavailable(dir: string, reason: string): AppError {
  return experimentError(AppErrorCode.InvalidArgument, `实验目录不可用：${dir}`, {
    reason: 'dir_unavailable',
    path: dir,
    message: reason
  })
}

export function dirGuard(dir: string, message: string): AppError {
  return experimentError(AppErrorCode.InvalidArgument, message, {
    reason: 'dir_guard',
    path: dir
  })
}

export function aiUnavailable(message: string): AppError {
  return experimentError(AppErrorCode.AiUnavailable, message)
}

/** Thrown when the AI draft was persisted but the snapshot could not be written. */
export class SnapshotPersistError extends AppError {
  readonly aiOutputId: string

  constructor(aiOutputId: string, cause: unknown) {
    super(
      AppErrorCode.Internal,
      `生成快照失败，AI 草稿已保留（${aiOutputId}）`,
      { reason: 'snapshot_persist', ai_output_id: aiOutputId },
      { cause }
    )
    this.name = 'SnapshotPersistError'
    this.aiOutputId = aiOutputId
  }
}