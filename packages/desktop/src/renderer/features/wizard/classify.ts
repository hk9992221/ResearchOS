import type { WorkspaceScanReport } from '@researchos/shared'

/**
 * The five folder states of the Go workspace probe, derived from a scan of
 * the candidate folder. The scan lists paths relative to the root; the marker
 * and the global database are the two files that decide what the folder is.
 */
export type WizardDirectoryState =
  | 'empty'
  | 'existing'
  | 'foreign'
  | 'marker_missing'
  | 'marker_corrupt'

const MARKER = '.researchos'
const GLOBAL_DB = 'global.db'

export function classifyWorkspace(report: WorkspaceScanReport): WizardDirectoryState {
  const paths = new Set(
    report.entries.map((entry) =>
      entry.relativePath.replace(/\\/g, '/').replace(/^\.\//, '').replace(/^\/+/, '')
    )
  )
  const hasMarker = paths.has(MARKER)
  const hasGlobalDb = paths.has(GLOBAL_DB)
  if (report.entries.length === 0) return 'empty'
  if (hasMarker && hasGlobalDb) return 'existing'
  if (hasGlobalDb && !hasMarker) return 'marker_missing'
  if (hasMarker && !hasGlobalDb) return 'marker_corrupt'
  return 'foreign'
}

/** The action the review step offers for a state; null means blocked. */
export function wizardAction(state: WizardDirectoryState): 'initialize' | 'open' | null {
  if (state === 'empty') return 'initialize'
  if (state === 'existing') return 'open'
  return null
}