import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { FlaskConical, RefreshCw } from 'lucide-react'

import type { ExperimentDetail, TargetRef } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
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
import { MatrixTab } from './MatrixTab'
import { OverviewTab } from './OverviewTab'
import { RelinkDialog } from './RelinkDialog'
import { RunsTab } from './RunsTab'
import { ScanTab } from './ScanTab'
import { SnapshotsTab } from './SnapshotsTab'
import { DirBadge } from './StatusBadge'
import { formatTime, parseRosTarget, rosHrefFor } from './model'
import { useFeatureToasts } from './useFeatureToasts'
import styles from './experiments.module.css'

type DetailTab = 'overview' | 'scan' | 'outline' | 'results' | 'runs' | 'snapshots' | 'matrix'

const TAB_ORDER: readonly DetailTab[] = [
  'overview',
  'scan',
  'outline',
  'results',
  'runs',
  'snapshots',
  'matrix'
]

function initialTab(view: string | null): DetailTab {
  return TAB_ORDER.find((tab) => tab === view) ?? 'overview'
}

export interface ExperimentDetailPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

/**
 * Experiment detail (ui-inventory §3.10, reorganised into the tabs the Electron
 * port exposes). The page reads the experiment, binds its folder and runs the
 * deterministic scan/AI flows; every tab keeps the read-only promise.
 */
export default function ExperimentDetailPage({
  projectId,
  entityId
}: ExperimentDetailPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived, isLoading: projectLoading } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId
  const [searchParams] = useSearchParams()
  const [tab, setTab] = useState<DetailTab>(() => initialTab(searchParams.get('view')))
  const [relinkOpen, setRelinkOpen] = useState(false)
  const [scanSelection, setScanSelection] = useState<string | null>(null)
  const { toasts, notify, dismiss } = useFeatureToasts()

  const detailQuery = useQuery({
    queryKey: queryKeys.experiments.detail(resolvedProjectId ?? 'none', entityId ?? 'none'),
    queryFn: () =>
      experimentsApi.get(resolvedProjectId as string, entityId as string),
    enabled: resolvedProjectId !== null && entityId !== undefined
  })

  const target = useMemo<TargetRef>(
    () => ({ kind: 'experiment', id: entityId ?? '' }),
    [entityId]
  )

  useEffect(() => {
    const view = searchParams.get('view')
    if (view !== null) setTab(initialTab(view))
  }, [searchParams])

  const goToList = (): void => {
    const href = routeHref('experiments', { projectId: resolvedProjectId ?? undefined })
    if (href !== undefined) navigate(href)
  }

  const openReference = (href: string): void => {
    if (resolvedProjectId === null) return
    const parsed = parseRosTarget(href)
    if (parsed === null) return
    if (parsed.type === 'run') {
      const runHref = routeHref('run', { projectId: resolvedProjectId, entityId: parsed.id })
      if (runHref !== undefined) navigate(`${runHref}?experiment=${encodeURIComponent(entityId ?? '')}`)
      return
    }
    const target = rosHrefFor(parsed, resolvedProjectId)
    if (target !== null) navigate(target)
  }

  if (resolvedProjectId === null) {
    return (
      <Page>
        <EmptyState
          icon={FlaskConical}
          title={t('pages.experiments.needs_project_title')}
          description={t('pages.experiments.needs_project_reason')}
        />
      </Page>
    )
  }

  if (entityId === undefined) {
    return (
      <Page>
        <EmptyState title={t('pages.experiments.notfound_title')} description={t('pages.experiments.notfound_reason')} />
      </Page>
    )
  }

  if (projectLoading && detailQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.experiments.detail_title')} />
        <Skeleton rows={8} />
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

  if (detailQuery.isError) {
    return (
      <Page>
        <PageHeader title={t('pages.experiments.detail_title')} />
        <ErrorState
          title={t('pages.experiments.notfound_title')}
          message={detailQuery.error instanceof Error ? detailQuery.error.message : undefined}
          onRetry={() => void detailQuery.refetch()}
          retryLabel={t('pages.experiments.btn_refresh')}
        />
        <Button variant="secondary" onClick={goToList}>
          {t('pages.experiments.btn_back_list')}
        </Button>
      </Page>
    )
  }

  const detail: ExperimentDetail = detailQuery.data
  const experiment = detail.experiment
  const dirStatus =
    experiment.dirRef === null
      ? 'unbound'
      : detail.dirRefs.some((ref) => ref.status === 'broken')
        ? 'broken'
        : 'ok'
  const latest = detail.recentSnapshots[0] ?? null

  const tabs = [
    { id: 'overview', label: t('pages.experiments.tab_overview') },
    { id: 'scan', label: t('pages.experiments.tab_files') },
    { id: 'outline', label: t('pages.experiments.tab_outline') },
    { id: 'results', label: t('pages.experiments.tab_results') },
    { id: 'runs', label: t('pages.experiments.card_runs') },
    { id: 'snapshots', label: t('pages.experiments.tab_history') },
    { id: 'matrix', label: t('pages.experiments.tab_matrix') }
  ]

  return (
    <Page>
      <div className={styles.detailHeader}>
        <PageHeader
          title={experiment.title}
          description={experiment.purpose.trim() === '' ? undefined : experiment.purpose}
          actions={
            <div className={styles.actions}>
              <Button
                variant="ghost"
                icon={RefreshCw}
                onClick={() => void detailQuery.refetch()}
                disabled={detailQuery.isFetching}
              >
                {t('pages.experiments.btn_refresh')}
              </Button>
            </div>
          }
        />
        <div className={styles.breadcrumbs}>
          <button type="button" className={styles.crumbLink} onClick={goToList}>
            {t('pages.experiments.title')}
          </button>
          <span aria-hidden="true">/</span>
          <span>{experiment.title}</span>
        </div>
      </div>

      <div className={styles.statusBar}>
        <span className={styles.badges}>
          <DirBadge status={dirStatus} />
          {experiment.dirRef !== null && <span className={styles.mono}>{experiment.dirRef}</span>}
        </span>
        {latest !== null && (
          <span>
            {t('pages.experiments.bar_analysis', { value: formatTime(latest.createdAt) })}
          </span>
        )}
        <Badge tone="neutral">
          {t('pages.experiments.about_counts', {
            runs: detail.runs.length,
            ideas: detail.linkedIdeas.length,
            snapshots: detail.recentSnapshots.length
          })}
        </Badge>
      </div>

      {archived && (
        <Banner tone="warning" title={t('pages.experiments.readonly_title')}>
          {t('pages.experiments.readonly_message')}
        </Banner>
      )}

      <Banner tone="info" title={t('pages.experiments.nonintrusive_title')}>
        {t('pages.experiments.nonintrusive_message')}
      </Banner>

      <Tabs
        label={t('pages.experiments.detail_title')}
        items={tabs}
        value={tab}
        onChange={(value) => setTab(value as DetailTab)}
      />

      {tab === 'overview' && (
        <OverviewTab
          projectId={resolvedProjectId}
          detail={detail}
          archived={archived}
          onRelink={() => setRelinkOpen(true)}
          onOpenRules={() => setTab('scan')}
        />
      )}

      {tab === 'scan' && (
        <ScanTab
          projectId={resolvedProjectId}
          target={target}
          experiment={experiment}
          archived={archived}
          selectedPath={scanSelection}
          onSelectPath={setScanSelection}
          notify={notify}
        />
      )}

      {tab === 'outline' && (
        <AnalysisTab
          projectId={resolvedProjectId}
          target={target}
          mode="outline"
          experiment={experiment}
          archived={archived}
          onOpenReference={openReference}
          onOpenSource={(path) => {
            setScanSelection(path)
            setTab('scan')
          }}
          notify={notify}
        />
      )}

      {tab === 'results' && (
        <AnalysisTab
          projectId={resolvedProjectId}
          target={target}
          mode="results"
          experiment={experiment}
          archived={archived}
          onOpenReference={openReference}
          onOpenSource={(path) => {
            setScanSelection(path)
            setTab('scan')
          }}
          notify={notify}
        />
      )}

      {tab === 'runs' && (
        <RunsTab
          projectId={resolvedProjectId}
          detail={detail}
          archived={archived}
          notify={notify}
        />
      )}

      {tab === 'snapshots' && (
        <SnapshotsTab
          projectId={resolvedProjectId}
          target={target}
          onOpenReference={openReference}
        />
      )}

      {tab === 'matrix' && (
        <MatrixTab projectId={resolvedProjectId} experiment={experiment} notify={notify} />
      )}

      <RelinkDialog
        open={relinkOpen}
        projectId={resolvedProjectId}
        experiment={experiment}
        onClose={() => setRelinkOpen(false)}
        onSaved={() => {
          setRelinkOpen(false)
          notify({ tone: 'success', title: t('pages.experiments.toast_dir_saved') })
          void queryClient.invalidateQueries({
            queryKey: queryKeys.experiments.detail(resolvedProjectId, experiment.id)
          })
        }}
      />

      <Toasts toasts={toasts} onDismiss={dismiss} />
    </Page>
  )
}