import type { PluginCommandView, PluginContributions, PluginPermission } from '@researchos/shared'

import { contributionScopes, scopesSatisfied } from './permissions'

interface RegisteredCommand {
  id: string
  title: string
  pluginId: string
  required: readonly PluginPermission[]
}

interface PluginCommands {
  commands: RegisteredCommand[]
  granted: readonly PluginPermission[]
}

/**
 * Host-owned command table. A contribution is active only while the plugin is
 * enabled and its required scopes are still granted; revoking a scope removes
 * the dependent commands without stopping the plugin's runtime. Re-granting a
 * scope makes the same command visible again.
 */
export class PluginCommandRegistry {
  private readonly byPlugin = new Map<string, PluginCommands>()

  register(
    pluginId: string,
    contributions: PluginContributions,
    declared: readonly PluginPermission[],
    granted: readonly PluginPermission[]
  ): void {
    const commands: RegisteredCommand[] = []
    for (const command of contributions.commands ?? []) {
      commands.push({
        id: command.id,
        title: command.title,
        pluginId,
        required: contributionScopes(declared, command.permissions)
      })
    }
    for (const editor of contributions.editor ?? []) {
      // Editor entries execute an existing command; registering the same id
      // twice would make `plugins.commands.list` ambiguous.
      if (commands.some((command) => command.id === editor.command)) continue
      commands.push({
        id: editor.command,
        title: editor.title,
        pluginId,
        required: contributionScopes(declared, editor.permissions)
      })
    }
    this.byPlugin.set(pluginId, { commands, granted: [...granted] })
  }

  unregister(pluginId: string): void {
    this.byPlugin.delete(pluginId)
  }

  /** Recomputes the active set after grants change; returns newly blocked ids. */
  reconcile(pluginId: string, granted: readonly PluginPermission[]): string[] {
    const entry = this.byPlugin.get(pluginId)
    if (entry === undefined) return []
    const before = new Set(
      entry.commands
        .filter((command) => scopesSatisfied(command.required, entry.granted))
        .map((command) => command.id)
    )
    entry.granted = [...granted]
    const after = new Set(
      entry.commands
        .filter((command) => scopesSatisfied(command.required, entry.granted))
        .map((command) => command.id)
    )
    return [...before].filter((id) => !after.has(id))
  }

  list(): PluginCommandView[] {
    const out: PluginCommandView[] = []
    for (const entry of this.byPlugin.values()) {
      for (const command of entry.commands) {
        if (!scopesSatisfied(command.required, entry.granted)) continue
        out.push({ id: command.id, title: command.title, pluginId: command.pluginId })
      }
    }
    return out.sort((left, right) => (left.id < right.id ? -1 : left.id > right.id ? 1 : 0))
  }

  /** Commands contributed by one plugin, for diagnostics and tests. */
  forPlugin(pluginId: string): PluginCommandView[] {
    const entry = this.byPlugin.get(pluginId)
    if (entry === undefined) return []
    return entry.commands
      .filter((command) => scopesSatisfied(command.required, entry.granted))
      .map((command) => ({ id: command.id, title: command.title, pluginId: command.pluginId }))
  }
}