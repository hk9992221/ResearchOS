import { useEffect, useRef } from 'react'

import { IconButton } from '../design/components'
import { useT } from '../i18n'
import type { GlobalToast, ToastTone } from '../state/toasts'
import { iconFor, type IconName } from './icons'
import styles from './Toasts.module.css'

export type { ToastTone } from '../state/toasts'

/** One queued toast (state/toasts owns the queue). */
export type ToastMessage = GlobalToast

export interface ToastsProps {
  readonly toasts: readonly GlobalToast[]
  readonly onDismiss: (id: string) => void
}

/** Default lifetime: failures stay longer because they carry a next step. */
export const TOAST_DURATION_MS = 5000
export const TOAST_ERROR_DURATION_MS = 8000

const TONE_ICON: Record<ToastTone, IconName> = {
  success: 'success',
  error: 'failure',
  warning: 'warning',
  info: 'info'
}

interface ToastItemProps {
  readonly toast: ToastMessage
  readonly onDismiss: (id: string) => void
}

function ToastItem({ toast, onDismiss }: ToastItemProps): JSX.Element {
  const t = useT()
  const duration =
    toast.durationMs ??
    (toast.tone === 'error' ? TOAST_ERROR_DURATION_MS : TOAST_DURATION_MS)
  const onDismissRef = useRef(onDismiss)
  onDismissRef.current = onDismiss

  useEffect(() => {
    if (duration <= 0) return
    const timer = window.setTimeout(() => onDismissRef.current(toast.id), duration)
    return () => window.clearTimeout(timer)
  }, [duration, toast.id])

  const Icon = iconFor(TONE_ICON[toast.tone])
  return (
    <div
      className={[styles.toast, styles[toast.tone]].join(' ')}
      role={toast.tone === 'error' ? 'alert' : 'status'}
    >
      <Icon aria-hidden="true" className={styles.icon} strokeWidth={2} />
      <div className={styles.text}>
        <p className={styles.title}>{toast.title}</p>
        {toast.message !== undefined && <p className={styles.message}>{toast.message}</p>}
        {toast.action !== undefined && (
          <button type="button" className={styles.action} onClick={toast.action.onClick}>
            {toast.action.label}
          </button>
        )}
      </div>
      <IconButton
        icon={iconFor('close')}
        label={t('components.toast.dismiss')}
        size="sm"
        onClick={() => onDismiss(toast.id)}
      />
    </div>
  )
}

/**
 * Toasts is the single feedback queue, bottom right above the page (§15.9).
 * Errors announce themselves; the others are polite. Each toast dismisses
 * itself unless the caller keeps it open.
 */
export function Toasts({ toasts, onDismiss }: ToastsProps): JSX.Element | null {
  if (toasts.length === 0) return null
  return (
    <div className={styles.stack}>
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} onDismiss={onDismiss} />
      ))}
    </div>
  )
}