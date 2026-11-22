import type { ButtonHTMLAttributes } from 'react'
import type { LucideIcon } from 'lucide-react'

import styles from './IconButton.module.css'

export type IconButtonVariant = 'ghost' | 'secondary'
export type IconButtonSize = 'md' | 'sm' | 'nav'

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  readonly icon: LucideIcon
  /** Accessible name: icon buttons never ship without one. */
  readonly label: string
  /** Hover tooltip; defaults to the accessible name. */
  readonly tooltip?: string
  readonly variant?: IconButtonVariant
  readonly size?: IconButtonSize
  readonly active?: boolean
}

/** IconButton is an icon-only control with a name; the label is the tooltip. */
export function IconButton({
  icon: Icon,
  label,
  tooltip,
  variant = 'ghost',
  size = 'md',
  active = false,
  className,
  type = 'button',
  ...rest
}: IconButtonProps): JSX.Element {
  return (
    <button
      {...rest}
      type={type}
      aria-label={label}
      title={tooltip ?? label}
      aria-pressed={active ? true : undefined}
      className={[
        styles.button,
        styles[variant],
        styles[size],
        active ? styles.active : '',
        className ?? ''
      ].join(' ')}
    >
      <Icon aria-hidden="true" strokeWidth={2} />
    </button>
  )
}