/**
 * The semver subset the market needs: compare two published
 * versions to decide whether a remote artifact is newer. A full `semver`
 * dependency is not warranted for `>`, so this covers numeric cores,
 * optional `v` prefix and pre-release ordering rules that actually appear in
 * Release tags. Anything unparsable compares as "unknown" (`null`), which the
 * callers treat as "never update".
 */

export interface Semver {
  major: number
  minor: number
  patch: number
  /** Dotted identifiers after `-`; numeric ones compare numerically. */
  prerelease: readonly string[]
  /** Build metadata after `+` is ignored for precedence, kept for display. */
  build: string
}

const SEMVER_PATTERN =
  /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$/

export function parseSemver(value: string): Semver | null {
  const match = SEMVER_PATTERN.exec(value.trim())
  if (match === null) return null
  const [, major, minor, patch, prerelease, build] = match
  return {
    major: Number(major),
    minor: Number(minor),
    patch: Number(patch),
    prerelease: prerelease === undefined ? [] : prerelease.split('.'),
    build: build ?? ''
  }
}

export function isSemver(value: string): boolean {
  return parseSemver(value) !== null
}

function comparePrerelease(
  left: readonly string[],
  right: readonly string[]
): number {
  // A version without a pre-release outranks one with it.
  if (left.length === 0) return right.length === 0 ? 0 : 1
  if (right.length === 0) return -1
  const length = Math.max(left.length, right.length)
  for (let index = 0; index < length; index += 1) {
    const a = left[index]
    const b = right[index]
    if (a === undefined) return b === undefined ? 0 : -1
    if (b === undefined) return 1
    const numericA = /^\d+$/.test(a)
    const numericB = /^\d+$/.test(b)
    if (numericA && numericB) {
      const delta = Number(a) - Number(b)
      if (delta !== 0) return delta < 0 ? -1 : 1
      continue
    }
    // Numeric identifiers always have lower precedence.
    if (numericA) return -1
    if (numericB) return 1
    if (a !== b) return a < b ? -1 : 1
  }
  return 0
}

/** `-1` when `left` is older, `0` equal, `1` newer, `null` unparsable input. */
export function compareSemver(left: string, right: string): number | null {
  const a = parseSemver(left)
  const b = parseSemver(right)
  if (a === null || b === null) return null
  for (const key of ['major', 'minor', 'patch'] as const) {
    if (a[key] !== b[key]) return a[key] < b[key] ? -1 : 1
  }
  return comparePrerelease(a.prerelease, b.prerelease)
}

/** True only when both sides parse and `left` is strictly newer. */
export function semverGt(left: string, right: string): boolean {
  return compareSemver(left, right) === 1
}

/** The version part of a Release tag (`v1.2.3-rc.1` → `1.2.3-rc.1`). */
export function versionFromTag(tag: string): string {
  return tag.trim().replace(/^v/i, '')
}
