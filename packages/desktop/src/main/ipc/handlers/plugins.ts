import type {
  PluginMarketService,
  PluginService,
  PluginUpdateScheduler
} from '@researchos/core/modules/plugins/index'
import type { PluginHostContext } from '@researchos/shared'
import { pluginValidators } from '@researchos/core/validation/plugins'

import type { CommandRouter } from '../router'

export interface PluginIpcDeps {
  /**
   * Accessors rather than instances: the workspace can be replaced while the
   * handlers stay registered, and a market command must never reach the
   * service of a closed workspace. The archived gate does not apply to
   * workspace-level plugins.
   */
  service(): PluginService
  market(): PluginMarketService
  updates(): PluginUpdateScheduler
  /** Renderer answer to an outstanding `ui.prompt` / `ui.confirm`. */
  respondPrompt(
    requestId: string,
    answer: { submitted?: boolean; confirmed?: boolean; value?: string }
  ): boolean
  hostContext(): PluginHostContext
  setHostContext(patch: {
    projectId?: string | null
    theme?: 'light' | 'dark'
    locale?: string
  }): PluginHostContext
}

/**
 * Plugin IPC adapters (plan §3.5/§4.12). Validation happens here;
 * lifecycle and permission decisions live in `PluginService`, and the market
 * state machine in `PluginMarketService`, so the same rules apply to hot
 * reload, the renderer, the scheduler and the utility-process RPC router.
 */
export function registerPluginHandlers(router: CommandRouter, deps: PluginIpcDeps): void {
  const { service, market, updates } = deps

  router.registerHandler('plugins.list', pluginValidators['plugins.list'], () => service().list())

  router.registerHandler('plugins.install', pluginValidators['plugins.install'], (request) =>
    service().install(request.sourcePath, { enable: request.enable })
  )

  router.registerHandler('plugins.enable', pluginValidators['plugins.enable'], (request) =>
    service().enable(request.pluginId, {
      permissions: request.permissions,
      networkDomains: request.networkDomains,
      confirmationToken: request.confirmationToken,
      confirmWriteScopes: request.confirmWriteScopes
    })
  )

  router.registerHandler('plugins.disable', pluginValidators['plugins.disable'], (request) =>
    service().disable(request.pluginId)
  )

  router.registerHandler('plugins.update', pluginValidators['plugins.update'], (request) =>
    service().update(request.pluginId, request.sourcePath)
  )

  router.registerHandler('plugins.uninstall', pluginValidators['plugins.uninstall'], (request) =>
    service().uninstall(request.pluginId)
  )

  router.registerHandler('plugins.grants.set', pluginValidators['plugins.grants.set'], (request) =>
    service().setGrants(
      request.pluginId,
      request.permissions,
      request.networkDomains,
      request.confirmationToken,
      request.confirmWriteScopes
    )
  )

  router.registerHandler('plugins.rpc.call', pluginValidators['plugins.rpc.call'], (request) =>
    service().call(request.pluginId, request.method, request.params)
  )

  router.registerHandler('plugins.commands.list', pluginValidators['plugins.commands.list'], () =>
    service().commandsList()
  )

  router.registerHandler(
    'plugins.prompt.respond',
    pluginValidators['plugins.prompt.respond'],
    (request) => ({
      delivered: deps.respondPrompt(request.requestId, {
        ...(request.submitted === undefined ? {} : { submitted: request.submitted }),
        ...(request.confirmed === undefined ? {} : { confirmed: request.confirmed }),
        ...(request.value === undefined ? {} : { value: request.value })
      })
    })
  )

  router.registerHandler(
    'plugins.host.context.set',
    pluginValidators['plugins.host.context.set'],
    (request) => ({ activeProjectId: deps.setHostContext(request).activeProjectId })
  )

  /* ------------------------------------------------------------------ market */

  router.registerHandler('plugins.market.search', pluginValidators['plugins.market.search'], (request) =>
    market().search(request)
  )

  router.registerHandler('plugins.market.refresh', pluginValidators['plugins.market.refresh'], (request) =>
    market().refresh(request)
  )

  router.registerHandler('plugins.market.detail', pluginValidators['plugins.market.detail'], (request) =>
    market().detail(request.pluginId, request.registryId)
  )

  router.registerHandler('plugins.market.install', pluginValidators['plugins.market.install'], (request) =>
    market().install(request)
  )

  router.registerHandler('plugins.market.previewGithub', pluginValidators['plugins.market.previewGithub'], (request) =>
    market().previewGithub(request.repo, request.tag)
  )

  router.registerHandler(
    'plugins.market.installFromGithub',
    pluginValidators['plugins.market.installFromGithub'],
    (request) => market().installFromGithub(request)
  )

  // Manual checks run through the scheduler so a click cannot race the
  // background poll, and the chain re-arms afterwards.
  router.registerHandler('plugins.market.checkUpdates', pluginValidators['plugins.market.checkUpdates'], () =>
    updates().runNow()
  )

  router.registerHandler('plugins.market.update', pluginValidators['plugins.market.update'], (request) =>
    market().update(request)
  )

  router.registerHandler('plugins.setAutoUpdate', pluginValidators['plugins.setAutoUpdate'], (request) =>
    market().setAutoUpdate(request.pluginId, request.enabled)
  )

  router.registerHandler('plugins.registries.list', pluginValidators['plugins.registries.list'], () =>
    market().registries()
  )

  router.registerHandler('plugins.registries.add', pluginValidators['plugins.registries.add'], (request) =>
    market().addRegistry(request.name, request.url)
  )

  router.registerHandler('plugins.registries.remove', pluginValidators['plugins.registries.remove'], (request) =>
    market().removeRegistry(request.id)
  )

  router.registerHandler('plugins.market.settings.get', pluginValidators['plugins.market.settings.get'], () =>
    market().marketSettings()
  )

  router.registerHandler(
    'plugins.market.settings.update',
    pluginValidators['plugins.market.settings.update'],
    (request) => {
      const settings = market().updateMarketSettings(request)
      // The poll cadence changed; re-arm without waiting for the current tick.
      updates().reschedule()
      return settings
    }
  )
}
