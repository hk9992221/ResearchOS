import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'

import type { TargetRef } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Rows,
  Row,
  Skeleton,
  Tabs
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { Toasts } from '../../app/Toasts'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { experimentsApi } from './api'
import { AnalysisTab } from './AnalysisTab'
import { RunDialog } from './RunDialog'
import { RunStatusBadge } from './StatusBadge'
import { SnapshotsTab } from './SnapshotsTab'
import { formatTime, parseRosTarget, rosHrefFor } from './model'
import { useFeatureToasts } from './useFeatureToasts'
import styles from './experiments.module.css'

type RunTab = 'results' | 'snapshots'

export interface RunDetailPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

/**
 * The run route. A run belongs to an experiment and the contract has no
 * `runs.get`, so the experiment id travels in `?experiment=` from the runs tab;
 * the list of that experiment then resolves the run. A direct link without the
 * hint says so instead of guessing.
 */
export default function RunDetailPage({ projectId, entityId }: RunDetailPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId
  const [searchParams] = useSearchParams()
  const experimentId = searchParams.get('experiment')
  const [tab, setTab] = useState<RunTab>('results')
  const [editOpen, setEditOpen] = useState(false)
  const { toasts, notify, dismiss } = useFeatureToasts()

  const detailQuery = useQuery({
    queryKey: queryKeys.experiments.detail(resolvedProjectId ?? 'none', experimentId ?? 'none'),
    queryFn: () => experimentsApi.get(resolvedProjectId as string, experimentId as string),
    enabled: resolvedProjectId !== null && experimentId !== null
  })

  const run = useMemo(
    () => detailQuery.data?.runs.find((candidate) => candidate.id === entityId) ?? null,
    [detailQuery.data, entityId]
  )

  const target = useMemo<TargetRef>(() => ({ kind: 'run', id: entityId ?? '' }), [entityId])

  const openExperiment = (): void => {
    if (resolvedProjectId === null || experimentId === null) return
    const href = routeHref('experiment', {
      projectId: resolvedProjectId,
      entityId: experimentId
    })
    if (href !== undefined) navigate(href)
  }

  const openReference = (href: string): void => {
    if (resolvedProjectId === null) return
    const parsed = parseRosTarget(href)
    if (parsed === null) return
    if (parsed.type === 'run') {
      const runHref = routeHref('run', { projectId: resolvedProjectId, entityId: parsed.id })
      if (runHref !== undefined) navigate(`${runHref}?experiment=${encodeURIComponent(experimentId ?? '')}`)
      return
    }
    const route = rosHrefFor(parsed, resolvedProjectId)
    if (route !== null) navigate(route)
  }

  if (resolvedProjectId === null) {
    return (
      <Page>
        <EmptyState
          title={t('pages.experiments.needs_project_title')}
          description={t('pages.experiments.needs_project_reason')}
        />
      </Page>
    )
  }

  if (entityId === undefined || experimentId === null) {
    return (
      <Page>
        <EmptyState
          title={t('pages.experiments.notfound_title')}
          description={t('pages.experiments.notfound_reason')}
        />
      </Page>
    )
  }

  if (detailQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.experiments.detail_title')} />
        <Skeleton rows={8} />
      </Page>
    )
  }

  if (detailQuery.isError || run === null || detailQuery.data === undefined) {
    return (
      <Page>
        <PageHeader title={t('pages.experiments.detail_title')} />
        <ErrorState
          title={t('pages.experiments.notfound_title')}
          message={
            detailQuery.error instanceof Error ? detailQuery.error.message : undefined
          }
          onRetry={() => void detailQuery.refetch()}
          retryLabel={t('pages.experiments.btn_refresh')}
        />
      </Page>
    )
  }

  const detail = detailQuery.data
  const experiment = detail.experiment
  const paramEntries = Object.entries(run.params).sort(([left], [right]) =>
    left.localeCompare(right)
  )

  return (
    <Page>
      <div className={styles.detailHeader}>
        <PageHeader
          title={run.displayName || run.title || t('pages.experiments.run_unnamed')}
          description={t('pages.experiments.run_banner_message')}
          actions={
            <div className={styles.actions}>
              <Button variant="secondary" onClick={openExperiment}>
                {t('pages.experiments.btn_back')}
              </Button>
              <Button
                variant="primary"
                onClick={() => setEditOpen(true)}
                disabled={archived}
                title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
              >
                {t('pages.experiments.btn_edit')}
              </Button>
            </div>
          }
        />
        <div className={styles.breadcrumbs}>
          <button type="button" className={styles.crumbLink} onClick={openExperiment}>
            {experiment.title}
          </button>
          <span aria-hidden="true">/</span>
          <span>{run.displayName || run.title}</span>
        </div>
      </div>

      {archived && (
        <Banner tone="warning" title={t('pages.experiments.readonly_title')}>
          {t('pages.experiments.readonly_message')}
        </Banner>
      )}

      <Card title={t('pages.experiments.results_card_title')}>
        <div className={styles.stack}>
          <div className={styles.badges}>
            <RunStatusBadge status={run.status} />
            <Badge tone="neutral">{run.paramCount}</Badge>
            <span className={styles.muted}>
              {t('pages.experiments.col_analyzed')}: {formatTime(run.lastAnalyzedAt)}
            </span>
          </div>
          <Rows>
            <Row
              label={t('pages.experiments.run_dir_label')}
              value={run.dirRef ?? t('pages.experiments.run_dir_inherit')}
              mono={run.dirRef !== null}
            />
            {paramEntries.length === 0 ? (
              <Row
                label={t('pages.experiments.run_params_label')}
                value={t('pages.experiments.params_none')}
              />
            ) : (
              paramEntries.map(([key, value]) => (
                <Row key={key} label={key} value={value.value} mono />
              ))
            )}
          </Rows>
        </div>
      </Card>

      <Tabs
        label={t('pages.experiments.run_banner_title', { name: run.displayName || run.title })}
        items={[
          { id: 'results', label: t('pages.experiments.tab_results') },
          { id: 'snapshots', label: t('pages.experiments.tab_history') }
        ]}
        value={tab}
        onChange={(value) => setTab(value as RunTab)}
      />

      {tab === 'results' && (
        <AnalysisTab
          projectId={resolvedProjectId}
          target={target}
          mode="results"
          experiment={experiment}
          archived={archived}
          onOpenReference={openReference}
          onOpenSource={() => {
            const href = routeHref('experiment', {
              projectId: resolvedProjectId,
              entityId: experiment.id
            })
            if (href !== undefined) navigate(`${href}?view=scan`)
          }}
          notify={notify}
        />
      )}

      {tab === 'snapshots' && (
        <SnapshotsTab projectId={resolvedProjectId} target={target} onOpenReference={openReference} />
      )}

      <RunDialog
        open={editOpen}
        projectId={resolvedProjectId}
        experimentId={experiment.id}
        experimentDirRef={experiment.dirRef}
        run={run}
        onClose={() => setEditOpen(false)}
        onSaved={() => {
          setEditOpen(false)
          void queryClient.invalidateQueries({
            queryKey: queryKeys.experiments.detail(resolvedProjectId, experiment.id)
          })
        }}
        notify={notify}
      />

      <Toasts toasts={toasts} onDismiss={dismiss} />
    </Page>
  )
}