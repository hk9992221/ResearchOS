import type { SnapshotDetail } from '@researchos/shared'

import { Badge, Card, EmptyState, Rows, Row } from '../../design/components'
import { useT } from '../../i18n'
import { formatBytes } from '../../lib/bytes'
import { ExperimentMarkdown } from './ExperimentMarkdown'
import { formatTime, shortSha } from './model'
import styles from './experiments.module.css'

export interface SnapshotDetailPanelProps {
  readonly detail: SnapshotDetail
  readonly onOpenReference: (href: string) => void
}

/**
 * One frozen snapshot: its manifest, Git facts, metrics and model metadata.
 * Everything here is read-only by design; the live draft is edited on the
 * analysis tab, never in the snapshot.
 */
export function SnapshotDetailPanel({
  detail,
  onOpenReference
}: SnapshotDetailPanelProps): JSX.Element {
  const t = useT()
  const analysis = detail.aiAnalysis

  return (
    <div className={styles.stack}>
      <div className={styles.badges}>
        <Badge tone="info">
          {detail.targetType === 'run' ? t('pages.experiments.target_run') : t('pages.experiments.target_experiment')}
        </Badge>
        <Badge tone="neutral">{formatTime(detail.createdAt)}</Badge>
        <span className={styles.mono}>{detail.id}</span>
      </div>

      <Card
        title={t('pages.experiments.history_meta_manifest', {
          count: detail.fileManifest.length
        })}
      >
        {detail.fileManifest.length === 0 ? (
          <p className={styles.muted}>{t('pages.experiments.value_none')}</p>
        ) : (
          <div className={styles.manifest}>
            {detail.fileManifest.slice(0, 2000).map((entry) => (
              <span key={entry.path}>
                {entry.path} · {entry.kind} · {formatBytes(entry.size, t)} ·{' '}
                {entry.sha256 === null
                  ? t('pages.experiments.file_hash_skipped', { reason: entry.hashSkipped ?? '—' })
                  : t('pages.experiments.file_hash', { hash: entry.sha256.slice(0, 12) })}
              </span>
            ))}
          </div>
        )}
      </Card>

      <Card title={t('pages.experiments.git_card_title')}>
        {detail.gitInfo === null || !detail.gitInfo.detected ? (
          <p className={styles.muted}>{t('pages.experiments.git_none_reason')}</p>
        ) : (
          <Rows>
            <Row label={t('pages.experiments.git_branch')} value={detail.gitInfo.branch ?? t('pages.experiments.git_detached')} />
            <Row label={t('pages.experiments.git_commit')} value={shortSha(detail.gitInfo.commit)} mono />
            <Row label={t('pages.experiments.git_time')} value={formatTime(detail.gitInfo.commitTime)} />
            <Row label={t('pages.experiments.git_captured')} value={formatTime(detail.gitInfo.capturedAt)} />
          </Rows>
        )}
      </Card>

      <Card title={t('pages.experiments.metrics_title')}>
        {detail.metrics.metrics.length === 0 ? (
          <EmptyState
            title={t('pages.experiments.metrics_empty_title')}
            description={t('pages.experiments.metrics_empty_reason')}
            compact
          />
        ) : (
          <Rows>
            {detail.metrics.metrics.map((metric) => (
              <Row
                key={`${metric.key}-${metric.source.path}-${metric.source.line ?? 0}`}
                label={metric.label || metric.key}
                value={
                  <span className={styles.mono}>
                    {metric.raw}
                    {metric.unit === null ? '' : ` ${metric.unit}`}
                  </span>
                }
              />
            ))}
          </Rows>
        )}
      </Card>

      <Card title={t('pages.experiments.snapshot_ai_section')}>
        {analysis === null ? (
          <p className={styles.muted}>{t('pages.experiments.snapshot_no_ai')}</p>
        ) : (
          <div className={styles.stack}>
            <div className={styles.badges}>
              <Badge tone="info">{analysis.kind === 'results' ? t('pages.experiments.kind_results') : t('pages.experiments.kind_outline')}</Badge>
              {analysis.warnings.map((warning) => (
                <Badge key={warning} tone="warning">
                  {warning}
                </Badge>
              ))}
            </div>
            <Rows>
              <Row label={t('pages.experiments.history_col_model')} value={`${detail.modelInfo?.providerName ?? '—'} · ${detail.modelInfo?.modelId ?? '—'}`} />
              <Row label={t('pages.experiments.snapshot_protocol')} value={detail.modelInfo?.protocol ?? '—'} />
              <Row
                label={t('pages.experiments.snapshot_prompt_version')}
                value={detail.modelInfo?.promptVersion ?? analysis.promptVersion}
              />
              <Row label={t('pages.experiments.git_captured')} value={formatTime(detail.modelInfo?.generatedAt)} />
            </Rows>
            <ExperimentMarkdown markdown={analysis.contentMd} onOpenReference={onOpenReference} />
            <p className={styles.reason}>{t('pages.experiments.snapshot_frozen')}</p>
          </div>
        )}
      </Card>
    </div>
  )
}