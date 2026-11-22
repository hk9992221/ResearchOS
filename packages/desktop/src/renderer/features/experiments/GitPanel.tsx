import { useQuery } from '@tanstack/react-query'

import type { TargetRef } from '@researchos/shared'

import {
  Badge,
  Banner,
  EmptyState,
  ErrorState,
  Rows,
  Row,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { experimentsApi } from './api'
import { formatTime, gitState, shortSha } from './model'
import styles from './experiments.module.css'

export interface GitPanelProps {
  readonly projectId: string
  readonly target: TargetRef
}

/**
 * Git facts of the bound folder. Git is optional: a folder without a repository
 * still scans. `available=false` means the metadata was read but the CLI
 * status was not; that is shown as a degraded state, never as green.
 */
export function GitPanel({ projectId, target }: GitPanelProps): JSX.Element {
  const t = useT()
  const query = useQuery({
    queryKey: ['experiments', projectId, 'git', target.kind, target.id],
    queryFn: () => experimentsApi.gitInfo(projectId, target)
  })

  if (query.isPending) return <Skeleton rows={4} />
  if (query.isError) {
    return (
      <ErrorState
        title={t('pages.experiments.banner_problem')}
        message={query.error instanceof Error ? query.error.message : undefined}
        onRetry={() => void query.refetch()}
        retryLabel={t('pages.experiments.btn_refresh')}
      />
    )
  }

  const info = query.data
  const state = gitState(info)
  if (state === 'absent') {
    return (
      <EmptyState
        title={t('pages.experiments.git_none_title')}
        description={t('pages.experiments.git_none_reason')}
        compact
      />
    )
  }

  return (
    <div className={styles.stack}>
      <div className={styles.badges}>
        <Badge tone={state === 'ok' ? 'success' : 'warning'}>
          {t('pages.experiments.git_branch')}: {info.branch ?? t('pages.experiments.git_detached')}
        </Badge>
        {info.dirty && <Badge tone="warning">{t('pages.experiments.git_dirty')}</Badge>}
      </div>

      {state === 'metadata_only' && (
        <Banner tone="warning" title={t('pages.experiments.git_warning_title')}>
          {info.error ?? t('pages.experiments.git_warning_message')}
        </Banner>
      )}

      <Rows>
        <Row label={t('pages.experiments.git_commit')} value={shortSha(info.commit)} mono />
        <Row label={t('pages.experiments.git_time')} value={formatTime(info.commitTime)} />
        <Row label={t('pages.experiments.git_changed')} value={info.changedFiles} />
        <Row label={t('pages.experiments.git_untracked')} value={info.untrackedFiles} />
        <Row
          label={t('pages.experiments.field_dir')}
          value={
            <span className={styles.mono} title={info.repoRoot}>
              {info.repoRoot}
            </span>
          }
        />
        <Row label={t('pages.experiments.git_captured')} value={formatTime(info.capturedAt)} />
      </Rows>
    </div>
  )
}