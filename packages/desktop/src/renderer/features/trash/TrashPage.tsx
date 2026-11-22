import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  Banner,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  PageHeader,
  SelectField,
  Skeleton,
  Table
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { trashApi, type TrashEntry, type TrashEntityType, type TrashPlan } from './trash-api'

const TYPE_LABEL: Record<TrashEntityType, string> = {
  note: 'pages.trash.type_note',
  paper: 'pages.trash.type_paper',
  idea: 'pages.trash.type_idea',
  experiment: 'pages.trash.type_experiment',
  run: 'pages.trash.type_run',
  task: 'pages.trash.type_task',
  weekly_report: 'pages.trash.type_weekly_report'
}

const FILTERS: readonly { value: string; key: string }[] = [
  { value: 'all', key: 'pages.trash.filter_all' },
  { value: 'note', key: 'pages.trash.filter_notes' },
  { value: 'paper', key: 'pages.trash.filter_papers' },
  { value: 'idea', key: 'pages.trash.filter_ideas' },
  { value: 'experiment', key: 'pages.trash.filter_experiments' },
  { value: 'task', key: 'pages.trash.filter_tasks' },
  { value: 'weekly_report', key: 'pages.trash.filter_weekly_reports' }
]

interface PendingPlan {
  readonly kind: 'purge' | 'empty'
  readonly plan: TrashPlan
}

function entryKey(entry: TrashEntry): string {
  return `${entry.type}:${entry.id}`
}

/** Recycle bin: filter, multi-select restore, token-confirmed purge/empty. */
export default function TrashPage(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const { projectId, archived } = useCurrentProject()

  const [filter, setFilter] = useState('all')
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [pending, setPending] = useState<PendingPlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: queryKeys.trash.list(projectId ?? 'none'),
    queryFn: () => trashApi.list(projectId as string),
    enabled: projectId !== null
  })

  const entries = listQuery.data ?? []
  const visible = useMemo(
    () => (filter === 'all' ? entries : entries.filter((entry) => entry.type === filter)),
    [entries, filter]
  )
  const visibleKeys = visible.map(entryKey)
  const selectedEntries = entries.filter((entry) => selected.has(entryKey(entry)))

  if (projectId === null) {
    return (
      <Page>
        <PageHeader title={t('pages.trash.title')} />
        <EmptyState
          title={t('pages.common.empty_no_project_title')}
          description={t('pages.trash.empty_no_project_reason')}
        />
      </Page>
    )
  }

  if (listQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.trash.title')} />
        <Skeleton rows={5} />
      </Page>
    )
  }
  if (listQuery.isError) {
    return (
      <Page>
        <PageHeader title={t('pages.trash.title')} />
        <ErrorState
          title={t('pages.trash.banner_load_fail')}
          message={listQuery.error.message}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void listQuery.refetch()}
        />
      </Page>
    )
  }

  const refresh = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.trash.all(projectId) })
  }

  const toggle = (key: string): void => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const toggleAll = (): void => {
    setSelected((current) => {
      const allSelected = visibleKeys.every((key) => current.has(key))
      const next = new Set(current)
      for (const key of visibleKeys) {
        if (allSelected) next.delete(key)
        else next.add(key)
      }
      return next
    })
  }

  const restore = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const result = await trashApi.restore(
        projectId,
        selectedEntries.map((entry) => ({ type: entry.type, id: entry.id }))
      )
      await refresh()
      setSelected(new Set())
      setNotice(t('pages.trash.toast_restored_message', { count: result.restored }))
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const planPurge = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    try {
      const plan = await trashApi.planPurge(
        projectId,
        selectedEntries.map((entry) => ({ type: entry.type, id: entry.id }))
      )
      setPending({ kind: 'purge', plan })
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const planEmpty = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    try {
      const plan = await trashApi.planEmpty(projectId)
      setPending({ kind: 'empty', plan })
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const confirmPlan = async (): Promise<void> => {
    if (pending === null) return
    setBusy(true)
    setError(null)
    try {
      if (pending.kind === 'purge') {
        const result = await trashApi.confirmPurge(pending.plan.token)
        setNotice(t('pages.trash.toast_purged_message', { count: result.purged }))
      } else {
        const result = await trashApi.confirmEmpty(pending.plan.token)
        setNotice(t('pages.trash.toast_emptied_message', { count: result.emptied }))
      }
      setPending(null)
      setSelected(new Set())
      await refresh()
    } catch (cause) {
      setError(cause)
      setPending(null)
    } finally {
      setBusy(false)
    }
  }

  const selectedCount = selectedEntries.length
  const allVisibleSelected =
    visibleKeys.length > 0 && visibleKeys.every((key) => selected.has(key))

  return (
    <Page>
      <PageHeader title={t('pages.trash.title')} />
      {archived && (
        <Banner tone="warning" title={t('shell.overview.readonly_title')}>
          {t('shell.overview.readonly_message')}
        </Banner>
      )}
      {notice !== null && <Banner tone="success">{notice}</Banner>}
      {error !== null && (
        <Banner tone="danger" title={t('pages.trash.error_restore')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}
      <Card>
        <div role="toolbar" aria-label={t('pages.trash.toolbar_label')}>
          <SelectField
            label={t('pages.trash.filter_label')}
            value={filter}
            options={FILTERS.map((item) => ({ value: item.value, label: t(item.key) }))}
            onChange={setFilter}
          />
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || archived || visibleKeys.length === 0}
            onClick={toggleAll}
          >
            {allVisibleSelected
              ? t('pages.trash.btn_clear_selection')
              : t('pages.trash.btn_select_all')}
          </Button>
          {selectedCount > 0 && (
            <Mono>{t('pages.trash.selected_count', { count: selectedCount })}</Mono>
          )}
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || archived || selectedCount === 0}
            onClick={() => void restore()}
          >
            {t('pages.trash.btn_restore')}
          </Button>
          <Button
            variant="danger"
            size="sm"
            disabled={busy || archived || selectedCount === 0}
            onClick={() => void planPurge()}
          >
            {t('pages.trash.btn_delete_permanently')}
          </Button>
          <Button
            variant="danger"
            size="sm"
            disabled={busy || archived || entries.length === 0}
            onClick={() => void planEmpty()}
          >
            {t('pages.trash.btn_empty')}
          </Button>
        </div>

        {entries.length === 0 ? (
          <EmptyState
            title={t('pages.trash.empty_title')}
            description={t('pages.trash.empty_reason')}
          />
        ) : visible.length === 0 ? (
          <EmptyState
            title={t('pages.trash.no_filter_results_title')}
            description={t('pages.trash.no_filter_results_reason')}
          />
        ) : (
          <Table
            label={t('pages.trash.title')}
            columns={[
              t('pages.trash.col_select'),
              t('pages.trash.col_type'),
              t('pages.trash.col_title'),
              t('pages.trash.col_deleted'),
              t('pages.trash.col_state')
            ]}
          >
            {visible.map((entry) => {
              const key = entryKey(entry)
              return (
                <tr key={key}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selected.has(key)}
                      aria-label={`${t(TYPE_LABEL[entry.type])}: ${entry.title}`}
                      onChange={() => toggle(key)}
                    />
                  </td>
                  <td>{t(TYPE_LABEL[entry.type])}</td>
                  <td>{entry.title}</td>
                  <td>{entry.deletedAt}</td>
                  <td>
                    {entry.fileState === 'missing'
                      ? t('pages.trash.status_file_missing')
                      : t('pages.trash.status_recoverable')}
                  </td>
                </tr>
              )
            })}
          </Table>
        )}
      </Card>

      <ConfirmDialog
        open={pending !== null}
        title={
          pending?.kind === 'empty'
            ? t('pages.trash.confirm_empty_title')
            : t('pages.trash.confirm_purge_title')
        }
        description={
          pending === null
            ? undefined
            : `${t(
                pending.kind === 'empty'
                  ? 'pages.trash.confirm_empty_message'
                  : 'pages.trash.confirm_purge_message',
                { count: pending.plan.count }
              )} ${t('pages.trash.confirm_purge_hint')}`
        }
        confirmLabel={
          pending?.kind === 'empty'
            ? t('pages.trash.confirm_empty_yes')
            : t('pages.trash.confirm_purge_yes')
        }
        cancelLabel={t('common.cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void confirmPlan()}
        onCancel={() => setPending(null)}
      />
    </Page>
  )
}