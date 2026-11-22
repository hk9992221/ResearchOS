import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'

import type { Note, NoteKind } from '@researchos/shared'

import {
  Banner,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  IconButton,
  Modal,
  Page,
  PageHeader,
  Skeleton,
  Tabs,
  TextInput,
  type TabItem
} from '../../design/components'
import { useCurrentProject } from '../../app/useCurrentProject'
import { iconFor } from '../../app/icons'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { DatabaseView } from './DatabaseView'
import { notesApi } from './notes-api'
import { formatRelative } from './format'
import styles from './notes.module.css'

type NotesTab = NoteKind | 'databases'

const KINDS: readonly NoteKind[] = ['scratchpad', 'paper_note', 'overview']

const TAB_LABEL: Readonly<Record<NotesTab, string>> = {
  scratchpad: 'pages.notes.tab_scratchpad',
  paper_note: 'pages.notes.tab_paper_note',
  overview: 'pages.notes.tab_overview',
  db_record: 'pages.notes.tab_database',
  databases: 'pages.notes.tab_database'
}

/** noteKindLabel names the kind filter tabs and a row's kind badge. */
export function noteKindLabel(kind: NoteKind): string {
  switch (kind) {
    case 'scratchpad':
      return 'pages.notes.tab_scratchpad'
    case 'paper_note':
      return 'pages.notes.tab_paper_note'
    case 'overview':
      return 'pages.notes.tab_overview'
    case 'db_record':
      return 'pages.notes.tab_database'
  }
}

interface DeleteTarget {
  readonly note: Note
}

export interface NotesPageProps {
  readonly projectId?: string
}

/**
 * Research Notes: the note list (kind tabs, search, tags) and the Research
 * Database view. "Databases" is a sibling mode, not a note kind: the rows of a
 * database are records whose page is a `db_record` note.
 */
export default function NotesPage({ projectId: routeProjectId }: NotesPageProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const current = useCurrentProject()
  const projectId = routeProjectId ?? current.projectId
  const archived = current.archived

  const [searchParams] = useSearchParams()
  const [tab, setTab] = useState<NotesTab>('scratchpad')
  const [search, setSearch] = useState('')
  const [tags, setTags] = useState<readonly string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null)
  const [undo, setUndo] = useState<{ id: string; title: string } | null>(null)

  useEffect(() => {
    if (searchParams.get('database') !== null) setTab('databases')
  }, [searchParams])

  const kind: NoteKind = tab === 'databases' ? 'scratchpad' : tab
  const listParams = useMemo(
    () => ({
      kind,
      q: search.trim() === '' ? undefined : search.trim(),
      tags: tags.length === 0 ? undefined : [...tags],
      limit: 500
    }),
    [kind, search, tags]
  )

  const listQuery = useQuery({
    queryKey: queryKeys.notes.list(projectId ?? 'none', listParams),
    queryFn: () => notesApi.list(projectId ?? '', listParams),
    enabled: projectId !== null && tab !== 'databases'
  })
  const tagsQuery = useQuery({
    queryKey: ['notes', projectId ?? 'none', 'tags'],
    queryFn: () => notesApi.listTags(projectId ?? ''),
    enabled: projectId !== null,
    retry: 0
  })

  if (projectId === null) {
    return (
      <EmptyState
        title={t('pages.notes.needs_project_title')}
        description={t('pages.notes.needs_project_reason')}
        icon={iconFor('note')}
        action={
          <Button variant="primary" onClick={() => navigate(routeHref('projects') ?? '/projects')}>
            {t('pages.notes.btn_projects')}
          </Button>
        }
      />
    )
  }

  const tabs: TabItem[] = [
    ...KINDS.map((kindId) => ({ id: kindId, label: t(TAB_LABEL[kindId]) })),
    { id: 'databases', label: t('pages.notes.tab_database') }
  ]

  const refreshNotes = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.notes.all(projectId) })
  }

  const openNote = (noteId: string): void => {
    const href = routeHref('note', { projectId, entityId: noteId })
    if (href !== undefined) navigate(href)
  }

  const createNote = async (): Promise<void> => {
    if (title.trim() === '') return
    setBusy(true)
    setActionError(null)
    try {
      const note = await notesApi.create(projectId, {
        title: title.trim(),
        description: description.trim() === '' ? undefined : description.trim(),
        kind
      })
      setCreateOpen(false)
      setTitle('')
      setDescription('')
      await refreshNotes()
      openNote(note.id)
    } catch (error) {
      setActionError(error)
    } finally {
      setBusy(false)
    }
  }

  const deleteNote = async (): Promise<void> => {
    if (deleteTarget === null) return
    setBusy(true)
    setActionError(null)
    try {
      const note = deleteTarget.note
      await notesApi.remove(projectId, note.id)
      setDeleteTarget(null)
      setUndo({ id: note.id, title: note.title })
      await refreshNotes()
    } catch (error) {
      setActionError(error)
    } finally {
      setBusy(false)
    }
  }

  const restoreNote = async (): Promise<void> => {
    if (undo === null) return
    setBusy(true)
    try {
      await notesApi.restore(projectId, undo.id)
      setUndo(null)
      await refreshNotes()
    } catch (error) {
      setActionError(error)
    } finally {
      setBusy(false)
    }
  }

  const items = listQuery.data?.items ?? []
  const visibleTags = tagsQuery.data ?? []

  const toggleTag = (tagId: string): void => {
    setTags((current) => (current.includes(tagId) ? current.filter((id) => id !== tagId) : [...current, tagId]))
  }

  return (
    <Page>
      <PageHeader
        title={t('pages.notes.title')}
        actions={
          tab === 'databases' ? undefined : (
            <Button
              variant="primary"
              icon={iconFor('plus')}
              disabled={archived}
              onClick={() => setCreateOpen(true)}
            >
              {t('pages.notes.btn_create')}
            </Button>
          )
        }
      />

      {archived && <Banner tone="warning" title={t('pages.notes.readonly_message')} />}
      {actionError !== null && (
        <Banner
          tone="danger"
          title={t('pages.notes.banner_problem')}
          action={
            <Button variant="ghost" size="sm" onClick={() => setActionError(null)}>
              {t('shell.overview.error_retry')}
            </Button>
          }
        >
          {actionError instanceof Error ? actionError.message : String(actionError)}
        </Banner>
      )}
      {undo !== null && (
        <Banner
          tone="info"
          title={t('pages.notes.toast_deleted')}
          action={
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => void restoreNote()}>
              {t('pages.notes.undo')}
            </Button>
          }
        >
          {undo.title}
        </Banner>
      )}

      <Tabs
        label={t('pages.notes.tab_label')}
        items={tabs}
        value={tab}
        onChange={(id) => setTab(id as NotesTab)}
      />

      {tab === 'databases' ? (
        <DatabaseView projectId={projectId} archived={archived} />
      ) : (
        <>
          <div className={styles.filters}>
            <TextInput
              label={t('pages.notes.field_search')}
              hideLabel
              placeholder={t('pages.notes.field_search')}
              value={search}
              onChange={setSearch}
            />
            {tagsQuery.isError && (
              <span className={styles.warnText}>{t('pages.notes.banner_problem')}</span>
            )}
            {visibleTags.length > 0 && (
              <div className={styles.tagRow} role="group" aria-label={t('pages.notes.tag_label')}>
                {visibleTags.map((tag) => (
                  <button
                    key={tag.id}
                    type="button"
                    className={[styles.tag, tags.includes(tag.name) ? styles.tagActive : ''].join(' ')}
                    aria-pressed={tags.includes(tag.name)}
                    onClick={() => toggleTag(tag.name)}
                  >
                    {tag.name}
                  </button>
                ))}
              </div>
            )}
          </div>

          {listQuery.isPending ? (
            <Skeleton rows={5} />
          ) : listQuery.isError ? (
            <ErrorState
              title={t('pages.notes.banner_problem')}
              message={listQuery.error.message}
              onRetry={() => void listQuery.refetch()}
              retryLabel={t('shell.overview.error_retry')}
            />
          ) : items.length === 0 ? (
            <EmptyState
              icon={iconFor('note')}
              title={search.trim() === '' ? t('pages.notes.empty_title') : t('pages.notes.no_results_title')}
              description={
                search.trim() === ''
                  ? t('pages.notes.empty_reason')
                  : t('pages.notes.no_results_reason')
              }
              action={
                search.trim() === '' ? undefined : (
                  <Button variant="secondary" onClick={() => setSearch('')}>
                    {t('pages.notes.db_clear')}
                  </Button>
                )
              }
            />
          ) : (
            <ul className={styles.noteList} aria-label={t('pages.notes.title')}>
              {items.map((note) => (
                <li key={note.id} className={styles.noteRow}>
                  <button
                    type="button"
                    className={styles.noteOpen}
                    onClick={() => openNote(note.id)}
                  >
                    <span className={styles.noteTitle}>
                      {note.title === '' ? t('pages.notes.untitled') : note.title}
                    </span>
                    {note.description !== '' && (
                      <span className={styles.noteDescription}>{note.description}</span>
                    )}
                    <span className={styles.noteMeta}>
                      <span>{t(noteKindLabel(note.kind))}</span>
                      <span>{formatRelative(t, note.updatedAt)}</span>
                      {note.fileState === 'missing' && (
                        <span className={styles.warnText}>{t('pages.notes.missing_title')}</span>
                      )}
                    </span>
                  </button>
                  <IconButton
                    icon={iconFor('trash')}
                    label={t('pages.notes.btn_delete')}
                    disabled={archived}
                    onClick={() => setDeleteTarget({ note })}
                  />
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      <Modal
        open={createOpen}
        title={t('pages.notes.dialog_title')}
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setCreateOpen(false)} disabled={busy}>
              {t('pages.notes.btn_cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => void createNote()}
              disabled={busy || title.trim() === ''}
            >
              {t('pages.notes.dialog_create')}
            </Button>
          </>
        }
      >
        <TextInput
          label={t('pages.notes.field_title')}
          value={title}
          onChange={setTitle}
          autoFocus
          error={title.trim() === '' ? t('pages.notes.title_required') : undefined}
        />
        <TextInput
          label={t('pages.notes.field_description')}
          value={description}
          onChange={setDescription}
        />
      </Modal>

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('pages.notes.delete_title')}
        description={t('pages.notes.delete_message', {
          title:
            deleteTarget === null
              ? ''
              : deleteTarget.note.title === ''
                ? t('pages.notes.untitled')
                : deleteTarget.note.title
        })}
        confirmLabel={t('pages.notes.delete_confirm')}
        cancelLabel={t('pages.notes.btn_cancel')}
        tone="danger"
        busy={busy}
        onConfirm={() => void deleteNote()}
        onCancel={() => setDeleteTarget(null)}
      />
    </Page>
  )
}