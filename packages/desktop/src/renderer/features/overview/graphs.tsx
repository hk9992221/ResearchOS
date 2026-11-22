import { useMemo, type CSSProperties } from 'react'
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps
} from '@xyflow/react'

import type { ContextGraph, ProvenanceLineageNode, ProvenanceLineageTree } from '@researchos/shared'

import { Badge } from '../../design/components'
import { useT } from '../../i18n'
import { ideaStatusLabel, ideaStatusTone } from './format'
import styles from './overview.module.css'

import '@xyflow/react/dist/style.css'

interface LineageNodeData extends Record<string, unknown> {
  readonly title: string
  readonly status: string
  readonly support: number
  readonly oppose: number
  readonly current: boolean
}

interface LineageLayout {
  readonly nodes: Array<{ id: string; position: { x: number; y: number }; data: LineageNodeData }>
  readonly edges: Array<{ id: string; source: string; target: string }>
}

/**
 * layoutLineage places the derivation forest on a left-to-right grid: depth is
 * the column, the preorder index the row. Positions are deterministic, so a
 * click and the drawn node always agree.
 */
export function layoutLineage(
  roots: readonly ProvenanceLineageNode[],
  focusId: string | null
): LineageLayout {
  const nodes: LineageLayout['nodes'] = []
  const edges: LineageLayout['edges'] = []
  let row = 0
  const walk = (list: readonly ProvenanceLineageNode[], depth: number): void => {
    for (const node of list) {
      nodes.push({
        id: node.id,
        position: { x: depth * 260, y: row * 104 },
        data: {
          title: node.title,
          status: node.status,
          support: node.supportCount,
          oppose: node.opposeCount,
          current: node.id === focusId
        }
      })
      row += 1
      for (const child of node.children) {
        edges.push({ id: `${node.id}->${child.id}`, source: node.id, target: child.id })
      }
      walk(node.children, depth + 1)
    }
  }
  walk(roots, 0)
  return { nodes, edges }
}

export interface LineageFlowProps {
  readonly tree: ProvenanceLineageTree
  readonly focusId: string | null
  readonly onFocus: (ideaId: string) => void
}

/** The Idea derivation forest; a node click focuses the timeline chain. */
export function LineageFlow({ tree, focusId, onFocus }: LineageFlowProps): JSX.Element {
  const t = useT()
  const layout = useMemo(() => layoutLineage(tree.roots, focusId), [tree.roots, focusId])
  const titles = new Map(layout.nodes.map((node) => [node.id, node.data.title]))

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
    label: t('shell.prov.edge_derived'),
    animated: false
  }))

  return (
    <>
      <div className={styles.graph} data-testid="lineage-graph">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={{ lineage: LineageNodeView }}
          fitView
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          onNodeClick={(_event, node) => onFocus(node.id)}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      <ol className={styles.srOnly} aria-label={t('shell.prov.lineage_edges')}>
        {layout.edges.map((edge) => (
          <li key={edge.id}>
            {titles.get(edge.source) ?? edge.source} → {titles.get(edge.target) ?? edge.target}
          </li>
        ))}
      </ol>
    </>
  )
}

function LineageNodeView({ data }: NodeProps<Node<LineageNodeData>>): JSX.Element {
  const t = useT()
  return (
    <div className={[styles.flowNode, data.current ? styles.flowCurrent : ''].join(' ')}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <p className={styles.flowTitle}>{data.title.trim() === '' ? t('shell.overview.untitled') : data.title}</p>
      <div className={styles.flowMeta}>
        <Badge tone={ideaStatusTone(data.status)}>{ideaStatusLabel(t, data.status)}</Badge>
        <span className={styles.metaText}>
          +{data.support} / -{data.oppose}
        </span>
      </div>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  )
}

export interface EgoGraphProps {
  readonly graph: ContextGraph
  readonly onOpen: (ref: { type: string; id: string }) => void
}

/** The context ego graph laid out on two rings: depth 1 inner, depth 2 outer. */
export function EgoGraph({ graph, onOpen }: EgoGraphProps): JSX.Element {
  const nodes: Node[] = useMemo(() => {
    const byDepth = new Map<number, ContextGraph['nodes']>()
    for (const node of graph.nodes) {
      const depth = Math.max(1, node.depth)
      byDepth.set(depth, [...(byDepth.get(depth) ?? []), node])
    }
    const out: Node[] = []
    for (const node of graph.nodes) {
      const style: CSSProperties = {
        width: 170,
        padding: 6,
        border: node.center
          ? '2px solid var(--accent-default)'
          : '1px solid var(--border-default)',
        borderRadius: 'var(--radius-md)',
        background: 'var(--bg-surface)',
        color: 'var(--text-primary)',
        fontSize: 'var(--text-caption-size)'
      }
      if (node.center) {
        out.push({
          id: `${node.type}:${node.id}`,
          position: { x: 0, y: 0 },
          data: { label: `${node.type} · ${node.title}` },
          style,
          draggable: false
        })
        continue
      }
      const depth = Math.max(1, node.depth)
      const ring = byDepth.get(depth) ?? []
      const index = ring.findIndex((candidate) => candidate.id === node.id)
      const angle = (2 * Math.PI * index) / Math.max(ring.length, 1) - Math.PI / 2
      const radius = depth >= 2 ? 340 : 210
      out.push({
        id: `${node.type}:${node.id}`,
        position: { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius },
        data: { label: `${node.type} · ${node.title}` },
        style,
        draggable: false
      })
    }
    return out
  }, [graph.nodes])

  const edges: Edge[] = useMemo(
    () =>
      graph.edges.map((edge, index) => ({
        id: `${edge.from.type}:${edge.from.id}->${edge.to.type}:${edge.to.id}:${index}`,
        source: `${edge.from.type}:${edge.from.id}`,
        target: `${edge.to.type}:${edge.to.id}`,
        label: edge.relation,
        animated: false
      })),
    [graph.edges]
  )

  return (
    <div className={styles.graph} data-testid="ego-graph">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        onNodeClick={(_event, node) => {
          const [type, ...rest] = node.id.split(':')
          if (type !== undefined && rest.length > 0) onOpen({ type, id: rest.join(':') })
        }}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}
