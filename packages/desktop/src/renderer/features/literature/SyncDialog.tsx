/**
 * PDF sync check. `literature.sync.check` compares the Zotero attachment with
 * the local copy (hash and size); the dialog lists changed, unchanged and
 * missing copies with both hashes. Applying is an explicit, per-paper danger
 * confirmation: only checked papers are overwritten, and the service re-checks
 * the source hash before it writes.
 */

import { useEffect, useState } from 'react'

import type { SyncApplyResult, SyncItem, SyncReport } from '@researchos/shared'

import { Badge, Banner, Button, ConfirmDialog, Modal, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { literatureApi } from './api'
import { formatBytes, formatSavedAt, shortHash } from './model'
import styles from './literature.module.css'

const MISSING_KEY: Readonly<Record<string, string>> = {
  no_pdf_attachment: 'pages.literature.sync_missing_source',
  local_file_missing: 'pages.literature.sync_missing_local',
  source_unavailable: 'pages.literature.sync_source_unavailable'
}

export interface SyncDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly onClose: () => void
  readonly onApplied: () => void
}

export function SyncDialog({ open, projectId, onClose, onApplied }: SyncDialogProps): JSX.Element {
  const t = useT()
  const [report, setReport] = useState<SyncReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [overwrite, setOverwrite] = useState<ReadonlySet<string>>(new Set())
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [applying, setApplying] = useState(false)
  const [result, setResult] = useState<SyncApplyResult | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) {
      setReport(null)
      setError(null)
      setOverwrite(new Set())
      setResult(null)
      setApplyError(null)
      setConfirmOpen(false)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    setResult(null)
    literatureApi
      .syncCheck(projectId)
      .then((next) => {
        if (!cancelled) setReport(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, projectId])

  const changed = report?.items.filter((item) => item.changed && !item.missing) ?? []
  const unchanged = report?.items.filter((item) => !item.changed) ?? []
  const missing = report?.items.filter((item) => item.missing) ?? []

  const toggle = (paperId: string): void => {
    setOverwrite((current) => {
      const next = new Set(current)
      if (next.has(paperId)) next.delete(paperId)
      else next.add(paperId)
      return next
    })
  }

  const apply = async (): Promise<void> => {
    setApplying(true)
    setApplyError(null)
    try {
      const decisions = changed.map((item) => ({
        paperId: item.paperId,
        action: overwrite.has(item.paperId) ? ('overwrite' as const) : ('skip' as const)
      }))
      const next = await literatureApi.syncApply(projectId, decisions)
      setResult(next)
      onApplied()
    } catch (cause) {
      setApplyError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setApplying(false)
      setConfirmOpen(false)
    }
  }

  const hashCell = (item: SyncItem): JSX.Element => (
    <div className={styles.diffMeta}>
      <span>
        {t('pages.literature.sync_local', {
          time: item.localHash === null ? '—' : formatSavedAt(report?.checkedAt ?? ''),
          hash: shortHash(item.localHash) || '—'
        })}
      </span>
      <span>
        {t('pages.literature.sync_source', {
          size: formatBytes(item.sourceSize) || '—',
          hash: shortHash(item.sourceHash) || '—'
        })}
      </span>
      {item.warnings.length > 0 && (
        <span>{item.warnings.map((warning) => t(MISSING_KEY[warning] ?? warning)).join(' · ')}</span>
      )}
      {item.error !== null && <span className={styles.mono}>{item.error}</span>}
    </div>
  )

  return (
    <>
      <Modal
        open={open}
        title={t('pages.literature.sync_title')}
        width={820}
        onClose={onClose}
        footer={
          result !== null ? (
            <Button variant="primary" onClick={onClose}>
              {t('pages.literature.btn_close')}
            </Button>
          ) : (
            <div className={styles.footerRow}>
              <Button variant="secondary" onClick={onClose} disabled={applying}>
                {t('pages.literature.btn_cancel')}
              </Button>
              <Button
                variant="primary"
                disabled={loading || applying || overwrite.size === 0 || changed.length === 0}
                onClick={() => setConfirmOpen(true)}
              >
                {t('pages.literature.sync_apply')}
              </Button>
            </div>
          )
        }
      >
        {loading && <Skeleton rows={6} />}
        {error !== null && (
          <Banner tone="danger" title={t('pages.literature.sync_fail')}>
            {error}
          </Banner>
        )}
        {applyError !== null && (
          <Banner tone="danger" title={t('pages.literature.sync_fail')}>
            {applyError}
          </Banner>
        )}
        {result !== null && (
          <Banner tone={result.failures.length === 0 ? 'success' : 'warning'}>
            {t('pages.literature.sync_result', {
              applied: result.applied,
              skipped: result.skipped,
              conflicts: result.conflicted.length
            })}
            {result.failures.length > 0 && (
              <ul className={styles.outcomeList}>
                {result.failures.map((failure) => (
                  <li key={failure.paperId} className={styles.outcomeRow}>
                    <span className={styles.outcomeTitle}>{failure.paperId}</span>
                    <span className={styles.subtle}>{failure.error}</span>
                  </li>
                ))}
              </ul>
            )}
          </Banner>
        )}
        {report !== null && result === null && (
          <div className={styles.stack}>
            <p className={styles.subtle}>
              {t('pages.literature.sync_checked', { count: report.checked, source: report.source })}
            </p>
            {changed.length === 0 && missing.length === 0 ? (
              <Banner tone="success">{t('pages.literature.sync_all_same')}</Banner>
            ) : null}

            {changed.length > 0 && (
              <section className={styles.diffGroup}>
                <div className={styles.diffHead}>
                  <h3 className={styles.sectionTitle}>{t('pages.literature.sync_changed')}</h3>
                  <Badge tone="warning">{changed.length}</Badge>
                </div>
                {changed.map((item) => (
                  <div key={item.paperId} className={styles.diffRow}>
                    <label className={styles.itemBody}>
                      <span className={styles.itemTitle}>
                        <input
                          type="checkbox"
                          className={styles.checkbox}
                          checked={overwrite.has(item.paperId)}
                          onChange={() => toggle(item.paperId)}
                          aria-label={t('pages.literature.sync_overwrite')}
                        />{' '}
                        {item.title || t('pages.paper.untitled')}
                      </span>
                      {hashCell(item)}
                    </label>
                    <div className={styles.diffSide}>
                      <Badge tone={overwrite.has(item.paperId) ? 'danger' : 'neutral'}>
                        {t(
                          overwrite.has(item.paperId)
                            ? 'pages.literature.sync_overwrite'
                            : 'pages.literature.sync_keep'
                        )}
                      </Badge>
                    </div>
                  </div>
                ))}
              </section>
            )}

            {missing.length > 0 && (
              <section className={styles.diffGroup}>
                <div className={styles.diffHead}>
                  <h3 className={styles.sectionTitle}>{t('pages.literature.sync_missing')}</h3>
                  <Badge tone="warning">{missing.length}</Badge>
                </div>
                {missing.map((item) => (
                  <div key={item.paperId} className={styles.diffRow}>
                    <div className={styles.itemBody}>
                      <span className={styles.itemTitle}>
                        {item.title || t('pages.paper.untitled')}
                      </span>
                      {hashCell(item)}
                    </div>
                  </div>
                ))}
              </section>
            )}

            {unchanged.length > 0 && (
              <section className={styles.diffGroup}>
                <div className={styles.diffHead}>
                  <h3 className={styles.sectionTitle}>{t('pages.literature.sync_unchanged')}</h3>
                  <Badge tone="success">{unchanged.length}</Badge>
                </div>
                {unchanged.map((item) => (
                  <div key={item.paperId} className={styles.diffRow}>
                    <span className={styles.itemTitle}>
                      {item.title || t('pages.paper.untitled')}
                    </span>
                    <span className={styles.mono}>{shortHash(item.localHash) || '—'}</span>
                  </div>
                ))}
              </section>
            )}
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={confirmOpen}
        title={t('pages.literature.sync_apply_confirm_title')}
        description={t('pages.literature.sync_apply_confirm_message', { count: overwrite.size })}
        confirmLabel={t('pages.literature.sync_apply')}
        cancelLabel={t('pages.literature.btn_cancel')}
        tone="danger"
        busy={applying}
        onConfirm={() => void apply()}
        onCancel={() => setConfirmOpen(false)}
      />
    </>
  )
}