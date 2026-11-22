/**
 * Shell store: the chrome's own state, never domain data.
 *
 * Sidebar fold, palette visibility, the open project's identity and the small
 * notification badges live here. Projects, notes, jobs and
 * everything else that the main process owns belong to the query cache or the
 * tasks store.
 */

import { create } from 'zustand'

import { readStorage, writeStorage } from '../lib/storage'

export const SIDEBAR_STORAGE_KEY = 'researchos.sidebar'

export interface ShellStore {
  readonly sidebarCollapsed: boolean
  readonly paletteOpen: boolean
  /** What the palette shows when opened: the command list or global search. */
  readonly paletteMode: 'commands' | 'search'
  readonly currentProjectId: string | null
  /** Pending MCP confirmations, mirrored onto the Settings badge. */
  readonly mcpPendingConfirmations: number
  readonly toggleSidebar: () => void
  readonly setSidebarCollapsed: (collapsed: boolean) => void
  readonly openPalette: (mode: 'commands' | 'search') => void
  readonly closePalette: () => void
  readonly togglePalette: (mode: 'commands' | 'search') => void
  readonly setCurrentProjectId: (projectId: string | null) => void
  readonly setMcpPendingConfirmations: (count: number) => void
}

export const useShellStore = create<ShellStore>((set, get) => ({
  sidebarCollapsed: readStorage(SIDEBAR_STORAGE_KEY) === '1',
  paletteOpen: false,
  paletteMode: 'commands',
  currentProjectId: null,
  mcpPendingConfirmations: 0,

  toggleSidebar: () => {
    set((state) => {
      const sidebarCollapsed = !state.sidebarCollapsed
      writeStorage(SIDEBAR_STORAGE_KEY, sidebarCollapsed ? '1' : '0')
      return { sidebarCollapsed }
    })
  },
  setSidebarCollapsed: (sidebarCollapsed) => {
    writeStorage(SIDEBAR_STORAGE_KEY, sidebarCollapsed ? '1' : '0')
    set({ sidebarCollapsed })
  },

  openPalette: (paletteMode) => set({ paletteOpen: true, paletteMode }),
  closePalette: () => set({ paletteOpen: false }),
  togglePalette: (paletteMode) =>
    set((state) =>
      state.paletteOpen && state.paletteMode === paletteMode
        ? { paletteOpen: false }
        : { paletteOpen: true, paletteMode }
    ),

  setCurrentProjectId: (currentProjectId) => set({ currentProjectId }),

  setMcpPendingConfirmations: (mcpPendingConfirmations) => set({ mcpPendingConfirmations })
}))