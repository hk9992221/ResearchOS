/**
 * Stable plugin error identifiers. They are deliberately not new AppError
 * codes: the closed application taxonomy (shared/errors.ts) already describes
 * these failures, and tests branch on the standard codes.
 */
export const PluginErrorCode = {
  ManifestInvalid: 'PLUGIN_MANIFEST_INVALID',
  InstallConflict: 'PLUGIN_INSTALL_CONFLICT',
  PermissionRequired: 'PLUGIN_PERMISSION_REQUIRED',
  RuntimeTimeout: 'PLUGIN_RUNTIME_TIMEOUT',
  RuntimeCrashed: 'PLUGIN_RUNTIME_CRASHED',
  PathEscape: 'PLUGIN_PATH_ESCAPE'
} as const

export type PluginErrorCode = (typeof PluginErrorCode)[keyof typeof PluginErrorCode]