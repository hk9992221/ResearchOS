import { TriangleAlert } from 'lucide-react'

import { Button } from './Button'
import styles from './ErrorState.module.css'

export interface ErrorStateProps {
  readonly title: string
  /** The safe message main returned; already localized by the caller. */
  readonly message?: string
  /** Stable code from the result envelope, shown as the last resort detail. */
  readonly code?: string
  readonly onRetry?: () => void
  readonly retryLabel?: string
}

/**
 * ErrorState is the shared failure surface: what failed, why in the words the
 * main process chose, and one retry when retrying can help. Errors are never
 * swallowed into a blank page.
 */
export function ErrorState({
  title,
  message,
  code,
  onRetry,
  retryLabel
}: ErrorStateProps): JSX.Element {
  return (
    <section className={styles.error} role="alert">
      <TriangleAlert aria-hidden="true" className={styles.icon} strokeWidth={2} />
      <h2 className={styles.title}>{title}</h2>
      {message !== undefined && <p className={styles.message}>{message}</p>}
      {code !== undefined && <code className={styles.code}>{code}</code>}
      {onRetry !== undefined && retryLabel !== undefined && (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {retryLabel}
        </Button>
      )}
    </section>
  )
}