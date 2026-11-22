import type {
  IdeaReadiness,
  ReadinessCheckItem,
  ReadinessReport,
  ReadinessRuleId,
  TaskPriorityLevel,
  TaskSuggestion
} from '@researchos/shared'

import { formatTimestamp, type Clock } from '../../clock'
import type { Db } from '../../db/open'
import type { WorkspaceManager } from '../../workspace'
import { projectNotFound } from '../project/errors'

/**
 * Deterministic readiness audit (R-01…R-06) plus its
 * gap-derived task suggestions. The audit is fully offline and never writes:
 * creating a task stays an explicit user action in Kanban.
 *
 * Only ideas in `Validating | Supported | PartiallySupported` are audited;
 * every other status is out of scope.
 */

const AUDIT_STATUSES = ['Validating', 'Supported', 'PartiallySupported'] as const

/** Deterministic boundary markers for R-06; never a model judgement. */
const BOUNDARY_KEYWORDS = [
  '边界',
  '适用',
  '条件',
  '局限',
  '前提',
  '仅在',
  '不适用',
  '失效',
  'boundary',
  'limitation',
  'assum',
  'only when',
  'unless',
  'except'
] as const

export interface ReadinessServiceOptions {
  workspace: WorkspaceManager
  clock?: Clock
}

interface IdeaRow {
  id: string
  title: string
  description: string
  status: string
}

interface ExperimentRef {
  id: string
  title: string
}

export class ReadinessService {
  private readonly workspace: WorkspaceManager
  private readonly clock: Clock

  constructor(options: ReadinessServiceOptions) {
    this.workspace = options.workspace
    this.clock = options.clock ?? options.workspace.clock
  }

  /** Runs the deterministic audit for a project or one idea. */
  check(projectId: string, ideaId?: string): ReadinessReport {
    const db = this.projectDb(projectId)
    const report: ReadinessReport = {
      projectId,
      generatedAt: formatTimestamp(this.clock.now()),
      ideaId: ideaId ?? null,
      checkedIdeas: 0,
      completeIdeas: 0,
      ideas: [],
      items: []
    }
    for (const idea of this.loadIdeas(db, ideaId)) {
      const readiness = this.checkIdea(db, idea)
      report.ideas.push(readiness)
      report.items.push(...readiness.items)
      report.checkedIdeas += 1
      if (readiness.items.length === 0) report.completeIdeas += 1
    }
    return report
  }

  /**
   * Gap-derived suggestions, sorted by rule then entity. They are previews:
   * nothing is written to `tasks` by this method.
   */
  suggestions(projectId: string, ideaId?: string): TaskSuggestion[] {
    const report = this.check(projectId, ideaId)
    return report.items.map((item) => ({
      rule: item.rule,
      title: item.suggestion,
      reason: item.summary,
      priority: item.priority,
      targetEntity: item.targetEntity
    }))
  }

  private projectDb(projectId: string): Db {
    const row = this.workspace.global.db.prepare(`SELECT id FROM projects WHERE id = ?`).get(projectId)
    if (row === undefined) throw projectNotFound(projectId)
    return this.workspace.project(projectId).db
  }

  private loadIdeas(db: Db, ideaId?: string): IdeaRow[] {
    const statuses = AUDIT_STATUSES.map(() => '?').join(',')
    const params: unknown[] = [...AUDIT_STATUSES]
    let sql = `SELECT id, title, description, status FROM ideas
                WHERE deleted_at IS NULL AND status IN (${statuses})`
    if (ideaId !== undefined && ideaId !== '') {
      sql += ' AND id = ?'
      params.push(ideaId)
    }
    sql += ' ORDER BY created_at, id'
    return db.prepare(sql).all(...params) as IdeaRow[]
  }

  private checkIdea(db: Db, idea: IdeaRow): IdeaReadiness {
    const readiness: IdeaReadiness = {
      ideaId: idea.id,
      title: idea.title,
      status: idea.status,
      checked: true,
      passed: 0,
      total: 0,
      items: []
    }
    const { support, oppose } = this.evidenceCounts(db, idea.id)
    const experiments = this.linkedExperiments(db, idea.id)

    let applicable = 1 // R-02 always applies
    if (idea.status === 'Supported' || idea.status === 'PartiallySupported') applicable += 1 // R-01
    applicable += 2 * experiments.length // R-03 + R-04 per experiment
    if (idea.status === 'Supported') applicable += 1 // R-06

    if ((idea.status === 'Supported' || idea.status === 'PartiallySupported') && support === 0) {
      readiness.items.push(
        item(
          'R-01',
          idea,
          null,
          '缺少已确认的支撑证据',
          `为「${idea.title}」补充并确认支撑证据`,
          'high',
          { type: 'idea', id: idea.id }
        )
      )
    }
    if (experiments.length === 0) {
      readiness.items.push(
        item(
          'R-02',
          idea,
          null,
          '未通过 validates 关联任何验证实验',
          `为「${idea.title}」设计并关联验证实验`,
          'high',
          { type: 'idea', id: idea.id }
        )
      )
    }
    for (const experiment of experiments) {
      const state = this.experimentState(db, experiment.id)
      if (state.latest === null) {
        readiness.items.push(
          item(
            'R-03',
            idea,
            experiment,
            '实验尚无已确认的 Results 快照',
            `运行并确认实验「${experiment.title}」的结果分析快照`,
            'medium',
            { type: 'experiment', id: experiment.id }
          )
        )
      } else if (state.latest.dirty || state.latest.commit.trim() === '') {
        readiness.items.push(
          item(
            'R-05',
            idea,
            experiment,
            '最新确认结果基于脏工作区或无 Commit SHA',
            `提交实验「${experiment.title}」工作区代码并复现干净版本结果`,
            'high',
            { type: 'experiment', id: experiment.id }
          )
        )
      }
      if (state.runCount <= 1) {
        readiness.items.push(
          item(
            'R-04',
            idea,
            experiment,
            '实验仅有 ≤1 个有效 Run，缺少 Baseline 或对比组',
            `为实验「${experiment.title}」补充 Baseline 或多参数对比 Run`,
            'medium',
            { type: 'experiment', id: experiment.id }
          )
        )
      }
    }
    if (idea.status === 'Supported' && oppose > 0 && this.opposeWithoutBoundary(db, idea.id)) {
      readiness.items.push(
        item(
          'R-06',
          idea,
          null,
          '存在已确认的反对证据且未说明适用边界',
          `复核「${idea.title}」的反例证据并明确假设适用边界`,
          'medium',
          { type: 'idea', id: idea.id }
        )
      )
    }

    readiness.total = applicable
    readiness.passed = Math.max(0, applicable - readiness.items.length)
    return readiness
  }

  private evidenceCounts(db: Db, ideaId: string): { support: number; oppose: number } {
    const rows = db
      .prepare(
        `SELECT direction, count(*) AS n FROM evidence
          WHERE idea_id = ? AND confirmed = 1 AND deleted_at IS NULL GROUP BY direction`
      )
      .all(ideaId) as Array<{ direction: string; n: number }>
    let support = 0
    let oppose = 0
    for (const row of rows) {
      if (row.direction === 'support') support = Number(row.n)
      if (row.direction === 'oppose') oppose = Number(row.n)
    }
    return { support, oppose }
  }

  private linkedExperiments(db: Db, ideaId: string): ExperimentRef[] {
    return db
      .prepare(
        `SELECT e.id, e.title FROM experiments e
           JOIN entity_relations r ON r.src_type = 'experiment' AND r.src_id = e.id
          WHERE r.relation = 'validates' AND r.dst_type = 'idea' AND r.dst_id = ?
            AND e.deleted_at IS NULL
          ORDER BY e.created_at, e.id`
      )
      .all(ideaId) as ExperimentRef[]
  }

  private experimentState(
    db: Db,
    experimentId: string
  ): { runCount: number; latest: { commit: string; dirty: boolean } | null } {
    const runs = db
      .prepare(`SELECT count(*) AS n FROM runs WHERE experiment_id = ? AND deleted_at IS NULL`)
      .get(experimentId) as { n: number }
    const snapshot = db
      .prepare(
        `SELECT coalesce(s.git_info, '{}') AS git_info FROM snapshots s
           JOIN ai_outputs o ON o.id = s.ai_output_id
          WHERE s.target_type = 'experiment' AND s.target_id = ?
            AND o.status = 'confirmed'
            AND CASE WHEN json_valid(s.ai_analysis)
                     THEN json_extract(s.ai_analysis, '$.kind') END = 'results'
          ORDER BY s.created_at DESC, s.id DESC LIMIT 1`
      )
      .get(experimentId) as { git_info: string } | undefined
    if (snapshot === undefined) return { runCount: Number(runs.n), latest: null }
    try {
      const parsed = JSON.parse(snapshot.git_info) as { commit?: unknown; dirty?: unknown }
      return {
        runCount: Number(runs.n),
        latest: {
          commit: typeof parsed.commit === 'string' ? parsed.commit : '',
          dirty: parsed.dirty === true
        }
      }
    } catch {
      return { runCount: Number(runs.n), latest: { commit: '', dirty: false } }
    }
  }

  /** True when some confirmed oppose evidence omits every boundary marker. */
  private opposeWithoutBoundary(db: Db, ideaId: string): boolean {
    const rows = db
      .prepare(
        `SELECT content FROM evidence
          WHERE idea_id = ? AND direction = 'oppose' AND confirmed = 1 AND deleted_at IS NULL`
      )
      .all(ideaId) as Array<{ content: string }>
    for (const row of rows) {
      const content = row.content.toLowerCase()
      const qualified = BOUNDARY_KEYWORDS.some((keyword) => content.includes(keyword.toLowerCase()))
      if (!qualified) return true
    }
    return false
  }
}

function item(
  rule: ReadinessRuleId,
  idea: IdeaRow,
  experiment: ExperimentRef | null,
  summary: string,
  suggestion: string,
  priority: TaskPriorityLevel,
  targetEntity: ReadinessCheckItem['targetEntity']
): ReadinessCheckItem {
  return {
    rule,
    ideaId: idea.id,
    ideaTitle: idea.title,
    experimentId: experiment?.id ?? null,
    experimentTitle: experiment?.title ?? '',
    summary,
    suggestion,
    priority,
    targetEntity
  }
}