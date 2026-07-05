import { describe, expect, it } from "vitest";
import { revealShift } from "./reveal";

// The scroll a reveal asks for, on one axis, as arithmetic — which is the only place it can be
// checked at all: jsdom lays nothing out, so a test driven through the DOM can prove that the
// container was scrolled and not by how much. Everything below is stated in the vocabulary of
// what the user sees (a tile off to the left, a tile wider than the screen) rather than in the
// vocabulary of the branches, so it still means something if the branches are rewritten.
//
// The convention under test: the returned number is ADDED to the scroll offset, so a negative
// answer moves the view towards the workspace's origin (left/up) and a positive one away from it.
describe("revealShift", () => {
  // A 400px scrollport starting at the viewport's origin, which is what every case below is
  // measured against.
  const host = { start: 0, end: 400 };

  it("leaves something already fully visible exactly where it is", () => {
    expect(revealShift({ start: 0, end: 400 }, host)).toBe(0); // flush with both edges
    expect(revealShift({ start: 100, end: 300 }, host)).toBe(0);
  });

  // "nearest": the least scroll that makes it fully visible. This is what keeps a walk across a
  // workspace two screens wide from jumping a screen per step.
  it("scrolls a tile that fits only as far as its nearest edge", () => {
    // 100px off the left edge — the view moves 100px left, and no further.
    expect(revealShift({ start: -100, end: 200 }, host)).toBe(-100);
    // 50px past the right edge.
    expect(revealShift({ start: 150, end: 450 }, host)).toBe(50);
    // Entirely past the right edge: still only far enough to sit against it.
    expect(revealShift({ start: 900, end: 1100 }, host)).toBe(700);
  });

  // THE REGRESSION. A tile can be wider than the screen — the golden sizing rule grows new tiles
  // — and the browser's own "nearest" brings the closer edge in, which walking LEFTWARDS is the
  // tile's RIGHT edge: the view pans, and stops with the pane's head still off screen. Measured
  // in a browser at gapLeft -273, gapRight 0 before this function existed.
  it("shows the start edge of a tile too big to fit, however it was approached", () => {
    // Approached from the RIGHT: 600px of tile, 400px of screen, left edge 500px off screen.
    // The browser's own rule answers -300 here — right edge flush against the scrollport's,
    // left edge still 200px away — which is the reported bug: it pans, and it pans too little.
    expect(revealShift({ start: -500, end: 100 }, host)).toBe(-500);
    // Already covering the whole scrollport, head off screen: still worth the pan, because a
    // rectangle of pane with no tab bar, no number plate and no corner belongs to no tile in
    // particular.
    expect(revealShift({ start: -300, end: 700 }, host)).toBe(-300);
    // Approached from the LEFT, where "nearest" already agreed: align the head, not the tail.
    expect(revealShift({ start: 600, end: 1200 }, host)).toBe(600);
  });

  // Exactly as wide as the screen is not "too big": it fits, and the nearest-edge branch says
  // the same thing the start-edge one would. Pinned because the comparison is a strict `>` and
  // an off-by-one there is invisible everywhere else.
  it("treats a tile the width of the screen as fitting", () => {
    expect(revealShift({ start: -120, end: 280 }, host)).toBe(-120);
    expect(revealShift({ start: 120, end: 520 }, host)).toBe(120);
  });

  // Right to left, where the inline axis starts at the RIGHT edge. Only the oversized branch
  // asks: "the least scroll that makes it fully visible" is the same distance whichever way the
  // text runs. Nothing renders rtl today (the app sets no `dir`), which is exactly why this is
  // pinned here rather than left to be noticed.
  it("shows the right edge of an oversized tile when the container runs right to left", () => {
    // The same tile as the first oversized case above: ltr shows its left edge, rtl its right.
    expect(revealShift({ start: -500, end: 100 }, host, false)).toBe(-300);
    // And a fitting tile is untouched by the direction.
    expect(revealShift({ start: -100, end: 200 }, host, false)).toBe(-100);
    expect(revealShift({ start: 150, end: 450 }, host, false)).toBe(50);
  });

  // The block axis has no direction question — a tile's head is its top — so the caller passes
  // nothing and gets the start edge.
  it("defaults to the start edge, which is what the block axis wants", () => {
    const tall = { start: -500, end: 100 };
    expect(revealShift(tall, host)).toBe(revealShift(tall, host, true));
  });
});
