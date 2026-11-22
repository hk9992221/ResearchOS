import { AppError } from '@researchos/shared'
import { useNavigate } from 'react-router-dom'

import { Button, EmptyState, ErrorState, Skeleton } from '../design/components'
import { useT } from '../i18n'
import { isBridgeUnavailable } from '../lib/ipc'
import { iconFor } from './icons'
import styles from './WorkspaceGate.module.css'

export interface WorkspaceGateProps {
  /** The workspace.status query is still running. */
  readonly pending: boolean
  /** workspace.status.open; false means the shell has nothing to show yet. */
  readonly open: boolean
  readonly error?: unknown
  readonly onRetry: () => void
}

/**
 * WorkspaceGate stands in for the shell until a workspace is open.
 *
 * Three honest states, never a blank window: loading, a read failure with the
 * safe message and a retry, and "no workspace" with the one action that leads
 * to setup. The gate is not a page inside the sidebar because no project can
 * exist before a workspace does.
 */
export function WorkspaceGate({ pending, open, error, onRetry }: WorkspaceGateProps): JSX.Element | null {
  const t = useT()
  const navigate = useNavigate()

  if (pending) {
    return (
      <div className={styles.screen}>
        <div className={styles.panel}>
          <Skeleton rows={4} />
        </div>
      </div>
    )
  }

  if (error !== undefined) {
    const message = isBridgeUnavailable(error)
      ? t('pages.workspace.bridge_unavailable')
      : error instanceof Error
        ? error.message
        : String(error)
    return (
      <div className={styles.screen}>
        <div className={styles.panel}>
          <ErrorState
            title={t('pages.workspace.error_title')}
            message={message}
            code={error instanceof AppError ? error.code : undefined}
            retryLabel={t('shell.overview.error_retry')}
            onRetry={onRetry}
          />
        </div>
      </div>
    )
  }

  if (open) return null

  return (
    <div className={styles.screen}>
      <div className={styles.panel}>
        <EmptyState
          icon={iconFor('projects')}
          title={t('pages.workspace.gate_title')}
          description={t('pages.workspace.gate_reason')}
          action={
            <Button variant="primary" onClick={() => navigate('/welcome')}>
              {t('pages.workspace.btn_open')}
            </Button>
          }
        />
      </div>
    </div>
  )
}