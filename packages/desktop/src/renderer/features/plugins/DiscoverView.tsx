import { useMemo, useState } from 'react'
import { useQuery, keepPreviousData } from '@tanstack/react-query'

import type { MarketPluginDetail, MarketPluginSummary, PluginPermission } from '@researchos/shared'
import { PLUGIN_MARKET_TAG_LABELS, isPluginWritePermission } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  CardGrid,
  EmptyState,
  ErrorState,
  Mono,
  Skeleton,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { PluginDetailDrawer } from './PluginDetailDrawer'
import styles from './PluginManagerPage.module.css'

const TAG_KEYS: Readonly<Record<(typeof PLUGIN_MARKET_TAG_LABELS)[number], string>> = {
  literature: 'pages.plugins.tag_literature',
  notes: 'pages.plugins.tag_notes',
  experiments: 'pages.plugins.tag_experiments',
  ai: 'pages.plugins.tag_ai',
  productivity: 'pages.plugins.tag_productivity'
}

export interface DiscoverViewProps {
  readonly busy: string | null
  readonly refreshFailed: boolean
  readonly onInstall: (plugin: MarketPluginSummary, detail?: MarketPluginDetail) => void
  readonly onUpdate: (plugin: MarketPluginSummary) => void
}

/** Discover view: local search over the cached index, tags, verified filter. */
export function DiscoverView({
  busy,
  refreshFailed,
  onInstall,
  onUpdate
}: DiscoverViewProps): JSX.Element {
  const t = useT()
  const [search, setSearch] = useState('')
  const [tags, setTags] = useState<ReadonlySet<string>>(new Set())
  const [verifiedOnly, setVerifiedOnly] = useState(false)
  const [selected, setSelected] = useState<MarketPluginSummary | null>(null)

  const criteria = useMemo(
    () => ({
      query: search.trim(),
      tags: [...tags].sort(),
      verifiedOnly
    }),
    [search, tags, verifiedOnly]
  )

  const market = useQuery({
    queryKey: queryKeys.plugins.market(criteria),
    queryFn: () =>
      ipc.invoke('plugins.market.search', {
        query: criteria.query === '' ? undefined : criteria.query,
        tags: criteria.tags.length === 0 ? undefined : criteria.tags,
        verifiedOnly: criteria.verifiedOnly
      }),
    placeholderData: keepPreviousData
  })

  const plugins = market.data ?? []

  const toggleTag = (tag: string): void => {
    setTags((current) => {
      const next = new Set(current)
      if (next.has(tag)) next.delete(tag)
      else next.add(tag)
      return next
    })
  }

  return (
    <div className={styles.view}>
      {refreshFailed && plugins.length === 0 && (
        <Banner tone="warning" title={t('pages.plugins.market_offline')} />
      )}

      <div className={styles.marketToolbar}>
        <div className={styles.searchField}>
          <TextInput
            label={t('pages.plugins.market_search')}
            hideLabel
            value={search}
            placeholder={t('pages.plugins.market_search')}
            onChange={setSearch}
          />
        </div>
        <div className={styles.chips} role="group" aria-label={t('pages.plugins.tags')}>
          <button
            type="button"
            className={styles.chip}
            aria-pressed={tags.size === 0}
            onClick={() => setTags(new Set())}
          >
            {t('pages.plugins.tag_all')}
          </button>
          {PLUGIN_MARKET_TAG_LABELS.map((tag) => (
            <button
              key={tag}
              type="button"
              className={styles.chip}
              aria-pressed={tags.has(tag)}
              onClick={() => toggleTag(tag)}
            >
              {t(TAG_KEYS[tag])}
            </button>
          ))}
        </div>
        <SwitchField
          label={t('pages.plugins.verified_only')}
          checked={verifiedOnly}
          onChange={setVerifiedOnly}
        />
      </div>

      {market.isPending ? (
        <Skeleton rows={5} />
      ) : market.isError ? (
        <ErrorState
          title={t('pages.plugins.load_failed')}
          message={market.error instanceof Error ? market.error.message : String(market.error)}
          onRetry={() => void market.refetch()}
          retryLabel={t('pages.plugins.retry')}
        />
      ) : plugins.length === 0 ? (
        <EmptyState title={t('pages.plugins.discover_empty')} />
      ) : (
        <CardGrid>
          {plugins.map((plugin) => (
            <div
              key={`${plugin.registryId}:${plugin.pluginId}`}
              className={styles.marketCard}
              onClick={() => setSelected(plugin)}
            >
              <Card>
                <div className={styles.cardHead}>
                  <button
                    type="button"
                    className={styles.cardName}
                    onClick={(event) => {
                      event.stopPropagation()
                      setSelected(plugin)
                    }}
                  >
                    {plugin.name}
                  </button>
                  {plugin.verified && (
                    <Badge tone="success">{t('pages.plugins.verified')}</Badge>
                  )}
                </div>
                <p className={styles.author}>{plugin.author}</p>
                {plugin.description !== '' && (
                  <p className={styles.description}>{plugin.description}</p>
                )}
                <div className={styles.meta}>
                  <Mono>{plugin.latestVersion}</Mono>
                </div>
                <PermissionSummary
                  permissions={plugin.permissions}
                  networkDomains={plugin.networkDomains}
                />
                <div
                  className={styles.actions}
                  onClick={(event) => event.stopPropagation()}
                >
                  {plugin.installedVersion !== null ? (
                    plugin.updateAvailable ? (
                      <Button
                        variant="primary"
                        size="sm"
                        disabled={busy !== null}
                        onClick={() => onUpdate(plugin)}
                      >
                        {t('pages.plugins.update')}
                      </Button>
                    ) : (
                      <Button variant="secondary" size="sm" disabled>
                        {t('pages.plugins.action_installed')}
                      </Button>
                    )
                  ) : (
                    <Button
                      variant="primary"
                      size="sm"
                      disabled={busy !== null}
                      onClick={() => onInstall(plugin)}
                    >
                      {t('pages.plugins.install')}
                    </Button>
                  )}
                </div>
              </Card>
            </div>
          ))}
        </CardGrid>
      )}

      <PluginDetailDrawer
        plugin={selected}
        busy={busy}
        onClose={() => setSelected(null)}
        onInstall={(plugin, detail) => {
          setSelected(null)
          onInstall(plugin, detail)
        }}
        onUpdate={(plugin) => {
          setSelected(null)
          onUpdate(plugin)
        }}
      />
    </div>
  )
}

function PermissionSummary({
  permissions,
  networkDomains
}: {
  readonly permissions: readonly PluginPermission[]
  readonly networkDomains: readonly string[]
}): JSX.Element {
  const t = useT()
  if (permissions.length === 0) {
    return <p className={styles.muted}>{t('pages.plugins.no_permissions')}</p>
  }
  return (
    <div className={styles.scopes}>
      {permissions.map((permission) => {
        if (permission === 'network') {
          return (
            <Badge key={permission} tone="warning">
              {networkDomains.length > 0
                ? `${t('pages.plugins.network_scope')} · ${String(networkDomains.length)}`
                : t('pages.plugins.network_scope')}
            </Badge>
          )
        }
        if (isPluginWritePermission(permission)) {
          return (
            <Badge key={permission} tone="warning">
              {permission} · {t('pages.plugins.write_scope')}
            </Badge>
          )
        }
        return (
          <Badge key={permission} tone="neutral">
            {permission}
          </Badge>
        )
      })}
    </div>
  )
}