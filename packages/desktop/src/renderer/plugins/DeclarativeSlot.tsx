import { useId, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import type { PluginSlot, PluginSlotField } from '@researchos/shared'

import styles from './DeclarativeSlot.module.css'

/**
 * Declarative slot vocabulary rendered by the host (plan §3.7). No plugin HTML
 * reaches the host DOM: every node is a host component, markdown is sanitized
 * (raw HTML is never enabled), and actions are commands the host dispatches.
 */

export interface DeclarativeSlotProps {
  readonly slot: PluginSlot
  readonly pluginId: string
  readonly onCommand?: (command: string, values?: Record<string, string | boolean>) => void
}

export function DeclarativeSlot({ slot, pluginId, onCommand }: DeclarativeSlotProps): JSX.Element | null {
  switch (slot.kind) {
    case 'text':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'text'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          {slot.body !== undefined && <p className={styles.text}>{slot.body}</p>}
        </section>
      )
    case 'markdown':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'markdown'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          <div className={styles.markdown}>
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{slot.body ?? ''}</ReactMarkdown>
          </div>
        </section>
      )
    case 'list':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'list'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          <ul className={styles.list}>
            {(slot.items ?? []).map((item, index) => (
              <li key={`${index}-${item}`}>{item}</li>
            ))}
          </ul>
        </section>
      )
    case 'table':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'table'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  {(slot.columns ?? []).map((column) => (
                    <th key={column} scope="col">
                      {column}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(slot.rows ?? []).map((row, rowIndex) => (
                  <tr key={rowIndex}>
                    {row.map((cell, cellIndex) => (
                      <td key={cellIndex}>{cell}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )
    case 'form':
      return (
        <DeclarativeForm
          slot={slot}
          onCommand={onCommand}
        />
      )
    case 'button':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'button'}>
          <button
            type="button"
            className={styles.button}
            disabled={slot.command === undefined}
            onClick={() => {
              if (slot.command !== undefined) onCommand?.(slot.command)
            }}
          >
            {slot.title ?? slot.body ?? 'Run'}
          </button>
        </section>
      )
    case 'chart':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'chart'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          <Chart points={slot.points ?? []} />
        </section>
      )
    case 'iframe':
      return (
        <section className={styles.slot} data-slot={slot.id ?? 'iframe'}>
          {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
          {slot.url !== undefined && slot.url.startsWith(`plugin://${pluginId}/`) ? (
            <iframe
              className={styles.frame}
              src={slot.url}
              title={slot.title ?? `${pluginId} content`}
              sandbox=""
              referrerPolicy="no-referrer"
              height={slot.height ?? 240}
            />
          ) : (
            <p className={styles.invalid}>This iframe does not belong to {pluginId}.</p>
          )}
        </section>
      )
    default:
      return null
  }
}

function DeclarativeForm({
  slot,
  onCommand
}: {
  readonly slot: PluginSlot
  readonly onCommand?: DeclarativeSlotProps['onCommand']
}): JSX.Element {
  const formId = useId()
  const fields = slot.fields ?? []
  const [values, setValues] = useState<Record<string, string | boolean>>(() => initialValues(fields))
  return (
    <section className={styles.slot} data-slot={slot.id ?? 'form'}>
      {slot.title !== undefined && <h3 className={styles.title}>{slot.title}</h3>}
      <form
        className={styles.form}
        onSubmit={(event) => {
          event.preventDefault()
          if (slot.submitCommand !== undefined) onCommand?.(slot.submitCommand, values)
        }}
      >
        {fields.map((field) => (
          <label key={field.name} className={styles.field} htmlFor={`${formId}-${field.name}`}>
            <span>{field.label}</span>
            {renderField(field, `${formId}-${field.name}`, values[field.name], (value) =>
              setValues((current) => ({ ...current, [field.name]: value }))
            )}
          </label>
        ))}
        <button type="submit" className={styles.button} disabled={slot.submitCommand === undefined}>
          {slot.title ?? 'Submit'}
        </button>
      </form>
    </section>
  )
}

function initialValues(fields: PluginSlotField[]): Record<string, string | boolean> {
  const values: Record<string, string | boolean> = {}
  for (const field of fields) {
    const fallback = field.type === 'checkbox' ? false : ''
    values[field.name] =
      typeof field.value === 'number' ? String(field.value) : (field.value ?? fallback)
  }
  return values
}

function renderField(
  field: PluginSlotField,
  id: string,
  value: string | boolean | undefined,
  onChange: (value: string | boolean) => void
): JSX.Element {
  if (field.type === 'checkbox') {
    return (
      <input
        id={id}
        type="checkbox"
        checked={value === true}
        onChange={(event) => onChange(event.target.checked)}
      />
    )
  }
  if (field.type === 'textarea') {
    return (
      <textarea
        id={id}
        value={typeof value === 'string' ? value : ''}
        placeholder={field.placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
    )
  }
  return (
    <input
      id={id}
      type={field.type === 'number' ? 'number' : 'text'}
      value={typeof value === 'string' ? value : ''}
      placeholder={field.placeholder}
      onChange={(event) => onChange(event.target.value)}
    />
  )
}

function Chart({ points }: { readonly points: Array<{ label: string; value: number }> }): JSX.Element {
  if (points.length === 0) return <p className={styles.text}>No data.</p>
  const maximum = Math.max(...points.map((point) => Math.abs(point.value)), 1)
  return (
    <div className={styles.chart} role="img" aria-label="Plugin chart">
      {points.map((point) => (
        <div key={point.label} className={styles.barRow}>
          <span className={styles.barLabel}>{point.label}</span>
          <span className={styles.barTrack}>
            <span
              className={styles.bar}
              style={{ width: `${Math.round((Math.abs(point.value) / maximum) * 100)}%` }}
            />
          </span>
          <span className={styles.barValue}>{point.value}</span>
        </div>
      ))}
    </div>
  )
}