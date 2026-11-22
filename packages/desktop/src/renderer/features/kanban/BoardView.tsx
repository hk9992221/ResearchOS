import { useEffect, useRef, useState, type DragEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Plus } from 'lucide-react'

import type { BoardView, KanbanTask, TaskColumnInfo, TaskSummary } from '@researchos/shared'

import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  IconButton,
  Skeleton,
  TextInput
} from '../../design/components'
import { useT } from '../../i18n'
import { iconFor } from '../../app/icons'
import { kanbanApi } from './api'
import {
  applyOptimisticColumnMove,
  applyOptimisticTaskMove,
  canDrop,
  type TaskDragPayload
} from './board-model'
import { ColumnDeleteDialog, ColumnRenameDialog } from './ColumnDialogs'
import { kanbanKeys, type BoardFilters } from './keys'
import { TaskCard } from './TaskCard'
import { type ToastInput } from './useFeatureToasts'
import styles from './kanban.module.css'

export interface KanbanBoardProps {
  readonly projectId: string
  readonly archived: boolean
  readonly filters: BoardFilters
  readonly onOpenTask: (task: TaskSummary) => void
  readonly notify: (toast: ToastInput) => void
}

interface MoveVariables {
  readonly id: string
  readonly column: string
  readonly beforeId?: string
  readonly afterId?: string
}

interface MoveContext {
  readonly previous?: BoardView
}

function stepAnchor(tasks: readonly TaskSummary[], id: string, delta: number): { beforeId?: string } | null {
  const index = tasks.findIndex((task) => task.id === id)
  if (index < 0) return null
  const target = index + delta
  if (target < 0 || target >= tasks.length) return null
  if (delta < 0) {
    const previous = tasks[target]
    return previous === undefined ? null : { beforeId: previous.id }
  }
  const after = tasks[index + 2]
  return after === undefined ? {} : { beforeId: after.id }
}

/**
 * Board: columns from `column.list`, cards from `views.board`, typed HTML5
 * drag payloads and the context-menu fallback. A drop updates the query cache
 * first (optimistic), then writes; the write settling invalidates the module.
 */
export function BoardView({
  projectId,
  archived,
  filters,
  onOpenTask,
  notify
}: KanbanBoardProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const boardKey = kanbanKeys.board(projectId, filters)
  const boardQuery = useQuery({
    queryKey: boardKey,
    queryFn: () => kanbanApi.board(projectId, filters)
  })

  const dragPayload = useRef<TaskDragPayload | null>(null)
  const [dragging, setDragging] = useState<TaskDragPayload | null>(null)
  const [dropTarget, setDropTarget] = useState<string | null>(null)
  const [menuTaskId, setMenuTaskId] = useState<string | null>(null)
  const [movePanel, setMovePanel] = useState(false)
  const [menuColumn, setMenuColumn] = useState<string | null>(null)
  const [renameColumn, setRenameColumn] = useState<TaskColumnInfo | null>(null)
  const [deleteColumn, setDeleteColumn] = useState<TaskColumnInfo | null>(null)
  const [columnBusy, setColumnBusy] = useState(false)

  useEffect(() => {
    const close = (event: KeyboardEvent): void => {
      if (event.key !== 'Escape') return
      setMenuTaskId(null)
      setMenuColumn(null)
      setMovePanel(false)
    }
    document.addEventListener('keydown', close)
    return () => document.removeEventListener('keydown', close)
  }, [])

  const moveMutation = useMutation<KanbanTask, Error, MoveVariables, MoveContext>({
    mutationFn: (variables) =>
      kanbanApi.moveTask(projectId, variables.id, variables.column, {
        beforeId: variables.beforeId,
        afterId: variables.afterId
      }),
    onMutate: async (variables) => {
      await queryClient.cancelQueries({ queryKey: boardKey })
      const previous = queryClient.getQueryData<BoardView>(boardKey)
      if (previous !== undefined) {
        queryClient.setQueryData(
          boardKey,
          applyOptimisticTaskMove(previous, {
            id: variables.id,
            column: variables.column,
            beforeId: variables.beforeId
          })
        )
      }
      return { previous }
    },
    onError: (error, _variables, context) => {
      if (context?.previous !== undefined) queryClient.setQueryData(boardKey, context.previous)
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_move_fail'),
        message: error.message
      })
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  const columnMoveMutation = useMutation<
    TaskColumnInfo[],
    Error,
    { name: string; position: number },
    MoveContext
  >({
    mutationFn: ({ name, position }) => kanbanApi.moveColumn(projectId, name, position),
    onMutate: async ({ name, position }) => {
      await queryClient.cancelQueries({ queryKey: boardKey })
      const previous = queryClient.getQueryData<BoardView>(boardKey)
      if (previous !== undefined) {
        queryClient.setQueryData(boardKey, applyOptimisticColumnMove(previous, name, position))
      }
      return { previous }
    },
    onError: (error, _variables, context) => {
      if (context?.previous !== undefined) queryClient.setQueryData(boardKey, context.previous)
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_column_fail'),
        message: error.message
      })
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  const archiveMutation = useMutation<KanbanTask, Error, string>({
    mutationFn: (id) => kanbanApi.archiveTask(projectId, id),
    onSuccess: () => {
      notify({ tone: 'success', title: t('pages.kanban.toast_archived') })
    },
    onError: (error) => {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_archive_fail'),
        message: error.message
      })
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  const columnDoneMutation = useMutation({
    mutationFn: ({ name, isDone }: { name: string; isDone: boolean }) =>
      kanbanApi.setColumnDone(projectId, name, isDone),
    onError: (error: Error) => {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_column_fail'),
        message: error.message
      })
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    }
  })

  function beginDrag(event: DragEvent<HTMLElement>, payload: TaskDragPayload): void {
    if (archived) {
      event.preventDefault()
      return
    }
    dragPayload.current = payload
    setDragging(payload)
    event.dataTransfer?.setData('text/plain', JSON.stringify(payload))
    if (event.dataTransfer != null) event.dataTransfer.effectAllowed = 'move'
  }

  function endDrag(): void {
    dragPayload.current = null
    setDragging(null)
    setDropTarget(null)
  }

  function over(event: DragEvent<HTMLElement>, target: 'task' | 'column', key: string): void {
    const payload = dragPayload.current
    if (payload === null || !canDrop(payload, target)) return
    event.preventDefault()
    if (event.dataTransfer != null) event.dataTransfer.dropEffect = 'move'
    setDropTarget(key)
  }

  function dropOnCard(event: DragEvent<HTMLElement>, target: TaskSummary): void {
    event.preventDefault()
    event.stopPropagation()
    const payload = dragPayload.current
    endDrag()
    if (payload === null || payload.kind !== 'task' || archived) return
    if (payload.id === target.id) return
    moveMutation.mutate({ id: payload.id, column: target.column, beforeId: target.id })
  }

  function dropOnColumn(event: DragEvent<HTMLElement>, column: TaskColumnInfo): void {
    event.preventDefault()
    const payload = dragPayload.current
    endDrag()
    if (payload === null || archived) return
    if (payload.kind === 'column') {
      if (payload.name === column.name) return
      const board = boardQuery.data
      if (board === undefined) return
      const position = board.columns.findIndex((candidate) => candidate.column.name === column.name)
      if (position >= 0) columnMoveMutation.mutate({ name: payload.name, position })
      return
    }
    moveMutation.mutate({ id: payload.id, column: column.name })
  }

  if (boardQuery.isPending) {
    return <Skeleton rows={6} />
  }
  if (boardQuery.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.banner_problem')}
        message={boardQuery.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void boardQuery.refetch()}
      />
    )
  }

  const board = boardQuery.data
  if (board.columns.length === 0) {
    return (
      <EmptyState
        icon={iconFor('kanban')}
        title={t('pages.kanban.no_columns_title')}
        description={t('pages.kanban.no_columns_reason')}
      />
    )
  }

  const submitRename = async (newName: string): Promise<void> => {
    if (renameColumn === null) return
    setColumnBusy(true)
    try {
      await kanbanApi.renameColumn(projectId, renameColumn.name, newName)
      setRenameColumn(null)
      await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_column_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    } finally {
      setColumnBusy(false)
    }
  }

  const submitDelete = async (migrateTo: string): Promise<void> => {
    if (deleteColumn === null) return
    setColumnBusy(true)
    try {
      await kanbanApi.deleteColumn(projectId, deleteColumn.name, migrateTo)
      setDeleteColumn(null)
      notify({ tone: 'success', title: t('pages.kanban.toast_column_deleted') })
      await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_column_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    } finally {
      setColumnBusy(false)
    }
  }

  const taskMenu = (task: TaskSummary, column: TaskColumnInfo): JSX.Element => {
    const columnBoard = board.columns.find((candidate) => candidate.column.name === column.name)
    const tasks = columnBoard?.tasks ?? []
    const up = stepAnchor(tasks, task.id, -1)
    const down = stepAnchor(tasks, task.id, 1)
    return (
      <>
        <button
          type="button"
          role="menuitem"
          className={styles.menuItem}
          onClick={() => {
            setMenuTaskId(null)
            onOpenTask(task)
          }}
        >
          {t('pages.kanban.menu_open')}
        </button>
        <button
          type="button"
          role="menuitem"
          className={styles.menuItem}
          aria-expanded={movePanel}
          onClick={() => setMovePanel((open) => !open)}
        >
          {t('pages.kanban.menu_move_to')}
        </button>
        {movePanel &&
          board.columns
            .filter((candidate) => candidate.column.name !== task.column)
            .map((candidate) => (
              <button
                key={candidate.column.name}
                type="button"
                role="menuitem"
                className={styles.menuItem}
                onClick={() => {
                  setMenuTaskId(null)
                  setMovePanel(false)
                  moveMutation.mutate({ id: task.id, column: candidate.column.name })
                }}
              >
                {candidate.column.name}
              </button>
            ))}
        <button
          type="button"
          role="menuitem"
          className={styles.menuItem}
          disabled={archived || up === null}
          onClick={() => {
            setMenuTaskId(null)
            if (up !== null) moveMutation.mutate({ id: task.id, column: task.column, ...up })
          }}
        >
          {t('pages.kanban.menu_move_up')}
        </button>
        <button
          type="button"
          role="menuitem"
          className={styles.menuItem}
          disabled={archived || down === null}
          onClick={() => {
            setMenuTaskId(null)
            if (down !== null) moveMutation.mutate({ id: task.id, column: task.column, ...down })
          }}
        >
          {t('pages.kanban.menu_move_down')}
        </button>
        <div className={styles.menuSeparator} role="separator" />
        <button
          type="button"
          role="menuitem"
          className={styles.menuItem}
          disabled={archived}
          onClick={() => {
            setMenuTaskId(null)
            archiveMutation.mutate(task.id)
          }}
        >
          {t('pages.kanban.menu_archive')}
        </button>
      </>
    )
  }

  return (
    <>
      <div className={styles.board} aria-label={t('pages.kanban.title')}>
        {board.columns.map(({ column, tasks }, index) => {
          const overColumn = dropTarget === column.name
          const menuOpen = menuColumn === column.name
          return (
            <section
              key={column.name}
              aria-label={column.name}
              data-column={column.name}
              className={[styles.column, overColumn ? styles.columnDropActive : ''].join(' ')}
              onDragOver={(event) => over(event, 'column', column.name)}
              onDrop={(event) => dropOnColumn(event, column)}
              onDragLeave={(event) => {
                if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
                setDropTarget(null)
              }}
              onContextMenu={(event) => {
                if (archived) return
                event.preventDefault()
                setMenuColumn(column.name)
              }}
            >
              <header
                className={styles.columnHeader}
                draggable={!archived}
                onDragStart={(event) => beginDrag(event, { kind: 'column', name: column.name })}
                onDragEnd={endDrag}
              >
                <span className={styles.columnTitle} title={column.name}>
                  {column.name}
                  {column.isDone && (
                    <Check aria-label={t('pages.kanban.field_column_done')} size={14} className={styles.doneMark} />
                  )}
                </span>
                <Badge>{tasks.length}</Badge>
                <span className={styles.spacer} />
                {!archived && (
                  <span className={styles.menuHeader} onClick={(event) => event.stopPropagation()}>
                    <IconButton
                      icon={iconFor('more')}
                      label={t('pages.kanban.col_menu')}
                      size="sm"
                      aria-haspopup="menu"
                      aria-expanded={menuOpen}
                      onClick={() => {
                        setMenuTaskId(null)
                        setMenuColumn(menuOpen ? null : column.name)
                      }}
                    />
                    {menuOpen && (
                      <div role="menu" className={styles.columnMenu}>
                        <button
                          type="button"
                          role="menuitem"
                          className={styles.menuItem}
                          onClick={() => {
                            setMenuColumn(null)
                            setRenameColumn(column)
                          }}
                        >
                          {t('pages.kanban.col_menu_rename')}
                        </button>
                        <button
                          type="button"
                          role="menuitem"
                          className={styles.menuItem}
                          onClick={() => {
                            setMenuColumn(null)
                            columnDoneMutation.mutate({
                              name: column.name,
                              isDone: !column.isDone
                            })
                          }}
                        >
                          {column.isDone
                            ? t('pages.kanban.col_menu_unset_done')
                            : t('pages.kanban.col_menu_set_done')}
                        </button>
                        <button
                          type="button"
                          role="menuitem"
                          className={styles.menuItem}
                          disabled={index === 0}
                          onClick={() => {
                            setMenuColumn(null)
                            columnMoveMutation.mutate({ name: column.name, position: index - 1 })
                          }}
                        >
                          {t('pages.kanban.col_menu_move_left')}
                        </button>
                        <button
                          type="button"
                          role="menuitem"
                          className={styles.menuItem}
                          disabled={index === board.columns.length - 1}
                          onClick={() => {
                            setMenuColumn(null)
                            columnMoveMutation.mutate({ name: column.name, position: index + 1 })
                          }}
                        >
                          {t('pages.kanban.col_menu_move_right')}
                        </button>
                        <div className={styles.menuSeparator} role="separator" />
                        <button
                          type="button"
                          role="menuitem"
                          className={styles.menuItem}
                          disabled={board.columns.length <= 1}
                          onClick={() => {
                            setMenuColumn(null)
                            setDeleteColumn(column)
                          }}
                        >
                          {t('pages.kanban.col_menu_delete')}
                        </button>
                      </div>
                    )}
                  </span>
                )}
              </header>
              <div className={styles.columnBody}>
                {tasks.length === 0 && (
                  <p className={styles.columnEmpty}>{t('pages.kanban.col_empty')}</p>
                )}
                {tasks.map((task) => (
                  <TaskCard
                    key={task.id}
                    task={task}
                    draggable={!archived}
                    dragging={dragging?.kind === 'task' && dragging.id === task.id}
                    dropActive={dropTarget === task.id}
                    menuOpen={menuTaskId === task.id}
                    menu={menuTaskId === task.id ? taskMenu(task, column) : undefined}
                    onOpen={onOpenTask}
                    onMenu={(target) => {
                      setMenuColumn(null)
                      setMovePanel(false)
                      setMenuTaskId((current) => (current === target.id ? null : target.id))
                    }}
                    onDragStart={(event, target) => {
                      beginDrag(event, { kind: 'task', id: target.id, column: target.column })
                      event.stopPropagation()
                    }}
                    onDragEnd={endDrag}
                    onDragOver={(event, target) => {
                      event.stopPropagation()
                      over(event, 'task', target.id)
                    }}
                    onDrop={(event, target) => dropOnCard(event, target)}
                  />
                ))}
                <ColumnQuickAdd projectId={projectId} column={column.name} disabled={archived} />
              </div>
            </section>
          )
        })}
      </div>
      <ColumnRenameDialog
        open={renameColumn !== null}
        column={renameColumn}
        columns={board.columns.map((candidate) => candidate.column)}
        busy={columnBusy}
        onClose={() => setRenameColumn(null)}
        onSubmit={(newName) => void submitRename(newName)}
      />
      <ColumnDeleteDialog
        open={deleteColumn !== null}
        column={deleteColumn}
        columns={board.columns.map((candidate) => candidate.column)}
        taskCount={
          board.columns.find((candidate) => candidate.column.name === deleteColumn?.name)?.tasks
            .length ?? 0
        }
        busy={columnBusy}
        onClose={() => setDeleteColumn(null)}
        onSubmit={(migrateTo) => void submitDelete(migrateTo)}
      />
    </>
  )
}

interface ColumnQuickAddProps {
  readonly projectId: string
  readonly column: string
  readonly disabled: boolean
}

function ColumnQuickAdd({ projectId, column, disabled }: ColumnQuickAddProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (): Promise<void> => {
    const value = title.trim()
    if (value === '') {
      setError(t('pages.kanban.title_required'))
      return
    }
    setBusy(true)
    setError(null)
    try {
      await kanbanApi.createTask({ projectId, title: value, column })
      setTitle('')
      setOpen(false)
      await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  if (disabled) return <></>

  if (!open) {
    return (
      <Button
        variant="ghost"
        size="sm"
        icon={Plus}
        onClick={() => setOpen(true)}
        aria-label={t('pages.kanban.btn_add_card')}
      >
        {t('pages.kanban.btn_add_card')}
      </Button>
    )
  }

  return (
    <form
      className={styles.quickAdd}
      onSubmit={(event) => {
        event.preventDefault()
        void submit()
      }}
    >
      <TextInput
        label={t('pages.kanban.quick_label')}
        hideLabel
        placeholder={t('pages.kanban.quick_hint')}
        value={title}
        disabled={busy}
        autoFocus
        onChange={(value) => {
          setTitle(value)
          if (error !== null) setError(null)
        }}
      />
      {error !== null && <p className={styles.errorLine}>{error}</p>}
    </form>
  )
}