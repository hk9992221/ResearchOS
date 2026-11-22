import { AppError, AppErrorCode } from '@researchos/shared'

import type { WeeklyAiResult, WeeklyContext } from './context'
import { sectionIsEmpty, sectionOf } from './digest'
import {
  coverageMeta,
  createReport,
  digestFor,
  getReport,
  readWeeklySettings,
  requireReport,
  requireWritable,
  resolvePeriod,
  writeActor
} from './reports'
import { updateReportRow } from './store'
import {
  DEFAULT_TEMPLATE,
  assembleReport,
  buildSkeleton,
  periodEndDate,
  sectionTitle,
  skeletonLines,
  truncateRunes
} from './template'
import type {
  ActivityDigest,
  DigestSection,
  GenerateInput,
  GenerateOutcome,
  GeneratePeriodInput,
  GenerationMeta,
  PlannedTask,
  SectionKey,
  TemplateSection,
  Violation,
  WeeklyReport,
  WeeklySkill
} from './types'
import { buildCheckState, mergeAcks, validateReport } from './validate'

export const PROMPT_VERSION = 'weekly_report/v3'
export const TASK_KIND = 'weekly_report' as const
const SECTION_ATTEMPTS = 2
const SECTION_MAX_TOKENS = 900
const PROMPT_SECTION_ITEMS = 24
const PROMPT_BRIEF_RUNES = 200

/** Per-section writing hints: what the section is for, never the facts. */
export const SECTION_HINTS: Record<SectionKey, string> = {
  work: '先用一至两句话总述本周主线：推进了什么、推进到哪一步、卡在哪里；其下按主题分条，合并同一实体的多条记录。',
  literature: '写读文献得到的要点以及它与本课题的关系，不写「导入/收藏了文献」这类动作。',
  theory: '写理论推导、方法或模型上的推进与结论。',
  experiment: '写实验与运行的结果和当前状态；不下「成功/失败」结论。',
  idea: '写 Idea 的状态变化、证据与取舍，不写「创建了 Idea」。',
  problem: '写受阻项、未解决的矛盾与需要决策的点；原因只引用原文。',
  next_week: '只列下周要做的候选任务，每条写明依据（周计划 / 截止日 / 顺延）。'
}

/** The hard contract of one section call. Code, never a skill. */
export const PROMPT_SECTION_CONTRACT = `你是科研周报撰写助手。本次只写七个分区中的**一个**分区，只输出该分区的正文（Markdown 列表或短段落），不要输出任何标题、不要解释、不要 JSON 包装。

硬性约束：
1. 输出 2–6 条；每条不超过 120 字；每条事实性条目末尾附 [[ref:type:id]] 引用（可多个）。
2. 只能使用输入的活动条目、候选计划与项目元信息；不得补充外部知识，不得编造数值、结论或实验结果。
3. 数值只能引用输入中出现过的数字；不确定的度量用文字描述并标「（待确认）」。
4. 引用 unconfirmed_source=true 的内容必须标注「（待确认）」；无来源的成果性表述同样标注。
5. 计划项只能出现在「下周计划」，只能来自候选，不得写成已完成；其他分区不得写计划。
6. 实验与运行不得下「成功/失败」结论；Idea 状态只能引用 status.changed 的新值。
7. 不写「创建项目」「导入文献」「创建笔记」「新建任务」「导出文件」这类记录性动作——它们不是进展。
8. 本节没有可写的实质内容时，只输出一行「本周期无记录」。`

interface SectionRun {
  lines: Map<SectionKey, string[]>
  outputIds: string[]
  failed: SectionKey[]
  attempts: number
  last?: WeeklyAiResult
  firstError?: unknown
}

function requiredAi(context: WeeklyContext): NonNullable<WeeklyContext['ai']> {
  const ai = context.ai
  if (ai === undefined) {
    throw new AppError(AppErrorCode.AiUnavailable, 'AI 服务未配置', { reason: 'ai.unavailable' })
  }
  return ai
}

/** (Re)generates one report draft; AI failure degrades section by section. */
export async function generateDraft(
  context: WeeklyContext,
  input: GenerateInput
): Promise<GenerateOutcome> {
  writeActor(input.actor)
  requireWritable(context.globalDb, context.projectId)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  if (report.status === 'confirmed') {
    throw new AppError(AppErrorCode.Conflict, '已确认周报不可重新生成；请先撤回为草稿', {
      reason: 'report.confirmed_read_only'
    })
  }
  if (input.replace !== true && report.contentMd.trim() !== '') {
    throw new AppError(AppErrorCode.Conflict, '报告已有内容；覆盖需显式 replace', {
      reason: 'report.not_empty'
    })
  }
  if (
    input.replace === true &&
    input.actor === 'mcp' &&
    report.contentMd.trim() !== '' &&
    hasManualTrace(report)
  ) {
    throw new AppError(AppErrorCode.UserConfirmationRequired, '覆盖人工编辑内容需由应用内用户确认', {
      reason: 'confirmation.required',
      reportId: report.id
    })
  }

  const info = projectInfoFor(context)
  const digest = digestFor(context, report.period)

  if (context.ai === undefined) {
    const content = report.contentMd.trim() === '' ? buildSkeleton(info.name, report.period, digest) : report.contentMd
    const meta: GenerationMeta = {
      mode: 'manual',
      ai_status: 'failed',
      error: 'AI 服务未配置',
      template_id: DEFAULT_TEMPLATE.id,
      template_version: DEFAULT_TEMPLATE.version,
      attempts: 0
    }
    const updated = storeDraft(context, report, content, meta, digest, undefined, false, settings.timeZone)
    return { report: updated, digest, violations: updated.meta.checks?.violations ?? [] }
  }

  const skill = context.skills.resolve(input.skillId)
  const run = await generateSections(context, info, digest, report.id, skill, input)
  const violations = validateReport(
    run.outputIds.length === 0 ? report.contentMd : assembleReport(info.name, report.period, run.lines),
    digest
  )

  if (run.outputIds.length === 0) {
    const content = report.contentMd.trim() === '' ? buildSkeleton(info.name, report.period, digest) : report.contentMd
    const failure: GenerationMeta = {
      mode: 'manual',
      ai_status: 'failed',
      attempts: run.attempts,
      template_id: DEFAULT_TEMPLATE.id,
      template_version: DEFAULT_TEMPLATE.version
    }
    if (run.firstError !== undefined) failure.error = errorText(run.firstError)
    const updated = storeDraft(context, report, content, failure, digest, undefined, false, settings.timeZone)
    return { report: updated, digest, violations }
  }

  const body = assembleReport(info.name, report.period, run.lines)
  const last = run.last
  if (last === undefined) {
    throw new AppError(AppErrorCode.AiUnavailable, 'AI 生成失败：没有可用的输出', {
      reason: 'ai.unavailable'
    })
  }
  const meta = generationFrom(last, digest)
  meta.attempts = run.attempts
  meta.ai_status = 'ok'
  meta.ai_output_ids = run.outputIds
  meta.ai_output_id = run.outputIds[0]
  meta.skill_id = skill.id
  meta.skill_name = skill.name
  if (run.failed.length > 0) {
    meta.notes = partialNote(run.failed, run.firstError)
    if (meta.error === undefined && run.firstError !== undefined) {
      // The report succeeded overall; keep the cause of the degraded sections
      // on the row so a later "why is this section the skeleton?" is answerable.
      meta.error = errorText(run.firstError)
    }
  }
  const updated = storeDraft(context, report, body, meta, digest, violations, true, settings.timeZone)
  return { report: updated, digest, violations }
}

export function projectInfoFor(context: WeeklyContext): { id: string; name: string; summary: string; goals: string } {
  const row = context.globalDb
    .prepare('SELECT id, name, summary, goals FROM projects WHERE id = ?')
    .get(context.projectId) as { id: string; name: string; summary: string; goals: string } | undefined
  if (row === undefined) {
    throw new AppError(AppErrorCode.NotFound, `项目 ${context.projectId} 不存在`, {
      reason: 'project.not_found'
    })
  }
  return row
}

/** Marks a body that an MCP regeneration must not overwrite without approval. */
export function hasManualTrace(report: WeeklyReport): boolean {
  const generation = report.meta.generation
  if (generation === undefined) return true
  return generation.mode === 'manual' || generation.ai_status === 'skipped'
}

function errorText(error: unknown): string {
  if (error instanceof AppError) return `${error.code}: ${error.message}`
  if (error instanceof Error) return error.message
  return String(error)
}

function generationFrom(
  result: { outputId: string; provenance: { providerId?: string; modelId?: string; promptVersion?: string; generatedAt?: string } },
  digest: ActivityDigest
): GenerationMeta {
  const meta: GenerationMeta = {
    mode: 'ai',
    ai_status: 'ok',
    ai_output_id: result.outputId,
    template_id: DEFAULT_TEMPLATE.id,
    template_version: DEFAULT_TEMPLATE.version,
    attempts: 1,
    digest_hash: digest.digestHash
  }
  if (result.provenance.providerId !== undefined) meta.provider_id = result.provenance.providerId
  if (result.provenance.modelId !== undefined) meta.model_id = result.provenance.modelId
  if (result.provenance.promptVersion !== undefined) meta.prompt_version = result.provenance.promptVersion
  if (result.provenance.generatedAt !== undefined) meta.generated_at = result.provenance.generatedAt
  return meta
}

async function generateSections(
  context: WeeklyContext,
  info: { name: string; summary: string; goals: string },
  digest: ActivityDigest,
  reportId: string,
  skill: WeeklySkill,
  input: GenerateInput
): Promise<SectionRun> {
  const ai = requiredAi(context)
  const run: SectionRun = { lines: new Map(), outputIds: [], failed: [], attempts: 0 }
  const total = DEFAULT_TEMPLATE.sections.length
  for (let index = 0; index < DEFAULT_TEMPLATE.sections.length; index += 1) {
    const section = DEFAULT_TEMPLATE.sections[index]
    if (section === undefined) continue
    input.progress?.(index, total, section.key)
    if (sectionIsEmpty(digest, section.key)) {
      // Nothing to write from: the deterministic line is faster and safer
      // than letting the model invent content.
      run.lines.set(section.key, ['- 本周期无记录'])
      continue
    }
    const request = {
      taskKind: TASK_KIND,
      promptVersion: PROMPT_VERSION,
      messages: sectionMessages(info, digest, section, skill),
      sourceRefs: sectionSourceRefs(digest, section.key),
      entity: { type: 'weekly_report' as const, id: reportId },
      maxOutputTokens: SECTION_MAX_TOKENS,
      onDelta:
        input.delta === undefined
          ? undefined
          : (text: string): void => input.delta?.(section.key, text)
    }
    let lines: string[] | undefined
    for (let attempt = 0; attempt < SECTION_ATTEMPTS; attempt += 1) {
      try {
        const result = await ai.generate(request)
        run.attempts += Math.max(result.attempts, 1)
        const normalized = normalizeSectionOutput(result.contentMd)
        if (normalized === undefined) {
          if (run.firstError === undefined) {
            run.firstError = new AppError(
              AppErrorCode.AiOutputInvalid,
              `${section.title} 分区返回空内容`
            )
          }
          continue
        }
        lines = normalized
        run.last = result
        run.outputIds.push(result.outputId)
        break
      } catch (error) {
        run.attempts += 1
        if (run.firstError === undefined) run.firstError = error
      }
    }
    if (lines === undefined) {
      const fallback = skeletonLines(section.key, digest)
      run.lines.set(section.key, fallback.length === 0 ? ['- 本周期无记录'] : fallback)
      run.failed.push(section.key)
      continue
    }
    run.lines.set(section.key, lines)
  }
  input.progress?.(total, total, '')
  return run
}

export function normalizeSectionOutput(content: string): string[] | undefined {
  let text = content.trim()
  if (text.startsWith('```')) {
    const newline = text.indexOf('\n')
    if (newline >= 0) text = text.slice(newline + 1)
    text = text.replace(/```$/, '').trim()
  }
  const out: string[] = []
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (trimmed === '') continue
    if (trimmed.startsWith('# ') || trimmed.startsWith('## ')) continue
    if (trimmed.startsWith('```')) continue
    out.push(line.replace(/[ \t]+$/, ''))
  }
  return out.length === 0 ? undefined : out
}

export function sectionMessages(
  info: { name: string; summary: string; goals: string },
  digest: ActivityDigest,
  section: TemplateSection,
  skill: WeeklySkill
): Array<{ role: 'system' | 'user'; content: string }> {
  let body = `项目：${info.name}\n`
  if (info.summary !== '') body += `简介：${info.summary}\n`
  if (info.goals !== '') body += `目标：${info.goals}\n`
  body += `\n周期：${digest.period.startDate} – ${periodEndDate(digest.period)}\n`
  body += `本次只写第 ${section.num} 节「${section.title}」。\n`
  const hint = SECTION_HINTS[section.key]
  if (hint !== undefined) body += `本节要求：${hint}\n`
  if (section.key === 'work') {
    const overview = sectionOverview(digest)
    if (Object.keys(overview).length > 0) {
      body += '\n本周各分区概览（仅供总述，不要在正文里重复它们的细节）：\n'
      body += `${JSON.stringify(overview, null, 2)}\n`
    }
    if (digest.housekeeping.length > 0) {
      body += `另有 ${digest.housekeeping.length} 条创建/导入/删除等记录，属于事务性操作，不要写入正文。\n`
    }
  }
  body += '\n本节活动条目（唯一事实来源）：\n'
  body += `${JSON.stringify(sectionPromptDigest(digest, section.key), null, 2)}`
  const system =
    skill.prompt.trim() === ''
      ? PROMPT_SECTION_CONTRACT
      : `${PROMPT_SECTION_CONTRACT}\n\n写作规范（Skill：「${skill.name}」）：\n${skill.prompt.trim()}`
  return [
    { role: 'system', content: system },
    { role: 'user', content: body }
  ]
}

function sectionPromptDigest(digest: ActivityDigest, key: SectionKey): unknown {
  const all = sectionOf(digest, key)?.items ?? []
  const scoped: ActivityDigest = {
    ...digest,
    sections: [{ key, title: sectionTitle(key), items: all.slice(0, PROMPT_SECTION_ITEMS) }],
    blocked: key === 'problem' ? digest.blocked : [],
    planned: key === 'next_week' ? digest.planned : []
  }
  return promptDigest(scoped)
}

function sectionOverview(digest: ActivityDigest): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const section of digest.sections) {
    if (section.key === 'work') continue
    const lines = section.items.slice(0, 3).map((item) => item.summary)
    if (lines.length > 0) out[section.title] = lines
  }
  return out
}

export function sectionSourceRefs(
  digest: ActivityDigest,
  key: SectionKey
): Array<{ entityType: string; entityId: string }> {
  const seen = new Set<string>()
  const out: Array<{ entityType: string; entityId: string }> = []
  const add = (type: string, id: string): void => {
    if (type === '' || id === '' || seen.has(`${type}:${id}`)) return
    seen.add(`${type}:${id}`)
    out.push({ entityType: type, entityId: id })
  }
  for (const item of sectionOf(digest, key)?.items ?? []) add(item.ref.type, item.ref.id)
  if (key === 'problem') for (const task of digest.blocked) add(task.ref.type, task.ref.id)
  if (key === 'next_week') for (const task of digest.planned) add(task.ref.type, task.ref.id)
  return out
}

interface PromptItem {
  type: string
  id: string
  summary: string
  detail?: string
  old?: string
  new?: string
  state: string
  unconfirmed_source: boolean
  context?: string
}

function promptDigest(digest: ActivityDigest): unknown {
  return {
    period_start: digest.period.startDate,
    period_end: periodEndDate(digest.period),
    sections: digest.sections.map((section: DigestSection) => ({
      key: section.key,
      title: section.title,
      items: section.items.map((item): PromptItem => {
        const mapped: PromptItem = {
          type: item.ref.type,
          id: item.ref.id,
          summary: item.summary,
          state: item.state,
          unconfirmed_source: item.unconfirmedSource
        }
        if (item.detail !== '') mapped.detail = item.detail
        if (item.old !== '') mapped.old = item.old
        if (item.new !== '') mapped.new = item.new
        if (item.brief !== '') mapped.context = truncateRunes(item.brief, PROMPT_BRIEF_RUNES)
        return mapped
      })
    })),
    blocked: digest.blocked.map((task) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      column_name: task.columnName,
      reason: task.reason
    })),
    planned: digest.planned.map((task: PlannedTask) => ({
      type: task.ref.type,
      id: task.ref.id,
      title: task.title,
      column_name: task.columnName,
      origin: task.origin,
      deadline: task.deadline ?? '',
      week_plan: task.weekPlan ?? ''
    }))
  }
}

function partialNote(failed: SectionKey[], cause: unknown): string {
  const titles = failed.map((key) => sectionTitle(key)).join('、')
  let note = `以下分区未由 AI 生成，已写入确定性骨架：${titles}`
  if (cause !== undefined) note += `（${truncateRunes(errorText(cause), 160)}）`
  return note
}

/**
 * Writes generated content and metadata in one short transaction, optionally
 * emitting `report.generated` with actor=ai.
 */
function storeDraft(
  context: WeeklyContext,
  report: WeeklyReport,
  content: string,
  generation: GenerationMeta,
  digest: ActivityDigest,
  violations: Violation[] | undefined,
  emit: boolean,
  timeZone: string
): WeeklyReport {
  if (Buffer.byteLength(content, 'utf8') > 512 * 1024) {
    throw new AppError(AppErrorCode.InvalidArgument, '生成内容超过 512 KiB 上限', {
      reason: 'report.content_too_large'
    })
  }
  const now = context.clock.now().toISOString()
  context.handle.inTx(() => {
    const current = requireReport(context.handle.db, report.id, timeZone)
    current.contentMd = content
    current.updatedAt = now
    const merged = violations ?? mergeAcks(validateReport(content, digest), current.meta.checks)
    const coverage = coverageMeta(digest)
    current.meta = {
      ...current.meta,
      generation,
      coverage: coverage.coverage,
      refs: coverage.refs,
      // The draft always carries the checks of the body just written, so the
      // UI cannot claim "all acknowledged" while Confirm re-rejects it.
      checks: buildCheckState(merged, now)
    }
    updateReport(context, current)
    if (emit) {
      context.events.emit({
        type: 'report.generated',
        entityType: 'weekly_report',
        entityId: current.id,
        summary: `生成周报 ${current.period.startDate}–${periodEndDate(current.period)}（AI 初稿，prompt ${PROMPT_VERSION}）`,
        actor: 'ai',
        at: context.clock.now()
      })
    }
  })
  return getReport(context, report.id)
}

function updateReport(context: WeeklyContext, report: WeeklyReport): void {
  updateReportRow(context.handle.db, report)
}

/** Combined "generate this week" entry shared by UI and MCP. */
export async function generateForPeriod(
  context: WeeklyContext,
  input: GeneratePeriodInput
): Promise<GenerateOutcome> {
  writeActor(input.actor)
  const settings = readWeeklySettings(context)
  const period = resolvePeriod(context, input.period, settings)
  requireWritable(context.globalDb, context.projectId)
  const rows = context.handle.db
    .prepare(
      `SELECT id, status FROM weekly_reports
        WHERE period_start = ? AND period_end = ? AND deleted_at IS NULL
        ORDER BY created_at, id`
    )
    .all(period.startDate, periodEndDate(period)) as Array<{ id: string; status: string }>

  let report: WeeklyReport
  let created = false
  if (rows.length === 0) {
    created = true
    report = createReport(context, { period, actor: input.actor })
  } else if (rows.length === 1) {
    const first = rows[0]
    if (first === undefined) {
      throw new AppError(AppErrorCode.Internal, '周报查询结果为空')
    }
    if (first.status === 'confirmed') {
      throw new AppError(AppErrorCode.Conflict, '该周期周报已确认；请先撤回为草稿', {
        reason: 'report.confirmed_read_only'
      })
    }
    report = requireReport(context.handle.db, first.id, settings.timeZone)
  } else {
    throw new AppError(AppErrorCode.Conflict, '该周期存在多份周报，请指定报告 ID', {
      reason: 'report.period_occupied',
      reportIds: rows.map((row) => row.id)
    })
  }

  const digest = digestFor(context, period, settings.maxEvents)
  if (input.includeAI !== true) {
    let current = report
    if (current.contentMd.trim() === '') {
      const info = projectInfoFor(context)
      const meta: GenerationMeta = {
        mode: 'manual',
        ai_status: 'skipped',
        template_id: DEFAULT_TEMPLATE.id,
        template_version: DEFAULT_TEMPLATE.version,
        attempts: 0
      }
      current = storeDraft(
        context,
        report,
        buildSkeleton(info.name, period, digest),
        meta,
        digest,
        undefined,
        false,
        settings.timeZone
      )
    }
    return { report: current, digest, violations: current.meta.checks?.violations ?? [] }
  }
  return generateDraft(context, {
    reportId: report.id,
    replace: input.replace === true || created,
    actor: input.actor,
    skillId: input.skillId,
    progress: input.progress,
    delta: input.delta
  })
}

/** Sections with no facts are skipped without a model call. */
export { sectionIsEmpty }

/** Used by the MCP job progress adapter. */
export function sectionLabel(key: SectionKey): string {
  return sectionTitle(key)
}