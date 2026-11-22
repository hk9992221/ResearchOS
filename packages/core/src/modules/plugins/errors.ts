import { AppErrorCode, appError } from '../../errors'

/**
 * Plugin-subsystem errors reuse the closed application taxonomy. Codes are what
 * callers branch on; the details carry the offending field/file.
 */
export function invalidManifest(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.InvalidArgument, message, { reason: 'manifest', ...details })
}

export function pluginNotFound(pluginId: string): never {
  throw appError(AppErrorCode.NotFound, `plugin ${pluginId} is not installed`, { pluginId })
}

export function pluginConflict(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.Conflict, message, details)
}

export function permissionDenied(pluginId: string, permission: string, method?: string): never {
  throw appError(AppErrorCode.Forbidden, `plugin ${pluginId} lacks ${permission}`, {
    pluginId,
    permission,
    ...(method === undefined ? {} : { method })
  })
}

export function unsupportedSource(message: string, details: Record<string, unknown> = {}): never {
  throw appError(AppErrorCode.Unsupported, message, { reason: 'plugin_source', ...details })
}