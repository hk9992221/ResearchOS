import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type {
  AiOutput,
  ExperimentView,
  SnapshotDetail,
  SnapshotSummary,
  TargetRef
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Rows,
  Row,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import { ExperimentMarkdown } from './ExperimentMarkdown'
import { formatTime, parseRosTarget, readJobFailure, type JobFailure } from './model'
import { useJobOutcome } from './useJobOutcome'
import styles from './experiments.module.css'

export interface AnalysisTabProps {
  readonly projectId: string
  readonly target: TargetRef
  readonly mode: 'outline' | 'results'
  readonly experiment: ExperimentView
  readonly archived: boolean
  readonly onOpenReference: (href: string) => void
  readonly onOpenSource: (path: string) => void
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

function aiStatusKey(status: string): string {
  if (status === 'confirmed') return 'pages.experiments.ai_confirmed'
  if (status === 'draft') return 'pages.experiments.ai_draft'
  return 'pages.experiments.ai_unknown'
}

/**
 * Outline / Results: generate one AI analysis as a job, read the frozen
 * snapshot and edit the live draft.
 *
 * The snapshot is append-only: regenerating adds a row and older analyses keep
 * their bytes. Edits and confirmation act on the `ai_outputs` draft behind the
 * snapshot, which is why they never rewrite history.
 */
export function AnalysisTab({
  projectId,
  target,
  mode,
  experiment,
  archived,
  onOpenReference,
  onOpenSource,
  notify
}: AnalysisTabProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [jobId, setJobId] = useState<string | null>(null)
  const [failure, setFailure] = useState<JobFailure | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [editText, setEditText] = useState('')
  const [editBusy, setEditBusy] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const availability = useQuery({
    queryKey: queryKeys.ai.availability,
    queryFn: () => experimentsApi.aiAvailability(),
    staleTime: 60_000
  })

  const listKey = [
    'experiments',
    projectId,
    'snapshots',
    target.kind,
    target.id,
    mode
  ] as const

  const listQuery = useQuery({
    queryKey: listKey,
    queryFn: () => experimentsApi.snapshots(projectId, target, { kind: mode, limit: 50 })
  })

  const snapshots = listQuery.data ?? []
  const current: SnapshotSummary | null =
    snapshots.find((snapshot) => snapshot.id === selectedId) ?? snapshots[0] ?? null

  const detailQuery = useQuery({
    queryKey: ['experiments', projectId, 'snapshot', current?.id ?? 'none'],
    queryFn: () => experimentsApi.snapshot(projectId, current?.id as string),
    enabled: current !== null
  })

  useJobOutcome(jobId, (outcome) => {
    setJobId(null)
    if (outcome.state === 'succeeded') {
      setSelectedId(null)
      setEditing(false)
      notify({ tone: 'success', title: t('pages.experiments.toast_ai_done') })
      void queryClient.invalidateQueries({ queryKey: listKey })
      void queryClient.invalidateQueries({ queryKey: ['experiments', projectId] })
      return
    }
    if (outcome.state === 'failed') {
      const parsed = readJobFailure(outcome.error)
      setFailure(parsed)
      notify({ tone: 'error', title: t('pages.experiments.toast_ai_failed') })
      return
    }
    notify({ tone: 'info', title: t('pages.experiments.cancelled') })
  })

  const generate = async (): Promise<void> => {
    setFailure(null)
    try {
      const started =
        mode === 'outline'
          ? await experimentsApi.generateOutline(projectId, experiment.id)
          : await experimentsApi.generateResults(projectId, target)
      setJobId(started.jobId)
    } catch (cause) {
      setFailure({
        code: null,
        message: cause instanceof Error ? cause.message : t('pages.experiments.toast_ai_failed'),
        cause: null
      })
    }
  }

  const detail = detailQuery.data
  const analysis = detail?.aiAnalysis ?? null
  const outputId = analysis?.aiOutputId ?? null

  const toggleEdit = (): void => {
    if (editing) {
      setEditing(false)
      return
    }
    setEditText(analysis?.contentMd ?? '')
    setEditing(true)
  }

  const saveDraft = async (): Promise<void> => {
    if (outputId === null) return
    setEditBusy(true)
    try {
      await experimentsApi.updateAiOutput(projectId, outputId, editText)
      await queryClient.invalidateQueries({ queryKey: listKey })
      notify({ tone: 'success', title: t('pages.experiments.toast_saved_output') })
      setEditing(false)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.experiments.banner_save_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setEditBusy(false)
    }
  }

  const confirmDraft = async (): Promise<void> => {
    if (outputId === null) return
    setEditBusy(true)
    try {
      const output: AiOutput = await experimentsApi.confirmAiOutput(projectId, outputId)
      setConfirmOpen(false)
      await queryClient.invalidateQueries({ queryKey: listKey })
      await queryClient.invalidateQueries({ queryKey: ['experiments', projectId] })
      notify({ tone: 'success', title: t('pages.experiments.toast_confirmed') })
      if (output.status !== 'confirmed') {
        // The command returned without confirming; do not paint success twice.
        notify({ tone: 'warning', title: t('pages.experiments.banner_confirm_failed') })
      }
    } catch (cause) {
      setConfirmOpen(false)
      notify({
        tone: 'error',
        title: t('pages.experiments.banner_confirm_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setEditBusy(false)
    }
  }

  const aiAvailable = availability.data?.available === true
  const title = mode === 'outline'
    ? t('pages.experiments.tab_outline')
    : t('pages.experiments.tab_results')
  const emptyTitle = mode === 'outline'
    ? t('pages.experiments.outline_empty_title')
    : t('pages.experiments.results_empty_title')
  const emptyReason = mode === 'outline'
    ? t('pages.experiments.outline_empty_reason')
    : t('pages.experiments.results_empty_reason')

  const generateButton = (
    <Button
      variant="primary"
      size="sm"
      onClick={() => void generate()}
      disabled={archived || jobId !== null || !aiAvailable}
      title={
        archived
          ? t('pages.experiments.readonly_tooltip')
          : !aiAvailable
            ? t('pages.experiments.err_ai_unavailable')
            : undefined
      }
      aria-busy={jobId !== null}
    >
      {snapshots.length === 0
        ? t('pages.experiments.btn_generate')
        : t('pages.experiments.btn_regenerate')}
    </Button>
  )

  const snapshotList = useMemo(
    () => (
      <div className={styles.snapshotList}>
        {snapshots.map((snapshot) => (
          <div
            key={snapshot.id}
            className={[
              styles.snapshotRow,
              snapshot.id === current?.id ? styles.snapshotRowActive : ''
            ].join(' ')}
          >
            <Badge tone={snapshot.aiStatus === 'confirmed' ? 'success' : 'neutral'}>
              {t(aiStatusKey(snapshot.aiStatus))}
            </Badge>
            <div className={styles.cellStack}>
              <span className={styles.muted}>{formatTime(snapshot.createdAt)}</span>
              <span className={styles.mono}>
                {snapshot.providerName} · {snapshot.modelId}
              </span>
            </div>
            <Button
              variant="ghost"
              size="sm"
              aria-pressed={snapshot.id === current?.id}
              onClick={() => {
                setSelectedId(snapshot.id)
                setEditing(false)
              }}
            >
              {t('pages.experiments.action_open')}
            </Button>
          </div>
        ))}
      </div>
    ),
    [snapshots, current?.id, t]
  )

  return (
    <div className={styles.panel}>
      <Card
        title={title}
        actions={
          <div className={styles.actions}>
            {generateButton}
            {outputId !== null && (
              <Button
                variant="secondary"
                size="sm"
                onClick={toggleEdit}
                disabled={archived}
                title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
              >
                {t('pages.experiments.btn_edit_output')}
              </Button>
            )}
            {outputId !== null && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setConfirmOpen(true)}
                disabled={archived || current?.aiStatus === 'confirmed'}
              >
                {t('pages.experiments.btn_confirm')}
              </Button>
            )}
          </div>
        }
      >
        <div className={styles.stack}>
          {!aiAvailable && !availability.isPending && (
            <p className={styles.reason}>{t('pages.experiments.err_ai_unavailable')}</p>
          )}
          <p className={styles.reason}>{t('pages.experiments.outline_desc')}</p>

          {failure !== null && (
            <Banner tone="danger" title={t('pages.experiments.banner_ai_failed')}>
              <div className={styles.badges}>
                {failure.cause !== null && <Badge tone="danger">{failure.cause}</Badge>}
                <span>
                  {failure.cause === 'AI_OUTPUT_INVALID'
                    ? t('pages.experiments.err_invalid_ai_output')
                    : (failure.message ?? failure.code ?? '')}
                </span>
              </div>
            </Banner>
          )}

          {listQuery.isPending ? (
            <Skeleton rows={4} />
          ) : listQuery.isError ? (
            <ErrorState
              title={t('pages.experiments.banner_problem')}
              message={listQuery.error instanceof Error ? listQuery.error.message : undefined}
              onRetry={() => void listQuery.refetch()}
              retryLabel={t('pages.experiments.btn_refresh')}
            />
          ) : snapshots.length === 0 ? (
            <EmptyState title={emptyTitle} description={emptyReason} compact />
          ) : (
            snapshotList
          )}
        </div>
      </Card>

      {current !== null && detailQuery.isPending && <Skeleton rows={6} />}
      {current !== null && detailQuery.isError && (
        <ErrorState
          title={t('pages.experiments.toast_history_failed')}
          message={detailQuery.error instanceof Error ? detailQuery.error.message : undefined}
          onRetry={() => void detailQuery.refetch()}
          retryLabel={t('pages.experiments.btn_refresh')}
        />
      )}

      {detail !== undefined && (
        <AnalysisDetail
          detail={detail}
          summary={current}
          editing={editing}
          editText={editText}
          editBusy={editBusy}
          onEditText={setEditText}
          onSave={() => void saveDraft()}
          onOpenReference={onOpenReference}
          onOpenSource={onOpenSource}
        />
      )}

      <ConfirmDialog
        open={confirmOpen}
        title={t('pages.experiments.confirm_title')}
        description={t('pages.experiments.confirm_desc')}
        confirmLabel={t('pages.experiments.btn_confirm')}
        cancelLabel={t('pages.experiments.btn_back')}
        busy={editBusy}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={() => void confirmDraft()}
      />
    </div>
  )
}

function AnalysisDetail({
  detail,
  summary,
  editing,
  editText,
  editBusy,
  onEditText,
  onSave,
  onOpenReference,
  onOpenSource
}: {
  readonly detail: SnapshotDetail
  readonly summary: SnapshotSummary | null
  readonly editing: boolean
  readonly editText: string
  readonly editBusy: boolean
  readonly onEditText: (text: string) => void
  readonly onSave: () => void
  readonly onOpenReference: (href: string) => void
  readonly onOpenSource: (path: string) => void
}): JSX.Element {
  const t = useT()
  const analysis = detail.aiAnalysis
  if (analysis === null) {
    return <EmptyState title={t('pages.experiments.snapshot_no_ai')} compact />
  }

  return (
    <Card title={t('pages.experiments.history_detail_title')}>
      <div className={styles.stack}>
        <div className={styles.badges}>
          <Badge tone="info">{analysis.kind === 'results' ? t('pages.experiments.kind_results') : t('pages.experiments.kind_outline')}</Badge>
          <Badge tone={summary?.aiStatus === 'confirmed' ? 'success' : 'neutral'}>
            {t(aiStatusKey(summary?.aiStatus ?? 'draft'))}
          </Badge>
          {summary?.humanEdited === true && (
            <Badge tone="warning">{t('pages.experiments.ai_human_edited')}</Badge>
          )}
          <span className={styles.mono}>{detail.modelInfo?.promptVersion ?? analysis.promptVersion}</span>
        </div>

        {editing ? (
          <div className={styles.stack}>
            <p className={styles.reason}>
              {summary?.aiStatus === 'confirmed'
                ? t('pages.experiments.edit_confirmed_hint')
                : t('pages.experiments.edit_draft_hint')}
            </p>
            <textarea
              className={styles.editorArea}
              aria-label={t('pages.experiments.edit_field_label')}
              value={editText}
              onChange={(event) => onEditText(event.target.value)}
            />
            <div className={styles.actions}>
              <Button variant="primary" size="sm" onClick={onSave} disabled={editBusy} aria-busy={editBusy}>
                {t('pages.experiments.dir_save')}
              </Button>
            </div>
          </div>
        ) : (
          <>
            <ExperimentMarkdown markdown={analysis.contentMd} onOpenReference={onOpenReference} />
            {analysis.sections.map((section) => (
              <div key={section.key} className={styles.sectionText}>
                <div className={styles.rowBetween}>
                  <strong>{section.title}</strong>
                  <div className={styles.badges}>
                    {section.unconfirmed ? (
                      <Badge tone="warning">{section.unconfirmedReason ?? t('pages.experiments.section_unconfirmed')}</Badge>
                    ) : section.sources.length === 0 ? (
                      <Badge tone="neutral">{t('pages.experiments.section_unsourced')}</Badge>
                    ) : (
                      <Badge tone="success">{t('pages.experiments.section_sourced')}</Badge>
                    )}
                  </div>
                </div>
                {section.sources.length > 0 && (
                  <div className={styles.sectionSources}>
                    {section.sources.map((source) => (
                      <button
                        key={`${source.path}-${source.sha256 ?? ''}`}
                        type="button"
                        className={styles.sourceButton}
                        title={source.sha256}
                        onClick={() => onOpenSource(source.path)}
                      >
                        {source.path}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            ))}
            {analysis.unverifiedClaims.length > 0 && (
              <Banner tone="warning" title={t('pages.experiments.claim_unverified')}>
                <ul>
                  {analysis.unverifiedClaims.map((claim) => (
                    <li key={`${claim.section}-${claim.text}`}>
                      {claim.section}: {claim.text}
                    </li>
                  ))}
                </ul>
              </Banner>
            )}
          </>
        )}

        <Card title={t('pages.experiments.history_meta_title')}>
          <Rows>
            <Row
              label={t('pages.experiments.history_col_model')}
              value={`${detail.modelInfo?.providerName ?? summary?.providerName ?? '—'} · ${
                detail.modelInfo?.modelId ?? summary?.modelId ?? '—'
              }`}
            />
            <Row
              label={t('pages.experiments.history_col_commit')}
              value={detail.gitInfo?.commitShort ?? detail.gitInfo?.commit ?? '—'}
              mono
            />
            <Row
              label={t('pages.experiments.git_branch')}
              value={detail.gitInfo?.branch ?? '—'}
            />
            <Row label={t('pages.experiments.history_col_metrics')} value={detail.metrics.metrics.length} />
          </Rows>
          {detail.modelInfo !== null && detail.modelInfo.sourceRefs.length > 0 && (
            <div className={styles.sectionSources}>
              {detail.modelInfo.sourceRefs.map((ref) =>
                parseRosTarget(ref.locator) === null ? (
                  <span key={`${ref.entityType}-${ref.entityId}-${ref.locator}`} className={styles.mono}>
                    {ref.title || ref.locator}
                  </span>
                ) : (
                  <button
                    key={`${ref.entityType}-${ref.entityId}-${ref.locator}`}
                    type="button"
                    className={styles.sourceButton}
                    title={ref.locator}
                    onClick={() => onOpenReference(ref.locator)}
                  >
                    {ref.title || ref.locator}
                  </button>
                )
              )}
            </div>
          )}
        </Card>
      </div>
    </Card>
  )
}