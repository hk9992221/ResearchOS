/**
 * WCAG 2.x contrast helpers.
 *
 * The palette values are the ones in tokens.css (ported from
 * internal/ui/tokens/tokens.go). They exist here in TypeScript so the design
 * system can be checked by tests and so a component that paints text on an
 * accent or status fill picks black or white the same way the Go UI does
 * (`Color.ReadableText`).
 *
 * This module never invents a color: it only measures the tokens.
 */

export interface Rgb {
  readonly r: number
  readonly g: number
  readonly b: number
}

/** The light palette of ui_plan.md §2.1. Keys are the CSS custom properties. */
export const LIGHT_PALETTE = {
  'bg-canvas': '#F7F8FA',
  'bg-surface': '#FFFFFF',
  'bg-subtle': '#F1F3F6',
  'bg-hover': '#F2F4F7',
  'border-default': '#E4E7EC',
  'border-strong': '#D0D5DD',
  'text-primary': '#252A34',
  'text-secondary': '#667085',
  'text-muted': '#98A2B3',
  'accent-default': '#4263EB',
  'accent-hover': '#3451D1',
  'accent-subtle': '#EEF2FF',
  'status-success': '#15803D',
  'status-warning': '#B54708',
  'status-danger': '#B42318',
  'status-info': '#175CD3'
} as const

/** The dark palette of ui_plan.md §2.2. */
export const DARK_PALETTE = {
  'bg-canvas': '#111318',
  'bg-surface': '#181B22',
  'bg-subtle': '#20242D',
  'bg-hover': '#262B36',
  'border-default': '#303642',
  'border-strong': '#414958',
  'text-primary': '#E8ECF3',
  'text-secondary': '#A4ADBC',
  'text-muted': '#7C8798',
  'accent-default': '#8EA2FF',
  'accent-hover': '#A7B5FF',
  'accent-subtle': '#242E56',
  'status-success': '#56D68B',
  'status-warning': '#FDBA74',
  'status-danger': '#FDA29B',
  'status-info': '#84CAFF'
} as const

export type PaletteName = keyof typeof LIGHT_PALETTE

/** parseHex accepts #RRGGBB or #RRGGBBAA, as tokens.go does. */
export function parseHex(hex: string): Rgb {
  const match = /^#?([0-9a-f]{6})(?:[0-9a-f]{2})?$/i.exec(hex.trim())
  if (match === null || match[1] === undefined) {
    throw new Error(`invalid color literal: ${hex}`)
  }
  const value = Number.parseInt(match[1], 16)
  return { r: (value >> 16) & 0xff, g: (value >> 8) & 0xff, b: value & 0xff }
}

function channelLuminance(channel: number): number {
  const s = channel / 255
  return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
}

/** relativeLuminance is the WCAG 2.x relative luminance, 0 to 1. */
export function relativeLuminance(color: Rgb | string): number {
  const c = typeof color === 'string' ? parseHex(color) : color
  return (
    0.2126 * channelLuminance(c.r) +
    0.7152 * channelLuminance(c.g) +
    0.0722 * channelLuminance(c.b)
  )
}

/** contrastRatio returns the WCAG contrast ratio between two colors, 1 to 21. */
export function contrastRatio(a: Rgb | string, b: Rgb | string): number {
  const la = relativeLuminance(a)
  const lb = relativeLuminance(b)
  const [l1, l2] = la >= lb ? [la, lb] : [lb, la]
  return (l1 + 0.05) / (l2 + 0.05)
}

/** meetsAA reports the WCAG AA threshold; large text may use 3:1. */
export function meetsAA(ratio: number, large = false): boolean {
  return ratio >= (large ? 3 : 4.5)
}

/**
 * readableText returns the text color with the higher contrast on a fill, the
 * same choice `Color.ReadableText` makes in the Go UI.
 */
export function readableText(background: Rgb | string): '#111318' | '#FFFFFF' {
  const dark = '#111318'
  const light = '#FFFFFF'
  return contrastRatio(dark, background) >= contrastRatio(light, background) ? dark : light
}

/** paletteContrast is a convenience for tests: ratio of two named tokens. */
export function paletteContrast(
  palette: Record<PaletteName, string>,
  fg: PaletteName,
  bg: PaletteName
): number {
  return contrastRatio(palette[fg], palette[bg])
}