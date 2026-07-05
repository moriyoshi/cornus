import { describe, it, expect } from "vitest";
import { TERM_FONT_PX, termFontPx } from "./Term";
import { ZOOM_SCALES } from "../views/tiling/zoom";

// The only part of a terminal's zoom a test can reach. The Terminal instance is local to
// Term's onMount and stays there on purpose (nothing outside may hold a reference to a
// terminal that is mid-teardown), so what the effect writes into `term.options.fontSize` is
// not observable — this covers the number it writes, and the wiring around it is one line.
describe("termFontPx", () => {
  it("leaves an unzoomed terminal at exactly the size it has always been", () => {
    expect(termFontPx(1)).toBe(TERM_FONT_PX);
  });

  it("gives every step of the zoom table a distinct size", () => {
    // The point of the check: a step that rounded to the same px as its neighbour would be a
    // press with no effect, and the user would read the whole feature as unreliable rather
    // than that one step as a no-op. It is what ties the shape of ZOOM_SCALES to the fact
    // that a terminal's sizes are integers.
    const sizes = ZOOM_SCALES.map(termFontPx);
    expect(new Set(sizes).size).toBe(ZOOM_SCALES.length);
    expect(sizes).toEqual([...sizes].sort((a, b) => a - b));
  });

  it("never hands xterm a size that would break its cell measurement", () => {
    // A zero or negative divides by zero in the fit addon's cols/rows calculation. The scale
    // table cannot produce one today; the floor is here because this is the boundary and the
    // failure is a blank pane rather than an error anyone could read.
    expect(termFontPx(0)).toBeGreaterThan(0);
    expect(termFontPx(-3)).toBeGreaterThan(0);
    expect(termFontPx(0.0001)).toBe(6);
  });
});
