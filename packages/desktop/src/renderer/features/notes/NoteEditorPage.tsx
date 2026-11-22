import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import type { AttachmentInfo, DbRecord } from '@researchos/shared'

import {
  Banner,
  Button,
  EmptyState,
  ErrorState,
  IconButton,
  Modal,
  Skeleton,
  TextInput,
  Tabs
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { ipc } from '../../lib/ipc'
import { queryKeys, registerFlushHandler } from '../../lib/query'
import { routeHref } from '../../routes'
import { createBodySaver, type BodySaver, type SaveConflict, type SaveState } from './body-saver'
import { MarkdownEditor } from './MarkdownEditor'
import { MarkdownPreview } from './MarkdownPreview'
import { notesApi } from './notes-api'
import { parseRosTarget, routeKindForReference } from './reference'
import styles from './notes.module.css'

export interface NoteEditorPageProps {
  readonly projectId?: string
  readonly entityId?: string
}

type EditorMode = 'source' | 'split' | 'preview'

const MODE_LABEL: Readonly<Record<EditorMode, string>> = {
  source: 'pages.notes.mode_source',
  split: 'pages.notes.mode_split',
  preview: 'pages.notes.mode_preview'
}

/** Note editor: metadata, Markdown source/preview, debounced hash-checked saves. */
export default function NoteEditorPage({
  projectId: routeProjectId,
  entityId
}: NoteEditorPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const current = useCurrentProject()
  const projectId = routeProjectId ?? current.projectId
  const noteId = entityId ?? ''

  const [markdown, setMarkdown] = useState('')
  const [mode, setMode] = useState<EditorMode>('split')
  const [saveState, setSaveState] = useState<SaveState>('saved')
  const [conflict, setConflict] = useState<SaveConflict | null>(null)
  const [conflictBusy, setConflictBusy] = useState(false)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [promoteOpen, setPromoteOpen] = useState(false)
  const [promoteDatabaseId, setPromoteDatabaseId] = useState('')
  const [tagsOpen, setTagsOpen] = useState(false)
  const [tagDraft, setTagDraft] = useState('')
  const [detachOpen, setDetachOpen] = useState(false)
  const saverRef = useRef<BodySaver | null>(null)
  const loadedNoteRef = useRef<string | null>(null)
  const loadedBodyRef = useRef<string | null>(null)

  const noteQuery = useQuery({
    queryKey: queryKeys.notes.detail(projectId ?? 'none', noteId),
    queryFn: () => notesApi.get(projectId ?? '', noteId),
    enabled: projectId !== null && noteId !== ''
  })
  const bodyQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'body', noteId],
    queryFn: () => notesApi.body(projectId ?? '', noteId),
    enabled: projectId !== null && noteId !== ''
  })
  const note = noteQuery.data ?? null
  const body = bodyQuery.data ?? null

  const linksQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'links', noteId],
    queryFn: () => notesApi.outbound(projectId ?? '', noteId),
    enabled: projectId !== null && noteId !== ''
  })
  const attachmentsQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'attachments', noteId],
    queryFn: () => notesApi.attachments(projectId ?? '', noteId),
    enabled: projectId !== null && noteId !== ''
  })
  const databasesQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'databases'],
    queryFn: () => notesApi.databases(projectId ?? ''),
    enabled: projectId !== null && promoteOpen
  })
  const workspaceQuery = useQuery({
    queryKey: queryKeys.workspace.status(),
    queryFn: () => ipc.invoke('workspace.status')
  })
  const tagsQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'tags'],
    queryFn: () => notesApi.listTags(projectId ?? ''),
    enabled: projectId !== null && tagsOpen,
    retry: 0
  })

  const archived = current.archived
  const deleted = note?.deletedAt !== null && note?.deletedAt !== undefined
  const missing = body?.state === 'missing' || note?.fileState === 'missing'
  const readOnly = archived || deleted || missing

  useEffect(() => {
    if (note === null) return
    if (loadedNoteRef.current === note.id) return
    loadedNoteRef.current = note.id
    setTitle(note.title)
    setDescription(note.description)
  }, [note])

  useEffect(() => {
    if (body === null || loadedBodyRef.current === body.noteId) return
    loadedBodyRef.current = body.noteId
    setMarkdown(body.markdown)
  }, [body])

  useEffect(() => {
    if (body === null || projectId === null || noteId === '') return
    const saver = createBodySaver({
      projectId,
      noteId,
      initialHash: body.hash,
      onState: setSaveState,
      onConflict: (next) => {
        setConflict(next)
      },
      onSaved: () => {
        void queryClient.invalidateQueries({ queryKey: queryKeys.notes.detail(projectId, noteId) })
        void queryClient.invalidateQueries({ queryKey: ['notes', projectId, 'list'] })
      }
    })
    saverRef.current = saver
    const detachFlush = registerFlushHandler(() => saver.flush())
    return () => {
      detachFlush()
      saver.dispose()
      saverRef.current = null
    }
    // The token comes from the first successful body read; conflict recovery
    // replaces it through acceptDisk/overwrite, never by rebuilding the saver.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [body?.noteId, projectId, noteId])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault()
        void saverRef.current?.flush()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const changeMarkdown = useCallback((next: string) => {
    setMarkdown(next)
    if (readOnly) return
    saverRef.current?.schedule(next)
  }, [readOnly])

  const openEntity = useCallback(
    (type: string, id: string): void => {
      const kind = routeKindForReference(type)
      const path =
        kind === null ? undefined : routeHref(kind, { projectId: projectId ?? undefined, entityId: id })
      if (path === undefined) {
        setNotice(t('pages.notes.toast_link_missing'))
        return
      }
      navigate(path)
    },
    [navigate, projectId, t]
  )

  const openReference = useCallback(
    (href: string) => {
      const target = parseRosTarget(href)
      if (target === null) {
        setNotice(t('pages.notes.toast_link_missing'))
        return
      }
      openEntity(target.type, target.id)
    },
    [openEntity, t]
  )

  const refreshNote = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.notes.all(projectId ?? '') })
  }

  const saveMeta = async (patch: { title?: string; description?: string }): Promise<void> => {
    if (note === null || projectId === null || readOnly) return
    try {
      const updated = await notesApi.updateMeta(projectId, note.id, patch, note.updatedAt)
      setTitle(updated.title)
      setDescription(updated.description)
      await queryClient.invalidateQueries({ queryKey: queryKeys.notes.detail(projectId, note.id) })
      setNotice(t('pages.notes.toast_meta_saved'))
    } catch (cause) {
      setError(cause)
    }
  }

  const acceptDisk = async (): Promise<void> => {
    if (projectId === null) return
    setConflictBusy(true)
    try {
      const fresh = await notesApi.body(projectId, noteId)
      setMarkdown(fresh.markdown)
      saverRef.current?.acceptDisk(fresh.markdown, fresh.hash)
      queryClient.setQueryData(['notes', projectId, 'body', noteId], fresh)
      setConflict(null)
    } catch (cause) {
      setError(cause)
    } finally {
      setConflictBusy(false)
    }
  }

  const overwrite = async (): Promise<void> => {
    if (conflict === null) return
    setConflictBusy(true)
    try {
      await saverRef.current?.overwrite(conflict)
      setConflict(null)
    } catch (cause) {
      setError(cause)
    } finally {
      setConflictBusy(false)
    }
  }

  const insertImage = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    try {
      const picked = await ipc.invoke('app.pickFile', {
        title: t('pages.notes.image_insert'),
        filters: [{ name: 'Images', extensions: ['png', 'jpg', 'jpeg', 'webp', 'svg'] }]
      })
      if (picked.canceled || picked.paths.length === 0) return
      const attachment = await notesApi.addAttachment(projectId, noteId, picked.paths[0] ?? '')
      const path = `../attachments/${noteId}/${attachment.fileName}`
      const image = `\n![${attachment.fileName}](${path})\n`
      const next = `${markdown}${image}`
      setMarkdown(next)
      saverRef.current?.schedule(next)
      await queryClient.invalidateQueries({
        queryKey: ['notes', projectId, 'attachments', noteId]
      })
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const uploadAttachment = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    try {
      const picked = await ipc.invoke('app.pickFile', {
        title: t('pages.notes.attach_add')
      })
      if (picked.canceled || picked.paths.length === 0) return
      await notesApi.addAttachment(projectId, noteId, picked.paths[0] ?? '')
      await queryClient.invalidateQueries({
        queryKey: ['notes', projectId, 'attachments', noteId]
      })
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const addTags = async (): Promise<void> => {
    if (projectId === null) return
    const names = tagDraft
      .split(',')
      .map((entry) => entry.trim())
      .filter((entry) => entry !== '')
    if (names.length === 0) return
    setBusy(true)
    try {
      await notesApi.addTags(projectId, noteId, names)
      setTagDraft('')
      setTagsOpen(false)
      setNotice(t('pages.notes.toast_tags_saved'))
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const removeTags = async (): Promise<void> => {
    if (projectId === null) return
    const names = tagDraft
      .split(',')
      .map((entry) => entry.trim())
      .filter((entry) => entry !== '')
    if (names.length === 0) return
    setBusy(true)
    try {
      await notesApi.removeTags(projectId, noteId, names)
      setTagDraft('')
      setTagsOpen(false)
      setNotice(t('pages.notes.toast_tags_saved'))
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const promote = async (): Promise<void> => {
    if (projectId === null || promoteDatabaseId === '') return
    setBusy(true)
    setError(null)
    try {
      await notesApi.promote(projectId, noteId, promoteDatabaseId)
      setPromoteOpen(false)
      setNotice(t('pages.notes.toast_promoted'))
      await refreshNote()
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  const detach = async (): Promise<void> => {
    if (projectId === null) return
    setBusy(true)
    setError(null)
    try {
      const record = await findRecordForNote(projectId, noteId)
      if (record === null) {
        setNotice(t('pages.notes.detach_missing'))
        setDetachOpen(false)
        return
      }
      await notesApi.detach(projectId, record.id)
      setDetachOpen(false)
      setNotice(t('pages.notes.toast_detached'))
      await refreshNote()
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  if (projectId === null || noteId === '') {
    return <EmptyState title={t('pages.notes.needs_project_title')} description={t('pages.notes.needs_project_reason')} />
  }
  if (noteQuery.isPending || bodyQuery.isPending) return <Skeleton rows={8} />
  if (noteQuery.isError) {
    return (
      <ErrorState
        title={t('pages.notes.banner_problem')}
        message={noteQuery.error.message}
        onRetry={() => void noteQuery.refetch()}
        retryLabel={t('shell.overview.error_retry')}
      />
    )
  }
  if (bodyQuery.isError) {
    return (
      <ErrorState
        title={t('pages.notes.missing_title')}
        message={bodyQuery.error.message}
        onRetry={() => void bodyQuery.refetch()}
        retryLabel={t('shell.overview.error_retry')}
      />
    )
  }
  if (note === null) {
    return <EmptyState title={t('pages.notes.not_found_title')} description={t('pages.notes.not_found_reason')} />
  }

  const unresolved = new Set(
    (linksQuery.data ?? []).filter((link) => !link.resolved).map((link) => link.href)
  )
  const heading = note.title === '' ? t('pages.notes.untitled') : note.title
  const statusKey =
    saveState === 'saving'
      ? 'pages.notes.state_saving'
      : saveState === 'unsaved'
        ? 'pages.notes.state_unsaved'
        : saveState === 'conflict'
          ? 'pages.notes.state_conflict'
          : saveState === 'error'
            ? 'pages.notes.state_error'
            : 'pages.notes.state_saved'

  return (
    <div className={styles.editorPage}>
      <header className={styles.editorHeader}>
        <div className={styles.editorHeading}>
          <Button variant="ghost" size="sm" icon={iconFor('back')} onClick={() => navigate(-1)}>
            {t('pages.notes.btn_back')}
          </Button>
          <input
            className={styles.titleInput}
            aria-label={t('pages.notes.field_note_title')}
            value={title}
            readOnly={readOnly}
            onChange={(event) => setTitle(event.target.value)}
            onBlur={() => {
              if (!readOnly && title !== note.title) void saveMeta({ title })
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') event.currentTarget.blur()
            }}
          />
          <span className={styles.saveState} data-state={saveState}>
            {t(statusKey)}
          </span>
        </div>
        <div className={styles.editorActions} role="toolbar" aria-label={t('pages.notes.editor_title')}>
          <Tabs
            label={t('pages.notes.mode_label')}
            items={(['source', 'split', 'preview'] as const).map((id) => ({ id, label: t(MODE_LABEL[id]) }))}
            value={mode}
            onChange={(id) => setMode(id as EditorMode)}
          />
          <Button variant="ghost" size="sm" icon={iconFor('fileText')} disabled={readOnly || busy} onClick={() => void insertImage()}>
            {t('pages.notes.image_insert')}
          </Button>
          <Button variant="ghost" size="sm" disabled={readOnly} onClick={() => setTagsOpen(true)}>
            {t('pages.notes.tags_title')}
          </Button>
          <Button variant="ghost" size="sm" disabled={readOnly} onClick={() => setPromoteOpen(true)}>
            {t('pages.notes.menu_promote')}
          </Button>
          <Button variant="ghost" size="sm" disabled={readOnly} onClick={() => setDetachOpen(true)}>
            {t('pages.notes.detach')}
          </Button>
        </div>
      </header>

      {archived && <Banner tone="warning" title={t('pages.notes.readonly_message')} />}
      {deleted && (
        <Banner tone="warning" title={t('pages.notes.deleted_title')}>
          {t('pages.notes.deleted_message')}
        </Banner>
      )}
      {missing && <Banner tone="warning" title={t('pages.notes.missing_title')}>{t('pages.notes.missing_message')}</Banner>}
      {notice !== null && (
        <Banner tone="info" action={<Button variant="ghost" size="sm" onClick={() => setNotice(null)}>{t('pages.notes.btn_cancel')}</Button>}>
          {notice}
        </Banner>
      )}
      {error !== null && (
        <Banner tone="danger" title={t('pages.notes.banner_problem')}>
          {error instanceof Error ? error.message : String(error)}
        </Banner>
      )}

      <div className={styles.metaRow}>
        <TextInput
          label={t('pages.notes.field_note_description')}
          value={description}
          readOnly={readOnly}
          onChange={setDescription}
          onBlur={() => {
            if (!readOnly && description !== note.description) void saveMeta({ description })
          }}
        />
      </div>

      <div className={styles.editorBody} data-mode={mode}>
        {mode !== 'preview' && (
          <div className={styles.editorPane}>
            <MarkdownEditor
              value={markdown}
              projectId={projectId}
              readOnly={readOnly}
              onChange={changeMarkdown}
              onSave={() => void saverRef.current?.flush()}
              onOpenReference={openReference}
            />
          </div>
        )}
        {mode !== 'source' && (
          <div className={styles.previewPane}>
            <MarkdownPreview
              markdown={markdown}
              noteFilePath={note.filePath}
              workspaceRoot={workspaceQuery.data?.root}
              unresolved={unresolved}
              onOpenReference={openReference}
            />
          </div>
        )}
      </div>

      <aside className={styles.sidePanels}>
        <section className={styles.sidePanel}>
          <h2 className={styles.sideTitle}>{t('pages.notes.links')}</h2>
          {linksQuery.isError ? (
            <p className={styles.warnText}>{linksQuery.error.message}</p>
          ) : (linksQuery.data ?? []).length === 0 ? (
            <p className={styles.muted}>{t('pages.notes.links_empty')}</p>
          ) : (
            <ul className={styles.linkList}>
              {(linksQuery.data ?? []).map((link) => (
                <li key={link.href}>
                  {link.resolved ? (
                    <button
                      type="button"
                      className={styles.linkButton}
                      onClick={() => {
                        if (link.type !== null && link.targetId !== null) {
                          openEntity(link.type, link.targetId)
                          return
                        }
                        openReference(link.href)
                      }}
                    >
                      {link.href}
                    </button>
                  ) : (
                    <span className={styles.deletedRef} title={link.href}>
                      {link.href} — {t('pages.notes.link_unresolved')}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>
        <section className={styles.sidePanel}>
          <div className={styles.sideHead}>
            <h2 className={styles.sideTitle}>{t('pages.notes.attachments')}</h2>
            {!readOnly && (
              <Button variant="ghost" size="sm" disabled={busy} onClick={() => void uploadAttachment()}>
                {t('pages.notes.attach_add')}
              </Button>
            )}
          </div>
          {attachmentsQuery.isError ? (
            <p className={styles.warnText}>{attachmentsQuery.error.message}</p>
          ) : (attachmentsQuery.data ?? []).length === 0 ? (
            <p className={styles.muted}>{t('pages.notes.attachments_empty')}</p>
          ) : (
            <ul className={styles.linkList}>
              {(attachmentsQuery.data ?? []).map((attachment: AttachmentInfo) => (
                <li key={attachment.id}>
                  <span>{attachment.fileName}</span>
                  <span className={styles.muted}>{formatBytes(attachment.bytes)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </aside>

      <Modal
        open={conflict !== null}
        title={t('pages.notes.conflict_title')}
        onClose={() => undefined}
        escapeCloses={false}
        footer={
          <>
            <Button variant="secondary" disabled={conflictBusy} onClick={() => void acceptDisk()}>
              {t('pages.notes.btn_accept_disk')}
            </Button>
            <Button variant="danger" disabled={conflictBusy} onClick={() => void overwrite()}>
              {t('pages.notes.btn_overwrite')}
            </Button>
          </>
        }
      >
        <p>{t('pages.notes.conflict_message')}</p>
      </Modal>

      <Modal
        open={promoteOpen}
        title={t('pages.notes.promote_title')}
        onClose={() => setPromoteOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setPromoteOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button variant="primary" disabled={busy || promoteDatabaseId === ''} onClick={() => void promote()}>
              {t('pages.notes.menu_promote')}
            </Button>
          </>
        }
      >
        <DatabasePicker
          value={promoteDatabaseId}
          options={(databasesQuery.data ?? []).map((database) => ({ value: database.id, label: database.name }))}
          onChange={setPromoteDatabaseId}
        />
      </Modal>

      <Modal
        open={tagsOpen}
        title={t('pages.notes.tags_title')}
        onClose={() => setTagsOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setTagsOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => void removeTags()}>
              {t('pages.notes.tags_remove')}
            </Button>
            <Button variant="primary" disabled={busy} onClick={() => void addTags()}>
              {t('pages.notes.tags_add')}
            </Button>
          </>
        }
      >
        <TextInput
          label={t('pages.notes.tags_input')}
          value={tagDraft}
          onChange={setTagDraft}
          hint={t('pages.notes.tags_hint')}
        />
        {(tagsQuery.data ?? []).length > 0 && (
          <div className={styles.tagRow}>
            {(tagsQuery.data ?? []).map((tag) => (
              <button
                key={tag.id}
                type="button"
                className={styles.tag}
                onClick={() => setTagDraft((current) => (current.trim() === '' ? tag.name : `${current}, ${tag.name}`))}
              >
                {tag.name}
              </button>
            ))}
          </div>
        )}
      </Modal>

      <Modal
        open={detachOpen}
        title={t('pages.notes.detach')}
        onClose={() => setDetachOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDetachOpen(false)}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button variant="danger" disabled={busy} onClick={() => void detach()}>
              {t('pages.notes.detach_confirm')}
            </Button>
          </>
        }
      >
        <p>{t('pages.notes.detach_message')}</p>
      </Modal>
    </div>
  )
}

function DatabasePicker({
  value,
  options,
  onChange
}: {
  readonly value: string
  readonly options: ReadonlyArray<{ value: string; label: string }>
  readonly onChange: (value: string) => void
}): JSX.Element {
  if (options.length === 0) {
    return <p />
  }
  return (
    <ul className={styles.pickList}>
      {options.map((option) => (
        <li key={option.value}>
          <button
            type="button"
            className={[styles.pickRow, value === option.value ? styles.pickActive : ''].join(' ')}
            onClick={() => onChange(option.value)}
          >
            {option.label}
          </button>
        </li>
      ))}
    </ul>
  )
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} kB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * findRecordForNote discovers the database record whose page is this note.
 * The notes contract has no reverse lookup, so detach asks each database's
 * records; a note that is not promoted detaches nothing.
 */
async function findRecordForNote(projectId: string, noteId: string): Promise<DbRecord | null> {
  const databases = await notesApi.databases(projectId)
  for (const database of databases) {
    const page = await notesApi.records(projectId, { databaseId: database.id, limit: 500 })
    const found = page.items.find((record) => record.noteId === noteId)
    if (found !== undefined) return found
  }
  return null
}

