import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Archive, ArchiveRestore, ExternalLink, Plus, SquarePen, Trash2 } from 'lucide-react'

import type { Project } from '@researchos/shared'

import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Page,
  PageHeader,
  SelectField,
  Skeleton,
  Table,
  Tabs,
  TextInput,
  Toolbar
} from '../../design/components'
import { useT } from '../../i18n'
import { queryKeys } from '../../lib/query'
import { routeHref } from '../../routes'
import { useShellStore } from '../../state/shell'
import { Toasts, type ToastMessage } from '../../app/Toasts'
import { projectsApi } from './api'
import {
  CreateProjectDialog,
  DeleteProjectDialog,
  EditProjectDialog
} from './dialogs'
import { ImportProjectDialog } from '../import/ImportProjectDialog'
import styles from './projects.module.css'

type ProjectTab = 'active' | 'archived'
type SortKey = 'created' | 'name'

/**
 * Projects: the workspace's project list, the create/edit dialogs,
 * the archive toggle, the two-phase permanent delete and the import entry.
 * Opening a project is navigation, so it writes no Activity; every write drops
 * the `['projects']` query root that also holds the dashboard snapshot.
 */
export default function ProjectsPage(): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  const currentProjectId = useShellStore((state) => state.currentProjectId)
  const setCurrentProjectId = useShellStore((state) => state.setCurrentProjectId)

  const [search, setSearch] = useState('')
  const [query, setQuery] = useState('')
  const [tab, setTab] = useState<ProjectTab>('active')
  const [sort, setSort] = useState<SortKey>('created')
  const [createOpen, setCreateOpen] = useState(false)
  const [editId, setEditId] = useState<string | null>(null)
  const [deleteId, setDeleteId] = useState<string | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [toasts, setToasts] = useState<readonly ToastMessage[]>([])

  // Search is typed against the server list, with a short debounce so a
  // keystroke does not become one IPC call.
  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 250)
    return () => window.clearTimeout(timer)
  }, [search])

  const listQuery = useQuery({
    queryKey: queryKeys.projects.list({ includeArchived: true, query }),
    queryFn: () => projectsApi.list(query)
  })

  const projects = useMemo(() => listQuery.data ?? [], [listQuery.data])

  // Route parameters are the one-shot dialog requests the Home page and the
  // Overview header send here (`?create=1`, `?edit=<id>`, `?delete=<id>`).
  useEffect(() => {
    const create = searchParams.get('create')
    const edit = searchParams.get('edit')
    const remove = searchParams.get('delete')
    if (create === null && edit === null && remove === null) return
    if (create !== null) setCreateOpen(true)
    if (edit !== null) setEditId(edit)
    if (remove !== null) setDeleteId(remove)
    const next = new URLSearchParams(searchParams)
    next.delete('create')
    next.delete('edit')
    next.delete('delete')
    setSearchParams(next, { replace: true })
  }, [searchParams, setSearchParams])

  const notify = (toast: Omit<ToastMessage, 'id'>): void => {
    setToasts((list) => [...list, { id: `projects-${Date.now()}-${list.length}`, ...toast }])
  }

  const invalidateProjects = async (projectId?: string): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: queryKeys.projects.all })
    if (projectId !== undefined) {
      await queryClient.invalidateQueries({ queryKey: queryKeys.project(projectId).all })
    }
  }

  const openProject = (projectId: string): void => {
    setCurrentProjectId(projectId)
    const href = routeHref('overview', { projectId })
    if (href !== undefined) navigate(href)
  }

  const toggleArchive = async (project: Project): Promise<void> => {
    const archiving = project.status === 'active'
    try {
      if (archiving) await projectsApi.archive(project.id)
      else await projectsApi.unarchive(project.id)
      await invalidateProjects(project.id)
      notify({
        tone: 'success',
        title: archiving
          ? t('shell.projects.toast_archived', { name: project.name })
          : t('shell.projects.toast_unarchived', { name: project.name })
      })
    } catch {
      notify({ tone: 'error', title: t('shell.projects.toast_archive_fail') })
    }
  }

  const visible = useMemo(() => {
    const rows = projects.filter((project) =>
      tab === 'active' ? project.status === 'active' : project.status === 'archived'
    )
    return rows.sort((left, right) => {
      if (sort === 'name') return left.name.localeCompare(right.name)
      return left.createdAt > right.createdAt ? -1 : left.createdAt < right.createdAt ? 1 : 0
    })
  }, [projects, tab, sort])

  const activeCount = projects.filter((project) => project.status === 'active').length
  const archivedCount = projects.length - activeCount
  const editing = projects.find((project) => project.id === editId) ?? null
  const deleting = projects.find((project) => project.id === deleteId) ?? null

  return (
    <Page>
      <PageHeader
        title={t('shell.page.projects')}
        actions={
          <>
            <Button variant="secondary" icon={ArchiveRestore} onClick={() => setImportOpen(true)}>
              {t('shell.projects.btn_import')}
            </Button>
            <Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>
              {t('shell.projects.btn_create')}
            </Button>
          </>
        }
      />

      <Toolbar label={t('shell.page.projects')}>
        <TextInput
          label={t('shell.projects.search_placeholder')}
          value={search}
          onChange={setSearch}
          hideLabel
          placeholder={t('shell.projects.search_placeholder')}
          type="search"
        />
        <Tabs
          label={t('shell.page.projects')}
          items={[
            { id: 'active', label: `${t('shell.projects.tab_active')} · ${activeCount}` },
            { id: 'archived', label: `${t('shell.projects.tab_archived')} · ${archivedCount}` }
          ]}
          value={tab}
          onChange={(id) => setTab(id as ProjectTab)}
        />
        <SelectField
          label={t('shell.projects.sort')}
          value={sort}
          options={[
            { value: 'created', label: t('shell.projects.sort_created') },
            { value: 'name', label: t('shell.projects.sort_name') }
          ]}
          onChange={(value) => setSort(value as SortKey)}
        />
      </Toolbar>

      {listQuery.isError && (
        <ErrorState
          title={t('shell.projects.banner_load_fail')}
          message={listQuery.error instanceof Error ? listQuery.error.message : undefined}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void listQuery.refetch()}
        />
      )}

      {listQuery.isPending && <Skeleton rows={5} />}

      {listQuery.isSuccess && visible.length === 0 && (
        <>
          {query !== '' ? (
            <EmptyState
              title={t('shell.projects.empty_search_title')}
              description={t('shell.projects.empty_search_reason')}
              action={
                <Button variant="secondary" onClick={() => setSearch('')}>
                  {t('shell.projects.btn_clear_search')}
                </Button>
              }
            />
          ) : tab === 'archived' ? (
            <EmptyState
              title={t('shell.projects.empty_archived_title')}
              description={t('shell.projects.empty_archived_reason')}
            />
          ) : (
            <EmptyState
              title={t('shell.projects.empty_title')}
              description={t('shell.projects.empty_reason_list')}
              action={
                <Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>
                  {t('shell.projects.btn_create')}
                </Button>
              }
            />
          )}
        </>
      )}

      {listQuery.isSuccess && visible.length > 0 && (
        <Card>
          <Table
            label={t('shell.page.projects')}
            columns={[
              t('shell.projects.col_name'),
              t('shell.projects.col_status'),
              t('shell.projects.col_created'),
              t('shell.projects.col_actions')
            ]}
          >
            {visible.map((project) => (
              <tr
                key={project.id}
                className={styles.row}
                tabIndex={0}
                onClick={(event) => {
                  if ((event.target as HTMLElement).closest('button') !== null) return
                  openProject(project.id)
                }}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget) return
                  if (event.key !== 'Enter' && event.key !== ' ') return
                  if (event.key === ' ') event.preventDefault()
                  openProject(project.id)
                }}
              >
                <td className={styles.name}>
                  <span>{project.name}</span>
                  {project.summary !== '' && (
                    <span className={styles.summary}>{project.summary}</span>
                  )}
                </td>
                <td>
                  <Badge tone={project.status === 'active' ? 'success' : 'neutral'}>
                    {t(`shell.project_status.${project.status}`)}
                  </Badge>
                </td>
                <td className={styles.date}>{project.createdAt.slice(0, 10)}</td>
                <td>
                  <div className={styles.actions}>
                    <Button
                      variant="primary"
                      size="sm"
                      icon={ExternalLink}
                      onClick={() => openProject(project.id)}
                    >
                      {t('shell.projects.btn_open')}
                    </Button>
                    <IconButton
                      icon={Archive}
                      label={t('shell.projects.btn_export_aria', { name: project.name })}
                      size="sm"
                      onClick={() => {
                        const href = routeHref('export', {
                          projectId: project.id,
                          entityId: project.id
                        })
                        if (href !== undefined) navigate(href)
                      }}
                    />
                    <IconButton
                      icon={SquarePen}
                      label={t('shell.projects.btn_edit_aria', { name: project.name })}
                      size="sm"
                      onClick={() => setEditId(project.id)}
                    />
                    <IconButton
                      icon={project.status === 'active' ? Archive : ArchiveRestore}
                      label={
                        project.status === 'active'
                          ? t('shell.projects.btn_archive_aria', { name: project.name })
                          : t('shell.projects.btn_unarchive_aria', { name: project.name })
                      }
                      size="sm"
                      onClick={() => void toggleArchive(project)}
                    />
                    <IconButton
                      icon={Trash2}
                      label={t('shell.projects.btn_delete_aria', { name: project.name })}
                      size="sm"
                      onClick={() => setDeleteId(project.id)}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        </Card>
      )}

      <CreateProjectDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(project) => {
          setCreateOpen(false)
          void invalidateProjects()
          notify({ tone: 'success', title: t('shell.projects.toast_created', { name: project.name }) })
        }}
        onError={(message) => {
          notify({ tone: 'error', title: t('shell.projects.toast_create_fail'), message })
        }}
      />

      <EditProjectDialog
        project={editing}
        onClose={() => setEditId(null)}
        onSaved={(project) => {
          setEditId(null)
          void invalidateProjects(project.id)
          notify({ tone: 'success', title: t('shell.projects.toast_edited') })
        }}
        onError={(message) => {
          notify({ tone: 'error', title: t('shell.projects.toast_edit_fail'), message })
        }}
      />

      <DeleteProjectDialog
        project={deleting}
        onClose={() => setDeleteId(null)}
        onDeleted={(projectId) => {
          setDeleteId(null)
          if (currentProjectId === projectId) {
            setCurrentProjectId(null)
            navigate('/')
          }
          void invalidateProjects()
          notify({ tone: 'success', title: t('shell.projects.toast_deleted') })
        }}
      />

      <ImportProjectDialog
        open={importOpen}
        onClose={() => {
          setImportOpen(false)
          void queryClient.invalidateQueries({ queryKey: queryKeys.projects.all })
          void queryClient.invalidateQueries({ queryKey: queryKeys.workspace.all })
        }}
      />

      <Toasts
        toasts={toasts}
        onDismiss={(id) => setToasts((list) => list.filter((toast) => toast.id !== id))}
      />
    </Page>
  )
}