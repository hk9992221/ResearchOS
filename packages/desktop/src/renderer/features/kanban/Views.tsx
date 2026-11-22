import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CalendarDays, ChevronLeft, ChevronRight, Plus } from 'lucide-react'

import type { KanbanTask, TaskPriority, TaskSummary } from '@researchos/shared'

import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { iconFor } from '../../app/icons'
import { kanbanApi } from './api'
import { isOverdue } from './board-model'
import { kanbanKeys, type BoardFilters } from './keys'
import { TaskTable, type TableSort } from './TaskTable'
import type { ToastInput } from './useFeatureToasts'
import { isoWeekNow, isoWeekRange, shiftIsoWeek } from './week'
import styles from './kanban.module.css'

const DEFAULT_SORT: TableSort = { key: 'updated', direction: 'desc' }

interface ListViewProps {
  readonly projectId: string
  readonly filters: BoardFilters
  readonly onOpenTask: (task: TaskSummary) => void
}

export function ListView({ projectId, filters, onOpenTask }: ListViewProps): JSX.Element {
  const t = useT()
  const [sort, setSort] = useState<TableSort>(DEFAULT_SORT)
  const listQuery = {
    priority: filters.priority === undefined ? undefined : ([...filters.priority] as TaskPriority[]),
    labels: filters.labels === undefined ? undefined : [...filters.labels],
    blocked: filters.blocked,
    q: filters.q,
    onlyTopLevel: false,
    includeArchived: false
  }
  const query = useQuery({
    queryKey: kanbanKeys.list(projectId, listQuery),
    queryFn: () => kanbanApi.list(projectId, { ...listQuery, sort: 'position_asc' })
  })

  if (query.isPending) return <Skeleton rows={6} />
  if (query.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.banner_problem')}
        message={query.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void query.refetch()}
      />
    )
  }

  return (
    <TaskTable
      label={t('pages.kanban.tab_list')}
      tasks={query.data.items}
      sort={sort}
      onSortChange={setSort}
      onOpen={onOpenTask}
      empty={
        <EmptyState
          icon={iconFor('kanban')}
          title={t('pages.kanban.list_empty_title')}
          description={t('pages.kanban.list_empty_reason')}
        />
      }
    />
  )
}

interface BlockedViewProps {
  readonly projectId: string
  readonly onOpenTask: (task: TaskSummary) => void
}

export function BlockedView({ projectId, onOpenTask }: BlockedViewProps): JSX.Element {
  const t = useT()
  const [sort, setSort] = useState<TableSort>({ key: 'updated', direction: 'desc' })
  const query = useQuery({
    queryKey: kanbanKeys.blocked(projectId),
    queryFn: () => kanbanApi.blocked(projectId)
  })

  if (query.isPending) return <Skeleton rows={6} />
  if (query.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.banner_problem')}
        message={query.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void query.refetch()}
      />
    )
  }

  return (
    <TaskTable
      label={t('pages.kanban.tab_blocked')}
      tasks={query.data.items}
      sort={sort}
      onSortChange={setSort}
      onOpen={onOpenTask}
      empty={
        <EmptyState
          icon={iconFor('success')}
          title={t('pages.kanban.blocked_empty_title')}
          description={t('pages.kanban.blocked_empty_reason')}
        />
      }
    />
  )
}

interface ArchiveViewProps {
  readonly projectId: string
  readonly onOpenTask: (task: TaskSummary) => void
  readonly notify: (toast: ToastInput) => void
}

export function ArchiveView({ projectId, onOpenTask, notify }: ArchiveViewProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [sort, setSort] = useState<TableSort>({ key: 'updated', direction: 'desc' })
  const query = useQuery({
    queryKey: kanbanKeys.archive(projectId, {}),
    queryFn: () => kanbanApi.archive(projectId)
  })

  const unarchive = useMutation<KanbanTask, Error, string>({
    mutationFn: (id) => kanbanApi.unarchiveTask(projectId, id),
    onSuccess: () => notify({ tone: 'success', title: t('pages.kanban.toast_unarchived') }),
    onError: (error) =>
      notify({ tone: 'error', title: t('pages.kanban.toast_unarchive_fail'), message: error.message }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  if (query.isPending) return <Skeleton rows={6} />
  if (query.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.banner_problem')}
        message={query.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void query.refetch()}
      />
    )
  }

  return (
    <TaskTable
      label={t('pages.kanban.archive_empty_title')}
      tasks={query.data.items}
      sort={sort}
      onSortChange={setSort}
      onOpen={onOpenTask}
      archive
      onUnarchive={(task) => unarchive.mutate(task.id)}
      empty={
        <EmptyState
          icon={iconFor('archive')}
          title={t('pages.kanban.archive_empty_title')}
          description={t('pages.kanban.archive_empty_reason')}
        />
      }
    />
  )
}

interface WeekViewProps {
  readonly projectId: string
  readonly archived: boolean
  readonly onOpenTask: (task: TaskSummary) => void
  readonly notify: (toast: ToastInput) => void
}

export function WeekView({ projectId, archived, onOpenTask, notify }: WeekViewProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [week, setWeek] = useState(isoWeekNow())

  const weekQuery = useQuery({
    queryKey: kanbanKeys.week(projectId, week),
    queryFn: () => kanbanApi.week(projectId, week)
  })
  const columnsQuery = useQuery({
    queryKey: kanbanKeys.columns(projectId),
    queryFn: () => kanbanApi.columns(projectId)
  })
  const candidatesQuery = useQuery({
    queryKey: kanbanKeys.weekCandidates(projectId),
    queryFn: () => kanbanApi.list(projectId, { onlyTopLevel: true, includeArchived: false })
  })

  const columns = useMemo(
    () => new Map((columnsQuery.data ?? []).map((column) => [column.name, column])),
    [columnsQuery.data]
  )
  const candidates = useMemo(() => {
    const items = candidatesQuery.data?.items ?? []
    return items.filter((task) => {
      if (task.weekPlan !== null || task.completedAt !== null) return false
      const column = columns.get(task.column)
      return column === undefined || column.kind === 'backlog' || column.kind === 'todo'
    })
  }, [candidatesQuery.data, columns])

  const plan = useMutation<KanbanTask, Error, { task: TaskSummary; week: string | null }>({
    mutationFn: ({ task, week: target }) =>
      kanbanApi.updateTask(
        projectId,
        task.id,
        { weekPlan: target },
        task.updatedAt
      ),
    onError: (error) =>
      notify({ tone: 'error', title: t('pages.kanban.toast_week_fail'), message: error.message }),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  if (weekQuery.isPending || columnsQuery.isPending) return <Skeleton rows={6} />
  if (weekQuery.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.banner_problem')}
        message={weekQuery.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void weekQuery.refetch()}
      />
    )
  }

  const planned = weekQuery.data.items

  return (
    <div className={styles.stack}>
      <div className={styles.weekNav}>
        <IconButton
          icon={ChevronLeft}
          label={t('pages.kanban.week_prev')}
          size="sm"
          onClick={() => setWeek((current) => shiftIsoWeek(current, -1))}
        />
        <span className={styles.weekLabel}>
          {week} · {isoWeekRange(week)}
        </span>
        <IconButton
          icon={ChevronRight}
          label={t('pages.kanban.week_next')}
          size="sm"
          onClick={() => setWeek((current) => shiftIsoWeek(current, 1))}
        />
        <Button size="sm" onClick={() => setWeek(isoWeekNow())}>
          {t('pages.kanban.week_today')}
        </Button>
        <span className={styles.weekCount}>{t('pages.kanban.week_count', { n: planned.length })}</span>
      </div>
      <div className={styles.weekGrid}>
        <Card title={t('pages.kanban.tab_my_week')}>
          {planned.length === 0 ? (
            <EmptyState
              compact
              icon={CalendarDays}
              title={t('pages.kanban.week_empty_title')}
              description={t('pages.kanban.week_empty_reason')}
            />
          ) : (
            <div className={styles.tableWrap}>
              <table className={styles.table} aria-label={t('pages.kanban.tab_my_week')}>
                <thead>
                  <tr>
                    <th scope="col">{t('pages.kanban.col_title')}</th>
                    <th scope="col">{t('pages.kanban.col_column')}</th>
                    <th scope="col">{t('pages.kanban.col_deadline')}</th>
                    <th scope="col">{t('pages.kanban.col_actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {planned.map((task) => (
                    <tr key={task.id}>
                      <td>
                        <button
                          type="button"
                          className={styles.titleButton}
                          onClick={() => onOpenTask(task)}
                        >
                          {task.title}
                        </button>
                      </td>
                      <td>{task.column}</td>
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
                      <td>
                        <div className={styles.listRowActions}>
                          <Button
                            size="sm"
                            disabled={archived}
                            onClick={() => plan.mutate({ task, week: null })}
                          >
                            {t('pages.kanban.week_remove')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
        <Card title={t('pages.kanban.week_backlog_title')}>
          {candidates.length === 0 ? (
            <EmptyState compact icon={iconFor('inbox')} title={t('pages.kanban.col_empty')} />
          ) : (
            <div className={styles.stack}>
              {candidates.map((task) => (
                <div key={task.id} className={styles.candidate}>
                  <button
                    type="button"
                    className={styles.titleButton}
                    onClick={() => onOpenTask(task)}
                  >
                    {task.title}
                  </button>
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={Plus}
                    disabled={archived}
                    onClick={() => plan.mutate({ task, week })}
                  >
                    {t('pages.kanban.week_add')}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}