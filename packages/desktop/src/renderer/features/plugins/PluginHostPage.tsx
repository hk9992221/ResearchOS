import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router-dom'

import type { PluginFrameTheme, PluginHostEvent, PluginInfo } from '@researchos/shared'

import { EmptyState, Skeleton } from '../../design/components'
import { useTheme } from '../../design/theme'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { DeclarativeSlot, PluginFrame } from '../../plugins'
import { iconFor } from '../../app/icons'
import styles from './PluginManagerPage.module.css'

/**
 * One mounted plugin panel. The route names the plugin and its
 * declared entry; enabled/granted state is re-read from `plugins.list`, and the
 * main process relays `files.changed` / `project.changed` / `theme.changed`
 * events into both the iframe and the declarative view.
 */
export function PluginHostPage(): JSX.Element {
  const t = useT()
  const params = useParams<{ projectId?: string; pluginId?: string; entry?: string }>()
  const pluginId = params.pluginId ?? ''
  const entry = params.entry ?? ''
  const projectId = params.projectId ?? null
  const theme = useTheme()

  const pluginsQuery = useQuery({
    queryKey: queryKeys.plugins.list,
    queryFn: () => ipc.invoke('plugins.list')
  })
  const plugin = useMemo<PluginInfo | undefined>(
    () => pluginsQuery.data?.find((candidate) => candidate.id === pluginId),
    [pluginsQuery.data, pluginId]
  )
  const contribution = plugin?.contributions.sidebar?.find(
    (candidate) => candidate.entry === entry || candidate.id === entry
  )

  const [events, setEvents] = useState<PluginHostEvent[]>([])
  useEffect(() => {
    setEvents([])
    if (pluginId === '') return
    return ipc.on('plugins.event', (payload) => {
      if (payload.pluginId !== pluginId) return
      setEvents((current) => [...current.slice(-49), payload.event])
    })
  }, [pluginId])

  const contextEvent = useMemo<PluginHostEvent>(
    () => ({ type: 'host.context', payload: { projectId, pluginId, entry } }),
    [projectId, pluginId, entry]
  )

  const frameTheme = useMemo<PluginFrameTheme>(
    () => ({
      mode: theme.mode === 'dark' ? 'dark' : 'light',
      tokens: {}
    }),
    [theme.mode]
  )

  if (pluginsQuery.isPending) return <Skeleton rows={6} />
  if (plugin === undefined || !plugin.enabled) {
    return (
      <EmptyState
        icon={iconFor('pluginMarket')}
        title={t('pages.plugins.host.unavailable_title')}
        description={t('pages.plugins.host.unavailable_body')}
      />
    )
  }
  if (contribution === undefined) {
    return (
      <EmptyState
        icon={iconFor('pluginMarket')}
        title={t('pages.plugins.host.entry_missing_title')}
        description={t('pages.plugins.host.entry_missing_body')}
      />
    )
  }
  const required = contribution.permissions ?? []
  if (!required.every((permission) => plugin.granted.includes(permission))) {
    return (
      <EmptyState
        icon={iconFor('pluginMarket')}
        title={t('pages.plugins.host.permission_missing_title')}
        description={t('pages.plugins.host.permission_missing_body')}
      />
    )
  }

  if (plugin.renderer !== null && (entry === 'renderer' || entry === plugin.renderer)) {
    return (
      <div className={styles.hostPage}>
        <PluginFrame
          pluginId={pluginId}
          entry={plugin.renderer}
          title={contribution.title}
          theme={frameTheme}
          invoke={(method, methodParams) =>
            ipc.invoke('plugins.rpc.call', { pluginId, method, params: methodParams })
          }
          events={[contextEvent, ...events]}
        />
      </div>
    )
  }

  return (
    <div className={styles.hostPage}>
      <DeclarativeSlot
        pluginId={pluginId}
        slot={{
          kind: 'text',
          id: contribution.id,
          title: contribution.title,
          body: plugin.description
        }}
      />
    </div>
  )
}
