import { useMemo } from 'react'
import { TriangleAlert } from 'lucide-react'

import type { TaskSummary } from '@researchos/shared'

import { Button } from '../../design/components'
import { useT } from '../../i18n'
import {
  formatDate,
  formatDateTime,
  isOverdue,
  sortTasks,
  type SortDirection,
  type TaskSortKey
} from './board-model'
import styles from './kanban.module.css'

export interface TableSort {
  readonly key: TaskSortKey
  readonly direction: SortDirection
}

export interface TaskTableProps {
  readonly label: string
  readonly tasks: readonly TaskSummary[]
  readonly sort: TableSort
  readonly onSortChange: (sort: TableSort) => void
  readonly onOpen: (task: TaskSummary) => void
  readonly empty: JSX.Element
  /** Archive tables replace the counters with the archived date and an action. */
  readonly archive?: boolean
  readonly onUnarchive?: (task: TaskSummary) => void
  readonly unarchiveLabel?: string
}

const PRIORITY_KEY: Readonly<Record<string, string>> = {
  high: 'pages.kanban.priority_high',
  medium: 'pages.kanban.priority_medium',
  low: 'pages.kanban.priority_low',
  none: 'pages.kanban.priority_none'
}

const PRIORITY_DOT: Readonly<Record<string, string | undefined>> = {
  high: styles.dotHigh,
  medium: styles.dotMedium,
  low: styles.dotLow,
  none: styles.dotNone
}

/**
 * TaskTable is the shared List/Blocked/Archive table. Sorting is local: the
 * service page is already bounded (limit 200), and the table must sort every
 * visible column, not only the four server sorts.
 */
export function TaskTable({
  label,
  tasks,
  sort,
  onSortChange,
  onOpen,
  empty,
  archive = false,
  onUnarchive,
  unarchiveLabel
}: TaskTableProps): JSX.Element {
  const t = useT()
  const rows = useMemo(() => sortTasks(tasks, sort.key, sort.direction), [tasks, sort])

  if (tasks.length === 0) return empty

  const header = (key: TaskSortKey, labelText: string): JSX.Element => {
    const active = sort.key === key
    return (
      <th
        scope="col"
        aria-sort={active ? (sort.direction === 'asc' ? 'ascending' : 'descending') : 'none'}
      >
        <button
          type="button"
          className={[styles.sortButton, active ? styles.sortActive : ''].join(' ')}
          onClick={() =>
            onSortChange({
              key,
              direction: active && sort.direction === 'asc' ? 'desc' : 'asc'
            })
          }
        >
          {labelText}
          {active && <span aria-hidden="true">{sort.direction === 'asc' ? '↑' : '↓'}</span>}
        </button>
      </th>
    )
  }

  return (
    <div className={styles.tableWrap}>
      <table className={styles.table} aria-label={label}>
        <thead>
          <tr>
            {header('title', t('pages.kanban.col_title'))}
            {header('column', t('pages.kanban.col_column'))}
            {header('priority', t('pages.kanban.col_priority'))}
            {header('deadline', t('pages.kanban.col_deadline'))}
            {archive ? (
              <th scope="col">{t('pages.kanban.col_archived')}</th>
            ) : (
              <>
                {header('labels', t('pages.kanban.col_labels'))}
                {header('subtasks', t('pages.kanban.col_subtasks'))}
                {header('blocked', t('pages.kanban.col_blocked'))}
                {header('updated', t('pages.kanban.col_updated'))}
              </>
            )}
            {archive && <th scope="col">{t('pages.kanban.col_actions')}</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((task) => {
            const priorityLabel = t(PRIORITY_KEY[task.priority] ?? 'pages.kanban.priority_none')
            return (
              <tr key={task.id} data-task-id={task.id}>
                <td>
                  <button type="button" className={styles.titleButton} onClick={() => onOpen(task)}>
                    {task.title}
                  </button>
                </td>
                <td>{task.column}</td>
                <td>
                  <span
                    role="img"
                    aria-label={priorityLabel}
                    title={priorityLabel}
                    className={[styles.dot, PRIORITY_DOT[task.priority] ?? ''].join(' ')}
                  />
                </td>
                <td
                  className={
                    task.deadline === null
                      ? styles.cellMuted
                      : isOverdue(task.deadline)
                        ? styles.cellOverdue
                        : undefined
                  }
                >
                  {task.deadline ?? t('pages.kanban.no_deadline')}
                </td>
                {archive ? (
                  <td>{formatDate(task.archivedAt)}</td>
                ) : (
                  <>
                    <td>
                      {task.labels.length === 0 ? (
                        <span className={styles.cellMuted}>—</span>
                      ) : (
                        <span className={styles.labels}>
                          {task.labels.map((label) => (
                            <span key={label} className={styles.label}>
                              {label}
                            </span>
                          ))}
                        </span>
                      )}
                    </td>
                    <td>
                      {task.subtaskTotal === 0
                        ? '—'
                        : t('pages.kanban.count_pair', {
                            done: task.subtaskDone,
                            total: task.subtaskTotal
                          })}
                    </td>
                    <td>
                      {task.blocked ? (
                        <span className={styles.blockedBadge}>
                          <TriangleAlert aria-hidden="true" size={14} />
                          {t('pages.kanban.blocked_yes')}
                        </span>
                      ) : (
                        <span className={styles.cellMuted}>—</span>
                      )}
                    </td>
                    <td className={styles.cellMuted}>{formatDateTime(task.updatedAt)}</td>
                  </>
                )}
                {archive && (
                  <td>
                    <div className={styles.listRowActions}>
                      <Button size="sm" onClick={() => onUnarchive?.(task)}>
                        {unarchiveLabel ?? t('pages.kanban.btn_unarchive')}
                      </Button>
                    </div>
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}