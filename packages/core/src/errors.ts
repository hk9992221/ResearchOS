import { AppError, AppErrorCode, isAppError, type AppErrorShape } from '@researchos/shared'

/**
 * The shared contract owns the error taxonomy; the foundation layer only adds
 * the Go-sentinel convenience constructor. Codes are matched, never messages.
 */
export { AppError, AppErrorCode, isAppError }
export type SerializedAppError = AppErrorShape

export interface AppErrorOptions {
  cause?: unknown
  details?: Record<string, unknown>
  /** Additional fields are merged into `details` for call-site convenience. */
  [field: string]: unknown
}

/**
 * Builds a shared AppError. Keys other than `cause`/`details` are flattened
 * into `details`, so `appError(code, message, { field: 'protocol' })` and
 * `appError(code, message, { details: { field: 'protocol' } })` are equivalent.
 */
export function appError(code: AppErrorCode, message: string, options: AppErrorOptions = {}): AppError {
  const { cause, details, ...fields } = options
  const merged = { ...fields, ...(details ?? {}) }
  return new AppError(
    code,
    message,
    Object.keys(merged).length > 0 ? merged : undefined,
    cause === undefined ? undefined : { cause }
  )
}

/** Normalizes anything thrown into an AppError without losing the cause. */
export function asAppError(value: unknown, fallbackCode: AppErrorCode = AppErrorCode.Internal): AppError {
  if (isAppError(value)) return value
  const message = value instanceof Error ? value.message : String(value)
  return appError(fallbackCode, message, { cause: value })
}