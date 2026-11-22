import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router-dom'

import type { SearchHitView, SearchQueryRequest } from '@researchos/shared'

import { Banner, Skeleton } from '../design/components'
import { useT } from '../i18n'
import { ipc } from '../lib/ipc'
import { queryKeys } from '../lib/query'
import { searchJumpHref } from '../lib/search-nav'
import { DEGRADE_KEYS } from '../features/search/degraded'
import { SearchResultRow } from '../features/search/SearchResultRow'
import { commandSearchFields, type PaletteCommand } from './commands'
import { rankByQuery } from './fuzzy'
import { iconFor } from './icons'
import styles from './CommandPalette.module.css'

export type PaletteMode = 'commands' | 'search'

export interface CommandPaletteProps {
  readonly open: boolean
  readonly mode: PaletteMode
  /** The open project; search in project scope needs it. */
  readonly projectId?: string | null
  readonly commands: readonly PaletteCommand[]
  readonly onClose: () => void
}

/** Maximum command rows shown at once; typing narrows the rest. */
const VISIBLE_LIMIT = 12
const SEARCH_LIMIT = 20

/**
 * CommandPalette is the shell's omni-control in two modes:
 *  - commands (Ctrl/Cmd+Shift+P, toolbar button): fuzzy filter over pages and
 *    shell/feature commands, Enter runs, Escape closes.
 *  - search (Ctrl/Cmd+K, toolbar search field): `search.query` results with
 *    their jump locator; Enter navigates to the entity with one-shot
 *    heading/offset/page parameters. Typing `>` switches to commands.
 */
export function CommandPalette({
  open,
  mode,
  projectId = null,
  commands,
  onClose
}: CommandPaletteProps): JSX.Element | null {
  const t = useT()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)
  const [scope, setScope] = useState<'project' | 'global'>('global')
  const inputRef = useRef<HTMLInputElement>(null)
  const listId = useId()

  useEffect(() => {
    if (!open) return
    setQuery('')
    setSelected(0)
    setScope(projectId === null ? 'global' : 'project')
    inputRef.current?.focus()
  }, [open, mode, projectId])

  const commandMode = mode === 'search' ? query.startsWith('>') : true
  const commandQuery = commandMode ? (mode === 'search' ? query.slice(1) : query) : ''
  const searchText = mode === 'search' && !commandMode ? query.trim() : ''

  const filteredCommands = useMemo(
    () => rankByQuery(commandQuery, commands, commandSearchFields).slice(0, VISIBLE_LIMIT),
    [commands, commandQuery]
  )

  const request = useMemo<SearchQueryRequest>(
    () => ({
      scope,
      projectId: scope === 'project' ? (projectId ?? undefined) : undefined,
      query: searchText,
      limit: SEARCH_LIMIT
    }),
    [scope, projectId, searchText]
  )

  const searchQuery = useQuery({
    queryKey: queryKeys.search.query(request),
    queryFn: () => ipc.invoke('search.query', request),
    enabled: open && mode === 'search' && !commandMode && searchText !== ''
  })

  const hits: readonly SearchHitView[] = useMemo(
    () => (mode === 'search' && !commandMode ? (searchQuery.data?.hits ?? []) : []),
    [mode, commandMode, searchQuery.data]
  )

  const itemCount = commandMode ? filteredCommands.length : hits.length
  const activeCommand = commandMode ? filteredCommands[selected] : undefined
  const activeHit = commandMode ? undefined : hits[selected]
  const activeId =
    activeCommand !== undefined
      ? `${listId}-c-${activeCommand.id}`
      : activeHit !== undefined
        ? `${listId}-h-${activeHit.docId}`
        : undefined

  useEffect(() => {
    setSelected(0)
  }, [query, mode, scope])

  useEffect(() => {
    if (!open || activeId === undefined) return
    document.getElementById(activeId)?.scrollIntoView?.({ block: 'nearest' })
  }, [activeId, open])

  if (!open || typeof document === 'undefined') return null

  const runCommand = (command: PaletteCommand): void => {
    onClose()
    command.run()
  }

  const runHit = (hit: SearchHitView): void => {
    const href = searchJumpHref(hit)
    onClose()
    if (href !== undefined) navigate(href)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key === 'Escape') {
      event.preventDefault()
      onClose()
      return
    }
    if (event.key === 'ArrowDown' && itemCount > 0) {
      event.preventDefault()
      setSelected((current) => (current + 1) % itemCount)
      return
    }
    if (event.key === 'ArrowUp' && itemCount > 0) {
      event.preventDefault()
      setSelected((current) => (current - 1 + itemCount) % itemCount)
      return
    }
    if (event.key === 'Enter') {
      if (activeCommand !== undefined) {
        event.preventDefault()
        runCommand(activeCommand)
      } else if (activeHit !== undefined) {
        event.preventDefault()
        runHit(activeHit)
      }
    }
  }

  const title = mode === 'commands' ? t('shell.palette.title') : t('shell.search.label')
  const placeholder =
    mode === 'commands' ? t('shell.palette.placeholder') : t('shell.search.placeholder')

  return createPortal(
    <div
      className={styles.backdrop}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className={styles.panel} role="dialog" aria-modal="true" aria-label={title}>
        <input
          ref={inputRef}
          className={styles.input}
          role="combobox"
          aria-expanded={true}
          aria-controls={listId}
          aria-activedescendant={activeId}
          aria-autocomplete="list"
          aria-label={mode === 'commands' ? t('shell.palette.title') : t('shell.topbar.search_label')}
          placeholder={placeholder}
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={onKeyDown}
        />

        {mode === 'search' && (
          <div className={styles.searchBar}>
            <select
              aria-label={t('shell.search.label')}
              className={styles.scope}
              value={scope}
              onChange={(event) =>
                setScope(event.target.value === 'project' ? 'project' : 'global')
              }
            >
              {projectId !== null && (
                <option value="project">{t('shell.search.scope_project')}</option>
              )}
              <option value="global">{t('shell.search.scope_global')}</option>
            </select>
            {searchQuery.data !== undefined && (
              <span className={styles.optionHint}>
                {t('shell.search.result_count', { count: searchQuery.data.total })}
              </span>
            )}
          </div>
        )}

        {mode === 'search' && searchQuery.data !== undefined && searchQuery.data.degraded.length > 0 && (
          <ul className={styles.degraded}>
            {searchQuery.data.degraded.map((code) => (
              <li key={code} className={styles.degradedChip}>
                {t(DEGRADE_KEYS[code])}
              </li>
            ))}
          </ul>
        )}

        {commandMode ? (
          filteredCommands.length === 0 ? (
            <p className={styles.message}>{t('shell.palette.no_results')}</p>
          ) : (
            <ul className={styles.list} role="listbox" id={listId} aria-label={t('shell.palette.title')}>
              {filteredCommands.map((command, index) => {
                const Icon = iconFor(command.icon)
                return (
                  <li
                    key={command.id}
                    id={`${listId}-c-${command.id}`}
                    role="option"
                    aria-selected={index === selected}
                    className={[styles.option, index === selected ? styles.optionSelected : ''].join(
                      ' '
                    )}
                    onMouseMove={() => setSelected(index)}
                    onClick={() => runCommand(command)}
                  >
                    <Icon aria-hidden="true" className={styles.optionIcon} strokeWidth={2} />
                    <span className={styles.optionTitle}>{command.title}</span>
                    {command.hint !== undefined && (
                      <span className={styles.optionHint}>{command.hint}</span>
                    )}
                  </li>
                )
              })}
            </ul>
          )
        ) : searchText === '' ? (
          <p className={styles.message}>{t('shell.search.hint')}</p>
        ) : searchQuery.isPending ? (
          <div className={styles.resultsLoading}>
            <Skeleton rows={4} />
          </div>
        ) : searchQuery.isError ? (
          <div className={styles.resultsLoading}>
            <Banner tone="danger" title={t('shell.search.failed', { error: searchQuery.error.message })}>
              {searchQuery.error.message}
            </Banner>
          </div>
        ) : hits.length === 0 ? (
          <p className={styles.message}>{t('shell.search.no_results')}</p>
        ) : (
          <ul className={styles.list} role="listbox" id={listId} aria-label={t('shell.search.label')}>
            {hits.map((hit, index) => (
              <SearchResultRow
                key={hit.docId}
                id={`${listId}-h-${hit.docId}`}
                hit={hit}
                selected={index === selected}
                onSelect={() => setSelected(index)}
                onRun={() => runHit(hit)}
              />
            ))}
          </ul>
        )}
      </div>
    </div>,
    document.body
  )
}