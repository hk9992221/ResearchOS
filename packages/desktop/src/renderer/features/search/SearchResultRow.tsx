import type { SearchHitView } from '@researchos/shared'

import { useT } from '../../i18n'
import { iconFor, type IconName } from '../../app/icons'
import styles from '../../app/CommandPalette.module.css'

export interface SearchResultRowProps {
  readonly hit: SearchHitView
  readonly selected: boolean
  readonly id: string
  readonly onSelect: () => void
  readonly onRun: () => void
}

const KIND_ICON: Record<string, IconName> = {
  paper: 'literature',
  note: 'note',
  db_record: 'fileText',
  idea: 'ideas',
  experiment: 'experiments'
}

/** One search hit: type, title, snippet, project and the jump locator. */
export function SearchResultRow({
  hit,
  selected,
  id,
  onSelect,
  onRun
}: SearchResultRowProps): JSX.Element {
  const t = useT()
  const Icon = iconFor(KIND_ICON[hit.entityType] ?? 'search')
  const locator =
    hit.locator.pageNo !== undefined
      ? t('shell.search.locator_page', { page: hit.locator.pageNo })
      : hit.locator.heading !== undefined && hit.locator.heading !== ''
        ? hit.locator.heading
        : ''

  return (
    <li
      id={id}
      role="option"
      aria-selected={selected}
      className={[styles.option, selected ? styles.optionSelected : ''].join(' ')}
      onMouseMove={onSelect}
      onClick={onRun}
    >
      <Icon aria-hidden="true" className={styles.optionIcon} strokeWidth={2} />
      <span className={styles.searchText}>
        <span className={styles.optionTitle}>{hit.title}</span>
        {hit.snippet !== '' && <span className={styles.snippet}>{hit.snippet}</span>}
      </span>
      <span className={styles.optionHint}>
        {hit.projectName}
        {locator === '' ? '' : ` · ${locator}`}
        {hit.archived ? ` · ${t('shell.search.archived')}` : ''}
      </span>
    </li>
  )
}