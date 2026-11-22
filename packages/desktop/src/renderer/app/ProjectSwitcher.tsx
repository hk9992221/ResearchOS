import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'

import { EmptyState, ErrorState, Skeleton } from '../design/components'
import { useT } from '../i18n'
import { ipc } from '../lib/ipc'
import { queryKeys } from '../lib/query'
import { routeHref } from '../routes'
import { useShellStore } from '../state/shell'
import { iconFor } from './icons'
import styles from './ProjectSwitcher.module.css'

export interface ProjectSwitcherProps {
  /** Project listing needs an open workspace; the query stays idle until then. */
  readonly enabled?: boolean
}

/**
 * ProjectSwitcher loads `project.list` through the query cache and makes the
 * open project switchable. Loading, failure and empty are real states: the
 * control never invents a project name and never hides a failed listing.
 *
 * Switching projects is navigation, not an Activity: the shell only records
 * which project the window is looking at.
 */
export function ProjectSwitcher({ enabled = true }: ProjectSwitcherProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  const currentProjectId = useShellStore((state) => state.currentProjectId)
  const setCurrentProjectId = useShellStore((state) => state.setCurrentProjectId)

  const projectsQuery = useQuery({
    queryKey: queryKeys.projects.list({ includeArchived: false }),
    queryFn: () => ipc.invoke('project.list', { includeArchived: false }),
    enabled
  })

  const projects = projectsQuery.data ?? []
  const current = projects.find((project) => project.id === currentProjectId)
  const FolderIcon = iconFor('projects')
  const ChevronIcon = iconFor('chevronDown')

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent): void => {
      if (rootRef.current === null) return
      if (!rootRef.current.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const choose = (projectId: string): void => {
    setCurrentProjectId(projectId)
    setOpen(false)
    navigate(routeHref('overview', { projectId }) ?? '/projects')
  }

  return (
    <div className={styles.root} ref={rootRef}>
      <button
        type="button"
        className={styles.trigger}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <FolderIcon aria-hidden="true" className={styles.icon} strokeWidth={2} />
        <span className={styles.name}>{current?.name ?? t('shell.page.projects')}</span>
        <ChevronIcon aria-hidden="true" className={styles.chevron} strokeWidth={2} />
      </button>

      {open && (
        <div className={styles.panel}>
          {projectsQuery.isPending && <Skeleton rows={3} />}
          {projectsQuery.isError && (
            <ErrorState
              title={t('shell.projects.toast_list_fail')}
              message={projectsQuery.error.message}
              retryLabel={t('shell.overview.error_retry')}
              onRetry={() => {
                void projectsQuery.refetch()
              }}
            />
          )}
          {projectsQuery.isSuccess && projects.length === 0 && (
            <EmptyState
              compact
              title={t('shell.projects.empty_title')}
              description={t('shell.projects.empty_reason_list')}
            />
          )}
          {projects.length > 0 && (
            <ul className={styles.list} role="listbox" aria-label={t('shell.page.projects')}>
              {projects.map((project) => (
                <li key={project.id}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={project.id === currentProjectId}
                    className={[
                      styles.option,
                      project.id === currentProjectId ? styles.selected : ''
                    ].join(' ')}
                    onClick={() => choose(project.id)}
                  >
                    {project.name}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}