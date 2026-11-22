import {
  Children,
  useId,
  useRef,
  useState,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type SelectHTMLAttributes
} from 'react'
import { Check, Copy } from 'lucide-react'

import { IconButton } from './IconButton'
import styles from './Page.module.css'

/**
 * Page kit for the system pages (settings, trash, consistency, diagnostics,
 * export, wizard). It is the same contract as the Go `components.UI` page
 * primitives: padding, page header, toolbar row, cards, two-column rows,
 * tables, banners and form fields — all painted from design tokens.
 *
 * These are layout primitives, not a second design system: colors, radii,
 * spacing and type sizes come from tokens.css only.
 */

export function Page({ children }: { readonly children: ReactNode }): JSX.Element {
  return <div className={styles.page}>{children}</div>
}

export function PageHeader({
  title,
  description,
  actions
}: {
  /** A page title may carry inline chrome (a status badge). */
  readonly title: ReactNode
  readonly description?: string
  readonly actions?: ReactNode
}): JSX.Element {
  return (
    <header className={styles.header}>
      <div className={styles.heading}>
        <h1 className={styles.title}>{title}</h1>
        {description !== undefined && <p className={styles.description}>{description}</p>}
      </div>
      {actions !== undefined && <div className={styles.actions}>{actions}</div>}
    </header>
  )
}

export function Toolbar({
  children,
  label
}: {
  readonly children: ReactNode
  readonly label: string
}): JSX.Element {
  return (
    <div className={styles.toolbar} role="toolbar" aria-label={label}>
      {children}
    </div>
  )
}

export function Card({
  title,
  actions,
  collapsible = false,
  children
}: {
  readonly title?: string
  readonly actions?: ReactNode
  readonly collapsible?: boolean
  readonly children: ReactNode
}): JSX.Element {
  const [open, setOpen] = useState(true)
  return (
    <section className={styles.card}>
      {(title !== undefined || actions !== undefined) && (
        <header className={collapsible && !open ? `${styles.cardHeader} ${styles.cardHeaderClosed}` : styles.cardHeader}>
          {title !== undefined &&
            (collapsible ? (
              <button
                type="button"
                className={styles.cardToggle}
                aria-expanded={open}
                onClick={() => setOpen((value) => !value)}
              >
                <span className={styles.cardChevron} data-open={open} aria-hidden />
                <span className={styles.cardTitle}>{title}</span>
              </button>
            ) : (
              <h2 className={styles.cardTitle}>{title}</h2>
            ))}
          {actions !== undefined && <div className={styles.cardActions}>{actions}</div>}
        </header>
      )}
      {(!collapsible || open) && <div className={styles.cardBody}>{children}</div>}
    </section>
  )
}

export function CardGrid({ children }: { readonly children: ReactNode }): JSX.Element {
  return <div className={styles.grid}>{children}</div>
}

export interface TabItem {
  readonly id: string
  readonly label: string
}

export function Tabs({
  label,
  items,
  value,
  onChange
}: {
  readonly label: string
  readonly items: readonly TabItem[]
  readonly value: string
  readonly onChange: (id: string) => void
}): JSX.Element {
  const refs = useRef(new Map<string, HTMLButtonElement>())
  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
    event.preventDefault()
    const index = items.findIndex((item) => item.id === value)
    if (index < 0) return
    const delta = event.key === 'ArrowRight' ? 1 : -1
    const next = items[(index + delta + items.length) % items.length]
    if (next === undefined) return
    onChange(next.id)
    refs.current.get(next.id)?.focus()
  }

  return (
    <div className={styles.tabs} role="tablist" aria-label={label} onKeyDown={onKeyDown}>
      {items.map((item) => (
        <button
          key={item.id}
          ref={(element) => {
            if (element === null) refs.current.delete(item.id)
            else refs.current.set(item.id, element)
          }}
          type="button"
          role="tab"
          aria-selected={item.id === value}
          tabIndex={item.id === value ? 0 : -1}
          className={[styles.tab, item.id === value ? styles.tabActive : ''].join(' ')}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  )
}

export type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'

export interface SegmentItem {
  readonly id: string
  readonly label: string
}

/** Pill segmented control for an immediate-choice setting (no submit step). */
export function Segmented({
  label,
  items,
  value,
  onChange
}: {
  readonly label: string
  readonly items: readonly SegmentItem[]
  readonly value: string
  readonly onChange: (id: string) => void
}): JSX.Element {
  return (
    <div className={styles.segmented} role="group" aria-label={label}>
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          aria-pressed={item.id === value}
          className={[styles.segment, item.id === value ? styles.segmentActive : ''].join(' ')}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  )
}

/** Monospace value with an inline copy affordance; success shows in place. */
export function CopyChip({
  value,
  copyLabel,
  copiedLabel
}: {
  readonly value: string
  readonly copyLabel: string
  readonly copiedLabel: string
}): JSX.Element {
  const [copied, setCopied] = useState(false)
  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard unavailable: the value stays visible for manual selection.
    }
  }
  return (
    <span className={styles.copyChip}>
      <code className={styles.copyChipValue}>{value}</code>
      <IconButton
        icon={copied ? Check : Copy}
        label={copied ? copiedLabel : copyLabel}
        size="sm"
        variant="ghost"
        onClick={() => void copy()}
      />
    </span>
  )
}

export function Banner({
  tone = 'info',
  title,
  children,
  action
}: {
  readonly tone?: Tone
  readonly title?: string
  readonly children?: ReactNode
  readonly action?: ReactNode
}): JSX.Element {
  return (
    <div className={[styles.banner, styles[`banner-${tone}`]].join(' ')} role={tone === 'danger' ? 'alert' : 'status'}>
      <div className={styles.bannerText}>
        {title !== undefined && <p className={styles.bannerTitle}>{title}</p>}
        {children !== undefined && <div className={styles.bannerBody}>{children}</div>}
      </div>
      {action !== undefined && <div className={styles.bannerAction}>{action}</div>}
    </div>
  )
}

export function ProgressBar({ value, label }: { readonly value: number; readonly label: string }): JSX.Element {
  const clamped = Math.min(100, Math.max(0, value))
  return (
    <div
      className={styles.progress}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(clamped)}
    >
      <span className={styles.progressBar} style={{ width: `${clamped}%` }} />
    </div>
  )
}

export function Badge({ tone = 'neutral', children }: { readonly tone?: Tone; readonly children: ReactNode }): JSX.Element {
  return <span className={[styles.badge, styles[`badge-${tone}`]].join(' ')}>{children}</span>
}

export function Rows({ children }: { readonly children: ReactNode }): JSX.Element {
  return <dl className={styles.rows}>{children}</dl>
}

export function Row({
  label,
  value,
  mono = false
}: {
  readonly label: string
  readonly value: ReactNode
  readonly mono?: boolean
}): JSX.Element {
  return (
    <div className={styles.row}>
      <dt className={styles.rowLabel}>{label}</dt>
      <dd className={[styles.rowValue, mono ? styles.mono : ''].join(' ')}>{value}</dd>
    </div>
  )
}

export function Table({
  label,
  columns,
  children,
  empty
}: {
  readonly label: string
  readonly columns: readonly string[]
  readonly children: ReactNode
  /** Rendered instead of the table when there are no rows. */
  readonly empty?: ReactNode
}): JSX.Element {
  const hasRows = Children.count(children) > 0
  if (!hasRows && empty !== undefined) return <>{empty}</>
  return (
    <table className={styles.table} aria-label={label}>
      <thead>
        <tr>
          {columns.map((column) => (
            <th key={column} scope="col" className={styles.th}>
              {column}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>{children}</tbody>
    </table>
  )
}

export function Mono({ children }: { readonly children: ReactNode }): JSX.Element {
  return <span className={styles.mono}>{children}</span>
}

export interface TextInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value'> {
  readonly label: string
  readonly value: string
  readonly onChange: (value: string) => void
  readonly hint?: string
  readonly error?: string
  readonly hideLabel?: boolean
}

export function TextInput({
  label,
  value,
  onChange,
  hint,
  error,
  hideLabel = false,
  id,
  ...rest
}: TextInputProps): JSX.Element {
  const generated = useId()
  const inputId = id ?? generated
  return (
    <div className={styles.field}>
      <label className={hideLabel ? styles.visuallyHidden : styles.label} htmlFor={inputId}>
        {label}
      </label>
      <input
        {...rest}
        id={inputId}
        className={[styles.input, error !== undefined ? styles.inputError : ''].join(' ')}
        value={value}
        aria-invalid={error !== undefined}
        aria-describedby={hint === undefined && error === undefined ? undefined : `${inputId}-hint`}
        onChange={(event) => onChange(event.target.value)}
      />
      {(hint !== undefined || error !== undefined) && (
        <p id={`${inputId}-hint`} className={error === undefined ? styles.hint : styles.error}>
          {error ?? hint}
        </p>
      )}
    </div>
  )
}

export interface SelectOption {
  readonly value: string
  readonly label: string
}

export interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'onChange' | 'value'> {
  readonly label: string
  readonly value: string
  readonly options: readonly SelectOption[]
  readonly onChange: (value: string) => void
  readonly hint?: string
  readonly error?: string
}

export function SelectField({
  label,
  value,
  options,
  onChange,
  hint,
  error,
  id,
  ...rest
}: SelectFieldProps): JSX.Element {
  const generated = useId()
  const selectId = id ?? generated
  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor={selectId}>
        {label}
      </label>
      <select
        {...rest}
        id={selectId}
        className={[styles.select, error !== undefined ? styles.inputError : ''].join(' ')}
        value={value}
        aria-invalid={error !== undefined}
        onChange={(event) => onChange(event.target.value)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      {(hint !== undefined || error !== undefined) && (
        <p className={error === undefined ? styles.hint : styles.error}>{error ?? hint}</p>
      )}
    </div>
  )
}

export function SwitchField({
  label,
  checked,
  onChange,
  description,
  disabled = false
}: {
  readonly label: string
  readonly checked: boolean
  readonly onChange: (checked: boolean) => void
  readonly description?: string
  readonly disabled?: boolean
}): JSX.Element {
  const id = useId()
  return (
    <div className={styles.switchRow}>
      <input
        id={id}
        type="checkbox"
        role="switch"
        className={styles.switch}
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <div className={styles.switchText}>
        <label className={styles.label} htmlFor={id}>
          {label}
        </label>
        {description !== undefined && <p className={styles.hint}>{description}</p>}
      </div>
    </div>
  )
}