/**
 * Appearance host: the bridge between the theme store and the document.
 *
 * The store (state/theme.ts) holds the chosen mode and the OS preferences;
 * this module puts them on the root element — `data-theme` for the palette and
 * the `reduced-motion` class for transitions — and watches the media queries
 * that report a live OS change. Components read `useTheme()`.
 */

import { createContext, createElement, useContext, useEffect, useMemo, type ReactNode } from 'react'

import {
  resolveTheme,
  useThemeStore,
  type ResolvedTheme,
  type ThemeMode
} from '../state/theme'

export {
  APPEARANCE_STORAGE_KEY,
  THEME_MODES,
  isThemeMode,
  loadThemeMode,
  nextThemeMode,
  resolveTheme,
  useThemeStore
} from '../state/theme'
export type { ResolvedTheme, ThemeMode } from '../state/theme'

const DARK_QUERY = '(prefers-color-scheme: dark)'
const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

/** mediaQuery returns the list, or null where the host has no matchMedia. */
export function mediaQuery(query: string): MediaQueryList | null {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return null
  return window.matchMedia(query)
}

/** Host seams: tests and the host replace the media query, not the logic. */
export function systemPrefersDark(): boolean {
  return mediaQuery(DARK_QUERY)?.matches ?? false
}

export function prefersReducedMotion(): boolean {
  return mediaQuery(REDUCED_MOTION_QUERY)?.matches ?? false
}

/** applyTheme writes the resolved appearance to the document root. */
export function applyTheme(theme: ResolvedTheme): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.dataset.theme = theme
  root.style.colorScheme = theme
}

/** applyReducedMotion disables transitions for hosts that report the setting. */
export function applyReducedMotion(reduced: boolean): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.classList.toggle('reduced-motion', reduced)
  root.dataset.reducedMotion = reduced ? 'true' : 'false'
}

/** watchMedia subscribes to a media query; returns an unsubscribe function. */
export function watchMedia(query: string, onChange: (matches: boolean) => void): () => void {
  const list = mediaQuery(query)
  if (list === null) return () => {}
  const handler = (event: MediaQueryListEvent): void => onChange(event.matches)
  list.addEventListener('change', handler)
  return () => list.removeEventListener('change', handler)
}

export interface ThemeValue {
  readonly mode: ThemeMode
  readonly resolved: ResolvedTheme
  readonly reducedMotion: boolean
  readonly setMode: (mode: ThemeMode) => void
  readonly cycleMode: () => void
}

const ThemeContext = createContext<ThemeValue | null>(null)

export interface ThemeProviderProps {
  readonly children: ReactNode
  /** Test and host override; the stored preference is used otherwise. */
  readonly initialMode?: ThemeMode
}

export function ThemeProvider({ children, initialMode }: ThemeProviderProps): JSX.Element {
  const mode = useThemeStore((state) => state.mode)
  const systemDark = useThemeStore((state) => state.systemDark)
  const reducedMotion = useThemeStore((state) => state.reducedMotion)
  const setMode = useThemeStore((state) => state.setMode)
  const cycleMode = useThemeStore((state) => state.cycleMode)

  const resolved = resolveTheme(mode, systemDark)

  // On mount, adopt the live OS preferences (the store may have been created
  // before the host was ready) and the caller's initial mode.
  useEffect(() => {
    const store = useThemeStore.getState()
    store.setSystemDark(systemPrefersDark())
    store.setReducedMotion(prefersReducedMotion())
    if (initialMode !== undefined) store.setMode(initialMode, { persist: false })
  }, [initialMode])

  useEffect(() => {
    applyTheme(resolved)
  }, [resolved])

  useEffect(() => {
    applyReducedMotion(reducedMotion)
  }, [reducedMotion])

  useEffect(
    () => watchMedia(DARK_QUERY, (dark) => useThemeStore.getState().setSystemDark(dark)),
    []
  )
  useEffect(
    () =>
      watchMedia(REDUCED_MOTION_QUERY, (reduced) =>
        useThemeStore.getState().setReducedMotion(reduced)
      ),
    []
  )

  const value = useMemo<ThemeValue>(
    () => ({ mode, resolved, reducedMotion, setMode, cycleMode }),
    [mode, resolved, reducedMotion, setMode, cycleMode]
  )

  return createElement(ThemeContext.Provider, { value }, children)
}

export function useTheme(): ThemeValue {
  const value = useContext(ThemeContext)
  if (value === null) {
    throw new Error('useTheme must be used inside <ThemeProvider>')
  }
  return value
}