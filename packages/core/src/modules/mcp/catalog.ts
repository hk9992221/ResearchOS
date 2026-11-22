/**
 * Catalog aggregation (rewrite plan §3.6).
 *
 * Each module exports its own `ToolDefinition[]`; this file concatenates them,
 * rejects duplicate names, and pins the total count. W17 activates the 131
 * assertion once every domain module has landed; until then `enforceCount`
 * stays off so a partial build can still register.
 */
import { AppErrorCode, type McpToolInfo } from '@researchos/shared'

import { appError } from '../../errors'
import { experimentsTools } from '../experiments/mcp'
import { exportTools } from '../export/mcp'
import { ideasTools } from '../ideas/mcp'
import { createLiteratureTools } from '../literature/mcp'
import { kanbanTools } from '../kanban/mcp'
import type { NotesModule } from '../notes/index'
import { createNotesTools } from '../notes/mcp'
import { projectTools } from '../project/mcp'
import { searchTools } from '../search/mcp'
import { weeklyTools } from '../weekly/mcp'
import { IDEMPOTENCY_KEY_FIELD } from './tools'
import type { ToolDefinition } from './types'

/** The frozen MCP catalog size. */
export const EXPECTED_TOOL_COUNT = 131

/** Tool count of the phase-1 landed set: core/misc 15 + kanban 21 + weekly 8 + search 3. */
export const PHASE1_TOOL_COUNT = 131

export interface CatalogSource {
  module: string
  tools: readonly ToolDefinition[]
}

export interface CatalogOptions {
  /** Throws unless the total equals `expectedCount`. W17 turns this on. */
  enforceCount?: boolean
  expectedCount?: number
}

export interface Catalog {
  tools: readonly ToolDefinition[]
  byName: ReadonlyMap<string, ToolDefinition>
  byModule: ReadonlyMap<string, readonly ToolDefinition[]>
}

/** Core/misc definitions are built with runtime deps by the composition root. */
export interface LandedToolSources {
  core: readonly ToolDefinition[]
  misc: readonly ToolDefinition[]
}

const notesTools = createNotesTools({
  project: (_projectId, ctx) => {
    const module = ctx.services['notes']
    if (module === undefined) {
      throw appError(AppErrorCode.Internal, 'notes runtime is not wired')
    }
    return module as NotesModule
  }
})

export const PHASE1_DOMAIN_SOURCES: readonly CatalogSource[] = [
  { module: 'kanban', tools: kanbanTools },
  { module: 'weekly', tools: weeklyTools },
  { module: 'search', tools: searchTools },
  { module: 'project', tools: projectTools },
  { module: 'experiments', tools: experimentsTools },
  { module: 'literature', tools: createLiteratureTools() },
  { module: 'notes', tools: notesTools },
  { module: 'ideas', tools: ideasTools },
  { module: 'export', tools: exportTools }
]

/** All phase-1 sources, in catalog order: core, misc, then domain modules. */
export function landedCatalogSources(sources: LandedToolSources): CatalogSource[] {
  return [
    { module: 'core', tools: sources.core },
    { module: 'misc', tools: sources.misc },
    ...PHASE1_DOMAIN_SOURCES
  ]
}

function duplicateNames(tools: readonly ToolDefinition[]): string[] {
  const seen = new Set<string>()
  const duplicates = new Set<string>()
  for (const tool of tools) {
    if (seen.has(tool.name)) duplicates.add(tool.name)
    seen.add(tool.name)
  }
  return [...duplicates].sort()
}

/** Flattens module arrays, checking names (and optionally the frozen count). */
export function buildCatalog(sources: readonly CatalogSource[], options: CatalogOptions = {}): Catalog {
  const tools: ToolDefinition[] = []
  const byModule = new Map<string, readonly ToolDefinition[]>()
  for (const source of sources) {
    byModule.set(source.module, source.tools)
    tools.push(...source.tools)
  }
  const duplicates = duplicateNames(tools)
  if (duplicates.length > 0) {
    throw new Error(`mcp catalog: duplicate tool name(s): ${duplicates.join(', ')}`)
  }
  if (options.enforceCount === true) {
    const expected = options.expectedCount ?? EXPECTED_TOOL_COUNT
    if (tools.length !== expected) {
      throw new Error(`mcp catalog: expected ${expected} tools, found ${tools.length}`)
    }
  }
  return {
    tools,
    byName: new Map(tools.map((tool) => [tool.name, tool])),
    byModule
  }
}

/**
 * Phase-1 catalog: core/misc supplied by the caller, domain arrays fixed here.
 * Count enforcement stays off until every module has landed.
 */
export function buildLandedCatalog(sources: LandedToolSources): Catalog {
  return buildCatalog(landedCatalogSources(sources), { enforceCount: true, expectedCount: PHASE1_TOOL_COUNT })
}

/** The `mcp.tools.list` projection used by Settings → MCP. */
export function toolInfo(definition: ToolDefinition): McpToolInfo {
  return {
    name: definition.name,
    title: definition.title,
    category: definition.category,
    description: definition.description,
    requiresConfirmation: definition.category === 'C',
    requiresIdempotencyKey: IDEMPOTENCY_KEY_FIELD in definition.inputSchema
  }
}

export function catalogToolInfo(catalog: Catalog): McpToolInfo[] {
  return catalog.tools.map(toolInfo)
}