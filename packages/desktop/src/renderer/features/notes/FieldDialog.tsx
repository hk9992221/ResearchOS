import { useEffect, useState } from 'react'

import type { DbField, FieldPatch } from '@researchos/shared'

import { Button, IconButton, Modal, SelectField, TextInput } from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import styles from './notes.module.css'

const RELATION_TARGETS = ['paper', 'note', 'idea', 'experiment', 'db_record'] as const

export interface FieldDialogProps {
  readonly open: boolean
  readonly mode: 'create' | 'edit'
  readonly field?: DbField
  readonly fieldTypes: ReadonlyArray<{ value: string; label: string }>
  readonly busy: boolean
  readonly onClose: () => void
  readonly onSave: (input: FieldPatch & { type?: DbField['type'] }, expectedUpdatedAt?: string) => Promise<void>
}

interface OptionDraft {
  readonly id?: string
  readonly name: string
  readonly color: string
}

function existingOptions(field: DbField | undefined): OptionDraft[] {
  if (field === undefined) return []
  const config = field.config
  if (config === null || typeof config !== 'object' || Array.isArray(config)) return []
  const options = config['options']
  if (!Array.isArray(options)) return []
  const out: OptionDraft[] = []
  for (const entry of options) {
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) continue
    const name = 'name' in entry && typeof entry.name === 'string' ? entry.name : ''
    const color = 'color' in entry && typeof entry.color === 'string' ? entry.color : ''
    const id = 'id' in entry && typeof entry.id === 'string' ? entry.id : undefined
    if (name !== '') out.push({ id, name, color })
  }
  return out
}

function existingTargets(field: DbField | undefined): string[] {
  if (field === undefined) return [...RELATION_TARGETS]
  const config = field.config
  if (config === null || typeof config !== 'object' || Array.isArray(config)) return [...RELATION_TARGETS]
  const targets = config['targets']
  return Array.isArray(targets) ? targets.filter((entry): entry is string => typeof entry === 'string') : [...RELATION_TARGETS]
}

/**
 * FieldDialog creates a field or edits its name/options/targets. A field keeps
 * its type: changing it would reinterpret stored values, which the contract
 * does not allow (§3.4 optimistic edits).
 */
export function FieldDialog({
  open,
  mode,
  field,
  fieldTypes,
  busy,
  onClose,
  onSave
}: FieldDialogProps): JSX.Element {
  const t = useT()
  const [name, setName] = useState('')
  const [type, setType] = useState<DbField['type']>('text')
  const [role, setRole] = useState<'title' | 'plain'>('plain')
  const [options, setOptions] = useState<OptionDraft[]>([])
  const [targets, setTargets] = useState<string[]>([...RELATION_TARGETS])

  useEffect(() => {
    if (!open) return
    setName(field?.name ?? '')
    setType(field?.type ?? 'text')
    setRole(field?.role ?? 'plain')
    setOptions(existingOptions(field))
    setTargets(existingTargets(field))
  }, [open, field])

  const needsOptions = type === 'select' || type === 'multi_select'
  const needsTargets = type === 'relation'

  const submit = (): void => {
    const patch: FieldPatch & { type?: DbField['type'] } = {
      name: name.trim(),
      role,
      ...(mode === 'create' ? { type } : {}),
      ...(needsOptions
        ? {
            options: options
              .filter((option) => option.name.trim() !== '')
              .map((option) => ({
                ...(option.id === undefined ? {} : { id: option.id }),
                name: option.name.trim(),
                color: option.color
              }))
          }
        : {}),
      ...(needsTargets ? { targets } : {})
    }
    void onSave(patch, mode === 'edit' ? field?.updatedAt : undefined)
  }

  return (
    <Modal
      open={open}
      title={mode === 'edit' ? t('pages.notes.db_edit_field') : t('pages.notes.db_add_field')}
      onClose={onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('pages.notes.btn_cancel')}
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy || name.trim() === ''}>
            {t('pages.notes.db_save')}
          </Button>
        </>
      }
    >
      <TextInput
        label={t('pages.notes.db_field_name')}
        value={name}
        onChange={setName}
        error={name.trim() === '' ? t('pages.notes.db_field_name_required') : undefined}
        autoFocus
      />
      <SelectField
        label={t('pages.notes.db_field_type')}
        value={type}
        options={fieldTypes}
        onChange={(value) => {
          if (mode === 'edit') return
          setType(value as DbField['type'])
        }}
        disabled={mode === 'edit'}
        hint={mode === 'edit' ? t('pages.notes.db_type_fixed') : undefined}
      />
      <SelectField
        label={t('pages.notes.db_field_role')}
        value={role}
        options={[
          { value: 'plain', label: t('pages.notes.db_role_plain') },
          { value: 'title', label: t('pages.notes.db_role_title') }
        ]}
        onChange={(value) => setRole(value === 'title' ? 'title' : 'plain')}
      />
      {needsOptions && (
        <div className={styles.optionList}>
          {options.map((option, index) => (
            <div key={index} className={styles.optionRow}>
              <TextInput
                label={t('pages.notes.db_option_name')}
                value={option.name}
                onChange={(value) =>
                  setOptions(options.map((entry, position) => (position === index ? { ...entry, name: value } : entry)))
                }
              />
              <input
                type="color"
                className={styles.colorInput}
                aria-label={t('pages.notes.db_option_color')}
                value={option.color === '' ? '#888888' : option.color}
                onChange={(event) =>
                  setOptions(
                    options.map((entry, position) =>
                      position === index ? { ...entry, color: event.target.value } : entry
                    )
                  )
                }
              />
              <IconButton
                icon={iconFor('close')}
                label={t('pages.notes.db_remove_condition')}
                onClick={() => setOptions(options.filter((_entry, position) => position !== index))}
              />
            </div>
          ))}
          <Button variant="ghost" icon={iconFor('plus')} onClick={() => setOptions([...options, { name: '', color: '' }])}>
            {t('pages.notes.db_add_option')}
          </Button>
        </div>
      )}
      {needsTargets && (
        <div className={styles.multiPick} role="group" aria-label={t('pages.notes.db_relation_targets')}>
          {RELATION_TARGETS.map((target) => {
            const active = targets.includes(target)
            return (
              <button
                key={target}
                type="button"
                className={[styles.tag, active ? styles.tagActive : ''].join(' ')}
                aria-pressed={active}
                onClick={() =>
                  setTargets(active ? targets.filter((entry) => entry !== target) : [...targets, target])
                }
              >
                {t(`pages.notes.rel_target_${target}`)}
              </button>
            )
          })}
        </div>
      )}
    </Modal>
  )
}