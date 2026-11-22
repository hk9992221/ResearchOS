/**
 * Interface internationalization.
 *
 * Catalogs are the area files under ./locales/<tag>/*.json, copied verbatim
 * from the Go implementation. Every file is a flat `key → text` object; the
 * files of one language merge into a single table at startup, exactly as
 * internal/i18n does. A duplicate key in one language is a programming error
 * and throws with the file names, so a copy never silently drops a string.
 *
 * Lookup falls back current language → English → the key itself, which keeps
 * a partially translated pack usable and makes a missing key visible.
 *
 * Placeholders are `{name}`. Missing values stay as the literal `{name}` so a
 * translator can spot them, matching i18n.Tf.
 */

import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  type ReactNode
} from 'react'

import { hasBridge, ipc } from '../lib/ipc'
import { useLanguageStore } from '../state/language'

export type LanguageTag = string
export type LanguagePreference = 'system' | LanguageTag
export type Translations = (key: string, vars?: Record<string, string | number>) => string

export const FALLBACK_LANGUAGE: LanguageTag = 'en'

const RAW_CATALOGS = import.meta.glob('./locales/*/*.json', {
  eager: true,
  import: 'default'
}) as Record<string, Record<string, unknown>>

interface CatalogFile {
  readonly tag: LanguageTag
  readonly file: string
  readonly entries: Record<string, string>
}

function readCatalogFiles(): CatalogFile[] {
  const files: CatalogFile[] = []
  for (const [path, contents] of Object.entries(RAW_CATALOGS)) {
    const match = /^\.\/locales\/([^/]+)\/([^/]+)\.json$/.exec(path)
    if (match === null) continue
    const tag = match[1]
    const file = match[2]
    if (tag === undefined || file === undefined) continue
    const entries: Record<string, string> = {}
    for (const [key, value] of Object.entries(contents)) {
      if (typeof value !== 'string') {
        throw new Error(`i18n: ${path} has a non-string value for "${key}"`)
      }
      if (key.trim() === '') {
        throw new Error(`i18n: ${path} has an empty key`)
      }
      entries[key] = value
    }
    files.push({ tag, file, entries })
  }
  return files
}

function buildCatalogs(files: readonly CatalogFile[]): Record<LanguageTag, Record<string, string>> {
  const catalogs: Record<LanguageTag, Record<string, string>> = {}
  const owners = new Map<string, string>()
  for (const { tag, file, entries } of files) {
    const table = (catalogs[tag] ??= {})
    for (const [key, value] of Object.entries(entries)) {
      const owner = owners.get(`${tag}:${key}`)
      if (owner !== undefined) {
        throw new Error(`i18n: duplicate key "${key}" in ${tag} (${owner} and ${file})`)
      }
      owners.set(`${tag}:${key}`, file)
      table[key] = value
    }
  }
  return catalogs
}

const CATALOG_FILES = readCatalogFiles()
const CATALOGS = buildCatalogs(CATALOG_FILES)

/** Language tags derived from the locale folders, English first. */
export const LANGUAGES: readonly LanguageTag[] = Object.keys(CATALOGS).sort((a, b) => {
  if (a === FALLBACK_LANGUAGE) return -1
  if (b === FALLBACK_LANGUAGE) return 1
  return a.localeCompare(b)
})

export interface LanguageOption {
  readonly tag: LanguageTag
  /** The language's own name, never translated (internal/i18n convention). */
  readonly nativeName: string
}

/** AVAILABLE is the language menu data, derived from the catalogs. */
export const AVAILABLE: readonly LanguageOption[] = LANGUAGES.map((tag) => ({
  tag,
  nativeName: CATALOGS[tag]?.['language.name'] ?? tag
}))

/** catalogKeys exposes one language's merged key set (parity tests, tooling). */
export function catalogKeys(tag: LanguageTag): string[] {
  const table = CATALOGS[tag]
  return table === undefined ? [] : Object.keys(table).sort()
}

/** systemLanguage resolves an OS locale to a pack; unknown locales use English. */
export function systemLanguage(locale?: string): LanguageTag {
  const browserLanguage = typeof navigator === 'undefined' ? undefined : navigator.language
  const source =
    locale ??
    (typeof browserLanguage === 'string' && browserLanguage !== ''
      ? browserLanguage
      : FALLBACK_LANGUAGE)
  const base = source.toLowerCase().split('-')[0] ?? ''
  return LANGUAGES.includes(base) ? base : FALLBACK_LANGUAGE
}

export function isLanguageTag(value: unknown): value is LanguageTag {
  return typeof value === 'string' && LANGUAGES.includes(value)
}

/** resolveLanguagePreference turns "system" (or an unknown tag) into a pack. */
export function resolveLanguagePreference(
  preference: LanguagePreference | null | undefined,
  locale?: string
): LanguageTag {
  if (preference === null || preference === undefined || preference === 'system') {
    return systemLanguage(locale)
  }
  return isLanguageTag(preference) ? preference : systemLanguage(locale)
}

export function interpolate(template: string, vars?: Record<string, string | number>): string {
  if (vars === undefined) return template
  return template.replace(/\{([a-zA-Z0-9_.]+)\}/g, (whole, name: string) => {
    const value = vars[name]
    return value === undefined ? whole : String(value)
  })
}

/** lookup is the fallback chain: current → English → the key itself. */
export function lookup(key: string, language: LanguageTag = FALLBACK_LANGUAGE): string {
  const current = CATALOGS[language]?.[key]
  if (current !== undefined) return current
  const fallback = CATALOGS[FALLBACK_LANGUAGE]?.[key]
  return fallback ?? key
}

let activeLanguage: LanguageTag = FALLBACK_LANGUAGE

/** translate is the non-React entry point; it follows the active pack. */
export function translate(
  key: string,
  vars?: Record<string, string | number>,
  language: LanguageTag = activeLanguage
): string {
  return interpolate(lookup(key, language), vars)
}

export interface I18nValue {
  /** The pack in use after resolving "system". */
  readonly language: LanguageTag
  readonly preference: LanguagePreference
  readonly available: readonly LanguageOption[]
  readonly t: Translations
  /** Switch packs without a reload; the choice is persisted. */
  readonly setLanguage: (preference: LanguagePreference) => void
}

const I18nContext = createContext<I18nValue | null>(null)

export interface I18nProviderProps {
  readonly children: ReactNode
  readonly initialPreference?: LanguagePreference
}

export function I18nProvider({ children, initialPreference }: I18nProviderProps): JSX.Element {
  const preference = useLanguageStore((state) => state.preference)
  const setPreference = useLanguageStore((state) => state.setPreference)
  const language = resolveLanguagePreference(preference)

  useEffect(() => {
    activeLanguage = language
    if (typeof document !== 'undefined') document.documentElement.lang = language
  }, [language])

  // The host owns the stored preference. With a bridge the main process is
  // authoritative; without one (a browser or a test) the local cache stands.
  useEffect(() => {
    if (initialPreference !== undefined) {
      setPreference(initialPreference, resolveLanguagePreference(initialPreference))
      return
    }
    if (!hasBridge()) return
    let cancelled = false
    void ipc
      .invoke('app.preferences.get')
      .then((preferences) => {
        if (!cancelled) useLanguageStore.getState().adoptRemote(preferences.locale)
      })
      .catch(() => {
        // The local cache remains the fallback for an older main process.
      })
    return () => {
      cancelled = true
    }
  }, [initialPreference, setPreference])

  const setLanguage = useCallback(
    (next: LanguagePreference) => setPreference(next, resolveLanguagePreference(next)),
    [setPreference]
  )

  const t = useCallback<Translations>(
    (key, vars) => interpolate(lookup(key, language), vars),
    [language]
  )

  const value = useMemo<I18nValue>(
    () => ({ language, preference, available: AVAILABLE, t, setLanguage }),
    [language, preference, t, setLanguage]
  )

  return createElement(I18nContext.Provider, { value }, children)
}

export function useI18n(): I18nValue {
  const value = useContext(I18nContext)
  if (value === null) {
    throw new Error('useI18n must be used inside <I18nProvider>')
  }
  return value
}

/** useT is the common case: a translation function bound to the live pack. */
export function useT(): Translations {
  return useI18n().t
}