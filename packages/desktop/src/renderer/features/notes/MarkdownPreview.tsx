import { useMemo } from 'react'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'

import 'katex/dist/katex.min.css'

import styles from './notes.module.css'

export interface MarkdownPreviewProps {
  readonly markdown: string
  /** Project-relative folder that relative links and images resolve from. */
  readonly noteFilePath?: string
  readonly workspaceRoot?: string | null
  /** Hrefs outbound links reported as missing; rendered as deleted refs. */
  readonly unresolved?: ReadonlySet<string>
  readonly onOpenReference?: (href: string) => void
}

const IMAGE_SCHEMES = ['https://', 'http://', 'data:image/', 'ros-asset://']

/** normalizePath resolves `.` and `..` segments without Node's path module. */
export function normalizePath(path: string): string {
  const absolute = path.startsWith('/')
  const out: string[] = []
  for (const part of path.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') {
      out.pop()
      continue
    }
    out.push(part)
  }
  return `${absolute ? '/' : ''}${out.join('/')}`
}

/** imageSource turns the note-relative Markdown path into a ros-asset URL. */
export function imageSource(
  src: string,
  noteFilePath: string | undefined,
  workspaceRoot: string | null | undefined
): string {
  if (IMAGE_SCHEMES.some((scheme) => src.startsWith(scheme))) return src
  if (noteFilePath === undefined || workspaceRoot === null || workspaceRoot === undefined) return src
  const noteDirectory = noteFilePath.includes('/')
    ? noteFilePath.slice(0, noteFilePath.lastIndexOf('/'))
    : ''
  const relative = normalizePath(`${noteDirectory}/${src}`)
  void workspaceRoot
  return `ros-asset://workspace/${relative
    .split('/')
    .filter((segment) => segment !== '')
    .map((segment) => encodeURIComponent(segment))
    .join('/')}`
}

/**
 * MarkdownPreview renders the live split view: GFM, math, and images from the
 * note's attachment folder. A `ros://` link is a button, never a navigation
 * to a dead URL; an unresolved target says so instead of disappearing.
 */
export function MarkdownPreview({
  markdown,
  noteFilePath,
  workspaceRoot,
  unresolved,
  onOpenReference
}: MarkdownPreviewProps): JSX.Element {
  const components = useMemo(
    () => ({
      a: ({
        href,
        children,
        node: _node,
        ...rest
      }: React.ComponentPropsWithoutRef<'a'> & { node?: unknown }) => {
        if (href === undefined || !href.startsWith('ros://')) {
          return (
            <a {...rest} href={href} target="_blank" rel="noreferrer">
              {children}
            </a>
          )
        }
        if (unresolved?.has(href) === true) {
          return (
            <span className={styles.deletedRef} title={href}>
              {children}
            </span>
          )
        }
        return (
          <button
            type="button"
            className={styles.linkRef}
            data-ros-href={href}
            onClick={() => onOpenReference?.(href)}
          >
            {children}
          </button>
        )
      },
      img: ({ src, alt, node: _node, ...rest }: React.ComponentPropsWithoutRef<'img'> & { node?: unknown }) => (
        <img
          {...rest}
          src={src === undefined ? undefined : imageSource(src, noteFilePath, workspaceRoot)}
          alt={alt ?? ''}
          loading="lazy"
        />
      )
    }),
    [noteFilePath, workspaceRoot, unresolved, onOpenReference]
  )

  return (
    <div className={styles.preview}>
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