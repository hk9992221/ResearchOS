import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/** Canonical window geometry and its persisted form (Go `StateKey "main"`). */
export const WINDOW_STATE_KEY = 'main'
export const WINDOW_STATE_FILE = 'window-state.json'
export const WINDOW_STATE_VERSION = 1

export const DEFAULT_WINDOW_WIDTH = 1440
export const DEFAULT_WINDOW_HEIGHT = 900
export const MIN_WINDOW_WIDTH = 1024
export const MIN_WINDOW_HEIGHT = 700

/** Minimum overlap with a display for a saved position to be considered usable. */
const MIN_VISIBLE_PX = 100

export interface DisplayBounds {
  x: number
  y: number
  width: number
  height: number
}

export interface WindowState {
  x: number
  y: number
  width: number
  height: number
  maximized: boolean
}

interface StateFile {
  version: number
  main?: WindowState
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function overlapWidth(a: WindowState, b: DisplayBounds): number {
  return Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)
}

function overlapHeight(a: WindowState, b: DisplayBounds): number {
  return Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y)
}

export function defaultWindowState(display?: DisplayBounds): WindowState {
  if (display === undefined) {
    return { x: 0, y: 0, width: DEFAULT_WINDOW_WIDTH, height: DEFAULT_WINDOW_HEIGHT, maximized: false }
  }
  const width = Math.min(DEFAULT_WINDOW_WIDTH, display.width)
  const height = Math.min(DEFAULT_WINDOW_HEIGHT, display.height)
  return {
    x: display.x + Math.max(0, Math.round((display.width - width) / 2)),
    y: display.y + Math.max(0, Math.round((display.height - height) / 2)),
    width,
    height,
    maximized: false
  }
}

/**
 * Restores a saved geometry onto the currently connected displays. A state
 * whose window no longer overlaps any display by at least 100×100 px is
 * re-centered on the primary display; sizes are clamped to the display and to
 * the application minimum.
 */
export function clampWindowState(state: WindowState, displays: readonly DisplayBounds[]): WindowState {
  const normalized: WindowState = {
    x: isFiniteNumber(state.x) ? Math.round(state.x) : 0,
    y: isFiniteNumber(state.y) ? Math.round(state.y) : 0,
    width: isFiniteNumber(state.width) && state.width > 0 ? Math.round(state.width) : DEFAULT_WINDOW_WIDTH,
    height: isFiniteNumber(state.height) && state.height > 0 ? Math.round(state.height) : DEFAULT_WINDOW_HEIGHT,
    maximized: state.maximized === true
  }

  const first = displays[0]
  if (first === undefined) {
    return { ...normalized, width: Math.max(MIN_WINDOW_WIDTH, normalized.width), height: Math.max(MIN_WINDOW_HEIGHT, normalized.height) }
  }

  const matched = displays.find(
    (display) =>
      overlapWidth(normalized, display) >= MIN_VISIBLE_PX && overlapHeight(normalized, display) >= MIN_VISIBLE_PX
  )
  const target = matched ?? first

  const width = Math.min(Math.max(normalized.width, MIN_WINDOW_WIDTH), target.width)
  const height = Math.min(Math.max(normalized.height, MIN_WINDOW_HEIGHT), target.height)

  if (matched === undefined) {
    return {
      x: target.x + Math.max(0, Math.round((target.width - width) / 2)),
      y: target.y + Math.max(0, Math.round((target.height - height) / 2)),
      width,
      height,
      maximized: normalized.maximized
    }
  }

  const maxX = target.x + target.width - width
  const maxY = target.y + target.height - height
  const x = Math.min(Math.max(normalized.x, target.x), maxX)
  const y = Math.min(Math.max(normalized.y, target.y), maxY)

  return { x, y, width, height, maximized: normalized.maximized }
}

export function parseWindowState(raw: string | null): WindowState | null {
  if (raw === null || raw === '') return null

  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }

  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
  // JSON.parse output is untrusted; every consumed field is checked below.
  const { x, y, width, height, maximized } = parsed as {
    x?: unknown
    y?: unknown
    width?: unknown
    height?: unknown
    maximized?: unknown
  }
  if (!isFiniteNumber(x) || !isFiniteNumber(y) || !isFiniteNumber(width) || !isFiniteNumber(height)) {
    return null
  }
  if (width <= 0 || height <= 0) return null

  return {
    x: Math.round(x),
    y: Math.round(y),
    width: Math.round(width),
    height: Math.round(height),
    maximized: maximized === true
  }
}

export function serializeWindowState(state: WindowState): string {
  return JSON.stringify({
    x: Math.round(state.x),
    y: Math.round(state.y),
    width: Math.round(state.width),
    height: Math.round(state.height),
    maximized: state.maximized === true
  })
}

/** Reads `window-state.json` from a userData/config directory. */
export function readWindowState(directory: string): WindowState | null {
  const file = join(directory, WINDOW_STATE_FILE)
  if (!existsSync(file)) return null

  try {
    const parsed: unknown = JSON.parse(readFileSync(file, 'utf8'))
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    const entry = 'main' in parsed ? parsed.main : undefined
    return parseWindowState(entry === undefined ? null : JSON.stringify(entry))
  } catch {
    return null
  }
}

/** Writes atomically so a crash mid-save never leaves a truncated state file. */
export function writeWindowState(directory: string, state: WindowState): void {
  mkdirSync(directory, { recursive: true })
  const file = join(directory, WINDOW_STATE_FILE)
  const payload: StateFile = { version: WINDOW_STATE_VERSION, [WINDOW_STATE_KEY]: state }
  const temporary = `${file}.tmp`
  writeFileSync(temporary, `${JSON.stringify(payload, null, 2)}\n`, 'utf8')
  renameSync(temporary, file)
}