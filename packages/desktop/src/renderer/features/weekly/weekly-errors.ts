/**
 * Weekly errors, in the user's language.
 *
 * The service returns a stable `code` plus a machine `details.reason`
 * Messages are Chinese and meant for logs; the UI branches on
 * the reason first, the code second, and shows the server message underneath
 * so an unexpected cause is still readable. Nothing here inspects prose.
 */

import { AppError, AppErrorCode } from '@researchos/shared'

export interface WeeklyErrorView {
  readonly code: string
  readonly message: string
  readonly labelKey: string
}

const REASON_KEYS: Readonly<Record<string, string>> = {
  'ai.unavailable': 'pages.weekly_review.err_ai_unavailable',
  'confirmation.required': 'pages.weekly_review.err_confirmation_required',
  'export.dir_missing': 'pages.weekly_review.err_export_dir_missing',
  'export.exists': 'pages.weekly_review.err_export_exists',
  'export.invalid_path': 'pages.weekly_review.err_invalid_export_path',
  'export.unsupported_format': 'pages.weekly_review.err_unsupported_format',
  'permission.invalid_actor': 'pages.weekly_review.err_invalid_actor',
  'project.archived': 'pages.weekly_review.err_project_archived',
  'project.not_found': 'pages.weekly_review.err_project_not_found',
  'report.confirmed_read_only': 'pages.weekly_review.err_confirmed_read_only',
  'report.content_conflict': 'pages.weekly_review.err_content_conflict',
  'report.content_too_large': 'pages.weekly_review.err_content_too_large',
  'report.invalid_period': 'pages.weekly_review.err_invalid_period',
  'report.not_draft': 'pages.weekly_review.err_confirmed_read_only',
  'report.not_empty': 'pages.weekly_review.err_report_not_empty',
  'report.not_found': 'pages.weekly_review.err_not_found',
  'report.period_occupied': 'pages.weekly_review.err_period_occupied',
  'report.unresolved_violations': 'pages.weekly_review.err_unresolved_violations'
}

const CODE_KEYS: Readonly<Record<string, string>> = {
  [AppErrorCode.AiUnavailable]: 'pages.weekly_review.err_ai_unavailable',
  [AppErrorCode.AiOutputInvalid]: 'pages.weekly_review.err_ai_output_invalid',
  [AppErrorCode.ArchivedReadOnly]: 'pages.weekly_review.err_project_archived',
  [AppErrorCode.Conflict]: 'pages.weekly_review.err_content_conflict',
  [AppErrorCode.ExternalUnavailable]: 'pages.weekly_review.err_external_unavailable',
  [AppErrorCode.InvalidArgument]: 'pages.weekly_review.err_invalid_input',
  [AppErrorCode.NotFound]: 'pages.weekly_review.err_not_found',
  [AppErrorCode.UserConfirmationRequired]: 'pages.weekly_review.err_confirmation_required'
}

export function errorReason(error: unknown): string | undefined {
  if (!(error instanceof AppError)) return undefined
  const reason = error.details?.['reason']
  return typeof reason === 'string' ? reason : undefined
}

export function weeklyErrorView(error: unknown): WeeklyErrorView {
  if (!(error instanceof AppError)) {
    const message = error instanceof Error ? error.message : String(error)
    return {
      code: AppErrorCode.Internal,
      message,
      labelKey: 'pages.weekly_review.err_internal'
    }
  }
  const reason = errorReason(error)
  const labelKey =
    (reason === undefined ? undefined : REASON_KEYS[reason]) ??
    CODE_KEYS[error.code] ??
    'pages.weekly_review.err_internal'
  return { code: error.code, message: error.message, labelKey }
}
