import { AppError, AppErrorCode, type ActivityActor, type Project } from '@researchos/shared'

import { appError } from '../../errors'

/**
 * Project-module error helpers. Every rejection is a typed `AppError` with the
 * code set, so IPC, MCP and the UI share one mapping and no domain
 * failure degrades into a catch-all INTERNAL (defect 1).
 */

export function projectNotFound(projectId: string): AppError {
  return appError(AppErrorCode.NotFound, `项目不存在：${projectId}`, { details: { projectId } })
}

export function projectArchived(projectId: string): AppError {
  return appError(AppErrorCode.ArchivedReadOnly, '项目已归档，只读', { details: { projectId } })
}

export function invalidField(field: string, reason: string): AppError {
  return appError(AppErrorCode.InvalidArgument, `${field}: ${reason}`, { details: { field } })
}

/** Stale optimistic token: the stored row is returned for the conflict dialog. */
export function projectConflict(current: Project): AppError {
  return appError(AppErrorCode.Conflict, '项目已被修改，请刷新后重试', {
    details: { projectId: current.id, current }
  })
}

export function invalidActor(actor: string): AppError {
  return appError(AppErrorCode.InvalidArgument, `调用者身份不合法：${actor}`, { details: { actor } })
}

/**
 * The business change is committed but its Activity row is not (§5.4 "b 失败
 * 不回滚已创建的项目"). Callers surface "saved, activity missing"; details name
 * the project so the UI can still navigate to it.
 */
export function eventMissing(projectId: string, cause: unknown): AppError {
  const message = cause instanceof Error ? cause.message : String(cause)
  return appError(AppErrorCode.Internal, '项目已保存，但活动记录写入失败', {
    details: { eventMissing: true, projectId, cause: message },
    cause
  })
}

export function deleteNotConfirmed(reason: string): AppError {
  return appError(AppErrorCode.UserConfirmationRequired, '永久删除需要应用内人工确认', {
    details: { reason }
  })
}

export function featureUnavailable(feature: string): AppError {
  return appError(AppErrorCode.Unsupported, `${feature} 模块未就绪`, { details: { feature } })
}

/** Actor allowed to edit/state-change a project: user or mcp (never ai). */
export function assertProjectActor(actor: string): ActivityActor {
  if (actor === 'user' || actor === 'mcp') return actor
  throw invalidActor(actor)
}