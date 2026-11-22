/**
 * Unlink dialog. One `papers.unlink` call runs per paper so a single failure
 * cannot hide the others; the dialog then lists every outcome and the page
 * reloads once. Paper Notes, copies and the Zotero library stay untouched.
 */

import { useEffect, useState } from 'react'

import type { ProjectPaper } from '@researchos/shared'

import { Banner, Button, Modal } from '../../design/components'
import { useT } from '../../i18n'
import { literatureApi } from './api'
import { paperDisplay } from './model'
import styles from './literature.module.css'

export interface UnlinkOutcome {
  readonly paper: ProjectPaper
  readonly ok: boolean
  readonly error: string | null
}

export interface UnlinkDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly papers: readonly ProjectPaper[]
  readonly onClose: () => void
  readonly onSettled: (outcomes: readonly UnlinkOutcome[]) => void
}

export function UnlinkDialog({
  open,
  projectId,
  papers,
  onClose,
  onSettled
}: UnlinkDialogProps): JSX.Element {
  const t = useT()
  const [outcomes, setOutcomes] = useState<readonly UnlinkOutcome[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) {
      setOutcomes([])
      setBusy(false)
    }
  }, [open])

  const single = papers.length === 1
  const first = papers[0]
  const notesKept = papers.filter((paper) => paper.noteId !== null).length
  const finished = outcomes.length > 0

  const run = async (): Promise<void> => {
    setBusy(true)
    const next: UnlinkOutcome[] = []
    for (const paper of papers) {
      try {
        await literatureApi.unlink(projectId, paper.id)
        next.push({ paper, ok: true, error: null })
      } catch (cause) {
        next.push({
          paper,
          ok: false,
          error: cause instanceof Error ? cause.message : t('pages.literature.toast_delete_fail')
        })
      }
    }
    setOutcomes(next)
    setBusy(false)
    onSettled(next)
  }

  const failed = outcomes.filter((outcome) => !outcome.ok).length

  return (
    <Modal
      open={open}
      title={t('pages.literature.delete_title')}
      onClose={() => {
        if (busy) return
        onClose()
      }}
      footer={
        finished ? (
          <Button variant="primary" onClick={onClose}>
            {t('pages.literature.btn_close')}
          </Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose} disabled={busy}>
              {t('pages.literature.btn_cancel')}
            </Button>
            <Button variant="danger" onClick={() => void run()} disabled={busy || papers.length === 0} aria-busy={busy}>
              {t('pages.literature.btn_delete_confirm')}
            </Button>
          </>
        )
      }
    >
      <div className={styles.stack}>
        {!finished && (
          <p>
            {single && first !== undefined
              ? t('pages.literature.delete_message', {
                  title: paperDisplay(first).title || t('pages.paper.untitled')
                })
              : t('pages.literature.delete_batch_message', { count: papers.length })}
          </p>
        )}
        {!finished && notesKept > 0 && (
          <p className={styles.subtle}>{t('pages.literature.delete_notes_kept', { count: notesKept })}</p>
        )}
        {finished && (
          <div className={styles.section}>
            <h3 className={styles.sectionTitle}>{t('pages.literature.unlink_outcomes')}</h3>
            {failed > 0 && (
              <Banner tone={failed === outcomes.length ? 'danger' : 'warning'}>
                {t('pages.literature.toast_delete_partial', { failed })}
              </Banner>
            )}
            <ul className={styles.outcomeList}>
              {outcomes.map((outcome) => (
                <li key={outcome.paper.id} className={styles.outcomeRow}>
                  <span className={styles.outcomeTitle}>
                    {paperDisplay(outcome.paper).title || t('pages.paper.untitled')}
                  </span>
                  <span className={outcome.ok ? styles.subtle : styles.mono}>
                    {outcome.ok ? t('pages.literature.unlink_ok') : outcome.error}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </Modal>
  )
}