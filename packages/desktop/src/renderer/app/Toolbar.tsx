import type { ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { Button, IconButton } from '../design/components'
import { useTheme } from '../design/theme'
import { useI18n, useT } from '../i18n'
import { resolveRoute } from '../routes'
import { iconFor, type IconName } from './icons'
import styles from './Toolbar.module.css'

export interface ToolbarProps {
  readonly sidebarCollapsed: boolean
  readonly onToggleSidebar: () => void
  readonly onOpenPalette: () => void
  /** The search entry opens the palette until the search service lands. */
  readonly onOpenSearch: () => void
  /** Project switcher slot, filled by the project module in a later wave. */
  readonly projectSwitcher?: ReactNode
}

const MODE_ICON: Record<string, IconName> = {
  light: 'sun',
  dark: 'moon',
  system: 'system'
}

/**
 * Toolbar is the 52px top bar (§6.4): navigation, breadcrumb, the project
 * switcher slot, then the global controls — search, palette, appearance and
 * language. It stays sparse on purpose.
 */
export function Toolbar({
  sidebarCollapsed,
  onToggleSidebar,
  onOpenPalette,
  onOpenSearch,
  projectSwitcher
}: ToolbarProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const location = useLocation()
  const theme = useTheme()
  const { language, available, setLanguage } = useI18n()

  const resolved = resolveRoute(location.pathname)
  const pageTitle = resolved === undefined ? t('app.name') : t(resolved.entry.titleKey)
  const ModeIcon = iconFor(MODE_ICON[theme.mode] ?? 'system')

  const nextLanguage =
    available.length === 0
      ? undefined
      : available[(available.findIndex((option) => option.tag === language) + 1) % available.length]

  return (
    <header className={styles.toolbar}>
      <div className={styles.left}>
        <IconButton
          icon={iconFor('sidebar')}
          label={t('shell.topbar.toggle_sidebar')}
          size="sm"
          active={!sidebarCollapsed}
          onClick={onToggleSidebar}
        />
        <IconButton
          icon={iconFor('chevronLeft')}
          label={t('shell.topbar.back')}
          size="sm"
          onClick={() => navigate(-1)}
        />
        <IconButton
          icon={iconFor('chevronRight')}
          label={t('shell.topbar.forward')}
          size="sm"
          onClick={() => navigate(1)}
        />

        <nav className={styles.breadcrumb} aria-label={t('components.breadcrumb.aria_label')}>
          <button type="button" className={styles.crumb} onClick={() => navigate('/')}>
            {t('app.name')}
          </button>
          <span aria-hidden="true" className={styles.separator}>
            /
          </span>
          <span className={styles.current} aria-current="page">
            {pageTitle}
          </span>
        </nav>

        {projectSwitcher !== undefined && <div className={styles.slot}>{projectSwitcher}</div>}
      </div>

      <div className={styles.right}>
        <Button
          variant="ghost"
          size="sm"
          icon={iconFor('search')}
          className={styles.search}
          aria-label={t('shell.topbar.search_label')}
          onClick={onOpenSearch}
        >
          {t('shell.topbar.search_placeholder')}
        </Button>
        <IconButton icon={iconFor('command')} label={t('shell.palette.title')} size="sm" onClick={onOpenPalette} />
        <IconButton
          icon={iconFor(MODE_ICON[theme.mode] ?? 'system')}
          label={t('shell.topbar.theme_label')}
          tooltip={t('shell.topbar.theme_tooltip')}
          size="sm"
          onClick={theme.cycleMode}
        />
        <IconButton
          icon={iconFor('language')}
          label={t('settings.language.label')}
          tooltip={nextLanguage?.nativeName}
          size="sm"
          onClick={() => {
            if (nextLanguage !== undefined) setLanguage(nextLanguage.tag)
          }}
        />
      </div>
    </header>
  )
}