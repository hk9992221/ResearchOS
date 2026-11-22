import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import {
  BookOpen,
  Check,
  FlaskConical,
  Lightbulb,
  ListTodo,
  Pencil,
  Plus,
  RefreshCw,
  type LucideIcon
} from 'lucide-react'

import type {
  OverviewSnapshot,
  OverviewTaskCard,
  OverviewWidgetKind,
  OverviewWidgetState,
  ProjectActivityItem
} from '@researchos/shared'

import {
  Badge,
  Button,
  Card,
  IconButton,
  Segmented,
  Skeleton,
  TextInput,
  type Tone
} from '../../design/components'
import { iconFor } from '../../app/icons'
import { useT, type Translations } from '../../i18n'
import { routeHref, type RouteKind } from '../../routes'
import { queryKeys } from '../../lib/query'
import { overviewApi } from './api'
import {
  ideaStatusLabel,
  ideaStatusTone,
  paperMeta,
  runStatusLabel,
  taskPriorityLabel,
  truncateRunes
} from './format'
import { activityHref, activityIcon, relativeTime } from '../home/format'
import 'katex/dist/katex.min.css'
import styles from './overview.module.css'

const FOCUS_RUNES = 1200

/**
 * Display caps mirror the core overview module's widget limits
 * (`packages/core/src/modules/overview/index.ts`); a KPI tile that sits at
 * its cap reads `N+` so it never claims an exact total it cannot see.
 */
const WIDGET_CAP = 5
const TASKS_CAP = 8
const ACTIVITY_LIMIT = 6

const PENDING_TIP: Readonly<Record<string, string>> = {
  recent_experiments: 'shell.overview.pending_tip_experiments',
  my_tasks: 'shell.overview.pending_tip_tasks',
  blocked_tasks: 'shell.overview.pending_tip_blocked',
  weekly_progress: 'shell.overview.pending_tip_weekly'
}

export interface OverviewBoardProps {
  readonly projectId: string
  readonly snapshot: OverviewSnapshot
  readonly writable: boolean
  readonly refreshing: OverviewWidgetKind | null
  readonly widgetErrors: Partial<Record<OverviewWidgetKind, string>>
  readonly quickNoteBusy: boolean
  readonly onRefresh: (kind: OverviewWidgetKind) => void
  readonly onCompleteTask: (task: OverviewTaskCard) => void
  readonly onQuickNote: (title: string) => void
  readonly onSaveFocus: (markdown: string) => Promise<boolean>
}

interface Navigate {
  (target: RouteKind, params?: { entityId?: string; query?: Record<string, string> }): void
}

/** Everything the dashboard cards share, assembled once by the board. */
interface BoardCtx {
  readonly projectId: string
  readonly snapshot: OverviewSnapshot
  readonly writable: boolean
  readonly refreshing: OverviewWidgetKind | null
  readonly widgetErrors: Partial<Record<OverviewWidgetKind, string>>
  readonly quickNoteBusy: boolean
  readonly onRefresh: (kind: OverviewWidgetKind) => void
  readonly onCompleteTask: (task: OverviewTaskCard) => void
  readonly onQuickNote: (title: string) => void
  readonly onSaveFocus: (markdown: string) => Promise<boolean>
  readonly go: Navigate
}

/* ------------------------------------------------------------ selectors */

function widgetState(
  snapshot: OverviewSnapshot,
  kind: OverviewWidgetKind
): OverviewWidgetState | undefined {
  return snapshot.widgets.find((widget) => widget.kind === kind)
}

function widgetVisible(snapshot: OverviewSnapshot, kind: OverviewWidgetKind): boolean {
  const state = widgetState(snapshot, kind)
  return state !== undefined && state.available
}

function cappedCount(n: number, cap: number): string {
  return n >= cap ? `${cap}+` : `${n}`
}

/** The deadline falls in the Monday-to-Sunday week containing `now`. */
export function inCurrentWeek(deadline: string | null, now: Date = new Date()): boolean {
  if (deadline === null) return false
  const day = deadline.slice(0, 10)
  if (day.length !== 10) return false
  const monday = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  monday.setDate(monday.getDate() - ((monday.getDay() + 6) % 7))
  const sunday = new Date(monday)
  sunday.setDate(monday.getDate() + 6)
  const key = (date: Date): string =>
    `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(
      date.getDate()
    ).padStart(2, '0')}`
  return day >= key(monday) && day <= key(sunday)
}

function runStatusTone(status: string): Tone | 'muted' {
  switch (status.trim().toLowerCase()) {
    case 'finished':
      return 'success'
    case 'running':
      return 'info'
    case 'failed':
      return 'danger'
    default:
      return 'muted'
  }
}

/* ------------------------------------------------------------ card chrome */

function WidgetBadge({
  kind,
  state
}: {
  readonly kind: OverviewWidgetKind
  readonly state: OverviewWidgetState | undefined
}): JSX.Element | null {
  const t = useT()
  if (state === undefined || !state.pendingConfirmation) return null
  return (
    <span title={t(PENDING_TIP[kind] ?? 'shell.overview.pending_tip_generic')}>
      <Badge tone="neutral">{t('shell.overview.badge_pending')}</Badge>
    </span>
  )
}

function WidgetRefresh({
  kind,
  state,
  refreshing,
  onRefresh
}: {
  readonly kind: OverviewWidgetKind
  readonly state: OverviewWidgetState | undefined
  readonly refreshing: OverviewWidgetKind | null
  readonly onRefresh: (kind: OverviewWidgetKind) => void
}): JSX.Element | null {
  const t = useT()
  if (state === undefined || !state.degraded) return null
  return (
    <IconButton
      icon={RefreshCw}
      label={t('shell.overview.btn_refresh_widget')}
      size="sm"
      disabled={refreshing === kind}
      onClick={() => onRefresh(kind)}
    />
  )
}

function WidgetError({
  kind,
  widgetErrors
}: {
  readonly kind: OverviewWidgetKind
  readonly widgetErrors: Partial<Record<OverviewWidgetKind, string>>
}): JSX.Element | null {
  const message = widgetErrors[kind]
  if (message === undefined) return null
  return (
    <p className={styles.widgetError} role="alert">
      {message}
    </p>
  )
}

function ViewAll({ onClick }: { readonly onClick: () => void }): JSX.Element {
  const t = useT()
  return (
    <Button variant="ghost" size="sm" onClick={onClick}>
      {t('shell.overview.btn_view_all')}
    </Button>
  )
}

/* ------------------------------------------------------------- the board */

/**
 * KPI strip, quick actions, then the asymmetric 2fr/1fr columns.
 * The eight §3.2 widgets keep their payloads, pending badges and per-card
 * degraded refresh; only their arrangement and chrome change.
 */
export function OverviewBoard(props: OverviewBoardProps): JSX.Element {
  const navigate = useNavigate()
  const { projectId, snapshot } = props

  const go: Navigate = (target, params) => {
    const href = routeHref(target, { projectId, entityId: params?.entityId })
    if (href === undefined) return
    const search =
      params?.query === undefined ? '' : new URLSearchParams(params.query).toString()
    navigate(search === '' ? href : `${href}?${search}`)
  }

  const ctx: BoardCtx = {
    ...props,
    writable: props.writable && !snapshot.project.archived,
    go
  }

  return (
    <div className={styles.board}>
      <KpiStrip ctx={ctx} />
      <QuickActions ctx={ctx} />
      <div className={styles.columns}>
        <div className={styles.mainColumn}>
          <FocusCard ctx={ctx} />
          <TasksCard ctx={ctx} />
          <div className={styles.subGrid}>
            <ExperimentsCard ctx={ctx} />
            <NotesCard ctx={ctx} />
          </div>
        </div>
        <div className={styles.sideColumn}>
          <IdeasCard ctx={ctx} />
          <LiteratureCard ctx={ctx} />
          <ActivityCard ctx={ctx} />
        </div>
      </div>
    </div>
  )
}

/* -------------------------------------------------------------- KPI strip */

interface KpiTile {
  readonly key: string
  readonly icon: LucideIcon
  readonly label: string
  readonly value: string
  readonly meta?: JSX.Element
  readonly onClick: () => void
}

function KpiStrip({ ctx }: { readonly ctx: BoardCtx }): JSX.Element {
  const t = useT()
  const { snapshot, go } = ctx
  const weekCount = snapshot.myTasks.items.filter((task) => inCurrentWeek(task.deadline)).length

  const tiles: KpiTile[] = [
    {
      key: 'tasks',
      icon: ListTodo,
      label: t('shell.overview.kpi_tasks'),
      value: cappedCount(snapshot.myTasks.items.length, TASKS_CAP),
      meta:
        weekCount > 0 ? (
          <span className={styles.kpiBadge}>{t('shell.overview.kpi_week', { n: weekCount })}</span>
        ) : undefined,
      onClick: () => go('kanban')
    },
    {
      key: 'ideas',
      icon: Lightbulb,
      label: t('shell.overview.kpi_ideas'),
      value: cappedCount(snapshot.activeIdeas.length, WIDGET_CAP),
      onClick: () => go('ideas')
    },
    {
      key: 'experiments',
      icon: FlaskConical,
      label: t('shell.overview.kpi_experiments'),
      value: cappedCount(snapshot.recentExperiments.length, WIDGET_CAP),
      meta:
        snapshot.recentExperiments.length > 0 ? (
          <span className={styles.statusDots} aria-hidden="true">
            {snapshot.recentExperiments.map((experiment) => (
              <span
                key={experiment.id}
                className={styles.statusDot}
                data-tone={runStatusTone(experiment.lastRunStatus)}
                title={runStatusLabel(t, experiment.lastRunStatus)}
              />
            ))}
          </span>
        ) : undefined,
      onClick: () => go('experiments')
    },
    {
      key: 'literature',
      icon: BookOpen,
      label: t('shell.overview.kpi_literature_notes'),
      value: `${cappedCount(snapshot.literature.length, WIDGET_CAP)} · ${cappedCount(
        snapshot.recentNotes.length,
        WIDGET_CAP
      )}`,
      onClick: () => go('literature')
    }
  ]

  return (
    <div className={styles.kpiStrip}>
      {tiles.map((tile) => {
        const Icon = tile.icon
        return (
          <button
            key={tile.key}
            type="button"
            className={styles.kpiTile}
            onClick={tile.onClick}
          >
            <span className={styles.kpiIcon}>
              <Icon aria-hidden="true" strokeWidth={2} />
            </span>
            <span className={styles.kpiText}>
              <span className={styles.kpiValue}>{tile.value}</span>
              <span className={styles.kpiLabel}>{tile.label}</span>
            </span>
            {tile.meta}
          </button>
        )
      })}
    </div>
  )
}

/* ---------------------------------------------------------- quick actions */

function QuickActions({ ctx }: { readonly ctx: BoardCtx }): JSX.Element {
  const t = useT()
  const [noteOpen, setNoteOpen] = useState(false)
  const [title, setTitle] = useState('')

  const submitNote = (): void => {
    const trimmed = title.trim()
    if (trimmed === '' || ctx.quickNoteBusy) return
    ctx.onQuickNote(trimmed)
    setTitle('')
    setNoteOpen(false)
  }

  return (
    <div className={styles.quickBar} role="toolbar" aria-label={t('shell.overview.quick_bar')}>
      <Button variant="secondary" size="sm" icon={Plus} onClick={() => ctx.go('kanban')}>
        {t('shell.overview.tasks_create')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        icon={Plus}
        disabled={!ctx.writable}
        onClick={() => setNoteOpen((open) => !open)}
      >
        {t('shell.overview.qa_note')}
      </Button>
      <Button variant="secondary" size="sm" icon={Plus} onClick={() => ctx.go('ideas')}>
        {t('shell.overview.qa_idea')}
      </Button>
      <Button variant="secondary" size="sm" icon={Plus} onClick={() => ctx.go('literature')}>
        {t('shell.overview.literature_add')}
      </Button>
      {noteOpen && (
        <form
          className={styles.quickNoteForm}
          onSubmit={(event) => {
            event.preventDefault()
            submitNote()
          }}
        >
          <TextInput
            label={t('shell.overview.qa_note')}
            hideLabel
            placeholder={t('shell.overview.quick_note_placeholder')}
            value={title}
            onChange={setTitle}
            disabled={ctx.quickNoteBusy}
            autoFocus
          />
          <Button
            variant="primary"
            size="sm"
            type="submit"
            disabled={ctx.quickNoteBusy || title.trim() === ''}
          >
            {t('shell.overview.btn_add_note')}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={ctx.quickNoteBusy}
            onClick={() => {
              setNoteOpen(false)
              setTitle('')
            }}
          >
            {t('common.cancel')}
          </Button>
        </form>
      )}
    </div>
  )
}

/* ----------------------------------------------------------- focus card */

function FocusCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot } = ctx
  const [editing, setEditing] = useState(false)
  if (!widgetVisible(snapshot, 'current_focus')) return null

  const focus = snapshot.currentFocus
  const editable = ctx.writable && focus.focusEditable
  const text = focus.focusMd.trim() || focus.goalsExcerpt.trim()

  return (
    <Card
      title={t('shell.overview.card_focus')}
      actions={
        editable && !editing ? (
          <IconButton
            icon={Pencil}
            label={t('shell.overview.focus_edit')}
            size="sm"
            onClick={() => setEditing(true)}
          />
        ) : undefined
      }
    >
      {editing ? (
        <FocusEditor
          focus={focus.focusMd}
          onSave={async (markdown) => {
            const saved = await ctx.onSaveFocus(markdown)
            if (saved) setEditing(false)
          }}
          onCancel={() => setEditing(false)}
        />
      ) : text === '' ? (
        editable ? (
          <button
            type="button"
            className={styles.focusCta}
            onClick={() => setEditing(true)}
          >
            {t('shell.overview.focus_cta')}
          </button>
        ) : (
          <p className={styles.metaText}>{t('shell.overview.focus_empty')}</p>
        )
      ) : (
        <div className={styles.markdown}>
          <ReactMarkdown remarkPlugins={[remarkGfm, remarkMath]} rehypePlugins={[rehypeKatex]}>
            {truncateRunes(text, FOCUS_RUNES)}
          </ReactMarkdown>
        </div>
      )}
    </Card>
  )
}

interface FocusEditorProps {
  readonly focus: string
  readonly onSave: (markdown: string) => Promise<void>
  readonly onCancel: () => void
}

function FocusEditor({ focus, onSave, onCancel }: FocusEditorProps): JSX.Element {
  const t = useT()
  const [draft, setDraft] = useState(focus)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setDraft(focus)
  }, [focus])

  return (
    <div className={styles.focusEdit}>
      <label className={styles.visuallyHidden} htmlFor="overview-focus">
        {t('shell.overview.focus_placeholder')}
      </label>
      <textarea
        id="overview-focus"
        className={styles.textarea}
        rows={8}
        value={draft}
        disabled={busy}
        onChange={(event) => setDraft(event.target.value)}
      />
      <div className={styles.focusButtons}>
        <Button variant="secondary" size="sm" onClick={onCancel} disabled={busy}>
          {t('common.cancel')}
        </Button>
        <Button
          variant="primary"
          size="sm"
          disabled={busy}
          onClick={() => {
            setBusy(true)
            void onSave(draft).then(() => setBusy(false))
          }}
        >
          {t('shell.overview.focus_save')}
        </Button>
      </div>
    </div>
  )
}

/* ------------------------------------------------------------ tasks card */

type TaskFilter = 'all' | 'week'

function TasksCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot, go } = ctx
  const [filter, setFilter] = useState<TaskFilter>('all')

  const myVisible = widgetVisible(snapshot, 'my_tasks')
  const blockedVisible = widgetVisible(snapshot, 'blocked_tasks')
  if (!myVisible && !blockedVisible) return null

  const items = myVisible ? snapshot.myTasks.items : []
  const shown =
    filter === 'week' ? items.filter((task) => inCurrentWeek(task.deadline)) : items

  return (
    <Card
      title={t('shell.overview.card_tasks')}
      actions={
        <>
          {myVisible && (
            <Segmented
              label={t('shell.overview.card_tasks')}
              items={[
                { id: 'all', label: t('shell.overview.filter_all') },
                { id: 'week', label: t('shell.overview.filter_week') }
              ]}
              value={filter}
              onChange={(id) => setFilter(id as TaskFilter)}
            />
          )}
          <WidgetBadge kind="my_tasks" state={widgetState(snapshot, 'my_tasks')} />
          <WidgetRefresh
            kind="my_tasks"
            state={widgetState(snapshot, 'my_tasks')}
            refreshing={ctx.refreshing}
            onRefresh={ctx.onRefresh}
          />
          <ViewAll onClick={() => go('kanban')} />
        </>
      }
    >
      <WidgetError kind="my_tasks" widgetErrors={ctx.widgetErrors} />
      {myVisible && snapshot.myTasks.items.length === 0 ? (
        <CardEmpty
          text={t('shell.overview.tasks_empty')}
          actionLabel={t('shell.overview.tasks_create')}
          onAction={() => go('kanban')}
        />
      ) : shown.length === 0 ? (
        <p className={styles.metaText}>{t('shell.overview.tasks_empty')}</p>
      ) : (
        <div className={styles.list}>
          {shown.map((task) => (
            <TaskRow
              key={task.id}
              task={task}
              writable={ctx.writable}
              onOpen={() => go('task', { entityId: task.id })}
              onComplete={() => ctx.onCompleteTask(task)}
            />
          ))}
        </div>
      )}
      {blockedVisible && <BlockedSection ctx={ctx} />}
    </Card>
  )
}

function BlockedSection({ ctx }: { readonly ctx: BoardCtx }): JSX.Element {
  const t = useT()
  const { snapshot, go } = ctx
  const items = snapshot.blockedTasks.items
  return (
    <div className={styles.blockedSection}>
      <div className={styles.blockedHead}>
        <span className={styles.metaText}>{t('shell.overview.card_blocked')}</span>
        <WidgetBadge kind="blocked_tasks" state={widgetState(snapshot, 'blocked_tasks')} />
        <WidgetRefresh
          kind="blocked_tasks"
          state={widgetState(snapshot, 'blocked_tasks')}
          refreshing={ctx.refreshing}
          onRefresh={ctx.onRefresh}
        />
      </div>
      {items.length === 0 ? (
        <p className={styles.metaText}>{t('shell.overview.blocked_empty')}</p>
      ) : (
        <div className={styles.list}>
          {items.map((task) => (
            <Row
              key={task.id}
              title={task.title}
              meta={<Badge tone="warning">{t('shell.overview.task_blocked')}</Badge>}
              onClick={() => go('task', { entityId: task.id })}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function TaskRow({
  task,
  writable,
  onOpen,
  onComplete
}: {
  readonly task: OverviewTaskCard
  readonly writable: boolean
  readonly onOpen: () => void
  readonly onComplete: () => void
}): JSX.Element {
  const t = useT()
  return (
    <div className={styles.taskRow}>
      {writable ? (
        <button
          type="button"
          className={styles.taskCheck}
          aria-label={t('shell.overview.btn_complete_task')}
          onClick={onComplete}
        >
          <Check aria-hidden="true" strokeWidth={2} />
        </button>
      ) : (
        <span className={`${styles.taskCheck} ${styles.taskCheckStatic}`} aria-hidden="true" />
      )}
      <button type="button" className={styles.row} onClick={onOpen}>
        <span
          className={styles.priorityDot}
          data-priority={String(task.priority)}
          title={taskPriorityLabel(t, task.priority)}
        />
        <span className={styles.rowTitle}>
          {task.title.trim() === '' ? t('shell.overview.untitled') : task.title}
        </span>
        <span className={styles.rowMeta}>
          {task.blocked && <Badge tone="warning">{t('shell.overview.task_blocked')}</Badge>}
          {inCurrentWeek(task.deadline) && (
            <span className={styles.weekTag}>{t('shell.overview.tag_week')}</span>
          )}
          {task.deadline !== null && (
            <span className={styles.metaText}>
              {t('shell.overview.task_deadline', { date: task.deadline.slice(0, 10) })}
            </span>
          )}
        </span>
      </button>
    </div>
  )
}

/* -------------------------------------------------- experiments and notes */

function ExperimentsCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot, go } = ctx
  if (!widgetVisible(snapshot, 'recent_experiments')) return null
  return (
    <Card
      title={t('shell.overview.card_experiments')}
      actions={
        <>
          <WidgetBadge
            kind="recent_experiments"
            state={widgetState(snapshot, 'recent_experiments')}
          />
          <WidgetRefresh
            kind="recent_experiments"
            state={widgetState(snapshot, 'recent_experiments')}
            refreshing={ctx.refreshing}
            onRefresh={ctx.onRefresh}
          />
          <ViewAll onClick={() => go('experiments')} />
        </>
      }
    >
      <WidgetError kind="recent_experiments" widgetErrors={ctx.widgetErrors} />
      {snapshot.recentExperiments.length === 0 ? (
        <CardEmpty
          text={t('shell.overview.experiments_empty')}
          actionLabel={t('shell.overview.experiments_create')}
          onAction={() => go('experiments')}
        />
      ) : (
        <div className={styles.list}>
          {snapshot.recentExperiments.map((experiment) => (
            <Row
              key={experiment.id}
              title={experiment.title}
              meta={
                <>
                  <span className={styles.metaText}>
                    {runStatusLabel(t, experiment.lastRunStatus)}
                  </span>
                  {experiment.runCount > 0 && (
                    <span className={styles.metaText}>
                      {t('shell.overview.experiment_runs', { n: experiment.runCount })}
                    </span>
                  )}
                </>
              }
              onClick={() => go('experiment', { entityId: experiment.id })}
            />
          ))}
        </div>
      )}
    </Card>
  )
}

function NotesCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot, go } = ctx
  if (!widgetVisible(snapshot, 'recent_notes')) return null
  return (
    <Card
      title={t('shell.overview.card_notes')}
      actions={<ViewAll onClick={() => go('notes')} />}
    >
      {snapshot.recentNotes.length === 0 ? (
        <CardEmpty
          text={t('shell.overview.notes_empty')}
          actionLabel={t('shell.overview.notes_create')}
          onAction={() => go('notes')}
        />
      ) : (
        <div className={styles.list}>
          {snapshot.recentNotes.map((note) => (
            <Row
              key={note.id}
              title={note.title}
              meta={<span className={styles.metaText}>{note.updatedAt.slice(0, 10)}</span>}
              onClick={() => go('note', { entityId: note.id })}
            />
          ))}
        </div>
      )}
    </Card>
  )
}

/* ----------------------------------------------------------- side column */

function IdeasCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot, go } = ctx
  if (!widgetVisible(snapshot, 'active_ideas')) return null
  return (
    <Card
      title={t('shell.overview.card_ideas')}
      actions={<ViewAll onClick={() => go('ideas')} />}
    >
      {snapshot.activeIdeas.length === 0 ? (
        <CardEmpty
          text={t('shell.overview.ideas_empty')}
          actionLabel={t('shell.overview.ideas_create')}
          onAction={() => go('ideas')}
        />
      ) : (
        <div className={styles.list}>
          {snapshot.activeIdeas.map((idea) => (
            <Row
              key={idea.id}
              title={idea.title}
              meta={
                <Badge tone={ideaStatusTone(idea.status)}>
                  {ideaStatusLabel(t, idea.status)}
                </Badge>
              }
              onClick={() => go('idea', { entityId: idea.id })}
            />
          ))}
        </div>
      )}
    </Card>
  )
}

function LiteratureCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element | null {
  const t = useT()
  const { snapshot, go } = ctx
  if (!widgetVisible(snapshot, 'literature')) return null
  return (
    <Card
      title={t('shell.overview.card_literature')}
      actions={<ViewAll onClick={() => go('literature')} />}
    >
      {snapshot.literature.length === 0 ? (
        <div className={styles.inlineEmpty}>
          <span className={styles.metaText}>{t('shell.overview.literature_empty')}</span>
          <button type="button" className={styles.linkButton} onClick={() => go('literature')}>
            {t('shell.overview.literature_add')}
          </button>
        </div>
      ) : (
        <div className={styles.list}>
          {snapshot.literature.map((paper) => {
            const meta = paperMeta(paper.authors, paper.year)
            return (
              <Row
                key={paper.id}
                title={paper.title}
                meta={
                  <>
                    {meta !== '' && <span className={styles.metaText}>{meta}</span>}
                    {paper.missing && (
                      <Badge tone="warning">{t('shell.overview.paper_missing')}</Badge>
                    )}
                  </>
                }
                onClick={() => go('paper', { entityId: paper.id })}
              />
            )
          })}
        </div>
      )}
    </Card>
  )
}

function ActivityCard({ ctx }: { readonly ctx: BoardCtx }): JSX.Element {
  const t = useT()
  const { snapshot, projectId, go } = ctx
  const weekly = snapshot.weeklyProgress
  const activity = useQuery({
    queryKey: [...queryKeys.project(projectId).all, 'activity', ACTIVITY_LIMIT],
    queryFn: () => overviewApi.activity(projectId, ACTIVITY_LIMIT),
    enabled: projectId !== ''
  })

  const viewAll = (): void => {
    if (weekly.reportId !== null) go('weekly_report', { entityId: weekly.reportId })
    else go('weekly_review')
  }

  return (
    <Card
      title={t('shell.overview.activity_title')}
      actions={
        <>
          <WidgetBadge kind="weekly_progress" state={widgetState(snapshot, 'weekly_progress')} />
          <WidgetRefresh
            kind="weekly_progress"
            state={widgetState(snapshot, 'weekly_progress')}
            refreshing={ctx.refreshing}
            onRefresh={ctx.onRefresh}
          />
          <ViewAll onClick={viewAll} />
        </>
      }
    >
      <WidgetError kind="weekly_progress" widgetErrors={ctx.widgetErrors} />
      {widgetVisible(snapshot, 'weekly_progress') && weekly.reportId !== null && (
        <button type="button" className={styles.weeklyStrip} onClick={viewAll}>
          <Badge tone="success">{t('shell.overview.weekly_confirmed')}</Badge>
          <span className={styles.weeklyExcerpt}>
            {weekly.excerpt.trim() === ''
              ? t('shell.overview.weekly_no_excerpt')
              : weekly.excerpt.split('\n')[0]}
          </span>
        </button>
      )}
      {activity.isPending ? (
        <Skeleton rows={3} />
      ) : activity.isError ? (
        <p className={styles.hint}>
          {activity.error instanceof Error
            ? activity.error.message
            : t('shell.overview.activity_empty')}
        </p>
      ) : activity.data.items.length === 0 ? (
        <p className={styles.metaText}>{t('shell.overview.activity_empty')}</p>
      ) : (
        <div className={styles.list}>
          {activity.data.items.slice(0, ACTIVITY_LIMIT).map((item) => (
            <ActivityRow key={item.id} item={item} t={t} />
          ))}
        </div>
      )}
    </Card>
  )
}

function ActivityRow({
  item,
  t
}: {
  readonly item: ProjectActivityItem
  readonly t: Translations
}): JSX.Element {
  const navigate = useNavigate()
  const Icon = iconFor(activityIcon(item.entityType))
  const href = item.entityGone ? undefined : activityHref(item)
  const relative = relativeTime(item.createdAt)
  const body = (
    <>
      <Icon aria-hidden="true" className={styles.timelineIcon} strokeWidth={2} />
      <span className={styles.timelineTitle}>{item.summary}</span>
      <span className={styles.time}>
        {relative.vars === undefined ? t(relative.key) : t(relative.key, relative.vars)}
      </span>
    </>
  )
  if (href === undefined) return <div className={styles.activityRow}>{body}</div>
  return (
    <button type="button" className={styles.activityRow} onClick={() => navigate(href)}>
      {body}
    </button>
  )
}

/* ---------------------------------------------------------------- pieces */

interface RowProps {
  readonly title: string
  readonly meta?: JSX.Element
  readonly onClick: () => void
}

function Row({ title, meta, onClick }: RowProps): JSX.Element {
  const t = useT()
  return (
    <button type="button" className={styles.row} onClick={onClick}>
      <span className={styles.rowTitle}>
        {title.trim() === '' ? t('shell.overview.untitled') : title}
      </span>
      {meta !== undefined && <span className={styles.rowMeta}>{meta}</span>}
    </button>
  )
}

interface CardEmptyProps {
  readonly text: string
  readonly actionLabel: string
  readonly onAction: () => void
}

function CardEmpty({ text, actionLabel, onAction }: CardEmptyProps): JSX.Element {
  return (
    <div className={styles.cardEmpty}>
      <p className={styles.metaText}>{text}</p>
      <Button variant="secondary" size="sm" onClick={onAction}>
        {actionLabel}
      </Button>
    </div>
  )
}
