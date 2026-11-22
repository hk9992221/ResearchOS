import { useMemo } from 'react'
import MarkdownIt from 'markdown-it'

import type { WeeklyGenerationMeta, WeeklySectionKey, WeeklyTemplate } from '@researchos/shared'

import { useT } from '../../i18n'
import { splitReport } from './weekly-sections'
import styles from './weekly.module.css'

/** Raw HTML stays escaped: the body is user text, not a template. */
const markdown = new MarkdownIt({ html: false, linkify: false, breaks: false })

export interface ReportEditorProps {
  readonly content: string
  readonly template: WeeklyTemplate
  readonly mode: 'edit' | 'preview'
  readonly readOnly: boolean
  readonly generation: WeeklyGenerationMeta | undefined
  readonly onSectionChange: (key: WeeklySectionKey, body: string) => void
  /** Reports the current textarea selection; null when the selection is empty. */
  readonly onSelectionChange?: (selection: { section: WeeklySectionKey; text: string } | null) => void
}

/**
 * ReportEditor edits the seven sections separately: each section is one
 * Markdown body, and the surrounding heading stays fixed so the validator
 * always sees the canonical seven-section structure. Preview renders the
 * assembled document, which is exactly what export and confirmation see.
 */
export function ReportEditor({
  content,
  template,
  mode,
  readOnly,
  generation,
  onSectionChange,
  onSelectionChange
}: ReportEditorProps): JSX.Element {
  const t = useT()
  const sections = useMemo(() => splitReport(content, template), [content, template])
  const html = useMemo(() => markdown.render(content), [content])

  const source =
    generation?.mode === 'ai'
      ? t('pages.weekly_review.section_ai_source', {
          provider: generation.provider_id ?? '—',
          model: generation.model_id ?? '—'
        })
      : t('pages.weekly_review.section_manual_source')

  if (mode === 'preview') {
    return (
      <section className={styles.sectionCard}>
        <div
          className={styles.preview}
          aria-label={t('pages.weekly_review.mode_preview')}
          dangerouslySetInnerHTML={{ __html: html }}
        />
      </section>
    )
  }

  return (
    <div className={styles.split}>
      {sections.map((section) => (
        <section key={section.key} className={styles.sectionCard}>
          <header className={styles.sectionHead}>
            <h3 className={styles.sectionTitle}>
              {section.num}. {section.title}
            </h3>
            <span className={styles.sectionMeta}>
              {!section.present && <span>{t('pages.weekly_review.section_missing')}</span>}
              <span>{source}</span>
            </span>
          </header>
          <textarea
            className={styles.sectionBody}
            aria-label={`${section.num}. ${section.title}`}
            value={section.body}
            readOnly={readOnly}
            rows={Math.max(3, section.body.split('\n').length + 1)}
            onChange={(event) => onSectionChange(section.key, event.target.value)}
            onSelect={(event) => {
              const element = event.currentTarget
              const text = element.value.slice(element.selectionStart, element.selectionEnd)
              onSelectionChange?.(
                text.trim() === '' ? null : { section: section.key, text }
              )
            }}
          />
        </section>
      ))}
    </div>
  )
}
