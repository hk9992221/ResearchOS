import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { AppError, AppErrorCode, type WeeklySectionKey, type WeeklyViolation } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Modal,
  Mono,
  Page,
  PageHeader,
  Skeleton,
  Table,
  Tabs
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useI18n, useT } from '../../i18n'
import { queryKeys, registerFlushHandler } from '../../lib/query'
import { routeHref, type RouteFeatureProps } from '../../routes'
import { ChecksPanel } from './ChecksPanel'
import { DigestPanel } from './DigestPanel'
import { ExportDialog } from './ExportDialog'
import { ProvenancePanel } from './ProvenancePanel'
import { ReportEditor } from './ReportEditor'
import { ReviseDialog } from './ReviseDialog'
import { weeklyApi, weeklyKeys } from './weekly-api'
import { errorReason, weeklyErrorView } from './weekly-errors'
import { formatInstant, periodLabel } from './weekly-format'
import { replaceSectionBody, unresolvedCount } from './weekly-sections'
import styles from './weekly.module.css'

const TABS = [
  { id: 'report', key: 'pages.weekly_review.tab_report' },
  { id: 'digest', key: 'pages.weekly_review.tab_digest' },
  { id: 'checks', key: 'pages.weekly_review.tab_checks' },
  { id: 'history', key: 'pages.weekly_review.tab_history' }
] as const

type TabId = (typeof TABS)[number]['id']

/**
 * Weekly report detail: the seven-section editor, its checks, the digest the
 * body was written from, provenance, and the report history of the project.
 *
 * Autosave follows the Go editor: a 3 s debounce with the content hash as the
 * optimistic token, Ctrl+S to save now, Ctrl+Enter to open the confirmation.
 * A save invalidates exactly the report collection and the checks of this
 * report, because those are the two views a body edit changes.
 */
export default function WeeklyReportPage({
  projectId: routeProjectId,
  entityId
}: RouteFeatureProps): JSX.Element {
  const t = useT()
  const { language } = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const projectId = routeProjectId ?? currentProjectId
  const reportId = entityId ?? ''

  const [tab, setTab] = useState<TabId>('report')
  const [draft, setDraft] = useState<string | null>(null)
  const [mode, setMode] = useState<'edit' | 'preview'>('edit')
  const [saving, setSaving] = useState(false)
  const [conflict, setConflict] = useState<string | null>(null)
  const [actionError, setActionError] = useState<unknown>(null)
  const [acks, setAcks] = useState<ReadonlySet<string>>(new Set())
  const [notes, setNotes] = useState<ReadonlyMap<string, string>>(new Map())
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [blockedOpen, setBlockedOpen] = useState(false)
  const [revertOpen, setRevertOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [reviseOpen, setReviseOpen] = useState(false)
  const [selection, setSelection] = useState<{ section: WeeklySectionKey; text: string } | null>(null)
  const [busy, setBusy] = useState(false)

  const templateQuery = useQuery({ queryKey: weeklyKeys.template, queryFn: weeklyApi.template })
  const preferencesQuery = useQuery({
    queryKey: weeklyKeys.preferences,
    queryFn: weeklyApi.preferences
  })
  const reportQuery = useQuery({
    queryKey: weeklyKeys.report(projectId ?? 'none', reportId),
    queryFn: () => weeklyApi.report(projectId as string, reportId),
    enabled: projectId !== null && reportId !== ''
  })
  const report = reportQuery.data
  const validationQuery = useQuery({
    queryKey: weeklyKeys.validation(projectId ?? 'none', reportId),
    queryFn: () => weeklyApi.validate(projectId as string, reportId),
    enabled: report !== undefined
  })
  const digestQuery = useQuery({
    queryKey: weeklyKeys.digest(
      projectId ?? 'none',
      report === undefined ? undefined : { start: report.periodStart, end: report.periodEnd }
    ),
    queryFn: () =>
      weeklyApi.digest(projectId as string, {
        start: report?.periodStart ?? '',
        end: report?.periodEnd ?? ''
      }),
    enabled: tab === 'digest' && report !== undefined
  })
  const historyQuery = useQuery({
    queryKey: weeklyKeys.reports(projectId ?? 'none', { history: true }),
    queryFn: () =>
      weeklyApi.reports({
        projectId: projectId as string,
        pageSize: 20,
        order: '-period_start',
        includeDeleted: true
      }),
    enabled: tab === 'history' && projectId !== null
  })

  const template = templateQuery.data
  const content = draft ?? report?.contentMd ?? ''
  const dirty = report !== undefined && draft !== null && draft !== report.contentMd
  const deleted = report?.deletedAt !== null && report?.deletedAt !== undefined
  const readOnly =
    report === undefined || report.status === 'confirmed' || archived || deleted === true
  const violations = validationQuery.data?.violations ?? report?.meta.checks?.violations ?? []
  const unresolved = unresolvedCount(violations, acks)

  const saveNow = useCallback(async (): Promise<boolean> => {
    if (projectId === null || report === undefined) return true
    if (draft === null || draft === report.contentMd) return true
    setSaving(true)
    setConflict(null)
    try {
      const updated = await weeklyApi.update({
        projectId,
        reportId,
        contentMd: draft,
        expectedHash: report.contentHash
      })
      queryClient.setQueryData(weeklyKeys.report(projectId, reportId), updated)
      setDraft(null)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: weeklyKeys.reportsRoot(projectId) }),
        queryClient.invalidateQueries({ queryKey: weeklyKeys.validation(projectId, reportId) })
      ])
      return true
    } catch (error) {
      if (errorReason(error) === 'report.content_conflict') {
        setConflict(
          error instanceof AppError ? error.message : t('pages.weekly_review.err_content_conflict')
        )
      } else {
        setActionError(error)
      }
      return false
    } finally {
      setSaving(false)
    }
  }, [draft, projectId, queryClient, report, reportId, t])

  const requestConfirm = useCallback(async (): Promise<void> => {
    if (report === undefined || report.status !== 'draft' || archived) return
    const saved = await saveNow()
    if (!saved) return
    if (unresolved > 0) {
      setBlockedOpen(true)
      return
    }
    setConfirmOpen(true)
  }, [archived, report, saveNow, unresolved])

  const saveRef = useRef(saveNow)
  useEffect(() => {
    saveRef.current = saveNow
  }, [saveNow])
  useEffect(
    () =>
      registerFlushHandler(async () => {
        await saveRef.current()
      }),
    []
  )

  // A different report is a different editor: local acknowledgements, notes
  // and the unsaved body must not leak across reports.
  useEffect(() => {
    setDraft(null)
    setAcks(new Set())
    setNotes(new Map())
    setConflict(null)
    setActionError(null)
    setConfirmOpen(false)
    setBlockedOpen(false)
    setRevertOpen(false)
    setExportOpen(false)
  }, [reportId])

  useEffect(() => {
    if (!dirty || saving || readOnly) return
    const timer = window.setTimeout(() => {
      void saveNow()
    }, 3000)
    return () => window.clearTimeout(timer)
  }, [dirty, saving, readOnly, saveNow])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (!event.ctrlKey && !event.metaKey) return
      if (event.key.toLowerCase() === 's') {
        event.preventDefault()
        void saveNow()
      } else if (event.key === 'Enter') {
        event.preventDefault()
        void requestConfirm()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [requestConfirm, saveNow])

  const toggleAck = (violation: WeeklyViolation): void => {
    if (violation.ack !== undefined) return
    setAcks((current) => {
      const next = new Set(current)
      if (next.has(violation.fingerprint)) next.delete(violation.fingerprint)
      else next.add(violation.fingerprint)
      return next
    })
  }

  const changeSection = (key: WeeklySectionKey, body: string): void => {
    if (template === undefined || report === undefined) return
    setDraft(replaceSectionBody(content, template, key, body))
  }

  const doConfirm = async (): Promise<void> => {
    if (projectId === null || report === undefined) return
    setBusy(true)
    setActionError(null)
    try {
      const acknowledged = [...acks]
      const ackNotes: Record<string, string> = {}
      for (const fingerprint of acknowledged) {
        const note = notes.get(fingerprint)
        if (note !== undefined) ackNotes[fingerprint] = note
      }
      const updated = await weeklyApi.confirm({
        projectId,
        reportId,
        ackFingerprints: acknowledged,
        ackNotes
      })
      queryClient.setQueryData(weeklyKeys.report(projectId, reportId), updated)
      setConfirmOpen(false)
      setAcks(new Set())
      setNotes(new Map())
      await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
    } catch (error) {
      setActionError(error)
      setConfirmOpen(false)
    } finally {
      setBusy(false)
    }
  }

  const doRevert = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    setActionError(null)
    try {
      const updated = await weeklyApi.revert(projectId, reportId)
      queryClient.setQueryData(weeklyKeys.report(projectId, reportId), updated)
      setRevertOpen(false)
      await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
    } catch (error) {
      setActionError(error)
      setRevertOpen(false)
    } finally {
      setBusy(false)
    }
  }

  const doDelete = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    setActionError(null)
    try {
      await weeklyApi.remove(projectId, reportId)
      setDeleteOpen(false)
      setDraft(null)
      await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
      await reportQuery.refetch()
    } catch (error) {
      setActionError(error)
      setDeleteOpen(false)
    } finally {
      setBusy(false)
    }
  }

  const doRestore = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    setActionError(null)
    try {
      const updated = await weeklyApi.restore(projectId, reportId)
      queryClient.setQueryData(weeklyKeys.report(projectId, reportId), updated)
      setDraft(null)
      await queryClient.invalidateQueries({ queryKey: queryKeys.weekly.all(projectId) })
    } catch (error) {
      setActionError(error)
    } finally {
      setBusy(false)
    }
  }

  const openRevision = async (): Promise<void> => {
    if (selection === null) return
    const saved = await saveNow()
    if (!saved) return
    setReviseOpen(true)
  }

  const submitRevision = async (instructions: string): Promise<void> => {
    if (projectId === null || report === undefined || selection === null) {
      throw new AppError(AppErrorCode.Internal, 'revision state is unavailable')
    }
    const result = await weeklyApi.revise({
      projectId,
      reportId,
      section: selection.section,
      focusText: selection.text,
      instructions,
      expectedHash: report.contentHash
    })
    queryClient.setQueryData(weeklyKeys.report(projectId, reportId), result.report)
    setDraft(null)
    setSelection(null)
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: weeklyKeys.reportsRoot(projectId) }),
      queryClient.invalidateQueries({ queryKey: weeklyKeys.validation(projectId, reportId) })
    ])
  }

  const reload = async (): Promise<void> => {
    setDraft(null)
    setConflict(null)
    setActionError(null)
    await reportQuery.refetch()
  }

  const goBack = async (): Promise<void> => {
    await saveNow()
    if (projectId === null) return
    const href = routeHref('weekly_review', { projectId })
    if (href !== undefined) navigate(href)
  }

  if (projectId === null) {
    return (
      <EmptyState
        title={t('pages.weekly_review.needs_project_title')}
        description={t('pages.weekly_review.needs_project_reason')}
      />
    )
  }

  if (reportQuery.isPending) {
    return (
      <Page>
        <Skeleton rows={8} />
      </Page>
    )
  }

  if (reportQuery.isError || report === undefined) {
    return (
      <ErrorState
        title={t('pages.weekly_review.banner_problem')}
        message={reportQuery.error?.message ?? t('pages.weekly_review.err_not_found')}
        retryLabel={t('pages.weekly_review.btn_reload_report')}
        onRetry={() => void reload()}
      />
    )
  }

  const coverage = report.meta.coverage
  const generation = report.meta.generation
  const actionView = actionError === null ? null : weeklyErrorView(actionError)
  const stateLabel = conflict !== null
    ? t('pages.weekly_review.state_conflict')
    : saving
      ? t('pages.weekly_review.state_saving')
      : dirty
        ? t('pages.weekly_review.state_unsaved')
        : t('pages.weekly_review.state_saved')
  const weekStartsOn = preferencesQuery.data?.weekStartsOn ?? 1
  const weekLabel = periodLabel(report.periodStart, weekStartsOn)
  const selectionTitle =
    selection === null
      ? ''
      : (template?.sections.find((entry) => entry.key === selection.section)?.title ??
        selection.section)

  return (
    <Page>
      <PageHeader
        title={report.title}
        description={t('pages.weekly_review.detail_period', {
          start: report.periodStart,
          end: report.periodEnd
        })}
        actions={
          <>
            <Button variant="ghost" onClick={() => void goBack()}>
              {t('pages.weekly_review.btn_back')}
            </Button>
            {report.status === 'draft' && !archived && (
              <Button
                variant="secondary"
                onClick={() => void saveNow()}
                disabled={!dirty || saving}
              >
                {t('pages.weekly_review.btn_save')}
              </Button>
            )}
            <Button variant="secondary" onClick={() => setExportOpen(true)} disabled={deleted}>
              {t('pages.weekly_review.btn_export')}
            </Button>
            {deleted ? (
              <Button
                variant="secondary"
                onClick={() => void doRestore()}
                disabled={archived || busy}
              >
                {t('pages.weekly_review.btn_restore')}
              </Button>
            ) : (
              <Button
                variant="danger"
                onClick={() => setDeleteOpen(true)}
                disabled={archived || busy}
              >
                {t('pages.weekly_review.btn_delete')}
              </Button>
            )}
            {report.status === 'draft' ? (
              <Button
                variant="primary"
                onClick={() => void requestConfirm()}
                disabled={archived || deleted === true}
              >
                {t('pages.weekly_review.btn_confirm')}
              </Button>
            ) : (
              <Button
                variant="secondary"
                onClick={() => setRevertOpen(true)}
                disabled={archived || deleted}
              >
                {t('pages.weekly_review.btn_revert')}
              </Button>
            )}
          </>
        }
      />

      <div className={styles.statusLine}>
        <Badge tone={report.status === 'confirmed' ? 'success' : 'warning'}>
          {t(
            report.status === 'confirmed'
              ? 'pages.weekly_review.status_confirmed'
              : 'pages.weekly_review.status_draft'
          )}
        </Badge>
        <Mono>{weekLabel}</Mono>
        <span>{stateLabel}</span>
        {!readOnly && <span>{t('pages.weekly_review.save_shortcut')}</span>}
        {report.confirmedAt !== null && (
          <span>
            {t('pages.weekly_review.metric_confirmed')}:{' '}
            {formatInstant(report.confirmedAt, language, coverage?.timezone)}
          </span>
        )}
      </div>

      {archived && (
        <Banner tone="warning" title={t('pages.weekly_review.archived_title')}>
          {t('pages.weekly_review.archived_detail')}
        </Banner>
      )}
      {deleted && (
        <Banner tone="warning" title={t('pages.weekly_review.deleted_title')}>
          {t('pages.weekly_review.deleted_message')}
        </Banner>
      )}
      {conflict !== null && (
        <Banner
          tone="danger"
          title={t('pages.weekly_review.conflict_title')}
          action={
            <Button variant="secondary" onClick={() => void reload()}>
              {t('pages.weekly_review.btn_reload_report')}
            </Button>
          }
        >
          {t('pages.weekly_review.conflict_message')} {conflict}
        </Banner>
      )}
      {generation?.ai_status === 'failed' && (
        <Banner tone="warning" title={t('pages.weekly_review.gen_note_title')}>
          {generation.error ?? t('pages.weekly_review.err_ai_output_invalid')}
        </Banner>
      )}
      {actionView !== null && (
        <Banner tone="danger" title={t(actionView.labelKey)}>
          <div className={styles.errorBox}>
            <span className={styles.errorMessage}>{actionView.message}</span>
            <Mono>{actionView.code}</Mono>
          </div>
        </Banner>
      )}

      <div className={styles.tabsRow}>
        <Tabs
          label={report.title}
          items={TABS.map((item) => ({ id: item.id, label: t(item.key) }))}
          value={tab}
          onChange={(id) => {
            setSelection(null)
            setTab(id === 'digest' || id === 'checks' || id === 'history' ? id : 'report')
          }}
        />
        {tab === 'report' && !readOnly && (
          <div className={styles.row}>
            <Button
              variant={mode === 'edit' ? 'primary' : 'ghost'}
              size="sm"
              onClick={() => setMode('edit')}
            >
              {t('pages.weekly_review.mode_edit')}
            </Button>
            <Button
              variant={mode === 'preview' ? 'primary' : 'ghost'}
              size="sm"
              onClick={() => setMode('preview')}
            >
              {t('pages.weekly_review.mode_preview')}
            </Button>
          </div>
        )}
        {tab === 'report' && readOnly && (
          <Badge tone="neutral">{t('pages.weekly_review.badge_readonly')}</Badge>
        )}
      </div>

      {tab === 'report' && selection !== null && !readOnly && (
        <div className={styles.revisionBar}>
          <Mono>
            {t('pages.weekly_review.revise_selected', {
              section: selectionTitle,
              count: Array.from(selection.text).length
            })}
          </Mono>
          <div className={styles.grow} />
          <Button variant="primary" size="sm" onClick={() => void openRevision()}>
            {t('pages.weekly_review.revise_button')}
          </Button>
        </div>
      )}

      {tab === 'report' && (
        <>
          {template === undefined ? (
            <Skeleton rows={6} />
          ) : (
            <ReportEditor
              content={content}
              template={template}
              mode={readOnly ? 'preview' : mode}
              readOnly={readOnly}
              generation={generation}
              onSectionChange={changeSection}
              onSelectionChange={setSelection}
            />
          )}
          <ProvenancePanel report={report} template={template} />
          <section className={styles.metrics}>
            <div className={styles.metric}>
              <span className={styles.metricLabel}>{t('pages.weekly_review.metric_events')}</span>
              <span className={styles.metricValue}>{coverage?.event_count ?? 0}</span>
            </div>
            <div className={styles.metric}>
              <span className={styles.metricLabel}>{t('pages.weekly_review.metric_checks')}</span>
              <span className={styles.metricValue}>{unresolved}</span>
            </div>
            <div className={styles.metric}>
              <span className={styles.metricLabel}>{t('pages.weekly_review.metric_refs')}</span>
              <span className={styles.metricValue}>{report.meta.refs?.length ?? 0}</span>
            </div>
          </section>
        </>
      )}

      {tab === 'checks' && (
        <ChecksPanel
          template={template}
          validation={validationQuery.data}
          loading={validationQuery.isPending}
          error={validationQuery.error}
          acks={acks}
          notes={notes}
          onToggleAck={toggleAck}
          onNoteChange={(fingerprint, note) =>
            setNotes((current) => {
              const next = new Map(current)
              next.set(fingerprint, note)
              return next
            })
          }
        />
      )}

      {tab === 'digest' && (
        <DigestPanel
          digest={digestQuery.data}
          loading={digestQuery.isPending}
          error={digestQuery.error}
          onRetry={() => void digestQuery.refetch()}
        />
      )}

      {tab === 'history' && (
        <Table
          label={t('pages.weekly_review.tab_history')}
          empty={
            <EmptyState
              title={t('pages.weekly_review.history_empty')}
              description={t('pages.weekly_review.empty_reason')}
            />
          }
          columns={[
            t('pages.weekly_review.col_period'),
            t('pages.weekly_review.col_title'),
            t('pages.weekly_review.col_status'),
            t('pages.weekly_review.col_updated')
          ]}
        >
          {(historyQuery.data?.items ?? []).map((item) => (
            <tr key={item.reportId} className={styles.historyRow}>
              <td>
                <Mono>{periodLabel(item.periodStart, weekStartsOn)}</Mono>
              </td>
              <td>
                <button
                  type="button"
                  className={styles.linkButton}
                  onClick={() => {
                    const href = routeHref('weekly_report', {
                      projectId,
                      entityId: item.reportId
                    })
                    if (href !== undefined) navigate(href)
                  }}
                >
                  {item.title}
                </button>
              </td>
              <td>
                <Badge tone={item.status === 'confirmed' ? 'success' : 'warning'}>
                  {t(
                    item.status === 'confirmed'
                      ? 'pages.weekly_review.status_confirmed'
                      : 'pages.weekly_review.status_draft'
                  )}
                </Badge>
              </td>
              <td>
                <Mono>{formatInstant(item.updatedAt, language, coverage?.timezone)}</Mono>
              </td>
            </tr>
          ))}
        </Table>
      )}

      <ConfirmDialog
        open={confirmOpen}
        title={t('pages.weekly_review.confirm_title')}
        description={t('pages.weekly_review.confirm_desc')}
        confirmLabel={t('pages.weekly_review.btn_confirm')}
        cancelLabel={t('pages.weekly_review.gen_close')}
        busy={busy}
        onConfirm={() => void doConfirm()}
        onCancel={() => setConfirmOpen(false)}
      />
      <Modal
        open={blockedOpen}
        title={t('pages.weekly_review.confirm_blocked_title')}
        onClose={() => setBlockedOpen(false)}
        width={520}
        footer={
          <Button variant="secondary" onClick={() => setBlockedOpen(false)}>
            {t('pages.weekly_review.gen_close')}
          </Button>
        }
      >
        <div className={styles.dialogGrid}>
          <p>{t('pages.weekly_review.confirm_blocked', { n: unresolved })}</p>
          <p className={styles.emptyLine}>{t('pages.weekly_review.confirm_blocked_hint')}</p>
          <Button variant="secondary" onClick={() => { setBlockedOpen(false); setTab('checks') }}>
            {t('pages.weekly_review.tab_checks')}
          </Button>
        </div>
      </Modal>
      <ConfirmDialog
        open={revertOpen}
        title={t('pages.weekly_review.revert_title')}
        description={t('pages.weekly_review.revert_desc')}
        confirmLabel={t('pages.weekly_review.btn_revert')}
        cancelLabel={t('pages.weekly_review.gen_close')}
        busy={busy}
        onConfirm={() => void doRevert()}
        onCancel={() => setRevertOpen(false)}
      />
      <ConfirmDialog
        open={deleteOpen}
        tone="danger"
        title={t('pages.weekly_review.delete_title')}
        description={t('pages.weekly_review.delete_message', { title: report.title })}
        confirmLabel={t('pages.weekly_review.delete_confirm')}
        cancelLabel={t('pages.weekly_review.gen_close')}
        busy={busy}
        onConfirm={() => void doDelete()}
        onCancel={() => setDeleteOpen(false)}
      />
      <ReviseDialog
        open={reviseOpen}
        sectionTitle={selectionTitle}
        selectionText={selection?.text ?? ''}
        onClose={() => setReviseOpen(false)}
        onSubmit={submitRevision}
      />
      <ExportDialog
        open={exportOpen}
        projectId={projectId}
        reportId={reportId}
        status={report.status}
        onClose={() => setExportOpen(false)}
      />
    </Page>
  )
}
