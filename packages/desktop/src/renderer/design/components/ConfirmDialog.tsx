import { type ReactNode } from 'react'

import { Button } from './Button'
import { Modal } from './Modal'
import styles from './ConfirmDialog.module.css'

export interface ConfirmDialogProps {
  readonly open: boolean
  readonly title: string
  readonly description?: string
  readonly confirmLabel: string
  readonly cancelLabel: string
  /** Destructive actions are marked and cannot be dismissed with Escape. */
  readonly tone?: 'default' | 'danger'
  readonly busy?: boolean
  readonly onConfirm: () => void
  readonly onCancel: () => void
  /** Extra content between the description and the footer (impact, note). */
  readonly children?: ReactNode
}

/**
 * ConfirmDialog asks one question with two answers. The confirm button is the
 * only way to say yes; Cancel is always present. A destructive dialog ignores
 * Escape so the question is answered explicitly.
 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  cancelLabel,
  tone = 'default',
  busy = false,
  onConfirm,
  onCancel,
  children
}: ConfirmDialogProps): JSX.Element {
  return (
    <Modal
      open={open}
      title={title}
      onClose={onCancel}
      escapeCloses={tone !== 'danger'}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button
            variant={tone === 'danger' ? 'danger' : 'primary'}
            onClick={onConfirm}
            disabled={busy}
            aria-busy={busy ? true : undefined}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {description !== undefined && <p className={styles.description}>{description}</p>}
      {children}
    </Modal>
  )
}