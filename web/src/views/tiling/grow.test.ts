import { describe, it, expect } from "vitest";
import {
  leaf,
  newPane,
  tileRects,
  findStackById,
  allStacks,
  UNIT_EXT,
  type Dir,
  type Ext,
  type Node,
} from "./layout";
import {
  EXISTING_SHARE,
  GOLDEN,
  closeExtending,
  evenHorizontal,
  extentOf,
  movePaneExtending,
  resizeEdge,
  resizeExtending,
  MIN_PANE_HEIGHT_REM,
  MIN_PANE_WIDTH_REM,
  applyMovePane,
  applySplit,
  minPaneRem,
  splitExtending,
  stackPaneExtending,
} from "./grow";

// The extending workspace, tested as GEOMETRY rather than as ratios.
//
// A ratio is only half an answer here — 0.6 means one thing under an extent of 1 and another
// under 1.667 — and the property this module exists to guarantee is about absolute rectangles:
// the tile you split does not move, and neither does anything else except what is behind the
// growth. So every assertion below reads the layout the way the screen does, as x/y/w/h in
// viewport extents, and the helper that produces it is the same arithmetic the CSS performs.

type P = { label: string };

// The three numbers of the sizing rule, named once so the tests quote the rule rather than
// restating its arithmetic. A split multiplies the workspace by GOLDEN; everything already
// there is scaled by SCALE and keeps its proportions; the new tile gets MADE.
const SCALE = EXISTING_SHARE * GOLDEN; // 1.0787
const MADE = GOLDEN - EXISTING_SHARE * GOLDEN; // 0.5393

// r rounds to 6 places, for the like-for-like comparisons. The arithmetic is exact in principle
// and binary in practice; without this, 0.6000000000000001 fails an assertion that is describing
// a correct layout.
const r = (n: number) => Math.round(n * 1e6) / 1e6;

// near is for the assertions that ADD two rounded numbers — ⅔ + ⅔ is not 2 × r(⅔), so an exact
// comparison against a sum of rounded values fails on a layout that is perfectly correct. It is
// a looser check than toEqual and used only where the arithmetic makes toEqual meaningless.
const near = (actual: number, expected: number) => expect(actual).toBeCloseTo(expected, 5);

interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

// layoutOf is what the workspace looks like: every tile's absolute rectangle in viewport
// extents, keyed by the labels of the panes it holds. Keyed by LABEL and not by id because a
// test should be able to say "the tile showing B" without threading an id through five
// operations, and because a move that rebuilds a tile changes its id but not what it shows.
function layoutOf(tree: Node<P>, ext: Ext): Record<string, Rect> {
  const out: Record<string, Rect> = {};
  for (const rect of tileRects(tree)) {
    const st = findStackById(tree, rect.id);
    if (!st) continue;
    out[st.panes.map((p) => p.data.label).join("+")] = {
      x: r(rect.x * ext.w),
      y: r(rect.y * ext.h),
      w: r(rect.w * ext.w),
      h: r(rect.h * ext.h),
    };
  }
  return out;
}

const extOf = (ext: Ext) => ({ w: r(ext.w), h: r(ext.h) });

// A workspace under test: the tree, its extent, and the pane ids by label.
class WS {
  tree: Node<P>;
  ext: Ext;
  constructor(label: string) {
    const p = newPane<P>({ label });
    this.tree = leaf(p);
    this.ext = { ...UNIT_EXT };
  }
  paneId(label: string): string {
    for (const st of allStacks(this.tree)) {
      const found = st.panes.find((p) => p.data.label === label);
      if (found) return found.id;
    }
    throw new Error(`no pane labelled ${label}`);
  }
  stackId(label: string): string {
    for (const st of allStacks(this.tree)) {
      if (st.panes.some((p) => p.data.label === label)) return st.id;
    }
    throw new Error(`no tile showing ${label}`);
  }
  split(from: string, dir: Dir, made: string, before = false): this {
    const out = splitExtending<P>(
      this.tree,
      this.ext,
      this.paneId(from),
      dir,
      () => ({ label: made }),
      before,
    );
    this.tree = out.tree;
    this.ext = out.ext;
    return this;
  }
  close(label: string): this {
    const out = closeExtending<P>(this.tree, this.ext, this.paneId(label), () => ({ label: "*" }));
    this.tree = out.tree;
    this.ext = out.ext;
    return this;
  }
  layout(): Record<string, Rect> {
    return layoutOf(this.tree, this.ext);
  }
}

describe("the extending workspace", () => {
  // The sizing rule as a worked example, pinned whole. If this drifts the feature has changed
  // shape and the documentation describing it is wrong, which is the point of quoting the whole
  // layout rather than asserting one number at a time.
  it("multiplies the workspace by the golden ratio and gives the new tile a third of it", () => {
    const ws = new WS("A");
    expect(ws.layout()).toEqual({ A: { x: 0, y: 0, w: 1, h: 1 } });

    ws.split("A", "h", "N1");
    expect(extOf(ws.ext)).toEqual({ w: r(GOLDEN), h: 1 });
    expect(ws.layout()).toEqual({
      // A did not stay put — it grew by ⅔φ, which is the rule: what was already here keeps its
      // PROPORTIONS, not its pixels.
      A: { x: 0, y: 0, w: r(SCALE), h: 1 },
      N1: { x: r(SCALE), y: 0, w: r(MADE), h: 1 },
    });

    ws.split("A", "v", "N2");
    expect(extOf(ws.ext)).toEqual({ w: r(GOLDEN), h: r(GOLDEN) });
    expect(ws.layout()).toEqual({
      A: { x: 0, y: 0, w: r(SCALE), h: r(SCALE) },
      N2: { x: 0, y: r(SCALE), w: r(SCALE), h: r(MADE) },
      // N1 is in the perpendicular branch: it spans the whole height by construction, so it
      // fills the new one. A tile that spans an axis cannot also be scaled along it.
      N1: { x: r(SCALE), y: 0, w: r(MADE), h: r(GOLDEN) },
    });
  });

  // The invariant that survives every arrangement: whatever the tree looks like and wherever the
  // split lands in it, the tile that was made is exactly a third of the workspace it made.
  it("gives the new tile exactly a third of the grown workspace, wherever it lands", () => {
    const deep = new WS("A").split("A", "h", "B").split("B", "v", "C");
    const out = splitExtending<P>(
      deep.tree,
      deep.ext,
      deep.paneId("C"),
      "h",
      () => ({ label: "D" }),
      false,
    );
    near(layoutOf(out.tree, out.ext)["D"].w, out.ext.w / 3);
  });

  // Everything already present is scaled by ONE factor, which is the whole of "keeps the ratios
  // it occupies": no two tiles change their relationship to each other, however many there are
  // and however they are arranged.
  it("scales every existing tile by the same factor, so no two change their relationship", () => {
    const ws = new WS("A").split("A", "h", "B").split("B", "h", "C");
    const before = ws.layout();
    ws.split("A", "h", "D");
    const after = ws.layout();
    for (const label of ["A", "B", "C"]) {
      near(after[label].w / before[label].w, SCALE);
    }
    // Which is the same as saying the proportions between them are untouched.
    near(after["A"].w / after["B"].w, before["A"].w / before["B"].w);
    near(after["B"].w / after["C"].w, before["B"].w / before["C"].w);
  });

  // A horizontal split is horizontal. Widths all move — that is the rule — but nothing changes
  // height or vertical position, which is what keeps the two axes independent.
  it("a horizontal split leaves every height and every vertical position alone", () => {
    const ws = new WS("A").split("A", "v", "B").split("A", "h", "C");
    const before = ws.layout();
    const beforeExt = extOf(ws.ext);

    ws.split("A", "h", "D");
    const after = ws.layout();

    for (const label of ["A", "B", "C"]) {
      expect({ label, y: after[label].y, h: after[label].h }).toEqual({
        label,
        y: before[label].y,
        h: before[label].h,
      });
    }
    expect(extOf(ws.ext).h).toBe(beforeExt.h);
    near(ws.ext.w, beforeExt.w * GOLDEN);
  });

  // `before` decides WHERE the new tile lands and nothing else: the sizes are the rule's, and
  // the rule does not know about sides. Asserted as a pair, because either direction alone would
  // pass for an implementation that ignored `before` entirely.
  it("places the new tile on the side asked for, at the same size either way", () => {
    const build = () => new WS("P").split("P", "h", "Q");

    const after = build();
    after.split("P", "h", "N", false);
    const far = after.layout();

    const first = build();
    first.split("P", "h", "N", true);
    const near_ = first.layout();

    // Same geometry, mirrored: the new tile is the same width and the workspace the same extent.
    expect(far["N"].w).toBe(near_["N"].w);
    expect(extOf(after.ext)).toEqual(extOf(first.ext));
    // And it is genuinely on the other side of the tile it was split from.
    expect(far["N"].x).toBeGreaterThan(far["P"].x);
    expect(near_["N"].x).toBeLessThan(near_["P"].x);
  });

  // Closing removes a tile's extent from the workspace. It does NOT undo the scaling its split
  // applied to everything else — a split is a change to the whole workspace under this rule, and
  // there is no record of which tiles paid for which tile. So the honest claim is the local one.
  it("shrinks the workspace by the extent of the tile that closed", () => {
    const ws = new WS("A").split("A", "h", "B");
    ws.split("B", "v", "C");
    const before = ws.layout();
    const beforeExt = extOf(ws.ext);

    ws.close("C");
    // The height C occupied is gone, and the tiles that did not touch it kept their own.
    expect(ws.ext.h).toBeLessThan(beforeExt.h);
    expect(ws.layout()["A"].w).toBe(before["A"].w);
    expect(ws.layout()["B"].w).toBe(before["B"].w);
  });

  it("shrinks back to one screen when everything is closed", () => {
    const ws = new WS("A").split("A", "h", "B").split("B", "v", "C");
    ws.close("C").close("B");
    expect(extOf(ws.ext)).toEqual({ w: 1, h: 1 });
    expect(ws.layout()).toEqual({ A: { x: 0, y: 0, w: 1, h: 1 } });
  });

  // A tab is not a tile: closing one leaves the tiling alone.
  it("does not resize anything when a tab closes but its tile survives", () => {
    const ws = new WS("A").split("A", "h", "B");
    const moved = stackPaneExtending<P>(ws.tree, ws.ext, ws.paneId("A"), ws.paneId("B"));
    ws.tree = moved.tree;
    ws.ext = moved.ext;
    // A is now a tab on B's tile, and the workspace has shrunk back to one screen.
    expect(extOf(ws.ext)).toEqual({ w: 1, h: 1 });
    const before = ws.layout();
    ws.close("A");
    expect(extOf(ws.ext)).toEqual({ w: 1, h: 1 });
    expect(Object.values(ws.layout())[0]).toEqual(Object.values(before)[0]);
  });

  it("carries a moved tile's extent to where it lands", () => {
    // h( A | B ), then a third tile so there is something to move between.
    const ws = new WS("A").split("A", "h", "B").split("B", "v", "C");
    const carried = ws.layout()["C"];

    const out = movePaneExtending<P>(ws.tree, ws.ext, ws.paneId("C"), ws.paneId("A"), "h", false);
    ws.tree = out.tree;
    ws.ext = out.ext;
    const after = ws.layout();

    // It kept its width (the axis it was placed on) and adopted A's height (the axis it now
    // shares with A), which is the only part of "carries its extent" a sibling can honour.
    expect(after["C"].w).toBe(carried.w);
    expect(after["C"].h).toBe(after["A"].h);
    expect(after["A"].w).toBe(r(SCALE)); // A is where the golden rule left it, not resized by the move
  });

  // The Alt-divider and the Grow/Shrink commands. The distinguishing property against setRatio
  // is not that one child grew — both do that — but that the OTHER one did not.
  it("resizes one child of a split and leaves its sibling alone", () => {
    const ws = new WS("A").split("A", "h", "B");
    const before = ws.layout();
    const beforeExt = extOf(ws.ext);
    const rootId = ws.tree.id;

    const out = resizeExtending<P>(ws.tree, ws.ext, rootId, true, 0.25);
    const after = layoutOf(out.tree, out.ext);

    near(after["A"].w, before["A"].w + 0.25);
    expect(after["B"].w).toBe(before["B"].w); // the sibling is what setRatio would have taken from
    near(out.ext.w, beforeExt.w + 0.25);
    near(after["B"].x, before["B"].x + 0.25); // it moved rather than resized
  });

  it("keeps a resized child's internal proportions", () => {
    // The child being grown is itself a column of two, and they should stay half and half.
    const ws = new WS("A").split("A", "h", "B").split("A", "v", "A2");
    const rootId = ws.tree.id;
    const out = resizeExtending<P>(ws.tree, ws.ext, rootId, true, 0.4);
    const after = layoutOf(out.tree, out.ext);
    expect(after["A"].w).toBe(after["A2"].w);
    // The column was made by one vertical split, so its two tiles stand at ⅔ : ⅓ — and a
    // resize along the other axis must not disturb that.
    near(after["A"].h / (after["A"].h + after["A2"].h), EXISTING_SHARE);
  });

  // The workspace only grows when halving would leave the panes too small. Above the floor an
  // ordinary 50/50 split is what the user gets, so a wide screen does not start scrolling until
  // there is a reason to.
  describe("the minimum-pane floor", () => {
    const split = (floor: number, dir: Dir = "h") => {
      const ws = new WS("A");
      const out = applySplit<P>(
        true,
        ws.tree,
        ws.ext,
        ws.paneId("A"),
        dir,
        () => ({ label: "B" }),
        false,
        floor,
      );
      return { layout: layoutOf(out.tree, out.ext), ext: out.ext };
    };

    it("halves the tile and leaves the workspace alone when both halves clear the floor", () => {
      // A full-screen tile halves to 0.5 of a viewport; a floor of 0.3 is comfortably under it.
      const { layout, ext } = split(0.3);
      expect(extOf(ext)).toEqual({ w: 1, h: 1 });
      expect(layout).toEqual({
        A: { x: 0, y: 0, w: 0.5, h: 1 },
        B: { x: 0.5, y: 0, w: 0.5, h: 1 },
      });
    });

    it("extends the workspace when halving would go under the floor", () => {
      const { layout, ext } = split(0.6);
      // The floor (0.6) is above the rule's own third (0.539), so the floor is what the new
      // tile gets and the workspace is the scaled remainder plus it.
      expect(layout["B"].w).toBe(0.6);
      expect(layout["A"].w).toBe(r(SCALE));
      near(ext.w, SCALE + 0.6);
    });

    // The boundary, from both sides. A single-sided check passes just as happily for `<=` as for
    // `<`, and the two disagree on exactly the layout that sits on the line.
    it("treats a half that exactly meets the floor as good enough", () => {
      expect(extOf(split(0.5).ext)).toEqual({ w: 1, h: 1 }); // 0.5 >= 0.5: divide
      expect(r(split(0.5000001).ext.w)).toBe(r(GOLDEN)); // a hair under: extend
    });

    it("asks the question per axis, on the extent of that axis", () => {
      // The tile is 1x1, so the same floor answers the same way on both axes here...
      expect(extOf(split(0.3, "v").ext)).toEqual({ w: 1, h: 1 });
      // ...and the vertical extent is what a vertical split is measured against.
      const tall = new WS("A").split("A", "h", "B"); // both tiles still a full viewport tall
      const out = applySplit<P>(
        true,
        tall.tree,
        tall.ext,
        tall.paneId("B"),
        "v",
        () => ({ label: "C" }),
        false,
        0.3,
      );
      expect(extOf(out.ext).h).toBe(1); // halving 1.0 of height clears a 0.3 floor
    });

    // The two constants, asserted as an ORDER rather than as values. Pinning 40 and 20 would
    // make this a copy of the source; what matters is that height is the smaller of the two,
    // because that is the fact a single shared constant could not express and the reason there
    // are two. Anyone retuning them keeps this test green; anyone collapsing them does not.
    it("asks less of a pane's height than of its width", () => {
      expect(minPaneRem("v")).toBeLessThan(minPaneRem("h"));
      expect(minPaneRem("h")).toBe(MIN_PANE_WIDTH_REM);
      expect(minPaneRem("v")).toBe(MIN_PANE_HEIGHT_REM);
    });

    // The floors are set against a real screen, so the sizes they were chosen for are worth
    // pinning: one vertical split of a laptop-height workspace is free, the second is not. Stated
    // in px against the rem values so it fails if either constant drifts out of that relationship.
    it("leaves one vertical split free on a laptop and not the second", () => {
      const bodyPx = 732; // a 1280x800 window, measured
      const floor = (MIN_PANE_HEIGHT_REM * 16) / bodyPx;
      expect(0.5).toBeGreaterThan(floor); // halving the workspace: divides
      expect(0.25).toBeLessThan(floor); // halving again: extends
    });

    // A MOVE is divided or extended on the same terms, asked of the DESTINATION — that is the
    // tile being halved to make room. Built as a pair so the two answers are told apart by the
    // floor and by nothing else: same tree, same move, two thresholds.
    describe("and a move asks it of the destination", () => {
      // h( A | B ), then C split off B, so there is a tile to move and two to move it between.
      const three = () => new WS("A").split("A", "h", "B").split("B", "v", "C");

      const move = (floor: number) => {
        const ws = three();
        const out = applyMovePane<P>(
          true,
          ws.tree,
          ws.ext,
          ws.paneId("C"),
          ws.paneId("A"),
          "h",
          false,
          floor,
        );
        return { layout: layoutOf(out.tree, out.ext), ext: out.ext, was: ws.layout(), wasExt: ws.ext };
      };

      it("halves the destination when both halves would clear the floor", () => {
        const { layout, ext, wasExt } = move(0.3);
        // A was a full viewport wide; halving it leaves 0.5, over the floor.
        expect(extOf(ext)).toEqual(extOf(wasExt)); // the workspace did not change size
        // A was 1.0787 wide and is halved in place; the workspace never changed size.
        near(layout["A"].w, SCALE / 2);
        near(layout["C"].w, SCALE / 2);
      });

      it("extends the workspace when halving the destination would go under it", () => {
        const { layout, ext, wasExt } = move(0.6);
        expect(ext.w).toBeGreaterThan(wasExt.w);
        expect(layout["A"].w).toBe(r(SCALE)); // the destination kept the size it had
      });

      it("carries the moved tile's own extent when it extends", () => {
        const ws = three();
        const carried = ws.layout()["C"].w;
        const out = applyMovePane<P>(
          true,
          ws.tree,
          ws.ext,
          ws.paneId("C"),
          ws.paneId("A"),
          "h",
          false,
          0.6,
        );
        expect(layoutOf(out.tree, out.ext)["C"].w).toBe(carried);
      });
    });

    // The floor has to hold for the pane the split MAKES, not only for the halving it refused.
    //
    // Extension happens when W/2 is under the floor, i.e. W < 2·floor. The new pane is ⅔W, and
    // ⅔W is under the floor whenever W < 1.5·floor — so there is a whole band of tile sizes
    // where the rule fires to avoid a cramped pane and then produces one anyway. It is not a
    // corner: on a 1400px screen the first split divides into two 700px panes, and splitting one
    // of THOSE lands here.
    it("never makes a new pane narrower than the floor that made it extend", () => {
      const floor = 0.8;
      const ws = new WS("A");
      const out = applySplit<P>(
        true,
        ws.tree,
        ws.ext,
        ws.paneId("A"),
        "h",
        () => ({ label: "B" }),
        false,
        floor,
      );
      const after = layoutOf(out.tree, out.ext);
      // Halving would have given 0.5, under the floor — which is the only reason this extended.
      expect(after["A"].w).toBe(r(SCALE)); // scaled by the rule, as everything present is
      expect(after["B"].w).toBeGreaterThanOrEqual(floor);
    });

    // ...and the floor is a MINIMUM, not a target: a tile with room to spare still gives two
    // thirds, so the rule stays scale-free wherever it is not binding.
    it("still gives two thirds when two thirds already clears the floor", () => {
      const ws = new WS("A");
      const out = applySplit<P>(
        true,
        ws.tree,
        ws.ext,
        ws.paneId("A"),
        "h",
        () => ({ label: "B" }),
        false,
        0.52, // halving (0.5) is under it, but the rule's own third (0.539) is over it
      );
      expect(layoutOf(out.tree, out.ext)["B"].w).toBe(r(MADE));
    });

    // The sequence a user produces by holding down the split key on a 1400px screen, measured:
    // new-pane widths of 692, 746, 1208, 1954, 977, 3162 px, with the workspace ending at
    // 9486px — 6.9 screens. Two things worth pinning about it.
    //
    // GOOD: nothing decays. Under the old ⅔-of-the-source rule the widths ran 692, 461, 308,
    // 205, 137 and everything after the second was unreadable; the new tile is now a third of a
    // workspace that is itself growing, so the trend is upward and the floor is never reached
    // from above.
    //
    // COSTLY, and flagged to the user rather than silently accepted: a third of a growing
    // workspace eventually exceeds the SCREEN. By the fourth split the new pane is 1954px on a
    // 1384px viewport — a pane you cannot see all of at once. The rule as specified has no term
    // that stops this, so the test records it instead of pretending otherwise.
    it("makes new panes grow rather than decay, and never one under the floor", () => {
      const floor = 640 / 1384; // 40rem on a 1400px window
      const ws = new WS("A");
      const widths: number[] = [];
      for (let i = 0; i < 6; i++) {
        const target = widths.length ? `N${widths.length}` : "A";
        const out = applySplit<P>(
          true,
          ws.tree,
          ws.ext,
          ws.paneId(target),
          "h",
          () => ({ label: `N${widths.length + 1}` }),
          false,
          floor,
        );
        ws.tree = out.tree;
        ws.ext = out.ext;
        widths.push(layoutOf(ws.tree, ws.ext)[`N${widths.length + 1}`].w);
      }
      // Not one of them under the floor, however deep the chain goes.
      for (const w of widths) expect(w).toBeGreaterThanOrEqual(r(floor) - 1e-6);
      // Upward, which is the whole difference from the fraction-of-the-source rule.
      expect(widths[widths.length - 1]).toBeGreaterThan(widths[0]);
      // And the recorded cost: past the viewport by the fourth.
      expect(widths[3]).toBeGreaterThan(1);
    });

    // One extending split multiplies the workspace by φ along the axis, whatever is in it.
    it("multiplies the workspace by the golden ratio on every extending split", () => {
      const ws = new WS("A").split("A", "h", "B");
      const was = ws.ext.w;
      ws.split("B", "h", "C");
      near(ws.ext.w, was * GOLDEN);
    });

    // An unmeasured viewport is the jsdom case and the first-paint case. "I do not know how big
    // the screen is" must not resolve to "so halve it": extending cannot make a pane too small.
    it("extends when the screen size is unknown", () => {
      expect(r(split(Infinity).ext.w)).toBe(r(GOLDEN));
    });

    // The floor is about crowding, not about the setting. Under the dividing layout the answer
    // is always to halve, floor or no floor.
    it("is not consulted under the dividing layout", () => {
      const ws = new WS("A");
      const out = applySplit<P>(
        false,
        ws.tree,
        ws.ext,
        ws.paneId("A"),
        "h",
        () => ({ label: "B" }),
        false,
        Infinity,
      );
      expect(extOf(out.ext)).toEqual({ w: 1, h: 1 });
      expect(layoutOf(out.tree, out.ext)["A"].w).toBe(0.5);
    });
  });

  // tmux's even-horizontal: every tile into one row, all the same width.
  describe("even horizontal", () => {
    // A deliberately lopsided starting layout — three splits deep on one side, nothing on the
    // other — which is the state the command exists to get out of.
    const lopsided = () =>
      new WS("A").split("A", "h", "B").split("B", "v", "C").split("C", "v", "D");

    it("puts every tile in one row of equal width", () => {
      const ws = lopsided();
      const out = evenHorizontal<P>(ws.tree, ws.ext);
      const after = layoutOf(out.tree, out.ext);

      const labels = Object.keys(after).sort();
      expect(labels).toEqual(["A", "B", "C", "D"]);
      // One row: every tile starts at the top and is the full height of the workspace.
      for (const l of labels) {
        expect({ l, y: after[l].y, h: after[l].h }).toEqual({ l, y: 0, h: out.ext.h });
      }
      // Equal widths, and they tile the row end to end with no gap and no overlap.
      const widths = labels.map((l) => after[l].w);
      expect(new Set(widths).size).toBe(1);
      const xs = labels.map((l) => after[l].x).sort((a, b) => a - b);
      xs.forEach((x, i) => near(x, i * widths[0]));
      near(out.ext.w, 4 * widths[0]);
    });

    it("flattens the workspace back to one screen tall", () => {
      const ws = lopsided();
      expect(ws.ext.h).toBeGreaterThan(1); // the vertical splits had made it taller
      const out = evenHorizontal<P>(ws.tree, ws.ext);
      expect(out.ext.h).toBe(1); // nothing is stacked any more, so nothing needs the height
    });

    // The property that lets this be run on a workspace full of live terminals: the tiles are
    // re-parented, not rebuilt, so their ids — and with them their panes, tabs and DOM — survive.
    it("keeps the very same tiles, with their tabs and their ids", () => {
      const ws = lopsided();
      const before = allStacks(ws.tree).map((s) => s.id).sort();
      const out = evenHorizontal<P>(ws.tree, ws.ext);
      expect(allStacks(out.tree).map((s) => s.id).sort()).toEqual(before);
    });

    it("fits the screen exactly when no floor is asked for", () => {
      const out = evenHorizontal<P>(lopsided().tree, lopsided().ext, 0);
      expect(extOf(out.ext)).toEqual({ w: 1, h: 1 });
      expect(layoutOf(out.tree, out.ext)["A"].w).toBe(0.25);
    });

    // Where an even share would be unreadable, the row is built at the floor and the workspace
    // gets wider instead — "even" must not quietly mean "each of eight is an eighth of nothing".
    it("builds the row at the floor and widens the workspace when a quarter is too narrow", () => {
      const out = evenHorizontal<P>(lopsided().tree, lopsided().ext, 0.4);
      const after = layoutOf(out.tree, out.ext);
      expect(after["A"].w).toBe(0.4);
      near(out.ext.w, 1.6);
    });

    it("leaves an unmeasured screen fitting the viewport rather than infinitely wide", () => {
      const out = evenHorizontal<P>(lopsided().tree, lopsided().ext, Infinity);
      expect(extOf(out.ext)).toEqual({ w: 1, h: 1 });
    });
  });

  // The workspace's own borders. What makes these different from every other divider is that
  // there is nothing on their far side to trade with, so they move the workspace itself — and
  // which tiles come along is the same `absorb` rule a split uses.
  describe("the outer edge handles", () => {
    it("widens only the tiles that touch the right edge", () => {
      // h( A | v(B / C) ) — B and C are stacked, so only C touches the bottom, but BOTH touch
      // the right. A touches neither.
      const ws = new WS("A").split("A", "h", "B").split("B", "v", "C");
      const before = ws.layout();

      const out = resizeEdge<P>(ws.tree, ws.ext, "h", 0.4);
      const after = layoutOf(out.tree, out.ext);

      near(out.ext.w, ws.ext.w + 0.4);
      expect(after["A"]).toEqual(before["A"]); // two columns in: untouched
      near(after["B"].w, before["B"].w + 0.4);
      near(after["C"].w, before["C"].w + 0.4);
      // Heights are the other axis and have no business changing.
      expect(after["B"].h).toBe(before["B"].h);
      expect(after["C"].h).toBe(before["C"].h);
    });

    it("heightens only the tile that touches the bottom edge", () => {
      const ws = new WS("A").split("A", "h", "B").split("B", "v", "C");
      const before = ws.layout();

      const out = resizeEdge<P>(ws.tree, ws.ext, "v", 0.3);
      const after = layoutOf(out.tree, out.ext);

      near(out.ext.h, ws.ext.h + 0.3);
      // C is under B, so C is the one at the bottom of that column; B does not move or grow.
      expect(after["B"]).toEqual(before["B"]);
      near(after["C"].h, before["C"].h + 0.3);
      // A spans the full height on its own, so it is also a tile touching the bottom.
      near(after["A"].h, before["A"].h + 0.3);
    });

    it("shrinks, and stops when the edge tile has nothing left to give", () => {
      const ws = new WS("A").split("A", "h", "B");
      const before = ws.layout();
      const shrunk = resizeEdge<P>(ws.tree, ws.ext, "h", -0.4);
      expect(shrunk.ext.w).toBeLessThan(ws.ext.w);

      // Dragged far past the left. The workspace does NOT collapse to one screen, and that is
      // the right answer rather than a missing clamp: this handle moves the tiles touching the
      // right edge, and A is not one of them. B bottoms out at the minimum tile, A keeps every
      // pixel it had, and the workspace is as wide as those two facts make it.
      const floored = resizeEdge<P>(ws.tree, ws.ext, "h", -99);
      const after = layoutOf(floored.tree, floored.ext);
      expect(after["A"].w).toBe(before["A"].w);
      expect(after["B"].w).toBeGreaterThan(0);
      expect(after["B"].w).toBeLessThan(0.1);
      near(floored.ext.w, after["A"].w + after["B"].w);
      // And never under a screen, whatever the arithmetic upstream produced.
      expect(floored.ext.w).toBeGreaterThanOrEqual(1);
      expect(floored.ext.h).toBe(1); // the other axis is not this handle's business
    });
  });

  it("reports a tile's extent on each axis", () => {
    const ws = new WS("A").split("A", "h", "B");
    expect(r(extentOf(ws.tree, ws.ext, ws.stackId("B"), "h"))).toBe(r(MADE));
    expect(r(extentOf(ws.tree, ws.ext, ws.stackId("B"), "v"))).toBe(1);
  });
});
