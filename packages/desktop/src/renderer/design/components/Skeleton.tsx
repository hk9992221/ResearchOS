import styles from './Skeleton.module.css'

export interface SkeletonProps {
  /** Number of placeholder lines. */
  readonly rows?: number
  /** Render as a block (a table body, a chart area) instead of lines. */
  readonly block?: boolean
  readonly height?: string
}

/**
 * Skeleton is the loading placeholder of lists, tables and panels. It is
 * decorative: the container that loads announces its own busy state, so the
 * skeleton never repeats text a screen reader would read.
 */
export function Skeleton({ rows = 3, block = false, height }: SkeletonProps): JSX.Element {
  if (block) {
    return (
      <div aria-hidden="true" className={styles.block} style={height === undefined ? undefined : { height }} />
    )
  }
  return (
    <div aria-hidden="true" className={styles.lines}>
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className={styles.line} />
      ))}
    </div>
  )
}