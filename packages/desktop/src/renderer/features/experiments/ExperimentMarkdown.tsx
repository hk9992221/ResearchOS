import { useMemo } from 'react'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'

import styles from './experiments.module.css'

import 'katex/dist/katex.min.css'

export interface ExperimentMarkdownProps {
  readonly markdown: string
  /** Opens a `ros://` reference; undefined leaves it as a plain label. */
  readonly onOpenReference?: (href: string) => void
}

/**
 * Snapshot text rendered as Markdown (GFM + KaTeX). Raw HTML stays disabled.
 * A `ros://` link becomes a button, because the URL is not navigable in the
 * renderer; a reference the caller cannot resolve stays visible as mono text
 * rather than disappearing.
 */
export function ExperimentMarkdown({
  markdown,
  onOpenReference
}: ExperimentMarkdownProps): JSX.Element {
  const components = useMemo(
    () => ({
      a: ({ href, children, ...rest }: React.ComponentPropsWithoutRef<'a'>) => {
        if (href === undefined || !href.startsWith('ros://')) {
          return (
            <a {...rest} href={href} target="_blank" rel="noreferrer">
              {children}
            </a>
          )
        }
        if (onOpenReference === undefined) {
          return <code className={styles.refPlain}>{children}</code>
        }
        return (
          <button
            type="button"
            className={styles.linkRef}
            data-ros-href={href}
            onClick={() => onOpenReference(href)}
          >
            {children}
          </button>
        )
      }
    }),
    [onOpenReference]
  )

  if (markdown.trim() === '') return <p className={styles.muted}>—</p>

  return (
    <div className={styles.markdownBody}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex]}
        components={components}
        urlTransform={(url) => (url.startsWith('ros://') ? url : defaultUrlTransform(url))}
      >
        {markdown}
      </ReactMarkdown>
    </div>
  )
}