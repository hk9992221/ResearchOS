import { Suspense, lazy, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'

import type { MarketPluginDetail, MarketPluginSummary } from '@researchos/shared'
import { isPluginWritePermission } from '@researchos/shared'

import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  IconButton,
  Mono,
  Rows,
  Row,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'
import { shortSha } from './marketFormat'
import styles from './PluginManagerPage.module.css'

// The README chunk carries KaTeX; the market grid must not pay for it.
const MarkdownPreview = lazy(() => import('../kanban/MarkdownPreview'))

export interface PluginDetailDrawerProps {
  readonly plugin: MarketPluginSummary | null
  readonly busy: string | null
  readonly onClose: () => void
  readonly onInstall: (plugin: MarketPluginSummary, detail?: MarketPluginDetail) => void
  readonly onUpdate: (plugin: MarketPluginSummary) => void
}

/** Right-side market drawer: README, full scopes, domains and provenance. */
export function PluginDetailDrawer({
  plugin,
  busy,
  onClose,
  onInstall,
  onUpdate
}: PluginDetailDrawerProps): JSX.Element | null {
  const t = useT()
  const detail = useQuery({
    queryKey: queryKeys.plugins.detail(plugin?.pluginId ?? ''),
    queryFn: () =>
      ipc.invoke('plugins.market.detail', {
        pluginId: plugin?.pluginId ?? '',
        registryId: plugin?.registryId
      }),
    enabled: plugin !== null
  })

  useEffect(() => {
    if (plugin === null) return
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [plugin, onClose])

  if (plugin === null) return null
  const resolved = detail.data

  return (
    <div
      className={styles.drawerScrim}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <aside
        className={styles.drawer}
        role="dialog"
        aria-modal="true"
        aria-label={plugin.name}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className={styles.drawerHeader}>
          <div className={styles.cardHead}>
            <h2 className={styles.drawerTitle}>{plugin.name}</h2>
            {plugin.verified && <Badge tone="success">{t('pages.plugins.verified')}</Badge>}
          </div>
          <IconButton icon={X} label={t('pages.plugins.close')} size="sm" variant="ghost" onClick={onClose} />
        </header>

        <div className={styles.drawerBody}>
          <p className={styles.author}>{plugin.author}</p>
          <p className={styles.description}>{plugin.description}</p>

          {detail.isPending ? (
            <Skeleton rows={8} />
          ) : detail.isError ? (
            <ErrorState
              title={t('pages.plugins.load_failed')}
              message={detail.error instanceof Error ? detail.error.message : String(detail.error)}
              onRetry={() => void detail.refetch()}
              retryLabel={t('pages.plugins.retry')}
            />
          ) : (
            <>
              {resolved?.readme !== null && resolved !== undefined ? (
                <div className={styles.readme}>
                  <Suspense fallback={<Skeleton rows={6} />}>
                    <MarkdownPreview source={resolved.readme} />
                  </Suspense>
                </div>
              ) : (
                <EmptyState title={t('pages.plugins.readme_empty')} compact headingLevel={3} />
              )}

              <Rows>
                <Row
                  label={t('pages.plugins.repo')}
                  value={
                    <a
                      href={`https://github.com/${plugin.repo}`}
                      target="_blank"
                      rel="noreferrer"
                    >
                      {plugin.repo}
                    </a>
                  }
                />
                <Row label={t('pages.plugins.registry')} value={plugin.registryName} />
                {resolved !== undefined && (
                  <Row
                    label={t('pages.plugins.sha256')}
                    value={<Mono>{shortSha(resolved.sha256)}</Mono>}
                  />
                )}
              </Rows>

              <section className={styles.drawerSection}>
                <h3 className={styles.sectionTitle}>{t('pages.plugins.permissions')}</h3>
                <div className={styles.scopes}>
                  {plugin.permissions.length === 0 ? (
                    <span className={styles.muted}>{t('pages.plugins.no_permissions')}</span>
                  ) : (
                    plugin.permissions.map((permission) => (
                      <Badge
                        key={permission}
                        tone={isPluginWritePermission(permission) ? 'warning' : 'neutral'}
                      >
                        {permission}
                      </Badge>
                    ))
                  )}
                </div>
              </section>

              {plugin.networkDomains.length > 0 && (
                <section className={styles.drawerSection}>
                  <h3 className={styles.sectionTitle}>{t('pages.plugins.network_domains')}</h3>
                  <div className={styles.domainList}>
                    {plugin.networkDomains.map((domain) => (
                      <Mono key={domain}>{domain}</Mono>
                    ))}
                  </div>
                </section>
              )}
            </>
          )}
        </div>

        <footer className={styles.drawerFooter}>
          {plugin.installedVersion !== null ? (
            plugin.updateAvailable ? (
              <Button variant="primary" disabled={busy !== null} onClick={() => onUpdate(plugin)}>
                {t('pages.plugins.update')}
              </Button>
            ) : (
              <Button variant="secondary" disabled>
                {t('pages.plugins.action_installed')}
              </Button>
            )
          ) : (
            <Button
              variant="primary"
              disabled={busy !== null || resolved === undefined}
              onClick={() => onInstall(plugin, resolved)}
            >
              {t('pages.plugins.install')}
            </Button>
          )}
        </footer>
      </aside>
    </div>
  )
}