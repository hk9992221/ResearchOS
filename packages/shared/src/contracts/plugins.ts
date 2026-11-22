import type {
  GithubReleasePreview,
  MarketPluginDetail,
  MarketPluginSummary,
  MarketGrantInput,
  MarketSettings,
  PluginGrants,
  PluginInfo,
  PluginPermission,
  PluginRegistryInfo,
  PluginUpdateCheckResult
} from '../plugin-api'

export interface PluginCommandView {
  id: string
  title: string
  pluginId: string
}

export interface PluginGrantsInput extends PluginGrants {
  confirmationToken?: string
}

export interface PluginCommands {
  'plugins.list': { req: void; res: PluginInfo[] }
  'plugins.install': { req: { sourcePath: string; enable?: boolean }; res: PluginInfo }
  'plugins.enable': {
    req: {
      pluginId: string
      permissions?: PluginPermission[]
      networkDomains?: string[]
      confirmationToken?: string
      /** Renderer confirmed the highlighted write scopes in its dialog. */
      confirmWriteScopes?: boolean
    }
    res: PluginInfo
  }
  'plugins.disable': { req: { pluginId: string }; res: PluginInfo }
  'plugins.update': { req: { pluginId: string; sourcePath: string }; res: PluginInfo }
  'plugins.uninstall': { req: { pluginId: string }; res: { removed: boolean } }
  'plugins.grants.set': {
    req: {
      pluginId: string
      permissions: PluginPermission[]
      networkDomains?: string[]
      confirmationToken?: string
      confirmWriteScopes?: boolean
    }
    res: PluginInfo
  }
  'plugins.rpc.call': {
    req: { pluginId: string; method: string; params: unknown; confirmationToken?: string }
    res: unknown
  }
  'plugins.commands.list': { req: void; res: PluginCommandView[] }

  /** Answer to an outstanding `ui.prompt` / `ui.confirm` request. */
  'plugins.prompt.respond': {
    req: { requestId: string; submitted?: boolean; confirmed?: boolean; value?: string }
    res: { delivered: boolean }
  }

  /** Renderer → main context report backing `host.getContext` and its events. */
  'plugins.host.context.set': {
    req: { projectId?: string | null; theme?: 'light' | 'dark'; locale?: string }
    res: { activeProjectId: string | null }
  }

  /* ------------------------------------------------------- market & updates */

  /** Local search over the cached registry documents. */
  'plugins.market.search': {
    req: { query?: string; tags?: string[]; verifiedOnly?: boolean; registryId?: string }
    res: MarketPluginSummary[]
  }
  'plugins.market.refresh': {
    req: { force?: boolean }
    res: { updatedRegistries: number; totalPlugins: number }
  }
  'plugins.market.detail': {
    req: { pluginId: string; registryId?: string }
    res: MarketPluginDetail
  }
  'plugins.market.install': {
    req: { pluginId: string; registryId?: string } & MarketGrantInput
    res: PluginInfo
  }
  'plugins.market.previewGithub': {
    req: { repo: string; tag?: string }
    res: GithubReleasePreview
  }
  'plugins.market.installFromGithub': {
    req: { repo: string; tag?: string; confirmUnverified?: boolean } & MarketGrantInput
    res: PluginInfo
  }
  /** Manual full check; also runs the safe silent updates. */
  'plugins.market.checkUpdates': { req: void; res: PluginUpdateCheckResult[] }
  'plugins.market.update': {
    req: { pluginId: string } & MarketGrantInput
    res: PluginInfo
  }
  'plugins.setAutoUpdate': { req: { pluginId: string; enabled: boolean }; res: PluginInfo }
  'plugins.registries.list': { req: void; res: PluginRegistryInfo[] }
  'plugins.registries.add': { req: { name: string; url: string }; res: PluginRegistryInfo }
  'plugins.registries.remove': { req: { id: string }; res: { removed: boolean } }

  /** Market configuration lives in workspace settings. */
  'plugins.market.settings.get': { req: void; res: MarketSettings }
  'plugins.market.settings.update': { req: Partial<MarketSettings>; res: MarketSettings }
}