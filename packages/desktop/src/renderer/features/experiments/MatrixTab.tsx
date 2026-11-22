import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import type {
  ComparisonTable,
  ExperimentView,
  MetricDirection,
  MetricComparisonRow,
  ParamDiffRow
} from '@researchos/shared'

import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  SelectField,
  Skeleton,
  SwitchField
} from '../../design/components'
import { useT } from '../../i18n'
import { experimentsApi } from './api'
import { MatrixExportDialog } from './MatrixExportDialog'
import { formatMetricValue, formatPercent, shortSha } from './model'
import styles from './experiments.module.css'

export interface MatrixTabProps {
  readonly projectId: string
  readonly experiment: ExperimentView
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

/**
 * Cross-run comparison. Everything is deterministic: the service
 * aligns metrics by key/context, computes delta and percent against the
 * baseline, and marks the best and second-best value in each direction.
 *
 * The directions are user overrides layered over the service's name-based
 * inference; flipping one re-reads the matrix rather than re-scoring it here.
 */
export function MatrixTab({ projectId, experiment, notify }: MatrixTabProps): JSX.Element {
  const t = useT()
  const [baselineId, setBaselineId] = useState('')
  const [diffOnly, setDiffOnly] = useState(true)
  const [showDelta, setShowDelta] = useState(true)
  const [precision, setPrecision] = useState(4)
  const [directions, setDirections] = useState<Record<string, MetricDirection>>({})
  const [exportOpen, setExportOpen] = useState(false)

  const compareQuery = useQuery({
    queryKey: [
      'experiments',
      projectId,
      'matrix',
      experiment.id,
      baselineId,
      diffOnly,
      directions,
      precision
    ],
    queryFn: () =>
      experimentsApi.compare({
        projectId,
        experimentId: experiment.id,
        diffParams: diffOnly,
        ...(baselineId === '' ? {} : { baselineId }),
        ...(Object.keys(directions).length === 0 ? {} : { directions }),
        table: { precision, showDelta, highlightBest: true }
      })
  })

  const table: ComparisonTable | undefined = compareQuery.data
  const runs = table?.runs ?? []
  const paramRows = table?.paramRows ?? []
  const metricRows = table?.metricRows ?? []

  const baselineOptions = useMemo(
    () => [
      { value: '', label: t('pages.experiments.matrix_baseline') },
      ...runs.map((run) => ({
        value: run.runId,
        label: `${run.label} · ${shortSha(run.gitShortSha)}${run.dirty ? ` (${t('pages.experiments.git_dirty')})` : ''}`
      }))
    ],
    [runs, t]
  )

  if (compareQuery.isPending) return <Skeleton rows={6} />
  if (compareQuery.isError) {
    return (
      <ErrorState
        title={t('pages.experiments.matrix_failed')}
        message={compareQuery.error instanceof Error ? compareQuery.error.message : undefined}
        onRetry={() => void compareQuery.refetch()}
        retryLabel={t('pages.experiments.btn_refresh')}
      />
    )
  }

  if (runs.length < 2) {
    return (
      <EmptyState
        title={t('pages.experiments.compare_title')}
        description={t('pages.experiments.compare_empty')}
      />
    )
  }

  const toggleDirection = (row: MetricComparisonRow): void => {
    const next: MetricDirection = row.direction === 'higher' ? 'lower' : 'higher'
    setDirections((current) => ({ ...current, [row.key]: next }))
  }

  const pctFor = (row: MetricComparisonRow, runId: string): string => {
    if (baselineId !== '' && runId === baselineId) return '—'
    return formatPercent(row.pct[runId])
  }

  return (
    <div className={styles.panel}>
      <Card
        title={t('pages.experiments.compare_title')}
        actions={
          <div className={styles.actions}>
            <Button variant="primary" size="sm" onClick={() => setExportOpen(true)}>
              {t('pages.experiments.matrix_export')}
            </Button>
          </div>
        }
      >
        <div className={styles.stack}>
          <div className={styles.filters}>
            <SelectField
              label={t('pages.experiments.matrix_baseline')}
              value={baselineId}
              options={baselineOptions}
              onChange={setBaselineId}
            />
            <SelectField
              label={t('pages.experiments.matrix_precision')}
              value={String(precision)}
              options={['2', '3', '4', '5', '6'].map((value) => ({ value, label: value }))}
              onChange={(value) => setPrecision(Number(value))}
            />
            <SwitchField
              label={t('pages.experiments.matrix_diff_only')}
              checked={diffOnly}
              onChange={setDiffOnly}
            />
            <SwitchField
              label={t('pages.experiments.matrix_show_delta')}
              checked={showDelta}
              onChange={setShowDelta}
            />
          </div>

          <div className={styles.matrixWrap}>
            <table className={styles.matrixTable} aria-label={t('pages.experiments.compare_title')}>
              <thead>
                <tr>
                  <th scope="col">{t('pages.experiments.metric_key')}</th>
                  {runs.map((run) => (
                    <th key={run.runId} scope="col">
                      {run.label}
                      <div className={styles.snapshotMeta}>
                        <span>{shortSha(run.gitShortSha)}</span>
                        {run.dirty && <Badge tone="warning">{t('pages.experiments.git_dirty')}</Badge>}
                      </div>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {paramRows.length > 0 && (
                  <tr className={styles.matrixSection}>
                    <td colSpan={runs.length + 1}>{t('pages.experiments.matrix_params')}</td>
                  </tr>
                )}
                {paramRows.length > 0 &&
                  paramRows.map((row: ParamDiffRow) => (
                    <tr key={`param-${row.key}`}>
                      <td>{row.key}</td>
                      {runs.map((run) => (
                        <td key={run.runId} className={row.isDiff ? styles.diffValue : undefined}>
                          {row.values[run.runId] ?? '—'}
                        </td>
                      ))}
                    </tr>
                  ))}

                {metricRows.length > 0 && (
                  <tr className={styles.matrixSection}>
                    <td colSpan={runs.length + 1}>{t('pages.experiments.matrix_metrics')}</td>
                  </tr>
                )}
                {metricRows.map((row) => (
                  <tr key={`metric-${row.key}`}>
                    <td>
                      <span>{row.key}</span>{' '}
                      <button
                        type="button"
                        className={styles.directionButton}
                        title={t('pages.experiments.matrix_metrics')}
                        onClick={() => toggleDirection(row)}
                      >
                        {row.direction === 'higher' ? '↑' : '↓'}
                      </button>
                    </td>
                    {runs.map((run) => {
                      const value = row.values[run.runId] ?? null
                      const isBest = run.runId === row.bestRunId
                      const isSecond = run.runId === row.secondBestRunId
                      return (
                        <td
                          key={run.runId}
                          className={isBest ? styles.best : isSecond ? styles.second : undefined}
                          data-best={isBest ? 'true' : undefined}
                          data-second={isSecond ? 'true' : undefined}
                        >
                          <div>{formatMetricValue(value, precision)}</div>
                          {showDelta && (
                            <div className={styles.delta}>{pctFor(row, run.runId)}</div>
                          )}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {diffOnly && paramRows.length === 0 && (
            <p className={styles.reason}>{t('pages.experiments.matrix_no_diff_params')}</p>
          )}
        </div>
      </Card>

      <MatrixExportDialog
        open={exportOpen}
        projectId={projectId}
        experimentId={experiment.id}
        baselineId={baselineId === '' ? null : baselineId}
        directions={directions}
        runIds={runs.map((run) => run.runId)}
        diffParams={diffOnly}
        precision={precision}
        onClose={() => setExportOpen(false)}
        notify={notify}
      />
    </div>
  )
}