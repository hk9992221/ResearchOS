import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import type { DbField, DbFieldType, DbRecord, SortKey } from '@researchos/shared'
import { DB_FIELD_TYPES } from '@researchos/shared'

import {
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  IconButton,
  Modal,
  SelectField,
  Skeleton,
  TextInput
} from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { notesApi } from './notes-api'
import { FieldDialog } from './FieldDialog'
import { FilterBuilder, countLeaves, emptyGroup, normalizeFilter, toFilterNode, type UiGroup } from './FilterBuilder'
import { RecordTable } from './RecordTable'
import styles from './notes.module.css'

export interface DatabaseViewProps {
  readonly projectId: string
  readonly archived: boolean
}

interface InitialField {
  readonly name: string
  readonly type: DbFieldType
}

const TYPE_LABEL: Readonly<Record<DbFieldType, string>> = {
  text: 'pages.notes.type_text',
  number: 'pages.notes.type_number',
  select: 'pages.notes.type_select',
  multi_select: 'pages.notes.type_multi',
  date: 'pages.notes.type_date',
  checkbox: 'pages.notes.type_checkbox',
  url: 'pages.notes.type_url',
  relation: 'pages.notes.type_relation',
  tags: 'pages.notes.type_tags'
}

const BUILTIN_SORTS: ReadonlyArray<{ id: string; label: string }> = [
  { id: '__title__', label: 'pages.notes.col_title' },
  { id: '__created_at__', label: 'pages.notes.db_created' },
  { id: '__updated_at__', label: 'pages.notes.db_updated' }
]

/** Research Database view: database list, table, filters, sorts, fields. */
export function DatabaseView({ projectId, archived }: DatabaseViewProps): JSX.Element {
  const t = useT()
  const queryClient = useQueryClient()

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [newOpen, setNewOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [newFields, setNewFields] = useState<InitialField[]>([{ name: 'Title', type: 'text' }])
  const [fieldsOpen, setFieldsOpen] = useState(false)
  const [fieldDialog, setFieldDialog] = useState<{ mode: 'create' } | { mode: 'edit'; field: DbField } | null>(null)
  const [deleteDatabase, setDeleteDatabase] = useState(false)
  const [deleteRecord, setDeleteRecord] = useState<DbRecord | null>(null)
  const [deleteFieldTarget, setDeleteFieldTarget] = useState<DbField | null>(null)
  const [filterOpen, setFilterOpen] = useState(false)
  const [draftFilter, setDraftFilter] = useState<UiGroup>(emptyGroup())
  const [filter, setFilter] = useState<UiGroup | null>(null)
  const [sortOpen, setSortOpen] = useState(false)
  const [drafts, setDrafts] = useState<SortKey[]>([])
  const [sorts, setSorts] = useState<SortKey[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const databasesQuery = useQuery({
    queryKey: ['notes', projectId, 'databases'],
    queryFn: () => notesApi.databases(projectId)
  })
  const databases = databasesQuery.data ?? []
  const selected = databases.find((database) => database.id === selectedId) ?? databases[0] ?? null
  const databaseId = selected?.id ?? null

  useEffect(() => {
    if (selectedId === null && databases.length > 0) setSelectedId(databases[0]?.id ?? null)
  }, [databases, selectedId])

  const fieldsQuery = useQuery({
    queryKey: ['notes', projectId, 'database', databaseId ?? 'none', 'fields'],
    queryFn: () => notesApi.fields(projectId, databaseId ?? ''),
    enabled: databaseId !== null
  })
  const fields = fieldsQuery.data ?? []

  const tagsQuery = useQuery({
    queryKey: ['notes', projectId, 'tags'],
    queryFn: () => notesApi.listTags(projectId),
    retry: 0
  })
  const tags = tagsQuery.data ?? []

  const appliedFilter = useMemo(
    () => (filter === null || countLeaves(filter) === 0 ? undefined : toFilterNode(normalizeFilter(filter, fields))),
    [filter, fields]
  )

  const recordsQuery = useQuery({
    queryKey: ['notes', projectId, 'database', databaseId ?? 'none', 'records', appliedFilter ?? null, sorts],
    queryFn: () =>
      notesApi.records(projectId, {
        databaseId: databaseId ?? '',
        filter: appliedFilter,
        sorts: sorts.length === 0 ? undefined : sorts,
        limit: 200
      }),
    enabled: databaseId !== null
  })

  const invalidate = async (what: 'all' | 'fields' | 'records'): Promise<void> => {
    if (what === 'fields' || what === 'all') {
      await queryClient.invalidateQueries({
        queryKey: ['notes', projectId, 'database', databaseId ?? 'none', 'fields']
      })
    }
    if (what === 'records' || what === 'all') {
      await queryClient.invalidateQueries({
        queryKey: ['notes', projectId, 'database', databaseId ?? 'none', 'records']
      })
    }
    if (what === 'all') {
      await queryClient.invalidateQueries({ queryKey: ['notes', projectId, 'databases'] })
    }
  }

  const fieldTypes = DB_FIELD_TYPES.map((type) => ({ value: type, label: t(TYPE_LABEL[type]) }))

  const openFilter = (): void => {
    setDraftFilter(filter ?? emptyGroup())
    setFilterOpen(true)
  }

  const applyFilter = (): void => {
    setFilter(countLeaves(draftFilter) === 0 ? null : draftFilter)
    setFilterOpen(false)
  }

  const openSort = (): void => {
    setDrafts([...sorts])
    setSortOpen(true)
  }

  const createDatabase = async (): Promise<void> => {
    if (newName.trim() === '') return
    setBusy(true)
    setError(null)
    try {
      const fieldsInput = newFields
        .filter((field) => field.name.trim() !== '')
        .map((field, index) => ({
          name: field.name.trim(),
          type: field.type,
          role: index === 0 ? ('title' as const) : ('plain' as const)
        }))
      if (fieldsInput.length === 0) return
      const database = await notesApi.createDatabase(projectId, {
        name: newName.trim(),
        fields: fieldsInput
      })
      setNewOpen(false)
      setNewName('')
      setNewFields([{ name: 'Title', type: 'text' }])
      await queryClient.invalidateQueries({ queryKey: ['notes', projectId, 'databases'] })
      setSelectedId(database.id)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const createRecord = async (): Promise<void> => {
    if (databaseId === null) return
    setBusy(true)
    setError(null)
    try {
      await notesApi.createRecord(projectId, { databaseId })
      await invalidate('records')
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const removeRecord = async (): Promise<void> => {
    if (databaseId === null || deleteRecord === null) return
    setBusy(true)
    setError(null)
    try {
      await notesApi.deleteRecord(projectId, databaseId, deleteRecord.id)
      setDeleteRecord(null)
      await invalidate('records')
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const removeDatabase = async (): Promise<void> => {
    if (databaseId === null) return
    setBusy(true)
    setError(null)
    try {
      await notesApi.deleteDatabase(projectId, databaseId)
      setDeleteDatabase(false)
      setSelectedId(null)
      await invalidate('all')
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const deleteField = async (): Promise<void> => {
    if (databaseId === null || deleteFieldTarget === null) return
    setBusy(true)
    setError(null)
    try {
      await notesApi.deleteField(projectId, databaseId, deleteFieldTarget.id, deleteFieldTarget.updatedAt)
      setDeleteFieldTarget(null)
      setFieldDialog(null)
      await invalidate('fields')
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  if (databasesQuery.isPending) return <Skeleton rows={5} />
  if (databasesQuery.isError) {
    return (
      <ErrorState
        title={t('pages.notes.db_banner_problem')}
        message={databasesQuery.error.message}
        onRetry={() => void databasesQuery.refetch()}
        retryLabel={t('shell.overview.error_retry')}
      />
    )
  }

  if (databases.length === 0) {
    return (
      <>
        <EmptyState
          icon={iconFor('note')}
          title={t('pages.notes.db_empty_title')}
          description={t('pages.notes.db_empty_reason')}
          action={
            <Button variant="primary" disabled={archived} onClick={() => setNewOpen(true)}>
              {t('pages.notes.db_new')}
            </Button>
          }
        />
        {newOpen && (
          <CreateDatabaseDialog
            open={newOpen}
            name={newName}
            fields={newFields}
            fieldTypes={fieldTypes}
            busy={busy}
            onName={setNewName}
            onFields={setNewFields}
            onClose={() => setNewOpen(false)}
            onCreate={() => void createDatabase()}
          />
        )}
      </>
    )
  }

  const records = recordsQuery.data?.items ?? []

  return (
    <div className={styles.databaseView}>
      {error !== null && (
        <Banner tone="danger" title={t('pages.notes.db_banner_problem')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}
      <div className={styles.dbToolbar} role="toolbar" aria-label={t('pages.notes.tab_database')}>
        <SelectField
          label={t('pages.notes.tab_database')}
          value={databaseId ?? ''}
          options={databases.map((database) => ({ value: database.id, label: database.name }))}
          onChange={(id) => setSelectedId(id)}
        />
        <Button variant="secondary" disabled={archived} onClick={() => setNewOpen(true)}>
          {t('pages.notes.db_new')}
        </Button>
        <Button variant="ghost" onClick={() => setFieldsOpen(true)}>
          {t('pages.notes.db_fields')}
        </Button>
        <Button variant="ghost" onClick={openFilter}>
          {filter === null ? t('pages.notes.db_filter') : t('pages.notes.db_filter_n', { n: countLeaves(filter) })}
        </Button>
        <Button variant="ghost" onClick={openSort}>
          {sorts.length === 0 ? t('pages.notes.db_sort') : t('pages.notes.db_sort_n', { n: sorts.length })}
        </Button>
        <Button
          variant="primary"
          icon={iconFor('plus')}
          disabled={archived || databaseId === null}
          onClick={() => void createRecord()}
        >
          {t('pages.notes.db_new_record')}
        </Button>
        <IconButton
          icon={iconFor('trash')}
          label={t('pages.notes.db_delete')}
          disabled={archived || databaseId === null}
          onClick={() => setDeleteDatabase(true)}
        />
      </div>

      {recordsQuery.isPending ? (
        <Skeleton rows={5} />
      ) : recordsQuery.isError ? (
        <ErrorState
          title={t('pages.notes.db_banner_problem')}
          message={recordsQuery.error.message}
          onRetry={() => void recordsQuery.refetch()}
          retryLabel={t('shell.overview.error_retry')}
        />
      ) : (
        <RecordTable
          projectId={projectId}
          databaseId={databaseId ?? ''}
          fields={fields}
          records={records}
          tags={tags}
          readOnly={archived}
          onChanged={() => void invalidate('records')}
          onError={(cause) => setError(cause)}
          onDelete={(record) => setDeleteRecord(record)}
          onReorderFields={async (fieldIds) => {
            if (databaseId === null) return
            await notesApi.reorderFields(projectId, databaseId, fieldIds)
            await invalidate('fields')
          }}
        />
      )}

      <CreateDatabaseDialog
        open={newOpen}
        name={newName}
        fields={newFields}
        fieldTypes={fieldTypes}
        busy={busy}
        onName={setNewName}
        onFields={setNewFields}
        onClose={() => setNewOpen(false)}
        onCreate={() => void createDatabase()}
      />

      <Modal
        open={filterOpen}
        title={t('pages.notes.db_filter')}
        onClose={() => setFilterOpen(false)}
        width={720}
        footer={
          <>
            <Button
              variant="ghost"
              onClick={() => {
                setDraftFilter(emptyGroup())
                setFilter(null)
              }}
            >
              {t('pages.notes.db_clear')}
            </Button>
            <Button variant="secondary" onClick={() => setFilterOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button variant="primary" onClick={applyFilter}>
              {t('pages.notes.db_apply')}
            </Button>
          </>
        }
      >
        <FilterBuilder value={draftFilter} fields={fields} tags={tags} onChange={setDraftFilter} />
      </Modal>

      <Modal
        open={sortOpen}
        title={t('pages.notes.db_sort')}
        onClose={() => setSortOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setSortOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setSorts(drafts)
                setSortOpen(false)
              }}
            >
              {t('pages.notes.db_apply')}
            </Button>
          </>
        }
      >
        <div className={styles.sortEditor}>
          {drafts.map((sort, index) => (
            <div key={`${sort.field}-${index}`} className={styles.sortRow}>
              <SelectField
                label={t('pages.notes.db_sort_field')}
                value={sort.field}
                options={[
                  ...BUILTIN_SORTS.map((entry) => ({ value: entry.id, label: t(entry.label) })),
                  ...fields.map((field) => ({ value: field.id, label: field.name }))
                ]}
                onChange={(field) =>
                  setDrafts((current) =>
                    current.map((entry, position) => (position === index ? { ...entry, field } : entry))
                  )
                }
              />
              <SelectField
                label={t('pages.notes.db_direction')}
                value={sort.direction}
                options={[
                  { value: 'asc', label: t('pages.notes.db_asc') },
                  { value: 'desc', label: t('pages.notes.db_desc') }
                ]}
                onChange={(direction) =>
                  setDrafts((current) =>
                    current.map((entry, position) =>
                      position === index ? { ...entry, direction: direction === 'desc' ? 'desc' : 'asc' } : entry
                    )
                  )
                }
              />
              <IconButton
                icon={iconFor('close')}
                label={t('pages.notes.db_remove_sort')}
                onClick={() => setDrafts((current) => current.filter((_entry, position) => position !== index))}
              />
            </div>
          ))}
          <Button
            variant="ghost"
            icon={iconFor('plus')}
            onClick={() =>
              setDrafts((current) => [...current, { field: fields[0]?.id ?? '__title__', direction: 'asc' }])
            }
          >
            {t('pages.notes.db_add_sort')}
          </Button>
        </div>
      </Modal>

      <Modal
        open={fieldsOpen}
        title={t('pages.notes.db_fields')}
        onClose={() => setFieldsOpen(false)}
        width={560}
        footer={
          <>
            <Button variant="secondary" onClick={() => setFieldsOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button
              variant="primary"
              icon={iconFor('plus')}
              disabled={archived}
              onClick={() => {
                setFieldsOpen(false)
                setFieldDialog({ mode: 'create' })
              }}
            >
              {t('pages.notes.db_add_field')}
            </Button>
          </>
        }
      >
        <ul className={styles.fieldList}>
          {fields.map((field) => (
            <li key={field.id} className={styles.fieldRow}>
              <span>{field.name}</span>
              <span className={styles.muted}>{t(TYPE_LABEL[field.type])}</span>
              <Button
                variant="ghost"
                size="sm"
                disabled={archived}
                onClick={() => {
                  setFieldsOpen(false)
                  setFieldDialog({ mode: 'edit', field })
                }}
              >
                {t('pages.notes.db_edit_field')}
              </Button>
              <IconButton
                icon={iconFor('trash')}
                label={t('pages.notes.db_delete_field')}
                disabled={archived || field.role === 'title'}
                onClick={() => setDeleteFieldTarget(field)}
              />
            </li>
          ))}
        </ul>
      </Modal>

      <FieldDialog
        open={fieldDialog !== null}
        mode={fieldDialog?.mode ?? 'create'}
        field={fieldDialog?.mode === 'edit' ? fieldDialog.field : undefined}
        fieldTypes={fieldTypes}
        busy={busy}
        onClose={() => setFieldDialog(null)}
        onSave={async (input, expectedUpdatedAt) => {
          if (databaseId === null) return
          setBusy(true)
          setError(null)
          try {
            if (fieldDialog?.mode === 'edit') {
              await notesApi.updateField(projectId, databaseId, fieldDialog.field.id, input, expectedUpdatedAt)
            } else {
              await notesApi.createField(projectId, databaseId, {
                name: input.name ?? '',
                type: input.type ?? 'text',
                options: input.options,
                targets: input.targets,
                role: input.role
              })
            }
            setFieldDialog(null)
            await invalidate('fields')
          } catch (cause) {
            setError(cause)
          } finally {
            setBusy(false)
          }
        }}
      />

      <ConfirmDialog
        open={deleteFieldTarget !== null}
        title={t('pages.notes.db_delete_field')}
        description={deleteFieldTarget?.name}
        confirmLabel={t('pages.notes.db_delete_field')}
        cancelLabel={t('pages.notes.btn_cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void deleteField()}
        onCancel={() => setDeleteFieldTarget(null)}
      />
      <ConfirmDialog
        open={deleteRecord !== null}
        title={t('pages.notes.db_delete_record')}
        confirmLabel={t('pages.notes.db_delete_record')}
        cancelLabel={t('pages.notes.btn_cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void removeRecord()}
        onCancel={() => setDeleteRecord(null)}
      />
      <ConfirmDialog
        open={deleteDatabase}
        title={t('pages.notes.db_delete')}
        confirmLabel={t('pages.notes.db_delete')}
        cancelLabel={t('pages.notes.btn_cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void removeDatabase()}
        onCancel={() => setDeleteDatabase(false)}
      />
    </div>
  )
}

function CreateDatabaseDialog({
  open,
  name,
  fields,
  fieldTypes,
  busy,
  onName,
  onFields,
  onClose,
  onCreate
}: {
  readonly open: boolean
  readonly name: string
  readonly fields: InitialField[]
  readonly fieldTypes: ReadonlyArray<{ value: string; label: string }>
  readonly busy: boolean
  readonly onName: (name: string) => void
  readonly onFields: (fields: InitialField[]) => void
  readonly onClose: () => void
  readonly onCreate: () => void
}): JSX.Element {
  const t = useT()
  return (
    <Modal
      open={open}
      title={t('pages.notes.db_new')}
      onClose={onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.notes.btn_cancel')}
          </Button>
          <Button
            variant="primary"
            onClick={onCreate}
            disabled={
              busy ||
              name.trim() === '' ||
              fields.length === 0 ||
              fields.some((field) => field.name.trim() === '')
            }
          >
            {t('pages.notes.db_create')}
          </Button>
        </>
      }
    >
      <TextInput
        label={t('pages.notes.db_name')}
        value={name}
        onChange={onName}
        autoFocus
        error={name.trim() === '' ? t('pages.notes.db_name_required') : undefined}
      />
      <div className={styles.initialFields}>
        {fields.map((field, index) => (
          <div key={index} className={styles.sortRow}>
            <TextInput
              label={t('pages.notes.db_field_name')}
              value={field.name}
              onChange={(value) =>
                onFields(fields.map((entry, position) => (position === index ? { ...entry, name: value } : entry)))
              }
            />
            <SelectField
              label={t('pages.notes.db_field_type')}
              value={field.type}
              options={fieldTypes}
              onChange={(type) =>
                onFields(
                  fields.map((entry, position) =>
                    position === index ? { ...entry, type: type as DbFieldType } : entry
                  )
                )
              }
            />
            <IconButton
              icon={iconFor('close')}
              label={t('pages.notes.db_remove_condition')}
              disabled={fields.length === 1}
              onClick={() => onFields(fields.filter((_entry, position) => position !== index))}
            />
          </div>
        ))}
        <Button
          variant="ghost"
          icon={iconFor('plus')}
          onClick={() => onFields([...fields, { name: '', type: 'text' }])}
        >
          {t('pages.notes.db_add_field')}
        </Button>
      </div>
    </Modal>
  )
}