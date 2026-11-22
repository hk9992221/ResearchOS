/**
 * Maps Node filesystem errno values to the export/import error taxonomy
 * (rewrite plan §4.11) so a permission or disk failure never surfaces as a
 * vague INTERNAL (defect 8 class).
 */
import { AppErrorCode } from '@researchos/shared'

import { appError, isAppError } from '../../errors'

const UNAVAILABLE_CODES: Readonly<Record<string, true>> = {
  EACCES: true,
  EPERM: true,
  EROFS: true,
  ENOSPC: true,
  EMFILE: true,
  ENFILE: true,
  EDQUOT: true
}

/** Converts a thrown value into a typed AppError, preserving an AppError. */
export function ioError(error: unknown, message: string, details: Record<string, unknown> = {}): Error {
  if (isAppError(error)) return error
  const code = typeof error === 'object' && error !== null ? (error as { code?: unknown }).code : undefined
  const cause = error instanceof Error ? error.message : String(error)
  if (typeof code === 'string') {
    if (UNAVAILABLE_CODES[code] === true) {
      return appError(AppErrorCode.ExternalUnavailable, message, { ...details, fs_code: code, cause })
    }
    if (code === 'ENOENT') {
      return appError(AppErrorCode.NotFound, message, { ...details, fs_code: code, cause })
    }
  }
  return appError(AppErrorCode.Internal, message, { ...details, cause })
}
