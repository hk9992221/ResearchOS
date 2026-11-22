import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { FlaskConical, RefreshCw } from 'lucide-react'

import type { ExperimentSummary } from '@researchos/shared'

import {
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Skeleton,
  Table,
  TextInput,
  Toolbar
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { Toasts } from '../../app/Toasts'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { experimentsApi } from './api'
import { CreateExperimentDialog } from './CreateExperimentDialog'
import { DirBadge, RunStatusBadge } from './StatusBadge'
import { formatTime } from './model'
import { useFeatureToasts } from './useFeatureToasts'
import styles from './experiments.module.css'

const SEARCH_DEBOUNCE_MS = 250
const PAGE_SIZE = 200

export interface ExperimentsPageProps {
  readonly projectId?: string
}

/**
 * Experiments list (ui-inventory §3.9). The table is the entry point to the
 * folder a project references; none of the columns implies the tool can write
 * or run it. Filters are local over the fetched page, exactly as the Go list
 * filtered its 500-row page.
 */
export default function ExperimentsPage({ projectId }: ExperimentsPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived, isLoading: projectLoading } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId
  const { toasts, notify, dismiss } = useFeatureToasts()

  const [searchInput, setSearchInput] = useState('')
  const [search, setSearch] = useState('')
  const [brokenOnly, setBrokenOnly] = useState(false)
  const [unanalyzedOnly, setUnanalyzedOnly] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<ExperimentSummary | null>(null)

  useEffect(() => {
    const timer = window.setTimeout(() => setSearch(searchInput), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [searchInput])

  const listQuery = useQuery({
    queryKey: queryKeys.experiments.list(resolvedProjectId ?? 'none', {
      q: search,
      limit: PAGE_SIZE
    }),
    queryFn: () =>
      experimentsApi.list(resolvedProjectId as string, { q: search, limit: PAGE_SIZE }),
    enabled: resolvedProjectId !== null
  })

  const deleteMutation = useMutation({
    mutationFn: (experimentId: string) =>
      experimentsApi.remove(resolvedProjectId as string, experimentId),
    onSuccess: async () => {
      setPendingDelete(null)
      await queryClient.invalidateQueries({
        queryKey: queryKeys.experiments.all(resolvedProjectId as string)
      })
      notify({ tone: 'success', title: t('pages.experiments.toast_deleted') })
    },
    onError: (error) => {
      setPendingDelete(null)
      notify({
        tone: 'error',
        title: t('pages.experiments.toast_delete_failed'),
        message: error instanceof Error ? error.message : undefined
      })
    }
  })

  const rows = useMemo(() => {
    const items = listQuery.data ?? []
    return items.filter((experiment) => {
      if (brokenOnly && experiment.dirStatus !== 'broken') return false
      if (unanalyzedOnly && experiment.lastAnalyzedAt !== null) return false
      return true
    })
  }, [listQuery.data, brokenOnly, unanalyzedOnly])

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

  const open = (experimentId: string): void => {
    const href = routeHref('experiment', { projectId: resolvedProjectId, entityId: experimentId })
    if (href !== undefined) navigate(href)
  }

  return (
    <Page>
      <PageHeader
        title={t('pages.experiments.title')}
        description={t('pages.experiments.empty_reason')}
        actions={
          <div className={styles.actions}>
            <Button
              variant="ghost"
              icon={RefreshCw}
              onClick={() => void listQuery.refetch()}
              disabled={listQuery.isFetching}
            >
              {t('pages.experiments.btn_refresh')}
            </Button>
            <Button
              variant="primary"
              onClick={() => setCreateOpen(true)}
              disabled={archived}
              title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
            >
              {t('pages.experiments.btn_create')}
            </Button>
          </div>
        }
      />

      {archived && (
        <Banner tone="warning" title={t('pages.experiments.readonly_title')}>
          {t('pages.experiments.readonly_message')}
        </Banner>
      )}

      <Toolbar label={t('pages.experiments.title')}>
        <div className={styles.toolbarSearch}>
          <TextInput
            label={t('pages.experiments.field_search')}
            hint={t('pages.experiments.field_search_hint')}
            value={searchInput}
            onChange={setSearchInput}
            hideLabel
            placeholder={t('pages.experiments.field_search')}
            type="search"
          />
        </div>
        <div className={styles.filters}>
          <label className={styles.checkRow}>
            <input
              type="checkbox"
              checked={brokenOnly}
              onChange={(event) => setBrokenOnly(event.target.checked)}
            />
            {t('pages.experiments.filter_broken')}
          </label>
          <label className={styles.checkRow}>
            <input
              type="checkbox"
              checked={unanalyzedOnly}
              onChange={(event) => setUnanalyzedOnly(event.target.checked)}
            />
            {t('pages.experiments.filter_unanalyzed')}
          </label>
        </div>
      </Toolbar>

      {projectLoading && listQuery.isPending ? (
        <Skeleton rows={6} />
      ) : listQuery.isError ? (
        <ErrorState
          title={t('pages.experiments.disabled_title')}
          message={
            listQuery.error instanceof Error
              ? listQuery.error.message
              : t('pages.experiments.disabled_reason')
          }
          onRetry={() => void listQuery.refetch()}
          retryLabel={t('pages.experiments.btn_refresh')}
        />
      ) : listQuery.isPending ? (
        <Skeleton rows={6} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={FlaskConical}
          title={t('pages.experiments.empty_title')}
          description={t('pages.experiments.empty_reason')}
          action={
            <Button variant="primary" onClick={() => setCreateOpen(true)} disabled={archived}>
              {t('pages.experiments.btn_create')}
            </Button>
          }
        />
      ) : (
        <Table
          label={t('pages.experiments.title')}
          columns={[
            t('pages.experiments.col_title'),
            t('pages.experiments.col_dir'),
            t('pages.experiments.col_last_run'),
            t('pages.experiments.col_analyzed'),
            t('pages.experiments.col_ideas'),
            t('pages.experiments.col_updated'),
            t('pages.experiments.col_actions')
          ]}
        >
          {rows.map((experiment) => (
            <tr key={experiment.id}>
              <td>
                <div className={styles.cellStack}>
                  <button
                    type="button"
                    className={styles.crumbLink}
                    onClick={() => open(experiment.id)}
                  >
                    {experiment.title}
                  </button>
                </div>
              </td>
              <td>
                <div className={styles.cellStack}>
                  <DirBadge status={experiment.dirStatus ?? 'unbound'} />
                  {experiment.dirStatus === 'broken' && experiment.dirReason !== '' && (
                    <span className={styles.reason}>
                      {t('pages.experiments.dir_reason', { reason: experiment.dirReason })}
                    </span>
                  )}
                </div>
              </td>
              <td>
                <div className={styles.badges}>
                  <RunStatusBadge status={experiment.lastRunStatus} />
                  {experiment.runCount > 0 && (
                    <span className={styles.muted}>{experiment.runCount}</span>
                  )}
                </div>
              </td>
              <td className={styles.muted}>
                {experiment.lastAnalyzedAt === null
                  ? t('pages.experiments.value_never')
                  : formatTime(experiment.lastAnalyzedAt)}
              </td>
              <td>{experiment.linkedIdeas}</td>
              <td className={styles.muted}>{formatTime(experiment.updatedAt)}</td>
              <td>
                <div className={styles.actions}>
                  <Button variant="ghost" size="sm" onClick={() => open(experiment.id)}>
                    {t('pages.experiments.action_open')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setPendingDelete(experiment)}
                    disabled={archived}
                    title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
                  >
                    {t('pages.experiments.action_delete')}
                  </Button>
                </div>
              </td>
            </tr>
          ))}
        </Table>
      )}

      <CreateExperimentDialog
        open={createOpen}
        projectId={resolvedProjectId}
        onClose={() => setCreateOpen(false)}
        onCreated={(experiment) => {
          setCreateOpen(false)
          notify({ tone: 'success', title: t('pages.experiments.toast_created') })
          open(experiment.id)
        }}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title={t('pages.experiments.delete_title')}
        description={t('pages.experiments.delete_message', {
          title: pendingDelete?.title ?? ''
        })}
        confirmLabel={t('pages.experiments.delete_confirm')}
        cancelLabel={t('pages.experiments.btn_back')}
        tone="danger"
        busy={deleteMutation.isPending}
        onCancel={() => setPendingDelete(null)}
        onConfirm={() => {
          if (pendingDelete !== null) deleteMutation.mutate(pendingDelete.id)
        }}
      />

      <Toasts toasts={toasts} onDismiss={dismiss} />
    </Page>
  )
}