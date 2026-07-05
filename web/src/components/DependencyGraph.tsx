import { For, createMemo } from "solid-js";
import dagre from "@dagrejs/dagre";
import type { Graph } from "../api";

const NODE_W = 190;
const NODE_H = 58;

// DependencyGraph renders the compose depends_on graph: dagre computes the
// left-to-right layered layout, plain SVG draws it. Edges point from the
// dependent service to its dependency, labeled with the wait condition.
export default function DependencyGraph(props: { graph: Graph }) {
  const layout = createMemo(() => {
    const g = new dagre.graphlib.Graph();
    g.setGraph({ rankdir: "LR", nodesep: 28, ranksep: 70, marginx: 12, marginy: 12 });
    g.setDefaultEdgeLabel(() => ({}));
    for (const n of props.graph.nodes) {
      g.setNode(n.service, { width: NODE_W, height: NODE_H });
    }
    for (const e of props.graph.edges) {
      g.setEdge(e.from, e.to, { condition: e.condition });
    }
    dagre.layout(g);

    const nodes = props.graph.nodes.map((n) => ({ ...n, ...g.node(n.service) }));
    const edges = props.graph.edges.map((e) => {
      const pts = g.edge(e.from, e.to).points;
      const path = pts.map((p, i) => `${i === 0 ? "M" : "L"}${p.x},${p.y}`).join(" ");
      const mid = pts[Math.floor(pts.length / 2)];
      return { ...e, path, labelX: mid.x, labelY: mid.y - 6 };
    });
    const { width = 0, height = 0 } = g.graph();
    return { nodes, edges, width, height };
  });

  return (
    <svg
      class="graph"
      viewBox={`0 0 ${layout().width} ${layout().height}`}
      width={layout().width}
      height={layout().height}
      style={{ "max-width": "100%", height: "auto" }}
    >
      <defs>
        <marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path class="arrow" d="M 0 0 L 8 4 L 0 8 z" />
        </marker>
      </defs>
      <For each={layout().edges}>
        {(e) => (
          <g class="edge">
            <path d={e.path} marker-end="url(#arrow)" />
            {e.condition && (
              <text x={e.labelX} y={e.labelY} text-anchor="middle">
                {e.condition.replace("service_", "")}
              </text>
            )}
          </g>
        )}
      </For>
      <For each={layout().nodes}>
        {(n) => (
          <g class={`node${n.running ? " running" : ""}`}>
            <rect x={n.x - NODE_W / 2} y={n.y - NODE_H / 2} width={NODE_W} height={NODE_H} rx="8" />
            <text x={n.x} y={n.y - 6} text-anchor="middle" font-weight="600">
              {n.service}
            </text>
            <text class="sub" x={n.x} y={n.y + 14} text-anchor="middle">
              {n.summary}
            </text>
          </g>
        )}
      </For>
    </svg>
  );
}
