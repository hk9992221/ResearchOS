import { useQuery } from '@tanstack/react-query'

import { Modal, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { experimentsApi } from './api'
import { ExperimentMarkdown } from './ExperimentMarkdown'
import { changedMetricKeys, metricValueMap } from './model'
import styles from './experiments.module.css'

export interface CompareSnapshotsDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly leftId: string | null
  readonly rightId: string | null
  readonly onClose: () => void
}

/**
 * Two frozen analyses side by side. This is a client-side diff of the stored
 * Markdown and metric values; it never asks the model and never rewrites
 * either snapshot.
 */
export function CompareSnapshotsDialog({
  open,
  projectId,
  leftId,
  rightId,
  onClose
}: CompareSnapshotsDialogProps): JSX.Element {
  const t = useT()
  const enabled = open && leftId !== null && rightId !== null
  const left = useQuery({
    queryKey: ['experiments', projectId, 'snapshot', leftId ?? 'none'],
    queryFn: () => experimentsApi.snapshot(projectId, leftId as string),
    enabled
  })
  const right = useQuery({
    queryKey: ['experiments', projectId, 'snapshot', rightId ?? 'none'],
    queryFn: () => experimentsApi.snapshot(projectId, rightId as string),
    enabled
  })

  const loading = enabled && (left.isPending || right.isPending)
  const changed =
    left.data !== undefined && right.data !== undefined
      ? changedMetricKeys(
          metricValueMap(left.data.metrics.metrics),
          metricValueMap(right.data.metrics.metrics)
        )
      : []
  const leftValues =
    left.data === undefined ? {} : metricValueMap(left.data.metrics.metrics)
  const rightValues =
    right.data === undefined ? {} : metricValueMap(right.data.metrics.metrics)

  return (
    <Modal open={open} title={t('pages.experiments.snapshot_compare_title')} onClose={onClose} width={920}>
      {loading ? (
        <Skeleton rows={8} />
      ) : left.data === undefined || right.data === undefined ? (
        <p className={styles.errorText} role="alert">
          {t('pages.experiments.toast_history_failed')}
        </p>
      ) : (
        <div className={styles.stack}>
          <div className={styles.compareGrid}>
            <div className={styles.cellStack}>
              <span className={styles.mono}>{left.data.id}</span>
              <ExperimentMarkdown
                markdown={left.data.aiAnalysis?.contentMd ?? ''}
              />
            </div>
            <div className={styles.cellStack}>
              <span className={styles.mono}>{right.data.id}</span>
              <ExperimentMarkdown
                markdown={right.data.aiAnalysis?.contentMd ?? ''}
              />
            </div>
          </div>

          <div className={styles.stack}>
            <strong>{t('pages.experiments.snapshot_compare_metrics')}</strong>
            {changed.length === 0 ? (
              <p className={styles.muted}>{t('pages.experiments.compare_empty')}</p>
            ) : (
              <table className={styles.matrixTable}>
                <thead>
                  <tr>
                    <th scope="col">{t('pages.experiments.metric_key')}</th>
                    <th scope="col">{t('pages.experiments.metric_value')}</th>
                    <th scope="col">{t('pages.experiments.metric_value')}</th>
                  </tr>
                </thead>
                <tbody>
                  {changed.map((key) => (
                    <tr key={key}>
                      <td>{key}</td>
                      <td>{leftValues[key] ?? '—'}</td>
                      <td>{rightValues[key] ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )}
    </Modal>
  )
}