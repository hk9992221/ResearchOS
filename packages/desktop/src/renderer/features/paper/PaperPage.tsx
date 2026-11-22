/**
 * The paper page: the Paper Note's six structured fields and its Markdown
 * body, plus the notes and ideas the paper is linked to. The body autosaves
 * after 600 ms with `baseHash`; a conflict shows the shared banner and only an
 * explicit reload (adopt disk) or overwrite (retry with the disk token) moves
 * forward. Structured fields save with `expectedUpdatedAt`.
 */

import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import {
  EMPTY_PAPER_NOTE_FIELDS,
  AppError,
  AppErrorCode,
  type PaperNoteFields,
  type PaperNoteView
} from '@researchos/shared'

import {
  Badge,
  Banner,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Page,
  PageHeader,
  Skeleton
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { pushToast } from '../../state/toasts'
import { paperApi } from './api'
import { paperDisplay } from '../literature/model'
import styles from './paper.module.css'

const BODY_DEBOUNCE_MS = 600

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

type ConflictKind = 'fields' | 'body'

interface ConflictState {
  readonly kind: ConflictKind
  readonly message: string
}

interface RelatedProps {
  readonly projectId: string
  readonly paperId: string
}

function RelatedPanel({ projectId, paperId }: RelatedProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const contextQuery = useQuery({
    queryKey: ['project', projectId, 'context', 'paper', paperId],
    queryFn: () => paperApi.context(projectId, paperId)
  })

  const open = (kind: 'note' | 'idea', id: string): void => {
    const href = routeHref(kind, { projectId, entityId: id })
    if (href !== undefined) navigate(href)
  }

  if (contextQuery.isPending) return <Skeleton rows={4} />
  if (contextQuery.isError) {
    return (
      <Banner tone="warning" title={t('pages.paper.related_error')}>
        {contextQuery.error instanceof Error ? contextQuery.error.message : undefined}
      </Banner>
    )
  }
  const context = contextQuery.data
  const notes = context.backlinks
  const ideas = context.ideas.filter((idea) => !idea.self)

  return (
    <div className={styles.grid}>
      <section>
        <h3 className={styles.label}>{t('pages.paper.card_related')}</h3>
        {notes.length === 0 ? (
          <p className={styles.muted}>{t('pages.paper.related_notes_empty')}</p>
        ) : (
          <ul className={styles.relatedList}>
            {notes.map((note) => (
              <li key={note.noteId} className={styles.relatedItem}>
                <button
                  type="button"
                  className={styles.relatedButton}
                  onClick={() => open('note', note.noteId)}
                >
                  {note.noteTitle}
                </button>
                <span className={styles.muted}>{note.snippet}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section>
        <h3 className={styles.label}>{t('pages.paper.related_ideas')}</h3>
        {ideas.length === 0 ? (
          <p className={styles.muted}>{t('pages.paper.related_ideas_empty')}</p>
        ) : (
          <ul className={styles.relatedList}>
            {ideas.map((idea) => (
              <li key={idea.id} className={styles.relatedItem}>
                <button
                  type="button"
                  className={styles.relatedButton}
                  onClick={() => open('idea', idea.id)}
                >
                  {idea.title}
                </button>
                <span className={styles.muted}>
                  <Badge tone="neutral">{idea.status}</Badge>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

export default function PaperPage({
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

  const paperQuery = useQuery({
    queryKey: queryKeys.literature.paper(resolvedProjectId ?? 'none', paperId),
    queryFn: () => paperApi.get(resolvedProjectId as string, paperId),
    enabled: resolvedProjectId !== null && paperId !== ''
  })

  const noteQuery = useQuery({
    queryKey: ['literature', resolvedProjectId ?? 'none', 'paperNote', paperId],
    queryFn: () => paperApi.note(resolvedProjectId as string, paperId),
    enabled: resolvedProjectId !== null && paperId !== ''
  })

  const [fields, setFields] = useState<PaperNoteFields>({ ...EMPTY_PAPER_NOTE_FIELDS })
  const [body, setBody] = useState('')
  const [bodyHash, setBodyHash] = useState<string | null>(null)
  const [fieldsDirty, setFieldsDirty] = useState(false)
  const [bodyDirty, setBodyDirty] = useState(false)
  const [savingFields, setSavingFields] = useState(false)
  const [savingBody, setSavingBody] = useState(false)
  const [savedFieldsAt, setSavedFieldsAt] = useState<string | null>(null)
  const [savedBodyAt, setSavedBodyAt] = useState<string | null>(null)
  const [conflict, setConflict] = useState<ConflictState | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const loadedKey = useRef<string | null>(null)

  const note: PaperNoteView | null = noteQuery.data ?? null
  const noteKey = note === null ? '' : `${note.note.id}:${note.note.updatedAt}`

  useEffect(() => {
    if (noteQuery.data === undefined) return
    if (fieldsDirty || bodyDirty) return
    if (loadedKey.current === noteKey) return
    loadedKey.current = noteKey
    setFields(noteQuery.data?.fields ?? { ...EMPTY_PAPER_NOTE_FIELDS })
    setBody(noteQuery.data?.bodyMd ?? '')
    setBodyHash(noteQuery.data?.bodyHash ?? null)
    setConflict(null)
    setSaveError(null)
  }, [noteQuery.data, noteKey, fieldsDirty, bodyDirty])

  const saveBody = async (hash?: string): Promise<void> => {
    if (resolvedProjectId === null || note === null) return
    const base = hash ?? bodyHash
    if (base === null || base === '') {
      setSaveError(t('pages.paper.banner_save_fail'))
      return
    }
    setSavingBody(true)
    setSaveError(null)
    try {
      const saved = await paperApi.saveBody(resolvedProjectId, paperId, body, base)
      setBodyHash(saved.fileHash)
      setSavedBodyAt(saved.savedAt)
      setBodyDirty(false)
      setConflict(null)
    } catch (cause) {
      if (cause instanceof AppError && cause.code === AppErrorCode.Conflict) {
        setConflict({ kind: 'body', message: cause.message })
      } else {
        setSaveError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      setSavingBody(false)
    }
  }

  useEffect(() => {
    if (!bodyDirty || conflict !== null) return
    const timer = window.setTimeout(() => void saveBody(), BODY_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
    // The debounce is per keystroke; saveBody reads the latest body.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [body, bodyDirty, conflict])

  const saveFields = async (force = false): Promise<void> => {
    if (resolvedProjectId === null || note === null) return
    setSavingFields(true)
    setSaveError(null)
    try {
      const saved = await paperApi.saveFields(
        resolvedProjectId,
        paperId,
        fields,
        force ? undefined : note.note.updatedAt
      )
      setFields(saved.fields)
      queryClient.setQueryData(['literature', resolvedProjectId, 'paperNote', paperId], saved)
      setFieldsDirty(false)
      setSavedFieldsAt(saved.note.updatedAt)
      setConflict(null)
      pushToast({ tone: 'success', title: t('pages.paper.toast_fields_saved') })
    } catch (cause) {
      if (cause instanceof AppError && cause.code === AppErrorCode.Conflict) {
        setConflict({ kind: 'fields', message: cause.message })
      } else {
        setSaveError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      setSavingFields(false)
    }
  }

  const createNote = async (): Promise<void> => {
    if (resolvedProjectId === null) return
    setCreating(true)
    setSaveError(null)
    try {
      const created = await paperApi.createNote(resolvedProjectId, paperId)
      queryClient.setQueryData(['literature', resolvedProjectId, 'paperNote', paperId], created)
      loadedKey.current = `${created.note.id}:${created.note.updatedAt}`
      setFields(created.fields)
      setBody(created.bodyMd)
      setBodyHash(created.bodyHash)
      setFieldsDirty(false)
      setBodyDirty(false)
    } catch (cause) {
      setSaveError(cause instanceof Error ? cause.message : t('pages.paper.banner_create_fail'))
    } finally {
      setCreating(false)
    }
  }

  const reloadNote = async (): Promise<void> => {
    const fresh = await noteQuery.refetch()
    setFields(fresh.data?.fields ?? { ...EMPTY_PAPER_NOTE_FIELDS })
    setBody(fresh.data?.bodyMd ?? '')
    setBodyHash(fresh.data?.bodyHash ?? null)
    setFieldsDirty(false)
    setBodyDirty(false)
    setConflict(null)
    setSaveError(null)
  }

  const overwrite = async (): Promise<void> => {
    if (conflict?.kind === 'fields') {
      await saveFields(true)
      return
    }
    const fresh = await paperApi.note(resolvedProjectId as string, paperId)
    const hash = fresh?.bodyHash ?? null
    setBodyHash(hash)
    await saveBody(hash ?? undefined)
  }

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
        <PageHeader title={t('pages.paper.title')} />
        <Skeleton rows={8} />
      </Page>
    )
  }

  if (paperQuery.isError) {
    return (
      <Page>
        <ErrorState
          title={t('pages.paper.paper_missing_title')}
          message={paperQuery.error instanceof Error ? paperQuery.error.message : undefined}
          onRetry={() => void paperQuery.refetch()}
          retryLabel={t('pages.literature.btn_retry')}
        />
      </Page>
    )
  }

  const paper = paperQuery.data
  const display = paperDisplay(paper)
  const citation = [display.authors, display.year === null ? '' : String(display.year), display.venue]
    .filter((part) => part !== '')
    .join(' · ')
  const noteDeleted = note !== null && note.note.deletedAt !== null
  const readonly = archived || noteDeleted
  const pdfHref = routeHref('pdf', { projectId: resolvedProjectId, entityId: paperId })

  return (
    <Page>
      <PageHeader
        title={display.title || t('pages.paper.untitled')}
        description={citation === '' ? undefined : citation}
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                const href = routeHref('literature', { projectId: resolvedProjectId })
                if (href !== undefined) navigate(href)
              }}
            >
              {t('pages.paper.btn_back')}
            </Button>
            <Button
              variant="primary"
              disabled={paper.fileState === 'missing' || pdfHref === undefined}
              onClick={() => {
                if (pdfHref !== undefined) navigate(pdfHref)
              }}
            >
              {t('pages.paper.btn_open_pdf')}
            </Button>
          </>
        }
      />

      {archived && <Banner tone="warning">{t('pages.literature.archived_banner')}</Banner>}
      {noteDeleted && (
        <Banner tone="warning" title={t('pages.paper.note_deleted_title')}>
          {t('pages.paper.note_deleted_message')}
        </Banner>
      )}
      {conflict !== null && (
        <Banner
          tone="warning"
          title={t('pages.paper.conflict_title')}
          action={
            <div className={styles.conflictActions}>
              <Button variant="secondary" size="sm" onClick={() => void reloadNote()}>
                {t('pages.paper.btn_reload')}
              </Button>
              <Button variant="danger" size="sm" onClick={() => void overwrite()}>
                {t('pages.paper.btn_overwrite')}
              </Button>
            </div>
          }
        >
          {t('pages.paper.conflict_message')}
        </Banner>
      )}
      {saveError !== null && conflict === null && (
        <Banner tone="danger" title={t('pages.paper.banner_save_fail')}>
          {saveError}
        </Banner>
      )}

      <div className={styles.columns}>
        <div className={styles.stack}>
          <Card
            title={t('pages.paper.card_note')}
            actions={
              note === null ? undefined : (
                <div className={styles.cardActions}>
                  {savingFields && <span className={styles.saveState}>{t('pages.paper.state_saving')}</span>}
                  {!savingFields && fieldsDirty && (
                    <span className={styles.saveState}>{t('pages.paper.state_unsaved')}</span>
                  )}
                  {!savingFields && !fieldsDirty && savedFieldsAt !== null && (
                    <span className={styles.saveState}>{t('pages.paper.state_saved')}</span>
                  )}
                  <Button
                    variant="primary"
                    size="sm"
                    disabled={readonly || savingFields || !fieldsDirty}
                    onClick={() => void saveFields()}
                  >
                    {t('pages.paper.btn_save_fields')}
                  </Button>
                </div>
              )
            }
          >
            {note === null ? (
              <EmptyState
                title={t('pages.paper.note_missing_title')}
                description={t('pages.paper.note_missing_reason')}
                action={
                  <Button
                    variant="primary"
                    disabled={readonly || creating}
                    onClick={() => void createNote()}
                  >
                    {t('pages.paper.note_create')}
                  </Button>
                }
              />
            ) : (
              <div className={styles.formGrid}>
                {FIELD_KEYS.map((field) => (
                  <label key={field} className={styles.field}>
                    <span className={styles.label}>{t(FIELD_LABEL[field])}</span>
                    <textarea
                      className={styles.textarea}
                      aria-label={t(FIELD_LABEL[field])}
                      value={fields[field]}
                      disabled={readonly}
                      onChange={(event) => {
                        setFields((current) => ({ ...current, [field]: event.target.value }))
                        setFieldsDirty(true)
                        setSaveError(null)
                      }}
                    />
                  </label>
                ))}
              </div>
            )}
          </Card>

          {note !== null && (
            <Card
              title={t('pages.paper.field_body')}
              actions={
                <div className={styles.cardActions}>
                  {savingBody && <span className={styles.saveState}>{t('pages.paper.state_saving')}</span>}
                  {!savingBody && bodyDirty && (
                    <span className={styles.saveState}>{t('pages.paper.state_unsaved')}</span>
                  )}
                  {!savingBody && !bodyDirty && savedBodyAt !== null && (
                    <span className={styles.saveState}>{t('pages.paper.state_saved')}</span>
                  )}
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={readonly || savingBody || !bodyDirty}
                    onClick={() => void saveBody()}
                  >
                    {t('pages.paper.btn_save_note')}
                  </Button>
                </div>
              }
            >
              <label className={styles.field}>
                <span className={styles.srOnly}>{t('pages.paper.field_body')}</span>
                <textarea
                  className={`${styles.textarea} ${styles.bodyTextarea}`}
                  aria-label={t('pages.paper.field_body')}
                  value={body}
                  disabled={readonly}
                  onChange={(event) => {
                    setBody(event.target.value)
                    setBodyDirty(true)
                    setSaveError(null)
                  }}
                />
              </label>
            </Card>
          )}
        </div>

        <Card title={t('pages.paper.card_links')}>
          <RelatedPanel projectId={resolvedProjectId} paperId={paperId} />
        </Card>
      </div>
    </Page>
  )
}