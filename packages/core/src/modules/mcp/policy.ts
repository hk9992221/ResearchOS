/**
 * Tool policy: access mode and the archived-project gate
 * (rewrite plan §3.6).
 *
 * Read-only mode is enforced by *not registering* write tools, exactly like the
 * Go host's per-request provider filter: the tool is absent from `tools/list`
 * and unknown at `tools/call`. The host still checks the category before
 * dispatch (defense in depth) so a stale list can never mutate state.
 */
import { AppErrorCode, type AppErrorCode as ErrorCode } from '@researchos/shared'

import { appError } from '../../errors'
import { compareTools } from './tools'
import type { AccessMode, ToolDefinition } from './types'

export const ACCESS_MODES: readonly AccessMode[] = ['read-only', 'read-write']

/** Parses `mcp.access_mode`; anything else is an invalid setting. */
export function parseAccessMode(raw: string | undefined): AccessMode {
  if (raw === undefined || raw === '') return 'read-write'
  // Workspaces initialized by the Go engine stored the underscore spelling.
  const value = raw === 'read_only' ? 'read-only' : raw === 'read_write' ? 'read-write' : raw
  if (value === 'read-only' || value === 'read-write') return value
  throw appError(AppErrorCode.InvalidSettingKey, `mcp.access_mode 取值必须是 read-only 或 read-write`, {
    details: { value: raw }
  })
}

/** True when the tool may mutate application state. */
export function isMutating(definition: ToolDefinition<never> | ToolDefinition): boolean {
  switch (definition.category) {
    case 'R':
      return false
    case 'J':
      return definition.annotations.readOnlyHint !== true
    case 'W':
    case 'C':
      return true
  }
}

/**
 * The per-request registration set. Ordering is deterministic so `tools/list`
 * is stable: category (R, J, W, C), then name.
 */
export function toolsForMode(
  tools: readonly ToolDefinition[],
  mode: AccessMode
): readonly ToolDefinition[] {
  const visible = mode === 'read-write' ? tools : tools.filter((tool) => !isMutating(tool))
  return [...visible].sort(compareTools)
}

/** Defense in depth: rejects a mutating call that slipped past the list. */
export function assertModeAllows(definition: ToolDefinition<never> | ToolDefinition, mode: AccessMode): void {
  if (mode === 'read-only' && isMutating(definition)) {
    throw appError(
      AppErrorCode.AccessModeReadOnly,
      `工具 ${definition.name} 在只读模式下不可用；请在设置中切换为读写模式`,
      { details: { tool: definition.name, access_mode: mode } }
    )
  }
}

/**
 * Tools that may write while the project is archived: removing/archiving a
 * project is the way out of the archived state, so the gate cannot apply to it.
 */
export const ARCHIVED_GATE_EXEMPT: Readonly<Record<string, true>> = {
  project_unarchive: true,
  project_delete: true,
  // Produces a read-only bundle; the catalog marks it allowArchived.
  project_export: true
}

export function isArchivedGateExempt(toolName: string): boolean {
  return ARCHIVED_GATE_EXEMPT[toolName] === true
}

/**
 * Applies the archived gate before a tool runs. `writes` is the tool's policy
 * decision, `status` the project's current status. Delete is a confirmation
 * tool and unarchive is the documented escape hatch.
 */
export function assertProjectWritable(
  toolName: string,
  status: 'active' | 'archived',
  writes: boolean
): void {
  if (!writes || status === 'active' || isArchivedGateExempt(toolName)) return
  throw appError(AppErrorCode.ArchivedReadOnly, '项目已归档，写入前请先解除归档', {
    details: { tool: toolName, project_status: status }
  })
}

/**
 * The policy error used by the host when a tool is absent from the current
 * mode. It is only reachable through a stale client cache; the registry lookup
 * normally reports the tool as unknown.
 */
export function accessModeError(toolName: string): ErrorCode {
  void toolName
  return AppErrorCode.AccessModeReadOnly
}