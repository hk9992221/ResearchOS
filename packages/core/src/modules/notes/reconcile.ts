import type {
  ReconcileAction,
  ReconcileApplyResult,
  ReconcileReport
} from '@researchos/shared/contracts/notes'

import { formatTimestamp } from '../../clock'
import { AppErrorCode, isAppError } from '../../errors'
import { ReconcileService } from '../../reconcile'
import type { NotesContext } from './support'

/**
 * Adapter over the foundation `ReconcileService` (W1-B). The report is the
 * canonical `ReconcileReport` from `@researchos/shared/contracts/notes`;
 * timestamps are ISO strings at the IPC/MCP boundary.
 */
function service(ctx: NotesContext): ReconcileService {
  return new ReconcileService(ctx.handle, ctx.events, ctx.clock)
}

export function scanNotes(ctx: NotesContext): ReconcileReport {
  const report = service(ctx).scan()
  return {
    projectId: report.projectId,
    scanned: report.scanned,
    orphans: report.orphans.map((orphan) => ({
      relPath: orphan.relPath,
      size: orphan.size,
      modTime: formatTimestamp(orphan.modTime)
    })),
    missing: report.missing,
    modified: report.modified,
    generatedAt: formatTimestamp(report.generatedAt)
  }
}

function serializeError(error: unknown): { code: string; message: string } {
  if (isAppError(error)) return { code: error.code, message: error.message }
  return {
    code: AppErrorCode.Internal,
    message: error instanceof Error ? error.message : String(error)
  }
}

export function applyReconcile(ctx: NotesContext, actions: ReconcileAction[]): ReconcileApplyResult {
  const result = service(ctx).apply(actions)
  return {
    items: result.items.map((item) =>
      item.error === undefined
        ? { action: item.action, ...(item.noteId === undefined ? {} : { noteId: item.noteId }) }
        : {
            action: item.action,
            ...(item.noteId === undefined ? {} : { noteId: item.noteId }),
            error: serializeError(item.error)
          }
    )
  }
}