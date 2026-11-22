/**
 * Appearance store.
 *
 * The mode is the user's choice ("light", "dark" or "system"); the resolved
 * palette is `mode === 'system' ? systemDark : mode`. The store holds the two
 * inputs, and the ThemeProvider writes the resolved appearance to the root
 * element. Keeping this in zustand (not the provider) lets main-process events
 * (`theme.system.changed`) reach the palette without a prop chain.
 */

import { create } from 'zustand'

import { readStorage, writeStorage } from '../lib/storage'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_MODES: readonly ThemeMode[] = ['light', 'dark', 'system'] as const
export const APPEARANCE_STORAGE_KEY = 'researchos.appearance'

export function isThemeMode(value: unknown): value is ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system'
}

/** resolveTheme turns the chosen mode into the palette that is painted. */
export function resolveTheme(mode: ThemeMode, systemDark: boolean): ResolvedTheme {
  if (mode === 'system') return systemDark ? 'dark' : 'light'
  return mode
}

/** nextThemeMode is the cycle of the appearance control: light → dark → system. */
export function nextThemeMode(mode: ThemeMode): ThemeMode {
  switch (mode) {
    case 'light':
      return 'dark'
    case 'dark':
      return 'system'
    default:
      return 'light'
  }
}

export function loadThemeMode(): ThemeMode {
  const stored = readStorage(APPEARANCE_STORAGE_KEY)
  return isThemeMode(stored) ? stored : 'light'
}

export interface ThemeStore {
  readonly mode: ThemeMode
  /** The OS preference, pushed by the host media query or `theme.system.changed`. */
  readonly systemDark: boolean
  /** OS reduced-motion preference; the root class disables transitions. */
  readonly reducedMotion: boolean
  readonly setMode: (mode: ThemeMode, options?: { persist?: boolean }) => void
  readonly cycleMode: () => void
  readonly setSystemDark: (dark: boolean) => void
  readonly setReducedMotion: (reduced: boolean) => void
}

export const useThemeStore = create<ThemeStore>((set, get) => ({
  mode: loadThemeMode(),
  systemDark: false,
  reducedMotion: false,
  setMode: (mode, options) => {
    set({ mode })
    if (options?.persist !== false) writeStorage(APPEARANCE_STORAGE_KEY, mode)
  },
  cycleMode: () => {
    get().setMode(nextThemeMode(get().mode))
  },
  setSystemDark: (systemDark) => set({ systemDark }),
  setReducedMotion: (reducedMotion) => set({ reducedMotion })
}))

/** selectResolvedTheme is the palette a component should paint with. */
export function selectResolvedTheme(state: Pick<ThemeStore, 'mode' | 'systemDark'>): ResolvedTheme {
  return resolveTheme(state.mode, state.systemDark)
}