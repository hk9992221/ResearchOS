import { Button, IconButton, TextInput } from '../../design/components'
import { useT } from '../../i18n'
import { Trash2, Plus } from 'lucide-react'

import type { ParamRow } from './model'
import styles from './experiments.module.css'

export interface ParamsEditorProps {
  readonly rows: readonly ParamRow[]
  readonly onChange: (rows: ParamRow[]) => void
  readonly disabled?: boolean
  readonly error?: 'key_required' | 'duplicate_key' | 'title_required' | null
}

/**
 * Parameters are a string map (`key=value`), entered by hand. The editor adds
 * and removes rows; validation lives in `parseParamRows`, so the dialog and the
 * run page agree on what a saveable map is.
 */
export function ParamsEditor({ rows, onChange, disabled = false, error = null }: ParamsEditorProps): JSX.Element {
  const t = useT()
  const replace = (index: number, patch: Partial<ParamRow>): void => {
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)))
  }

  return (
    <div className={styles.paramGrid}>
      {rows.map((row, index) => (
        <div key={index} className={styles.paramRow}>
          <TextInput
            label={t('pages.experiments.run_param_key')}
            value={row.key}
            onChange={(value) => replace(index, { key: value })}
            disabled={disabled}
            placeholder="lr"
          />
          <TextInput
            label={t('pages.experiments.run_param_value')}
            value={row.value}
            onChange={(value) => replace(index, { value })}
            disabled={disabled}
            placeholder="0.001"
          />
          <IconButton
            className={styles.paramRemove}
            label={t('pages.experiments.run_param_remove')}
            icon={Trash2}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
            disabled={disabled}
          />
        </div>
      ))}
      <div className={styles.actions}>
        <Button
          variant="ghost"
          size="sm"
          icon={Plus}
          onClick={() => onChange([...rows, { key: '', value: '' }])}
          disabled={disabled}
        >
          {t('pages.experiments.run_param_add')}
        </Button>
      </div>
      <p className={styles.reason}>{t('pages.experiments.run_params_hint')}</p>
      {error === 'key_required' && (
        <p className={styles.errorText} role="alert">
          {t('pages.experiments.params_key_required')}
        </p>
      )}
      {error === 'duplicate_key' && (
        <p className={styles.errorText} role="alert">
          {t('pages.experiments.params_duplicate')}
        </p>
      )}
      {error === 'title_required' && (
        <p className={styles.errorText} role="alert">
          {t('pages.experiments.title_required')}
        </p>
      )}
    </div>
  )
}