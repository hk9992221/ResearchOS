import type { WeeklyDigest } from '@researchos/shared'

import { Badge, Banner, EmptyState, ErrorState, Mono, Skeleton, Table } from '../../design/components'
import { useI18n, useT } from '../../i18n'
import { formatInstant } from './weekly-format'
import styles from './weekly.module.css'

export interface DigestPanelProps {
  readonly digest: WeeklyDigest | undefined
  readonly loading: boolean
  readonly error: unknown
  readonly onRetry: () => void
}

const ACTOR_KEYS: Readonly<Record<string, string>> = {
  user: 'pages.weekly_review.actor_user',
  mcp: 'pages.weekly_review.actor_mcp',
  ai: 'pages.weekly_review.actor_ai'
}

/**
 * DigestPanel is the evidence behind the report: the same activity digest the
 * generator reads, with the blocked tasks and next-week candidates that feed
 * the Checks panel. Nothing here is recomputed from the body.
 */
export function DigestPanel({ digest, loading, error, onRetry }: DigestPanelProps): JSX.Element {
  const t = useT()
  const { language } = useI18n()

  if (error !== null && error !== undefined) {
    return (
      <ErrorState
        title={t('pages.weekly_review.banner_problem')}
        message={error instanceof Error ? error.message : String(error)}
        retryLabel={t('pages.weekly_review.btn_reload')}
        onRetry={onRetry}
      />
    )
  }
  if (loading || digest === undefined) return <Skeleton rows={5} />

  const sectionTitles = new Map(digest.sections.map((section) => [section.key, section.title]))
  const items = digest.sections.flatMap((section) =>
    section.items.map((item) => ({ section: section.key, item }))
  )

  if (items.length === 0) {
    return (
      <EmptyState
        title={t('pages.weekly_review.digest_empty_title')}
        description={t('pages.weekly_review.digest_empty_reason')}
      />
    )
  }

  return (
    <div className={styles.dialogGrid}>
      <div className={styles.row}>
        <Badge tone="info">{t('pages.weekly_review.metric_events')}</Badge>
        <span className={styles.mono}>{digest.eventCount}</span>
        <span className={styles.mono}>{digest.timeZone}</span>
      </div>
      {digest.truncated && (
        <Banner tone="warning" title={t('pages.weekly_review.digest_truncated')} />
      )}
      <Table
        label={t('pages.weekly_review.tab_digest')}
        columns={[
          t('pages.weekly_review.col_time'),
          t('pages.weekly_review.col_section'),
          t('pages.weekly_review.col_summary'),
          t('pages.weekly_review.col_actor')
        ]}
      >
        {items.map(({ section, item }) => (
          <tr key={`${item.type}:${item.id}:${item.kind}`}>
            <td>
              <Mono>{formatInstant(item.lastAt, language, digest.timeZone)}</Mono>
            </td>
            <td>{sectionTitles.get(section) ?? section}</td>
            <td>
              {item.summary}
              {item.detail !== '' && <span className={styles.excerpt}> {item.detail}</span>}
            </td>
            <td>{t(ACTOR_KEYS[item.actor] ?? 'pages.weekly_review.actor_user')}</td>
          </tr>
        ))}
      </Table>
      <div>
        <h3 className={styles.sectionTitle}>{t('pages.weekly_review.digest_blocked_title')}</h3>
        {digest.blocked.length === 0 ? (
          <p className={styles.emptyLine}>—</p>
        ) : (
          <ul className={styles.list}>
            {digest.blocked.map((task) => (
              <li key={`${task.type}:${task.id}`}>{task.title}</li>
            ))}
          </ul>
        )}
      </div>
      <div>
        <h3 className={styles.sectionTitle}>{t('pages.weekly_review.digest_planned_title')}</h3>
        {digest.planned.length === 0 ? (
          <p className={styles.emptyLine}>—</p>
        ) : (
          <ul className={styles.list}>
            {digest.planned.map((task) => (
              <li key={`${task.type}:${task.id}`}>
                {task.title}
                {task.deadline !== null && <span className={styles.mono}> · {task.deadline}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
      <span className={styles.mono}>
        {formatInstant(digest.collectedAt, language, digest.timeZone)} · {digest.digestHash}
      </span>
    </div>
  )
}
