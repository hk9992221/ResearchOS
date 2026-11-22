import { Suspense, lazy, useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Archive, ArchiveRestore, Plus, Trash2, X } from 'lucide-react'

import {
  AppError,
  AppErrorCode,
  type TaskDetail,
  type TaskPatch,
  type TaskPriority
} from '@researchos/shared'

import {
  Banner,
  Button,
  ConfirmDialog,
  ErrorState,
  IconButton,
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
import { kanbanApi } from './api'
import { ChecklistPanel } from './ChecklistPanel'
import { LinksPanel } from './LinksPanel'
import { kanbanKeys } from './keys'
import { useFeatureToasts } from './useFeatureToasts'
import { isoWeekNow, isValidWeekPlan } from './week'
import type { ToastInput } from './useFeatureToasts'
import styles from './kanban.module.css'

interface TaskForm {
  readonly title: string
  readonly descriptionMd: string
  readonly priority: TaskPriority
  readonly deadline: string
  readonly weekPlan: string
  readonly labels: readonly string[]
  readonly blocked: boolean
  readonly column: string
}

function formFromDetail(detail: TaskDetail): TaskForm {
  return {
    title: detail.title,
    descriptionMd: detail.descriptionMd,
    priority: detail.priority,
    deadline: detail.deadline ?? '',
    weekPlan: detail.weekPlan ?? '',
    labels: [...detail.labels],
    blocked: detail.blocked,
    column: detail.column
  }
}

const MarkdownPreview = lazy(() => import('./MarkdownPreview'))

const PRIORITIES: readonly { value: TaskPriority; key: string }[] = [
  { value: 'none', key: 'pages.kanban.priority_none' },
  { value: 'low', key: 'pages.kanban.priority_low' },
  { value: 'medium', key: 'pages.kanban.priority_medium' },
  { value: 'high', key: 'pages.kanban.priority_high' }
]

export interface TaskDetailBodyProps {
  readonly projectId: string
  readonly taskId: string
  readonly projectArchived: boolean
  readonly variant: 'page' | 'panel'
  readonly onClose?: () => void
  readonly onOpenTask: (taskId: string) => void
  readonly onDeleted: (taskId: string, title: string) => void
  readonly notify: (toast: ToastInput) => void
}

/**
 * Task detail: the inspector body of the Go UI (details, checklist, links) and
 * the `task/:id` page. The save carries `expectedUpdatedAt`; a CONFLICT shows
 * the two honest answers (reload the server row, or overwrite it).
 */
export function TaskDetailBody({
  projectId,
  taskId,
  projectArchived,
  variant,
  onClose,
  onOpenTask,
  onDeleted,
  notify
}: TaskDetailBodyProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState<'detail' | 'checklist' | 'links'>('detail')
  const [form, setForm] = useState<TaskForm | null>(null)
  const [dirty, setDirty] = useState(false)
  const [mode, setMode] = useState<'source' | 'preview'>('source')
  const [labelDraft, setLabelDraft] = useState('')
  const [subtaskDraft, setSubtaskDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteSubtask, setDeleteSubtask] = useState<{ id: string; title: string } | null>(null)

  const detailKey = kanbanKeys.detail(projectId, taskId)
  const detailQuery = useQuery({
    queryKey: detailKey,
    queryFn: () => kanbanApi.getTask(projectId, taskId)
  })
  const columnsQuery = useQuery({
    queryKey: kanbanKeys.columns(projectId),
    queryFn: () => kanbanApi.columns(projectId)
  })

  useEffect(() => {
    if (detailQuery.data === undefined) return
    setForm(formFromDetail(detailQuery.data))
    setDirty(false)
    setConflict(false)
  }, [detailQuery.data?.id])

  if (detailQuery.isPending) return <Skeleton rows={6} />
  if (detailQuery.isError) {
    return (
      <ErrorState
        title={t('pages.kanban.detail_load_failed')}
        message={detailQuery.error.message}
        retryLabel={t('pages.kanban.btn_retry')}
        onRetry={() => void detailQuery.refetch()}
      />
    )
  }

  const detail = detailQuery.data
  const readOnly = projectArchived || detail.archivedAt !== null
  const columns = columnsQuery.data ?? []
  const columnOptions =
    columns.length > 0
      ? columns.map((column) => ({ value: column.name, label: column.name }))
      : [{ value: detail.column, label: detail.column }]

  const update = (patch: Partial<TaskForm>): void => {
    setForm((current) => (current === null ? current : { ...current, ...patch }))
    setDirty(true)
  }

  const weekValid = form === null || isValidWeekPlan(form.weekPlan.trim())
  const titleValid = form !== null && form.title.trim() !== ''

  const invalidateAll = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: kanbanKeys.all(projectId) })
  }

  const save = async (overwrite: boolean): Promise<void> => {
    if (form === null || readOnly || saving) return
    if (!titleValid || !weekValid) return
    setSaving(true)
    const patch: TaskPatch = {
      title: form.title.trim(),
      descriptionMd: form.descriptionMd,
      priority: form.priority,
      deadline: form.deadline === '' ? null : form.deadline,
      weekPlan: form.weekPlan.trim() === '' ? null : form.weekPlan.trim(),
      labels: [...form.labels],
      blocked: form.blocked,
      column: form.column
    }
    try {
      await kanbanApi.updateTask(
        projectId,
        taskId,
        patch,
        overwrite ? undefined : detail.updatedAt
      )
      setDirty(false)
      setConflict(false)
      notify({ tone: 'success', title: t('pages.kanban.toast_saved') })
      await invalidateAll()
    } catch (error) {
      if (error instanceof AppError && error.code === AppErrorCode.Conflict) {
        setConflict(true)
      } else {
        notify({
          tone: 'error',
          title: t('pages.kanban.toast_save_fail'),
          message: error instanceof Error ? error.message : String(error)
        })
      }
    } finally {
      setSaving(false)
    }
  }

  const reload = async (): Promise<void> => {
    const result = await detailQuery.refetch()
    if (result.data !== undefined) {
      setForm(formFromDetail(result.data))
      setDirty(false)
    }
    setConflict(false)
  }

  const archive = async (archived: boolean): Promise<void> => {
    try {
      if (archived) {
        await kanbanApi.unarchiveTask(projectId, taskId)
        notify({ tone: 'success', title: t('pages.kanban.toast_unarchived') })
      } else {
        await kanbanApi.archiveTask(projectId, taskId)
        notify({ tone: 'success', title: t('pages.kanban.toast_archived') })
      }
      await invalidateAll()
    } catch (error) {
      notify({
        tone: 'error',
        title: archived ? t('pages.kanban.toast_unarchive_fail') : t('pages.kanban.toast_archive_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }

  const removeTask = async (): Promise<void> => {
    setDeleteOpen(false)
    try {
      await kanbanApi.deleteTask(projectId, taskId)
      await invalidateAll()
      onDeleted(taskId, detail.title)
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_delete_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }

  const addLabel = (): void => {
    if (form === null) return
    const value = labelDraft.trim()
    if (value === '' || form.labels.includes(value)) return
    update({ labels: [...form.labels, value] })
    setLabelDraft('')
  }

  const toggleSubtask = async (subtaskId: string, done: boolean): Promise<void> => {
    try {
      await kanbanApi.completeTask(projectId, subtaskId, done)
      await invalidateAll()
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_save_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }

  const addSubtask = async (): Promise<void> => {
    const value = subtaskDraft.trim()
    if (value === '' || detail.parentTaskId !== null) return
    try {
      await kanbanApi.createTask({
        projectId,
        title: value,
        parentTaskId: detail.id,
        column: detail.column
      })
      setSubtaskDraft('')
      await invalidateAll()
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_create_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    }
  }

  const removeSubtask = async (): Promise<void> => {
    if (deleteSubtask === null) return
    try {
      await kanbanApi.deleteTask(projectId, deleteSubtask.id)
      await invalidateAll()
    } catch (error) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_delete_fail'),
        message: error instanceof Error ? error.message : String(error)
      })
    } finally {
      setDeleteSubtask(null)
    }
  }

  const header = (): JSX.Element | null => {
    if (variant !== 'panel') return null
    return (
      <header className={styles.dockHeader}>
        <span className={styles.dockTitle}>{detail.title}</span>
        {onClose !== undefined && (
          <IconButton icon={iconFor('close')} label={t('common.close')} size="sm" onClick={onClose} />
        )}
      </header>
    )
  }

  return (
    <div className={styles.stack}>
      {header()}
      {projectArchived && (
        <Banner tone="warning" title={t('pages.kanban.archived_title')}>
          {t('pages.kanban.archived_message')}
        </Banner>
      )}
      {!projectArchived && readOnly && (
        <Banner
          tone="warning"
          title={t('pages.kanban.detail_archived_title')}
          action={
            <Button size="sm" icon={ArchiveRestore} onClick={() => void archive(true)}>
              {t('pages.kanban.btn_unarchive')}
            </Button>
          }
        >
          {t('pages.kanban.detail_archived_message')}
        </Banner>
      )}
      {conflict && (
        <Banner
          tone="danger"
          title={t('pages.kanban.stale_title')}
          action={
            <div className={styles.rowActions}>
              <Button size="sm" onClick={() => void reload()}>
                {t('pages.kanban.stale_reload')}
              </Button>
              <Button size="sm" variant="danger" onClick={() => void save(true)}>
                {t('pages.kanban.stale_overwrite')}
              </Button>
            </div>
          }
        >
          {t('pages.kanban.stale_message')}
        </Banner>
      )}
      <Tabs
        label={t('pages.kanban.detail_tabs_label')}
        value={tab}
        onChange={(value) => {
          if (value === 'detail' || value === 'checklist' || value === 'links') setTab(value)
        }}
        items={[
          { id: 'detail', label: t('pages.kanban.detail_tab_detail') },
          { id: 'checklist', label: t('pages.kanban.detail_tab_checklist') },
          { id: 'links', label: t('pages.kanban.detail_tab_links') }
        ]}
      />

      {tab === 'detail' && form !== null && (
        <div className={styles.detailForm}>
          <TextInput
            label={t('pages.kanban.field_title')}
            value={form.title}
            disabled={readOnly}
            error={!titleValid ? t('pages.kanban.title_required') : undefined}
            onChange={(title) => update({ title })}
          />
          <div className={styles.stack}>
            <Tabs
              label={t('pages.kanban.mode_label')}
              value={mode}
              onChange={(value) => {
                if (value === 'source' || value === 'preview') setMode(value)
              }}
              items={[
                { id: 'source', label: t('pages.kanban.mode_source') },
                { id: 'preview', label: t('pages.kanban.mode_preview') }
              ]}
            />
            {mode === 'source' ? (
              <textarea
                className={styles.textarea}
                aria-label={t('pages.kanban.field_description')}
                value={form.descriptionMd}
                disabled={readOnly}
                onChange={(event) => update({ descriptionMd: event.target.value })}
              />
            ) : (
              <Suspense fallback={<Skeleton rows={3} />}>
                <MarkdownPreview source={form.descriptionMd} />
              </Suspense>
            )}
          </div>
          <div className={styles.formGrid}>
            <SelectField
              label={t('pages.kanban.field_priority')}
              value={form.priority}
              options={PRIORITIES.map((priority) => ({
                value: priority.value,
                label: t(priority.key)
              }))}
              disabled={readOnly}
              onChange={(value) => {
                const selected = PRIORITIES.find((priority) => priority.value === value)
                if (selected !== undefined) update({ priority: selected.value })
              }}
            />
            <TextInput
              label={t('pages.kanban.field_deadline')}
              type="date"
              value={form.deadline}
              disabled={readOnly}
              onChange={(deadline) => update({ deadline })}
            />
            <TextInput
              label={t('pages.kanban.field_week_plan')}
              placeholder={`${isoWeekNow()}`}
              value={form.weekPlan}
              disabled={readOnly}
              error={!weekValid ? t('pages.kanban.err_invalid') : undefined}
              onChange={(weekPlan) => update({ weekPlan })}
            />
            <SelectField
              label={t('pages.kanban.field_column')}
              value={form.column}
              options={columnOptions}
              disabled={readOnly}
              onChange={(column) => update({ column })}
            />
          </div>
          <div className={styles.stack}>
            <span className={styles.sectionTitle}>{t('pages.kanban.field_labels')}</span>
            <div className={styles.chips}>
              {form.labels.map((label) => (
                <span key={label} className={styles.chip}>
                  {label}
                  {!readOnly && (
                    <button
                      type="button"
                      className={styles.chipRemove}
                      aria-label={t('pages.kanban.label_remove', { label })}
                      onClick={() =>
                        update({ labels: form.labels.filter((candidate) => candidate !== label) })
                      }
                    >
                      <X aria-hidden="true" size={12} />
                    </button>
                  )}
                </span>
              ))}
            </div>
            {!readOnly && (
              <form
                className={styles.quickAddRow}
                onSubmit={(event) => {
                  event.preventDefault()
                  addLabel()
                }}
              >
                <TextInput
                  label={t('pages.kanban.field_label_add')}
                  hideLabel
                  placeholder={t('pages.kanban.field_label_hint')}
                  value={labelDraft}
                  onChange={setLabelDraft}
                />
                <Button type="submit" size="sm" icon={Plus}>
                  {t('pages.kanban.field_label_add')}
                </Button>
              </form>
            )}
          </div>
          <SwitchField
            label={t('pages.kanban.field_blocked')}
            description={t('pages.kanban.field_blocked_desc')}
            checked={form.blocked}
            disabled={readOnly}
            onChange={(blocked) => update({ blocked })}
          />
          <div className={styles.stack}>
            <span className={styles.sectionTitle}>{t('pages.kanban.subtasks_title')}</span>
            {detail.subtasks.length === 0 ? (
              <p className={styles.muted}>{t('pages.kanban.subtasks_empty')}</p>
            ) : (
              <div className={styles.subtasks}>
                {detail.subtasks.map((subtask) => (
                  <div key={subtask.id} className={styles.subtask}>
                    <input
                      type="checkbox"
                      aria-label={subtask.title}
                      checked={subtask.completedAt !== null}
                      disabled={readOnly}
                      onChange={() =>
                        void toggleSubtask(subtask.id, subtask.completedAt === null)
                      }
                    />
                    <button
                      type="button"
                      className={[styles.subtaskTitle, subtask.completedAt !== null ? styles.subtaskDone : '']
                        .filter(Boolean)
                        .join(' ')}
                      onClick={() => onOpenTask(subtask.id)}
                    >
                      {subtask.title}
                    </button>
                    {!readOnly && (
                      <IconButton
                        icon={Trash2}
                        label={t('pages.kanban.btn_delete_task')}
                        size="sm"
                        onClick={() => setDeleteSubtask({ id: subtask.id, title: subtask.title })}
                      />
                    )}
                  </div>
                ))}
              </div>
            )}
            {!readOnly && detail.parentTaskId === null && (
              <form
                className={styles.quickAddRow}
                onSubmit={(event) => {
                  event.preventDefault()
                  void addSubtask()
                }}
              >
                <TextInput
                  label={t('pages.kanban.field_subtask_add')}
                  hideLabel
                  placeholder={t('pages.kanban.field_subtask_hint')}
                  value={subtaskDraft}
                  onChange={setSubtaskDraft}
                />
                <Button type="submit" size="sm" icon={Plus}>
                  {t('pages.kanban.field_subtask_add')}
                </Button>
              </form>
            )}
          </div>
          {!readOnly && (
            <div className={styles.rowActions}>
              <Button
                variant="primary"
                disabled={!dirty || saving || !titleValid || !weekValid}
                onClick={() => void save(false)}
              >
                {t('pages.kanban.btn_save')}
              </Button>
              <Button icon={Archive} onClick={() => void archive(false)}>
                {t('pages.kanban.btn_archive_task')}
              </Button>
              <Button variant="danger" icon={Trash2} onClick={() => setDeleteOpen(true)}>
                {t('pages.kanban.btn_delete_task')}
              </Button>
            </div>
          )}
        </div>
      )}

      {tab === 'checklist' && (
        <ChecklistPanel
          projectId={projectId}
          detail={detail}
          readOnly={readOnly}
          notify={notify}
        />
      )}
      {tab === 'links' && (
        <LinksPanel projectId={projectId} detail={detail} readOnly={readOnly} notify={notify} />
      )}

      <ConfirmDialog
        open={deleteOpen}
        title={t('pages.kanban.delete_title')}
        description={t('pages.kanban.delete_message', { title: detail.title })}
        confirmLabel={t('pages.kanban.delete_confirm')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        onCancel={() => setDeleteOpen(false)}
        onConfirm={() => void removeTask()}
      />
      <ConfirmDialog
        open={deleteSubtask !== null}
        title={t('pages.kanban.delete_title')}
        description={t('pages.kanban.delete_message', { title: deleteSubtask?.title ?? '' })}
        confirmLabel={t('pages.kanban.delete_confirm')}
        cancelLabel={t('common.cancel')}
        tone="danger"
        onCancel={() => setDeleteSubtask(null)}
        onConfirm={() => void removeSubtask()}
      />
    </div>
  )
}

export interface TaskPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

/** Full page route `task/:id`; the board's dock uses the same body. */
export function TaskPage({ projectId, entityId }: TaskPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const params = useParams<{ projectId?: string; entityId?: string }>()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const { toasts, notify, dismiss } = useFeatureToasts()
  const resolvedProject = projectId ?? params.projectId ?? currentProjectId
  const taskId = entityId ?? params.entityId

  if (resolvedProject === null || resolvedProject === undefined || taskId === undefined) {
    return (
      <ErrorState
        title={t('pages.kanban.needs_project_title')}
        message={t('pages.kanban.needs_project_reason')}
      />
    )
  }

  return (
    <div className={styles.fullPage}>
      <div className={styles.rowActions}>
        <Button
          variant="ghost"
          icon={ArrowLeft}
          onClick={() => {
            const href = routeHref('kanban', { projectId: resolvedProject })
            if (href !== undefined) navigate(href)
          }}
        >
          {t('pages.kanban.title')}
        </Button>
      </div>
      <TaskDetailBody
        projectId={resolvedProject}
        taskId={taskId}
        projectArchived={archived}
        variant="page"
        onOpenTask={(next) => {
          const href = routeHref('task', { projectId: resolvedProject, entityId: next })
          if (href !== undefined) navigate(href)
        }}
        onDeleted={() => {
          const href = routeHref('kanban', { projectId: resolvedProject })
          if (href !== undefined) navigate(href)
        }}
        notify={notify}
      />
      <Toasts toasts={toasts} onDismiss={dismiss} />
    </div>
  )
}

export default TaskPage