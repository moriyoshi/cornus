// Generic, DOM-free binary-tree model for a tiled (tmux-style) workspace. Internal
// nodes are splits; the leaves are STACKS — one or more panes sharing a tile, shown as
// tabs, with `active` selecting the visible one. A single-pane stack renders as a plain
// pane. Each pane carries an opaque payload P (the per-pane content state a view wants
// to carry through splits, moves, stacking, and persistence). Every operation returns a
// NEW tree, so it plays well with Solid's reconciliation and is trivially unit-testable.
//
// This is the view-agnostic core both the Files explorer and the Terminal workspace
// build their tiling on (chrome in views/tiling/panes.tsx).

export type Dir = "h" | "v";

// A pane: a stable id plus the view's own payload.
export interface Pane<P> {
  id: string;
  data: P;
}

export type Node<P> =
  // A stack of one or more panes occupying one tile, shown as tabs; `active` indexes
  // the visible pane. Its `id` identifies the tile.
  | { type: "stack"; id: string; panes: Pane<P>[]; active: number }
  // A split arranges two children along an axis: dir "h" places them left/right
  // (vertical divider, dragged horizontally); dir "v" places them top/bottom. ratio is
  // a's fraction of the axis (0..1).
  | { type: "split"; id: string; dir: Dir; a: Node<P>; b: Node<P>; ratio: number };

export interface LayoutState<P> {
  // focused is the id of the active pane of the focused stack.
  tree: Node<P>;
  focused: string;
}

const MIN_RATIO = 0.08;
const MAX_RATIO = 0.92;

let counter = 0;

export function uid(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === "function") return c.randomUUID();
  return `n${(counter++).toString(36)}-${Math.floor(Math.random() * 1e9).toString(36)}`;
}

export function newPane<P>(data: P): Pane<P> {
  return { id: uid(), data };
}

export function stack<P>(panes: Pane<P>[], active = 0): Node<P> {
  return { type: "stack", id: uid(), panes, active };
}

// leaf is sugar for a single-pane stack.
export function leaf<P>(pane: Pane<P>): Node<P> {
  return stack([pane], 0);
}

// splitPane replaces the STACK containing `targetId` with a split of that stack plus a
// fresh single-pane stack whose payload comes from `makeData(target)`. `before` places
// the new tile on the low side of the axis (left for dir "h", top for dir "v"). New
// split gets ratio 0.5. Returns the new pane id so the caller can focus it.
export function splitPane<P>(
  tree: Node<P>,
  targetId: string,
  dir: Dir,
  makeData: (from: Pane<P>) => P,
  before = false,
): { tree: Node<P>; newPaneId: string } {
  let newPaneId = "";
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      const target = n.panes.find((p) => p.id === targetId);
      if (target) {
        const np = newPane(makeData(target));
        newPaneId = np.id;
        const created = stack([np], 0);
        const [a, b] = before ? [created, n] : [n, created];
        return { type: "split", id: uid(), dir, a, b, ratio: 0.5 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return { tree: walk(tree), newPaneId };
}

// stackPane moves `srcId` into the stack containing `destId` as a new, active tab,
// removing it from its old stack (which collapses if it becomes empty). This is the
// drag-onto-center gesture (it replaces the old "swap"). If the two panes already share
// a stack it just activates src. No-op if an id is missing or the two are the same.
export function stackPane<P>(tree: Node<P>, srcId: string, destId: string): Node<P> {
  if (srcId === destId) return tree;
  const srcPane = findPane(tree, srcId);
  const destStack = stackOf(tree, destId);
  if (!srcPane || !destStack) return tree;
  if (destStack.panes.some((p) => p.id === srcId)) return activatePane(tree, srcId);

  const { tree: detached } = detachPane(tree, srcId);
  if (!detached) return tree;
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.id === destStack.id) {
        const panes = [...n.panes, srcPane];
        return { ...n, panes, active: panes.length - 1 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return walk(detached);
}

// detachPane removes a pane from its stack. If the stack still has panes it survives
// (its active index is kept in range); if it empties, its parent split collapses,
// promoting the sibling. Returns a null tree when the removed pane was the only one in
// the whole tree, plus the removed pane and the natural next-focus (the surviving
// stack's active pane, or the promoted sibling's first pane).
function detachPane<P>(
  tree: Node<P>,
  paneId: string,
): { tree: Node<P> | null; removed?: Pane<P>; focus?: string } {
  if (tree.type === "stack") {
    const idx = tree.panes.findIndex((p) => p.id === paneId);
    if (idx < 0) return { tree };
    const removed = tree.panes[idx];
    const panes = tree.panes.filter((_, i) => i !== idx);
    if (panes.length === 0) return { tree: null, removed };
    const active = Math.min(idx <= tree.active ? Math.max(0, tree.active - 1) : tree.active, panes.length - 1);
    const next: Node<P> = { ...tree, panes, active };
    return { tree: next, removed, focus: panes[active].id };
  }
  const ra = detachPane(tree.a, paneId);
  if (ra.removed) {
    if (ra.tree === null) return { tree: tree.b, removed: ra.removed, focus: firstPaneId(tree.b) };
    return { tree: { ...tree, a: ra.tree }, removed: ra.removed, focus: ra.focus };
  }
  const rb = detachPane(tree.b, paneId);
  if (rb.removed) {
    if (rb.tree === null) return { tree: tree.a, removed: rb.removed, focus: firstPaneId(tree.a) };
    return { tree: { ...tree, b: rb.tree }, removed: rb.removed, focus: rb.focus };
  }
  return { tree };
}

// closePane removes a pane (a tab). Its stack survives if other tabs remain, otherwise
// the parent split collapses. Closing the last pane in the whole tree yields a fresh
// single pane built from `freshData`. Returns the removed pane and a focus target (the
// natural neighbour).
export function closePane<P>(
  tree: Node<P>,
  paneId: string,
  freshData: () => P,
): { tree: Node<P>; removed?: Pane<P>; focus: string } {
  const { tree: detached, removed, focus } = detachPane(tree, paneId);
  if (!detached) {
    const fresh = newPane(freshData());
    return { tree: leaf(fresh), removed, focus: fresh.id };
  }
  return { tree: detached, removed, focus: focus ?? firstPaneId(detached) };
}

// movePane detaches a pane and re-inserts it as its own single-pane stack beside the
// stack containing `destId`, re-tiling the layout — the drag-to-an-edge gesture.
// `dir`/`before` place the moved pane on the chosen side (left/top ⇒ before). The moved
// pane keeps its payload. No-op if an id is missing or src === dest. New split ratio 0.5.
export function movePane<P>(
  tree: Node<P>,
  srcId: string,
  destId: string,
  dir: Dir,
  before: boolean,
): Node<P> {
  if (srcId === destId) return tree;
  const srcPane = findPane(tree, srcId);
  if (!srcPane || !findPane(tree, destId)) return tree;
  const { tree: detached } = detachPane(tree, srcId);
  if (!detached) return tree;
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.panes.some((p) => p.id === destId)) {
        const created = stack([srcPane], 0);
        const [a, b] = before ? [created, n] : [n, created];
        return { type: "split", id: uid(), dir, a, b, ratio: 0.5 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return walk(detached);
}

// addTab appends a fresh pane (from `data`) to the stack containing `targetId` and
// makes it the active tab. Returns the new pane id. No-op if the target is missing.
export function addTab<P>(tree: Node<P>, targetId: string, data: P): { tree: Node<P>; newPaneId: string } {
  const np = newPane(data);
  let added = false;
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.panes.some((p) => p.id === targetId)) {
        added = true;
        const panes = [...n.panes, np];
        return { ...n, panes, active: panes.length - 1 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  const out = walk(tree);
  return { tree: out, newPaneId: added ? np.id : "" };
}

// findStackById returns the stack node with the given id (whole-stack drag targets).
function findStackById<P>(tree: Node<P>, id: string): Extract<Node<P>, { type: "stack" }> | undefined {
  if (tree.type === "stack") return tree.id === id ? tree : undefined;
  return findStackById(tree.a, id) ?? findStackById(tree.b, id);
}

// detachStack removes a whole stack node, collapsing its parent split (promoting the
// sibling). Returns a null tree when it was the only stack.
function detachStack<P>(
  tree: Node<P>,
  stackId: string,
): { tree: Node<P> | null; removed?: Extract<Node<P>, { type: "stack" }> } {
  if (tree.type === "stack") {
    if (tree.id === stackId) return { tree: null, removed: tree };
    return { tree };
  }
  if (tree.a.type === "stack" && tree.a.id === stackId) return { tree: tree.b, removed: tree.a };
  if (tree.b.type === "stack" && tree.b.id === stackId) return { tree: tree.a, removed: tree.b };
  const ra = detachStack(tree.a, stackId);
  if (ra.removed) return { tree: ra.tree === null ? tree.b : { ...tree, a: ra.tree }, removed: ra.removed };
  const rb = detachStack(tree.b, stackId);
  if (rb.removed) return { tree: rb.tree === null ? tree.a : { ...tree, b: rb.tree }, removed: rb.removed };
  return { tree };
}

// moveStack detaches a whole stack (all its tabs) and re-inserts it beside the stack
// containing destPaneId — the drag-a-tile-to-an-edge gesture. The moved stack keeps its
// tabs and active tab. focus is "" (no change) when it is a no-op (missing ids or
// src === dest).
export function moveStack<P>(
  tree: Node<P>,
  srcStackId: string,
  destPaneId: string,
  dir: Dir,
  before: boolean,
): { tree: Node<P>; focus: string } {
  const src = findStackById(tree, srcStackId);
  const destStack = stackOf(tree, destPaneId);
  if (!src || !destStack || src.id === destStack.id) return { tree, focus: "" };
  const { tree: detached } = detachStack(tree, srcStackId);
  if (!detached) return { tree, focus: "" };
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.id === destStack.id) {
        const [a, b] = before ? [src, n] : [n, src];
        return { type: "split", id: uid(), dir, a, b, ratio: 0.5 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return { tree: walk(detached), focus: src.panes[src.active].id };
}

// stackStack merges all tabs of one stack into the stack containing destPaneId — the
// drag-a-tile-to-center gesture — removing the source stack. The moved stack's active
// tab becomes active in the merged stack. focus is "" on a no-op.
export function stackStack<P>(
  tree: Node<P>,
  srcStackId: string,
  destPaneId: string,
): { tree: Node<P>; focus: string } {
  const src = findStackById(tree, srcStackId);
  const destStack = stackOf(tree, destPaneId);
  if (!src || !destStack || src.id === destStack.id) return { tree, focus: "" };
  const { tree: detached } = detachStack(tree, srcStackId);
  if (!detached) return { tree, focus: "" };
  const activeIdx = destStack.panes.length + src.active;
  const merged = [...destStack.panes, ...src.panes];
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.id === destStack.id) return { ...n, panes: merged, active: activeIdx };
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return { tree: walk(detached), focus: src.panes[src.active].id };
}

// activatePane makes `paneId` the visible tab of its stack.
export function activatePane<P>(tree: Node<P>, paneId: string): Node<P> {
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      const idx = n.panes.findIndex((p) => p.id === paneId);
      if (idx >= 0 && idx !== n.active) return { ...n, active: idx };
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return walk(tree);
}

export function setRatio<P>(tree: Node<P>, splitId: string, ratio: number): Node<P> {
  const clamped = Math.min(MAX_RATIO, Math.max(MIN_RATIO, ratio));
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") return n;
    const next = { ...n, a: walk(n.a), b: walk(n.b) };
    if (n.id === splitId) next.ratio = clamped;
    return next;
  };
  return walk(tree);
}

// updatePane merges a patch into a pane's payload.
export function updatePane<P>(tree: Node<P>, paneId: string, patch: Partial<P>): Node<P> {
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.panes.some((p) => p.id === paneId)) {
        return {
          ...n,
          panes: n.panes.map((p) => (p.id === paneId ? { ...p, data: { ...p.data, ...patch } } : p)),
        };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return walk(tree);
}

export function allPanes<P>(tree: Node<P>): Pane<P>[] {
  return tree.type === "stack" ? tree.panes : [...allPanes(tree.a), ...allPanes(tree.b)];
}

export function findPane<P>(tree: Node<P>, id: string): Pane<P> | undefined {
  return allPanes(tree).find((p) => p.id === id);
}

// stackOf returns the stack node that holds the given pane.
export function stackOf<P>(tree: Node<P>, paneId: string): Extract<Node<P>, { type: "stack" }> | undefined {
  if (tree.type === "stack") return tree.panes.some((p) => p.id === paneId) ? tree : undefined;
  return stackOf(tree.a, paneId) ?? stackOf(tree.b, paneId);
}

// firstPaneId is the active pane of the first (leftmost/topmost) stack — a stable focus
// fallback.
export function firstPaneId<P>(tree: Node<P>): string {
  return tree.type === "stack" ? tree.panes[tree.active].id : firstPaneId(tree.a);
}

function isValidNode<P>(n: unknown, isValidData: (d: unknown) => boolean): n is Node<P> {
  if (!n || typeof n !== "object") return false;
  const node = n as Record<string, unknown>;
  if (node.type === "stack") {
    const panes = node.panes;
    if (!Array.isArray(panes) || panes.length === 0) return false;
    if (typeof node.active !== "number" || node.active < 0 || node.active >= panes.length) return false;
    return panes.every((p) => {
      const pane = p as Record<string, unknown> | undefined;
      return !!pane && typeof pane.id === "string" && isValidData(pane.data);
    });
  }
  if (node.type === "split") {
    return (
      (node.dir === "h" || node.dir === "v") &&
      isValidNode(node.a, isValidData) &&
      isValidNode(node.b, isValidData)
    );
  }
  return false;
}

export function loadLayout<P>(
  key: string,
  isValidData: (d: unknown) => boolean,
  fallback: () => LayoutState<P>,
): LayoutState<P> {
  try {
    const raw = globalThis.localStorage?.getItem(key);
    if (!raw) return fallback();
    const parsed = JSON.parse(raw) as LayoutState<P>;
    if (!parsed || !isValidNode<P>(parsed.tree, isValidData)) return fallback();
    const focused = findPane(parsed.tree, parsed.focused) ? parsed.focused : firstPaneId(parsed.tree);
    return { tree: parsed.tree, focused };
  } catch {
    return fallback();
  }
}

export function saveLayout<P>(key: string, state: LayoutState<P>): void {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify(state));
  } catch {
    // Ignore quota / unavailable storage; the workspace still works in-memory.
  }
}
