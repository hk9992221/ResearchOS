import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Network, X } from 'lucide-react'

import type {
  ContextGraph,
  ProvenanceGranularity,
  ProvenanceNode,
  ProvenanceReport
} from '@researchos/shared'

import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  Skeleton,
  Tabs,
  Toolbar
} from '../../design/components'
import { useT } from '../../i18n'
import { routeHref, type RouteKind } from '../../routes'
import { isContextKind, overviewApi, overviewKeys } from './api'
import { milestoneLabel, milestoneTone, nodeTime } from './format'
import { EgoGraph, LineageFlow } from './graphs'
import styles from './overview.module.css'

const ENTITY_ROUTE: Readonly<Record<string, RouteKind>> = {
  paper: 'paper',
  note: 'note',
  idea: 'idea',
  experiment: 'experiment',
  run: 'run',
  task: 'task',
  weekly_report: 'weekly_report'
}

function entityHref(projectId: string, type: string, id: string): string | undefined {
  const kind = ENTITY_ROUTE[type]
  return kind === undefined ? undefined : routeHref(kind, { projectId, entityId: id })
}

export interface ProvenanceViewProps {
  readonly projectId: string
  readonly onInspect: (ref: { type: string; id: string; title: string }) => void
}

/**
 * The「科研脉络」view: Idea lineage forest, the milestone timeline grouped by
 * ISO week or month, and the milestone/focus filters from 
 */
export function ProvenanceView({ projectId, onInspect }: ProvenanceViewProps): JSX.Element {
  const t = useT()
  const [milestones, setMilestones] = useState(false)
  const [granularity, setGranularity] = useState<ProvenanceGranularity>('week')
  const [focusIdeaId, setFocusIdeaId] = useState<string | null>(null)

  const filter = { milestones, granularity, focusIdeaId }
  const query = useQuery({
    queryKey: overviewKeys.provenance(projectId, filter),
    queryFn: () => overviewApi.provenance(projectId, filter)
  })

  const report: ProvenanceReport | undefined = query.data
  const tree = focusIdeaId !== null ? report?.focusedLineage : report?.lineage

  return (
    <>
      <Toolbar label={t('shell.prov.timeline_title')}>
        <Tabs
          label={t('shell.prov.filter_milestones')}
          items={[
            { id: 'all', label: t('shell.prov.filter_all') },
            { id: 'milestones', label: t('shell.prov.filter_milestones') }
          ]}
          value={milestones ? 'milestones' : 'all'}
          onChange={(id) => setMilestones(id === 'milestones')}
        />
        <Tabs
          label={t('shell.prov.group_week')}
          items={[
            { id: 'week', label: t('shell.prov.group_week') },
            { id: 'month', label: t('shell.prov.group_month') }
          ]}
          value={granularity}
          onChange={(id) => setGranularity(id as ProvenanceGranularity)}
        />
        {focusIdeaId !== null && (
          <Button variant="ghost" size="sm" icon={X} onClick={() => setFocusIdeaId(null)}>
            {t('shell.prov.clear_focus')}
          </Button>
        )}
      </Toolbar>

      {query.isError && (
        <ErrorState
          title={t('shell.prov.load_fail')}
          message={query.error instanceof Error ? query.error.message : undefined}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void query.refetch()}
        />
      )}

      {query.isPending && <Skeleton rows={10} />}

      {query.isSuccess && report !== undefined && (
        <>
          {tree !== undefined && tree.roots.length > 0 && (
            <Card title={t('shell.prov.lineage_title')}>
              <LineageFlow
                tree={tree}
                focusId={focusIdeaId}
                onFocus={(ideaId) =>
                  setFocusIdeaId((current) => (current === ideaId ? null : ideaId))
                }
              />
            </Card>
          )}

          <Card title={t('shell.prov.timeline_title')}>
            {focusIdeaId !== null && (
              <p className={styles.metaText}>{t('shell.prov.focus_active')}</p>
            )}
            {report.nodes.length === 0 ? (
              <EmptyState
                title={t('shell.prov.empty_range_title')}
                description={t('shell.prov.empty_range_reason')}
                compact
                headingLevel={3}
              />
            ) : (
              report.sections.map((section) => (
                <section key={section.key} className={styles.section}>
                  <h3 className={styles.sectionTitle}>{section.label}</h3>
                  <div className={styles.list}>
                    {section.nodes.map((node) => (
                      <TimelineNode
                        key={node.id}
                        projectId={projectId}
                        node={node}
                        month={granularity === 'month'}
                        onInspect={() =>
                          onInspect({ type: node.entity.type, id: node.entity.id, title: node.title })
                        }
                      />
                    ))}
                  </div>
                </section>
              ))
            )}
          </Card>
        </>
      )}
    </>
  )
}

interface TimelineNodeProps {
  readonly projectId: string
  readonly node: ProvenanceNode
  readonly month: boolean
  readonly onInspect: () => void
}

function TimelineNode({ projectId, node, month, onInspect }: TimelineNodeProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const href = entityHref(projectId, node.entity.type, node.entity.id)
  return (
    <div className={styles.timelineRow}>
      <span className={styles.time}>{nodeTime(node.at, month)}</span>
      <Badge tone={milestoneTone(node.kind)}>{milestoneLabel(t, node.kind)}</Badge>
      <span className={styles.timelineBody}>
        {href === undefined ? (
          <span className={styles.timelineTitle}>{node.title}</span>
        ) : (
          <button
            type="button"
            className={styles.linkButton}
            onClick={() => navigate(href)}
          >
            {node.title}
          </button>
        )}
        {node.summary !== '' && <span className={styles.metaText}>{node.summary}</span>}
      </span>
      {node.badge !== '' && <Badge tone="neutral">{node.badge}</Badge>}
      {isContextKind(node.entity.type) && (
        <IconButton
          icon={Network}
          label={t('shell.prov.node_context')}
          size="sm"
          onClick={onInspect}
        />
      )}
    </div>
  )
}

/* ----------------------------------------------------------- connections */

export interface GraphTarget {
  readonly type: string
  readonly id: string
  readonly title: string
}

export interface ConnectionsViewProps {
  readonly projectId: string
  readonly target: GraphTarget | null
  readonly onClear: () => void
}

/**
 * Backlink ego graph from `project.context`: depth 1–2, capped at 24 nodes by
 * the domain. The graph and the backlink list are one read, never two.
 */
export function ConnectionsView({
  projectId,
  target,
  onClear
}: ConnectionsViewProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [depth, setDepth] = useState<1 | 2>(1)

  const enabled = target !== null && isContextKind(target.type)
  const query = useQuery({
    queryKey: overviewKeys.context(projectId, target?.type ?? 'none', target?.id ?? 'none', depth),
    queryFn: () => {
      const resolved = target
      if (resolved === null || !isContextKind(resolved.type)) {
        throw new Error('context needs an entity')
      }
      return overviewApi.context(projectId, resolved.type, resolved.id, depth)
    },
    enabled
  })

  if (!enabled) {
    return (
      <EmptyState
        title={t('shell.overview.graph_empty_title')}
        description={t('shell.overview.graph_empty_reason')}
      />
    )
  }

  const graph: ContextGraph | undefined = query.data?.graph

  return (
    <>
      <Toolbar label={t('shell.overview.graph_title')}>
        <Tabs
          label={t('shell.overview.graph_title')}
          items={[
            { id: '1', label: t('shell.overview.graph_depth_1') },
            { id: '2', label: t('shell.overview.graph_depth_2') }
          ]}
          value={String(depth)}
          onChange={(id) => setDepth(id === '2' ? 2 : 1)}
        />
        <Button variant="ghost" size="sm" icon={X} onClick={onClear}>
          {t('shell.overview.btn_clear_target')}
        </Button>
      </Toolbar>

      {query.isPending && <Skeleton rows={6} />}

      {query.isError && (
        <ErrorState
          title={t('shell.overview.graph_load_fail')}
          message={query.error instanceof Error ? query.error.message : undefined}
          retryLabel={t('shell.overview.error_retry')}
          onRetry={() => void query.refetch()}
        />
      )}

      {query.isSuccess && (
        <>
          <Card title={t('shell.overview.graph_title')}>
            <div className={styles.graphHead}>
              <span className={styles.graphTarget}>{target.title}</span>
              {graph?.truncated === true && (
                <Badge tone="warning">
                  {t('shell.overview.graph_truncated', { n: 24 })}
                </Badge>
              )}
            </div>
            {graph !== undefined && graph.nodes.length > 1 ? (
              <EgoGraph
                graph={graph}
                onOpen={(ref) => {
                  const href = entityHref(projectId, ref.type, ref.id)
                  if (href !== undefined) navigate(href)
                }}
              />
            ) : (
              <p className={styles.metaText}>{t('shell.overview.graph_hint')}</p>
            )}
          </Card>

          <Card title={t('shell.overview.graph_backlinks')}>
            {query.data.backlinks.length === 0 ? (
              <p className={styles.metaText}>{t('pages.inspector.empty_title')}</p>
            ) : (
              <ul className={styles.backlinks}>
                {query.data.backlinks.map((backlink) => (
                  <li key={backlink.noteId} className={styles.backlinkRow}>
                    <span className={styles.metaText}>{backlink.snippet}</span>
                    <button
                      type="button"
                      className={styles.linkButton}
                      onClick={() => {
                        const href = routeHref('note', {
                          projectId,
                          entityId: backlink.noteId
                        })
                        if (href !== undefined) navigate(href)
                      }}
                    >
                      {backlink.noteTitle}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </>
      )}
    </>
  )
}