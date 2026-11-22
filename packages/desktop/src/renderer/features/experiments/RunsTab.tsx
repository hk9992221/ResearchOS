import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Plus } from 'lucide-react'

import type { ExperimentDetail, RunSummary } from '@researchos/shared'

import { Badge, Button, Card, EmptyState } from '../../design/components'
import { useT } from '../../i18n'
import { routeHref } from '../../routes'
import { RunStatusBadge } from './StatusBadge'
import { RunDialog } from './RunDialog'
import { formatTime } from './model'
import styles from './experiments.module.css'

export interface RunsTabProps {
  readonly projectId: string
  readonly detail: ExperimentDetail
  readonly archived: boolean
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

function paramLine(run: RunSummary): string {
  const entries = Object.entries(run.params).sort(([left], [right]) => left.localeCompare(right))
  if (entries.length === 0) return ''
  return entries.map(([key, value]) => `${key}=${value.value}`).join('  ')
}

/**
 * Runs of the experiment. A run is a named parameter set pointing at a
 * directory; nothing is executed. The full parameter list is shown (wrapped),
 * never truncated to a summary that hides a value.
 */
export function RunsTab({ projectId, detail, archived, notify }: RunsTabProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<RunSummary | null>(null)

  const openRun = (runId: string): void => {
    const href = routeHref('run', { projectId, entityId: runId })
    if (href === undefined) return
    navigate(`${href}?experiment=${encodeURIComponent(detail.experiment.id)}`)
  }

  return (
    <div className={styles.panel}>
      <Card
        title={t('pages.experiments.card_runs')}
        actions={
          <Button
            variant="primary"
            size="sm"
            icon={Plus}
            onClick={() => {
              setEditing(null)
              setDialogOpen(true)
            }}
            disabled={archived}
            title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
          >
            {t('pages.experiments.btn_new_run')}
          </Button>
        }
      >
        {detail.runs.length === 0 ? (
          <EmptyState
            title={t('pages.experiments.runs_empty_title')}
            description={t('pages.experiments.runs_empty_reason')}
            compact
          />
        ) : (
          <div className={styles.runList}>
            {detail.runs.map((run) => (
              <div key={run.id} className={styles.runCard}>
                <div className={styles.rowBetween}>
                  <div className={styles.cellStack}>
                    <span className={styles.cellTitle}>{run.displayName || run.title}</span>
                    <span className={styles.muted}>
                      {t('pages.experiments.col_analyzed')}: {formatTime(run.lastAnalyzedAt)}
                    </span>
                  </div>
                  <div className={styles.badges}>
                    <RunStatusBadge status={run.status} />
                    <Badge tone="neutral">{run.paramCount}</Badge>
                  </div>
                </div>
                <div className={styles.runParams}>
                  {paramLine(run) === '' ? t('pages.experiments.params_none') : paramLine(run)}
                </div>
                {run.dirRef !== null && <span className={styles.mono}>{run.dirRef}</span>}
                <div className={styles.actions}>
                  <Button variant="ghost" size="sm" onClick={() => openRun(run.id)}>
                    {t('pages.experiments.action_open')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      setEditing(run)
                      setDialogOpen(true)
                    }}
                    disabled={archived}
                    title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
                  >
                    {t('pages.experiments.btn_edit')}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      <RunDialog
        open={dialogOpen}
        projectId={projectId}
        experimentId={detail.experiment.id}
        experimentDirRef={detail.experiment.dirRef}
        run={editing}
        onClose={() => setDialogOpen(false)}
        onSaved={() => setDialogOpen(false)}
        notify={notify}
      />
    </div>
  )
}