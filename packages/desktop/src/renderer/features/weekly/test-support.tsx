/**
 * Test support for the weekly review pages.
 *
 * Every test drives the real IPC client, query cache and renderer against the
 * fake host in `app/testing.tsx`; only the weekly command responses are
 * fixtures. No component is mocked.
 */

import type { ReactNode } from 'react'
import { render, type RenderResult } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import type {
  Period,
  WeeklyDigest,
  WeeklyReportView,
  WeeklyTemplate,
  WeeklyValidationReport,
  WeeklyViolation
} from '@researchos/shared'

import { I18nProvider } from '../../i18n'
import { createQueryClient } from '../../lib/query'
import { makeProject, type FakeBridgeOptions } from '../../app/testing'

export const TEMPLATE: WeeklyTemplate = {
  id: 'default-7',
  version: 1,
  sections: [
    { key: 'work', title: '本周工作内容', num: 1 },
    { key: 'literature', title: '文献调研进展', num: 2 },
    { key: 'theory', title: '理论研究进展', num: 3 },
    { key: 'experiment', title: '实验工作与结果', num: 4 },
    { key: 'idea', title: 'Idea 进展', num: 5 },
    { key: 'problem', title: '遇到的问题', num: 6 },
    { key: 'next_week', title: '下周计划', num: 7 }
  ]
}

export const PERIOD: Period = { start: '2026-11-16', end: '2026-11-22' }

export function reportContent(work = '- 完成数据清洗与第一次基线运行 [[ref:task:task-1]]'): string {
  return [
    '# Weekly Research Report — Alpha（2026-11-16 – 2026-11-22）',
    '',
    '## 1. 本周工作内容',
    work,
    '',
    '## 2. 文献调研进展',
    '- 阅读三篇关于对比学习的论文',
    '',
    '## 3. 理论研究进展',
    '- 本周期无记录',
    '',
    '## 4. 实验工作与结果',
    '- 基线运行完成，见运行记录',
    '',
    '## 5. Idea 进展',
    '- 本周期无记录',
    '',
    '## 6. 遇到的问题',
    '- 显存不足，尚未决定是否更换批次大小',
    '',
    '## 7. 下周计划',
    '- 补做消融实验（来源：周计划）[[ref:task:task-1]]',
    ''
  ].join('\n')
}

export function makeReport(overrides: Partial<WeeklyReportView> = {}): WeeklyReportView {
  return {
    reportId: 'r1',
    periodStart: PERIOD.start,
    periodEnd: PERIOD.end,
    title: 'Weekly 2026-W47',
    contentMd: reportContent(),
    contentHash: 'hash-1',
    status: 'draft',
    meta: {
      generation: {
        mode: 'ai',
        ai_status: 'ok',
        provider_id: 'prov-1',
        model_id: 'gpt-x',
        prompt_version: 'weekly_report/v3',
        template_id: 'default-7',
        template_version: 1,
        attempts: 1,
        skill_name: 'Results first'
      },
      coverage: {
        period_start: PERIOD.start,
        period_end: PERIOD.end,
        timezone: 'UTC',
        event_count: 12,
        counts_by_type: {},
        counts_by_section: {},
        truncated: false,
        digest_hash: 'digest-hash',
        computed_at: '2026-11-18T09:00:00.000Z'
      },
      refs: [{ type: 'task', id: 'task-1' }]
    },
    confirmedAt: null,
    deletedAt: null,
    createdAt: '2026-11-16T08:00:00.000Z',
    updatedAt: '2026-11-18T10:00:00.000Z',
    ...overrides
  }
}

export function makeViolation(overrides: Partial<WeeklyViolation> = {}): WeeklyViolation {
  return {
    rule: 'V1',
    severity: 'error',
    fingerprint: 'fp-planned-1',
    section: 'work',
    ref: { type: 'task', id: 'task-1' },
    message: '计划任务「补做消融实验」出现在「本周工作内容」中',
    ...overrides
  }
}

export function makeValidation(
  violations: readonly WeeklyViolation[] = []
): WeeklyValidationReport {
  return {
    reportId: 'r1',
    ok: violations.every((violation) => violation.ack !== undefined),
    violations: [...violations],
    checkedAt: '2026-11-18T09:30:00.000Z',
    truncated: false
  }
}

export function makeDigest(): WeeklyDigest {
  return {
    periodStart: PERIOD.start,
    periodEnd: PERIOD.end,
    timeZone: 'UTC',
    eventCount: 2,
    countsByType: { 'task.moved': 2 },
    countsBySection: { work: 2 },
    truncated: false,
    warnings: [],
    digestHash: 'digest-hash',
    collectedAt: '2026-11-18T09:00:00.000Z',
    sections: [
      {
        key: 'work',
        title: '本周工作内容',
        items: [
          {
            type: 'task',
            id: 'task-1',
            kind: 'moved',
            summary: '任务移到进行中',
            detail: '',
            old: 'todo',
            new: 'doing',
            state: 'doing',
            actor: 'user',
            firstAt: '2026-11-17T08:00:00.000Z',
            lastAt: '2026-11-17T09:00:00.000Z',
            unconfirmedSource: false,
            broken: false
          }
        ]
      }
    ],
    blocked: [],
    planned: []
  }
}

type BridgeHandlers = NonNullable<FakeBridgeOptions['handlers']>
type BridgeHandler = BridgeHandlers[string]

/** readField reads one string field from an unvalidated IPC request. */
export function readField(request: unknown, key: string): string | undefined {
  if (request === null || typeof request !== 'object') return undefined
  const value = (request as Record<string, unknown>)[key]
  return typeof value === 'string' ? value : undefined
}

/** Default weekly handlers; each test overrides the channels it exercises. */
export function weeklyHandlers(
  overrides: Readonly<Record<string, BridgeHandler>> = {}
): BridgeHandlers {
  const project = makeProject('p1', 'Alpha')
  return {
    'project.get': () => project,
    'project.list': () => [project],
    'app.preferences.get': () => ({
      locale: 'en',
      theme: 'light',
      weekStartsOn: 1,
      timezone: 'UTC'
    }),
    'weekly.template.get': () => TEMPLATE,
    'weekly.skills.list': () => ({
      skills: [
        { id: 'builtin', name: 'Built-in weekly', prompt: '', source: 'builtin', builtin: true },
        { id: 'sk-1', name: 'Results first', prompt: 'Lead with results.', source: 'custom' }
      ],
      default: 'sk-1'
    }),
    'weekly.periods.list': () => [],
    'weekly.reports.list': () => ({
      items: [
        {
          reportId: 'r1',
          title: 'Weekly 2026-W47',
          periodStart: PERIOD.start,
          periodEnd: PERIOD.end,
          status: 'draft' as const,
          excerpt: '本周完成基线运行',
          updatedAt: '2026-11-18T10:00:00.000Z',
          confirmedAt: null,
          deletedAt: null
        }
      ],
      total: 1,
      page: 1,
      pageSize: 20
    }),
    'weekly.reports.get': () => makeReport(),
    'weekly.reports.validate': () => makeValidation([]),
    'weekly.reports.update': (request) =>
      makeReport({
        contentMd: readField(request, 'contentMd') ?? '',
        contentHash: 'hash-2',
        updatedAt: '2026-11-18T11:00:00.000Z'
      }),
    'weekly.digest.get': () => makeDigest(),
    ...overrides
  }
}

export interface RenderOptions {
  readonly route?: string
}

export interface RenderedPage extends RenderResult {
  readonly client: ReturnType<typeof createQueryClient>
}

/** renderPage mounts tree inside i18n, a fresh query cache and a router. */
export function renderPage(node: ReactNode, options: RenderOptions = {}): RenderedPage {
  const client = createQueryClient()
  const result = render(
    <I18nProvider initialPreference="en">
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[options.route ?? '/p/p1/weekly']}>{node}</MemoryRouter>
      </QueryClientProvider>
    </I18nProvider>
  )
  return { ...result, client }
}

/** countCalls is how many times a channel reached the fake host. */
export function countCalls(calls: readonly { channel: string }[], channel: string): number {
  return calls.filter((call) => call.channel === channel).length
}
