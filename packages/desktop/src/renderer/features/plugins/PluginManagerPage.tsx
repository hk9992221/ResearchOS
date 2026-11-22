import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type {
  MarketPluginDetail,
  MarketPluginSummary,
  OpenPathResult,
  PluginInfo,
  PluginPermission
} from '@researchos/shared'
import { isPluginWritePermission } from '@researchos/shared'

import {
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Skeleton,
  Tabs,
  Toolbar
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { pushToast } from '../../state/toasts'
import { DiscoverView } from './DiscoverView'
import { GithubInstallDialog } from './GithubInstallDialog'
import { InstalledPluginsView } from './InstalledPluginsView'
import { PermissionDialog, type GrantTarget } from './PermissionDialog'
import { RegistrySettingsDialog } from './RegistrySettingsDialog'
import { requiresGrant } from './marketFormat'
import styles from './PluginManagerPage.module.css'

interface GrantState extends GrantTarget {
  readonly confirm: (permissions: PluginPermission[], networkDomains: string[]) => void
}

interface UpdateTarget {
  readonly pluginId: string
  readonly name: string
  readonly requiresApproval: boolean
  readonly registryId?: string
}

type TabId = 'installed' | 'discover'

/**
 * Plugin centre (route `/plugins`): installed lifecycle on one side, the cached
 * registry on the other. Every mutating action goes through the main process,
 * and any newly requested write/network scope is shown before it is granted.
 */
export function PluginManagerPage(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState<TabId>('installed')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [grant, setGrant] = useState<GrantState | null>(null)
  const [uninstallTarget, setUninstallTarget] = useState<PluginInfo | null>(null)
  const [githubOpen, setGithubOpen] = useState(false)
  const [githubError, setGithubError] = useState<string | null>(null)
  const [sourcesOpen, setSourcesOpen] = useState(false)
  const [refreshFailed, setRefreshFailed] = useState(false)

  const list = useQuery({
    queryKey: queryKeys.plugins.list,
    queryFn: () => ipc.invoke('plugins.list')
  })

  const refreshQueries = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.plugins.all })
  }

  const run = async (key: string, action: () => Promise<void>): Promise<boolean> => {
    setBusy(key)
    setError(null)
    try {
      await action()
      await refreshQueries()
      return true
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      return false
    } finally {
      setBusy(null)
    }
  }

  const refreshMarket = async (): Promise<void> => {
    setBusy('market-refresh')
    setRefreshFailed(false)
    try {
      await ipc.invoke('plugins.market.refresh', { force: true })
      await refreshQueries()
    } catch {
      // The cached index stays browsable; the discover view shows the single
      // offline banner only when nothing is cached at all.
      setRefreshFailed(true)
    } finally {
      setBusy(null)
    }
  }

  const toastInstalled = (key: 'install_done' | 'update_done', name: string): void => {
    pushToast({ tone: 'success', title: t(`pages.plugins.${key}`, { name }) })
  }

  const installLocal = async (mode: 'folder' | 'zip'): Promise<void> => {
    let picked: OpenPathResult
    try {
      picked =
        mode === 'folder'
          ? await ipc.invoke('app.pickFolder', { title: t('pages.plugins.pick_folder') })
          : await ipc.invoke('app.pickFile', {
              title: t('pages.plugins.pick_zip'),
              filters: [{ name: 'Zip', extensions: ['zip'] }]
            })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      return
    }
    const sourcePath = picked.canceled ? undefined : picked.paths[0]
    if (sourcePath === undefined) return
    await run('install', async () => {
      await ipc.invoke('plugins.install', { sourcePath })
    })
  }

  const enable = (plugin: PluginInfo): void => {
    if (plugin.permissions.length === 0) {
      void run(`enable:${plugin.id}`, async () => {
        await ipc.invoke('plugins.enable', { pluginId: plugin.id, permissions: [] })
      })
      return
    }
    setGrant({
      name: plugin.name,
      permissions: plugin.permissions,
      networkDomains: plugin.networkDomains,
      confirm: (permissions, networkDomains) => {
        setGrant(null)
        void run(`enable:${plugin.id}`, async () => {
          await ipc.invoke('plugins.enable', {
            pluginId: plugin.id,
            permissions,
            networkDomains,
            confirmWriteScopes: permissions.some(isPluginWritePermission)
          })
        })
      }
    })
  }

  const fetchDetail = async (
    pluginId: string,
    registryId?: string
  ): Promise<MarketPluginDetail | null> => {
    setBusy(`detail:${pluginId}`)
    setError(null)
    try {
      return await ipc.invoke('plugins.market.detail', {
        pluginId,
        ...(registryId === undefined || registryId === '' ? {} : { registryId })
      })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      return null
    } finally {
      setBusy(null)
    }
  }

  const installMarket = async (
    plugin: MarketPluginSummary,
    detail?: MarketPluginDetail
  ): Promise<void> => {
    const resolved = detail ?? (await fetchDetail(plugin.pluginId, plugin.registryId))
    if (resolved === null) return
    if (!requiresGrant(resolved.permissions)) {
      await run(`install:${plugin.pluginId}`, async () => {
        await ipc.invoke('plugins.market.install', {
          pluginId: plugin.pluginId,
          registryId: plugin.registryId,
          permissions: resolved.permissions,
          networkDomains: resolved.networkDomains,
          confirmWriteScopes: false
        })
        toastInstalled('install_done', resolved.name)
      })
      return
    }
    setGrant({
      name: resolved.name,
      permissions: resolved.permissions,
      networkDomains: resolved.networkDomains,
      confirm: (permissions, networkDomains) => {
        setGrant(null)
        void run(`install:${plugin.pluginId}`, async () => {
          await ipc.invoke('plugins.market.install', {
            pluginId: plugin.pluginId,
            registryId: plugin.registryId,
            permissions,
            networkDomains,
            confirmWriteScopes: permissions.some(isPluginWritePermission)
          })
          toastInstalled('install_done', resolved.name)
        })
      }
    })
  }

  const updateMarket = async (target: UpdateTarget): Promise<void> => {
    if (!target.requiresApproval) {
      await run(`update:${target.pluginId}`, async () => {
        await ipc.invoke('plugins.market.update', { pluginId: target.pluginId })
        toastInstalled('update_done', target.name)
      })
      return
    }
    const resolved = await fetchDetail(target.pluginId, target.registryId)
    if (resolved === null) return
    // Every declared scope starts checked: the newly required ones are part of
    // the new manifest and the user must confirm the full grant set.
    setGrant({
      name: resolved.name,
      permissions: resolved.permissions,
      networkDomains: resolved.networkDomains,
      confirm: (permissions, networkDomains) => {
        setGrant(null)
        void run(`update:${target.pluginId}`, async () => {
          await ipc.invoke('plugins.market.update', {
            pluginId: target.pluginId,
            permissions,
            networkDomains,
            confirmWriteScopes: permissions.some(isPluginWritePermission)
          })
          toastInstalled('update_done', resolved.name)
        })
      }
    })
  }

  const updateInstalled = (plugin: PluginInfo): void => {
    void updateMarket({
      pluginId: plugin.id,
      name: plugin.name,
      requiresApproval: plugin.updateRequiresApproval,
      registryId: plugin.registryId === '' ? undefined : plugin.registryId
    })
  }

  const checkUpdates = async (): Promise<void> => {
    await run('check-updates', async () => {
      const results = await ipc.invoke('plugins.market.checkUpdates')
      const updated = results.filter((result) => result.state === 'updated').length
      const pending = results.filter(
        (result) => result.state === 'pending-approval' || result.state === 'available'
      ).length
      const failed = results.filter((result) => result.state === 'failed').length
      pushToast({
        tone: failed > 0 ? 'warning' : 'success',
        title: t('pages.plugins.update_summary', { updated, pending, failed })
      })
    })
  }

  const installGithub = async (
    repo: string,
    tag: string,
    confirmUnverified: boolean
  ): Promise<void> => {
    setBusy('github-install')
    setGithubError(null)
    try {
      const info = await ipc.invoke('plugins.market.installFromGithub', {
        repo,
        ...(tag === '' ? {} : { tag }),
        confirmUnverified
      })
      setGithubOpen(false)
      setGithubError(null)
      await refreshQueries()
      // A release's manifest is known only after download. The install leaves
      // the plugin disabled; the same grant dialog then enables it.
      if (requiresGrant(info.permissions)) {
        setGrant({
          name: info.name,
          permissions: info.permissions,
          networkDomains: info.networkDomains,
          confirm: (permissions, networkDomains) => {
            setGrant(null)
            void run(`enable:${info.id}`, async () => {
              await ipc.invoke('plugins.enable', {
                pluginId: info.id,
                permissions,
                networkDomains,
                confirmWriteScopes: permissions.some(isPluginWritePermission)
              })
              toastInstalled('install_done', info.name)
            })
          }
        })
      } else {
        await ipc.invoke('plugins.enable', { pluginId: info.id, permissions: info.permissions })
        await refreshQueries()
        toastInstalled('install_done', info.name)
      }
    } catch (cause) {
      setGithubError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(null)
    }
  }

  if (list.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.plugins.title')} />
        <Skeleton rows={5} />
      </Page>
    )
  }

  if (list.isError) {
    return (
      <Page>
        <PageHeader title={t('pages.plugins.title')} />
        <ErrorState
          title={t('pages.plugins.load_failed')}
          message={list.error instanceof Error ? list.error.message : String(list.error)}
          onRetry={() => void list.refetch()}
          retryLabel={t('pages.plugins.retry')}
        />
      </Page>
    )
  }

  const plugins = list.data

  return (
    <Page>
      <PageHeader
        title={t('pages.plugins.title')}
        actions={
          <Toolbar label={t('pages.plugins.toolbar')}>
            <Button
              variant="secondary"
              onClick={() => void refreshMarket()}
              disabled={busy !== null}
              aria-busy={busy === 'market-refresh'}
            >
              {t('pages.plugins.refresh')}
            </Button>
            <Button variant="secondary" onClick={() => void installLocal('folder')} disabled={busy !== null}>
              {t('pages.plugins.install_folder')}
            </Button>
            <Button variant="secondary" onClick={() => void installLocal('zip')} disabled={busy !== null}>
              {t('pages.plugins.install_zip')}
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                setGithubError(null)
                setGithubOpen(true)
              }}
              disabled={busy !== null}
            >
              {t('pages.plugins.github_install')}
            </Button>
            <Button variant="secondary" onClick={() => setSourcesOpen(true)} disabled={busy !== null}>
              {t('pages.plugins.source_settings')}
            </Button>
          </Toolbar>
        }
      />

      {error !== null && (
        <Banner tone="danger" title={t('pages.plugins.action_failed')}>
          {error}
        </Banner>
      )}

      <Tabs
        label={t('pages.plugins.tab_label')}
        items={[
          { id: 'installed', label: t('pages.plugins.tab_installed') },
          { id: 'discover', label: t('pages.plugins.tab_discover') }
        ]}
        value={tab}
        onChange={(id) => setTab(id as TabId)}
      />

      {tab === 'installed' ? (
        plugins.length === 0 ? (
          <EmptyState title={t('pages.plugins.empty_title')} />
        ) : (
          <InstalledPluginsView
            plugins={plugins}
            busy={busy}
            onEnable={enable}
            onDisable={(plugin) =>
              void run(`disable:${plugin.id}`, async () => {
                await ipc.invoke('plugins.disable', { pluginId: plugin.id })
              })
            }
            onUninstall={setUninstallTarget}
            onUpdate={updateInstalled}
            onAutoUpdate={(plugin, enabled) =>
              void run(`auto-update:${plugin.id}`, async () => {
                await ipc.invoke('plugins.setAutoUpdate', { pluginId: plugin.id, enabled })
              })
            }
            onCheckUpdates={() => void checkUpdates()}
          />
        )
      ) : (
        <DiscoverView
          busy={busy}
          refreshFailed={refreshFailed}
          onInstall={(plugin, detail) => void installMarket(plugin, detail)}
          onUpdate={(plugin) =>
            void updateMarket({
              pluginId: plugin.pluginId,
              name: plugin.name,
              requiresApproval: plugin.requiresApproval,
              registryId: plugin.registryId
            })
          }
        />
      )}

      <PermissionDialog
        key={
          grant === null
            ? 'none'
            : `${grant.name}:${grant.permissions.join(',')}:${grant.networkDomains.join(',')}`
        }
        target={grant}
        busy={busy !== null}
        onCancel={() => setGrant(null)}
        onConfirm={(permissions, networkDomains) => grant?.confirm(permissions, networkDomains)}
      />

      <GithubInstallDialog
        open={githubOpen}
        busy={busy !== null}
        installError={githubError}
        onClose={() => {
          setGithubOpen(false)
          setGithubError(null)
        }}
        onInstall={(repo, tag, confirmUnverified) =>
          void installGithub(repo, tag, confirmUnverified)
        }
      />

      <RegistrySettingsDialog open={sourcesOpen} onClose={() => setSourcesOpen(false)} />

      <ConfirmDialog
        open={uninstallTarget !== null}
        tone="danger"
        title={t('pages.plugins.uninstall_title', { name: uninstallTarget?.name ?? '' })}
        description={t('pages.plugins.uninstall_description')}
        confirmLabel={t('pages.plugins.uninstall_confirm')}
        cancelLabel={t('pages.plugins.cancel')}
        busy={busy !== null}
        onCancel={() => setUninstallTarget(null)}
        onConfirm={() => {
          const target = uninstallTarget
          if (target === null) return
          setUninstallTarget(null)
          void run(`uninstall:${target.id}`, async () => {
            await ipc.invoke('plugins.uninstall', { pluginId: target.id })
          })
        }}
      />
    </Page>
  )
}