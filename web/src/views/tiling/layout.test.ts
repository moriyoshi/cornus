import { describe, it, expect, beforeEach } from "vitest";
import {
  splitPane,
  closePane,
  stackPane,
  movePane,
  moveStack,
  stackStack,
  activatePane,
  addTab,
  setRatio,
  updatePane,
  allPanes,
  allStacks,
  rotateStacks,
  tileRects,
  neighborStack,
  findPane,
  nextPaneId,
  stackOf,
  firstPaneId,
  loadLayout,
  saveLayout,
  leaf,
  stack,
  newPane,
  type LayoutState,
  type Node,
} from "./layout";

// The generic tiling tree, exercised with a tiny string-payload so the pure reducers
// are tested independently of any view. Leaves are stacks (tabs); the drag-onto-center
// gesture stacks rather than swaps.

type P = { label: string };
const KEY = "cornus.test.layout";
const pane = (label = "") => newPane<P>({ label });
const inherit = (from: { data: P }): P => ({ ...from.data });
const fresh = (): P => ({ label: "" });
const isValidData = (d: unknown): boolean =>
  !!d && typeof d === "object" && typeof (d as { label?: unknown }).label === "string";

type Stack = Extract<Node<P>, { type: "stack" }>;
type Split = Extract<Node<P>, { type: "split" }>;

function single(label = ""): LayoutState<P> {
  const p = pane(label);
  return { tree: leaf(p), focused: p.id };
}

describe("tiling layout reducer", () => {
  it("splits a stack into a split, keeping the original tile as `a`", () => {
    const s = single();
    const origId = firstPaneId(s.tree);
    const { tree, newPaneId } = splitPane(s.tree, origId, "h", inherit);
    expect(tree.type).toBe("split");
    if (tree.type !== "split") throw new Error("expected split");
    expect(tree.dir).toBe("h");
    expect(tree.ratio).toBeCloseTo(0.5);
    expect((tree.a as Stack).panes[0].id).toBe(origId);
    expect((tree.b as Stack).panes[0].id).toBe(newPaneId);
    expect(allPanes(tree)).toHaveLength(2);
  });

  it("the new pane's payload comes from makeData (inherit vs fresh)", () => {
    const tree = leaf(pane("src"));
    const origId = firstPaneId(tree);
    const inh = splitPane(tree, origId, "v", inherit);
    expect(findPane(inh.tree, inh.newPaneId)!.data.label).toBe("src");
    const blank = splitPane(tree, origId, "v", fresh);
    expect(findPane(blank.tree, blank.newPaneId)!.data.label).toBe("");
  });

  it("places the new tile before or after the target per the edge clicked", () => {
    const tree = leaf(pane());
    const origId = firstPaneId(tree);
    const after = splitPane(tree, origId, "h", inherit).tree as Split;
    expect((after.a as Stack).panes[0].id).toBe(origId);
    const bef = splitPane(tree, origId, "v", inherit, true);
    const bs = bef.tree as Split;
    expect((bs.a as Stack).panes[0].id).toBe(bef.newPaneId);
    expect((bs.b as Stack).panes[0].id).toBe(origId);
  });

  it("stacks a pane onto another (tabs), removing it from its old tile", () => {
    let tree: Node<P> = leaf(pane());
    const aId = firstPaneId(tree);
    const r = splitPane(tree, aId, "h", fresh); // a | b
    tree = r.tree;
    const bId = r.newPaneId;
    tree = updatePane(tree, aId, { label: "AAA" });
    tree = updatePane(tree, bId, { label: "BBB" });

    // Drag a onto b's center -> the split collapses to a single stack [b, a], a active.
    const stacked = stackPane(tree, aId, bId);
    expect(stacked.type).toBe("stack");
    const st = stacked as Stack;
    expect(st.panes.map((p) => p.id)).toEqual([bId, aId]);
    expect(st.panes[st.active].id).toBe(aId); // the moved pane becomes active
    expect(allPanes(stacked)).toHaveLength(2);
  });

  it("stackPane onto a pane in the same stack just activates it", () => {
    const p1 = pane("one");
    const p2 = pane("two");
    const tree = stack([p1, p2], 0);
    const after = stackPane(tree, p2.id, p1.id) as Stack;
    expect(after.panes.map((p) => p.id)).toEqual([p1.id, p2.id]); // order unchanged
    expect(after.panes[after.active].id).toBe(p2.id); // src activated
  });

  it("stackPane is a no-op for the same id or a missing id", () => {
    const tree = leaf(pane());
    const id = firstPaneId(tree);
    expect(stackPane(tree, id, id)).toBe(tree);
    expect(stackPane(tree, id, "nope")).toBe(tree);
  });

  it("activatePane switches the visible tab", () => {
    const p1 = pane();
    const p2 = pane();
    const tree = stack([p1, p2], 0);
    expect((activatePane(tree, p2.id) as Stack).active).toBe(1);
  });

  it("addTab appends a new active tab to the target's stack, leaving others alone", () => {
    let tree: Node<P> = leaf(pane("a"));
    const aId = firstPaneId(tree);
    const r = splitPane(tree, aId, "h", fresh); // a | b
    tree = r.tree;
    const bId = r.newPaneId;
    const { tree: after, newPaneId } = addTab(tree, aId, { label: "new" });
    const aStack = stackOf(after, aId)!;
    expect(aStack.panes.map((p) => p.id)).toEqual([aId, newPaneId]);
    expect(aStack.panes[aStack.active].id).toBe(newPaneId); // new tab is active
    expect(findPane(after, newPaneId)!.data.label).toBe("new");
    // b's stack is untouched (still a single pane).
    expect(stackOf(after, bId)!.panes).toHaveLength(1);
    // Missing target is a no-op (empty newPaneId).
    expect(addTab(tree, "nope", { label: "x" }).newPaneId).toBe("");
  });

  it("moves a pane to an edge of another, re-tiling and keeping its payload", () => {
    let tree: Node<P> = leaf(pane());
    const aId = firstPaneId(tree);
    const r = splitPane(tree, aId, "h", fresh);
    tree = r.tree;
    const bId = r.newPaneId;
    tree = updatePane(tree, aId, { label: "AAA" });

    const moved = movePane(tree, aId, bId, "v", false) as Split;
    expect(moved.type).toBe("split");
    expect(moved.dir).toBe("v");
    expect(moved.ratio).toBe(0.5);
    expect(allPanes(moved)).toHaveLength(2);
    expect((moved.a as Stack).panes[0].id).toBe(bId);
    expect((moved.b as Stack).panes[0].id).toBe(aId);
    expect((moved.b as Stack).panes[0].data.label).toBe("AAA");
  });

  it("stackStack merges every tab of one tile into another, removing the source", () => {
    // Left tile: [a, x] (a active); right tile: [b]. Stack the left tile onto the right.
    const a = pane("a");
    const x = pane("x");
    const b = pane("b");
    const tree: Node<P> = {
      type: "split",
      id: "s",
      dir: "h",
      a: stack([a, x], 0),
      b: stack([b], 0),
      ratio: 0.5,
    };
    const leftId = (tree as Split).a.id;
    const { tree: after, focus } = stackStack(tree, leftId, b.id);
    expect(after.type).toBe("stack"); // collapsed to a single tile
    const st = after as Stack;
    expect(st.panes.map((p) => p.id)).toEqual([b.id, a.id, x.id]); // dest tabs first, then source
    expect(st.panes[st.active].id).toBe(a.id); // source's active tab stays active
    expect(focus).toBe(a.id);
  });

  it("moveStack re-tiles a whole stack beside another, keeping its tabs", () => {
    const a = pane("a");
    const x = pane("x");
    const b = pane("b");
    const tree: Node<P> = {
      type: "split",
      id: "s",
      dir: "h",
      a: stack([a, x], 1),
      b: stack([b], 0),
      ratio: 0.5,
    };
    const leftId = (tree as Split).a.id;
    // Move the left tile below the right tile → (b / [a,x]).
    const { tree: after, focus } = moveStack(tree, leftId, b.id, "v", false);
    expect(after.type).toBe("split");
    const s = after as Split;
    expect(s.dir).toBe("v");
    expect((s.a as Stack).panes.map((p) => p.id)).toEqual([b.id]);
    expect((s.b as Stack).panes.map((p) => p.id)).toEqual([a.id, x.id]); // tabs preserved
    expect((s.b as Stack).active).toBe(1); // active tab preserved
    expect(focus).toBe(x.id); // the moved stack's active tab
  });

  it("moveStack/stackStack are no-ops (focus '') for a missing or same-tile target", () => {
    const p1 = pane();
    const p2 = pane();
    const tree = stack([p1, p2], 0);
    const st = tree as Stack;
    expect(stackStack(tree, st.id, p2.id).focus).toBe(""); // onto itself
    expect(moveStack(tree, "nope", p1.id, "h", false).focus).toBe("");
    expect(stackStack(tree, st.id, p1.id).tree).toBe(tree); // unchanged
  });

  it("pulls a tab out of a stack to a new tile (move within a stack)", () => {
    const p1 = pane("one");
    const p2 = pane("two");
    const tree = stack([p1, p2], 0);
    // Move p1 to the right edge of p2 -> two side-by-side single stacks.
    const moved = movePane(tree, p1.id, p2.id, "h", false) as Split;
    expect(moved.type).toBe("split");
    expect((moved.a as Stack).panes.map((p) => p.id)).toEqual([p2.id]);
    expect((moved.b as Stack).panes.map((p) => p.id)).toEqual([p1.id]);
  });

  it("movePane is a no-op onto itself or a missing id", () => {
    let tree: Node<P> = leaf(pane());
    const id = firstPaneId(tree);
    tree = splitPane(tree, id, "h", fresh).tree;
    expect(movePane(tree, id, id, "h", true)).toBe(tree);
    expect(movePane(tree, id, "nope", "h", true)).toBe(tree);
    expect(movePane(tree, "nope", id, "h", true)).toBe(tree);
  });

  it("closes a tab, keeping the stack when other tabs remain", () => {
    const p1 = pane();
    const p2 = pane();
    const tree = stack([p1, p2], 1);
    const { tree: closed, removed, focus } = closePane(tree, p2.id, fresh);
    expect(removed?.id).toBe(p2.id);
    expect(closed.type).toBe("stack");
    expect((closed as Stack).panes.map((p) => p.id)).toEqual([p1.id]);
    expect(focus).toBe(p1.id);
  });

  it("closes a pane by promoting its sibling when the stack empties", () => {
    const s = single();
    const a = firstPaneId(s.tree);
    const { tree: split, newPaneId: b } = splitPane(s.tree, a, "h", fresh);
    const { tree: closed, removed, focus } = closePane(split, a, fresh);
    expect(removed?.id).toBe(a);
    expect(closed.type).toBe("stack");
    expect(allPanes(closed)).toHaveLength(1);
    expect((closed as Stack).panes[0].id).toBe(b);
    expect(focus).toBe(b);
  });

  it("closing the last pane yields a fresh single pane", () => {
    const s = single("live");
    const a = firstPaneId(s.tree);
    const { tree, removed } = closePane(s.tree, a, fresh);
    expect(removed?.id).toBe(a);
    expect(tree.type).toBe("stack");
    const st = tree as Stack;
    expect(st.panes[0].id).not.toBe(a);
    expect(st.panes[0].data.label).toBe("");
  });

  it("clamps split ratio to sane bounds", () => {
    const s = single();
    const { tree } = splitPane(s.tree, firstPaneId(s.tree), "h", fresh);
    if (tree.type !== "split") throw new Error("expected split");
    expect((setRatio(tree, tree.id, 5) as Split).ratio).toBeLessThanOrEqual(0.92);
    expect((setRatio(tree, tree.id, -1) as Split).ratio).toBeGreaterThanOrEqual(0.08);
  });

  it("stackOf finds the stack holding a pane", () => {
    const p1 = pane();
    const p2 = pane();
    const tree = stack([p1, p2], 0);
    expect(stackOf(tree, p2.id)?.id).toBe((tree as Stack).id);
    expect(stackOf(tree, "nope")).toBeUndefined();
  });

  it("updatePane merges a payload patch", () => {
    let tree: Node<P> = leaf(pane("old"));
    const id = firstPaneId(tree);
    tree = updatePane(tree, id, { label: "new" });
    expect(findPane(tree, id)!.data.label).toBe("new");
  });

  describe("persistence", () => {
    beforeEach(() => globalThis.localStorage?.clear());

    it("round-trips a stacked layout through localStorage", () => {
      const p1 = pane("keep");
      const p2 = pane("two");
      const state: LayoutState<P> = { tree: stack([p1, p2], 1), focused: p2.id };
      saveLayout(KEY, state);
      const loaded = loadLayout<P>(KEY, isValidData, () => single());
      expect(allPanes(loaded.tree)).toHaveLength(2);
      expect((loaded.tree as Stack).active).toBe(1);
      expect(findPane(loaded.tree, p1.id)?.data.label).toBe("keep");
    });

    it("falls back to a default when storage is empty or corrupt", () => {
      expect(allPanes(loadLayout<P>(KEY, isValidData, () => single()).tree)).toHaveLength(1);
      globalThis.localStorage?.setItem(KEY, "{not json");
      expect(allPanes(loadLayout<P>(KEY, isValidData, () => single()).tree)).toHaveLength(1);
      // out-of-range active index is rejected too
      globalThis.localStorage?.setItem(
        KEY,
        JSON.stringify({ tree: { type: "stack", id: "s", panes: [{ id: "p", data: { label: "" } }], active: 5 }, focused: "p" }),
      );
      expect(allPanes(loadLayout<P>(KEY, isValidData, () => single()).tree)).toHaveLength(1);
    });

    it("repairs a focused id that no longer exists", () => {
      const s = single();
      saveLayout(KEY, { tree: s.tree, focused: "ghost" });
      const loaded = loadLayout<P>(KEY, isValidData, () => single());
      expect(loaded.focused).toBe(firstPaneId(loaded.tree));
    });
  });

  // rotateStacks is tmux's rotate-window: the tiles move through the slots, the slots
  // themselves do not move. Every test below therefore asserts BOTH halves — which labels
  // ended up where, and that the geometry it travelled through is untouched.
  // The geometry behind the pane chooser's arrow keys. It is arithmetic on the tree's own
  // ratios, never a measurement, so it can be asserted exactly — and has to be, because
  // jsdom lays nothing out and a DOM-measuring version would be untestable here.
  describe("tileRects / neighborStack", () => {
    // A 2x2 grid, built as a column of two rows so that document order is A B C D:
    //   A | B
    //   --+--
    //   C | D
    const grid = () => {
      const [a, b, c, d] = [stack([pane("a")], 0), stack([pane("b")], 0), stack([pane("c")], 0), stack([pane("d")], 0)];
      const tree: Node<P> = {
        type: "split", id: "outer", dir: "v", ratio: 0.5,
        a: { type: "split", id: "top", dir: "h", ratio: 0.5, a, b },
        b: { type: "split", id: "bot", dir: "h", ratio: 0.5, a: c, b: d },
      };
      return { tree, a, b, c, d };
    };
    const label = (s: Stack | undefined) => s?.panes[0].data.label;

    it("lays the tree out as fractions of the workspace", () => {
      const { tree, a, d } = grid();
      const rects = tileRects(tree);
      expect(rects).toHaveLength(4);
      expect(rects.find((r) => r.id === a.id)).toEqual({ id: a.id, x: 0, y: 0, w: 0.5, h: 0.5 });
      expect(rects.find((r) => r.id === d.id)).toEqual({ id: d.id, x: 0.5, y: 0.5, w: 0.5, h: 0.5 });
      // The four tile the whole surface exactly: a layout arithmetic that leaked would
      // still place each tile plausibly while leaving gaps only the sums can show.
      expect(rects.reduce((n, r) => n + r.w * r.h, 0)).toBeCloseTo(1);
    });

    it("takes an uneven ratio into account rather than assuming halves", () => {
      const s = stack([pane("s")], 0);
      const t: Node<P> = { type: "split", id: "x", dir: "h", ratio: 0.2, a: s, b: stack([pane("t")], 0) };
      expect(tileRects(t).find((r) => r.id === s.id)?.w).toBeCloseTo(0.2);
    });

    it("steps to the tile on the named side", () => {
      const { tree, a, d } = grid();
      expect(label(neighborStack(tree, a.id, "right"))).toBe("b");
      expect(label(neighborStack(tree, a.id, "bottom"))).toBe("c");
      expect(label(neighborStack(tree, d.id, "left"))).toBe("c");
      expect(label(neighborStack(tree, d.id, "top"))).toBe("b");
    });

    it("refuses the diagonal: a corner neighbour is on neither side", () => {
      // D touches A only at a corner, so from A neither → nor ↓ may reach it — a rule that
      // only asked "is it further right" would hand D to → in exactly the layout where the
      // four arrows have to mean four things.
      //
      // What this pins is the CONTRACT, not one line of the implementation: the rule is
      // written twice (the overlap filter and the overlap tie-break) and, on a complete
      // tiling, either alone would produce this answer. Deleting one does not fail this
      // test, and that is a property of the geometry rather than a gap in the test —
      // see neighborStack.
      const { tree, a, d } = grid();
      expect(neighborStack(tree, a.id, "right")?.id).not.toBe(d.id);
      expect(neighborStack(tree, a.id, "bottom")?.id).not.toBe(d.id);
    });

    it("stops at the wall instead of wrapping round", () => {
      const { tree, a } = grid();
      expect(neighborStack(tree, a.id, "left")).toBeUndefined();
      expect(neighborStack(tree, a.id, "top")).toBeUndefined();
      // tmux wraps here; this deliberately does not — see neighborStack. Asserting the
      // wall as undefined rather than "unchanged" is what pins that decision.
      expect(neighborStack(leaf(pane("only")), firstPaneId(leaf(pane("only"))), "right")).toBeUndefined();
    });

    it("takes the NEAREST candidate, not the first one in the tree", () => {
      // A column of three. From the bottom tile, ↑ must reach the middle one — which is the
      // LAST of the two candidates in document order, so a walk that stopped at its first
      // hit would jump past a whole tile.
      const [x, y, z] = [stack([pane("x")], 0), stack([pane("y")], 0), stack([pane("z")], 0)];
      const tree: Node<P> = {
        type: "split", id: "o", dir: "v", ratio: 0.5,
        a: { type: "split", id: "i", dir: "v", ratio: 0.5, a: x, b: y },
        b: z,
      };
      expect(label(neighborStack(tree, z.id, "top"))).toBe("y");
      expect(label(neighborStack(tree, x.id, "bottom"))).toBe("y");
    });

    it("breaks a tie on shared border, so a wide neighbour beats a sliver", () => {
      // A full-height tile facing a column split 25 / 75. Both are equally close (they share
      // the same border), so the one it actually adjoins for most of its height wins.
      const [full, sliver, most] = [stack([pane("full")], 0), stack([pane("sliver")], 0), stack([pane("most")], 0)];
      const tree: Node<P> = {
        type: "split", id: "o", dir: "h", ratio: 0.5,
        a: full,
        b: { type: "split", id: "i", dir: "v", ratio: 0.25, a: sliver, b: most },
      };
      expect(label(neighborStack(tree, full.id, "right"))).toBe("most");
    });
  });

  describe("rotateStacks", () => {
    // a | (b / c): a split whose right half is split again, so the walk order (a, b, c) is
    // not the same as the tree's nesting and a rotation that followed the nesting instead
    // would be visible.
    const three = (): Split => ({
      type: "split",
      id: "outer",
      dir: "h",
      ratio: 0.4,
      a: stack([pane("a")], 0),
      b: {
        type: "split",
        id: "inner",
        dir: "v",
        ratio: 0.7,
        a: stack([pane("b")], 0),
        b: stack([pane("c")], 0),
      },
    });
    const labels = (t: Node<P>) => allStacks(t).map((s) => s.panes.map((p) => p.data.label).join("+"));

    it("moves every tile one slot along, wrapping", () => {
      expect(labels(three())).toEqual(["a", "b", "c"]);
      expect(labels(rotateStacks(three()))).toEqual(["c", "a", "b"]);
      // ...and the other way, which is the same walk read backwards.
      expect(labels(rotateStacks(three(), false))).toEqual(["b", "c", "a"]);
    });

    it("leaves the geometry exactly as it found it", () => {
      const before = three();
      const after = rotateStacks(before) as Split;
      // The splits keep their ids, axes and ratios: a rotation must not resize anything.
      expect(after.id).toBe("outer");
      expect(after.dir).toBe("h");
      expect(after.ratio).toBe(0.4);
      const inner = after.b as Split;
      expect(inner.id).toBe("inner");
      expect(inner.dir).toBe("v");
      expect(inner.ratio).toBe(0.7);
    });

    it("carries a tile's tabs with it, rather than rotating panes between tiles", () => {
      const t: Split = {
        type: "split",
        id: "s",
        dir: "h",
        ratio: 0.5,
        a: stack([pane("a1"), pane("a2")], 1),
        b: stack([pane("b1")], 0),
      };
      const after = rotateStacks(t) as Split;
      expect(labels(after)).toEqual(["b1", "a1+a2"]);
      // The travelling tile keeps its ACTIVE tab too — arriving showing a different tab
      // than it left with would be a second, invisible change.
      expect((after.b as Stack).active).toBe(1);
    });

    it("is a no-op below two tiles, and a swap at exactly two", () => {
      const one = leaf(pane("only"));
      expect(rotateStacks(one)).toBe(one); // same object: nothing to do, nothing rebuilt

      const two: Split = {
        type: "split", id: "s", dir: "h", ratio: 0.5,
        a: stack([pane("a")], 0),
        b: stack([pane("b")], 0),
      };
      expect(labels(rotateStacks(two))).toEqual(["b", "a"]);
      // Twice round is identity for two tiles, which is what makes it a swap; asserting
      // one direction alone would pass for a rotation that dropped a tile.
      expect(labels(rotateStacks(rotateStacks(two)))).toEqual(["a", "b"]);
    });

    it("returns to the start after as many rotations as there are tiles", () => {
      let t: Node<P> = three();
      for (let i = 0; i < 3; i++) t = rotateStacks(t);
      expect(labels(t)).toEqual(["a", "b", "c"]);
      // Every pane survived the round trip — a cycle that lost or duplicated one would
      // still come back looking right by label alone if it duplicated the right one.
      expect(allPanes(t)).toHaveLength(3);
    });
  });

  // nextPaneId is tmux's `prefix o`. Next in LAYOUT order — the order allPanes walks and
  // therefore the order the pane numbers are assigned in, so the walk goes where the digits
  // on the tabs say it will.
  describe("nextPaneId", () => {
    // a | (b1+b2 / c). The middle tile holds two TABS, so pane order (a, b1, b2, c) is not
    // tile order (a, b*, c): every test below turns on that difference, because stepping
    // through background tabs is what separates this from rotateStacks and from the
    // chooser's tile-to-tile arrows.
    const mixed = (): Split => ({
      type: "split",
      id: "outer",
      dir: "h",
      ratio: 0.5,
      a: stack([pane("a")], 0),
      b: {
        type: "split",
        id: "inner",
        dir: "v",
        ratio: 0.5,
        a: stack([pane("b1"), pane("b2")], 0),
        b: stack([pane("c")], 0),
      },
    });
    const idOf = (t: Node<P>, label: string) => allPanes(t).find((p) => p.data.label === label)!.id;
    const step = (t: Node<P>, from: string) => findPane(t, nextPaneId(t, idOf(t, from)))!.data.label;

    it("steps to the next pane in layout order, background tabs included, and wraps", () => {
      const t = mixed();
      expect(allPanes(t).map((p) => p.data.label)).toEqual(["a", "b1", "b2", "c"]);
      // Four distinct answers, which is what rules out the two plausible wrong functions:
      // "always the first pane" fails on every line but the last, and "the next TILE's
      // active pane" fails on b1 -> b2, the step that stays inside one tile.
      expect(step(t, "a")).toBe("b1");
      expect(step(t, "b1")).toBe("b2");
      expect(step(t, "b2")).toBe("c");
      expect(step(t, "c")).toBe("a");
    });

    it("visits every pane exactly once before it comes back", () => {
      const t = mixed();
      let id = idOf(t, "a");
      const seen: string[] = [];
      for (let i = 0; i < 4; i++) {
        id = nextPaneId(t, id);
        seen.push(findPane(t, id)!.data.label);
      }
      // A cycle that skipped one and doubled another would still return to "a" on the
      // fourth step, so the whole itinerary is pinned and not just its end.
      expect(seen).toEqual(["b1", "b2", "c", "a"]);
    });

    it("starts at the first pane when the id names no pane in the tree", () => {
      // A pane can vanish under a stale `focused` — a shell that exits closes its own pane.
      // Recovering to the first is the same fallback loadLayout makes with firstPaneId, and
      // it is a recovery rather than the general behaviour: the test above already shows the
      // walk is not "always the first pane".
      const t = mixed();
      expect(findPane(t, nextPaneId(t, "no-such-pane"))!.data.label).toBe("a");
    });

    it("returns the pane itself when it is the only one", () => {
      // Which is why the command is offered DISABLED at one pane: the key would be a no-op
      // rather than an error, and a key that silently does nothing is the worse of the two.
      const only = leaf(pane("only"));
      expect(nextPaneId(only, firstPaneId(only))).toBe(firstPaneId(only));
    });
  });
});
