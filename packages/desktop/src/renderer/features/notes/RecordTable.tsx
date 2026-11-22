import { useMemo, useState } from 'react'
import {
  DndContext,
  PointerSensor,
  KeyboardSensor,
  useSensor,
  useSensors,
  type DragEndEvent
} from '@dnd-kit/core'
import {
  SortableContext,
  arrayMove,
  horizontalListSortingStrategy,
  useSortable,
  sortableKeyboardCoordinates
} from '@dnd-kit/sortable'
import { tableFeatures, useTable } from '@tanstack/react-table'
import { useNavigate } from 'react-router-dom'

import type { DbField, DbRecord } from '@researchos/shared'

import { EmptyState, IconButton } from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { routeHref } from '../../routes'
import { notesApi } from './notes-api'
import { RecordCell } from './RecordCell'
import styles from './notes.module.css'

const features = tableFeatures({})

/** moveFieldOrder is the persisted column order after a header drop. */
export function moveFieldOrder(
  fields: readonly DbField[],
  activeId: string,
  overId: string
): DbField[] | null {
  const ids = fields.map((field) => field.id)
  const from = ids.indexOf(activeId)
  const to = ids.indexOf(overId)
  if (from < 0 || to < 0 || from === to) return null
  const nextIds = arrayMove(ids, from, to)
  const byId = new Map(fields.map((field) => [field.id, field]))
  return nextIds.map((id) => byId.get(id)).filter((field): field is DbField => field !== undefined)
}

export interface RecordTableProps {
  readonly projectId: string
  readonly databaseId: string
  readonly fields: readonly DbField[]
  readonly records: readonly DbRecord[]
  readonly tags: ReadonlyArray<{ id: string; name: string }>
  readonly readOnly: boolean
  readonly onChanged: () => void
  readonly onError: (error: unknown) => void
  readonly onDelete: (record: DbRecord) => void
  readonly onReorderFields: (fieldIds: string[]) => Promise<void>
}

interface SortableHeaderProps {
  readonly id: string
  readonly label: string
  readonly readOnly: boolean
  readonly onMoveLeft?: () => void
  readonly onMoveRight?: () => void
}

function SortableHeader({
  id,
  label,
  readOnly,
  onMoveLeft,
  onMoveRight
}: SortableHeaderProps): JSX.Element {
  const t = useT()
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id })
  const style = {
    transform:
      transform === null
        ? undefined
        : `translate3d(${transform.x}px, ${transform.y}px, 0) scaleX(${transform.scaleX}) scaleY(${transform.scaleY})`,
    transition,
    opacity: isDragging ? 0.6 : 1
  }
  return (
    <th ref={setNodeRef} style={style} scope="col" className={styles.th}>
      <span className={styles.thLabel}>{label}</span>
      {!readOnly && (
        <>
          <button
            type="button"
            className={styles.dragHandle}
            aria-label={t('pages.notes.db_move_field')}
            {...attributes}
            {...listeners}
          >
            ⋮⋮
          </button>
          {onMoveLeft !== undefined && (
            <button
              type="button"
              className={styles.dragHandle}
              aria-label={t('pages.notes.db_move_left')}
              onClick={onMoveLeft}
            >
              ‹
            </button>
          )}
          {onMoveRight !== undefined && (
            <button
              type="button"
              className={styles.dragHandle}
              aria-label={t('pages.notes.db_move_right')}
              onClick={onMoveRight}
            >
              ›
            </button>
          )}
        </>
      )}
    </th>
  )
}

/**
 * RecordTable is the TanStack Table v9 surface of a research database: one
 * column per field, dnd-kit header reorder persisted as the field order, and
 * inline editors bound to `expected_updated_at`.
 */
export function RecordTable({
  projectId,
  databaseId,
  fields,
  records,
  tags,
  readOnly,
  onChanged,
  onError,
  onDelete,
  onReorderFields
}: RecordTableProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [localFields, setLocalFields] = useState<readonly DbField[] | null>(null)
  const orderedFields = localFields ?? fields
  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  )

  const columns = useMemo(
    () => [
      {
        id: '__title__',
        header: t('pages.notes.col_title'),
        accessorFn: (record: DbRecord) => record.title
      },
      ...orderedFields.map((field) => ({
        id: field.id,
        header: field.name,
        accessorFn: (record: DbRecord) => record.data[field.id]
      }))
    ],
    [orderedFields, t]
  )
  const table = useTable({ features, columns, data: records })

  const commit = async (record: DbRecord, field: DbField, value: unknown): Promise<void> => {
    try {
      await notesApi.updateRecord(
        projectId,
        databaseId,
        record.id,
        { values: { [field.id]: value } },
        record.updatedAt
      )
      onChanged()
    } catch (error) {
      onError(error)
    }
  }

  const persistOrder = async (next: DbField[]): Promise<void> => {
    setLocalFields(next)
    try {
      await onReorderFields(next.map((field) => field.id))
    } catch (error) {
      setLocalFields(null)
      onError(error)
    }
  }

  const onDragEnd = async (event: DragEndEvent): Promise<void> => {
    const { active, over } = event
    if (over === null || active.id === over.id) return
    const next = moveFieldOrder(orderedFields, String(active.id), String(over.id))
    if (next !== null) await persistOrder(next)
  }

  const move = (fieldId: string, delta: -1 | 1): void => {
    const index = orderedFields.findIndex((field) => field.id === fieldId)
    const neighbour = orderedFields[index + delta]
    if (index < 0 || neighbour === undefined) return
    const next = moveFieldOrder(orderedFields, fieldId, neighbour.id)
    if (next !== null) void persistOrder(next)
  }

  const headerGroups = table.getHeaderGroups()
  const rows = table.getRowModel().rows

  if (records.length === 0) {
    return (
      <EmptyState
        compact
        icon={iconFor('note')}
        title={t('pages.notes.db_no_records')}
        headingLevel={3}
      />
    )
  }

  return (
    <div className={styles.tableWrap}>
      <DndContext sensors={sensors} onDragEnd={(event) => void onDragEnd(event)}>
        <table className={styles.recordTable} aria-label={t('pages.notes.db_fields')}>
          <thead>
            {headerGroups.map((group) => (
              <tr key={group.id}>
                <th scope="col" className={styles.th}>
                  {t('pages.notes.col_title')}
                </th>
                <SortableContext
                  items={orderedFields.map((candidate) => candidate.id)}
                  strategy={horizontalListSortingStrategy}
                >
                  {group.headers
                    .filter((header) => header.column.id !== '__title__')
                    .map((header) => {
                      const id = header.column.id
                      const field = orderedFields.find((candidate) => candidate.id === id)
                      const index = orderedFields.findIndex((candidate) => candidate.id === id)
                      return (
                        <SortableHeader
                          key={id}
                          id={id}
                          label={field?.name ?? id}
                          readOnly={readOnly}
                          onMoveLeft={index > 0 ? () => move(id, -1) : undefined}
                          onMoveRight={
                            index >= 0 && index < orderedFields.length - 1 ? () => move(id, 1) : undefined
                          }
                        />
                      )
                    })}
                </SortableContext>
                <th scope="col" className={styles.th}>
                  {t('pages.notes.col_actions')}
                </th>
              </tr>
            ))}
          </thead>
          <tbody>
            {rows.map((row) => {
              const record = row.original
              return (
                <tr key={record.id} className={styles.recordRow}>
                  <td className={styles.td}>
                    {record.noteId === null ? (
                      <span>—</span>
                    ) : (
                      <button
                        type="button"
                        className={styles.recordTitle}
                        onClick={() => {
                          const href = routeHref('note', { projectId, entityId: record.noteId ?? '' })
                          if (href !== undefined) navigate(href)
                        }}
                      >
                        {record.title === '' ? t('pages.notes.untitled') : record.title}
                      </button>
                    )}
                  </td>
                  {orderedFields.map((field) => (
                    <td key={field.id} className={styles.td}>
                      <RecordCell
                        field={field}
                        record={record}
                        tags={tags}
                        projectId={projectId}
                        readOnly={readOnly}
                        onCommit={(target, value) => void commit(record, target, value)}
                      />
                    </td>
                  ))}
                  <td className={styles.td}>
                    <IconButton
                      icon={iconFor('trash')}
                      label={t('pages.notes.db_delete_record')}
                      disabled={readOnly}
                      onClick={() => onDelete(record)}
                    />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </DndContext>
    </div>
  )
}