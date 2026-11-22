import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Background, Controls, Handle, Position, ReactFlow, type Edge, type Node, type NodeProps } from '@xyflow/react'

import type { IdeaStatus } from '@researchos/shared'

import { Badge, EmptyState, ErrorState, Skeleton } from '../../design/components'
import { useT } from '../../i18n'
import { routeHref } from '../../routes'
import { ideasApi } from './api'
import {
  STATUS_ORDER,
  layoutLineage,
  statusKey,
  statusTone,
  type LineageNodeData
} from './model'
import styles from './ideas.module.css'

import '@xyflow/react/dist/style.css'

export interface LineageGraphProps {
  readonly projectId: string
  readonly ideaId: string
}

/** Read-only derivation forest: derived_from edges, click a node to open it. */
export function LineageGraph({ projectId, ideaId }: LineageGraphProps): JSX.Element {
  const t = useT()
  const navigate = useNavigate()

  const lineage = useQuery({
    queryKey: ['project', projectId, 'provenance', ideaId],
    queryFn: () => ideasApi.lineage(projectId, ideaId)
  })

  const tree = lineage.data?.focusedLineage ?? lineage.data?.lineage
  const roots = tree?.roots ?? []
  const layout = useMemo(() => layoutLineage(roots, ideaId), [roots, ideaId])

  const nodes: Node<LineageNodeData>[] = layout.nodes.map((node) => ({
    id: node.id,
    type: 'lineage',
    position: node.position,
    data: node.data,
    draggable: false
  }))

  const edges: Edge[] = layout.edges.map((edge) => ({
    id: edge.id,
    source: edge.source,
    target: edge.target,
    label: edge.label,
    animated: false
  }))

  const titleById = new Map(layout.nodes.map((node) => [node.id, node.data.title]))

  if (lineage.isPending) return <Skeleton rows={5} />
  if (lineage.isError) {
    return (
      <ErrorState
        title={t('pages.ideas.lineage_error')}
        message={lineage.error instanceof Error ? lineage.error.message : undefined}
        onRetry={() => void lineage.refetch()}
        retryLabel={t('pages.ideas.retry')}
      />
    )
  }
  if (roots.length === 0) {
    return (
      <EmptyState
        title={t('pages.ideas.lineage_empty_title')}
        description={t('pages.ideas.lineage_empty_reason')}
        headingLevel={3}
        compact
      />
    )
  }

  return (
    <section className={styles.panel}>
      <div className={styles.panelHead}>
        <h2 className={styles.sectionTitle}>{t('pages.ideas.lineage_title')}</h2>
      </div>
      <p className={styles.muted}>{t('pages.ideas.lineage_hint')}</p>
      <div className={styles.graph} data-testid="lineage-graph">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={{ lineage: LineageNodeView }}
          fitView
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          onNodeClick={(_event, node) => {
            if (node.id === ideaId) return
            const href = routeHref('idea', { projectId, entityId: node.id })
            if (href !== undefined) navigate(href)
          }}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      <ol className={styles.srOnly} aria-label={t('pages.ideas.lineage_edges')}>
        {layout.edges.map((edge) => (
          <li key={edge.id}>
            {titleById.get(edge.source) ?? edge.source} → {titleById.get(edge.target) ?? edge.target}
          </li>
        ))}
      </ol>
    </section>
  )
}

function LineageNodeView({ data }: NodeProps<Node<LineageNodeData>>): JSX.Element {
  const t = useT()
  const status = (
    STATUS_ORDER.includes(data.status as IdeaStatus) ? data.status : 'New'
  ) as IdeaStatus
  return (
    <div className={[styles.lineageNode, data.current ? styles.lineageCurrent : ''].join(' ')}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <p className={styles.lineageTitle}>{data.title.trim() === '' ? t('pages.ideas.untitled') : data.title}</p>
      <div className={styles.lineageMeta}>
        <Badge tone={statusTone(status)}>{t(statusKey(status))}</Badge>
        <span className={styles.muted}>
          {data.supportCount} / {data.opposeCount}
        </span>
      </div>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  )
}