import { useEffect, useState } from 'react'

import { Banner, Button, Modal } from '../../design/components'
import { useT } from '../../i18n'
import { weeklyErrorView } from './weekly-errors'
import styles from './weekly.module.css'

const SUGGESTIONS = [
  'pages.weekly_review.revise_suggest_concise',
  'pages.weekly_review.revise_suggest_academic',
  'pages.weekly_review.revise_suggest_detail',
  'pages.weekly_review.revise_suggest_facts',
  'pages.weekly_review.revise_suggest_merge',
  'pages.weekly_review.revise_suggest_logic'
] as const

export interface ReviseDialogProps {
  readonly open: boolean
  readonly sectionTitle: string
  readonly selectionText: string
  readonly onClose: () => void
  /** Resolves after the report is stored; rejections are shown in the dialog. */
  readonly onSubmit: (instructions: string) => Promise<void>
}

/**
 * Selection-driven AI revision. The selection scopes the prompt; the
 * suggestions and the textarea become one-shot instructions that are never
 * persisted as a skill. The section body is replaced by the server response.
 */
export function ReviseDialog({
  open,
  sectionTitle,
  selectionText,
  onClose,
  onSubmit
}: ReviseDialogProps): JSX.Element {
  const t = useT()
  const [instructions, setInstructions] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    if (open) return
    setInstructions('')
    setBusy(false)
    setError(null)
  }, [open])

  const appendSuggestion = (suggestion: string): void => {
    setInstructions((current) => (current.trim() === '' ? suggestion : `${current.trim()}\n${suggestion}`))
  }

  const submit = async (): Promise<void> => {
    setBusy(true)
    setError(null)
    try {
      await onSubmit(instructions.trim())
      onClose()
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const errorView = error === null ? null : weeklyErrorView(error)

  return (
    <Modal
      open={open}
      title={t('pages.weekly_review.revise_title')}
      onClose={busy ? () => undefined : onClose}
      width={680}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.weekly_review.gen_close')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy}>
            {t('pages.weekly_review.revise_submit')}
          </Button>
        </>
      }
    >
      <div className={styles.dialogGrid}>
        <p className={styles.subtle}>
          {t('pages.weekly_review.revise_desc', { title: sectionTitle })}
        </p>
        <p className={styles.subtle}>
          {t('pages.weekly_review.revise_selection_hint', {
            n: Array.from(selectionText).length
          })}
        </p>
        <span className={styles.subtle}>{t('pages.weekly_review.revise_excerpt_label')}</span>
        <blockquote className={styles.reviseSelection}>{selectionText}</blockquote>
        <div className={styles.suggestionRow}>
          {SUGGESTIONS.map((key) => (
            <Button
              key={key}
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => appendSuggestion(t(key))}
            >
              {t(key)}
            </Button>
          ))}
        </div>
        <label className={styles.reviseLabel}>
          {t('pages.weekly_review.revise_notes_label')}
          <textarea
            className={styles.reviseInput}
            rows={5}
            value={instructions}
            disabled={busy}
            placeholder={t('pages.weekly_review.revise_notes_placeholder')}
            onChange={(event) => setInstructions(event.target.value)}
          />
        </label>
        {errorView !== null && (
          <Banner tone="danger" title={t(errorView.labelKey)}>
            <span className={styles.errorMessage}>{errorView.message}</span>
          </Banner>
        )}
      </div>
    </Modal>
  )
}
