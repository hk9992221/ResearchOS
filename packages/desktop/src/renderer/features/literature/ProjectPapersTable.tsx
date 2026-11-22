/**
 * The project's papers table. Selection is keyed by paper id and the header
 * checkbox reflects the page (select-on-page), matching the Go table's
 * tri-state behaviour. Every action is a hook the page wires to navigation or
 * the unlink dialog; this component writes nothing.
 */

import { useEffect, useRef } from 'react'

import type { ProjectPaper } from '@researchos/shared'

import { Badge, IconButton } from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { paperDisplay } from './model'
import styles from './literature.module.css'

export interface ProjectPapersTableProps {
  readonly papers: readonly ProjectPaper[]
  readonly selected: ReadonlySet<string>
  readonly onToggle: (paperId: string) => void
  readonly onTogglePage: (selected: boolean) => void
  readonly onOpenPaper: (paper: ProjectPaper) => void
  readonly onOpenPdf: (paper: ProjectPaper) => void
  readonly onUnlink: (paper: ProjectPaper) => void
}

function PageCheckbox({
  checked,
  indeterminate,
  label,
  onChange
}: {
  readonly checked: boolean
  readonly indeterminate: boolean
  readonly label: string
  readonly onChange: (checked: boolean) => void
}): JSX.Element {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current !== null) ref.current.indeterminate = indeterminate
  }, [indeterminate])
  return (
    <input
      ref={ref}
      type="checkbox"
      className={styles.checkbox}
      aria-label={label}
      checked={checked}
      onChange={(event) => onChange(event.target.checked)}
    />
  )
}

export function ProjectPapersTable({
  papers,
  selected,
  onToggle,
  onTogglePage,
  onOpenPaper,
  onOpenPdf,
  onUnlink
}: ProjectPapersTableProps): JSX.Element {
  const t = useT()
  const selectedOnPage = papers.filter((paper) => selected.has(paper.id)).length
  const allSelected = papers.length > 0 && selectedOnPage === papers.length
  const someSelected = selectedOnPage > 0 && !allSelected

  return (
    <div className={styles.tableWrap}>
      <table className={styles.table} aria-label={t('pages.literature.card_project')}>
        <thead>
          <tr>
            <th className={styles.checkCell}>
              <PageCheckbox
                checked={allSelected}
                indeterminate={someSelected}
                label={t('pages.literature.select_all_page')}
                onChange={onTogglePage}
              />
            </th>
            <th>{t('pages.literature.col_title')}</th>
            <th>{t('pages.literature.col_creators')}</th>
            <th>{t('pages.literature.col_year')}</th>
            <th>{t('pages.literature.col_copy')}</th>
            <th>{t('pages.literature.col_note')}</th>
            <th aria-label={t('pages.literature.col_state')} />
          </tr>
        </thead>
        <tbody>
          {papers.map((paper) => {
            const display = paperDisplay(paper)
            const checked = selected.has(paper.id)
            const missing = paper.fileState === 'missing' || paper.broken
            return (
              <tr key={paper.id} className={styles.row}>
                <td className={styles.checkCell}>
                  <input
                    type="checkbox"
                    className={styles.checkbox}
                    aria-label={display.title === '' ? paper.id : display.title}
                    checked={checked}
                    onChange={() => onToggle(paper.id)}
                  />
                </td>
                <td>
                  <div className={styles.titleCell}>
                    <button
                      type="button"
                      className={styles.titleButton}
                      onClick={() => onOpenPaper(paper)}
                    >
                      {display.title === '' ? t('pages.paper.untitled') : display.title}
                    </button>
                    {paper.sourceState !== 'ok' && (
                      <span className={styles.subtle}>
                        {t(
                          paper.sourceState === 'missing'
                            ? 'pages.literature.source_missing'
                            : 'pages.literature.source_unreadable'
                        )}
                      </span>
                    )}
                  </div>
                </td>
                <td className={styles.subtle}>{display.authors}</td>
                <td className={styles.subtle}>{display.year ?? ''}</td>
                <td>
                  <div className={styles.badges}>
                    <Badge tone={missing ? 'warning' : 'success'}>
                      {t(
                        missing
                          ? 'pages.literature.copy_missing'
                          : 'pages.literature.copy_present'
                      )}
                    </Badge>
                  </div>
                </td>
                <td>
                  <Badge tone={paper.noteId === null ? 'neutral' : 'info'}>
                    {t(
                      paper.noteId === null
                        ? 'pages.literature.note_absent'
                        : 'pages.literature.note_present'
                    )}
                  </Badge>
                </td>
                <td>
                  <div className={styles.actions}>
                    <IconButton
                      icon={iconFor('info')}
                      label={t('pages.literature.btn_open_paper')}
                      size="sm"
                      onClick={() => onOpenPaper(paper)}
                    />
                    <IconButton
                      icon={iconFor('fileText')}
                      label={t('pages.literature.btn_open_pdf')}
                      size="sm"
                      disabled={missing || paper.filePath === null}
                      onClick={() => onOpenPdf(paper)}
                    />
                    <IconButton
                      icon={iconFor('trash')}
                      label={t('pages.literature.btn_unlink')}
                      size="sm"
                      onClick={() => onUnlink(paper)}
                    />
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}