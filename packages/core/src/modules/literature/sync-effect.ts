/**
 * `paper_sync_apply` desktop executor. MCP only enqueues the
 * request; after a human approves, this effect re-checks the frozen upstream
 * hash and applies the overwrite as `actor=user`.
 */
import type { JsonObject } from '@researchos/shared'

import { appError, AppErrorCode } from '../../errors'
import type { ConfirmationEffect } from '../mcp/executors'
import type { LiteratureModule } from './module'

export function createPaperSyncApplyEffect(
  moduleFor: (projectId: string) => LiteratureModule
): ConfirmationEffect {
  return async ({ args }): Promise<JsonObject> => {
    const projectId = typeof args['project_id'] === 'string' ? args['project_id'] : ''
    const paperId = typeof args['paper_id'] === 'string' ? args['paper_id'] : ''
    const expectedNewHash =
      typeof args['expected_new_hash'] === 'string' ? args['expected_new_hash'] : ''
    if (projectId === '' || paperId === '' || expectedNewHash === '') {
      throw appError(AppErrorCode.InvalidArgument, 'paper_sync_apply: 确认参数不完整')
    }
    const result = await moduleFor(projectId).applyConfirmedOverwrite(
      projectId,
      paperId,
      expectedNewHash
    )
    return {
      applied: result.applied,
      skipped: result.skipped,
      conflicted: [...result.conflicted],
      failures: result.failures.map((failure) => ({ ...failure }))
    }
  }
}