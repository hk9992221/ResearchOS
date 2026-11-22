import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Check, Plus, X } from 'lucide-react'

import type { BoardView, ChecklistItem, TaskDetail } from '@researchos/shared'

import { Button, IconButton, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { kanbanApi } from './api'
import { kanbanKeys } from './keys'
import type { ToastInput } from './useFeatureToasts'
import styles from './kanban.module.css'

export interface ChecklistPanelProps {
  readonly projectId: string
  readonly detail: TaskDetail
  readonly readOnly: boolean
  readonly notify: (toast: ToastInput) => void
}

interface PageLike {
  readonly items: readonly { id: string; checklistDone: number; checklistTotal: number }[]
}

function isBoard(value: unknown): value is BoardView {
  return typeof value === 'object' && value !== null && 'columns' in value && Array.isArray(value.columns)
}

function isPage(value: unknown): value is PageLike {
  return typeof value === 'object' && value !== null && 'items' in value && Array.isArray(value.items)
}

/**
 * Checklist tab. A toggle writes one item and patches the card counters in the
 * cached board/page; only the task detail query is invalidated, so a checkbox
 * never refetches the whole task list.
 */
export function ChecklistPanel({
  projectId,
  detail,
  readOnly,
  notify
}: ChecklistPanelProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  const detailKey = kanbanKeys.detail(projectId, detail.id)
  const items = [...detail.checklist].sort((left, right) => left.sortOrder - right.sortOrder)

  const patchCounters = (deltaDone: number, deltaTotal: number): void => {
    queryClient.setQueriesData({ queryKey: kanbanKeys.all(projectId) }, (cached: unknown) => {
      const patch = (task: {
        id: string
        checklistDone: number
        checklistTotal: number
      }): typeof task =>
        task.id === detail.id
          ? {
              ...task,
              checklistDone: Math.max(0, task.checklistDone + deltaDone),
              checklistTotal: Math.max(0, task.checklistTotal + deltaTotal)
            }
          : task
      if (isBoard(cached)) {
        return {
          ...cached,
          columns: cached.columns.map((column) => ({
            ...column,
            tasks: column.tasks.map(patch)
          }))
        }
      }
      if (isPage(cached)) {
        return { ...cached, items: cached.items.map(patch) }
      }
      return cached
    })
  }

  const setLocalChecklist = (update: (items: ChecklistItem[]) => ChecklistItem[]): void => {
    queryClient.setQueryData<TaskDetail>(detailKey, (current) =>
      current === undefined ? current : { ...current, checklist: update(current.checklist) }
    )
  }

  const invalidateDetail = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: detailKey })
  }

  const toggle = async (item: ChecklistItem): Promise<void> => {
    if (readOnly) return
    setBusy(true)
    setLocalChecklist((current) =>
      current.map((candidate) =>
        candidate.id === item.id ? { ...candidate, done: !candidate.done } : candidate
      )
    )
    patchCounters(item.done ? -1 : 1, 0)
    try {
      await kanbanApi.checklist.setDone(projectId, item.id, !item.done)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_checklist_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    } finally {
      setBusy(false)
      await invalidateDetail()
    }
  }

  const add = async (): Promise<void> => {
    const value = text.trim()
    if (value === '' || readOnly) return
    setBusy(true)
    try {
      await kanbanApi.checklist.add(projectId, detail.id, value)
      setText('')
      patchCounters(0, 1)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_checklist_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    } finally {
      setBusy(false)
      await invalidateDetail()
    }
  }

  const updateText = async (item: ChecklistItem, value: string): Promise<void> => {
    if (readOnly || value.trim() === '' || value === item.text) return
    try {
      await kanbanApi.checklist.update(projectId, item.id, value.trim())
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_checklist_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    }
    await invalidateDetail()
  }

  const remove = async (item: ChecklistItem): Promise<void> => {
    if (readOnly) return
    setLocalChecklist((current) => current.filter((candidate) => candidate.id !== item.id))
    patchCounters(item.done ? -1 : 0, -1)
    try {
      await kanbanApi.checklist.remove(projectId, item.id)
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_checklist_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    }
    await invalidateDetail()
  }

  const move = async (index: number, delta: number): Promise<void> => {
    const target = index + delta
    if (readOnly || target < 0 || target >= items.length) return
    const next = [...items]
    const [moved] = next.splice(index, 1)
    if (moved === undefined) return
    next.splice(target, 0, moved)
    setLocalChecklist(() => next.map((item, sortOrder) => ({ ...item, sortOrder })))
    try {
      await kanbanApi.checklist.reorder(
        projectId,
        detail.id,
        next.map((item) => item.id)
      )
    } catch (cause) {
      notify({
        tone: 'error',
        title: t('pages.kanban.toast_checklist_fail'),
        message: cause instanceof Error ? cause.message : String(cause)
      })
    }
    await invalidateDetail()
  }

  return (
    <div className={styles.stack}>
      {items.length === 0 ? (
        <p className={styles.muted}>{t('pages.kanban.checklist_empty')}</p>
      ) : (
        <div className={styles.checklist}>
          {items.map((item, index) => (
            <div key={item.id} className={styles.checklistItem}>
              <input
                type="checkbox"
                aria-label={item.text}
                checked={item.done}
                disabled={readOnly || busy}
                onChange={() => void toggle(item)}
              />
              <input
                className={[styles.checklistText, item.done ? styles.checklistTextDone : ''].join(' ')}
                aria-label={t('pages.kanban.checklist_edit')}
                defaultValue={item.text}
                disabled={readOnly}
                onBlur={(event) => void updateText(item, event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') event.currentTarget.blur()
                }}
              />
              <IconButton
                icon={ArrowUp}
                label={t('pages.kanban.checklist_up')}
                size="sm"
                disabled={readOnly || index === 0}
                onClick={() => void move(index, -1)}
              />
              <IconButton
                icon={ArrowDown}
                label={t('pages.kanban.checklist_down')}
                size="sm"
                disabled={readOnly || index === items.length - 1}
                onClick={() => void move(index, 1)}
              />
              <IconButton
                icon={X}
                label={t('pages.kanban.checklist_remove')}
                size="sm"
                disabled={readOnly}
                onClick={() => void remove(item)}
              />
            </div>
          ))}
        </div>
      )}
      {!readOnly && (
        <form
          className={styles.quickAddRow}
          onSubmit={(event) => {
            event.preventDefault()
            void add()
          }}
        >
          <TextInput
            label={t('pages.kanban.field_checklist_add')}
            hideLabel
            placeholder={t('pages.kanban.field_checklist_hint')}
            value={text}
            disabled={busy}
            onChange={setText}
          />
          <Button type="submit" variant="secondary" size="sm" icon={Plus} disabled={busy}>
            {t('pages.kanban.field_checklist_add')}
          </Button>
        </form>
      )}
      {items.length > 0 && (
        <p className={styles.muted}>
          <Check aria-hidden="true" size={12} />{' '}
          {t('pages.kanban.count_pair', {
            done: items.filter((item) => item.done).length,
            total: items.length
          })}
        </p>
      )}
    </div>
  )
}