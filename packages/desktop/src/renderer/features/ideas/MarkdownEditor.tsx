import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'

import { useT } from '../../i18n'
import styles from './ideas.module.css'

import 'katex/dist/katex.min.css'

export interface MarkdownEditorProps {
  readonly label: string
  readonly value: string
  readonly onChange: (value: string) => void
  readonly placeholder?: string
  readonly rows?: number
  readonly readOnly?: boolean
}

/**
 * Description editor: segmented Markdown source / preview, with GFM and
 * KaTeX. Raw HTML is never enabled, so stored text cannot inject markup.
 */
export function MarkdownEditor({
  label,
  value,
  onChange,
  placeholder,
  rows = 14,
  readOnly = false
}: MarkdownEditorProps): JSX.Element {
  const t = useT()
  const [mode, setMode] = useState<'source' | 'preview'>('source')

  return (
    <section className={styles.markdown}>
      <div className={styles.markdownBar}>
        <span className={styles.fieldLabel}>{label}</span>
        <div className={styles.segmented} role="group" aria-label={t('pages.ideas.desc_label')}>
          <button
            type="button"
            className={styles.segment}
            aria-pressed={mode === 'source'}
            onClick={() => setMode('source')}
          >
            {t('pages.ideas.desc_source')}
          </button>
          <button
            type="button"
            className={styles.segment}
            aria-pressed={mode === 'preview'}
            onClick={() => setMode('preview')}
          >
            {t('pages.ideas.desc_preview')}
          </button>
        </div>
      </div>
      {mode === 'source' ? (
        <textarea
          className={styles.textarea}
          rows={rows}
          value={value}
          placeholder={placeholder}
          readOnly={readOnly}
          aria-label={label}
          onChange={(event) => onChange(event.target.value)}
        />
      ) : value.trim() === '' ? (
        <p className={styles.muted}>{t('pages.ideas.description_empty')}</p>
      ) : (
        <div className={styles.markdownBody}>
          <ReactMarkdown
            remarkPlugins={[remarkGfm, remarkMath]}
            rehypePlugins={[rehypeKatex]}
          >
            {value}
          </ReactMarkdown>
        </div>
      )}
    </section>
  )
}