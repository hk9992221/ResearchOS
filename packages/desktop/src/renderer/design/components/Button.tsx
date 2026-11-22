import type { ButtonHTMLAttributes, ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

import styles from './Button.module.css'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'
export type ButtonSize = 'md' | 'sm'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  readonly variant?: ButtonVariant
  readonly size?: ButtonSize
  readonly icon?: LucideIcon
  readonly children?: ReactNode
}

/**
 * Button is the one text button of the design system. Variants only choose
 * tokens; no caller passes a color.
 */
export function Button({
  variant = 'secondary',
  size = 'md',
  icon: Icon,
  children,
  className,
  type = 'button',
  ...rest
}: ButtonProps): JSX.Element {
  return (
    <button
      {...rest}
      type={type}
      className={[styles.button, styles[variant], styles[size], className ?? ''].join(' ')}
    >
      {Icon !== undefined && <Icon aria-hidden="true" className={styles.icon} strokeWidth={2} />}
      {children !== undefined && <span>{children}</span>}
    </button>
  )
}