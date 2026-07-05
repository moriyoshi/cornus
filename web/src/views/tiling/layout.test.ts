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
  findPane,
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
});
