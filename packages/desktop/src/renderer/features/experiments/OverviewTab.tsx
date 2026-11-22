import type { ExperimentDetail, SnapshotSummary } from '@researchos/shared'

import { Badge, Button, Card, CardGrid } from '../../design/components'
import { useT } from '../../i18n'
import { GitPanel } from './GitPanel'
import { DirBadge } from './StatusBadge'
import { formatTime, rulesSummaryText } from './model'
import styles from './experiments.module.css'

export interface OverviewTabProps {
  readonly projectId: string
  readonly detail: ExperimentDetail
  readonly archived: boolean
  readonly onRelink: () => void
  readonly onOpenRules: () => void
}

function aiStatusKey(status: string): string {
  if (status === 'confirmed') return 'pages.experiments.ai_confirmed'
  if (status === 'draft') return 'pages.experiments.ai_draft'
  return 'pages.experiments.ai_unknown'
}

function kindKey(kind: SnapshotSummary['kind']): string {
  return kind === 'results'
    ? 'pages.experiments.kind_results'
    : 'pages.experiments.kind_outline'
}

/**
 * Overview: the folder the experiment references, its Git facts, the rules in
 * force and the latest analyses. The read-only promise is stated once here, not
 * repeated on every tab.
 */
export function OverviewTab({
  projectId,
  detail,
  archived,
  onRelink,
  onOpenRules
}: OverviewTabProps): JSX.Element {
  const t = useT()
  const experiment = detail.experiment
  const dirStatus = experiment.dirRef === null ? 'unbound' : detail.dirRefs.some((ref) => ref.status === 'broken') ? 'broken' : 'ok'
  const brokenReason = detail.dirRefs.find((ref) => ref.status === 'broken')?.reason ?? ''
  const recent = detail.recentSnapshots.slice(0, 5)
  const target = { kind: 'experiment' as const, id: experiment.id }

  return (
    <div className={styles.panel}>
      <CardGrid>
        <Card title={t('pages.experiments.card_about')}>
          <div className={styles.stack}>
            <p>
              {experiment.purpose.trim() === ''
                ? t('pages.experiments.value_none')
                : t('pages.experiments.about_purpose', { value: experiment.purpose })}
            </p>
            <p className={styles.muted}>
              {t('pages.experiments.about_counts', {
                runs: detail.runs.length,
                ideas: detail.linkedIdeas.length,
                snapshots: detail.recentSnapshots.length
              })}
            </p>
            <div className={styles.badges}>
              <DirBadge status={dirStatus} />
            </div>
          </div>
        </Card>

        <Card
          title={t('pages.experiments.field_dir')}
          actions={
            <Button
              variant="secondary"
              size="sm"
              onClick={onRelink}
              disabled={archived}
              title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
            >
              {experiment.dirRef === null
                ? t('pages.experiments.btn_bind')
                : t('pages.experiments.btn_relocate')}
            </Button>
          }
        >
          <div className={styles.stack}>
            {experiment.dirRef === null ? (
              <p className={styles.muted}>{t('pages.experiments.dir_unbound')}</p>
            ) : (
              <span className={styles.mono}>{experiment.dirRef}</span>
            )}
            {dirStatus === 'broken' && brokenReason !== '' && (
              <span className={styles.reason}>
                {t('pages.experiments.dir_reason', { reason: brokenReason })}
              </span>
            )}
            {detail.dirRefs.length > 1 && (
              <div className={styles.dirList}>
                {detail.dirRefs.map((ref) => (
                  <div key={ref.refId} className={styles.rowBetween}>
                    <span className={styles.mono}>{ref.path}</span>
                    <Badge tone={ref.status === 'ok' ? 'success' : 'danger'}>
                      {ref.ownerType}: {ref.ownerId}
                    </Badge>
                  </div>
                ))}
              </div>
            )}
          </div>
        </Card>

        <Card
          title={t('pages.experiments.card_rules')}
          actions={
            <Button
              variant="secondary"
              size="sm"
              onClick={onOpenRules}
              disabled={archived}
              title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
            >
              {t('pages.experiments.btn_rules')}
            </Button>
          }
        >
          <div className={styles.stack}>
            <p className={styles.muted}>{rulesSummaryText(experiment.scanRules, t)}</p>
            <p className={styles.reason}>{t('pages.experiments.rules_git_forced')}</p>
          </div>
        </Card>
      </CardGrid>

      <Card title={t('pages.experiments.git_card_title')}>
        <GitPanel projectId={projectId} target={target} />
      </Card>

      <Card title={t('pages.experiments.card_recent')}>
        {recent.length === 0 ? (
          <p className={styles.muted}>{t('pages.experiments.recent_empty')}</p>
        ) : (
          <div className={styles.snapshotList}>
            {recent.map((snapshot) => (
              <div key={snapshot.id} className={styles.rowBetween}>
                <span className={styles.muted}>{formatTime(snapshot.createdAt)}</span>
                <div className={styles.badges}>
                  <Badge tone="info">{t(kindKey(snapshot.kind))}</Badge>
                  <Badge tone={snapshot.aiStatus === 'confirmed' ? 'success' : 'neutral'}>
                    {t(aiStatusKey(snapshot.aiStatus))}
                  </Badge>
                  <span className={styles.mono}>{snapshot.modelId}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}