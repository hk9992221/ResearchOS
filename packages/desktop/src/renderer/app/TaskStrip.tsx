import { useT } from '../i18n'
import { Button } from '../design/components'
import styles from './TaskStrip.module.css'

export interface TaskProgress {
  readonly id: string
  readonly name: string
  /** The phase the operation is in; shown next to the name when present. */
  readonly stage?: string
  readonly done?: number
  readonly total?: number
  /** 0–100 from `job.progress`, used when the job has no done/total count. */
  readonly percent?: number
  readonly canceling?: boolean
  readonly failed?: boolean
  /** Only jobs whose producer registered a cancel command offer the button. */
  readonly cancellable?: boolean
  /** Safe error text of a failed task. */
  readonly error?: string
}

export interface TaskStripProps {
  readonly tasks: readonly TaskProgress[]
  readonly onCancel?: (id: string) => void
  readonly onClear?: () => void
}

/**
 * TaskStrip is the global long-operation indicator under the toolbar (§17):
 * running tasks with their stage and progress, failures until they are
 * cleared. It is fed by props; the job events of the main process arrive in
 * phase 2.
 */
export function TaskStrip({ tasks, onCancel, onClear }: TaskStripProps): JSX.Element | null {
  const t = useT()
  if (tasks.length === 0) return null

  const failed = tasks.filter((task) => task.failed === true)
  const running = tasks.filter((task) => task.failed !== true)

  return (
    <div className={styles.strip}>
      {running.map((task) => (
        <div key={task.id} className={styles.task}>
          <span className={styles.name}>
            {task.stage === undefined || task.stage === ''
              ? task.name
              : t('shell.task.strip_stage', { name: task.name, stage: task.stage })}
          </span>
          {task.total !== undefined && task.total > 0 && (
            <span
              className={styles.progress}
              role="progressbar"
              aria-label={t('shell.task.progress_count', {
                done: task.done ?? 0,
                total: task.total
              })}
              aria-valuemin={0}
              aria-valuemax={task.total}
              aria-valuenow={task.done ?? 0}
            >
              <span
                className={styles.bar}
                style={{ width: `${Math.min(100, ((task.done ?? 0) / task.total) * 100)}%` }}
              />
            </span>
          )}
          {task.percent !== undefined && (task.total === undefined || task.total <= 0) && (
            <span
              className={styles.progress}
              role="progressbar"
              aria-label={t('pages.workspace.percent', { percent: Math.round(task.percent) })}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(task.percent)}
            >
              <span
                className={styles.bar}
                style={{ width: `${Math.min(100, Math.max(0, task.percent))}%` }}
              />
            </span>
          )}
          {task.canceling === true ? (
            <span className={styles.canceling}>{t('shell.task.canceling')}</span>
          ) : (
            onCancel !== undefined &&
            task.cancellable !== false && (
              <Button variant="ghost" size="sm" onClick={() => onCancel(task.id)}>
                {t('shell.task.btn_cancel')}
              </Button>
            )
          )}
        </div>
      ))}
      {failed.map((task) => (
        <div key={task.id} className={styles.failure}>
          {task.error === undefined
            ? t('shell.task.failed', { task: task.name })
            : t('shell.task.failed_detail', { task: task.name, err: task.error })}
        </div>
      ))}
      {failed.length > 0 && onClear !== undefined && (
        <Button variant="ghost" size="sm" onClick={onClear}>
          {t('shell.task.btn_clear')}
        </Button>
      )}
    </div>
  )
}