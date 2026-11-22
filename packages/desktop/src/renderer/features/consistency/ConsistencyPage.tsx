import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type {
  ReconcileAction,
  ReconcileActionKind,
  ReconcileMissing,
  ReconcileModified,
  ReconcileOrphan
} from '@researchos/shared'

import {
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  PageHeader,
  SelectField,
  Skeleton
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys } from '../../lib/query'

type RowKind = 'orphan' | 'missing' | 'modified'

function rowKey(kind: RowKind, id: string): string {
  return `${kind}:${id}`
}

const ACTION_LABEL: Record<ReconcileActionKind, string> = {
  adopt: 'pages.consistency.kind_adopt',
  accept_external_edit: 'pages.consistency.kind_accept_external_edit',
  mark_broken: 'pages.consistency.kind_mark_broken',
  reattach: 'pages.consistency.kind_reattach',
  recreate: 'pages.consistency.kind_recreate'
}

function actionsForKind(kind: RowKind): readonly ReconcileActionKind[] {
  if (kind === 'orphan') return ['adopt']
  if (kind === 'missing') return ['recreate', 'mark_broken']
  return ['accept_external_edit', 'mark_broken']
}

function defaultKind(kind: RowKind): ReconcileActionKind {
  return kind === 'orphan' ? 'adopt' : kind === 'missing' ? 'recreate' : 'accept_external_edit'
}

interface RowProps {
  readonly kind: RowKind
  readonly id: string
  readonly title: string
  readonly detail: string
  readonly selected: boolean
  readonly action: ReconcileActionKind
  readonly onToggle: () => void
  readonly onAction: (action: ReconcileActionKind) => void
}

function FindingRow({
  kind,
  id,
  title,
  detail,
  selected,
  action,
  onToggle,
  onAction
}: RowProps): JSX.Element {
  const t = useT()
  const options = actionsForKind(kind).map((candidate) => ({
    value: candidate,
    label: t(ACTION_LABEL[candidate])
  }))
  return (
    <li>
      <label>
        <input type="checkbox" checked={selected} onChange={onToggle} />{' '}
        <Mono>{title}</Mono>
        {id !== '' && ` — ${id}`}
      </label>
      {detail !== '' && <p>{detail}</p>}
      {options.length > 1 && (
        <SelectField
          label={`${t(ACTION_LABEL.accept_external_edit)}: ${title}`}
          value={action}
          options={options}
          onChange={(value) => {
            const next = actionsForKind(kind).find((candidate) => candidate === value)
            if (next !== undefined) onAction(next)
          }}
        />
      )}
    </li>
  )
}

/** File consistency: scan note files, choose actions, apply them at once. */
export default function ConsistencyPage(): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const { projectId, archived } = useCurrentProject()
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [choices, setChoices] = useState<Readonly<Record<string, ReconcileActionKind>>>({})
  const [actionResult, setActionResult] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)

  const scanQuery = useQuery({
    queryKey: queryKeys.notes.reconcile(projectId ?? 'none'),
    queryFn: () => ipc.invoke('notes.reconcile.scan', { projectId: projectId as string }),
    enabled: projectId !== null
  })

  const report = scanQuery.data

  const actions = useMemo<ReconcileAction[]>(() => {
    if (report === undefined) return []
    const built: ReconcileAction[] = []
    const choice = (kind: RowKind, keyId: string): ReconcileActionKind =>
      choices[rowKey(kind, keyId)] ?? defaultKind(kind)

    for (const orphan of report.orphans) {
      if (!selected.has(rowKey('orphan', orphan.relPath))) continue
      built.push({ kind: 'adopt', relPath: orphan.relPath, actor: 'user' })
    }
    for (const missing of report.missing) {
      if (!selected.has(rowKey('missing', missing.noteId))) continue
      built.push({
        kind: choice('missing', missing.noteId),
        noteId: missing.noteId,
        actor: 'user'
      })
    }
    for (const modified of report.modified) {
      if (!selected.has(rowKey('modified', modified.noteId))) continue
      built.push({
        kind: choice('modified', modified.noteId),
        noteId: modified.noteId,
        actor: 'user'
      })
    }
    return built
  }, [report, selected, choices])

  if (projectId === null) {
    return (
      <Page>
        <PageHeader title={t('pages.consistency.title')} />
        <EmptyState
          title={t('pages.common.empty_no_project_title')}
          description={t('pages.consistency.empty_no_project_reason')}
        />
      </Page>
    )
  }

  if (scanQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.consistency.title')} />
        <Skeleton rows={5} />
      </Page>
    )
  }
  if (scanQuery.isError) {
    return (
      <Page>
        <PageHeader title={t('pages.consistency.title')} />
        <ErrorState
          title={t('pages.consistency.banner_scan_failed')}
          message={scanQuery.error.message}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void scanQuery.refetch()}
        />
      </Page>
    )
  }

  const toggle = (key: string): void => {
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const apply = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    setActionResult(null)
    try {
      const result = await ipc.invoke('notes.reconcile.apply', { projectId, actions })
      const applied = result.items.filter((item) => item.error == null).length
      const failed = result.items.length - applied
      setActionResult(
        failed === 0
          ? t('pages.consistency.apply_result', { applied })
          : `${t('pages.consistency.apply_result', { applied })} ${t('pages.consistency.banner_repair_failed')} (${failed})`
      )
      setSelected(new Set())
      setChoices({})
      await queryClient.invalidateQueries({ queryKey: queryKeys.notes.reconcile(projectId) })
      await queryClient.invalidateQueries({ queryKey: queryKeys.notes.all(projectId) })
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const orphans: readonly ReconcileOrphan[] = report?.orphans ?? []
  const missing: readonly ReconcileMissing[] = report?.missing ?? []
  const modified: readonly ReconcileModified[] = report?.modified ?? []

  return (
    <Page>
      <PageHeader
        title={t('pages.consistency.title')}
        actions={
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => void scanQuery.refetch()}
          >
            {t('pages.consistency.btn_rescan')}
          </Button>
        }
      />
      {archived && (
        <Banner tone="warning" title={t('shell.overview.readonly_title')}>
          {t('shell.overview.readonly_message')}
        </Banner>
      )}
      <p>
        {report === undefined
          ? t('pages.consistency.scan_never')
          : t('pages.consistency.scan_time', { time: report.generatedAt })}
      </p>
      <div role="toolbar" aria-label={t('pages.consistency.toolbar_label')}>
        <Button
          variant="primary"
          size="sm"
          disabled={busy || archived || actions.length === 0}
          onClick={() => void apply()}
        >
          {t('pages.consistency.btn_apply')}
        </Button>
        <Mono>{t('pages.consistency.selected_count', { count: actions.length })}</Mono>
      </div>
      {actionResult !== null && <Banner tone="success">{actionResult}</Banner>}
      {error !== null && (
        <Banner tone="danger" title={t('pages.consistency.banner_repair_failed')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}

      <Card title={t('pages.consistency.card_orphans', { count: orphans.length })}>
        {orphans.length === 0 ? (
          <p>{t('pages.consistency.orphans_none')}</p>
        ) : (
          <ul>
            {orphans.map((orphan) => {
              const key = rowKey('orphan', orphan.relPath)
              return (
                <FindingRow
                  key={key}
                  kind="orphan"
                  id=""
                  title={orphan.relPath}
                  detail={orphan.modTime}
                  selected={selected.has(key)}
                  action={choices[key] ?? defaultKind('orphan')}
                  onToggle={() => toggle(key)}
                  onAction={(action) =>
                    setChoices((current) => ({ ...current, [key]: action }))
                  }
                />
              )
            })}
          </ul>
        )}
      </Card>

      <Card title={t('pages.consistency.card_missing', { count: missing.length })}>
        {missing.length === 0 ? (
          <p>{t('pages.consistency.missing_none')}</p>
        ) : (
          <ul>
            {missing.map((entry) => {
              const key = rowKey('missing', entry.noteId)
              return (
                <FindingRow
                  key={key}
                  kind="missing"
                  id={entry.noteId}
                  title={entry.title}
                  detail={entry.filePath}
                  selected={selected.has(key)}
                  action={choices[key] ?? defaultKind('missing')}
                  onToggle={() => toggle(key)}
                  onAction={(action) =>
                    setChoices((current) => ({ ...current, [key]: action }))
                  }
                />
              )
            })}
          </ul>
        )}
      </Card>

      <Card title={t('pages.consistency.card_modified', { count: modified.length })}>
        {modified.length === 0 ? (
          <p>{t('pages.consistency.modified_none')}</p>
        ) : (
          <ul>
            {modified.map((entry) => {
              const key = rowKey('modified', entry.noteId)
              return (
                <FindingRow
                  key={key}
                  kind="modified"
                  id={entry.noteId}
                  title={entry.filePath}
                  detail={`${entry.dbHash.slice(0, 12)} → ${entry.diskHash.slice(0, 12)}`}
                  selected={selected.has(key)}
                  action={choices[key] ?? defaultKind('modified')}
                  onToggle={() => toggle(key)}
                  onAction={(action) =>
                    setChoices((current) => ({ ...current, [key]: action }))
                  }
                />
              )
            })}
          </ul>
        )}
      </Card>
    </Page>
  )
}