import { useEffect, useState } from 'react'

import type { TaskColumnInfo } from '@researchos/shared'

import { Button, Modal, SelectField, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import styles from './kanban.module.css'

export interface ColumnRenameDialogProps {
  readonly open: boolean
  readonly column: TaskColumnInfo | null
  readonly columns: readonly TaskColumnInfo[]
  readonly busy: boolean
  readonly onClose: () => void
  readonly onSubmit: (newName: string) => void
}

/**
 * Column rename. A column name is the task rows' key, so the service rejects
 * duplicates; the dialog validates the same rules before dispatching.
 */
export function ColumnRenameDialog({
  open,
  column,
  columns,
  busy,
  onClose,
  onSubmit
}: ColumnRenameDialogProps): JSX.Element {
  const t = useT()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(column?.name ?? '')
    setError(null)
  }, [open, column])

  const submit = (): void => {
    const value = name.trim()
    if (value === '') {
      setError(t('pages.kanban.column_name_required'))
      return
    }
    if (column !== null && value !== column.name) {
      const duplicate = columns.some(
        (candidate) => candidate.name !== column.name && candidate.name === value
      )
      if (duplicate) {
        setError(t('pages.kanban.err_duplicate_column'))
        return
      }
    }
    onSubmit(value)
  }

  return (
    <Modal
      open={open}
      title={t('pages.kanban.column_rename_title')}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {t('pages.kanban.column_rename_submit')}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <TextInput
          label={t('pages.kanban.field_column_name')}
          value={name}
          error={error ?? undefined}
          disabled={busy}
          autoFocus
          onChange={(value) => {
            setName(value)
            setError(null)
          }}
        />
      </form>
    </Modal>
  )
}

export interface ColumnDeleteDialogProps {
  readonly open: boolean
  readonly column: TaskColumnInfo | null
  readonly columns: readonly TaskColumnInfo[]
  readonly taskCount: number
  readonly busy: boolean
  readonly onClose: () => void
  readonly onSubmit: (migrateTo: string) => void
}

/**
 * Deleting a column always asks where its tasks go; the confirm stays
 * disabled until a target is chosen (a migration target is required, not a
 * default). The last column cannot be deleted, so at least one target exists.
 */
export function ColumnDeleteDialog({
  open,
  column,
  columns,
  taskCount,
  busy,
  onClose,
  onSubmit
}: ColumnDeleteDialogProps): JSX.Element {
  const t = useT()
  const [target, setTarget] = useState('')

  const options = columns
    .filter((candidate) => candidate.name !== column?.name)
    .map((candidate) => ({ value: candidate.name, label: candidate.name }))
  const targetColumn = columns.find((candidate) => candidate.name === target)

  useEffect(() => {
    if (!open) return
    setTarget('')
  }, [open, column])

  return (
    <Modal
      open={open}
      title={t('pages.kanban.column_delete_title')}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="danger"
            disabled={busy || target === ''}
            onClick={() => onSubmit(target)}
          >
            {t('pages.kanban.column_delete_submit')}
          </Button>
        </>
      }
    >
      <div className={styles.questionBlock}>
        <SelectField
          label={t('pages.kanban.field_migrate_to')}
          value={target}
          options={[{ value: '', label: '—' }, ...options]}
          disabled={busy}
          onChange={setTarget}
        />
        {target !== '' && (
          <p className={styles.muted}>
            {t('pages.kanban.column_delete_message', { count: taskCount, target })}
          </p>
        )}
        {targetColumn?.isDone === true && (
          <p className={styles.errorLine}>
            {t('pages.kanban.migrate_done_title')} — {t('pages.kanban.migrate_done_message')}
          </p>
        )}
      </div>
    </Modal>
  )
}