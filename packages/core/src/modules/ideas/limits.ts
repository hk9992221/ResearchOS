/** Field budgets of the ideas spec — shared by the service, IPC and MCP schemas. */
export const IDEA_LIMITS = {
  titleRunes: 200,
  descriptionRunes: 100_000,
  rejectionReasonRunes: 4_000,
  evidenceContentRunes: 8_000,
  instructionRunes: 500,
  maxDraftSourceRefs: 8,
  defaultMaxDrafts: 8,
  maxDrafts: 20,
  /** AI item content bounds (§5.7.3). */
  aiContentMinRunes: 20,
  aiContentMaxRunes: 1_000,
  aiRationaleMaxRunes: 300,
  /** Extraction input budgets (§5.7.2). */
  sourceChars: 4_000,
  totalChars: 24_000,
  /** derived_from chain cap (§5.4). */
  maxLineageDepth: 20
} as const

/** Character (code point) count, matching Go's `utf8.RuneCountInString`. */
export function runeCount(value: string): number {
  let count = 0
  for (const _ of value) count += 1
  return count
}