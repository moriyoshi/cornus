// The EXTENDING workspace: a split does not divide what is on screen, it makes the workspace
// bigger and the screen becomes a window onto it.
//
// The dividing layout (layout.ts on its own) shares a fixed budget: every split is `ratio: 0.5`,
// so a tile costs its neighbour half its size and four splits in nothing is readable. niri and
// PaperWM answer that by letting the workspace exceed the display, and this is that answer for
// this tree.
//
// WHAT THIS MODULE IS FOR. There are two rules in it, and they answer different questions:
//
//   A SPLIT multiplies the workspace by the golden ratio along the axis being split. What was
//   already there shares two thirds of the result and keeps its PROPORTIONS exactly — every
//   existing tile scaled by the same ⅔φ — and the new tile takes the remaining third. See
//   GOLDEN below. Nothing keeps its pixels; everything keeps its relationships.
//
//   EVERY OTHER OPERATION — a close, a move, a divider, a border handle — leaves every tile
//   that was not its subject in the same absolute rectangle, except tiles facing the edge the
//   workspace grew at, which take the whole change. See absorb().
//
// The two used to be one: splits also worked the second way, and the source tile kept its exact
// size. That made a split a local event. It is now a change to the whole workspace, which is
// what "keep the ratios they occupy" asks for — you cannot both hold every existing pixel still
// and re-proportion the workspace around a new tile.
//
// Both are exactly what the tests check, tile by tile.
//
// The TREE IS UNCHANGED — still `split{dir,a,b,ratio}` over `stack{panes,active}`. Ratios stay
// relative; all that is added is `LayoutState.ext`, how many viewports the whole tree occupies.
// "Everything else keeps its size" is then not a new kind of node, it is arithmetic on the
// ratios: a tile keeps its size when the ratios above it are re-solved to say so.
//
// The shape of every operation here is the same four steps, and they are worth naming because
// the alternative — patching ratios in place, per operation — is where this goes wrong:
//
//   1. MEASURE the old tree into absolute sizes (nodeSizes).
//   2. Run the ordinary layout.ts reducer for the structural change.
//   3. Fix up LEAF sizes only: the new tile gets its extent, and perpendicular siblings absorb
//      the growth at their facing edge (propagate/absorb).
//   4. SETTLE the interior bottom-up and restate every ratio from the sizes (settle/rebuild).
//
// Step 4 is what makes step 3 safe: nothing here has to keep a split's own size correct by hand,
// because it is recomputed from the leaves at the end. That is also why `ext` is read back off
// the root rather than accumulated as `ext + delta` — if a clamp fires anywhere, the extent
// still describes the tree that was actually built.

import {
  allStacks,
  closePane,
  detachPane,
  detachStack,
  findPane,
  findStackById,
  insertBeside,
  movePane,
  moveStack,
  setRatio,
  splitPane,
  stack,
  stackOf,
  stackPane,
  stackStack,
  uid,
  clampExt,
  UNIT_EXT,
  type Dir,
  type Ext,
  type Node,
  type Pane,
} from "./layout";

// THE SIZING RULE for a split that extends the workspace, in three numbers.
//
//   1. The workspace grows by the GOLDEN RATIO along the axis being split: E' = φ·E.
//   2. Everything that is already there shares TWO THIRDS of E', keeping the proportions it
//      already had — so every existing tile scales by the same factor, ⅔φ ≈ 1.0787, and no two
//      tiles change their relationship to each other.
//   3. The new tile gets the rest, ⅓·E'.
//
// Note what this is NOT, because it replaced exactly that: the tile you split no longer keeps
// its size. Under the old rule the source stayed put and the new tile was ⅔ of it, which made
// the split a local event; under this one a split is a change to the whole workspace, and every
// tile grows a little to pay for it. The proportions are what is preserved now, not the pixels.
//
// φ rather than any other factor because it is the one that makes step 2 and step 3 agree with
// each other at every scale: the workspace and the space its existing tiles occupy stand in the
// same relation after the split as before.
export const GOLDEN = (1 + Math.sqrt(5)) / 2;

// The share of the grown workspace that the tiles already present divide between them. The new
// tile gets 1 - this.
export const EXISTING_SHARE = 2 / 3;

// What one split does to the workspace, as a factor: E' = GOLDEN · E. Kept as its own name
// because the tests and the docs both quote it.

// No tile may be shrunk below this many viewport extents. It exists for the shrink paths only:
// a close gives back exactly what the matching split took, but the two can be separated by any
// number of divider drags, so the amount coming back is not guaranteed to still be there.
const MIN_TILE = 0.05;

// The two axes, named as the keys of a size so the code can be written once and indexed.
// `Dir` is the SPLIT's direction: "h" places children left/right, so it divides the WIDTH.
type Axis = "w" | "h";
const AXIS: Record<Dir, Axis> = { h: "w", v: "h" };

export interface Size {
  w: number;
  h: number;
}
export type Sizes = Map<string, Size>;

// nodeSizes gives EVERY node — splits as well as tiles — its absolute extent in viewport units,
// by walking the ratios down from the root. This is tileRects' arithmetic (layout.ts) scaled by
// `ext` and kept for the interior nodes too, which is what the ancestor walk needs.
export function nodeSizes<P>(tree: Node<P>, ext: Ext): Sizes {
  const out: Sizes = new Map();
  const walk = (n: Node<P>, w: number, h: number) => {
    out.set(n.id, { w, h });
    if (n.type === "stack") return;
    if (n.dir === "h") {
      const aw = w * n.ratio;
      walk(n.a, aw, h);
      walk(n.b, w - aw, h);
    } else {
      const ah = h * n.ratio;
      walk(n.a, w, ah);
      walk(n.b, w, h - ah);
    }
  };
  walk(tree, ext.w, ext.h);
  return out;
}

// pathTo is the chain of nodes from the root down to `id`, inclusive; empty when it is not in
// the tree. Identity-comparable — the entries ARE the nodes — which is how the walk below knows
// which child it came through without matching ids twice.
function pathTo<P>(tree: Node<P>, id: string): Node<P>[] {
  if (tree.id === id) return [tree];
  if (tree.type === "stack") return [];
  const a = pathTo(tree.a, id);
  if (a.length) return [tree, ...a];
  const b = pathTo(tree.b, id);
  if (b.length) return [tree, ...b];
  return [];
}

// absorb hands `delta` along `axis` to the ONE descendant of this subtree that faces the edge
// the workspace grew at — the "tiles facing the extending edge expand" rule, and the reason a
// vertical split does not stretch every tile in the neighbouring column proportionally.
//
// Only LEAVES are written. The interior is recomputed by settle() afterwards, so this cannot
// leave a split claiming a size its children disagree with.
//
// A split ACROSS the axis has both children spanning the whole of it, so both must absorb — not
// one of them. Getting this backwards produces a layout that is geometrically valid and visibly
// torn, which is why the tests assert rectangles rather than extents.
function absorb<P>(node: Node<P>, sizes: Sizes, axis: Axis, delta: number, before: boolean): void {
  if (node.type === "stack") {
    const s = sizes.get(node.id);
    if (s) s[axis] = Math.max(MIN_TILE, s[axis] + delta);
    return;
  }
  if (AXIS[node.dir] === axis) {
    absorb(before ? node.a : node.b, sizes, axis, delta, before);
    return;
  }
  absorb(node.a, sizes, axis, delta, before);
  absorb(node.b, sizes, axis, delta, before);
}

// propagate walks from the root down to the node whose subtree just changed size by `delta`
// along `axis`, and asks each ancestor the one question that matters: does your split run ALONG
// this axis or ACROSS it?
//
//   ALONG  — you divide this axis, so the sibling simply keeps its size and your own size grows.
//            Nothing to do: settle() will add the children up.
//   ACROSS — both your children span the whole of this axis, so the sibling is now short by
//            `delta` and has to absorb it, or the two halves of your split disagree about how
//            tall they are.
//
// The anchor itself is untouched: its size was set by the caller, which is the only place that
// knows whether the node is new, moved, or resized.
function propagate<P>(
  tree: Node<P>,
  sizes: Sizes,
  anchorId: string,
  axis: Axis,
  delta: number,
  before: boolean,
): void {
  const path = pathTo(tree, anchorId);
  for (let i = 0; i < path.length - 1; i++) {
    const n = path[i];
    if (n.type !== "split") break; // unreachable: only a split has children to descend through
    if (AXIS[n.dir] === axis) continue;
    absorb(path[i + 1] === n.a ? n.b : n.a, sizes, axis, delta, before);
  }
}

// settle recomputes every SPLIT's size from its children, bottom-up, and returns the root's —
// which is the workspace extent. Leaves are the source of truth after a mutation; this is what
// makes the interior agree with them.
//
// The cross axis takes `max` rather than either child: siblings normally agree exactly (absorb
// is what keeps them agreeing), and when a clamp has made them disagree, the larger one is the
// box and the shorter renders stretched into it. That degrades to a slightly-too-tall tile
// instead of to a gap.
function settle<P>(node: Node<P>, sizes: Sizes): Size {
  if (node.type === "stack") return sizes.get(node.id) ?? { w: MIN_TILE, h: MIN_TILE };
  const a = settle(node.a, sizes);
  const b = settle(node.b, sizes);
  const size: Size =
    node.dir === "h"
      ? { w: a.w + b.w, h: Math.max(a.h, b.h) }
      : { w: Math.max(a.w, b.w), h: a.h + b.h };
  sizes.set(node.id, size);
  return size;
}

// rebuild restates every split's ratio from the settled sizes. This is the step that turns "who
// is how big" back into the only thing the tree actually stores.
function rebuild<P>(node: Node<P>, sizes: Sizes): Node<P> {
  if (node.type === "stack") return node;
  const a = rebuild(node.a, sizes);
  const b = rebuild(node.b, sizes);
  const axis = AXIS[node.dir];
  const sa = sizes.get(node.a.id)?.[axis] ?? 0;
  const sb = sizes.get(node.b.id)?.[axis] ?? 0;
  const total = sa + sb;
  return { ...node, a, b, ratio: total > 0 ? sa / total : node.ratio };
}

// finish is steps 4 and 5 of every operation: settle the interior, read the extent off the root,
// and restate the ratios. One function so no operation can do half of it.
function finish<P>(tree: Node<P>, sizes: Sizes): { tree: Node<P>; ext: Ext } {
  const root = settle(tree, sizes);
  return { tree: rebuild(tree, sizes), ext: clampExt({ w: root.w, h: root.h }) };
}

// growInto sizes the two halves of a split that was JUST created — by splitPane or by
// insertBeside, which agree on the convention that `before` puts the newcomer at `a`.
//
// The newcomer gets `along` on the split's axis and the incumbent's extent on the other, because
// siblings of a split share the cross axis by construction. That is also the one thing a MOVED
// tile cannot carry with it: it keeps the extent it had along the axis it is being placed on,
// and adopts its new neighbour's on the other.
function growInto<P>(
  tree: Node<P>,
  sizes: Sizes,
  splitId: string,
  axis: Axis,
  along: number,
  before: boolean,
): void {
  const path = pathTo(tree, splitId);
  const split = path[path.length - 1];
  if (!split || split.type !== "split") return;
  const created = before ? split.a : split.b;
  const kept = before ? split.b : split.a;
  const keptSize = sizes.get(kept.id);
  if (!keptSize) return;
  sizes.set(
    created.id,
    axis === "w" ? { w: along, h: keptSize.h } : { w: keptSize.w, h: along },
  );
  propagate(tree, sizes, split.id, axis, along, before);
}

// shrinkAway is the mirror image: a tile has left the tree, its parent split has collapsed, and
// the workspace gives back exactly the extent that tile occupied. The promoted sibling keeps its
// own size — it does NOT inherit the space, which is what separates this from the dividing
// layout, where closing a pane is how you get a bigger one.
function shrinkAway<P>(
  oldTree: Node<P>,
  next: Node<P>,
  sizes: Sizes,
  removedId: string,
): void {
  const path = pathTo(oldTree, removedId);
  const parent = path[path.length - 2];
  if (!parent || parent.type !== "split") return; // it was the whole tree; nothing to give back
  const removedWasA = parent.a.id === removedId;
  const sibling = removedWasA ? parent.b : parent.a;
  const axis = AXIS[parent.dir];
  const delta = -(sizes.get(removedId)?.[axis] ?? 0);
  propagate(next, sizes, sibling.id, axis, delta, removedWasA);
}

// ---------------------------------------------------------------------------------------
// The operations, one per layout.ts reducer that changes how many tiles there are
// ---------------------------------------------------------------------------------------

export interface Extended<P> {
  tree: Node<P>;
  ext: Ext;
}

// splitExtending is `prefix %` and the edge-split gesture under the extending layout: the tile
// keeps its size, the workspace grows by ⅔ of it (or by the floor, whichever is larger), and the
// new tile is exactly that much.
//
// Where the floor is NOT binding the ratio is always 0.6 — W / (W + ⅔W), which cancels — so the
// rule is scale-free and nothing has to special-case a tile that is merely small. Where it IS
// binding the ratio is W / (W + floor) and varies, which is the point: below a certain size,
// "proportional" stops being the right answer and "readable" takes over.
export function splitExtending<P>(
  tree: Node<P>,
  ext: Ext,
  targetPaneId: string,
  dir: Dir,
  makeData: (from: Pane<P>) => P,
  before: boolean,
  floor = 0,
): { tree: Node<P>; ext: Ext; newPaneId: string } {
  const host = stackOf(tree, targetPaneId);
  const base = splitPane(tree, targetPaneId, dir, makeData, before);
  if (!host || !base.newPaneId) return { ...base, ext };

  const sizes = nodeSizes(tree, ext);
  const axis = AXIS[dir];
  const cross: Axis = axis === "w" ? "h" : "w";
  const was = ext[axis];
  if (!(was > 0)) return { ...base, ext };

  // The three numbers. The workspace grows by φ; what is already here shares two thirds of that
  // and keeps its proportions exactly, because every tile is scaled by the SAME factor; the new
  // tile takes the rest.
  const grown = was * GOLDEN;
  const scale = (EXISTING_SHARE * grown) / was; // = ⅔φ ≈ 1.0787, independent of `was`
  // The floor still wins where it binds. It is the reason this split is extending rather than
  // halving, so delivering a tile under it would defeat the gate that chose this path — and an
  // unmeasured screen reads as no floor, not as an infinitely wide tile.
  const min = Number.isFinite(floor) ? floor : 0;
  const made = Math.max(grown - EXISTING_SHARE * grown, min);

  // Every tile that was already here, scaled along the axis. One factor for all of them is the
  // whole of "keeping the ratios they occupy": no two tiles change their relationship, so
  // nothing in the tree below the root has to be reasoned about individually.
  for (const st of allStacks(tree)) {
    const s = sizes.get(st.id);
    if (s) s[axis] *= scale;
  }

  // The split splitPane just made is the host tile's new parent; `before` decides which child
  // is the newcomer, the same convention insertBeside uses.
  const path = pathTo(base.tree, host.id);
  const split = path[path.length - 2];
  if (!split || split.type !== "split") return { ...base, ext };
  const created = before ? split.a : split.b;
  const hostSize = sizes.get(host.id);
  if (!hostSize) return { ...base, ext };
  sizes.set(
    created.id,
    axis === "w" ? { w: made, h: hostSize[cross] } : { w: hostSize[cross], h: made },
  );

  return { ...finish(base.tree, sizes), newPaneId: base.newPaneId };
}

// closeExtending shrinks the workspace by the extent of the tile that left. A tile with other
// tabs still open does not leave, so nothing moves — closing a TAB is not closing a tile.
export function closeExtending<P>(
  tree: Node<P>,
  ext: Ext,
  paneId: string,
  freshData: () => P,
): { tree: Node<P>; ext: Ext; removed?: Pane<P>; focus: string } {
  const host = stackOf(tree, paneId);
  const sizes = nodeSizes(tree, ext);
  const base = closePane(tree, paneId, freshData);
  if (!host || host.panes.length > 1) return { ...base, ext };
  // The last tile in the tree: closePane replaced it with a fresh one, and a one-tile workspace
  // is one screen by definition.
  if (pathTo(tree, host.id).length < 2) return { ...base, ext: { ...UNIT_EXT } };
  shrinkAway(tree, base.tree, sizes, host.id);
  // ONE TILE LEFT MEANS ONE SCREEN. Under the golden rule every split scales the tiles that
  // were already there, so closing back down does not undo that scaling and the last survivor
  // would be left larger than the viewport — a single pane you have to scroll to see all of,
  // which is not a layout anyone asked for. Stated here rather than in shrinkAway because it is
  // about the workspace being empty of structure, not about the arithmetic of one removal.
  if (base.tree.type === "stack") return { ...base, tree: base.tree, ext: { ...UNIT_EXT } };
  return { ...base, ...finish(base.tree, sizes) };
}

// movePaneExtending is the drag-a-tab-to-an-edge gesture: the pane leaves its tile (which
// collapses if it was the only tab, shrinking the workspace) and lands as a new tile beside the
// destination, growing it back by the extent it carried.
export function movePaneExtending<P>(
  tree: Node<P>,
  ext: Ext,
  srcId: string,
  destId: string,
  dir: Dir,
  before: boolean,
): Extended<P> {
  if (srcId === destId) return { tree, ext };
  const srcPane = findPane(tree, srcId);
  const host = stackOf(tree, srcId);
  if (!srcPane || !host || !findPane(tree, destId)) return { tree, ext };

  const sizes = nodeSizes(tree, ext);
  const axis = AXIS[dir];
  const carried = sizes.get(host.id)?.[axis] ?? 0;

  const { tree: detached } = detachPane(tree, srcId);
  if (!detached) return { tree, ext };
  if (host.panes.length === 1) shrinkAway(tree, detached, sizes, host.id);

  // The destination survived the detach by construction (a stack holding both panes keeps the
  // one that stayed), so this is a "cannot happen" guard rather than a fallback — and the safe
  // answer to a layout half-built is the one that was there before.
  const { tree: out, splitId } = insertBeside(detached, stack([srcPane], 0), destId, dir, before);
  if (!splitId) return { tree, ext };
  growInto(out, sizes, splitId, axis, carried, before);
  return finish(out, sizes);
}

// moveStackExtending is the same gesture with a whole tile — all its tabs — in hand.
export function moveStackExtending<P>(
  tree: Node<P>,
  ext: Ext,
  srcStackId: string,
  destPaneId: string,
  dir: Dir,
  before: boolean,
): { tree: Node<P>; ext: Ext; focus: string } {
  const src = findStackById(tree, srcStackId);
  const dest = stackOf(tree, destPaneId);
  if (!src || !dest || src.id === dest.id) return { tree, ext, focus: "" };

  const sizes = nodeSizes(tree, ext);
  const axis = AXIS[dir];
  const carried = sizes.get(src.id)?.[axis] ?? 0;

  const { tree: detached } = detachStack(tree, srcStackId);
  if (!detached) return { tree, ext, focus: "" };
  shrinkAway(tree, detached, sizes, src.id);

  const { tree: out, splitId } = insertBeside(detached, src, destPaneId, dir, before);
  if (!splitId) return { tree, ext, focus: "" };
  growInto(out, sizes, splitId, axis, carried, before);
  return { ...finish(out, sizes), focus: src.panes[src.active].id };
}

// stackPaneExtending / stackStackExtending are the drag-onto-the-CENTRE gestures. They add no
// tile — the pane becomes a tab on one that already exists — so the only geometry is the source
// tile disappearing, and the workspace shrinking by what it occupied.
export function stackPaneExtending<P>(
  tree: Node<P>,
  ext: Ext,
  srcId: string,
  destId: string,
): Extended<P> {
  const host = stackOf(tree, srcId);
  const out = stackPane(tree, srcId, destId);
  if (!host || host.panes.length > 1 || out === tree) return { tree: out, ext };
  const sizes = nodeSizes(tree, ext);
  shrinkAway(tree, out, sizes, host.id);
  return finish(out, sizes);
}

export function stackStackExtending<P>(
  tree: Node<P>,
  ext: Ext,
  srcStackId: string,
  destPaneId: string,
): { tree: Node<P>; ext: Ext; focus: string } {
  const src = findStackById(tree, srcStackId);
  const base = stackStack(tree, srcStackId, destPaneId);
  if (!src || !base.focus) return { ...base, ext };
  const sizes = nodeSizes(tree, ext);
  shrinkAway(tree, base.tree, sizes, src.id);
  return { ...base, ...finish(base.tree, sizes) };
}

// resizeExtending is the Alt-dragged divider and the Grow / Shrink pane commands: one child of a
// split changes size by `delta` viewport units and the OTHER ONE DOES NOT, so the workspace
// grows or shrinks to suit. That is the whole difference from setRatio, which trades extent
// between the two and leaves the workspace alone.
//
// The child's own subtree is left alone and therefore scales with it: rebuild reads its interior
// ratios off sizes that all moved together, so they come out unchanged. A pane inside the child
// keeps its share of the child, which is what a divider drag has always meant.
//
// The growth lands at the FAR edge whichever child is resized, because that is where it lands:
// the split's near edge is pinned by everything before it, so the content after the divider is
// what moves. Hence `before: false` in the propagate below — the perpendicular siblings' far
// edge is the one that has to keep up.
export function resizeExtending<P>(
  tree: Node<P>,
  ext: Ext,
  splitId: string,
  growA: boolean,
  delta: number,
): Extended<P> {
  const path = pathTo(tree, splitId);
  const split = path[path.length - 1];
  if (!split || split.type !== "split") return { tree, ext };
  const sizes = nodeSizes(tree, ext);
  const child = growA ? split.a : split.b;
  const axis = AXIS[split.dir];
  const current = sizes.get(child.id)?.[axis] ?? 0;
  const applied = Math.max(MIN_TILE, current + delta) - current;
  if (applied === 0) return { tree, ext };
  scaleSubtree(child, sizes, axis, (current + applied) / current);
  propagate(tree, sizes, child.id, axis, applied, false);
  return finish(tree, sizes);
}

// evenHorizontal is tmux's `select-layout even-horizontal`: every tile in ONE ROW, all the same
// width. It is the way out of a layout that has grown lopsided — three splits deep on one side
// and nothing on the other — without closing anything.
//
// The TILES ARE REUSED, not rebuilt: `allStacks` returns the existing stack nodes and they are
// re-parented into a fresh chain, so every tab, every active index and every live terminal
// travels with its tile. Only the shape around them changes. That is the same promise
// rotateStacks makes, and the reason both can be run on a workspace full of running shells.
//
// `minWidth` is the floor from the extending layout, in viewport extents. Where the panes would
// otherwise be too narrow to read, the row is built at the floor and the WORKSPACE gets wider
// instead — which is the whole bargain of this layout applied to an arrangement rather than to a
// split. Pass 0 (or leave the viewport unmeasured) to get the classic behaviour: exactly one
// screen, divided N ways.
export function evenHorizontal<P>(tree: Node<P>, ext: Ext, minWidth = 0): Extended<P> {
  const stacks = allStacks(tree);
  const n = stacks.length;
  if (n === 0) return { tree, ext };
  // Infinity means the screen has never been measured, and the honest reading of that is "no
  // floor" — fit the row to the viewport — rather than an infinitely wide column.
  const floor = Number.isFinite(minWidth) ? minWidth : 0;
  const width = Math.max(1 / n, floor, MIN_TILE);

  // A right-leaning chain. The SHAPE is arbitrary — any binary tree over the same leaves in the
  // same order renders as the same row once the ratios are solved — so the simplest one wins.
  let out: Node<P> = stacks[n - 1];
  for (let i = n - 2; i >= 0; i--) {
    out = { type: "split", id: uid(), dir: "h", a: stacks[i], b: out, ratio: 0.5 };
  }
  // One row means nothing is stacked vertically, so the workspace is exactly one screen tall
  // again however many vertical splits it had before.
  const sizes: Sizes = new Map();
  for (const s of stacks) sizes.set(s.id, { w: width, h: 1 });
  return finish(out, sizes);
}

// resizeEdge drags the workspace's OWN outer edge — the right one for dir "h", the bottom one
// for dir "v" — growing or shrinking the whole workspace by `delta` viewport extents.
//
// Every other divider in the tiling sits BETWEEN two tiles and trades between them. These two sit
// at the boundary, where there is nothing on the far side to trade with, so what they move is the
// workspace itself. Which tiles come with it is not a new rule: it is `absorb`, the same one a
// split uses when the workspace grows and the tiles facing the new edge stretch to meet it. So
// dragging the right edge widens exactly the tiles that touch the right edge, and a tile two
// columns in does not move at all.
//
// The far side is not a parameter. A near-edge equivalent would move the workspace's origin, and
// the origin is the one coordinate this layout does not have — everything is laid out from 0, so
// "extend to the left" is always "insert, and shift what follows".
export function resizeEdge<P>(tree: Node<P>, ext: Ext, dir: Dir, delta: number): Extended<P> {
  const sizes = nodeSizes(tree, ext);
  absorb(tree, sizes, AXIS[dir], delta, false);
  return finish(tree, sizes);
}

// scaleSubtree multiplies every LEAF in a subtree along one axis, which is how a resized child
// keeps its internal proportions. Scaling the leaves rather than assigning the subtree a size is
// what lets settle() stay the only thing that computes an interior node's extent.
function scaleSubtree<P>(node: Node<P>, sizes: Sizes, axis: Axis, factor: number): void {
  if (!Number.isFinite(factor) || factor <= 0) return;
  if (node.type === "stack") {
    const s = sizes.get(node.id);
    if (s) s[axis] = Math.max(MIN_TILE, s[axis] * factor);
    return;
  }
  scaleSubtree(node.a, sizes, axis, factor);
  scaleSubtree(node.b, sizes, axis, factor);
}

// extentOf answers "how big is this tile, in viewport extents" — what the Grow / Shrink commands
// need in order to step by a fraction of a tile rather than by a fixed number of pixels.
export function extentOf<P>(tree: Node<P>, ext: Ext, nodeId: string, dir: Dir): number {
  return nodeSizes(tree, ext).get(nodeId)?.[AXIS[dir]] ?? 0;
}

// How much one press of Grow / Shrink pane moves, in viewport extents. A twentieth of the screen:
// big enough to be worth a keystroke, small enough that overshooting costs one press back.
export const RESIZE_STEP = 0.05;

// resizeTargetOf answers "which divider does resizing THIS tile along THIS axis actually move?".
// A tile's own parent may divide the other axis — a tile in a column has a horizontal divider
// above it and a vertical one somewhere further up — so the answer is the nearest ancestor split
// that runs along the axis asked about, plus which side of it the tile is on.
//
// Undefined when there is none, which is the honest answer for "make this wider" in a workspace
// with no vertical divider anywhere: there is nothing to move. The command reads this to say so
// rather than to silently do nothing.
export function resizeTargetOf<P>(
  tree: Node<P>,
  paneId: string,
  dir: Dir,
): { splitId: string; growA: boolean } | undefined {
  const host = stackOf(tree, paneId);
  if (!host) return undefined;
  const path = pathTo(tree, host.id);
  for (let i = path.length - 2; i >= 0; i--) {
    const n = path[i];
    if (n.type !== "split" || AXIS[n.dir] !== AXIS[dir]) continue;
    return { splitId: n.id, growA: path[i + 1] === n.a };
  }
  return undefined;
}

// resizePane is the Grow / Shrink pane commands, in both layouts. Under the extending workspace
// the tile grows and the workspace grows with it; under the dividing one the sibling pays, which
// is setRatio — the same command meaning the same thing to the user either way, since "make this
// bigger" is a sentence about the pane and not about the workspace.
export function resizePane<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  paneId: string,
  dir: Dir,
  delta: number,
): Extended<P> {
  const target = resizeTargetOf(tree, paneId, dir);
  if (!target) return { tree, ext };
  if (extending) return resizeExtending(tree, ext, target.splitId, target.growA, delta);
  const sizes = nodeSizes(tree, ext);
  const split = pathTo(tree, target.splitId).pop();
  if (!split || split.type !== "split") return { tree, ext };
  const axis = AXIS[split.dir];
  const sa = sizes.get(split.a.id)?.[axis] ?? 0;
  const sb = sizes.get(split.b.id)?.[axis] ?? 0;
  const total = sa + sb;
  if (total <= 0) return { tree, ext };
  return { tree: setRatio(tree, split.id, (target.growA ? sa + delta : sa - delta) / total), ext };
}

// ---------------------------------------------------------------------------------------
// Choosing between the two layouts
// ---------------------------------------------------------------------------------------
//
// One dispatcher per operation that changes the tiling, each taking `extending` as a plain
// boolean rather than reading the setting itself — which is what keeps this module a pile of
// arithmetic that a test can drive both ways without touching localStorage.
//
// The DIVIDING branch is layout.ts called directly, with `ext` passed straight back out. Not
// "the extending path with the growth set to zero": the whole value of a setting whose off state
// is the old behaviour is that the old behaviour is still the old CODE, so a regression in here
// cannot reach someone who turned this off.

// The smallest a pane should be made, in rem. A split that would go below one of these is what
// the extending workspace is FOR; a split that stays above it does not need the workspace to
// grow, and growing it anyway would put a scrollbar on a screen with room to spare.
//
// In rem rather than px because these are legibility floors — how much of a listing, a file, or
// a terminal is still worth having — and that scales with the user's text size.
//
// TWO CONSTANTS, because a pane is not square and neither is the screen. A single 40rem floor on
// both axes was measured across three viewports and made every vertical split extend on anything
// below a 1440p display: half of any ordinary screen height is under 640px, so the condition was
// vacuously true and the height axis never used the dividing layout at all.
//
//   WIDTH 40rem (640px) — a file listing with its columns intact, or an 80-column terminal with
//   room for the tab bar and the breadcrumb. Below this a pane stops being worth reading, which
//   is the whole premise.
//
//   HEIGHT 20rem (320px) — about eighteen terminal rows once the tab bar is taken off. Half of
//   what the width asks for, deliberately: text is wide and screens are wider than they are
//   tall, so the same number on both axes is the same rule stated twice about different things.
//   It is set so that ONE vertical split is free on a laptop (a 732px body halves to 366px, over
//   the floor) and the second is not (183px, well under) — which is the behaviour the horizontal
//   axis already has on a wide screen.
export const MIN_PANE_WIDTH_REM = 40;
export const MIN_PANE_HEIGHT_REM = 20;

// minPaneRem picks the floor for a split direction. `Dir` is the SPLIT's axis — "h" places
// children left and right, so it is the one constrained by WIDTH — and getting that backwards is
// exactly the kind of thing that reads correctly and behaves inside out, hence the one function
// both the host and its tests go through.
export function minPaneRem(dir: Dir): number {
  return dir === "h" ? MIN_PANE_WIDTH_REM : MIN_PANE_HEIGHT_REM;
}

// applySplit decides, per split, which of the two layouts to use.
//
// `floor` is the smallest a pane may be along this axis, in VIEWPORT EXTENTS — the same units
// the model works in — because this module measures nothing; the host converts rem to a
// fraction of its own scroll container (see floorFor in Workspace.tsx). Infinity means "the
// viewport has never been measured", and the answer to an unknown screen size is to extend,
// which is the behaviour that cannot make a pane too small to use.
//
// The test is on what DIVIDING would produce. Splitting a tile of extent W in half gives each
// side W/2, so W/2 below the floor is the case where halving is the wrong answer — and where
// this feature earns its keep. Above it, both halves are comfortable and the ordinary 50/50
// split is what the user gets: on a wide screen the workspace does not start scrolling until
// there is a real reason to.
export function applySplit<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  paneId: string,
  dir: Dir,
  makeData: (from: Pane<P>) => P,
  before: boolean,
  floor = Infinity,
): { tree: Node<P>; ext: Ext; newPaneId: string } {
  if (!extending || !crowds(tree, ext, paneId, dir, floor)) {
    return { ...splitPane(tree, paneId, dir, makeData, before), ext };
  }
  return splitExtending(tree, ext, paneId, dir, makeData, before, floor);
}

// crowds answers "would halving this tile put its halves under the floor?". Exported so the
// host can say which way a split is about to go before making it.
export function crowds<P>(
  tree: Node<P>,
  ext: Ext,
  paneId: string,
  dir: Dir,
  floor: number,
): boolean {
  const host = stackOf(tree, paneId);
  if (!host) return false;
  const w = nodeSizes(tree, ext).get(host.id)?.[AXIS[dir]] ?? 0;
  return w / 2 < floor;
}

export function applyClose<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  paneId: string,
  freshData: () => P,
): { tree: Node<P>; ext: Ext; removed?: Pane<P>; focus: string } {
  if (!extending) return { ...closePane(tree, paneId, freshData), ext };
  return closeExtending(tree, ext, paneId, freshData);
}

// A MOVE asks the floor the same question a split does, and asks it of the DESTINATION: that is
// the tile being divided to make room, so it is the one whose halves have to stay usable. The
// source is not consulted — a tile leaving cannot make anything too small.
//
// Under the dividing branch the workspace does not change size at all: layout.ts re-tiles, the
// vacated space goes to the source tile's sibling, and the destination halves. That is the same
// bargain applySplit strikes — the off path is the original code, not a re-implementation of it.
export function applyMovePane<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  srcId: string,
  destId: string,
  dir: Dir,
  before: boolean,
  floor = Infinity,
): Extended<P> {
  if (!extending || !crowds(tree, ext, destId, dir, floor)) {
    return { tree: movePane(tree, srcId, destId, dir, before), ext };
  }
  return movePaneExtending(tree, ext, srcId, destId, dir, before);
}

export function applyMoveStack<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  srcStackId: string,
  destPaneId: string,
  dir: Dir,
  before: boolean,
  floor = Infinity,
): { tree: Node<P>; ext: Ext; focus: string } {
  if (!extending || !crowds(tree, ext, destPaneId, dir, floor)) {
    return { ...moveStack(tree, srcStackId, destPaneId, dir, before), ext };
  }
  return moveStackExtending(tree, ext, srcStackId, destPaneId, dir, before);
}

export function applyStackPane<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  srcId: string,
  destId: string,
): Extended<P> {
  if (!extending) return { tree: stackPane(tree, srcId, destId), ext };
  return stackPaneExtending(tree, ext, srcId, destId);
}

export function applyStackStack<P>(
  extending: boolean,
  tree: Node<P>,
  ext: Ext,
  srcStackId: string,
  destPaneId: string,
): { tree: Node<P>; ext: Ext; focus: string } {
  if (!extending) return { ...stackStack(tree, srcStackId, destPaneId), ext };
  return stackStackExtending(tree, ext, srcStackId, destPaneId);
}
