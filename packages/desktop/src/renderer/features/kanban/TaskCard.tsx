import { useId, type DragEvent, type ReactNode } from 'react'
import { Star, TriangleAlert } from 'lucide-react'

import type { TaskSummary } from '@researchos/shared'

import { IconButton } from '../../design/components'
import { useT } from '../../i18n'
import { iconFor } from '../../app/icons'
import { isOverdue } from './board-model'
import styles from './kanban.module.css'

export interface TaskCardProps {
  readonly task: TaskSummary
  readonly draggable: boolean
  readonly dragging: boolean
  readonly dropActive: boolean
  readonly menuOpen: boolean
  readonly menu?: ReactNode
  readonly onOpen: (task: TaskSummary) => void
  readonly onMenu: (task: TaskSummary) => void
  readonly onDragStart: (event: DragEvent<HTMLElement>, task: TaskSummary) => void
  readonly onDragEnd: () => void
  readonly onDragOver: (event: DragEvent<HTMLElement>, task: TaskSummary) => void
  readonly onDrop: (event: DragEvent<HTMLElement>, task: TaskSummary) => void
}

const PRIORITY_KEY: Readonly<Record<string, string>> = {
  high: 'pages.kanban.priority_high',
  medium: 'pages.kanban.priority_medium',
  low: 'pages.kanban.priority_low',
  none: 'pages.kanban.priority_none'
}

/** Bar colour for a set priority; `none` paints nothing. */
const PRIORITY_BAR: Readonly<Record<string, string | undefined>> = {
  high: styles.priorityHigh,
  medium: styles.priorityMedium,
  low: styles.priorityLow
}

/**
 * TaskCard is the board card (§3.11): the priority bar is painted only when a
 * priority is actually set (an accent bar on every unset card read as noise),
 * the title, labels and the counters that change a decision follow. Drag
 * payloads are typed; the context menu is the non-gesture path.
 */
export function TaskCard({
  task,
  draggable,
  dragging,
  dropActive,
  menuOpen,
  menu,
  onOpen,
  onMenu,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDrop
}: TaskCardProps): JSX.Element {
  const t = useT()
  const menuId = useId()
  const priorityKey = PRIORITY_KEY[task.priority] ?? 'pages.kanban.priority_none'
  const priorityLabel = t(priorityKey)

  return (
    <article
      className={[styles.card, dragging ? styles.cardDragging : '', dropActive ? styles.cardDropActive : '']
        .filter(Boolean)
        .join(' ')}
      data-task-id={task.id}
      draggable={draggable}
      onDragStart={(event) => onDragStart(event, task)}
      onDragEnd={onDragEnd}
      onDragOver={(event) => onDragOver(event, task)}
      onDrop={(event) => onDrop(event, task)}
      onContextMenu={(event) => {
        event.preventDefault()
        event.stopPropagation()
        onMenu(task)
      }}
      onClick={() => onOpen(task)}
    >
      {task.priority === 'none' ? null : (
        <span
          role="img"
          aria-label={priorityLabel}
          title={priorityLabel}
          className={[styles.priorityBar, PRIORITY_BAR[task.priority] ?? ''].join(' ')}
        />
      )}
      <button
        type="button"
        className={styles.cardTitle}
        onClick={(event) => {
          event.stopPropagation()
          onOpen(task)
        }}
      >
        <span className={styles.cardTitleText}>{task.title}</span>
      </button>
      {draggable || menu !== undefined ? (
        <span className={styles.cardMenuButton} onClick={(event) => event.stopPropagation()}>
          <IconButton
            icon={iconFor('more')}
            label={t('pages.kanban.card_menu')}
            size="sm"
            variant="ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? menuId : undefined}
            onClick={(event) => {
              event.stopPropagation()
              onMenu(task)
            }}
          />
        </span>
      ) : null}
      {menuOpen && menu !== undefined && (
        <div id={menuId} role="menu" className={styles.cardMenu} onClick={(event) => event.stopPropagation()}>
          {menu}
        </div>
      )}
      <div className={styles.cardMeta}>
        {task.blocked && (
          <span className={styles.blockedBadge}>
            <TriangleAlert aria-hidden="true" size={14} />
            {t('pages.kanban.blocked_yes')}
          </span>
        )}
        {task.deadline !== null && (
          <span className={[styles.due, isOverdue(task.deadline) ? styles.dueOverdue : ''].join(' ')}>
            <span aria-hidden="true">{t('pages.kanban.col_deadline')}</span>
            {task.deadline}
          </span>
        )}
        {task.weekPlan !== null && (
          <span className={styles.weekStar} title={task.weekPlan}>
            <Star aria-hidden="true" size={14} />
            <span aria-label={task.weekPlan}>{task.weekPlan}</span>
          </span>
        )}
        {task.checklistTotal > 0 && (
          <span>
            {t('pages.kanban.card_checklist', {
              done: task.checklistDone,
              total: task.checklistTotal
            })}
          </span>
        )}
        {task.subtaskTotal > 0 && (
          <span>
            {t('pages.kanban.card_subtasks', {
              done: task.subtaskDone,
              total: task.subtaskTotal
            })}
          </span>
        )}
      </div>
      {task.labels.length > 0 && (
        <div className={styles.labels}>
          {task.labels.map((label) => (
            <span key={label} className={styles.label}>
              {label}
            </span>
          ))}
        </div>
      )}
    </article>
  )
}