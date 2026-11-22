/**
 * Safe access to the renderer's localStorage.
 *
 * A storage that is full, disabled or absent must never break the shell: the
 * value simply does not survive the session, and every caller keeps working
 * with its in-memory default.
 */

export function readStorage(key: string): string | null {
  if (typeof window === 'undefined') return null
  try {
    return window.localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writeStorage(key: string, value: string): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(key, value)
  } catch {
    // Read-only or full storage: the current session keeps the value in memory.
  }
}