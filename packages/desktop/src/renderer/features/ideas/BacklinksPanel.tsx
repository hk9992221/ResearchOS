import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Background, Controls, ReactFlow, type Edge, type Node } from '@xyflow/react'

import type { ContextGraph } from '@researchos/shared'

import { Badge, EmptyState, ErrorState, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { ideasApi } from './api'
import { entityRoute, formatTime, graphNodeLabel, isContextKind, rosHref } from './model'
import styles from './ideas.module.css'

import '@xyflow/react/dist/style.css'

export interface BacklinksPanelProps {
  readonly projectId: string
  readonly ideaId: string
}

/**
 * Backlink ego graph from W6 `project.context`: depth 1–2, capped at 24 nodes
 * by the domain. The list keeps the text snippet and its note link; the graph
 * is the same data laid out, never a second read.
 */
export function BacklinksPanel({ projectId, ideaId }: BacklinksPanelProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()
  const [depth, setDepth] = useState<1 | 2>(1)

  const context = useQuery({
    queryKey: ['project', projectId, 'context', ideaId, depth],
    queryFn: () => ideasApi.context(projectId, ideaId, depth)
  })

  const graph: ContextGraph | undefined = context.data?.graph
  const nodeTitle = new Map((graph?.nodes ?? []).map((node) => [node.id, node.title]))

  const nodes: Node[] = useMemo(
    () =>
      (graph?.nodes ?? []).map((node, index) => ({
        id: `${node.type}:${node.id}`,
        position: {
          x: node.center ? 0 : Math.cos((index / Math.max(1, (graph?.nodes.length ?? 1) - 1)) * Math.PI * 2) * 260,
          y: node.center ? 0 : Math.sin((index / Math.max(1, (graph?.nodes.length ?? 1) - 1)) * Math.PI * 2) * 220
        },
        data: { label: graphNodeLabel(node) },
        style: {
          width: 160,
          border: node.center ? '2px solid var(--accent-default)' : '1px solid var(--border-default)',
          borderRadius: 'var(--radius-md)',
          background: 'var(--bg-surface)',
          color: 'var(--text-primary)',
          fontSize: 'var(--text-caption-size)'
        }
      })),
    [graph]
  )

  const edges: Edge[] = useMemo(
    () =>
      (graph?.edges ?? []).map((edge, index) => ({
        id: `${edge.from.type}:${edge.from.id}->${edge.to.type}:${edge.to.id}:${index}`,
        source: `${edge.from.type}:${edge.from.id}`,
        target: `${edge.to.type}:${edge.to.id}`,
        label: edge.relation,
        animated: false
      })),
    [graph]
  )

  const openRef = (ref: { type: string; id: string }): void => {
    if (!isContextKind(ref.type)) return
    const destination = entityRoute(ref, projectId)
    if (destination !== undefined) navigate(destination)
  }

  if (context.isPending) return <Skeleton rows={5} />
  if (context.isError) {
    return (
      <ErrorState
        title={t('pages.ideas.backlinks_error')}
        message={context.error instanceof Error ? context.error.message : undefined}
        onRetry={() => void context.refetch()}
        retryLabel={t('pages.ideas.retry')}
      />
    )
  }

  const backlinks = context.data?.backlinks ?? []

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.ideas.backlinks_title')}</h2>
        <div className={styles.segmented} role="group" aria-label={t('pages.ideas.backlinks_title')}>
          {[1, 2].map((value) => (
            <button
              key={value}
              type="button"
              className={styles.segment}
              aria-pressed={depth === value}
              onClick={() => setDepth(value as 1 | 2)}
            >
              {t(value === 1 ? 'pages.ideas.backlinks_depth_1' : 'pages.ideas.backlinks_depth_2')}
            </button>
          ))}
        </div>
      </div>

      {backlinks.length === 0 && (graph?.nodes.length ?? 0) <= 1 ? (
        <EmptyState
          title={t('pages.ideas.backlinks_empty_title')}
          description={t('pages.ideas.backlinks_empty_reason')}
          headingLevel={3}
          compact
        />
      ) : (
        <>
          {backlinks.length > 0 && (
            <ul className={styles.backlinkList}>
              {backlinks.map((backlink) => (
                <li key={backlink.noteId} className={styles.backlinkRow}>
                  <span className={styles.snippet}>{backlink.snippet}</span>
                  <a
                    href={rosHref({ type: 'note', id: backlink.noteId })}
                    className={styles.linkButton}
                    onClick={(event) => {
                      event.preventDefault()
                      const destination = entityRoute({ type: 'note', id: backlink.noteId }, projectId)
                      if (destination !== undefined) navigate(destination)
                    }}
                  >
                    {backlink.noteTitle}
                  </a>
                  {backlink.viaRelation && <Badge tone="info">{t('pages.ideas.backlinks_via_relation')}</Badge>}
                  <span className={styles.muted}>{formatTime(backlink.updatedAt)}</span>
                </li>
              ))}
            </ul>
          )}

          {graph !== undefined && graph.nodes.length > 1 && (
            <>
              {graph.truncated && (
                <p className={styles.muted}>{t('pages.ideas.graph_truncated', { n: 24 })}</p>
              )}
              <div className={styles.graph}>
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  fitView
                  nodesDraggable={false}
                  nodesConnectable={false}
                  elementsSelectable={false}
                  onNodeClick={(_event, node) => {
                    const [type, ...rest] = node.id.split(':')
                    const id = rest.join(':')
                    if (type !== undefined && id !== '') openRef({ type, id })
                  }}
                >
                  <Background />
                  <Controls showInteractive={false} />
                </ReactFlow>
              </div>
              <ol className={styles.srOnly} aria-label={t('pages.ideas.backlinks_edges')}>
                {edges.map((edge) => (
                  <li key={edge.id}>
                    {nodeTitle.get(edge.source.split(':').slice(1).join(':')) ?? edge.source} →{' '}
                    {nodeTitle.get(edge.target.split(':').slice(1).join(':')) ?? edge.target}
                  </li>
                ))}
              </ol>
            </>
          )}
        </>
      )}
    </section>
  )
}