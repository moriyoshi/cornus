import { describe, it, expect } from "vitest";
import { UNIT_EXT, type Ext } from "./layout";
import {
  BULLET_GAP_REM,
  BULLET_REM,
  CELL_PAD_REM,
  MAP_MAX_H_REM,
  MAP_MAX_W_REM,
  NO_METRICS,
  bulletGrid,
  bulletRoom,
  bulletWindow,
  mapBox,
  viewportRect,
  type BulletWindow,
  type ScrollMetrics,
} from "./minimap";

// The mini map's arithmetic, away from the DOM. What is here is everything that decides what
// the map LOOKS like; what is not here is the one measurement that feeds it, which jsdom
// cannot produce and the browser pass checks instead.
//
// The load-bearing test in this file is the pair at the end of "the viewport rectangle":
// `viewportRect` returning null has two entirely different meanings — "nothing is off screen"
// and "nobody has measured anything yet" — and a map that drew the rectangle in the second
// case would be stating, with a confident 2px accent frame, a fact it does not have.

// A workspace `wide` viewports across and `tall` down, scrolled to (`sx`, `sy`) viewports,
// on a 1000x500 screen. Named in the units the model thinks in so that a case reads as the
// workspace it is about rather than as four pixel counts.
function screenOf(wide: number, tall: number, sx = 0, sy = 0): ScrollMetrics {
  return {
    clientW: 1000,
    clientH: 500,
    scrollW: 1000 * wide,
    scrollH: 500 * tall,
    scrollLeft: 1000 * sx,
    scrollTop: 500 * sy,
  };
}

describe("mini map: the viewport rectangle", () => {
  it("is a fraction of the workspace, positioned where the scroll is", () => {
    // Three viewports wide, scrolled one across: the screen is the middle third.
    const v = viewportRect(screenOf(3, 1, 1))!;
    expect(v).toBeTruthy();
    expect(v.w).toBeCloseTo(1 / 3, 10);
    expect(v.x).toBeCloseTo(1 / 3, 10);
    // Nothing is off screen vertically, so the rectangle spans the whole height. NOT null:
    // one axis overflowing is still something to say, and a rectangle that vanished because
    // the other axis fitted would leave the horizontal overflow undrawn.
    expect(v.h).toBe(1);
    expect(v.y).toBe(0);
  });

  it("says nothing when the whole workspace is on screen", () => {
    expect(viewportRect(screenOf(1, 1))).toBeNull();
  });

  // clientWidth is a rounded integer and scrollWidth is a ceiling, so a workspace that fits
  // exactly reports a pixel of overflow it does not have. Without the slack the map would
  // frame the entire map — an emphatic way of saying nothing.
  it("treats a pixel of rounding as fitting, not as overflow", () => {
    expect(viewportRect({ ...NO_METRICS, clientW: 1084, clientH: 800, scrollW: 1085, scrollH: 801 })).toBeNull();
    // …but a real one percent of overflow is overflow.
    const v = viewportRect({ ...NO_METRICS, clientW: 1000, clientH: 800, scrollW: 1010, scrollH: 801 });
    expect(v).toBeTruthy();
    expect(v!.w).toBeCloseTo(1000 / 1010, 10);
  });

  // The other meaning of null, and the reason this is a separate test from the one above:
  // an unmeasured container reports zeros, and 0/0 is NaN. A rectangle at NaN% renders as an
  // invalid style — nothing at all in a browser, which would look like the deliberate "it all
  // fits" case while being an accident.
  it("says nothing at all before anything has been measured", () => {
    expect(viewportRect(NO_METRICS)).toBeNull();
    expect(viewportRect({ ...NO_METRICS, clientW: 1000, clientH: 500 })).toBeNull();
  });

  it("stays inside the map when the scroll offset does not", () => {
    // Rubber-banding on a trackpad reports a negative offset, and past-the-end reports more
    // than there is. Either one hangs the rectangle off the edge of the map, where it reads
    // as a broken map rather than as a gesture in the workspace.
    const back = viewportRect({ ...screenOf(3, 1), scrollLeft: -120 })!;
    expect(back.x).toBe(0);
    const past = viewportRect({ ...screenOf(3, 1), scrollLeft: 99999 })!;
    expect(past.x + past.w).toBeCloseTo(1, 10);
  });
});

describe("mini map: the box it is drawn in", () => {
  // The map's shape IS information — a workspace four screens wide should look four screens
  // wide — so the box keeps the true proportions and gives up size to do it. Asserted as a
  // ratio rather than as two numbers, because the ratio is the claim.
  it("keeps the workspace's proportions, whichever budget binds", () => {
    // Wide: the width budget binds, and the height gives way.
    const wide = mapBox(screenOf(4, 1), UNIT_EXT);
    expect(wide.w).toBe(MAP_MAX_W_REM);
    expect(wide.w / wide.h).toBeCloseTo(4000 / 500, 10);
    expect(wide.h).toBeLessThan(MAP_MAX_H_REM);

    // Tall: the height budget binds, and the width gives way. A map of a tall workspace is a
    // narrow column, which is what a tall workspace is.
    const tall = mapBox(screenOf(1, 4), UNIT_EXT);
    expect(tall.h).toBe(MAP_MAX_H_REM);
    expect(tall.w / tall.h).toBeCloseTo(1000 / 2000, 10);
    expect(tall.w).toBeLessThan(MAP_MAX_W_REM);
  });

  it("never exceeds either budget", () => {
    for (const m of [screenOf(1, 1), screenOf(8, 1), screenOf(1, 8), screenOf(3, 2)]) {
      const b = mapBox(m, UNIT_EXT);
      expect(b.w).toBeLessThanOrEqual(MAP_MAX_W_REM + 1e-9);
      expect(b.h).toBeLessThanOrEqual(MAP_MAX_H_REM + 1e-9);
      expect(b.w).toBeGreaterThan(0);
      expect(b.h).toBeGreaterThan(0);
    }
  });

  // Before the first measurement the model is all there is, and the extent is the model's own
  // statement of shape. Deliberately not a guess at the screen's aspect: a fabricated 16:9
  // would make an unmeasured map look authoritative.
  it("falls back to the extent's own ratio when nothing has been measured", () => {
    const ext: Ext = { w: 3, h: 1 };
    const b = mapBox(NO_METRICS, ext);
    expect(b.w / b.h).toBeCloseTo(3, 10);
  });

  // A degenerate extent cannot reach here through any code path in the app, and the box must
  // still be a box: `width: NaNrem` is an invalid style, so the map would render at its
  // content size and every tile inside it — placed in percentages — would collapse.
  it("is a real box even for an impossible extent", () => {
    for (const ext of [{ w: 0, h: 0 }, { w: 1, h: 0 }, { w: -2, h: 1 }]) {
      const b = mapBox(NO_METRICS, ext);
      expect(Number.isFinite(b.w) && b.w > 0).toBe(true);
      expect(Number.isFinite(b.h) && b.h > 0).toBe(true);
    }
  });
});

// THE NUMBERS INSIDE A CELL. A tile is a stack, so it can hold several panes sharing one place
// on screen; the cell draws every one of their numbers, because the numbers are what tie a
// rectangle to a row and to a tab. When they do not all fit, an ellipsis stands in for the rest.
//
// Two invariants carry this, and they are asserted over a matrix rather than over examples:
// what is drawn never exceeds the room there is (or the cell overflows, silently, on exactly
// the crowded tiles the elision exists for), and the pane the cell stands for is always among
// the numbers shown (or the map stops reporting during the Tab walk it is being read for).
describe("mini map: the numbers in a cell", () => {
  // What the window costs to draw: the bullets plus each ellipsis, which takes a slot of its own.
  const slots = (w: BulletWindow) => w.to - w.from + (w.head ? 1 : 0) + (w.tail ? 1 : 0);

  const one = BULLET_REM + CELL_PAD_REM;
  const two = 2 * BULLET_REM + BULLET_GAP_REM + CELL_PAD_REM;

  it("measures the cell in both directions, by the same rule", () => {
    // Across: one bullet plus its padding exactly, a hair under it, and two with the gap.
    expect(bulletGrid(one, 8).cols).toBe(1);
    expect(bulletGrid(one - 0.01, 8).cols).toBe(0);
    expect(bulletGrid(two, 8).cols).toBe(2);
    // Down: the same three, which is the whole of what makes a tall cell roomier.
    expect(bulletGrid(8, one).rows).toBe(1);
    expect(bulletGrid(8, one - 0.01).rows).toBe(0);
    expect(bulletGrid(8, two).rows).toBe(2);
    // THE GAP IS PART OF THE MEASURE, on both axes. Room for two bullets and nothing between
    // them is room for one: the second would sit against the first, and on the vertical axis
    // it would sit outside the cell. Without this pair, dropping the gap from either
    // calculation over-counts by a row or a column and the cell quietly overflows.
    expect(bulletGrid(2 * BULLET_REM + CELL_PAD_REM, 8).cols).toBe(1);
    expect(bulletGrid(8, 2 * BULLET_REM + CELL_PAD_REM).rows).toBe(1);
  });

  // THE POINT OF THE GRID. The numbers wrap, so the height of a cell is room like its width
  // is: a tall cell holds several rows of them, and eliding while that height sat empty was
  // throwing away the space the map has to say something with.
  it("holds more in a tall cell than in a short one the same width", () => {
    const short = bulletRoom(6, one);
    const tall = bulletRoom(6, 4);
    expect(short).toBeGreaterThan(0);
    expect(tall).toBeGreaterThan(short);
    // And it is exactly the rows that made the difference — not a wider fit on the same line.
    expect(bulletGrid(6, 4).cols).toBe(bulletGrid(6, one).cols);
    expect(tall).toBe(short * bulletGrid(6, 4).rows);
  });

  // A cell can be wide and too SHORT — several deep vertical splits in an 8rem map. A clipped
  // glyph is worse than an empty rectangle, and a clipped ellipsis is worse still, so no row
  // means no room, however much width there is.
  it("holds none at all in a cell too short for one, however wide", () => {
    expect(bulletRoom(20, one)).toBeGreaterThan(0);
    expect(bulletRoom(20, BULLET_REM)).toBe(0);
    expect(bulletRoom(20, 0)).toBe(0);
    expect(bulletRoom(0, 20)).toBe(0);
  });

  it("draws every number when they all fit, and no ellipsis", () => {
    const w = bulletWindow(3, 5, 0);
    expect([w.from, w.to]).toEqual([0, 3]);
    expect(w.head || w.tail).toBe(false);
    // Exactly enough is enough — an off-by-one here would elide a cell that fits.
    expect(bulletWindow(3, 3, 2)).toEqual({ from: 0, to: 3, head: false, tail: false });
  });

  it("elides from the end when the kept pane is near the start", () => {
    // Four tabs, room for three: two numbers and the ellipsis that stands for the rest.
    expect(bulletWindow(4, 3, 0)).toEqual({ from: 0, to: 2, head: false, tail: true });
    expect(bulletWindow(4, 3, 1)).toEqual({ from: 0, to: 2, head: false, tail: true });
  });

  // The window SLIDES. Without this a Tab walk moves the highlight out of the cell and the map
  // goes quiet exactly while it is being read — the failure the window exists to prevent, and
  // the one a "first N then ellipsis" rule would have shipped.
  it("slides to the end when the kept pane is there", () => {
    expect(bulletWindow(4, 3, 3)).toEqual({ from: 2, to: 4, head: true, tail: false });
    expect(bulletWindow(4, 3, 2)).toEqual({ from: 2, to: 4, head: true, tail: false });
  });

  it("pays for both ellipses in the middle", () => {
    const w = bulletWindow(6, 3, 3);
    expect(w.head && w.tail).toBe(true);
    expect(w.to - w.from).toBe(1); // one slot each to the two ellipses
    expect(w.from).toBe(3);
  });

  it("says only 'there are more' when a single slot is all there is", () => {
    expect(bulletWindow(4, 1, 0)).toEqual({ from: 0, to: 0, head: false, tail: true });
    // …but one slot and one pane is a number, not an ellipsis.
    expect(bulletWindow(1, 1, 0)).toEqual({ from: 0, to: 1, head: false, tail: false });
  });

  it("draws nothing whatever in a cell with no room", () => {
    expect(bulletWindow(4, 0, 0)).toEqual({ from: 0, to: 0, head: false, tail: false });
  });

  // The two invariants, over every shape a cell can be in. Examples above name the cases; this
  // is what says there are no others.
  it("never draws more than it has room for, and never hides the pane the cell stands for", () => {
    for (let count = 1; count <= 9; count++) {
      for (let room = 0; room <= 10; room++) {
        for (let keep = 0; keep < count; keep++) {
          const w = bulletWindow(count, room, keep);
          const where = `count=${count} room=${room} keep=${keep}`;
          expect(slots(w), `${where} overflows`).toBeLessThanOrEqual(room);
          expect(w.from, where).toBeGreaterThanOrEqual(0);
          expect(w.to, where).toBeLessThanOrEqual(count);
          // Whenever a number is drawn at all, the kept one is among them. When none is drawn
          // there is nothing to promise — that is the "…" alone, and the empty cell.
          if (w.to > w.from) {
            expect(keep >= w.from && keep < w.to, `${where} hides the kept pane`).toBe(true);
          }
          // An ellipsis is drawn only where something is actually cut off, so it never lies
          // about there being more.
          expect(w.head, `${where} head`).toBe(w.from > 0);
          if (w.to > w.from) expect(w.tail, `${where} tail`).toBe(w.to < count);
        }
      }
    }
  });
});
