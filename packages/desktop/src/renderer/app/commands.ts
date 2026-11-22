/**
 * The command registry of the palette.
 *
 * Commands are navigation plus shell actions, the same set the Go palette
 * offers (internal/ui/shell/commands.go): every destination reachable from the
 * shell, the appearance cycle, the sidebar and the language.
 * Feature modules register their own commands by passing `commands` to the
 * shell; this file never grows a per-feature branch.
 */

import type { IconName } from './icons'
import { routeEntry, routeHref, type RouteKind } from '../routes'
import type { Translations } from '../i18n'
import { nextThemeMode, type ThemeMode } from '../design/theme'

export interface PaletteProject {
  readonly id: string
  readonly name: string
}

export interface PaletteCommand {
  readonly id: string
  readonly title: string
  /** Kind label shown at the end of the row ("Page", "Action"). */
  readonly hint?: string
  readonly icon: IconName
  /** Extra searchable text, translated like the Go `command.keywords`. */
  readonly keywords?: string
  readonly run: () => void
}

export interface CommandContext {
  readonly t: Translations
  readonly themeMode: ThemeMode
  readonly sidebarCollapsed: boolean
  /** The open project: project-scoped destinations are listed for it. */
  readonly project: PaletteProject | null
  /** Projects the switcher can open. */
  readonly projects: readonly PaletteProject[]
  readonly onOpenProject?: (projectId: string) => void
  readonly onCycleTheme: () => void
  readonly onToggleSidebar: () => void
  readonly onToggleLanguage: () => void
  readonly nextLanguageName: string
  readonly navigate: (path: string) => void
}

/** themeLabel is the catalog name of an appearance mode. */
export function themeLabel(t: Translations, mode: ThemeMode): string {
  return t(`shell.theme.${mode}`)
}

/** The destinations the palette lists, in the order of the Go shell. */
const PALETTE_ROUTES: readonly RouteKind[] = [
  'home',
  'projects',
  'plugin_market',
  'overview',
  'literature',
  'notes',
  'ideas',
  'experiments',
  'kanban',
  'weekly_review',
  'settings',
  'trash',
  'diagnostics',
  'consistency',
  'help'
]

export function buildCommands(context: CommandContext): PaletteCommand[] {
  const { t } = context
  const commands: PaletteCommand[] = []
  const seen = new Set<string>()

  const add = (command: PaletteCommand): void => {
    if (seen.has(command.id)) return
    seen.add(command.id)
    commands.push(command)
  }

  const addRoute = (kind: RouteKind): void => {
    const entry = routeEntry(kind)
    if (entry.scope === 'project' && context.project === null) return
    const href = routeHref(kind, context.project === null ? {} : { projectId: context.project.id })
    if (href === undefined) return
    const title = t(entry.titleKey)
    add({
      id: `go:${kind}`,
      title: t('shell.command.goto', { page: title }),
      hint: t('shell.command.hint_page'),
      icon: entry.icon,
      keywords: title,
      run: () => context.navigate(href)
    })
  }

  for (const kind of PALETTE_ROUTES) addRoute(kind)

  if (context.onOpenProject !== undefined) {
    for (const project of context.projects) {
      const href = routeHref('overview', { projectId: project.id })
      if (href === undefined) continue
      add({
        id: `project:${project.id}`,
        title: t('shell.command.open_project', { name: project.name }),
        hint: t('shell.command.hint_project'),
        icon: 'projects',
        keywords: project.name,
        run: () => context.onOpenProject?.(project.id)
      })
    }
  }

  const next = nextThemeMode(context.themeMode)
  add({
    id: 'action:theme',
    title: t('shell.command.switch_theme', { mode: themeLabel(t, next) }),
    hint: t('shell.command.hint_action'),
    icon: 'settings',
    keywords: t('shell.command.keywords_theme'),
    run: context.onCycleTheme
  })

  add({
    id: 'action:sidebar',
    title: context.sidebarCollapsed
      ? t('shell.command.sidebar_expand')
      : t('shell.command.sidebar_collapse'),
    hint: t('shell.command.hint_action'),
    icon: 'sidebar',
    keywords: t('shell.command.keywords_sidebar'),
    run: context.onToggleSidebar
  })

  add({
    id: 'action:language',
    title: `${t('settings.language.label')} · ${context.nextLanguageName}`,
    hint: t('shell.command.hint_action'),
    icon: 'language',
    run: context.onToggleLanguage
  })

  return commands
}

/** searchFields is what the palette fuzzy-matches a command against. */
export function commandSearchFields(command: PaletteCommand): string[] {
  return command.keywords === undefined
    ? [command.title]
    : [command.title, command.keywords]
}