import { Show, Switch, Match, For } from "solid-js";
import type { Accessor, JSX } from "solid-js";
import type { Dir, Node, Pane } from "./layout";

// The view-agnostic tiling chrome: it renders the split tree, the divider
// drag-to-resize, the edge-overlay split buttons, and the drag gestures. Each tile is a
// STACK shown as a tab bar over a body; dragging a tab onto another tile's center stacks
// it there (tabs), onto an edge moves it beside (re-tile). Everything screen-specific (a
// pane's tab label, optional sub-header, and body) is supplied via TileCtx callbacks, so
// the same chrome drives the Files explorer and the Terminal workspace.

const SPLIT_EDGES = [
  { side: "top", dir: "v", before: true },
  { side: "bottom", dir: "v", before: false },
  { side: "left", dir: "h", before: true },
  { side: "right", dir: "h", before: false },
] as const;

// Where a drop lands on a target tile: its center STACKS the dragged pane in as a tab,
// an edge moves it to that side.
export type DropZone = "stack" | "left" | "right" | "top" | "bottom";

// dropZone maps the pointer position within a tile to a zone: the central region (each
// edge > EDGE_BAND away) is "stack", otherwise the nearest edge.
const EDGE_BAND = 0.3;
export function dropZone(e: DragEvent): DropZone {
  const el = e.currentTarget as HTMLElement;
  const r = el.getBoundingClientRect();
  if (!r.width || !r.height) return "stack";
  const px = (e.clientX - r.left) / r.width;
  const py = (e.clientY - r.top) / r.height;
  const dl = px;
  const dr = 1 - px;
  const dt = py;
  const db = 1 - py;
  const m = Math.min(dl, dr, dt, db);
  if (m > EDGE_BAND) return "stack";
  if (m === dl) return "left";
  if (m === dr) return "right";
  if (m === dt) return "top";
  return "bottom";
}

// TileCtx threads the shared handlers and the host's per-pane renderers to every node.
// Drag target/drop ids are the DEST tile's active pane id (stackPane/movePane resolve
// the stack from any pane in it).
export interface TileCtx<P> {
  focused: Accessor<string>;
  setFocus: (id: string) => void;
  // activate makes a pane the visible tab of its stack (and focuses it).
  activate: (id: string) => void;
  closePane: (id: string) => void;
  splitAt: (id: string, dir: Dir, before: boolean) => void;
  setRatio: (id: string, ratio: number) => void;
  // A drag carries a "pane" (one tab) or a "stack" (the whole tile / all its tabs);
  // source is the dragged pane id or stack id accordingly.
  drag: {
    source: Accessor<string | null>;
    target: Accessor<string | null>;
    zone: Accessor<DropZone | null>;
    begin: (id: string, kind: "pane" | "stack") => void;
    over: (id: string, zone: DropZone) => void;
    drop: (id: string, zone: DropZone) => void;
    end: () => void;
  };
  // Host-supplied rendering. tabTitle is the short label on a tab; body draws a pane's
  // content; subHeader is an optional row under the tab bar for the active pane (e.g.
  // the Files breadcrumb) — return null to omit it for a given pane; headerExtra is an
  // optional slot at the tab bar's right (e.g. a refresh button), given the active pane.
  tabTitle: (pane: Pane<P>) => JSX.Element;
  body: (pane: Pane<P>) => JSX.Element;
  subHeader?: (pane: Pane<P>) => JSX.Element | null;
  headerExtra?: (pane: Pane<P>) => JSX.Element;
}

// TreeNode dispatches on node kind. Reading node.type tracks it in the store, so a
// stack becoming a split (or vice-versa) swaps the branch; a ratio or active-tab change
// leaves both branches — and their live content — mounted.
export function TreeNode<P>(props: { node: Node<P>; ctx: TileCtx<P> }): JSX.Element {
  return (
    <Switch>
      <Match when={props.node.type === "stack"}>
        <StackView node={props.node as Extract<Node<P>, { type: "stack" }>} ctx={props.ctx} />
      </Match>
      <Match when={props.node.type === "split"}>
        <SplitView node={props.node as Extract<Node<P>, { type: "split" }>} ctx={props.ctx} />
      </Match>
    </Switch>
  );
}

function SplitView<P>(props: { node: Extract<Node<P>, { type: "split" }>; ctx: TileCtx<P> }): JSX.Element {
  let container!: HTMLDivElement;

  const startDrag = (e: PointerEvent) => {
    e.preventDefault();
    const handle = e.currentTarget as HTMLElement;
    handle.setPointerCapture(e.pointerId);
    const move = (ev: PointerEvent) => {
      const rect = container.getBoundingClientRect();
      const frac =
        props.node.dir === "h"
          ? (ev.clientX - rect.left) / rect.width
          : (ev.clientY - rect.top) / rect.height;
      props.ctx.setRatio(props.node.id, frac);
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return (
    <div class="split" classList={{ h: props.node.dir === "h", v: props.node.dir === "v" }} ref={container}>
      <div class="split-child" style={{ "flex-grow": String(props.node.ratio) }}>
        <TreeNode node={props.node.a} ctx={props.ctx} />
      </div>
      <div class="divider" onPointerDown={startDrag} />
      <div class="split-child" style={{ "flex-grow": String(1 - props.node.ratio) }}>
        <TreeNode node={props.node.b} ctx={props.ctx} />
      </div>
    </div>
  );
}

function StackView<P>(props: { node: Extract<Node<P>, { type: "stack" }>; ctx: TileCtx<P> }): JSX.Element {
  const activeId = () => props.node.panes[props.node.active]?.id ?? "";
  const activePane = () => props.node.panes[props.node.active];
  const focused = () => props.ctx.focused() === activeId();
  const isDropTarget = () =>
    props.ctx.drag.target() === activeId() && props.ctx.drag.source() !== activeId();
  const zone = () => props.ctx.drag.zone() ?? "stack";

  return (
    <div
      class="stack"
      classList={{
        focused: focused(),
        "drop-target": isDropTarget(),
        dragging: props.ctx.drag.source() === props.node.id,
      }}
      onDragOver={(e) => {
        // Drag/drop and the split-edge overlays act on the whole TILE, so the top edge
        // is the tile's top (over the tab bar), not the body below the tabs.
        // Ignore hovering the tile that is itself being dragged as a whole stack.
        if (!props.ctx.drag.source() || props.ctx.drag.source() === props.node.id) return;
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
        props.ctx.drag.over(activeId(), dropZone(e));
      }}
      onDrop={(e) => {
        e.preventDefault();
        props.ctx.drag.drop(activeId(), dropZone(e));
      }}
    >
      {/* The tab bar is the drag handle for the WHOLE stack: dragging its empty area
          (not a tab) moves/stacks every tab together. Individual tabs stop propagation
          so they drag alone. */}
      <div
        class="stack-tabs"
        classList={{ solo: props.node.panes.length === 1 }}
        role="tablist"
        draggable={true}
        onDragStart={(e) => {
          if (e.dataTransfer) {
            e.dataTransfer.effectAllowed = "move";
            e.dataTransfer.setData("text/plain", props.node.id);
          }
          props.ctx.drag.begin(props.node.id, "stack");
        }}
        onDragEnd={() => props.ctx.drag.end()}
      >
        <For each={props.node.panes}>
          {(pane, i) => (
            <div
              class="tab"
              data-pane-id={pane.id}
              role="tab"
              classList={{
                active: i() === props.node.active,
                dragging: props.ctx.drag.source() === pane.id,
              }}
              draggable={true}
              onDragStart={(e) => {
                e.stopPropagation(); // a tab drags alone, not the whole stack
                if (e.dataTransfer) {
                  e.dataTransfer.effectAllowed = "move";
                  e.dataTransfer.setData("text/plain", pane.id);
                }
                props.ctx.drag.begin(pane.id, "pane");
              }}
              onDragEnd={() => props.ctx.drag.end()}
              onPointerDown={() => props.ctx.activate(pane.id)}
              onClick={() => props.ctx.activate(pane.id)}
            >
              <span class="tab-label">{props.ctx.tabTitle(pane)}</span>
              <button
                class="tab-close"
                title="Close tab"
                aria-label="Close tab"
                draggable={false}
                onClick={(e) => {
                  e.stopPropagation();
                  props.ctx.closePane(pane.id);
                }}
              >
                ✕
              </button>
            </div>
          )}
        </For>
        <span class="stack-tabs-spacer" />
        <Show when={props.ctx.headerExtra && activePane()}>{props.ctx.headerExtra!(activePane())}</Show>
      </div>

      <Show when={props.ctx.subHeader?.(activePane())}>
        {(content) => <div class="stack-subheader">{content()}</div>}
      </Show>

      <div class="stack-body" onPointerDown={() => props.ctx.setFocus(activeId())}>
        <For each={props.node.panes}>
          {(pane, i) => (
            <div
              class="stack-pane"
              classList={{ active: i() === props.node.active }}
              style={{ display: i() === props.node.active ? undefined : "none" }}
            >
              {props.ctx.body(pane)}
            </div>
          )}
        </For>
      </div>

      {/* The split-edge overlays and drop indicator sit on the TILE edges (relative to
          .stack), so the top zone hugs the tile's top edge rather than the body's. */}
      <For each={SPLIT_EDGES}>
        {(edge) => (
          <button
            type="button"
            tabindex={-1}
            class={`pane-split-zone edge-${edge.side}`}
            aria-label={`Split pane, new pane on the ${edge.side}`}
            title={`Split — new pane on the ${edge.side}`}
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation();
              props.ctx.splitAt(activeId(), edge.dir, edge.before);
            }}
          >
            <span class="pane-split-bar" />
          </button>
        )}
      </For>
      <Show when={isDropTarget()}>
        <div class={`pane-drop-indicator zone-${zone()}`}>
          <span class="pane-drop-label">{zone() === "stack" ? "⊞ Stack (tab)" : "Move here"}</span>
        </div>
      </Show>
    </div>
  );
}
