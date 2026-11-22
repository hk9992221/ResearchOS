import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Search } from 'lucide-react'

import type { TaskPriority } from '@researchos/shared'

import {
  Banner,
  Button,
  EmptyState,
  Modal,
  Page,
  PageHeader,
  SelectField,
  Skeleton,
  SwitchField,
  Tabs,
  TextInput
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { iconFor } from '../../app/icons'
import { Toasts } from '../../app/Toasts'
import { routeHref } from '../../routes'
import { useShellStore } from '../../state/shell'
import { kanbanApi } from './api'
import { BoardView } from './BoardView'
import { kanbanKeys, type BoardFilters } from './keys'
import { QuickCapture } from './QuickCapture'
import { TaskDetailBody } from './TaskDetail'
import { useFeatureToasts } from './useFeatureToasts'
import { ArchiveView, BlockedView, ListView, WeekView } from './Views'
import styles from './kanban.module.css'

type KanbanView = 'board' | 'list' | 'blocked' | 'week'

export interface KanbanPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

/**
 * Kanban route: header, view tabs, filter panel, archive switch and the four
 * views. The task detail opens in a feature dock beside the board, and the
 * full `task/:id` page is the second, linkable surface.
 */
export default function KanbanPage({ projectId }: KanbanPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  const { projectId: currentProjectId, archived, isLoading: projectLoading, error: projectError } =
    useCurrentProject()
  const resolvedProject = projectId ?? currentProjectId
  const { toasts, notify, dismiss, undoable } = useFeatureToasts()

  const [view, setView] = useState<KanbanView>('board')
  const [archive, setArchive] = useState(false)
  const [filterOpen, setFilterOpen] = useState(false)
  const [text, setText] = useState('')
  const [labelsText, setLabelsText] = useState('')
  const [blockedFilter, setBlockedFilter] = useState<'any' | 'only' | 'none'>('any')
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [createTitle, setCreateTitle] = useState('')
  const [createColumn, setCreateColumn] = useState('')
  const [createPriority, setCreatePriority] = useState<TaskPriority>('none')
  const [createError, setCreateError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const routeView = searchParams.get('view')
  useEffect(() => {
    if (routeView !== 'blocked') return
    setView('blocked')
    const next = new URLSearchParams(searchParams)
    next.delete('view')
    setSearchParams(next, { replace: true })
  }, [routeView, searchParams, setSearchParams])

  const filters: BoardFilters = useMemo(() => {
    const labels = labelsText
      .split(',')
      .map((label) => label.trim())
      .filter((label) => label !== '')
    return {
      q: text.trim() === '' ? undefined : text.trim(),
      labels: labels.length === 0 ? undefined : labels,
      blocked: blockedFilter === 'any' ? undefined : blockedFilter === 'only'
    }
  }, [text, labelsText, blockedFilter])

  const columnsQuery = useQuery({
    queryKey: kanbanKeys.columns(resolvedProject ?? 'none'),
    queryFn: () => kanbanApi.columns(resolvedProject as string),
    enabled: resolvedProject !== null
  })

  const invalidateAll = async (): Promise<void> => {
    if (resolvedProject === null) return
    await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(resolvedProject) })
  }

  if (resolvedProject === null) {
    return (
      <EmptyState
        icon={iconFor('kanban')}
        title={t('pages.kanban.needs_project_title')}
        description={t('pages.kanban.needs_project_reason')}
        action={
          <Button
            variant="primary"
            onClick={() => {
              const href = routeHref('projects')
              if (href !== undefined) navigate(href)
            }}
          >
            {t('pages.kanban.btn_projects')}
          </Button>
        }
      />
    )
  }

  if (projectLoading) return <Skeleton rows={6} />
  if (projectError !== null) {
    return (
      <EmptyState
        icon={iconFor('kanban')}
        title={t('pages.kanban.banner_problem')}
        description={projectError.message}
      />
    )
  }

  const openTask = (id: string): void => {
    setSelectedTaskId(id)
  }

  const closeTask = (): void => {
    setSelectedTaskId(null)
  }

  const deleteTask = async (id: string): Promise<void> => {
    closeTask()
    undoable(t('pages.kanban.toast_deleted'), async () => {
      try {
        await kanbanApi.restoreTask(resolvedProject, id)
        notify({ tone: 'success', title: t('common.undone') })
        await invalidateAll()
      } catch (error) {
        notify({
          tone: 'error',
          title: t('common.undo_failed'),
          message: error instanceof Error ? error.message : String(error)
        })
      }
    })
  }

  const submitCreate = async (): Promise<void> => {
    const value = createTitle.trim()
    if (value === '') {
      setCreateError(t('pages.kanban.title_required'))
      return
    }
    setCreating(true)
    setCreateError(null)
    try {
      await kanbanApi.createTask({
        projectId: resolvedProject,
        title: value,
        column: createColumn === '' ? undefined : createColumn,
        priority: createPriority
      })
      setCreateOpen(false)
      setCreateTitle('')
      setCreateColumn('')
      setCreatePriority('none')
      notify({ tone: 'success', title: t('pages.kanban.toast_created') })
      await invalidateAll()
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreating(false)
    }
  }

  const columns = columnsQuery.data ?? []

  return (
    <Page>
      <PageHeader
        title={t('pages.kanban.title')}
        actions={
          <Button
            variant="primary"
            icon={Plus}
            disabled={archived}
            onClick={() => {
              setCreateColumn('')
              setCreateOpen(true)
            }}
          >
            {t('pages.kanban.btn_new_task')}
          </Button>
        }
      />
      {archived && (
        <Banner tone="warning" title={t('pages.kanban.archived_title')}>
          {t('pages.kanban.archived_message')}
        </Banner>
      )}
      <div className={styles.toolbar}>
        <Tabs
          label={t('pages.kanban.tabs_label')}
          value={view}
          onChange={(value) => {
            if (value === 'board' || value === 'list' || value === 'blocked' || value === 'week') {
              setView(value)
            }
          }}
          items={[
            { id: 'board', label: t('pages.kanban.tab_board') },
            { id: 'list', label: t('pages.kanban.tab_list') },
            { id: 'blocked', label: t('pages.kanban.tab_blocked') },
            { id: 'week', label: t('pages.kanban.tab_my_week') }
          ]}
        />
        <span className={styles.spacer} />
        <Button
          variant={filterOpen ? 'primary' : 'secondary'}
          size="sm"
          icon={Search}
          onClick={() => setFilterOpen((open) => !open)}
          aria-expanded={filterOpen}
        >
          {t('pages.kanban.btn_filter')}
        </Button>
        <span className={styles.switch}>
          <SwitchField
            label={t('pages.kanban.archive_switch')}
            checked={archive}
            onChange={setArchive}
          />
        </span>
      </div>
      {filterOpen && !archive && (view === 'board' || view === 'list') && (
        <div className={styles.filterPanel} role="group" aria-label={t('pages.kanban.filter_title')}>
          <div className={styles.filterField}>
            <TextInput
              label={t('pages.kanban.filter_text')}
              placeholder={t('pages.kanban.filter_text_hint')}
              value={text}
              onChange={setText}
            />
          </div>
          <div className={styles.filterField}>
            <TextInput
              label={t('pages.kanban.filter_labels')}
              placeholder={t('pages.kanban.filter_labels_hint')}
              value={labelsText}
              onChange={setLabelsText}
            />
          </div>
          <SelectField
            label={t('pages.kanban.filter_blocked')}
            value={blockedFilter}
            options={[
              { value: 'any', label: t('pages.kanban.filter_blocked_any') },
              { value: 'only', label: t('pages.kanban.filter_blocked_only') },
              { value: 'none', label: t('pages.kanban.filter_blocked_none') }
            ]}
            onChange={(value) => {
              if (value === 'any' || value === 'only' || value === 'none') setBlockedFilter(value)
            }}
          />
          <Button
            size="sm"
            onClick={() => {
              setText('')
              setLabelsText('')
              setBlockedFilter('any')
            }}
          >
            {t('pages.kanban.filter_clear')}
          </Button>
        </div>
      )}

      {archive ? (
        <ArchiveView
          projectId={resolvedProject}
          onOpenTask={(task) => openTask(task.id)}
          notify={notify}
        />
      ) : (
        <div className={styles.contentRow}>
          <div className={styles.workspaceMain}>
            {view === 'board' && (
              <>
                <QuickCapture projectId={resolvedProject} disabled={archived} />
                <BoardView
                  projectId={resolvedProject}
                  archived={archived}
                  filters={filters}
                  onOpenTask={(task) => openTask(task.id)}
                  notify={notify}
                />
              </>
            )}
            {view === 'list' && (
              <ListView
                projectId={resolvedProject}
                filters={filters}
                onOpenTask={(task) => openTask(task.id)}
              />
            )}
            {view === 'blocked' && (
              <BlockedView
                projectId={resolvedProject}
                onOpenTask={(task) => openTask(task.id)}
              />
            )}
            {view === 'week' && (
              <WeekView
                projectId={resolvedProject}
                archived={archived}
                onOpenTask={(task) => openTask(task.id)}
                notify={notify}
              />
            )}
          </div>
          {selectedTaskId !== null && (
            <aside className={styles.detailDock} aria-label={t('pages.kanban.detail_title')}>
              <div className={styles.dockBody}>
                <TaskDetailBody
                  projectId={resolvedProject}
                  taskId={selectedTaskId}
                  projectArchived={archived}
                  variant="panel"
                  onClose={closeTask}
                  onOpenTask={openTask}
                  onDeleted={(id) => void deleteTask(id)}
                  notify={notify}
                />
              </div>
            </aside>
          )}
        </div>
      )}

      <Modal
        open={createOpen}
        title={t('pages.kanban.create_title')}
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setCreateOpen(false)} disabled={creating}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" disabled={creating} onClick={() => void submitCreate()}>
              {t('pages.kanban.create_submit')}
            </Button>
          </>
        }
      >
        <form
          className={styles.stack}
          onSubmit={(event) => {
            event.preventDefault()
            void submitCreate()
          }}
        >
          <TextInput
            label={t('pages.kanban.field_title')}
            placeholder={t('pages.kanban.field_title_hint')}
            value={createTitle}
            error={createError ?? undefined}
            disabled={creating}
            onChange={(value) => {
              setCreateTitle(value)
              if (createError !== null) setCreateError(null)
            }}
          />
          <SelectField
            label={t('pages.kanban.field_column')}
            value={createColumn}
            options={[
              { value: '', label: t('pages.kanban.field_column_default') },
              ...columns.map((column) => ({ value: column.name, label: column.name }))
            ]}
            disabled={creating}
            onChange={setCreateColumn}
          />
          <SelectField
            label={t('pages.kanban.field_priority')}
            value={createPriority}
            options={[
              { value: 'none', label: t('pages.kanban.priority_none') },
              { value: 'low', label: t('pages.kanban.priority_low') },
              { value: 'medium', label: t('pages.kanban.priority_medium') },
              { value: 'high', label: t('pages.kanban.priority_high') }
            ]}
            disabled={creating}
            onChange={(value) => {
              if (value === 'none' || value === 'low' || value === 'medium' || value === 'high') {
                setCreatePriority(value)
              }
            }}
          />
        </form>
      </Modal>

      <Toasts toasts={toasts} onDismiss={dismiss} />
    </Page>
  )
}