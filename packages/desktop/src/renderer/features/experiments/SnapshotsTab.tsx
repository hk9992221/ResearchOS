import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import type { TargetRef } from '@researchos/shared'

import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { experimentsApi } from './api'
import { CompareSnapshotsDialog } from './CompareSnapshotsDialog'
import { SnapshotDetailPanel } from './SnapshotDetailPanel'
import { formatTime, shortSha } from './model'
import styles from './experiments.module.css'

export interface SnapshotsTabProps {
  readonly projectId: string
  readonly target: TargetRef
  readonly onOpenReference: (href: string) => void
}

function kindKey(kind: string | null): string {
  if (kind === 'results') return 'pages.experiments.kind_results'
  if (kind === 'outline') return 'pages.experiments.kind_outline'
  return 'pages.experiments.value_none'
}

/**
 * Append-only analysis history. A snapshot can be read and compared but never
 * edited or removed; the newest analysis is only the newest row, not a
 * replacement for the older ones.
 */
export function SnapshotsTab({
  projectId,
  target,
  onOpenReference
}: SnapshotsTabProps): JSX.Element {
  const t = useT()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selection, setSelection] = useState<readonly string[]>([])
  const [compareOpen, setCompareOpen] = useState(false)

  const listQuery = useQuery({
    queryKey: ['experiments', projectId, 'snapshots', target.kind, target.id, 'all'],
    queryFn: () => experimentsApi.snapshots(projectId, target, { limit: 200 })
  })

  const snapshots = listQuery.data ?? []
  const current = snapshots.find((snapshot) => snapshot.id === selectedId) ?? snapshots[0] ?? null

  const detailQuery = useQuery({
    queryKey: ['experiments', projectId, 'snapshot', current?.id ?? 'none'],
    queryFn: () => experimentsApi.snapshot(projectId, current?.id as string),
    enabled: current !== null
  })

  const toggleSelection = (id: string): void => {
    setSelection((currentSelection) => {
      if (currentSelection.includes(id)) return currentSelection.filter((item) => item !== id)
      if (currentSelection.length >= 2) return [currentSelection[1] as string, id]
      return [...currentSelection, id]
    })
  }

  if (listQuery.isPending) {
    return <Skeleton rows={6} />
  }

  if (listQuery.isError) {
    return (
      <ErrorState
        title={t('pages.experiments.toast_history_failed')}
        message={listQuery.error instanceof Error ? listQuery.error.message : undefined}
        onRetry={() => void listQuery.refetch()}
        retryLabel={t('pages.experiments.btn_refresh')}
      />
    )
  }

  if (snapshots.length === 0) {
    return (
      <EmptyState
        title={t('pages.experiments.history_empty_title')}
        description={t('pages.experiments.history_empty_reason')}
      />
    )
  }

  return (
    <div className={styles.panel}>
      <div className={styles.rowBetween}>
        <p className={styles.reason}>{t('pages.experiments.history_append_note')}</p>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setCompareOpen(true)}
          disabled={selection.length !== 2}
        >
          {t('pages.experiments.snapshot_compare_title')}
        </Button>
      </div>

      <div className={styles.snapshotList}>
        {snapshots.map((snapshot) => (
          <div
            key={snapshot.id}
            className={[
              styles.snapshotRow,
              snapshot.id === current?.id ? styles.snapshotRowActive : ''
            ].join(' ')}
          >
            <input
              type="checkbox"
              aria-label={`${t('pages.experiments.history_col_target')} ${snapshot.id}`}
              checked={selection.includes(snapshot.id)}
              onChange={() => toggleSelection(snapshot.id)}
            />
            <button
              type="button"
              className={styles.crumbLink}
              onClick={() => setSelectedId(snapshot.id)}
            >
              <span className={styles.cellStack}>
                <span>
                  {t(kindKey(snapshot.kind))} · {formatTime(snapshot.createdAt)}
                </span>
                <span className={styles.mono}>
                  {snapshot.providerName} · {snapshot.modelId} · {shortSha(snapshot.gitCommit)}
                </span>
              </span>
            </button>
            <span className={styles.badges}>
              <Badge tone={snapshot.aiStatus === 'confirmed' ? 'success' : 'neutral'}>
                {snapshot.aiStatus}
              </Badge>
              {snapshot.humanEdited && <Badge tone="warning">{t('pages.experiments.ai_human_edited')}</Badge>}
              {snapshot.warnings > 0 && <Badge tone="warning">{snapshot.warnings}</Badge>}
              <span className={styles.muted}>{snapshot.metricCount}</span>
            </span>
          </div>
        ))}
      </div>

      {current !== null && detailQuery.isPending && <Skeleton rows={6} />}
      {current !== null && detailQuery.isError && (
        <ErrorState
          title={t('pages.experiments.toast_history_failed')}
          message={detailQuery.error instanceof Error ? detailQuery.error.message : undefined}
          onRetry={() => void detailQuery.refetch()}
          retryLabel={t('pages.experiments.btn_refresh')}
        />
      )}
      {detailQuery.data !== undefined && (
        <Card>
          <SnapshotDetailPanel detail={detailQuery.data} onOpenReference={onOpenReference} />
        </Card>
      )}

      <CompareSnapshotsDialog
        open={compareOpen}
        projectId={projectId}
        leftId={selection[0] ?? null}
        rightId={selection[1] ?? null}
        onClose={() => setCompareOpen(false)}
      />
    </div>
  )
}