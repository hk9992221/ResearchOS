import type { PluginPermission } from '@researchos/shared'

import type {
  PluginPromptPresenter,
  PluginPromptRequest
} from '@researchos/core/modules/plugins/index'

/**
 * Renderer-backed presenter for `ui.prompt` / `ui.confirm`. The
 * dialog request is broadcast to the single app window; the reply comes back
 * as the `plugins.prompt.respond` command. The plugin badge data is supplied
 * by the host, never by the plugin.
 */

export interface PluginPromptHostOptions {
  publishRequest(request: {
    requestId: string
    pluginId: string
    pluginName: string
    kind: 'prompt' | 'confirm'
    params: PluginPromptRequest['params']
    permissions: PluginPermission[]
  }): void
  publishCancel(requestId: string): void
  permissionsFor(pluginId: string): PluginPermission[]
  nameFor(pluginId: string): string
}

interface Pending {
  resolve(result: { submitted?: boolean; confirmed?: boolean; value?: string }): void
}

export class PluginPromptHost {
  private readonly pending = new Map<string, Pending>()
  private readonly options: PluginPromptHostOptions

  constructor(options: PluginPromptHostOptions) {
    this.options = options
  }

  readonly presenter: PluginPromptPresenter = {
    present: async (request) => {
      const answer = await new Promise<{
        submitted?: boolean
        confirmed?: boolean
        value?: string
      }>((resolve) => {
        this.pending.set(request.requestId, { resolve })
        try {
          this.options.publishRequest({
            requestId: request.requestId,
            pluginId: request.pluginId,
            pluginName: this.options.nameFor(request.pluginId),
            kind: request.kind,
            params: request.params,
            permissions: this.options.permissionsFor(request.pluginId)
          })
        } catch {
          this.pending.delete(request.requestId)
          resolve(request.kind === 'confirm' ? { confirmed: false } : { submitted: false })
        }
      })
      return request.kind === 'confirm'
        ? { confirmed: answer.confirmed === true }
        : answer.submitted === true
          ? { submitted: true, ...(answer.value === undefined ? {} : { value: answer.value }) }
          : { submitted: false }
    },
    cancel: (requestId) => {
      const entry = this.pending.get(requestId)
      if (entry === undefined) return
      this.pending.delete(requestId)
      entry.resolve({ submitted: false, confirmed: false })
      try {
        this.options.publishCancel(requestId)
      } catch {
        // Renderer may already be gone.
      }
    }
  }

  /** IPC reply; the first answer wins. */
  respond(
    requestId: string,
    answer: { submitted?: boolean; confirmed?: boolean; value?: string }
  ): boolean {
    const entry = this.pending.get(requestId)
    if (entry === undefined) return false
    this.pending.delete(requestId)
    entry.resolve(answer)
    return true
  }

  cancelAll(): void {
    for (const requestId of [...this.pending.keys()]) this.presenter.cancel(requestId)
  }
}
