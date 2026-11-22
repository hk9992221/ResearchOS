/**
 * PDF route: pdf.js reader, persistent highlights and the one-click actions on
 * a selection. A highlight is stored with the page number, normalized rects
 * and the quote; "Paper Note excerpt" inserts the quote plus a
 * `ros://paper/<id>#page=N` link into the Paper Note body, and "Idea evidence"
 * creates evidence with the paper source locator. The route's `?page=N` hint is
 * consumed once, for the paper it arrived with.
 */

import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'

import {
  AppError,
  AppErrorCode,
  type EvidenceDirection,
  type PaperAnnotation,
  type PaperNoteView
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  EmptyState,
  ErrorState,
  IconButton,
  Modal,
  Page,
  PageHeader,
  SelectField,
  Skeleton
} from '../../../design/components'
import { iconFor } from '../../../app/icons'
import { useCurrentProject } from '../../../app/useCurrentProject'
import { useT } from '../../../i18n'
import { queryKeys } from '../../../lib/query'
import { routeHref } from '../../../routes'
import { pushToast } from '../../../state/toasts'
import { paperApi } from '../api'
import { rosAssetUrl } from '../../literature/model'
import type { PdfFit, PdfSelection } from './PdfCanvas'
import styles from '../paper.module.css'

const PdfCanvas = lazy(() => import('./PdfCanvas'))

const ZOOM_STEP = 0.25
const MIN_ZOOM = 0.5
const MAX_ZOOM = 4

export default function PdfPage({
  projectId,
  entityId
}: {
  readonly projectId?: string
  readonly entityId?: string
}): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { projectId: currentProjectId, archived } = useCurrentProject()
  const resolvedProjectId = projectId ?? currentProjectId
  const paperId = entityId ?? ''
  const [searchParams, setSearchParams] = useSearchParams()

  const [pageNo, setPageNoState] = useState(1)
  const [pageInput, setPageInput] = useState('1')
  const [pageCount, setPageCount] = useState(0)
  const [zoom, setZoom] = useState(1)
  const [fit, setFit] = useState<PdfFit>('width')
  const [scale, setScale] = useState(1)
  const [rendering, setRendering] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [selection, setSelection] = useState<PdfSelection | null>(null)
  const [focusAnnotationId, setFocusAnnotationId] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busyAction, setBusyAction] = useState<'highlight' | 'excerpt' | 'evidence' | null>(null)

  const [evidenceOpen, setEvidenceOpen] = useState(false)
  const [evidenceIdeaId, setEvidenceIdeaId] = useState('')
  const [evidenceDirection, setEvidenceDirection] = useState<EvidenceDirection>('support')
  const [evidenceContent, setEvidenceContent] = useState('')
  const [evidenceError, setEvidenceError] = useState<string | null>(null)

  const consumedPageHints = useRef(new Set<string>())

  const paperQuery = useQuery({
    queryKey: queryKeys.literature.paper(resolvedProjectId ?? 'none', paperId),
    queryFn: () => paperApi.get(resolvedProjectId as string, paperId),
    enabled: resolvedProjectId !== null && paperId !== ''
  })

  const annotationsQuery = useQuery({
    queryKey: ['literature', resolvedProjectId ?? 'none', 'annotations', paperId],
    queryFn: () => paperApi.annotations(resolvedProjectId as string, paperId),
    enabled: resolvedProjectId !== null && paperId !== ''
  })

  const contextQuery = useQuery({
    queryKey: ['project', resolvedProjectId ?? 'none', 'context', 'paper', paperId],
    queryFn: () => paperApi.context(resolvedProjectId as string, paperId),
    enabled: resolvedProjectId !== null && paperId !== ''
  })

  const ideasQuery = useQuery({
    queryKey: ['ideas', resolvedProjectId ?? 'none', 'list', { pdfEvidence: true }],
    queryFn: () => paperApi.ideas(resolvedProjectId as string),
    enabled: resolvedProjectId !== null && evidenceOpen
  })

  const setPageNo = useCallback((next: number) => {
    setPageNoState(next)
    setPageInput(String(next))
  }, [])

  const jumpToPage = useCallback(
    (next: number) => {
      const clamped = Math.max(1, pageCount === 0 ? next : Math.min(pageCount, next))
      setPageNo(clamped)
      setFocusAnnotationId(null)
    },
    [pageCount, setPageNo]
  )

  useEffect(() => {
    const raw = searchParams.get('page')
    if (raw === null || pageCount === 0) return
    const parsed = Number.parseInt(raw, 10)
    if (!Number.isFinite(parsed) || parsed < 1) return
    const key = `${paperId}:${parsed}`
    if (consumedPageHints.current.has(key)) return
    consumedPageHints.current.add(key)
    setPageNo(Math.min(pageCount, parsed))
    const next = new URLSearchParams(searchParams)
    next.delete('page')
    setSearchParams(next, { replace: true })
  }, [paperId, pageCount, searchParams, setPageNo, setSearchParams])

  const onPageCount = useCallback((count: number) => setPageCount(count), [])
  const onScale = useCallback((next: number) => setScale(next), [])
  const onSelection = useCallback((next: PdfSelection | null) => setSelection(next), [])
  const onRendering = useCallback((next: boolean) => setRendering(next), [])
  const onError = useCallback((message: string) => {
    setLoadError(message)
    setRendering(false)
  }, [])

  const clearSelection = useCallback(() => {
    setSelection(null)
    window.getSelection()?.removeAllRanges()
  }, [])

  const invalidateAnnotations = useCallback(() => {
    if (resolvedProjectId === null) return
    void queryClient.invalidateQueries({
      queryKey: ['literature', resolvedProjectId, 'annotations', paperId]
    })
  }, [paperId, queryClient, resolvedProjectId])

  const createAnnotation = async (
    current: PdfSelection,
    extra: { noteId?: string; evidenceId?: string } = {}
  ): Promise<PaperAnnotation> => {
    if (resolvedProjectId === null) throw new Error('no project')
    return paperApi.createAnnotation(resolvedProjectId, paperId, {
      pageNo: current.pageNo,
      rects: current.rects,
      quote: current.quote,
      ...extra
    })
  }

  const highlight = async (): Promise<void> => {
    if (selection === null || resolvedProjectId === null) return
    setBusyAction('highlight')
    setActionError(null)
    try {
      await createAnnotation(selection)
      invalidateAnnotations()
      clearSelection()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusyAction(null)
    }
  }

  const appendExcerpt = async (): Promise<void> => {
    if (selection === null || resolvedProjectId === null) return
    setBusyAction('excerpt')
    setActionError(null)
    try {
      const annotation = await createAnnotation(selection)
      let note: PaperNoteView | null = await paperApi.note(resolvedProjectId, paperId)
      if (note === null) note = await paperApi.createNote(resolvedProjectId, paperId)
      const quoted = selection.quote
        .split(/\r?\n/u)
        .map((line) => `> ${line}`)
        .join('\n')
      const link = `ros://paper/${paperId}#page=${selection.pageNo}`
      const addition = `\n\n${quoted}\n\n[${t('pages.pdf.page_marker', { page: selection.pageNo })}](${link})\n`
      let markdown = `${note.bodyMd.trimEnd()}${addition}`
      try {
        await paperApi.saveBody(resolvedProjectId, paperId, markdown, note.bodyHash ?? '')
      } catch (cause) {
        if (!(cause instanceof AppError) || cause.code !== AppErrorCode.Conflict) throw cause
        // The note changed outside this viewer; adopt its disk body and append
        // there rather than overwrite the user's other edits.
        const fresh = await paperApi.note(resolvedProjectId, paperId)
        if (fresh === null) throw cause
        markdown = `${fresh.bodyMd.trimEnd()}${addition}`
        await paperApi.saveBody(resolvedProjectId, paperId, markdown, fresh.bodyHash ?? '')
      }
      await paperApi.updateAnnotation(resolvedProjectId, annotation.id, {
        noteId: note.note.id
      })
      void queryClient.invalidateQueries({
        queryKey: ['literature', resolvedProjectId, 'paperNote', paperId]
      })
      void queryClient.invalidateQueries({ queryKey: queryKeys.notes.all(resolvedProjectId) })
      invalidateAnnotations()
      clearSelection()
      pushToast({ tone: 'success', title: t('pages.pdf.excerpt_saved') })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusyAction(null)
    }
  }

  const openEvidence = (): void => {
    if (selection === null) return
    setEvidenceIdeaId('')
    setEvidenceDirection('support')
    setEvidenceContent(selection.quote)
    setEvidenceError(null)
    setEvidenceOpen(true)
  }

  const submitEvidence = async (): Promise<void> => {
    if (selection === null || resolvedProjectId === null) return
    if (evidenceIdeaId === '') {
      setEvidenceError(t('pages.pdf.evidence_idea_required'))
      return
    }
    setBusyAction('evidence')
    setEvidenceError(null)
    try {
      const annotation = await createAnnotation(selection)
      const evidence = await paperApi.createEvidence({
        projectId: resolvedProjectId,
        ideaId: evidenceIdeaId,
        direction: evidenceDirection,
        content: evidenceContent.trim() === '' ? selection.quote : evidenceContent.trim(),
        source: {
          type: 'paper',
          id: paperId,
          locator: { pageNo: selection.pageNo, rects: selection.rects }
        }
      })
      await paperApi.updateAnnotation(resolvedProjectId, annotation.id, {
        evidenceId: evidence.id
      })
      void queryClient.invalidateQueries({ queryKey: queryKeys.ideas.all(resolvedProjectId) })
      invalidateAnnotations()
      setEvidenceOpen(false)
      clearSelection()
      pushToast({ tone: 'success', title: t('pages.pdf.evidence_created') })
    } catch (cause) {
      setEvidenceError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusyAction(null)
    }
  }

  const evidenceIdea = useMemo(() => {
    const evidenceToIdea = new Map<string, string>()
    for (const evidence of contextQuery.data?.evidences ?? []) {
      evidenceToIdea.set(evidence.id, evidence.ideaId)
    }
    return evidenceToIdea
  }, [contextQuery.data])

  const annotations = annotationsQuery.data ?? []
  const pageAnnotations = annotations.filter((annotation) => annotation.pageNo === pageNo)
  const ideas = ideasQuery.data?.items ?? []

  if (resolvedProjectId === null || paperId === '') {
    return (
      <Page>
        <EmptyState
          title={t('pages.paper.needs_project_title')}
          description={t('pages.paper.needs_project_reason')}
        />
      </Page>
    )
  }

  if (paperQuery.isPending) {
    return (
      <Page>
        <PageHeader title={t('pages.pdf.title')} />
        <Skeleton rows={8} />
      </Page>
    )
  }

  if (paperQuery.isError) {
    return (
      <Page>
        <ErrorState
          title={t('pages.pdf.load_fail')}
          message={paperQuery.error instanceof Error ? paperQuery.error.message : undefined}
          onRetry={() => void paperQuery.refetch()}
          retryLabel={t('pages.literature.btn_retry')}
        />
      </Page>
    )
  }

  const paper = paperQuery.data
  const backHref = routeHref('paper', { projectId: resolvedProjectId, entityId: paperId })
  const paperTitle = paper.title || t('pages.paper.untitled')
  const canRead = paper.filePath !== null && paper.fileState !== 'missing'

  return (
    <Page>
      <PageHeader
        title={paperTitle}
        description={t('pages.pdf.subtitle')}
        actions={
          <Button
            variant="secondary"
            onClick={() => {
              if (backHref !== undefined) navigate(backHref)
            }}
          >
            {t('pages.pdf.btn_paper')}
          </Button>
        }
      />

      {archived && <Banner tone="warning">{t('pages.literature.archived_banner')}</Banner>}
      {actionError !== null && (
        <Banner tone="danger" title={t('pages.pdf.action_fail')}>
          {actionError}
        </Banner>
      )}
      {annotationsQuery.isError && (
        <Banner tone="warning" title={t('pages.pdf.annotations_fail')}>
          {annotationsQuery.error instanceof Error ? annotationsQuery.error.message : undefined}
        </Banner>
      )}

      {!canRead || paper.filePath === null ? (
        <EmptyState
          title={t('pages.paper.text_missing_title')}
          description={t('pages.paper.text_missing_reason')}
          action={
            <Button
              variant="secondary"
              onClick={() => {
                if (backHref !== undefined) navigate(backHref)
              }}
            >
              {t('pages.pdf.btn_paper')}
            </Button>
          }
        />
      ) : (
        <div className={styles.pdfShell}>
          <section className={styles.viewerCard} aria-label={t('pages.pdf.reader')}>
            <div className={styles.toolbar}>
              <IconButton
                icon={iconFor('chevronLeft')}
                label={t('pages.pdf.prev_page')}
                size="sm"
                disabled={pageNo <= 1}
                onClick={() => jumpToPage(pageNo - 1)}
              />
              <input
                className={styles.pageInput}
                aria-label={t('pages.pdf.page_label')}
                inputMode="numeric"
                value={pageInput}
                onChange={(event) => setPageInput(event.target.value.replace(/[^\d]/gu, ''))}
                onKeyDown={(event) => {
                  if (event.key !== 'Enter') return
                  const parsed = Number.parseInt(pageInput, 10)
                  if (Number.isFinite(parsed)) jumpToPage(parsed)
                  else setPageInput(String(pageNo))
                }}
                onBlur={() => {
                  const parsed = Number.parseInt(pageInput, 10)
                  if (Number.isFinite(parsed)) jumpToPage(parsed)
                  else setPageInput(String(pageNo))
                }}
              />
              <span className={styles.pageTotal}>
                / {pageCount === 0 ? '—' : pageCount}
              </span>
              <span className={styles.toolbarSpacer} />
              <Button
                variant="ghost"
                size="sm"
                aria-label={t('pages.pdf.zoom_out')}
                disabled={scale <= MIN_ZOOM}
                onClick={() => {
                  setFit('none')
                  setZoom(Math.max(MIN_ZOOM, Number((scale - ZOOM_STEP).toFixed(2))))
                }}
              >
                −
              </Button>
              <span className={styles.pageTotal}>{Math.round(scale * 100)}%</span>
              <Button
                variant="ghost"
                size="sm"
                aria-label={t('pages.pdf.zoom_in')}
                disabled={scale >= MAX_ZOOM}
                onClick={() => {
                  setFit('none')
                  setZoom(Math.min(MAX_ZOOM, Number((scale + ZOOM_STEP).toFixed(2))))
                }}
              >
                +
              </Button>
              <Button
                variant="secondary"
                size="sm"
                aria-pressed={fit === 'width'}
                onClick={() => setFit('width')}
              >
                {t('pages.pdf.fit_width')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                aria-pressed={fit === 'page'}
                onClick={() => setFit('page')}
              >
                {t('pages.pdf.fit_page')}
              </Button>
              {rendering && <span className={styles.muted}>{t('pages.pdf.rendering')}</span>}
            </div>

            {loadError !== null ? (
              <div className={styles.canvasArea}>
                <ErrorState
                  title={t('pages.pdf.load_fail')}
                  message={loadError}
                  onRetry={() => {
                    setLoadError(null)
                    void paperQuery.refetch()
                  }}
                  retryLabel={t('pages.literature.btn_retry')}
                />
              </div>
            ) : (
              <Suspense
                fallback={
                  <div className={styles.canvasArea}>
                    <Skeleton rows={8} />
                  </div>
                }
              >
                <PdfCanvas
                  url={rosAssetUrl(paper.filePath)}
                  pageNo={pageNo}
                  zoom={zoom}
                  fit={fit}
                  annotations={pageAnnotations}
                  focusAnnotationId={focusAnnotationId}
                  onPageCount={onPageCount}
                  onScale={onScale}
                  onSelection={onSelection}
                  onError={onError}
                  onRendering={onRendering}
                />
              </Suspense>
            )}

            {selection !== null && (
              <div className={styles.selectionBar} role="toolbar" aria-label={t('pages.pdf.selection_actions')}>
                <span className={styles.selectionQuote} title={selection.quote}>
                  {selection.quote}
                </span>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={archived || busyAction !== null}
                  onClick={() => void highlight()}
                >
                  {t('pages.pdf.create_highlight')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={archived || busyAction !== null}
                  onClick={() => void appendExcerpt()}
                >
                  {t('pages.pdf.create_excerpt')}
                </Button>
                <Button
                  variant="primary"
                  size="sm"
                  disabled={archived || busyAction !== null}
                  onClick={openEvidence}
                >
                  {t('pages.pdf.create_evidence')}
                </Button>
              </div>
            )}
          </section>

          <aside className={styles.sidebar} aria-label={t('pages.pdf.highlights')}>
            <div className={styles.sidebarHead}>
              <h2 className={styles.sidebarTitle}>{t('pages.pdf.highlights')}</h2>
              <Badge tone="neutral">{annotations.length}</Badge>
            </div>
            <div className={styles.sidebarBody}>
              {annotationsQuery.isPending ? (
                <Skeleton rows={4} />
              ) : annotations.length === 0 ? (
                <p className={styles.muted}>{t('pages.pdf.highlight_empty')}</p>
              ) : (
                annotations.map((annotation) => {
                  const noteHref =
                    annotation.noteId === null
                      ? undefined
                      : routeHref('note', {
                          projectId: resolvedProjectId,
                          entityId: annotation.noteId
                        })
                  const linkedIdeaId =
                    annotation.evidenceId === null
                      ? undefined
                      : evidenceIdea.get(annotation.evidenceId)
                  const ideaHref =
                    linkedIdeaId === undefined
                      ? undefined
                      : routeHref('idea', { projectId: resolvedProjectId, entityId: linkedIdeaId })
                  return (
                    <article key={annotation.id} className={styles.annotation}>
                      <button
                        type="button"
                        className={styles.annotationButton}
                        onClick={() => {
                          setPageNo(annotation.pageNo)
                          setFocusAnnotationId(annotation.id)
                        }}
                      >
                        {annotation.quote}
                      </button>
                      <div className={styles.annotationMeta}>
                        <span>
                          {t('pages.pdf.highlight_page', { page: annotation.pageNo })}
                        </span>
                        {noteHref !== undefined && (
                          <button
                            type="button"
                            className={styles.linkBadge}
                            onClick={() => navigate(noteHref)}
                          >
                            {t('pages.pdf.link_note')}
                          </button>
                        )}
                        {ideaHref !== undefined && (
                          <button
                            type="button"
                            className={styles.linkBadge}
                            onClick={() => navigate(ideaHref)}
                          >
                            {t('pages.pdf.link_evidence')}
                          </button>
                        )}
                      </div>
                    </article>
                  )
                })
              )}
            </div>
          </aside>
        </div>
      )}

      <Modal
        open={evidenceOpen}
        title={t('pages.pdf.evidence_title')}
        onClose={() => {
          if (busyAction === 'evidence') return
          setEvidenceOpen(false)
        }}
        footer={
          <>
            <Button
              variant="secondary"
              disabled={busyAction === 'evidence'}
              onClick={() => setEvidenceOpen(false)}
            >
              {t('pages.pdf.btn_cancel')}
            </Button>
            <Button
              variant="primary"
              disabled={busyAction === 'evidence' || evidenceIdeaId === ''}
              onClick={() => void submitEvidence()}
            >
              {t('pages.pdf.evidence_create')}
            </Button>
          </>
        }
      >
        <div className={styles.dialogForm}>
          {ideasQuery.isPending ? (
            <Skeleton rows={3} />
          ) : ideas.length === 0 ? (
            <Banner tone="warning">{t('pages.pdf.evidence_no_ideas')}</Banner>
          ) : (
            <SelectField
              label={t('pages.pdf.evidence_idea')}
              value={evidenceIdeaId}
              options={[
                { value: '', label: t('pages.pdf.evidence_choose_idea') },
                ...ideas.map((idea) => ({ value: idea.id, label: idea.title }))
              ]}
              onChange={setEvidenceIdeaId}
            />
          )}
          <SelectField
            label={t('pages.pdf.evidence_direction')}
            value={evidenceDirection}
            options={[
              { value: 'support', label: t('pages.pdf.direction_support') },
              { value: 'oppose', label: t('pages.pdf.direction_oppose') }
            ]}
            onChange={(value) => setEvidenceDirection(value as EvidenceDirection)}
          />
          <label className={styles.field}>
            <span className={styles.label}>{t('pages.pdf.evidence_content')}</span>
            <textarea
              className={styles.textarea}
              rows={5}
              value={evidenceContent}
              onChange={(event) => setEvidenceContent(event.target.value)}
            />
          </label>
          {evidenceError !== null && (
            <p className={styles.errorText} role="alert">
              {evidenceError}
            </p>
          )}
        </div>
      </Modal>
    </Page>
  )
}