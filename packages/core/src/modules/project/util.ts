import { existsSync, readdirSync, statSync } from 'node:fs'
import * as path from 'node:path'

/** Sums the regular files under a directory; a missing tree counts as zero. */
export function directoryBytes(directory: string): number {
  if (!existsSync(directory)) return 0
  let total = 0
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name)
    if (entry.isDirectory()) total += directoryBytes(full)
    else if (entry.isFile()) total += statSync(full).size
  }
  return total
}
