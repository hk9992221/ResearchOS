/**
 * Language preference store.
 *
 * The preference is the persisted choice: "system" or a language tag. The
 * i18n provider resolves it against the catalogs and the OS locale, so the
 * switch is live everywhere without a reload.
 *
 * Persistence: localStorage is the cache that always works; when the host
 * exposes the bridge, the resolved locale is also written through
 * `app.preferences.set`. A missing handler (an older main process or a dev
 * browser) leaves the local cache as the fallback — never a failed start.
 */

import { create } from 'zustand'

import { hasBridge, ipc } from '../lib/ipc'
import { readStorage, writeStorage } from '../lib/storage'

export const LANGUAGE_STORAGE_KEY = 'researchos.language'

/** "system" follows the OS locale; every other value is a language tag. */
export type LanguagePreference = string

export function loadLanguagePreference(): LanguagePreference {
  const stored = readStorage(LANGUAGE_STORAGE_KEY)
  return stored === null || stored === '' ? 'system' : stored
}

function isLocaleCode(value: string): value is 'en' | 'zh' {
  return value === 'en' || value === 'zh'
}

export interface LanguageStore {
  readonly preference: LanguagePreference
  /** Change the language and persist it (local cache + settings command). */
  readonly setPreference: (preference: LanguagePreference, resolved?: string) => void
  /** Adopt the locale the main process reported at startup. */
  readonly adoptRemote: (locale: string) => void
}

export const useLanguageStore = create<LanguageStore>((set) => ({
  preference: loadLanguagePreference(),

  setPreference: (preference, resolved) => {
    set({ preference })
    writeStorage(LANGUAGE_STORAGE_KEY, preference)
    if (resolved === undefined || !isLocaleCode(resolved) || !hasBridge()) return
    void ipc.invoke('app.preferences.set', { patch: { locale: resolved } }).catch(() => {
      // The local cache already holds the choice; an older main process
      // without the handler keeps the session in the chosen language.
    })
  },

  adoptRemote: (locale) => {
    if (!isLocaleCode(locale)) return
    set({ preference: locale })
    writeStorage(LANGUAGE_STORAGE_KEY, locale)
  }
}))