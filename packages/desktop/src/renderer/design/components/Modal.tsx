import { useEffect, useId, useRef, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import styles from './Modal.module.css'

export interface ModalProps {
  readonly open: boolean
  readonly title: string
  readonly onClose: () => void
  readonly children?: ReactNode
  readonly footer?: ReactNode
  /**
   * Escape closes the dialog. Destructive confirmations pass false: the only
   * way out is an explicit Cancel, so a stray keypress cannot dismiss the
   * question the user is answering (plan §3.4 accessibility rule).
   */
  readonly escapeCloses?: boolean
  readonly width?: number
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * Modal is the shared dialog: a scrim over the window, a titled panel, focus
 * moved inside on open and returned on close, Escape to dismiss when the
 * question is not destructive, and Tab kept inside the panel.
 */
export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  escapeCloses = true,
  width
}: ModalProps): JSX.Element | null {
  const titleId = useId()
  const panelRef = useRef<HTMLDivElement>(null)
  const restoreRef = useRef<HTMLElement | null>(null)

  useEffect(() => {
    if (!open) return
    restoreRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    panelRef.current?.focus()
    return () => {
      const target = restoreRef.current
      if (target !== null && document.contains(target)) target.focus()
    }
  }, [open])

  useEffect(() => {
    if (!open || !escapeCloses) return
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open, escapeCloses, onClose])

  if (!open || typeof document === 'undefined') return null

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (event.key !== 'Tab') return
    const panel = panelRef.current
    if (panel === null) return
    const items = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (items.length === 0) {
      event.preventDefault()
      panel.focus()
      return
    }
    const first = items[0]
    const last = items[items.length - 1]
    if (first === undefined || last === undefined) return
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div
      className={styles.backdrop}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={styles.panel}
        style={width === undefined ? undefined : { width }}
        onKeyDown={onKeyDown}
      >
        <header className={styles.header}>
          <h2 id={titleId} className={styles.title}>
            {title}
          </h2>
        </header>
        {children !== undefined && <div className={styles.body}>{children}</div>}
        {footer !== undefined && <footer className={styles.footer}>{footer}</footer>}
      </div>
    </div>,
    document.body
  )
}