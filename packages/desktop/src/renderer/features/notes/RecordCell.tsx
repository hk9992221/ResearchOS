import { useState } from 'react'

import type { DbField, DbRecord, JsonObject } from '@researchos/shared'

import { useT, type Translations } from '../../i18n'
import { fetchReferenceCandidates } from './reference'
import styles from './notes.module.css'

export interface CellOption {
  readonly id: string
  readonly name: string
  readonly color?: string
}

/** optionsOf reads the canonical `{options:[…]}` field config. */
export function optionsOf(field: DbField): CellOption[] {
  const config = field.config
  if (config === null || typeof config !== 'object' || Array.isArray(config)) return []
  const options = config['options']
  if (!Array.isArray(options)) return []
  const out: CellOption[] = []
  for (const entry of options) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
    const id = 'id' in entry && typeof entry.id === 'string' ? entry.id : ''
    const name = 'name' in entry && typeof entry.name === 'string' ? entry.name : ''
    const color = 'color' in entry && typeof entry.color === 'string' ? entry.color : undefined
    if (id !== '') out.push({ id, name, color })
  }
  return out
}

export function targetsOf(field: DbField): string[] {
  const config = field.config
  if (config === null || typeof config !== 'object' || Array.isArray(config)) return []
  const targets = config['targets']
  return Array.isArray(targets) ? targets.filter((entry): entry is string => typeof entry === 'string') : []
}

function isOptionList(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((entry) => typeof entry === 'string')
}

export function cellText(field: DbField, value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  switch (field.type) {
    case 'checkbox':
      return value === true ? 'checked' : 'unchecked'
    case 'select': {
      const option = optionsOf(field).find((candidate) => candidate.id === value)
      return option?.name ?? String(value)
    }
    case 'multi_select': {
      if (!isOptionList(value)) return '—'
      const options = optionsOf(field)
      const names = value.map((id) => options.find((option) => option.id === id)?.name ?? id)
      return names.length === 0 ? '—' : names.join(', ')
    }
    case 'relation': {
      if (!Array.isArray(value)) return '—'
      const count = value.filter(
        (entry) => entry !== null && typeof entry === 'object' && !Array.isArray(entry)
      ).length
      return count === 0 ? '—' : `${count}`
    }
    default:
      return String(value)
  }
}

export interface RecordCellProps {
  readonly field: DbField
  readonly record: DbRecord
  readonly tags: ReadonlyArray<{ id: string; name: string }>
  readonly projectId: string
  readonly readOnly: boolean
  readonly onCommit: (field: DbField, value: unknown) => void
}

/** RecordCell is the per-type inline editor of one record value. */
export function RecordCell({ field, record, tags, projectId, readOnly, onCommit }: RecordCellProps): JSX.Element {
  const t = useT()
  const value = (record.data as JsonObject)[field.id]

  if (field.type === 'checkbox') {
    const checked = value === true
    return (
      <input
        type="checkbox"
        className={styles.cellCheckbox}
        aria-label={field.name}
        checked={checked}
        disabled={readOnly}
        onChange={() => onCommit(field, !checked)}
      />
    )
  }

  if (readOnly) {
    return <span className={styles.cellText}>{renderValue(t, field, value, tags)}</span>
  }

  switch (field.type) {
    case 'select':
      return (
        <SingleSelectCell
          fieldName={field.name}
          options={optionsOf(field)}
          value={typeof value === 'string' ? value : ''}
          onCommit={(next) => onCommit(field, next)}
        />
      )
    case 'multi_select':
      return (
        <OptionCell
          field={field}
          fieldName={field.name}
          options={optionsOf(field)}
          selected={isOptionList(value) ? value : []}
          onCommit={(next) => onCommit(field, next)}
        />
      )
    case 'tags':
      return (
        <OptionCell
          field={field}
          fieldName={field.name}
          options={tags.map((tag) => ({ id: tag.id, name: tag.name }))}
          selected={isOptionList(value) ? value : []}
          onCommit={(next) => onCommit(field, next)}
        />
      )
    case 'relation':
      return (
        <RelationCell
          field={field}
          projectId={projectId}
          selected={Array.isArray(value) ? value : []}
          onCommit={(next) => onCommit(field, next)}
        />
      )
    case 'date':
      return (
        <input
          type="date"
          className={styles.cellInput}
          aria-label={field.name}
          value={typeof value === 'string' ? value : ''}
          onChange={(event) => onCommit(field, event.target.value === '' ? null : event.target.value)}
        />
      )
    case 'number':
      return (
        <NumberCell
          fieldName={field.name}
          value={typeof value === 'number' ? String(value) : ''}
          onCommit={(next) => onCommit(field, next)}
        />
      )
    default:
      return (
        <TextCell
          fieldName={field.name}
          value={typeof value === 'string' ? value : ''}
          type={field.type === 'url' ? 'url' : 'text'}
          onCommit={(next) => onCommit(field, next === '' ? null : next)}
        />
      )
  }
}

function renderValue(
  t: Translations,
  field: DbField,
  value: unknown,
  tags: ReadonlyArray<{ id: string; name: string }>
): JSX.Element {
  if (field.type === 'checkbox') {
    return <>{t(value === true ? 'pages.notes.db_true' : 'pages.notes.db_false')}</>
  }
  if (field.type === 'select') {
    const option = optionsOf(field).find((candidate) => candidate.id === value)
    return <>{option?.name ?? cellText(field, value)}</>
  }
  if (field.type === 'multi_select') {
    if (!isOptionList(value)) return <>—</>
    const options = optionsOf(field)
    return (
      <span className={styles.chipRow}>
        {value.map((id) => {
          const option = options.find((candidate) => candidate.id === id)
          return (
            <span key={id} className={styles.valueChip}>
              {option?.color !== undefined && option.color !== '' && (
                <span className={styles.colorDot} style={{ backgroundColor: option.color }} />
              )}
              {option?.name ?? id}
            </span>
          )
        })}
      </span>
    )
  }
  if (field.type === 'tags') {
    if (!isOptionList(value)) return <>—</>
    return (
      <span className={styles.chipRow}>
        {value.map((id) => (
          <span key={id} className={styles.valueChip}>
            {tags.find((tag) => tag.id === id)?.name ?? id}
          </span>
        ))}
      </span>
    )
  }
  if (field.type === 'url' && typeof value === 'string' && value !== '') {
    return (
      <a href={value} target="_blank" rel="noreferrer" onClick={(event) => event.stopPropagation()}>
        {value}
      </a>
    )
  }
  return <>{cellText(field, value)}</>
}

function TextCell({
  fieldName,
  value,
  type,
  onCommit
}: {
  readonly fieldName: string
  readonly value: string
  readonly type: 'text' | 'url'
  readonly onCommit: (next: string) => void
}): JSX.Element {
  const [draft, setDraft] = useState<string | null>(null)
  if (draft === null) {
    return (
      <button type="button" className={styles.cellOpen} onClick={() => setDraft(value)}>
        {value === '' ? '—' : value}
      </button>
    )
  }
  const commit = (): void => {
    onCommit(draft)
    setDraft(null)
  }
  return (
    <input
      autoFocus
      type={type}
      className={styles.cellInput}
      aria-label={fieldName}
      value={draft}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault()
          commit()
        }
        if (event.key === 'Escape') setDraft(null)
      }}
    />
  )
}

function SingleSelectCell({
  fieldName,
  options,
  value,
  onCommit
}: {
  readonly fieldName: string
  readonly options: readonly CellOption[]
  readonly value: string
  readonly onCommit: (next: string | null) => void
}): JSX.Element {
  const t = useT()
  const [open, setOpen] = useState(false)
  const current = options.find((option) => option.id === value)
  if (!open) {
    return (
      <button type="button" className={styles.cellOpen} aria-label={fieldName} onClick={() => setOpen(true)}>
        {current === undefined ? (
          '—'
        ) : (
          <span className={styles.chipRow}>
            {current.color !== undefined && current.color !== '' && (
              <span className={styles.colorDot} data-color={current.color} style={{ backgroundColor: current.color }} />
            )}
            {current.name}
          </span>
        )}
      </button>
    )
  }
  return (
    <div className={styles.multiEditor} role="group" aria-label={fieldName}>
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          className={[styles.tag, option.id === value ? styles.tagActive : ''].join(' ')}
          aria-pressed={option.id === value}
          onClick={() => {
            onCommit(option.id)
            setOpen(false)
          }}
        >
          {option.color !== undefined && option.color !== '' && (
            <span className={styles.colorDot} data-color={option.color} style={{ backgroundColor: option.color }} />
          )}
          {option.name}
        </button>
      ))}
      <div className={styles.multiActions}>
        {value !== '' && (
          <button
            type="button"
            className={styles.miniButton}
            aria-label={t('pages.notes.db_clear')}
            onClick={() => {
              onCommit(null)
              setOpen(false)
            }}
          >
            ×
          </button>
        )}
        <button
          type="button"
          className={styles.miniButton}
          aria-label={t('pages.notes.btn_cancel')}
          onClick={() => setOpen(false)}
        >
          ×
        </button>
      </div>
    </div>
  )
}

function NumberCell({
  fieldName,
  value,
  onCommit
}: {
  readonly fieldName: string
  readonly value: string
  readonly onCommit: (next: number | null) => void
}): JSX.Element {
  const [draft, setDraft] = useState<string | null>(null)
  if (draft === null) {
    return (
      <button type="button" className={styles.cellOpen} onClick={() => setDraft(value)}>
        {value === '' ? '—' : value}
      </button>
    )
  }
  const commit = (): void => {
    const parsed = Number(draft)
    onCommit(draft.trim() === '' || !Number.isFinite(parsed) ? null : parsed)
    setDraft(null)
  }
  return (
    <input
      autoFocus
      type="number"
      className={styles.cellInput}
      aria-label={fieldName}
      value={draft}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault()
          commit()
        }
        if (event.key === 'Escape') setDraft(null)
      }}
    />
  )
}

function OptionCell({
  fieldName,
  options,
  selected,
  onCommit
}: {
  readonly field: DbField
  readonly fieldName: string
  readonly options: readonly CellOption[]
  readonly selected: readonly string[]
  readonly onCommit: (next: string[]) => void
}): JSX.Element {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<readonly string[]>(selected)

  if (!open) {
    const names = selected.map((id) => options.find((option) => option.id === id)?.name ?? id)
    return (
      <button
        type="button"
        className={styles.cellOpen}
        aria-label={fieldName}
        onClick={() => {
          setDraft(selected)
          setOpen(true)
        }}
      >
        {names.length === 0 ? '—' : names.join(', ')}
      </button>
    )
  }

  return (
    <div className={styles.multiEditor} role="group" aria-label={fieldName}>
      {options.length === 0 && <span>—</span>}
      {options.map((option) => {
        const active = draft.includes(option.id)
        return (
          <button
            key={option.id}
            type="button"
            className={[styles.tag, active ? styles.tagActive : ''].join(' ')}
            aria-pressed={active}
            onClick={() =>
              setDraft(active ? draft.filter((id) => id !== option.id) : [...draft, option.id])
            }
          >
            {option.color !== undefined && option.color !== '' && (
              <span className={styles.colorDot} style={{ backgroundColor: option.color }} />
            )}
            {option.name}
          </button>
        )
      })}
      <div className={styles.multiActions}>
        <button
          type="button"
          className={styles.miniButton}
          aria-label={t('pages.notes.db_save')}
          onClick={() => {
            onCommit([...draft])
            setOpen(false)
          }}
        >
          ✓
        </button>
        <button
          type="button"
          className={styles.miniButton}
          aria-label={t('pages.notes.btn_cancel')}
          onClick={() => setOpen(false)}
        >
          ×
        </button>
      </div>
    </div>
  )
}

interface RelationRef {
  readonly type: string
  readonly id: string
}

function relationList(value: unknown[]): RelationRef[] {
  const refs: RelationRef[] = []
  for (const entry of value) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
    const type = 'type' in entry && typeof entry.type === 'string' ? entry.type : ''
    const id = 'id' in entry && typeof entry.id === 'string' ? entry.id : ''
    if (type !== '' && id !== '') refs.push({ type, id })
  }
  return refs
}

function RelationCell({
  field,
  projectId,
  selected,
  onCommit
}: {
  readonly field: DbField
  readonly projectId: string
  readonly selected: unknown[]
  readonly onCommit: (next: RelationRef[]) => void
}): JSX.Element {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [found, setFound] = useState<ReadonlyArray<{ type: string; id: string; title: string }>>([])
  const refs = relationList(selected)
  const targets = targetsOf(field)

  const search = async (text: string): Promise<void> => {
    setQuery(text)
    if (text.trim() === '') {
      setFound([])
      return
    }
    const candidates = await fetchReferenceCandidates(projectId, text.trim(), 6)
    setFound(candidates.filter((candidate) => targets.includes(candidate.type)))
  }

  if (!open) {
    return (
      <button
        type="button"
        className={styles.cellOpen}
        aria-label={field.name}
        onClick={() => setOpen(true)}
      >
        {refs.length === 0 ? '—' : `${refs.length}`}
      </button>
    )
  }

  return (
    <div className={styles.relationEditor}>
      {refs.length > 0 && (
        <div className={styles.chipRow}>
          {refs.map((ref) => (
            <span key={`${ref.type}:${ref.id}`} className={styles.valueChip}>
              {ref.type}/{ref.id.slice(0, 8)}
              <button
                type="button"
                className={styles.miniButton}
                aria-label={t('pages.notes.db_remove_condition')}
                onClick={() =>
                  onCommit(refs.filter((entry) => !(entry.type === ref.type && entry.id === ref.id)))
                }
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <input
        autoFocus
        className={styles.cellInput}
        aria-label={field.name}
        placeholder={t('pages.notes.ref_hint')}
        value={query}
        onChange={(event) => void search(event.target.value)}
      />
      {found.map((candidate) => (
        <button
          key={`${candidate.type}:${candidate.id}`}
          type="button"
          className={styles.pickRow}
          onClick={() => {
            if (!refs.some((ref) => ref.type === candidate.type && ref.id === candidate.id)) {
              onCommit([...refs, { type: candidate.type, id: candidate.id }])
            }
            setQuery('')
            setFound([])
          }}
        >
          {candidate.title}
        </button>
      ))}
      <div className={styles.multiActions}>
        <button
          type="button"
          className={styles.miniButton}
          aria-label={t('pages.notes.db_save')}
          onClick={() => setOpen(false)}
        >
          ✓
        </button>
        {refs.length > 0 && (
          <button
            type="button"
            className={styles.miniButton}
            aria-label={t('pages.notes.db_clear')}
            onClick={() => {
              onCommit([])
              setOpen(false)
            }}
          >
            ×
          </button>
        )}
      </div>
    </div>
  )
}