import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import {
  FILE_KINDS,
  type ExperimentView,
  type FileKind,
  type ScanPlan,
  type ScanRules,
  type TargetRef
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Rows,
  Row,
  SelectField,
  SwitchField,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { formatBytes } from '../../lib/bytes'
import { queryKeys } from '../../lib/query'
import { experimentsApi } from './api'
import {
  draftFromRules,
  parseRulesDraft,
  readJobFailure,
  type JobFailure
} from './model'
import { useJobOutcome } from './useJobOutcome'
import styles from './experiments.module.css'

const TREE_LIMIT = 2000

export interface ScanTabProps {
  readonly projectId: string
  readonly target: TargetRef
  readonly experiment: ExperimentView
  readonly archived: boolean
  readonly selectedPath: string | null
  readonly onSelectPath: (path: string | null) => void
  readonly notify: (input: {
    tone: 'success' | 'error' | 'warning' | 'info'
    title: string
    message?: string
  }) => void
}

function kindKey(kind: FileKind): string {
  return `pages.experiments.kind_${kind}`
}

function reasonKey(reason: string): string {
  const known = [
    'too_large',
    'model_weight',
    'cache',
    'binary',
    'secret',
    'user_exclude',
    'archive_or_media',
    'symlink_outside',
    'unreadable'
  ]
  return known.includes(reason)
    ? `pages.experiments.reason_${reason}`
    : 'pages.experiments.reason_other'
}

/**
 * Scan tab: the rules editor, the read-only scan job and the frozen manifest.
 *
 * The job's result is not read from the event (it carries none); after success
 * the deterministic preview is re-read with the same override, which is the
 * same plan the scan produced. Nothing here writes the folder.
 */
export function ScanTab({
  projectId,
  target,
  experiment,
  archived,
  selectedPath,
  onSelectPath,
  notify
}: ScanTabProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(() => draftFromRules(experiment.scanRules))
  const [rulesError, setRulesError] = useState<string | null>(null)
  const [savingRules, setSavingRules] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [runOverride, setRunOverride] = useState<ScanRules | null>(null)
  const [plan, setPlan] = useState<ScanPlan | null>(null)
  const [failure, setFailure] = useState<JobFailure | null>(null)
  const [scanError, setScanError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [kindFilter, setKindFilter] = useState<'all' | FileKind>('all')

  const rulesSignature = JSON.stringify(experiment.scanRules)
  useEffect(() => {
    setDraft(draftFromRules(experiment.scanRules))
    // The signature keeps a refetch that returns an equal rule set from
    // discarding edits in progress.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [experiment.id, rulesSignature])

  const parsed = useMemo(() => parseRulesDraft(draft), [draft])

  const runScan = async (): Promise<void> => {
    setRulesError(null)
    setFailure(null)
    setScanError(null)
    if (parsed.rules === undefined) {
      setRulesError(t('pages.experiments.rules_bad_number'))
      return
    }
    try {
      setRunOverride(parsed.rules)
      const started = await experimentsApi.scanStart(projectId, target, parsed.rules)
      setJobId(started.jobId)
    } catch (cause) {
      setScanError(
        cause instanceof Error ? cause.message : t('pages.experiments.banner_scan_failed')
      )
    }
  }

  useJobOutcome(jobId, (outcome) => {
    setJobId(null)
    if (outcome.state !== 'succeeded') {
      if (outcome.state === 'failed') setFailure(readJobFailure(outcome.error))
      return
    }
    if (runOverride === null) return
    void experimentsApi
      .scanPreview(projectId, target, runOverride)
      .then((next) => {
        setPlan(next)
        notify({
          tone: 'success',
          title: t('pages.experiments.scan_done', {
            files: next.totalFiles,
            included: next.includedFiles,
            warnings: next.warnings.length
          })
        })
      })
      .catch((cause: unknown) => {
        setScanError(
          cause instanceof Error ? cause.message : t('pages.experiments.banner_scan_failed')
        )
      })
  })

  const saveRules = async (): Promise<void> => {
    setRulesError(null)
    if (parsed.rules === undefined) {
      setRulesError(t('pages.experiments.rules_bad_number'))
      return
    }
    setSavingRules(true)
    try {
      await experimentsApi.update(
        projectId,
        experiment.id,
        { scanRules: parsed.rules as ScanRules },
        experiment.updatedAt
      )
      await queryClient.invalidateQueries({
        queryKey: queryKeys.experiments.detail(projectId, experiment.id)
      })
      notify({ tone: 'success', title: t('pages.experiments.toast_rules_saved') })
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.experiments.banner_save_failed'),
        message: cause instanceof Error ? cause.message : undefined
      })
    } finally {
      setSavingRules(false)
    }
  }

  const entries = useMemo(() => {
    if (plan === null) return []
    const needle = search.trim().toLowerCase()
    return plan.entries
      .filter((entry) => {
        if (kindFilter !== 'all' && entry.kind !== kindFilter) return false
        if (needle !== '' && !entry.path.toLowerCase().includes(needle)) return false
        return true
      })
      .slice(0, TREE_LIMIT)
  }, [plan, search, kindFilter])

  const selectedEntry = entries.find((entry) => entry.path === selectedPath) ?? null

  const rulesCard = (
    <Card
      title={t('pages.experiments.rules_title')}
      actions={
        <div className={styles.actions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void saveRules()}
            disabled={archived || savingRules}
            aria-busy={savingRules}
            title={archived ? t('pages.experiments.readonly_tooltip') : undefined}
          >
            {t('pages.experiments.dir_save')}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={() => void runScan()}
            disabled={archived || jobId !== null || experiment.dirRef === null}
            title={
              archived
                ? t('pages.experiments.readonly_tooltip')
                : experiment.dirRef === null
                  ? t('pages.experiments.scan_nodir_reason')
                  : undefined
            }
          >
            {t('pages.experiments.btn_run_scan')}
          </Button>
        </div>
      }
    >
      <div className={styles.stack}>
        <div className={styles.formGrid}>
          <label className={styles.stack}>
            <span className={styles.muted}>{t('pages.experiments.rules_include')}</span>
            <textarea
              className={styles.editorArea}
              aria-label={t('pages.experiments.rules_include')}
              placeholder="**/*.py"
              value={draft.includeText}
              onChange={(event) => setDraft({ ...draft, includeText: event.target.value })}
              rows={5}
            />
          </label>
          <label className={styles.stack}>
            <span className={styles.muted}>{t('pages.experiments.rules_exclude')}</span>
            <textarea
              className={styles.editorArea}
              aria-label={t('pages.experiments.rules_exclude')}
              placeholder=".git/"
              value={draft.excludeText}
              onChange={(event) => setDraft({ ...draft, excludeText: event.target.value })}
              rows={5}
            />
          </label>
        </div>
        <p className={styles.reason}>{t('pages.experiments.rules_desc')}</p>
        <div className={styles.formGrid}>
          <TextInput
            label={t('pages.experiments.rules_max_file')}
            value={draft.maxFileBytes}
            onChange={(value) => setDraft({ ...draft, maxFileBytes: value })}
            inputMode="numeric"
          />
          <TextInput
            label={t('pages.experiments.rules_max_total')}
            value={draft.maxTotalBytes}
            onChange={(value) => setDraft({ ...draft, maxTotalBytes: value })}
            inputMode="numeric"
          />
          <TextInput
            label={t('pages.experiments.rules_max_files')}
            value={draft.maxFiles}
            onChange={(value) => setDraft({ ...draft, maxFiles: value })}
            inputMode="numeric"
          />
          <TextInput
            label={t('pages.experiments.rules_hash')}
            value={draft.hashMaxBytes}
            onChange={(value) => setDraft({ ...draft, hashMaxBytes: value })}
            inputMode="numeric"
          />
        </div>
        <SwitchField
          label={t('pages.experiments.rules_images')}
          checked={draft.includeImagesInAi}
          onChange={(checked) => setDraft({ ...draft, includeImagesInAi: checked })}
          disabled={archived}
        />
        <p className={styles.reason}>{t('pages.experiments.rules_git_forced')}</p>
        {rulesError !== null && (
          <p className={styles.errorText} role="alert">
            {rulesError}
          </p>
        )}
      </div>
    </Card>
  )

  if (experiment.dirRef === null) {
    return (
      <div className={styles.panel}>
        {rulesCard}
        <EmptyState
          title={t('pages.experiments.scan_nodir_title')}
          description={t('pages.experiments.scan_nodir_reason')}
        />
      </div>
    )
  }

  return (
    <div className={styles.panel}>
      {rulesCard}

      {scanError !== null && (
        <Banner tone="danger" title={t('pages.experiments.banner_scan_failed')}>
          {scanError}
        </Banner>
      )}

      {failure !== null && (
        <Banner tone="danger" title={t('pages.experiments.banner_scan_failed')}>
          <div className={styles.badges}>
            {failure.cause !== null && <Badge tone="danger">{failure.cause}</Badge>}
            <span>{failure.message ?? failure.code ?? ''}</span>
          </div>
        </Banner>
      )}

      {plan === null ? (
        <EmptyState
          title={t('pages.experiments.scan_title')}
          description={t('pages.experiments.scan_readonly_note')}
        />
      ) : (
        <>
          {plan.truncated && (
            <Banner tone="warning" title={t('pages.experiments.scan_truncated_title')}>
              {t('pages.experiments.scan_truncated_message')}
            </Banner>
          )}
          {plan.warnings.length > 0 && (
            <Banner tone="warning">
              <ul>
                {plan.warnings.map((warning) => (
                  <li key={`${warning.code}-${warning.path ?? ''}`}>
                    {warning.path === undefined ? warning.message : `${warning.path}: ${warning.message}`}
                  </li>
                ))}
              </ul>
            </Banner>
          )}

          <Card title={t('pages.experiments.scan_summary')}>
            <Rows>
              <Row label={t('pages.experiments.scan_total_files')} value={plan.totalFiles} />
              <Row
                label={t('pages.experiments.scan_total_bytes')}
                value={formatBytes(plan.totalBytes, t)}
              />
              <Row label={t('pages.experiments.scan_ai_files')} value={plan.includedFiles} />
              <Row
                label={t('pages.experiments.scan_ai_bytes')}
                value={formatBytes(plan.includedBytes, t)}
              />
              <Row
                label={t('pages.experiments.scan_context_bytes')}
                value={formatBytes(plan.contextBytes, t)}
              />
            </Rows>
          </Card>

          <div className={styles.rowBetween}>
            <div className={styles.actions}>
              <div className={styles.toolbarSearch}>
                <TextInput
                  label={t('pages.experiments.filter_path')}
                  placeholder={t('pages.experiments.filter_path_hint')}
                  value={search}
                  onChange={setSearch}
                  hideLabel
                  type="search"
                />
              </div>
              <SelectField
                label={t('pages.experiments.filter_kind')}
                value={kindFilter}
                onChange={(value) => setKindFilter(value as 'all' | FileKind)}
                options={[
                  { value: 'all', label: t('pages.experiments.kind_all') },
                  ...FILE_KINDS.map((kind) => ({ value: kind, label: t(kindKey(kind)) }))
                ]}
              />
            </div>
            <span className={styles.muted}>
              {t('pages.experiments.tree_count', {
                files: plan.entries.length,
                included: plan.includedFiles
              })}
            </span>
          </div>

          <div className={styles.tree} role="tree" aria-label={t('pages.experiments.scan_title')}>
            {plan.pruned.map((entry) => (
              <div key={`pruned-${entry.path}`} className={styles.prunedRow}>
                <span className={styles.treePath}>{entry.path}</span>{' '}
                — {t('pages.experiments.tree_pruned', { reason: t(reasonKey(entry.reason)) })}
              </div>
            ))}
            {entries.length === 0 ? (
              <div className={styles.emptyBlock}>{t('pages.experiments.tree_empty_reason')}</div>
            ) : (
              entries.map((entry) => (
                <button
                  key={entry.path}
                  type="button"
                  role="treeitem"
                  aria-selected={entry.path === selectedPath}
                  className={[styles.treeRow, entry.path === selectedPath ? styles.treeRowSelected : ''].join(' ')}
                  onClick={() => onSelectPath(entry.path)}
                >
                  <span className={styles.treePath}>{entry.path}</span>
                  <span className={styles.treeMeta}>
                    <Badge tone="neutral">{t(kindKey(entry.kind))}</Badge>
                    <span className={styles.muted}>{formatBytes(entry.size, t)}</span>
                    {entry.sha256 === null ? (
                      <span className={styles.reason}>
                        {entry.hashSkipped === null
                          ? ''
                          : t('pages.experiments.file_hash_skipped', {
                              reason: entry.hashSkipped
                            })}
                      </span>
                    ) : (
                      <span className={styles.hash} title={entry.sha256}>
                        {entry.sha256.slice(0, 12)}…
                      </span>
                    )}
                    {entry.exclusionReason !== null && (
                      <Badge tone="warning">
                        {t('pages.experiments.tree_excluded', {
                          reason: t(reasonKey(entry.exclusionReason))
                        })}
                      </Badge>
                    )}
                    {entry.contentEligible ? (
                      <Badge tone="success">{t('pages.experiments.tree_sends')}</Badge>
                    ) : (
                      <span className={styles.reason}>{t('pages.experiments.tree_metadata_only')}</span>
                    )}
                    {entry.sampled && (
                      <Badge tone="info">{t('pages.experiments.tree_sampled')}</Badge>
                    )}
                  </span>
                </button>
              ))
            )}
          </div>

          {plan.entries.length > TREE_LIMIT && (
            <p className={styles.reason}>
              {t('pages.experiments.tree_paged', { limit: TREE_LIMIT })}
            </p>
          )}

          {selectedEntry !== null && (
            <Card title={selectedEntry.path}>
              <Rows>
                <Row label={t('pages.experiments.filter_kind')} value={t(kindKey(selectedEntry.kind))} />
                <Row label={t('pages.experiments.scan_total_bytes')} value={formatBytes(selectedEntry.size, t)} />
                <Row label={t('pages.experiments.history_col_time')} value={selectedEntry.mtime} />
                <Row
                  label={t('pages.experiments.metric_source')}
                  value={
                    selectedEntry.sha256 === null ? (
                      t('pages.experiments.file_hash_skipped', {
                        reason: selectedEntry.hashSkipped ?? '—'
                      })
                    ) : (
                      <span className={styles.hash}>{selectedEntry.sha256}</span>
                    )
                  }
                />
              </Rows>
              {selectedEntry.exclusionReason !== null && (
                <p className={styles.reason}>
                  {t('pages.experiments.file_excluded_reason', {
                    reason: t(reasonKey(selectedEntry.exclusionReason))
                  })}
                </p>
              )}
            </Card>
          )}
        </>
      )}
    </div>
  )
}