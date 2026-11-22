import { randomUUID } from 'node:crypto'

import type {
  PluginPromptParams,
  PluginPromptResult,
  PluginUiConfirmParams
} from '@researchos/shared'

import { AppErrorCode, appError } from '../../errors'

/**
 * One pending host dialog per plugin. The broker owns the
 * concurrency lock and cancellation; the desktop side owns the actual window.
 * `respond` is idempotent: the first answer wins, later answers are ignored.
 */

export type PluginPromptKind = 'prompt' | 'confirm'

export interface PluginPromptRequest {
  requestId: string
  pluginId: string
  kind: PluginPromptKind
  params: PluginPromptParams | PluginUiConfirmParams
}

export interface PluginPromptPresenter {
  /** Shows the dialog; resolves when the human answers or withdraws. */
  present(request: PluginPromptRequest): Promise<PluginPromptResult | { confirmed: boolean }>
  cancel(requestId: string): void
}

interface Outstanding {
  requestId: string
  pluginId: string
  presenter: PluginPromptPresenter
  kind: PluginPromptKind
  settle: (result: PluginPromptResult | { confirmed: boolean }) => void
}

export class PluginPromptBroker {
  private readonly pendingByPlugin = new Map<string, Outstanding>()
  private readonly pendingById = new Map<string, Outstanding>()

  async prompt(
    pluginId: string,
    pluginName: string,
    params: PluginPromptParams,
    presenter: PluginPromptPresenter
  ): Promise<PluginPromptResult> {
    const result = await this.ask(pluginId, pluginName, 'prompt', params, presenter)
    return result as PluginPromptResult
  }

  async confirm(
    pluginId: string,
    pluginName: string,
    params: PluginUiConfirmParams,
    presenter: PluginPromptPresenter
  ): Promise<{ confirmed: boolean }> {
    const result = await this.ask(pluginId, pluginName, 'confirm', params, presenter)
    return result as { confirmed: boolean }
  }

  /** Answers an outstanding request by id; false when it is already settled. */
  respond(
    requestId: string,
    answer: { submitted?: boolean; confirmed?: boolean; value?: string }
  ): boolean {
    const outstanding = this.pendingById.get(requestId)
    if (outstanding === undefined) return false
    this.release(outstanding)
    if (outstanding.kind === 'confirm') {
      outstanding.settle({ confirmed: answer.confirmed === true })
    } else {
      outstanding.settle(
        answer.submitted === true
          ? { submitted: true, ...(answer.value === undefined ? {} : { value: answer.value }) }
          : { submitted: false }
      )
    }
    return true
  }

  /** Plugin disabled / window closed: every pending dialog ends cancelled. */
  cancelAll(pluginId: string): number {
    let cancelled = 0
    for (const outstanding of [...this.pendingById.values()]) {
      if (outstanding.pluginId !== pluginId) continue
      this.release(outstanding)
      try {
        outstanding.presenter.cancel(outstanding.requestId)
      } catch {
        // Closing a dialog must never throw back into the service.
      }
      outstanding.settle(
        outstanding.kind === 'confirm' ? { confirmed: false } : { submitted: false }
      )
      cancelled += 1
    }
    return cancelled
  }

  cancelAllPlugins(): number {
    let cancelled = 0
    for (const pluginId of new Set([...this.pendingById.values()].map((item) => item.pluginId))) {
      cancelled += this.cancelAll(pluginId)
    }
    return cancelled
  }

  pendingCount(pluginId?: string): number {
    if (pluginId === undefined) return this.pendingById.size
    return [...this.pendingById.values()].filter((item) => item.pluginId === pluginId).length
  }

  private async ask(
    pluginId: string,
    _pluginName: string,
    kind: PluginPromptKind,
    params: PluginPromptParams | PluginUiConfirmParams,
    presenter: PluginPromptPresenter
  ): Promise<PluginPromptResult | { confirmed: boolean }> {
    if (this.pendingByPlugin.has(pluginId)) {
      throw appError(AppErrorCode.Conflict, 'another plugin dialog is already open', {
        pluginId,
        field: 'ui.prompt'
      })
    }
    const requestId = randomUUID()
    const promise = new Promise<PluginPromptResult | { confirmed: boolean }>((resolve) => {
      const outstanding: Outstanding = {
        requestId,
        pluginId,
        presenter,
        kind,
        settle: (result) => resolve(result)
      }
      this.pendingByPlugin.set(pluginId, outstanding)
      this.pendingById.set(requestId, outstanding)
    })
    void presenter
      .present({ requestId, pluginId, kind, params })
      .then((answer) => {
        const outstanding = this.pendingById.get(requestId)
        if (outstanding === undefined) return
        this.release(outstanding)
        outstanding.settle(answer)
      })
      .catch(() => {
        const outstanding = this.pendingById.get(requestId)
        if (outstanding === undefined) return
        this.release(outstanding)
        outstanding.settle(kind === 'confirm' ? { confirmed: false } : { submitted: false })
      })
    return promise
  }

  private release(outstanding: Outstanding): void {
    this.pendingById.delete(outstanding.requestId)
    if (this.pendingByPlugin.get(outstanding.pluginId) === outstanding) {
      this.pendingByPlugin.delete(outstanding.pluginId)
    }
  }
}
