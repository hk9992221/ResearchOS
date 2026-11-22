/**
 * Literature browser. Two tabs share the route: project papers (search,
 * filters, per-paper and batch unlink, import, sync, metadata refresh) and the
 * overview editor. Writes are archived-gated; a batch is one call per paper so
 * each outcome is reported separately.
 */

import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { PaperFileState, ProjectPaper } from '@researchos/shared'

import {
  Banner,
  Button,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  SelectField,
  Skeleton,
  Tabs,
  TextInput
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { pushToast } from '../../state/toasts'
import { ProjectPapersTable } from './ProjectPapersTable'
import { UnlinkDialog, type UnlinkOutcome } from './UnlinkDialog'
import { literatureApi } from './api'
import { waitForJob } from './model'
import styles from './literature.module.css'

const OverviewEditor = lazy(() =>
  import('./OverviewEditor').then((module) => ({ default: module.OverviewEditor }))
)
const ImportWizard = lazy(() =>
  import('./ImportWizard').then((module) => ({ default: module.ImportWizard }))
)
const SyncDialog = lazy(() =>
  import('./SyncDialog').then((module) => ({ default: module.SyncDialog }))
)

const PAGE_SIZE = 50
const SEARCH_DEBOUNCE_MS = 250

type Tab = 'list' | 'overview'
type NoteFilter = 'all' | 'with' | 'without'

export default function LiteraturePage({
  projectId
}: {
  readonly projectId?: string
}): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived, isLoading: projectLoading } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId

  const [tab, setTab] = useState<Tab>('list')
  const [searchInput, setSearchInput] = useState('')
  const [search, setSearch] = useState('')
  const [fileFilter, setFileFilter] = useState<'all' | PaperFileState>('all')
  const [noteFilter, setNoteFilter] = useState<NoteFilter>('all')
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [unlinkTargets, setUnlinkTargets] = useState<readonly ProjectPaper[] | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [syncOpen, setSyncOpen] = useState(false)
  const [refreshing, setRefreshing] = useState(false)

  useEffect(() => {
    const timer = window.setTimeout(() => setSearch(searchInput), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [searchInput])

  useEffect(() => {
    setSelected(new Set())
  }, [search, fileFilter, noteFilter, offset])

  const params = useMemo(
    () => ({
      q: search,
      fileState: fileFilter === 'all' ? undefined : fileFilter,
      hasNote: noteFilter === 'all' ? undefined : noteFilter === 'with',
      limit: PAGE_SIZE,
      offset
    }),
    [search, fileFilter, noteFilter, offset]
  )

  const papersQuery = useQuery({
    queryKey: queryKeys.literature.papers(resolvedProjectId ?? 'none', params),
    queryFn: () =>
      literatureApi.papers({
        projectId: resolvedProjectId as string,
        ...(params.q === '' ? {} : { q: params.q }),
        ...(params.fileState === undefined ? {} : { fileState: params.fileState }),
        ...(params.hasNote === undefined ? {} : { hasNote: params.hasNote }),
        limit: PAGE_SIZE,
        offset
      }),
    enabled: resolvedProjectId !== null
  })

  if (resolvedProjectId === null) {
    return (
      <Page>
        <EmptyState
          title={t('pages.literature.needs_project_title')}
          description={t('pages.literature.needs_project_reason')}
        />
      </Page>
    )
  }

  if (projectLoading && !papersQuery.isSuccess) {
    return (
      <Page>
        <PageHeader title={t('pages.literature.title')} />
        <Skeleton rows={7} />
      </Page>
    )
  }

  const papers = papersQuery.data?.items ?? []
  const total = papersQuery.data?.total ?? 0
  const hasFilters = search !== '' || fileFilter !== 'all' || noteFilter !== 'all'
  const from = total === 0 ? 0 : offset + 1
  const to = Math.min(offset + papers.length, total)
  const selectedPapers = papers.filter((paper) => selected.has(paper.id))

  const openPaper = (paper: ProjectPaper): void => {
    const href = routeHref('paper', { projectId: resolvedProjectId, entityId: paper.id })
    if (href !== undefined) navigate(href)
  }

  const openPdf = (paper: ProjectPaper): void => {
    const href = routeHref('pdf', { projectId: resolvedProjectId, entityId: paper.id })
    if (href !== undefined) navigate(href)
  }

  const refreshMetadata = async (): Promise<void> => {
    setRefreshing(true)
    try {
      const started = await literatureApi.refreshMetadata(resolvedProjectId)
      const job = await waitForJob(started.jobId, literatureApi.job)
      if (job.state === 'succeeded') {
        pushToast({ tone: 'success', title: t('pages.literature.toast_refresh_done') })
        await queryClient.invalidateQueries({ queryKey: queryKeys.literature.all(resolvedProjectId) })
      } else {
        pushToast({
          tone: 'error',
          title: t('pages.literature.toast_refresh_fail'),
          ...(job.error === null ? {} : { message: job.error.message })
        })
      }
    } catch (cause) {
      pushToast({
        tone: 'error',
        title: t('pages.literature.toast_refresh_fail'),
        ...(cause instanceof Error ? { message: cause.message } : {})
      })
    } finally {
      setRefreshing(false)
    }
  }

  const onUnlinkSettled = (outcomes: readonly UnlinkOutcome[]): void => {
    const failed = outcomes.filter((outcome) => !outcome.ok).length
    if (failed === 0) {
      pushToast({ tone: 'success', title: t('pages.literature.toast_deleted', { count: outcomes.length }) })
    } else if (failed === outcomes.length) {
      pushToast({ tone: 'error', title: t('pages.literature.toast_delete_fail') })
    } else {
      pushToast({ tone: 'warning', title: t('pages.literature.toast_delete_partial', { failed }) })
    }
    setSelected(new Set())
    void queryClient.invalidateQueries({ queryKey: queryKeys.literature.all(resolvedProjectId) })
  }

  const headerActions =
    tab === 'list' ? (
      <>
        <Button
          variant="secondary"
          disabled={archived || refreshing}
          onClick={() => void refreshMetadata()}
          aria-busy={refreshing}
        >
          {t('pages.literature.btn_refresh_metadata')}
        </Button>
        <Button variant="secondary" disabled={archived} onClick={() => setSyncOpen(true)}>
          {t('pages.literature.btn_sync_check')}
        </Button>
        <Button variant="primary" disabled={archived} onClick={() => setImportOpen(true)}>
          {t('pages.literature.btn_import')}
        </Button>
      </>
    ) : undefined

  return (
    <Page>
      <PageHeader title={t('pages.literature.title')} actions={headerActions} />
      {archived && <Banner tone="warning">{t('pages.literature.archived_banner')}</Banner>}

      <Tabs
        label={t('pages.literature.tab_label')}
        items={[
          { id: 'list', label: t('pages.literature.tab_list') },
          { id: 'overview', label: t('pages.literature.tab_overview') }
        ]}
        value={tab}
        onChange={(id) => setTab(id as Tab)}
      />

      {tab === 'overview' ? (
        <Suspense fallback={<Skeleton rows={10} />}>
          <OverviewEditor projectId={resolvedProjectId} archived={archived} />
        </Suspense>
      ) : (
        <div className={styles.tabPanel}>
          <div className={styles.toolbar}>
            <div className={styles.grow}>
              <TextInput
                label={t('pages.literature.field_search')}
                hideLabel
                value={searchInput}
                placeholder={t('pages.literature.field_search_hint')}
                onChange={(value) => {
                  setSearchInput(value)
                  setOffset(0)
                }}
              />
            </div>
            <SelectField
              label={t('pages.literature.filter_file_state')}
              value={fileFilter}
              options={[
                { value: 'all', label: t('pages.literature.filter_any') },
                { value: 'ok', label: t('pages.literature.copy_present') },
                { value: 'missing', label: t('pages.literature.copy_missing') }
              ]}
              onChange={(value) => {
                setFileFilter(value as 'all' | PaperFileState)
                setOffset(0)
              }}
            />
            <SelectField
              label={t('pages.literature.filter_note')}
              value={noteFilter}
              options={[
                { value: 'all', label: t('pages.literature.filter_any') },
                { value: 'with', label: t('pages.literature.note_present') },
                { value: 'without', label: t('pages.literature.note_absent') }
              ]}
              onChange={(value) => {
                setNoteFilter(value as NoteFilter)
                setOffset(0)
              }}
            />
            <span className={styles.count}>
              {t('pages.literature.pager', { from, to, total })}
            </span>
          </div>

          {papersQuery.isPending ? (
            <Skeleton rows={7} />
          ) : papersQuery.isError ? (
            <ErrorState
              title={t('pages.literature.load_fail')}
              message={papersQuery.error instanceof Error ? papersQuery.error.message : undefined}
              onRetry={() => void papersQuery.refetch()}
              retryLabel={t('pages.literature.btn_retry')}
            />
          ) : papers.length === 0 ? (
            <EmptyState
              title={t(hasFilters ? 'pages.literature.filter_empty_title' : 'pages.literature.project_empty_title')}
              description={t(
                hasFilters ? 'pages.literature.filter_empty_reason' : 'pages.literature.project_empty_reason'
              )}
              action={
                hasFilters ? undefined : (
                  <Button variant="primary" disabled={archived} onClick={() => setImportOpen(true)}>
                    {t('pages.literature.btn_import')}
                  </Button>
                )
              }
            />
          ) : (
            <>
              <ProjectPapersTable
                papers={papers}
                selected={selected}
                onToggle={(paperId) =>
                  setSelected((current) => {
                    const next = new Set(current)
                    if (next.has(paperId)) next.delete(paperId)
                    else next.add(paperId)
                    return next
                  })
                }
                onTogglePage={(checked) =>
                  setSelected(checked ? new Set(papers.map((paper) => paper.id)) : new Set())
                }
                onOpenPaper={openPaper}
                onOpenPdf={openPdf}
                onUnlink={(paper) => setUnlinkTargets([paper])}
              />
              <div className={styles.batchRow}>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={archived || selectedPapers.length === 0}
                  onClick={() => setUnlinkTargets(selectedPapers)}
                >
                  {t('pages.literature.btn_delete_selected', { count: selectedPapers.length })}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={selected.size === 0}
                  onClick={() => setSelected(new Set())}
                >
                  {t('pages.literature.btn_clear_selection')}
                </Button>
                <span className={styles.subtle}>
                  {t('pages.literature.project_count', { count: total })}
                </span>
                <div className={styles.grow} />
                <div className={styles.footerRow}>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={offset === 0}
                    onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                  >
                    {t('pages.literature.btn_prev')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={offset + PAGE_SIZE >= total}
                    onClick={() => setOffset(offset + PAGE_SIZE)}
                  >
                    {t('pages.literature.btn_next')}
                  </Button>
                </div>
              </div>
            </>
          )}
        </div>
      )}

      {importOpen && (
        <Suspense fallback={null}>
          <ImportWizard
            open
            projectId={resolvedProjectId}
            onClose={() => setImportOpen(false)}
            onImported={() => {
              void queryClient.invalidateQueries({
                queryKey: queryKeys.literature.all(resolvedProjectId)
              })
            }}
          />
        </Suspense>
      )}

      {syncOpen && (
        <Suspense fallback={null}>
          <SyncDialog
            open
            projectId={resolvedProjectId}
            onClose={() => setSyncOpen(false)}
            onApplied={() => {
              void queryClient.invalidateQueries({
                queryKey: queryKeys.literature.all(resolvedProjectId)
              })
            }}
          />
        </Suspense>
      )}

      <UnlinkDialog
        open={unlinkTargets !== null}
        projectId={resolvedProjectId}
        papers={unlinkTargets ?? []}
        onClose={() => setUnlinkTargets(null)}
        onSettled={onUnlinkSettled}
      />
    </Page>
  )
}