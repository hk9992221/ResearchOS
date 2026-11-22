import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

import styles from './EmptyState.module.css'

export interface EmptyStateProps {
  readonly title: string
  /** Only when it changes a decision; the layout explains the rest (§5). */
  readonly description?: string
  readonly icon?: LucideIcon
  readonly action?: ReactNode
  readonly headingLevel?: 2 | 3
  readonly compact?: boolean
}

/**
 * EmptyState is the honest stand-in for a page or panel with nothing to show:
 * a title, an optional reason that changes what the user does next, and at
 * most one action. No silent blank screens (plan §3.4).
 */
export function EmptyState({
  title,
  description,
  icon: Icon,
  action,
  headingLevel = 2,
  compact = false
}: EmptyStateProps): JSX.Element {
  const Heading = headingLevel === 3 ? 'h3' : 'h2'
  return (
    <section className={compact ? styles.compact : styles.empty}>
      {Icon !== undefined && (
        <Icon aria-hidden="true" className={styles.icon} strokeWidth={1.5} />
      )}
      <Heading className={styles.title}>{title}</Heading>
      {description !== undefined && <p className={styles.description}>{description}</p>}
      {action !== undefined && <div className={styles.action}>{action}</div>}
    </section>
  )
}