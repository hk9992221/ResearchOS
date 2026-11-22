import { AppError, type SerializedAppError, appError, asAppError } from './errors'

/**
 * Uniform success/failure envelope used by services and the IPC router.
 * Throwing across a process boundary loses identity, so failures travel as
 * serialized values and are re-thrown at the edge.
 */
export type Result<T> = { readonly ok: true; readonly value: T } | { readonly ok: false; readonly error: SerializedAppError }

export function ok<T>(value: T): Result<T> {
  return { ok: true, value }
}

export function fail<T = never>(error: AppError | SerializedAppError): Result<T> {
  return { ok: false, error: error instanceof AppError ? error.toShape() : error }
}

export function isOk<T>(result: Result<T>): result is { readonly ok: true; readonly value: T } {
  return result.ok
}

/** Returns the value or throws the deserialized AppError. */
export function unwrap<T>(result: Result<T>): T {
  if (result.ok) return result.value
  throw appError(result.error.code, result.error.message, { details: result.error.details })
}

/** Runs `fn`, converting a thrown error into a failure envelope. */
export function fromTry<T>(fn: () => T): Result<T> {
  try {
    return ok(fn())
  } catch (error) {
    return fail(asAppError(error))
  }
}

export function mapResult<T, U>(result: Result<T>, fn: (value: T) => U): Result<U> {
  return result.ok ? ok(fn(result.value)) : result
}