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

// The four sides of a tile, in the one spelling the whole workspace uses: the split-edge
// strips, the drop zones (DropZone is this plus "stack"), the pick overlay's arrows, and
// the directional keys that walk from tile to tile. One vocabulary, because a fifth
// spelling of "up" would be a fifth place for "top" and "up" to disagree.
export type Side = "left" | "right" | "top" | "bottom";

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

// How big the WORKSPACE is, in viewport extents: 1 means "exactly the screen", 1.667 means the
// tree is two thirds wider than the screen and the screen is a window onto it. This is the whole
// of the scrolling workspace in the model — every tile's size is still the tree's own ratios,
// read as fractions OF this rather than of the viewport.
//
// It has to be stored rather than derived. The ratios are relative by construction, so a tree
// alone cannot say whether it is one screen wide or five; nothing in the tree changes when the
// workspace grows, only what the same fractions are fractions of. See views/tiling/grow.ts, which
// owns every write to it, for why that is the seam.
export interface Ext {
  w: number;
  h: number;
}

// The workspace is never smaller than the screen: below 1 the tree would be laid out inside part
// of the viewport with the rest blank, which is not a layout anyone asked for.
export const MIN_EXT = 1;

export const UNIT_EXT: Ext = { w: MIN_EXT, h: MIN_EXT };

export function clampExt(ext: Ext): Ext {
  return { w: Math.max(MIN_EXT, ext.w), h: Math.max(MIN_EXT, ext.h) };
}

export interface LayoutState<P> {
  // focused is the id of the active pane of the focused stack.
  tree: Node<P>;
  focused: string;
  // How many viewports the tree occupies. Absent from layouts saved before the workspace could
  // scroll, and loadLayout reads those as UNIT_EXT — which is exactly what they were.
  ext: Ext;
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
// Exported for views/tiling/grow.ts, which needs the INTERMEDIATE tree. A move is a detach
// followed by an insert, and under the extending workspace those two halves have separate
// geometry — the workspace shrinks where the tile left and grows where it landed — so grow.ts
// has to fix up sizes between them. movePane below still composes them itself for callers that
// only want the structure.
export function detachPane<P>(
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
// insertBeside puts an already-built node next to the stack holding `destPaneId`, wrapping the
// two in a fresh split. It is the second half of every move — and, because it reports the id of
// the split it made, the hook the extending workspace needs: that split is the node whose size
// grew, and grow.ts walks up from it to keep every other tile where it was.
//
// The new split's ratio is 0.5 and is meant to be overwritten by the caller when the workspace
// extends; it is the right answer only for the dividing layout, which is the one that leaves it
// alone.
export function insertBeside<P>(
  tree: Node<P>,
  node: Node<P>,
  destPaneId: string,
  dir: Dir,
  before: boolean,
): { tree: Node<P>; splitId: string } {
  let splitId = "";
  const walk = (n: Node<P>): Node<P> => {
    if (n.type === "stack") {
      if (n.panes.some((p) => p.id === destPaneId)) {
        splitId = uid();
        const [a, b] = before ? [node, n] : [n, node];
        return { type: "split", id: splitId, dir, a, b, ratio: 0.5 };
      }
      return n;
    }
    return { ...n, a: walk(n.a), b: walk(n.b) };
  };
  return { tree: walk(tree), splitId };
}

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
  return insertBeside(detached, stack([srcPane], 0), destId, dir, before).tree;
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
export function findStackById<P>(
  tree: Node<P>,
  id: string,
): Extract<Node<P>, { type: "stack" }> | undefined {
  if (tree.type === "stack") return tree.id === id ? tree : undefined;
  return findStackById(tree.a, id) ?? findStackById(tree.b, id);
}

// detachStack removes a whole stack node, collapsing its parent split (promoting the
// sibling). Returns a null tree when it was the only stack.
// Exported alongside detachPane, and for the same reason.
export function detachStack<P>(
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
  const { tree: out } = insertBeside(detached, src, destPaneId, dir, before);
  return { tree: out, focus: src.panes[src.active].id };
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

// allStacks lists the TILES in document order — left before right, top before bottom,
// which is the order the layout reads in and therefore the order a rotation walks.
export function allStacks<P>(tree: Node<P>): Extract<Node<P>, { type: "stack" }>[] {
  return tree.type === "stack" ? [tree] : [...allStacks(tree.a), ...allStacks(tree.b)];
}

// A tile's place on screen, as fractions of the workspace: x/y are its top-left corner,
// w/h its size, all in 0..1. Derived from the tree's own split ratios, so it is the layout
// the CSS will produce rather than a measurement of it.
export interface TileRect {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// tileRects lays the tree out arithmetically. Deliberately NOT getBoundingClientRect on the
// rendered tiles: the numbers are wanted for "which tile is to my left", a question the
// model can answer exactly and a measurement can only approximate — dividers, borders and
// sub-pixel rounding all sit between the two. It also keeps the answer testable, since jsdom
// lays nothing out and would report every tile as a zero rect.
//
// The one thing this does not model is the divider's own width, which shifts each tile by a
// few px. Nothing here cares: every comparison below is between tiles, and a constant
// inset on both sides of a border cancels.
export function tileRects<P>(tree: Node<P>): TileRect[] {
  const out: TileRect[] = [];
  const walk = (n: Node<P>, x: number, y: number, w: number, h: number) => {
    if (n.type === "stack") {
      out.push({ id: n.id, x, y, w, h });
      return;
    }
    if (n.dir === "h") {
      const aw = w * n.ratio;
      walk(n.a, x, y, aw, h);
      walk(n.b, x + aw, y, w - aw, h);
    } else {
      const ah = h * n.ratio;
      walk(n.a, x, y, w, ah);
      walk(n.b, x, y + ah, w, h - ah);
    }
  };
  walk(tree, 0, 0, 1, 1);
  return out;
}

// A hair of slack for the fraction arithmetic above: two tiles that share a border have
// coordinates that agree to within rounding, and an exact `<=` would call that "not
// adjacent" as often as not.
const GEOM_EPS = 1e-9;

// neighborStack answers "which tile is immediately to the <side> of this one" — the step a
// direction key takes while choosing a pane, and the same question tmux's `select-pane -L`
// asks. A candidate must lie WHOLLY past the named edge and must overlap this tile's span
// on the other axis: a tile that is merely up-and-to-the-left is not "left", and pretending
// otherwise makes ← and ↑ interchangeable in exactly the layouts where they matter most.
// Nearest wins; ties (every directly-adjacent tile has gap 0) go to the one sharing the most
// border, and then to document order.
//
// It does NOT wrap. tmux does — its `window_pane_find_left` treats the leftmost pane's edge
// as the window's right — but tmux is moving the focus one pane at a time, where a wrap is a
// shortcut, while this is driving a highlight the user is watching travel. A ← at the left
// wall that lands the highlight on the far right of the screen reads as a bug, so it stops.
export function neighborStack<P>(
  tree: Node<P>,
  fromStackId: string,
  side: Side,
): Extract<Node<P>, { type: "stack" }> | undefined {
  const rects = tileRects(tree);
  const me = rects.find((r) => r.id === fromStackId);
  if (!me) return undefined;
  const horiz = side === "left" || side === "right";
  // How far past our edge a candidate begins. Negative means it has not cleared us.
  const gap = (r: TileRect) =>
    side === "left"
      ? me.x - (r.x + r.w)
      : side === "right"
        ? r.x - (me.x + me.w)
        : side === "top"
          ? me.y - (r.y + r.h)
          : r.y - (me.y + me.h);
  // How much of the perpendicular axis the two share.
  const overlap = (r: TileRect) =>
    horiz
      ? Math.min(r.y + r.h, me.y + me.h) - Math.max(r.y, me.y)
      : Math.min(r.x + r.w, me.x + me.w) - Math.max(r.x, me.x);
  // The overlap requirement is stated twice over — once as a filter, once as the tie-break —
  // and for a COMPLETE tiling, which is the only kind this tree can express, the second
  // subsumes the first: every side that is not a wall has a properly-adjacent tile at gap 0,
  // which outranks any corner-toucher on shared border. So the filter is a statement of the
  // rule rather than the thing enforcing it, and removing it changes no answer here. It
  // stays because the rule is not obvious from a sort comparator.
  const found = rects
    .filter((r) => r.id !== me.id && gap(r) >= -GEOM_EPS && overlap(r) > GEOM_EPS)
    .sort((a, b) => gap(a) - gap(b) || overlap(b) - overlap(a))[0];
  return found && findStackById(tree, found.id);
}

// rotateStacks cycles the tiles through the layout's slots — tmux's rotate-window, bound
// there to prefix C-o. The tree's SHAPE is untouched: every split keeps its direction and
// its ratio, so the screen holds its proportions while the contents move round it. A tile
// travels whole, tabs and all, because a slot holds a tile and not a pane. With two tiles
// this is a swap, which is the common case and the reason the direction rarely matters.
//
// `forward` sends each tile to the NEXT slot, so slot i receives what was in slot i-1 and
// the last wraps to the first.
//
// Note what this deliberately does NOT do: touch `focused`. The active pane travels with
// its tile, so the shell you were typing in is still the one you are typing in — it has
// merely moved. Moving the focus to whatever arrived in the old SLOT would put the keyboard
// in a different session, which is the one thing a rotation must not do.
export function rotateStacks<P>(tree: Node<P>, forward = true): Node<P> {
  const stacks = allStacks(tree);
  const n = stacks.length;
  if (n < 2) return tree;
  let slot = 0;
  const walk = (node: Node<P>): Node<P> => {
    if (node.type === "stack") {
      const from = (slot + (forward ? n - 1 : 1)) % n;
      slot++;
      return stacks[from];
    }
    // Property order is source order, so this visits a before b — the same walk allStacks
    // made, which is what keeps `slot` aligned with the list it indexes.
    return { ...node, a: walk(node.a), b: walk(node.b) };
  };
  return walk(tree);
}

export function allPanes<P>(tree: Node<P>): Pane<P>[] {
  return tree.type === "stack" ? tree.panes : [...allPanes(tree.a), ...allPanes(tree.b)];
}

export function findPane<P>(tree: Node<P>, id: string): Pane<P> | undefined {
  return allPanes(tree).find((p) => p.id === id);
}

// nextPaneId is the pane `delta` places after `fromId` in LAYOUT order, wrapping at either
// end — tmux's `prefix o`, select-pane -t :.+, and with delta -1 its Shift-flavoured twin.
//
// Layout order is allPanes' order, which is the order the pane NUMBERS are assigned in, so
// "the next pane" and "the next number" are the same step. Nothing else would do: a walk the
// user cannot predict is a walk they have to watch, and the numbers are already on the tabs
// and on the chooser's plates saying where it goes next.
//
// A STACKED TAB is a pane and takes its turn like any other — this steps through tabs as
// well as tiles, which is what separates it from rotateStacks (slots) and from the arrow
// walk in choose.ts (tiles). It is also the only keyboard route to a background tab that
// does not open the chooser first.
//
// An id that is not in the tree starts the walk at the first pane rather than throwing:
// findIndex returns -1, so a forward step wraps to 0. Focus that has fallen off the tree is
// recovered instead of propagated, the same way loadLayout recovers it with firstPaneId.
// That recovery is only meaningful forwards; a caller stepping BACKWARDS from an id it is
// not sure about should say what it wants to happen (choose.ts's cyclePane does).
//
// The modulo is doubled because JavaScript's `%` keeps the sign of its left operand, so the
// single one that sufficed while delta was always +1 returns a negative index the moment it
// is not.
export function nextPaneId<P>(tree: Node<P>, fromId: string, delta = 1): string {
  const panes = allPanes(tree);
  const at = panes.findIndex((p) => p.id === fromId);
  return panes[(((at + delta) % panes.length) + panes.length) % panes.length].id;
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

// readExt recovers the workspace extent from a stored blob. A layout saved before the workspace
// could scroll has no `ext` at all and reads as UNIT_EXT — which is not a fallback but the truth
// about it: it was laid out to fit the screen exactly. Anything non-finite (a hand-edited blob, a
// NaN that escaped an old divider drag) is treated the same way, because a NaN extent renders the
// workspace at zero size and there is no worse way to fail than an empty screen.
function readExt(raw: unknown): Ext {
  const e = raw as Partial<Ext> | undefined;
  if (!e || typeof e !== "object") return { ...UNIT_EXT };
  const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : MIN_EXT);
  return clampExt({ w: num(e.w), h: num(e.h) });
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
    return { tree: parsed.tree, focused, ext: readExt(parsed.ext) };
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
