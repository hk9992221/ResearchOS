import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

import type { Idea, IdeaDetail, IdeaStatus } from '@researchos/shared'

import { Button, Modal } from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { ideasApi } from './api'
import { statusKey } from './model'
import styles from './ideas.module.css'

export interface StatusDialogProps {
  readonly open: boolean
  readonly projectId: string
  readonly idea: IdeaDetail
  readonly target: IdeaStatus | null
  readonly onClose: () => void
  readonly onChanged: (idea: Idea) => void
}

/**
 * One status transition. Rejected needs a non-empty reason before the command
 * is sent; every transition carries an optional note. A failed call keeps the
 * dialog open with the typed text, so a retry costs one click.
 */
export function StatusDialog({
  open,
  projectId,
  idea,
  target,
  onClose,
  onChanged
}: StatusDialogProps): JSX.Element | null {
  const t = useT()
  const queryClient = useQueryClient()
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (target === null) return null
  const requiresReason = target === 'Rejected'

  const reset = (): void => {
    setReason('')
    setNote('')
    setError(null)
    setBusy(false)
  }

  const close = (): void => {
    if (busy) return
    reset()
    onClose()
  }

  const submit = async (): Promise<void> => {
    if (requiresReason && reason.trim() === '') {
      setError(t('pages.ideas.reason_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      const updated = await ideasApi.changeStatus(projectId, idea.id, target, {
        rejectionReason: requiresReason ? reason : undefined,
        note
      })
      await queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(projectId) })
      onChanged(updated)
      reset()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('pages.ideas.toast_status_fail'))
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('pages.ideas.status_label')}
      onClose={close}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            {t('pages.ideas.ai_cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy} aria-busy={busy}>
            {requiresReason ? t('pages.ideas.reason_confirm') : t('pages.ideas.status_confirm')}
          </Button>
        </>
      }
    >
      <p className={styles.dialogText}>
        {t('pages.ideas.status_dialog_desc', {
          title: idea.title,
          status: t(statusKey(target))
        })}
      </p>
      {requiresReason ? (
        <div className={styles.formBlock}>
          <label className={styles.fieldLabel} htmlFor="idea-reject-reason">
            {t('pages.ideas.field_reason')}
            <span className={styles.muted}>{t('pages.ideas.field_reason_hint')}</span>
          </label>
          <textarea
            id="idea-reject-reason"
            className={styles.textarea}
            rows={3}
            value={reason}
            autoFocus
            aria-invalid={error !== null}
            onChange={(event) => {
              setReason(event.target.value)
              setError(null)
            }}
          />
          {error !== null && (
            <p className={styles.fieldError} role="alert">
              {error}
            </p>
          )}
        </div>
      ) : (
        error !== null && (
          <p className={styles.fieldError} role="alert">
            {error}
          </p>
        )
      )}
      <div className={styles.formBlock}>
        <label className={styles.fieldLabel} htmlFor="idea-status-note">
          {t('pages.ideas.field_note')}
          <span className={styles.muted}>{t('pages.ideas.field_note_hint')}</span>
        </label>
        <textarea
          id="idea-status-note"
          className={styles.textarea}
          rows={2}
          value={note}
          onChange={(event) => setNote(event.target.value)}
        />
      </div>
    </Modal>
  )
}