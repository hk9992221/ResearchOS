/**
 * Shared plumbing for the four MCP runtime tables. Only this folder knows SQL;
 * timestamps always use `formatTimestamp` so the TEXT columns keep their
 * lexicographic order.
 */
import type Database from 'better-sqlite3'

import type { JsonObject } from '@researchos/shared'

import { formatTimestamp, type Clock, SystemClock } from '../../clock'

export type Db = Database.Database

export interface RuntimeOptions {
  clock?: Clock
}

/** Default confirmation lifetime (15 min). */
export const DEFAULT_CONFIRMATION_TTL_SECONDS = 900
export const MIN_CONFIRMATION_TTL_SECONDS = 60
export const MAX_CONFIRMATION_TTL_SECONDS = 86_400

/** Replay window. */
export const IDEMPOTENCY_TTL_SECONDS = 24 * 60 * 60

export const LIST_DEFAULT_LIMIT = 50
export const LIST_MAX_LIMIT = 200

export function nowText(clock: Clock): string {
  return formatTimestamp(clock.now())
}

export function clockOf(options: RuntimeOptions | undefined): Clock {
  return options?.clock ?? new SystemClock()
}

/** Clamps a list limit to the 1–200 range. */
export function clampLimit(limit: number | undefined): number {
  if (limit === undefined || !Number.isFinite(limit) || limit <= 0) return LIST_DEFAULT_LIMIT
  return Math.min(LIST_MAX_LIMIT, Math.trunc(limit))
}

/** Decodes a nullable JSON object column; malformed content is null, not a crash. */
export function jsonObject(raw: string | null | undefined): JsonObject | null {
  if (raw === null || raw === undefined || raw === '') return null
  try {
    const parsed: unknown = JSON.parse(raw)
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? (parsed as JsonObject)
      : null
  } catch {
    return null
  }
}

/** Serializes an optional impact/params/result object. */
export function jsonText(value: unknown): string | null {
  if (value === undefined || value === null) return null
  return JSON.stringify(value)
}

/** `undefined`/`''` become SQL NULL for the optional TEXT columns. */
export function nullText(value: string | null | undefined): string | null {
  return value === undefined || value === '' ? null : value
}

/** Validates the configured TTL, falling back to the 15-minute default. */
export function confirmationTtlSeconds(value: number | undefined): number {
  if (value === undefined || !Number.isFinite(value)) return DEFAULT_CONFIRMATION_TTL_SECONDS
  const seconds = Math.trunc(value)
  if (seconds < MIN_CONFIRMATION_TTL_SECONDS || seconds > MAX_CONFIRMATION_TTL_SECONDS) {
    return DEFAULT_CONFIRMATION_TTL_SECONDS
  }
  return seconds
}