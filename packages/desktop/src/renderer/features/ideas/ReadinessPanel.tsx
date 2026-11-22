import { useQuery } from '@tanstack/react-query'

import { Badge, ErrorState, ProgressBar, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { ideasApi } from './api'
import styles from './ideas.module.css'

export interface ReadinessPanelProps {
  readonly projectId: string
  readonly ideaId: string
}

/**
 * Readiness tab: the deterministic R-01…R-06 audit for this idea. Read-only —
 * suggestions explain the gap; creating tasks is not offered here.
 */
export function ReadinessPanel({ projectId, ideaId }: ReadinessPanelProps): JSX.Element {
  const t = useT()
  const readiness = useQuery({
    queryKey: ['project', projectId, 'readiness', ideaId],
    queryFn: () => ideasApi.readiness(projectId, ideaId)
  })

  if (readiness.isPending) return <Skeleton rows={4} />
  if (readiness.isError) {
    return (
      <ErrorState
        title={t('pages.readiness.failed')}
        message={readiness.error instanceof Error ? readiness.error.message : undefined}
        onRetry={() => void readiness.refetch()}
        retryLabel={t('pages.ideas.retry')}
      />
    )
  }

  const checks = readiness.data.ideas.find((item) => item.ideaId === ideaId)
  if (checks === undefined) {
    return (
      <section className={styles.panel}>
        <h2 className={styles.sectionTitle}>{t('pages.readiness.title')}</h2>
        <p className={styles.muted}>{t('pages.ideas.readiness_not_audited')}</p>
      </section>
    )
  }

  const complete = checks.items.length === 0

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.readiness.title')}</h2>
        <Badge tone={complete ? 'success' : 'warning'}>
          {t('pages.readiness.badge_complete', { passed: checks.passed, total: checks.total })}
        </Badge>
      </div>
      <ProgressBar
        value={checks.total === 0 ? 100 : (checks.passed / checks.total) * 100}
        label={t('pages.readiness.idea_complete', { passed: checks.passed, total: checks.total })}
      />
      <p className={styles.muted}>
        {complete
          ? t('pages.readiness.no_gaps')
          : t('pages.readiness.idea_gaps', { count: checks.items.length })}
      </p>

      {checks.items.length > 0 && (
        <ul className={styles.readinessList}>
          {checks.items.map((item) => (
            <li key={`${item.rule}-${item.experimentId ?? item.ideaId ?? ''}`} className={styles.readinessItem}>
              <Badge tone="neutral">{item.rule}</Badge>
              <Badge tone={item.priority === 'high' ? 'danger' : 'warning'}>{item.priority}</Badge>
              <div className={styles.readinessText}>
                <p className={styles.readinessSummary}>{item.summary}</p>
                <p className={styles.muted}>{item.suggestion}</p>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}