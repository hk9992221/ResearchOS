import type { Translations } from '../i18n'

/**
 * formatBytes is the React port of components.HumanBytes: binary units, one
 * decimal, and the same `pages.common.size_*` messages as the Go UI.
 */
export function formatBytes(bytes: number, t: Translations): string {
  const unit = 1024
  if (bytes < unit) return t('pages.common.size_bytes', { n: Math.trunc(bytes) })
  let div = unit
  let exp = 0
  for (let m = bytes / unit; m >= unit && exp < 4; m /= unit) {
    div *= unit
    exp += 1
  }
  const value = (bytes / div).toFixed(1)
  const suffix = 'KMGTP'[exp] ?? 'P'
  return t('pages.common.size_units', { value, unit: suffix })
}