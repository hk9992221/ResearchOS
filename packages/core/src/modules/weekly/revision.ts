import { AppError, AppErrorCode } from '@researchos/shared'

import type { WeeklyContext } from './context'
import { digestFor, getReport, readWeeklySettings, requireReport, requireWritable, writeActor } from './reports'
import {
  normalizeSectionOutput,
  PROMPT_VERSION,
  projectInfoFor,
  sectionMessages,
  sectionSourceRefs,
  TASK_KIND
} from './generate'
import { updateReportRow } from './store'
import { isSectionKey, replaceSectionBody, sectionBody, templateSection } from './template'
import type {
  ActivityDigest,
  SectionRevisionInput,
  SectionRevisionOutcome,
  TemplateSection,
  WeeklySkill
} from './types'
import { buildCheckState, mergeAcks, validateReport } from './validate'

/**
 * Section-level revision: one section is rewritten from
 * the same digest and skill as the whole draft, plus a block of temporary
 * instructions and, optionally, the selection the user made.
 *
 * The instructions live for one call. They are never written to the skill
 * store, settings or report metadata: the stored note names the section and
 * the output only, so a later regeneration cannot inherit them.
 */

export const MAX_REVISION_RUNES = 800
export const MAX_FOCUS_RUNES = 1200
const SECTION_MAX_TOKENS = 900

export async function regenerateSection(
  context: WeeklyContext,
  input: SectionRevisionInput
): Promise<SectionRevisionOutcome> {
  writeActor(input.actor)
  if (!isSectionKey(input.section)) {
    throw new AppError(AppErrorCode.InvalidArgument, `未知的章节：${input.section}`, {
      field: 'section'
    })
  }
  const section = templateSection(input.section)
  if (section === undefined) {
    throw new AppError(AppErrorCode.InvalidArgument, `未知的章节：${input.section}`, {
      field: 'section'
    })
  }
  const instructions = (input.instructions ?? '').trim()
  const focus = (input.focusText ?? '').trim()
  if (instructions === '' && focus === '') {
    throw new AppError(AppErrorCode.InvalidArgument, '请填写修改意见或选中要修改的片段', {
      field: 'instructions'
    })
  }
  const instructionRunes = Array.from(instructions).length
  if (instructionRunes > MAX_REVISION_RUNES) {
    throw new AppError(
      AppErrorCode.InvalidArgument,
      `修改意见过长（${instructionRunes} 字，上限 ${MAX_REVISION_RUNES} 字）`,
      { field: 'instructions' }
    )
  }
  const focusRunes = Array.from(focus).length
  if (focusRunes > MAX_FOCUS_RUNES) {
    throw new AppError(
      AppErrorCode.InvalidArgument,
      `选中的片段过长（${focusRunes} 字，上限 ${MAX_FOCUS_RUNES} 字）；请缩小选区或改用修改意见`,
      { field: 'focusText' }
    )
  }

  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  if (report.status === 'confirmed') {
    throw new AppError(AppErrorCode.Conflict, '已确认周报不可修订；请先撤回为草稿', {
      reason: 'report.confirmed_read_only'
    })
  }
  if (report.contentMd.trim() === '') {
    throw new AppError(AppErrorCode.InvalidArgument, '报告尚无正文，无法按节修订', {
      reason: 'report.empty'
    })
  }
  if (input.expectedHash !== undefined && input.expectedHash !== report.contentHash) {
    throw new AppError(AppErrorCode.Conflict, '内容已在别处修改', {
      reason: 'report.content_conflict',
      updatedAt: report.updatedAt,
      contentHash: report.contentHash
    })
  }
  const ai = context.ai
  if (ai === undefined) {
    throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置，无法按节修订', {
      reason: 'ai.unavailable'
    })
  }

  const digest = digestFor(context, report.period)
  const info = projectInfoFor(context)
  const skill = context.skills.resolve(input.skillId)
  const currentBody = sectionBody(report.contentMd, section.key)
  if (currentBody === undefined) {
    throw new AppError(AppErrorCode.InvalidArgument, `报告中找不到「${section.title}」这一节`, {
      reason: 'report.section_missing'
    })
  }

  input.progress?.(0, 1, section.key)
  const result = await ai.generate({
    taskKind: TASK_KIND,
    promptVersion: PROMPT_VERSION,
    messages: revisionMessages(info, digest, section, skill, currentBody, focus, instructions),
    sourceRefs: sectionSourceRefs(digest, section.key),
    entity: { type: 'weekly_report', id: report.id },
    maxOutputTokens: SECTION_MAX_TOKENS,
    onDelta:
      input.delta === undefined ? undefined : (text: string): void => input.delta?.(section.key, text)
  })
  const lines = normalizeSectionOutput(result.contentMd)
  if (lines === undefined) {
    throw new AppError(AppErrorCode.AiOutputInvalid, `${section.title}：分区返回空内容`, {
      section: section.key
    })
  }
  const assembled = replaceSectionBody(report.contentMd, section.key, lines)
  if (assembled === undefined) {
    throw new AppError(AppErrorCode.InvalidArgument, `找不到章节「${section.title}」`, {
      reason: 'report.section_missing'
    })
  }
  if (Buffer.byteLength(assembled, 'utf8') > 512 * 1024) {
    throw new AppError(AppErrorCode.InvalidArgument, '修订后的正文超过 512 KiB 上限', {
      reason: 'report.content_too_large'
    })
  }
  const violations = validateReport(assembled, digest)
  const now = context.clock.now().toISOString()
  const note = revisionNote(section, result.outputId)

  context.handle.inTx(() => {
    const current = requireReport(context.handle.db, input.reportId, settings.timeZone)
    if (current.status === 'confirmed') {
      throw new AppError(AppErrorCode.Conflict, '已确认周报不可修订', {
        reason: 'report.confirmed_read_only'
      })
    }
    if (input.expectedHash !== undefined && current.contentHash !== input.expectedHash) {
      throw new AppError(AppErrorCode.Conflict, '内容已在别处修改', {
        reason: 'report.content_conflict',
        updatedAt: current.updatedAt,
        contentHash: current.contentHash
      })
    }
    current.contentMd = assembled
    current.updatedAt = now
    const generation = current.meta.generation
    current.meta = {
      ...current.meta,
      generation:
        generation === undefined
          ? {
              mode: 'ai',
              ai_status: 'ok',
              ai_output_id: result.outputId,
              ai_output_ids: [result.outputId],
              template_id: 'default-7',
              template_version: 1,
              attempts: 1,
              notes: note
            }
          : { ...generation, notes: appendNote(generation.notes, note) },
      checks: buildCheckState(mergeAcks(violations, current.meta.checks), now)
    }
    updateReportRow(context.handle.db, current)
  })
  input.progress?.(1, 1, '')
  const stored = getReport(context, input.reportId)
  return {
    report: stored,
    section: section.key,
    markdown: lines.join('\n'),
    violations,
    outputId: result.outputId
  }
}

/** Records which section a tempory revision rewrote; never the instructions. */
export function revisionNote(section: TemplateSection, outputId: string): string {
  let note = `第${section.num}节「${section.title}」按临时修改意见重写`
  if (outputId !== '') note += `（output ${outputId}）`
  return note
}

export function appendNote(existing: string | undefined, note: string): string {
  const trimmed = (existing ?? '').trim()
  return trimmed === '' ? note : `${trimmed}\n${note}`
}

function revisionMessages(
  info: { name: string; summary: string; goals: string },
  digest: ActivityDigest,
  section: TemplateSection,
  skill: WeeklySkill,
  current: string,
  focus: string,
  instructions: string
): Array<{ role: 'system' | 'user'; content: string }> {
  const messages = sectionMessages(info, digest, section, skill)
  const last = messages[messages.length - 1]
  if (last === undefined) return []
  let body = `${last.content}\n本次不是从零写作，而是修订下面这一节的既有正文。\n\n当前这一节的正文：\n${current}\n`
  if (focus !== '') {
    body += `\n用户选中的片段（本次修订的重点；只改与它相关的表述，不要牵动整节的其余部分）：\n<<<\n${focus}\n>>>\n`
  }
  body += '\n本轮临时修改意见（只适用于本次生成，不是长期写作规范）：\n'
  body += instructions === '' ? '（未填写，按选中片段与既有正文自行判断）' : instructions
  body +=
    '\n\n输出要求：给出该节修订后的完整正文，不要输出标题、不要解释改动；仍未改动的条目照原样保留；引用格式与硬性约束不变。\n'
  return [...messages.slice(0, -1), { role: last.role, content: body }]
}
