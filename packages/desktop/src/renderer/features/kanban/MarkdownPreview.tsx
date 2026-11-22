import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import 'katex/dist/katex.min.css'

import styles from './kanban.module.css'

export interface MarkdownPreviewProps {
  readonly source: string
}

/**
 * Markdown + LaTeX preview for the task description. It is a separate chunk:
 * the board does not need the 900 kB KaTeX runtime, and the detail page only
 * loads it when the user opens Preview.
 */
export default function MarkdownPreview({ source }: MarkdownPreviewProps): JSX.Element {
  return (
    <div className={styles.markdown}>
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkMath]} rehypePlugins={[rehypeKatex]}>
        {source}
      </ReactMarkdown>
    </div>
  )
}