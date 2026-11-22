import type { Translations } from '../../i18n'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** formatRelative renders the compact time strings of the Go notes page. */
export function formatRelative(t: Translations, iso: string, now: number = Date.now()): string {
  const at = Date.parse(iso)
  if (Number.isNaN(at)) return ''
  const delta = Math.max(0, now - at)
  if (delta < MINUTE) return t('pages.notes.time_just_now')
  if (delta < HOUR) return t('pages.notes.time_minutes', { n: Math.floor(delta / MINUTE) })
  if (delta < DAY) return t('pages.notes.time_hours', { n: Math.floor(delta / HOUR) })
  return t('pages.notes.time_days', { n: Math.floor(delta / DAY) })
}