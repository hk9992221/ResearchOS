/**
 * Stable application error codes.
 *
 * The first block is the closed MCP tool-error set of the MCP spec (and
 * docs/mcp-tools-catalog.json policy.errorCodes). The rest transcribe the
 * sentinel families of internal/foundation/errs plus the AI, export/import and
 * job contracts that cross IPC. Codes are matched, never messages.
 */
export const AppErrorCode = {

  InvalidArgument: 'INVALID_ARGUMENT',
  NotFound: 'NOT_FOUND',
  Conflict: 'CONFLICT',
  ArchivedReadOnly: 'ARCHIVED_READ_ONLY',
  AccessModeReadOnly: 'ACCESS_MODE_READ_ONLY',
  CapabilityInvalid: 'CAPABILITY_INVALID',
  AiUnavailable: 'AI_UNAVAILABLE',
  ExternalUnavailable: 'EXTERNAL_UNAVAILABLE',
  LimitExceeded: 'LIMIT_EXCEEDED',
  RateLimited: 'RATE_LIMITED',
  Internal: 'INTERNAL',

  // foundation/errs: paths, workspace, migrations, config.
  PathEscape: 'PATH_ESCAPE',
  NotWorkspace: 'NOT_WORKSPACE',
  WorkspaceCorrupt: 'WORKSPACE_CORRUPT',
  DirectoryNotEmpty: 'DIRECTORY_NOT_EMPTY',
  MigrationTampered: 'MIGRATION_TAMPERED',
  SchemaTooNew: 'SCHEMA_TOO_NEW',
  InvalidSettingKey: 'INVALID_SETTING_KEY',
  ConfigCorrupt: 'CONFIG_CORRUPT',

  // foundation/errs: entities, files, destructive operations.
  EntityDeleted: 'ENTITY_DELETED',
  NoteFileMissing: 'NOTE_FILE_MISSING',
  RefNotFound: 'REF_NOT_FOUND',
  UserConfirmationRequired: 'USER_CONFIRMATION_REQUIRED',
  ConfirmationScopeMismatch: 'CONFIRMATION_SCOPE_MISMATCH',

  // foundation/errs: activity and relations registries.
  BadEventType: 'BAD_EVENT_TYPE',
  BadActor: 'BAD_ACTOR',
  BadEntityType: 'BAD_ENTITY_TYPE',
  BadEntityId: 'BAD_ENTITY_ID',
  BadPayloadShape: 'BAD_PAYLOAD_SHAPE',
  WrongEmitPath: 'WRONG_EMIT_PATH',
  RelationNotAllowed: 'RELATION_NOT_ALLOWED',
  SelfRelation: 'SELF_RELATION',

  // AI gateway and job lifecycle.
  SecretUnavailable: 'SECRET_UNAVAILABLE',
  AiOutputInvalid: 'AI_OUTPUT_INVALID',
  AiOutputTruncated: 'AI_OUTPUT_TRUNCATED',
  Interrupted: 'INTERRUPTED',

  // Export/import integrity and unsupported protocol capabilities.
  Integrity: 'INTEGRITY',
  Unsupported: 'UNSUPPORTED',

  // IPC boundary: sender is not the application main window.
  Forbidden: 'FORBIDDEN'
} as const

export type AppErrorCode = (typeof AppErrorCode)[keyof typeof AppErrorCode]

export interface AppErrorShape {
  code: AppErrorCode
  message: string
  details?: Record<string, unknown>
}

const KNOWN_CODES: Readonly<Record<string, true>> = Object.fromEntries(
  Object.values(AppErrorCode).map((code) => [code, true])
)

export function isAppErrorCode(value: string): value is AppErrorCode {
  return KNOWN_CODES[value] === true
}

export class AppError extends Error {
  readonly code: AppErrorCode
  readonly details?: Record<string, unknown>

  constructor(
    code: AppErrorCode,
    message: string,
    details?: Record<string, unknown>,
    options?: { cause?: unknown }
  ) {
    super(message, options)
    this.name = 'AppError'
    this.code = code
    this.details = details
  }

  toShape(): AppErrorShape {
    const shape: AppErrorShape = { code: this.code, message: this.message }
    if (this.details !== undefined) shape.details = this.details
    return shape
  }

  static from(error: unknown, fallbackMessage = 'Internal error'): AppError {
    if (error instanceof AppError) return error
    return new AppError(AppErrorCode.Internal, fallbackMessage, undefined, { cause: error })
  }
}

export function isAppError(value: unknown): value is AppError {
  return value instanceof AppError
}