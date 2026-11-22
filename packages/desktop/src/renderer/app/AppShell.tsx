import { useCallback, useEffect, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useLocation, useNavigate } from 'react-router-dom'

import { useTheme } from '../design/theme'
import { useI18n, useT } from '../i18n'
import { ipc } from '../lib/ipc'
import { queryKeys } from '../lib/query'
import { resolveRoute, routeHref, type RouteKind } from '../routes'
import { useShellStore } from '../state/shell'
import { useTasksStore } from '../state/tasks'
import { AiActivityPet } from './AiActivityPet'
import { AppRoutes } from './AppRoutes'
import { CommandPalette } from './CommandPalette'
import { buildCommands, type PaletteCommand } from './commands'
import { ProjectSwitcher } from './ProjectSwitcher'
import { Sidebar, sidebarPluginEntries } from './Sidebar'
import { PluginPromptDialog } from '../features/plugins/PluginPromptDialog'
import { TaskStrip, type TaskProgress } from './TaskStrip'
import { Toasts } from './Toasts'
import { Toolbar } from './Toolbar'
import { WorkspaceGate } from './WorkspaceGate'
import { pushToast, useToastStore } from '../state/toasts'
import styles from './AppShell.module.css'

export interface AppShellProps {
  readonly workspaceName?: string
  /** Extra commands registered by feature modules. */
  readonly commands?: readonly PaletteCommand[]
}

/**
 * AppShell is the application frame (§6.2): sidebar, toolbar, task strip,
 * content.
 *
 * The frame's own state lives in the shell store; server state (workspace,
 * projects, entities) comes from TanStack Query over the typed IPC client.
 * Until `workspace.status` reports an open workspace the gate stands in for
 * the frame — the sidebar has nothing true to show before that.
 */
export function AppShell({
  workspaceName,
  commands = []
}: AppShellProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const location = useLocation()
  const theme = useTheme()
  const { language, available, setLanguage } = useI18n()

  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)
  const paletteOpen = useShellStore((state) => state.paletteOpen)
  const paletteMode = useShellStore((state) => state.paletteMode)
  const openPalette = useShellStore((state) => state.openPalette)
  const closePalette = useShellStore((state) => state.closePalette)
  const togglePalette = useShellStore((state) => state.togglePalette)
  const currentProjectId = useShellStore((state) => state.currentProjectId)
  const setCurrentProjectId = useShellStore((state) => state.setCurrentProjectId)
  const mcpPending = useShellStore((state) => state.mcpPendingConfirmations)
  const toasts = useToastStore((state) => state.toasts)
  const dismissToast = useToastStore((state) => state.dismiss)
  const jobTasks = useTasksStore((state) => state.tasks)
  const cancelJob = useTasksStore((state) => state.cancel)
  const clearFailures = useTasksStore((state) => state.clearFailures)
  const queryClient = useQueryClient()

  const statusQuery = useQuery({
    queryKey: queryKeys.workspace.status(),
    queryFn: () => ipc.invoke('workspace.status')
  })
  const workspaceOpen = statusQuery.data?.open === true

  const projectsQuery = useQuery({
    queryKey: queryKeys.projects.list({ includeArchived: false }),
    queryFn: () => ipc.invoke('project.list', { includeArchived: false }),
    enabled: workspaceOpen
  })
  const projects = useMemo(() => projectsQuery.data ?? [], [projectsQuery.data])

  const pluginsQuery = useQuery({
    queryKey: queryKeys.plugins.list,
    queryFn: () => ipc.invoke('plugins.list'),
    enabled: workspaceOpen
  })

  useEffect(
    () =>
      ipc.on('plugins.changed', () => {
        void queryClient.invalidateQueries({ queryKey: queryKeys.plugins.all })
      }),
    [queryClient]
  )

  const pluginEntries = useMemo(
    () => sidebarPluginEntries(pluginsQuery.data ?? []),
    [pluginsQuery.data]
  )

  const resolved = resolveRoute(location.pathname)
  const routeProjectId = resolved?.params.projectId

  // A project route names its project; adopt it so the sidebar and palette
  // agree with the URL (deep links and reloads included).
  useEffect(() => {
    if (routeProjectId === undefined || routeProjectId === currentProjectId) return
    setCurrentProjectId(routeProjectId)
  }, [routeProjectId, currentProjectId, setCurrentProjectId])

  useEffect(() => {
    closePalette()
  }, [location.pathname, closePalette])

  // The main process answers `host.getContext` and emits project.changed /
  // theme.changed; the renderer is the only place that knows the active route.
  useEffect(() => {
    void ipc
      .invoke('plugins.host.context.set', {
        projectId: currentProjectId,
        theme: theme.mode === 'dark' ? 'dark' : 'light',
        locale: language
      })
      .catch(() => undefined)
  }, [currentProjectId, theme.mode, language])

  useEffect(
    () =>
      ipc.on('plugins.toast', (payload) => {
        pushToast({ tone: payload.level, title: payload.message })
      }),
    []
  )

  useEffect(
    () =>
      ipc.on('plugins.navigate', (payload) => {
        const projectId = payload.projectId ?? currentProjectId ?? undefined
        if (payload.target === 'entity') {
          const kinds: Record<string, RouteKind> = {
            note: 'note',
            paper: 'paper',
            idea: 'idea',
            experiment: 'experiment',
            run: 'run',
            task: 'task',
            weekly_report: 'weekly_report'
          }
          const kind = payload.entityType === undefined ? undefined : kinds[payload.entityType]
          const href =
            kind === undefined
              ? undefined
              : routeHref(kind, { projectId, entityId: payload.entityId })
          if (href !== undefined) navigate(href)
          return
        }
        const kinds: Record<string, RouteKind> = {
          project: 'overview',
          notes: 'notes',
          literature: 'literature',
          ideas: 'ideas',
          experiments: 'experiments',
          kanban: 'kanban',
          weekly: 'weekly_review'
        }
        const kind = kinds[payload.target]
        const href = kind === undefined ? undefined : routeHref(kind, { projectId })
        if (href !== undefined) navigate(href)
      }),
    [navigate, currentProjectId]
  )

  // Ctrl/Cmd+K is global search; Ctrl/Cmd+Shift+P is the command palette
  // (Go help page). Escape closes whichever is open.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      const modifier = event.ctrlKey || event.metaKey
      if (modifier && event.shiftKey && event.key.toLowerCase() === 'p') {
        event.preventDefault()
        togglePalette('commands')
        return
      }
      if (modifier && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        togglePalette('search')
        return
      }
      if (event.key === 'Escape') closePalette()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [togglePalette, closePalette])

  const nextLanguage = useMemo(() => {
    if (available.length === 0) return undefined
    const index = available.findIndex((option) => option.tag === language)
    return available[(index + 1) % available.length]
  }, [available, language])

  const toggleLanguage = useCallback(() => {
    if (nextLanguage !== undefined) setLanguage(nextLanguage.tag)
  }, [nextLanguage, setLanguage])

  const openProject = useCallback(
    (projectId: string) => {
      setCurrentProjectId(projectId)
      const href = routeHref('overview', { projectId })
      if (href !== undefined) navigate(href)
    },
    [navigate, setCurrentProjectId]
  )

  const paletteCommands = useMemo(() => {
    const paletteProjects = projects.map((project) => ({ id: project.id, name: project.name }))
    const built = buildCommands({
      t,
      themeMode: theme.mode,
      sidebarCollapsed,
      project: paletteProjects.find((project) => project.id === currentProjectId) ?? null,
      projects: paletteProjects,
      onOpenProject: openProject,
      onCycleTheme: theme.cycleMode,
      onToggleSidebar: toggleSidebar,
      onToggleLanguage: toggleLanguage,
      nextLanguageName: nextLanguage?.nativeName ?? '',
      navigate: (path) => navigate(path)
    })
    return [...built, ...commands]
  }, [
    t,
    theme.mode,
    theme.cycleMode,
    sidebarCollapsed,
    projects,
    currentProjectId,
    openProject,
    toggleSidebar,
    toggleLanguage,
    nextLanguage,
    navigate,
    commands
  ])

  const sidebarProject = useMemo(() => {
    if (currentProjectId === null) return null
    const project = projects.find((candidate) => candidate.id === currentProjectId)
    return { id: currentProjectId, name: project?.name ?? t('shell.page.projects') }
  }, [currentProjectId, projects, t])

  const badges = useMemo<Partial<Record<RouteKind, string | number>> | undefined>(
    () => (mcpPending > 0 ? { settings: mcpPending } : undefined),
    [mcpPending]
  )

  const taskItems = useMemo<TaskProgress[]>(
    () =>
      jobTasks
        .filter((task) => task.visible || task.failed)
        .map((task) => ({
          id: task.id,
          name: task.name,
          percent: task.percent ?? undefined,
          canceling: task.canceling,
          failed: task.failed,
          cancellable: task.cancellable,
          error: task.error
        })),
    [jobTasks]
  )

  if (statusQuery.isPending || statusQuery.isError || !workspaceOpen) {
    return (
      <div className={styles.gate}>
        <WorkspaceGate
          pending={statusQuery.isPending}
          open={workspaceOpen}
          error={statusQuery.error ?? undefined}
          onRetry={() => {
            void statusQuery.refetch()
          }}
        />
      </div>
    )
  }

  return (
    <div className={styles.app}>
      <Sidebar
        project={sidebarProject}
        collapsed={sidebarCollapsed}
        workspaceName={workspaceName ?? t('app.name')}
        badges={badges}
        pluginEntries={pluginEntries}
      />
      <div className={styles.main}>
        <Toolbar
          sidebarCollapsed={sidebarCollapsed}
          onToggleSidebar={toggleSidebar}
          onOpenPalette={() => openPalette('commands')}
          onOpenSearch={() => openPalette('search')}
          projectSwitcher={<ProjectSwitcher enabled={workspaceOpen} />}
        />
        <TaskStrip tasks={taskItems} onCancel={cancelJob} onClear={clearFailures} />
        <div className={styles.body}>
          <main className={styles.content}>
            <AppRoutes />
          </main>
        </div>
      </div>

      <CommandPalette
        open={paletteOpen}
        mode={paletteMode}
        projectId={currentProjectId}
        commands={paletteCommands}
        onClose={closePalette}
      />
      <AiActivityPet />
      <PluginPromptDialog />
      <Toasts toasts={toasts} onDismiss={dismissToast} />
    </div>
  )
}