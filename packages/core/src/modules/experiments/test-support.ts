import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import * as path from 'node:path'

import { FixedClock } from '../../clock'
import { newId } from '../../ids'
import { WorkspaceManager } from '../../workspace/index'
import type { ExperimentsIndexHook, ExperimentsServiceOptions } from './service'
import { ExperimentsService } from './service'

export interface ExperimentsFixture {
  scratch: string
  workspaceRoot: string
  manager: WorkspaceManager
  projectId: string
  clock: FixedClock
  /** Creates one external directory outside the workspace and fills it. */
  createDir(name: string, files: Record<string, string | Buffer>): string
  service(overrides?: Partial<ExperimentsServiceOptions>): ExperimentsService
  close(): void
}

/**
 * Real workspace + project database for the experiments tests: no mocks, the
 * real migrations and real transactions. External directories live next to the
 * workspace so the internal-path guard stays meaningful.
 */
export function createFixture(): ExperimentsFixture {
  const scratch = mkdtempSync(path.join(tmpdir(), 'researchos-experiments-'))
  const workspaceRoot = path.join(scratch, 'workspace')
  const clock = new FixedClock('2026-11-18T10:00:00.000Z')
  const manager = WorkspaceManager.init(workspaceRoot, { clock, skipBackup: true })
  const projectId = newId(clock)
  clock.advance(1)
  manager.global.db
    .prepare(
      `INSERT INTO projects(id,name,summary,goals,focus_md,status,archived_at,created_at,updated_at)
       VALUES (?, ?, '', '', '', 'active', NULL, ?, ?)`
    )
    .run(projectId, 'Experiments Fixture', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')
  mkdirSync(path.join(workspaceRoot, 'projects', projectId), { recursive: true })
  manager.project(projectId)

  const createDir = (name: string, files: Record<string, string | Buffer>): string => {
    const directory = path.join(scratch, name)
    mkdirSync(directory, { recursive: true })
    for (const [relative, content] of Object.entries(files)) {
      const target = path.join(directory, ...relative.split('/'))
      mkdirSync(path.dirname(target), { recursive: true })
      writeFileSync(target, content)
      clock.advance(1)
    }
    return directory
  }

  return {
    scratch,
    workspaceRoot,
    manager,
    projectId,
    clock,
    createDir,
    service(overrides = {}) {
      return new ExperimentsService({
        workspace: manager,
        projectId,
        clock,
        ...overrides
      })
    },
    close() {
      manager.close()
      rmSync(scratch, { recursive: true, force: true })
    }
  }
}

export const noopIndexer: ExperimentsIndexHook = {
  onEntityChanged() {
    // Post-commit hook is irrelevant for these assertions.
  }
}