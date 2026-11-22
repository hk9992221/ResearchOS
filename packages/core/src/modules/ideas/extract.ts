/**
 * The `evidence.normalize` skill contract: prompt, strict JSON
 * parsing and per-item validation. Persistence and de-duplication live in the
 * service so the AI response handling stays pure and testable.
 */
import type { EvidenceDirection, EvidenceExtractionSourceType } from '@researchos/shared'

import { AppErrorCode } from '../../errors'
import { IDEA_LIMITS, runeCount } from './limits'
import { IDEA_ERROR, ideaError } from './errors'
import { hashContent, truncateParagraphs } from './text'


export const SKILL_NAME = 'evidence.normalize'
export const PROMPT_VERSION = 'evidence.normalize/1.0'

export interface SkillSource {
  index: number
  type: EvidenceExtractionSourceType
  id: string
  title: string
  text: string
  version: string
  truncated: boolean
}

export interface AIItem {
  direction: string
  content: string
  sourceIndex: number
  rationale: string
}

export interface AICandidate {
  direction: EvidenceDirection
  content: string
  sourceType: EvidenceExtractionSourceType
  sourceId: string
  rationale: string
  hash: string
}

export interface SchemaSkip {
  type: EvidenceExtractionSourceType | ''
  id: string
  reason: string
  contentHash: string
}

interface RawAIOutput {
  items?: unknown
}

function invalidOutput(message: string): never {
  throw ideaError(IDEA_ERROR.aiOutputInvalid, AppErrorCode.AiOutputInvalid, message)
}

/** Parses the strict JSON body, tolerating a single Markdown code fence. */
export function parseAIOutput(raw: string): AIItem[] {
  let text = raw.trim()
  if (text.startsWith('```')) {
    const newline = text.indexOf('\n')
    if (newline >= 0) text = text.slice(newline + 1)
    text = text.trimEnd()
    if (text.endsWith('```')) text = text.slice(0, -3)
  }
  const start = text.indexOf('{')
  const end = text.lastIndexOf('}')
  if (start < 0 || end < start) invalidOutput('model output is not valid JSON: no JSON object found')
  text = text.slice(start, end + 1)

  let parsed: RawAIOutput
  try {
    parsed = JSON.parse(text) as RawAIOutput
  } catch (error) {
    invalidOutput(`model output is not valid JSON: ${error instanceof Error ? error.message : String(error)}`)
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    invalidOutput('model output is not a JSON object')
  }
  const unknownKeys = Object.keys(parsed).filter((key) => key !== 'items')
  if (unknownKeys.length > 0) {
    invalidOutput(`model output has unknown fields: ${unknownKeys.join(', ')}`)
  }
  if (!Array.isArray(parsed.items)) invalidOutput('model output has no items array')

  return parsed.items.map((item, position) => {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      invalidOutput(`items[${position}] is not an object`)
    }
    const record = item as Record<string, unknown>
    const allowed = ['direction', 'content', 'source_index', 'rationale']
    const extra = Object.keys(record).filter((key) => !allowed.includes(key))
    if (extra.length > 0) invalidOutput(`items[${position}] has unknown fields: ${extra.join(', ')}`)
    return {
      direction: typeof record.direction === 'string' ? record.direction : '',
      content: typeof record.content === 'string' ? record.content : '',
      sourceIndex: typeof record.source_index === 'number' ? record.source_index : Number.NaN,
      rationale: typeof record.rationale === 'string' ? record.rationale : ''
    }
  })
}

function validDirection(value: string): value is EvidenceDirection {
  return value === 'support' || value === 'oppose'
}

/** Applies the per-item schema; invalid items become `skipped` entries. */
export function validateAIItems(
  items: readonly AIItem[],
  sources: readonly SkillSource[],
  maxDrafts: number
): { candidates: AICandidate[]; skipped: SchemaSkip[]; schemaValid: number } {
  const candidates: AICandidate[] = []
  const skipped: SchemaSkip[] = []
  let schemaValid = 0

  for (const item of items) {
    if (candidates.length >= maxDrafts) {
      skipped.push({ type: '', id: '', reason: 'schema', contentHash: '' })
      continue
    }
    const direction = item.direction.trim()
    const content = item.content.trim()
    const inRange = Number.isInteger(item.sourceIndex) && item.sourceIndex >= 1 && item.sourceIndex <= sources.length
    const contentRunes = runeCount(content)
    if (
      !validDirection(direction) ||
      contentRunes < IDEA_LIMITS.aiContentMinRunes ||
      contentRunes > IDEA_LIMITS.aiContentMaxRunes ||
      !inRange
    ) {
      const source = inRange ? sources[item.sourceIndex - 1] : undefined
      skipped.push({
        type: source?.type ?? '',
        id: source?.id ?? '',
        reason: 'schema',
        contentHash: content === '' ? '' : hashContent(content)
      })
      continue
    }
    schemaValid += 1
    const source = sources[item.sourceIndex - 1] as SkillSource
    const rationaleRunes = runeCount(item.rationale.trim())
    const rationale =
      rationaleRunes > IDEA_LIMITS.aiRationaleMaxRunes
        ? [...item.rationale.trim()].slice(0, IDEA_LIMITS.aiRationaleMaxRunes).join('')
        : item.rationale.trim()
    candidates.push({
      direction,
      content,
      sourceType: source.type,
      sourceId: source.id,
      rationale,
      hash: hashContent(content)
    })
  }
  return { candidates, skipped, schemaValid }
}

export interface IdeaPromptContext {
  title: string
  description: string
}

/** Builds the ideas request, including the JSON schema. */
export interface SkillRequest {
  system: string
  prompt: string
  jsonSchema: Record<string, unknown>
  maxOutputTokens: number
}

export function buildSkillRequest(
  idea: IdeaPromptContext,
  sources: readonly SkillSource[],
  instruction: string,
  maxDrafts: number
): SkillRequest {
  const parts: string[] = []
  parts.push('研究假设（仅作上下文，不得判断其成立与否）：')
  parts.push(`标题：${idea.title}`)
  parts.push('描述：')
  parts.push(idea.description)
  parts.push('')
  if (instruction.trim() !== '') {
    parts.push(`筛选侧重：${instruction.trim()}`)
    parts.push('')
  }
  parts.push('来源材料：')
  for (const source of sources) {
    parts.push('')
    parts.push(
      `[${source.index}] type=${source.type} title=${source.title}${source.truncated ? '（已截断）' : ''}`
    )
    parts.push(source.text)
  }
  parts.push('')
  parts.push(`请输出不超过 ${maxDrafts} 条 Evidence 草稿。严格返回 JSON：`)
  parts.push('{"items":[{"direction":"support|oppose","content":"陈述句","source_index":1,"rationale":"依据来源的哪一部分"}]}')
  parts.push('content 为 20..1000 字符的陈述句；每条必须绑定一个来源序号；rationale 不超过 300 字符；')
  parts.push('不得给出假设成立/不成立的结论，不得引入来源之外的事实。')

  return {
    system: '你是科研证据规范化助手，只从给定来源文本中抽取可支持或反对研究假设的事实性陈述。',
    prompt: parts.join('\n'),
    jsonSchema: {
      type: 'object',
      properties: {
        items: {
          type: 'array',
          maxItems: maxDrafts,
          items: {
            type: 'object',
            required: ['direction', 'content', 'source_index', 'rationale'],
            properties: {
              direction: { enum: ['support', 'oppose'] },
              content: { type: 'string', minLength: 20, maxLength: 1000 },
              source_index: { type: 'integer', minimum: 1 },
              rationale: { type: 'string', maxLength: 300 }
            }
          }
        }
      },
      required: ['items']
    },
    maxOutputTokens: 2048
  }
}

/** Applies the 4000/24000 code-point budgets at paragraph boundaries. */
export function budgetSources(
  inputs: readonly Omit<SkillSource, 'index' | 'truncated'>[]
): SkillSource[] {
  const budgeted: SkillSource[] = []
  let remaining = IDEA_LIMITS.totalChars
  inputs.forEach((source, position) => {
    const budget = Math.max(0, Math.min(IDEA_LIMITS.sourceChars, remaining))
    const cut = truncateParagraphs(source.text, budget)
    remaining -= runeCount(cut.text)
    budgeted.push({ ...source, index: position + 1, text: cut.text, truncated: cut.truncated })
  })
  return budgeted
}