import { Suspense } from 'react'
import { useParams } from 'react-router-dom'

import { EmptyState, Skeleton } from '../design/components'
import { useT } from '../i18n'
import type { RouteEntry } from '../routes'
import { iconFor } from './icons'
import styles from './RouteOutlet.module.css'

export interface RouteOutletProps {
  readonly entry: RouteEntry
}

/**
 * RouteOutlet is the content area of one destination. Until a feature module
 * registers its lazy element in the registry, the honest shell state is an
 * EmptyState that names the destination and the module that will build it —
 * never a blank screen and never fake data (plan §3.4).
 */
export function RouteOutlet({ entry }: RouteOutletProps): JSX.Element {
  const t = useT()
  const params = useParams<{
    projectId?: string
    entityId?: string
    pluginId?: string
    entry?: string
  }>()
  const title = t(entry.titleKey)
  const Icon = iconFor(entry.icon)
  const Feature = entry.feature

  if (Feature !== undefined) {
    return (
      <Suspense fallback={<div className={styles.page}><Skeleton rows={6} /></div>}>
        <Feature
          projectId={params.projectId}
          entityId={params.entityId}
          pluginId={params.pluginId}
          entry={params.entry}
        />
      </Suspense>
    )
  }

  return (
    <article className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{title}</h1>
      </header>
      <EmptyState
        icon={Icon}
        title={t('shell.placeholder.title', { page: title })}
        description={t('shell.placeholder.reason', {
          module: t(entry.downstreamKey ?? 'shell.downstream.default')
        })}
      />
    </article>
  )
}