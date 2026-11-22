import { Fragment } from 'react'
import type { LucideIcon } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'

import type { PluginInfo } from '@researchos/shared'

import { useT } from '../i18n'
import { ROUTE_REGISTRY, resolveRoute, routeHref, type RouteEntry, type RouteKind } from '../routes'
import { ICONS, iconFor, type IconName } from './icons'
import type { PaletteProject } from './commands'
import styles from './Sidebar.module.css'

export interface SidebarPluginEntry {
  readonly pluginId: string
  readonly contributionId: string
  readonly title: string
  readonly icon?: string
  readonly entry: string
  readonly scope: 'workspace' | 'project'
}

/**
 * The enabled contributions the sidebar may show. A contribution that names
 * scopes is hidden the moment any of them is revoked; omitted scopes inherit
 * nothing beyond the enabled check.
 */
export function sidebarPluginEntries(plugins: readonly PluginInfo[]): SidebarPluginEntry[] {
  const entries: SidebarPluginEntry[] = []
  for (const plugin of plugins) {
    if (!plugin.enabled) continue
    for (const contribution of plugin.contributions.sidebar ?? []) {
      const required = contribution.permissions ?? []
      if (!required.every((permission) => plugin.granted.includes(permission))) continue
      entries.push({
        pluginId: plugin.id,
        contributionId: contribution.id,
        title: contribution.title,
        ...(contribution.icon === undefined ? {} : { icon: contribution.icon }),
        entry: contribution.entry,
        scope: contribution.scope ?? 'project'
      })
    }
  }
  return entries
}

export interface SidebarProps {
  readonly project: PaletteProject | null
  readonly collapsed: boolean
  readonly workspaceName: string
  /** Counts keyed by route kind (for example the recycle bin). */
  readonly badges?: Partial<Record<RouteKind, string | number>>
  /** Enabled plugin contributions whose declared scopes are all granted. */
  readonly pluginEntries?: readonly SidebarPluginEntry[]
}

interface NavEntryProps {
  readonly entry: RouteEntry
  readonly href: string
  readonly active: boolean
  readonly collapsed: boolean
  readonly badge?: string | number
}

function NavEntry({ entry, href, active, collapsed, badge }: NavEntryProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const label = t(entry.titleKey)
  const Icon = iconFor(entry.icon)
  return (
    <button
      type="button"
      className={[styles.item, active ? styles.active : ''].join(' ')}
      aria-current={active ? 'page' : undefined}
      aria-label={collapsed ? label : undefined}
      title={collapsed ? label : undefined}
      data-kind={entry.kind}
      onClick={() => navigate(href)}
    >
      <Icon aria-hidden="true" className={styles.icon} strokeWidth={2} />
      {!collapsed && <span className={styles.label}>{label}</span>}
      {!collapsed && badge !== undefined && <span className={styles.badge}>{badge}</span>}
    </button>
  )
}

/**
 * Sidebar is the application navigation (§6.3): the workspace switcher, the
 * global entries, the module entries of the open project, and the system
 * entries. Folding hides the labels but keeps icons, names and tooltips; the
 * selected destination is marked for assistive tech.
 */
function pluginIcon(name: string | undefined): LucideIcon {
  if (name !== undefined && name in ICONS) return ICONS[name as IconName]
  return ICONS.pluginMarket
}

function PluginNavEntry({
  entry,
  href,
  active,
  collapsed
}: {
  readonly entry: SidebarPluginEntry
  readonly href: string
  readonly active: boolean
  readonly collapsed: boolean
}): JSX.Element {
  const navigate = useNavigate()
  const Icon = pluginIcon(entry.icon)
  return (
    <button
      type="button"
      className={[styles.item, active ? styles.active : ''].join(' ')}
      aria-current={active ? 'page' : undefined}
      aria-label={collapsed ? entry.title : undefined}
      title={collapsed ? entry.title : undefined}
      data-plugin={entry.pluginId}
      onClick={() => navigate(href)}
    >
      <Icon aria-hidden="true" className={styles.icon} strokeWidth={2} />
      {!collapsed && <span className={styles.label}>{entry.title}</span>}
    </button>
  )
}

export function Sidebar({
  project,
  collapsed,
  workspaceName,
  badges,
  pluginEntries = []
}: SidebarProps): JSX.Element {
  const t = useT()
  const location = useLocation()
  const navigate = useNavigate()
  const resolved = resolveRoute(location.pathname)

  // Project sub-pages only stay listed while the current route is inside a
  // project; Home, Projects, the plugin market and Settings collapse them.
  const activeProject = resolved !== undefined && resolved.entry.scope === 'project' ? project : null

  const isActive = (entry: RouteEntry): boolean =>
    resolved !== undefined &&
    (resolved.entry.kind === entry.kind || resolved.entry.parent === entry.kind)

  const visible = (entry: RouteEntry): boolean =>
    entry.nav && (entry.scope === 'global' || activeProject !== null)

  const workspace = ROUTE_REGISTRY.filter((entry) => entry.group === 'workspace' && visible(entry))
  const projectItems = ROUTE_REGISTRY.filter((entry) => entry.group === 'project' && visible(entry))
  const system = ROUTE_REGISTRY.filter((entry) => entry.group === 'system' && visible(entry))

  const workspacePlugins = pluginEntries.filter((entry) => entry.scope === 'workspace')
  const projectPlugins = activeProject === null
    ? []
    : pluginEntries.filter((entry) => entry.scope === 'project')
  const pluginHref = (entry: SidebarPluginEntry): string | undefined =>
    activeProject === null || entry.scope === 'workspace'
      ? `/plugins/${encodeURIComponent(entry.pluginId)}/${encodeURIComponent(entry.entry)}`
      : `/projects/${encodeURIComponent(activeProject.id)}/plugins/${encodeURIComponent(entry.pluginId)}/${encodeURIComponent(entry.entry)}`
  const renderPlugin = (entry: SidebarPluginEntry): JSX.Element => {
    const href = pluginHref(entry) ?? '/'
    return (
      <PluginNavEntry
        key={`${entry.pluginId}:${entry.contributionId}`}
        entry={entry}
        href={href}
        active={location.pathname === href}
        collapsed={collapsed}
      />
    )
  }

  const renderEntry = (entry: RouteEntry): JSX.Element => {
    const href = routeHref(entry.kind, { projectId: activeProject?.id ?? undefined })
    return (
      <NavEntry
        key={entry.kind}
        entry={entry}
        href={href ?? '/'}
        active={isActive(entry)}
        collapsed={collapsed}
        badge={badges?.[entry.kind]}
      />
    )
  }

  const WorkspaceIcon = iconFor('overview')

  return (
    <nav
      className={[styles.sidebar, collapsed ? styles.collapsed : ''].join(' ')}
      aria-label={t('app.name')}
      data-collapsed={collapsed ? 'true' : 'false'}
    >
      <button
        type="button"
        className={styles.workspace}
        aria-label={workspaceName}
        title={collapsed ? workspaceName : undefined}
        onClick={() => navigate('/')}
      >
        <span className={styles.workspaceIcon}>
          <WorkspaceIcon aria-hidden="true" strokeWidth={2} />
        </span>
        {!collapsed && <span className={styles.workspaceName}>{workspaceName}</span>}
      </button>

      <div className={styles.group}>
        {!collapsed && <p className={styles.sectionLabel}>{t('shell.nav.section_workspace')}</p>}
        {workspace.map((entry) => (
          <Fragment key={entry.kind}>
            {renderEntry(entry)}
            {entry.kind === 'projects' &&
              activeProject !== null &&
              (projectItems.length > 0 || projectPlugins.length > 0) && (
                <div className={styles.subGroup}>
                  {projectItems.map(renderEntry)}
                  {projectPlugins.map(renderPlugin)}
                </div>
              )}
          </Fragment>
        ))}
        {workspacePlugins.map(renderPlugin)}
      </div>

      <div className={styles.spacer} />

      <div className={styles.group}>{system.map(renderEntry)}</div>
    </nav>
  )
}