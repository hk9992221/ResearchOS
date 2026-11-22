/**
 * Literature overview: the project's papers as a comparison table over the
 * Paper Note fields, plus the free Markdown synthesis. The synthesis is one
 * `kind=overview` note; saving carries `baseHash` so a file edited outside the
 * app asks before it is overwritten, and `ros://` links in the body navigate
 * to the referenced entity (a `paper` link with `#page=N` opens the reader on
 * that page).
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import 'katex/dist/katex.min.css'

import type { PaperNoteView, ProjectPaper } from '@researchos/shared'
import { AppError, AppErrorCode } from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Skeleton
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { useNavigate } from 'react-router-dom'
import { routeHref } from '../../routes'
import { literatureApi } from './api'
import { paperDisplay, rosDestination, rosHrefs } from './model'
import styles from './literature.module.css'

type Mode = 'source' | 'split' | 'preview'

const FIELD_KEYS = [
  'summary',
  'problem',
  'method',
  'contribution',
  'limitations',
  'understanding'
] as const

type FieldKey = (typeof FIELD_KEYS)[number]

const FIELD_LABEL: Readonly<Record<FieldKey, string>> = {
  summary: 'pages.paper.field_summary',
  problem: 'pages.paper.field_problem',
  method: 'pages.paper.field_method',
  contribution: 'pages.paper.field_contribution',
  limitations: 'pages.paper.field_limitations',
  understanding: 'pages.paper.field_understanding'
}

interface ComparisonData {
  readonly papers: readonly ProjectPaper[]
  readonly notes: ReadonlyMap<string, PaperNoteView>
}

export interface OverviewEditorProps {
  readonly projectId: string
  readonly archived: boolean
}

export function OverviewEditor({ projectId, archived }: OverviewEditorProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<Mode>('split')
  const [markdown, setMarkdown] = useState('')
  const [baseHash, setBaseHash] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [conflict, setConflict] = useState(false)
  const lastLoaded = useRef<string | null>(null)

  const overviewQuery = useQuery({
    queryKey: ['literature', projectId, 'overview'],
    queryFn: () => literatureApi.overview(projectId)
  })

  const comparisonQuery = useQuery<ComparisonData>({
    queryKey: ['literature', projectId, 'comparison'],
    queryFn: async () => {
      const page = await literatureApi.papers({ projectId, limit: 60, offset: 0 })
      const notes = await Promise.all(
        page.items.map(async (paper) => {
          try {
            return [paper.id, await literatureApi.paperNote(projectId, paper.id)] as const
          } catch {
            return [paper.id, null] as const
          }
        })
      )
      const map = new Map<string, PaperNoteView>()
      for (const [paperId, note] of notes) if (note !== null) map.set(paperId, note)
      return { papers: page.items, notes: map }
    }
  })

  const loadedHash = overviewQuery.data?.bodyHash ?? null
  useEffect(() => {
    if (overviewQuery.data === undefined) return
    const marker = overviewQuery.data?.bodyHash ?? ''
    if (lastLoaded.current === marker && dirty) return
    lastLoaded.current = marker
    setMarkdown(overviewQuery.data?.markdown ?? '')
    setBaseHash(overviewQuery.data?.bodyHash ?? null)
    setDirty(false)
  }, [overviewQuery.data, dirty])

  const save = async (force?: string): Promise<void> => {
    setSaving(true)
    setSaveError(null)
    setConflict(false)
    try {
      const saved = await literatureApi.saveOverview(
        projectId,
        markdown,
        force ?? baseHash ?? undefined
      )
      setBaseHash(saved.fileHash)
      setSavedAt(saved.savedAt)
      setDirty(false)
      await queryClient.invalidateQueries({ queryKey: ['literature', projectId, 'overview'] })
    } catch (cause) {
      if (cause instanceof AppError && cause.code === AppErrorCode.Conflict) {
        setConflict(true)
      }
      setSaveError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const reload = async (): Promise<void> => {
    const fresh = await overviewQuery.refetch()
    setMarkdown(fresh.data?.markdown ?? '')
    setBaseHash(fresh.data?.bodyHash ?? null)
    setDirty(false)
    setConflict(false)
    setSaveError(null)
  }

  const overwrite = async (): Promise<void> => {
    const fresh = await literatureApi.overview(projectId)
    await save(fresh?.bodyHash ?? undefined)
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault()
        if (!dirty || saving || archived) return
        void save()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  })

  const refs = useMemo(() => rosHrefs(markdown), [markdown])

  const openRef = (href: string): void => {
    const destination = rosDestination(href, projectId)
    if (destination !== undefined) navigate(destination)
  }

  const previewComponents = useMemo(
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
        const destination = rosDestination(href, projectId)
        if (destination === undefined) {
          return <span className={styles.rosMissing}>{children}</span>
        }
        return (
          <button type="button" className={styles.rosLink} onClick={() => navigate(destination)}>
            {children}
          </button>
        )
      }
    }),
    [projectId, navigate]
  )

  if (overviewQuery.isPending && !dirty) return <Skeleton rows={10} />
  if (overviewQuery.isError && !dirty) {
    return (
      <ErrorState
        title={t('pages.literature.overview_banner')}
        message={overviewQuery.error instanceof Error ? overviewQuery.error.message : undefined}
        onRetry={() => void overviewQuery.refetch()}
        retryLabel={t('pages.literature.btn_retry')}
      />
    )
  }

  const comparison = comparisonQuery.data
  const editorColumn = (
    <textarea
      className={styles.editor}
      aria-label={t('pages.literature.overview_editor')}
      value={markdown}
      placeholder={t('pages.literature.overview_placeholder')}
      disabled={archived}
      onChange={(event) => {
        setMarkdown(event.target.value)
        setDirty(true)
        setSaveError(null)
      }}
    />
  )
  const previewColumn = (
    <div className={styles.preview}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex]}
        components={previewComponents}
        urlTransform={(url) => (url.startsWith('ros://') ? url : defaultUrlTransform(url))}
      >
        {markdown}
      </ReactMarkdown>
    </div>
  )

  return (
    <div className={styles.stack}>
      {archived && <Banner tone="warning">{t('pages.literature.archived_banner')}</Banner>}
      {conflict && (
        <Banner
          tone="warning"
          title={t('pages.literature.conflict_title')}
          action={
            <div className={styles.footerRow}>
              <Button variant="secondary" size="sm" onClick={() => void reload()}>
                {t('pages.paper.btn_reload')}
              </Button>
              <Button variant="danger" size="sm" onClick={() => void overwrite()}>
                {t('pages.paper.btn_overwrite')}
              </Button>
            </div>
          }
        >
          {t('pages.literature.overview_conflict')}
        </Banner>
      )}
      {saveError !== null && !conflict && (
        <Banner tone="danger" title={t('pages.literature.overview_banner')}>
          {saveError}
        </Banner>
      )}

      <Card title={t('pages.literature.overview_comparison')}>
        {comparisonQuery.isPending ? (
          <Skeleton rows={4} />
        ) : comparisonQuery.isError ? (
          <ErrorState
            title={t('pages.literature.overview_comparison_fail')}
            onRetry={() => void comparisonQuery.refetch()}
            retryLabel={t('pages.literature.btn_retry')}
          />
        ) : comparison === undefined || comparison.papers.length === 0 ? (
          <EmptyState
            title={t('pages.literature.project_empty_title')}
            description={t('pages.literature.project_empty_reason')}
          />
        ) : (
          <div className={styles.comparisonWrap}>
            <table className={styles.comparison} aria-label={t('pages.literature.overview_comparison')}>
              <thead>
                <tr>
                  <th>{t('pages.literature.col_title')}</th>
                  <th>{t('pages.literature.col_year')}</th>
                  {FIELD_KEYS.map((field) => (
                    <th key={field}>{t(FIELD_LABEL[field])}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {comparison.papers.map((paper) => {
                  const display = paperDisplay(paper)
                  const note = comparison.notes.get(paper.id)
                  return (
                    <tr key={paper.id}>
                      <td>
                        <button
                          type="button"
                          className={styles.paperLink}
                          onClick={() => {
                            const href = routeHref('paper', { projectId, entityId: paper.id })
                            if (href !== undefined) navigate(href)
                          }}
                        >
                          {display.title || t('pages.paper.untitled')}
                        </button>
                        {note === null && (
                          <div>
                            <Badge tone="neutral">{t('pages.literature.overview_no_note')}</Badge>
                          </div>
                        )}
                      </td>
                      <td className={styles.subtle}>{display.year ?? ''}</td>
                      {FIELD_KEYS.map((field) => (
                        <td key={field}>
                          <span className={styles.cellText}>{note?.fields[field] ?? ''}</span>
                        </td>
                      ))}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card
        title={t('pages.literature.tab_overview')}
        actions={
          <div className={styles.overviewModes}>
            <div className={styles.segmented} role="group" aria-label={t('pages.literature.overview_mode_label')}>
              {(['source', 'split', 'preview'] as const).map((entry) => (
                <button
                  key={entry}
                  type="button"
                  className={styles.segment}
                  aria-pressed={mode === entry}
                  onClick={() => setMode(entry)}
                >
                  {t(`pages.literature.overview_mode_${entry}`)}
                </button>
              ))}
            </div>
            {saving && <span className={styles.saveState}>{t('pages.paper.state_saving')}</span>}
            {!saving && savedAt !== null && (
              <span className={styles.saveState}>{t('pages.literature.overview_saved')}</span>
            )}
            {!saving && dirty && <span className={styles.saveState}>{t('pages.paper.state_unsaved')}</span>}
            <Button
              variant="primary"
              size="sm"
              disabled={archived || saving || !dirty}
              onClick={() => void save()}
            >
              {t('pages.literature.overview_save')}
            </Button>
          </div>
        }
      >
        <div className={mode === 'split' ? styles.editorGrid : styles.editorGridSingle}>
          {mode !== 'preview' && editorColumn}
          {mode !== 'source' && previewColumn}
        </div>
      </Card>

      <Card title={t('pages.literature.overview_refs')}>
        {refs.length === 0 ? (
          <p className={styles.subtle}>{t('pages.literature.overview_refs_empty')}</p>
        ) : (
          <ul className={styles.outcomeList}>
            {refs.map((href) => {
              const destination = rosDestination(href, projectId)
              return (
                <li key={href} className={styles.outcomeRow}>
                  <Badge tone="neutral">{href.split('/')[2] ?? ''}</Badge>
                  {destination === undefined ? (
                    <span className={styles.rosMissing}>{href}</span>
                  ) : (
                    <button type="button" className={styles.rosLink} onClick={() => openRef(href)}>
                      {href}
                    </button>
                  )}
                  {destination === undefined && (
                    <Badge tone="warning">{t('pages.literature.overview_ref_missing')}</Badge>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </Card>
    </div>
  )
}