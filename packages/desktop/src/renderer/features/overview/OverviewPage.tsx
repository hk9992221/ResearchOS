import { useState } from 'react'
import { useQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Archive, ArchiveRestore, LogOut, RefreshCw, SquarePen } from 'lucide-react'

import type { OverviewSnapshot, OverviewTaskCard, OverviewWidgetKind } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Menu,
  Page,
  PageHeader,
  Skeleton,
  Tabs,
  type MenuItem
} from '../../design/components'
import { Toasts, type ToastMessage } from '../../app/Toasts'
import { useT } from '../../i18n'
import { routeHref } from '../../routes'
import { queryKeys } from '../../lib/query'
import { useShellStore } from '../../state/shell'
import { overviewApi, overviewKeys } from './api'
import { OverviewBoard } from './Widgets'
import { ConnectionsView, ProvenanceView, type GraphTarget } from './provenance'
import styles from './overview.module.css'

type OverviewView = 'board' | 'provenance' | 'graph'

export interface OverviewPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

/**
 * Project Overview: the open project's 8 widgets, the
 * provenance tab and the context graph tab. Every write goes through the
 * owning module command; the page only invalidates the query roots those
 * writes change.
 */
export default function OverviewPage({ projectId }: OverviewPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const currentProjectId = useShellStore((state) => state.currentProjectId)
  const setCurrentProjectId = useShellStore((state) => state.setCurrentProjectId)
  const pid = projectId ?? currentProjectId ?? ''

  const [view, setView] = useState<OverviewView>('board')
  const [target, setTarget] = useState<GraphTarget | null>(null)
  const [refreshing, setRefreshing] = useState<OverviewWidgetKind | null>(null)
  const [widgetErrors, setWidgetErrors] = useState<
    Partial<Record<OverviewWidgetKind, string>>
  >({})
  const [quickNoteBusy, setQuickNoteBusy] = useState(false)
  const [toasts, setToasts] = useState<readonly ToastMessage[]>([])

  const query = useQuery({
    queryKey: overviewKeys.snapshot(pid),
    queryFn: () => overviewApi.snapshot(pid),
    enabled: pid !== ''
  })

  const snapshot: OverviewSnapshot | undefined = query.data

  const notify = (toast: Omit<ToastMessage, 'id'>): void => {
    setToasts((list) => [...list, { id: `overview-${Date.now()}-${list.length}`, ...toast }])
  }

  const invalidate = async (roots: readonly QueryKey[]): Promise<void> => {
    await Promise.all(
      roots.map((key) => queryClient.invalidateQueries({ queryKey: key }))
    )
  }

  const refreshWidget = async (kind: OverviewWidgetKind): Promise<void> => {
    if (refreshing !== null) return
    setRefreshing(kind)
    setWidgetErrors((current) => {
      if (current[kind] === undefined) return current
      const next = { ...current }
      delete next[kind]
      return next
    })
    try {
      const next = await overviewApi.refreshWidget(pid, kind)
      queryClient.setQueryData(overviewKeys.snapshot(pid), next)
    } catch (error) {
      // The snapshot cache is untouched, so the card keeps its last payload
      // and the error states why the refresh failed.
      setWidgetErrors((current) => ({
        ...current,
        [kind]: error instanceof Error ? error.message : String(error)
      }))
    } finally {
      setRefreshing(null)
    }
  }

  const completeTask = async (task: OverviewTaskCard): Promise<void> => {
    try {
      await overviewApi.completeTask(pid, task.id)
      notify({ tone: 'success', title: t('shell.overview.toast_task_done') })
      await invalidate([
        queryKeys.kanban.all(pid),
        queryKeys.project(pid).all,
        queryKeys.projects.all
      ])
    } catch (error) {
      notify({
        tone: 'error',
        title: t('shell.overview.toast_task_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }

  const quickNote = async (title: string): Promise<void> => {
    setQuickNoteBusy(true)
    try {
      await overviewApi.quickNote(pid, title)
      notify({ tone: 'success', title: t('shell.overview.toast_note_added') })
      await invalidate([
        queryKeys.notes.all(pid),
        queryKeys.project(pid).all,
        queryKeys.projects.all
      ])
    } catch (error) {
      notify({
        tone: 'error',
        title: t('shell.overview.toast_note_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    } finally {
      setQuickNoteBusy(false)
    }
  }

  const saveFocus = async (markdown: string): Promise<boolean> => {
    try {
      await overviewApi.updateFocus(pid, markdown)
      notify({ tone: 'success', title: t('shell.overview.toast_focus_saved') })
      await invalidate([queryKeys.project(pid).all, queryKeys.projects.all])
      return true
    } catch (error) {
      notify({
        tone: 'error',
        title: t('shell.overview.toast_focus_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
      return false
    }
  }

  const toggleArchive = async (): Promise<void> => {
    if (snapshot === undefined) return
    const archiving = snapshot.project.status === 'active'
    try {
      if (archiving) await overviewApi.setArchive(pid)
      else await overviewApi.setUnarchive(pid)
      await invalidate([queryKeys.project(pid).all, queryKeys.projects.all])
      notify({
        tone: 'success',
        title: archiving
          ? t('shell.projects.toast_archived', { name: snapshot.project.name })
          : t('shell.projects.toast_unarchived', { name: snapshot.project.name })
      })
    } catch {
      notify({ tone: 'error', title: t('shell.projects.toast_archive_fail') })
    }
  }

  if (pid === '') {
    return (
      <Page>
        <PageHeader title={t('shell.page.overview')} />
        <EmptyState
          title={t('shell.overview.empty_no_project_title')}
          description={t('shell.overview.empty_no_project_reason')}
          action={
            <Button variant="secondary" onClick={() => navigate('/projects')}>
              {t('shell.overview.btn_projects')}
            </Button>
          }
        />
      </Page>
    )
  }

  if (query.isPending) {
    return (
      <Page>
        <PageHeader
          title={t('shell.page.overview')}
          actions={
            <IconButton
              icon={RefreshCw}
              label={t('shell.overview.btn_refresh')}
              disabled
              onClick={() => {}}
            />
          }
        />
        <div className={styles.kpiStripSkeleton}>
          <Card>
            <Skeleton rows={1} />
          </Card>
          <Card>
            <Skeleton rows={1} />
          </Card>
          <Card>
            <Skeleton rows={1} />
          </Card>
          <Card>
            <Skeleton rows={1} />
          </Card>
        </div>
        <Card>
          <Skeleton rows={6} />
        </Card>
      </Page>
    )
  }

  if (query.isError && snapshot === undefined) {
    return (
      <Page>
        <PageHeader title={t('shell.page.overview')} />
        <ErrorState
          title={t('shell.overview.error_title')}
          message={
            query.error instanceof Error
              ? query.error.message
              : t('shell.overview.error_project_missing')
          }
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void query.refetch()}
        />
      </Page>
    )
  }

  if (snapshot === undefined) {
    return (
      <Page>
        <PageHeader title={t('shell.page.overview')} />
        <Skeleton rows={8} />
      </Page>
    )
  }

  const archived = snapshot.project.archived
  const exportHref = routeHref('export', { projectId: pid, entityId: pid })

  const menuItems: MenuItem[] = [
    ...(archived
      ? []
      : [
          {
            id: 'edit',
            label: t('shell.overview.btn_edit'),
            icon: SquarePen,
            onSelect: (): void => {
              navigate(`/projects?edit=${encodeURIComponent(pid)}`)
            }
          }
        ]),
    {
      id: 'archive',
      label: archived ? t('shell.overview.btn_unarchive') : t('shell.overview.btn_archive'),
      icon: archived ? ArchiveRestore : Archive,
      onSelect: (): void => {
        void toggleArchive()
      }
    },
    {
      id: 'close',
      label: t('shell.overview.btn_close'),
      icon: LogOut,
      danger: true,
      onSelect: (): void => {
        setCurrentProjectId(null)
        navigate('/')
      }
    }
  ]

  return (
    <Page>
      <PageHeader
        title={
          <>
            {snapshot.project.name}
            <span className={styles.headerBadge}>
              <Badge tone={archived ? 'neutral' : 'info'}>
                {t(archived ? 'shell.project_status.archived' : 'shell.project_status.active')}
              </Badge>
            </span>
          </>
        }
        description={snapshot.project.summary === '' ? undefined : snapshot.project.summary}
        actions={
          <>
            <IconButton
              icon={RefreshCw}
              label={t('shell.overview.btn_refresh')}
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            />
            {exportHref !== undefined && (
              <Button variant="secondary" size="sm" onClick={() => navigate(exportHref)}>
                {t('shell.overview.btn_export')}
              </Button>
            )}
            <Menu label={t('shell.overview.btn_more')} items={menuItems} />
          </>
        }
      />

      {archived && (
        <Banner tone="warning" title={t('shell.overview.readonly_title')}>
          {t('shell.overview.readonly_message')}
        </Banner>
      )}

      {query.isError && (
        <Banner tone="danger" title={t('shell.overview.banner_load_fail')}>
          {query.error instanceof Error ? query.error.message : ''}
        </Banner>
      )}

      <Tabs
        label={t('shell.page.overview')}
        items={[
          { id: 'board', label: t('shell.overview.view_board') },
          { id: 'provenance', label: t('shell.overview.view_provenance') },
          { id: 'graph', label: t('shell.overview.view_connections') }
        ]}
        value={view}
        onChange={(id) => setView(id as OverviewView)}
      />

      {view === 'board' && (
        <OverviewBoard
          projectId={pid}
          snapshot={snapshot}
          writable={snapshot.writable}
          refreshing={refreshing}
          widgetErrors={widgetErrors}
          quickNoteBusy={quickNoteBusy}
          onRefresh={(kind) => void refreshWidget(kind)}
          onCompleteTask={(task) => void completeTask(task)}
          onQuickNote={(title) => void quickNote(title)}
          onSaveFocus={saveFocus}
        />
      )}

      {view === 'provenance' && (
        <ProvenanceView
          projectId={pid}
          onInspect={(ref) => {
            setTarget(ref)
            setView('graph')
          }}
        />
      )}

      {view === 'graph' && (
        <ConnectionsView projectId={pid} target={target} onClear={() => setTarget(null)} />
      )}

      <Toasts
        toasts={toasts}
        onDismiss={(id) => setToasts((list) => list.filter((toast) => toast.id !== id))}
      />
    </Page>
  )
}