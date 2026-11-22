/**
 * Bundle file-name sanitizing (§5.9.3). Mirrors the Go `sanitizeBase`: letters,
 * digits and `_` survive, `-` is kept once, every other rune becomes a single
 * separator run, the result is trimmed and cut to 80 runes, empty values fall
 * back to `project-<id8>` and Windows device names get a `_` prefix.
 */

const MAX_BASE_RUNES = 80

const RESERVED_NAMES: Readonly<Record<string, true>> = {
  CON: true,
  PRN: true,
  AUX: true,
  NUL: true,
  COM1: true,
  COM2: true,
  COM3: true,
  COM4: true,
  COM5: true,
  COM6: true,
  COM7: true,
  COM8: true,
  COM9: true,
  LPT1: true,
  LPT2: true,
  LPT3: true,
  LPT4: true,
  LPT5: true,
  LPT6: true,
  LPT7: true,
  LPT8: true,
  LPT9: true
}

const LETTER_OR_DIGIT = /[\p{L}\p{Nd}_]/u

export function fallbackBase(projectId: string): string {
  const id = projectId.slice(0, 8)
  return `project-${id === '' ? 'export' : id}`
}

export function truncateRunes(value: string, limit: number): string {
  const runes = Array.from(value)
  return runes.length <= limit ? value : runes.slice(0, limit).join('')
}

function trimSeparators(value: string): string {
  return value.replace(/^[-_]+/, '').replace(/[-_]+$/, '')
}

/** Turns an arbitrary project name into a portable bundle base name. */
export function sanitizeBase(raw: string, projectId: string): string {
  let out = ''
  let lastDash = false
  for (const rune of raw) {
    if (LETTER_OR_DIGIT.test(rune) || rune === '-') {
      if (rune === '-') {
        if (lastDash) continue
        out += '-'
        lastDash = true
        continue
      }
      out += rune
      lastDash = false
      continue
    }
    if (out === '' || lastDash) continue
    out += '-'
    lastDash = true
  }

  let name = trimSeparators(out)
  if (name === '') name = fallbackBase(projectId)
  name = truncateRunes(name, MAX_BASE_RUNES)
  name = trimSeparators(name)
  if (name === '') name = fallbackBase(projectId)
  if (RESERVED_NAMES[name.toUpperCase()] === true) name = `_${name}`
  return name
}

/** Suffix of the `-2..-100` collision policy; index 0 is the plain name. */
export function candidateBase(base: string, index: number): string {
  return index === 0 ? base : `${base}-${index + 1}`
}