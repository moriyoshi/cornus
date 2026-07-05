// The pane chooser's MINI MAP: the workspace drawn small, so that "which pane?" can be
// answered by pointing at a place instead of by reading a list.
//
// It exists because the workspace outgrew the screen. Under the extending layout a split
// makes the canvas bigger rather than making the source pane smaller (see ./grow), so by the
// third or fourth pane most of the workspace is off screen — and the chooser's list, which is
// ORDERED but not POSITIONED, can no longer say where a pane is. "Row 5 of 7" is not an answer
// to "where"; a rectangle in the upper right is. The list stays: it carries the names, the
// tabs and the refusals, none of which fit in a 30px box. The map carries the geometry, which
// is the one thing the list never could.
//
// Only the geometry lives here, for the reason everything geometric in this tree is split this
// way: jsdom lays nothing out, so a number that comes from a real box can only be checked in a
// browser pass. What is arithmetic is here and is tested; what is measured is one call to
// `metricsOf` at the edge, and the browser pass is what confirms it was wired up.

import type { Ext } from "./layout";

// A rectangle in the same 0..1 fractions `tileRects` speaks, so the map can draw a tile and
// the screen with one rule and they cannot drift apart.
export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

// What a scroll container can say about itself. Taken as plain numbers rather than as the
// element, so that the single measurement happens at one named place and everything after it
// is arithmetic a unit test can reach.
export interface ScrollMetrics {
  clientW: number;
  clientH: number;
  scrollW: number;
  scrollH: number;
  scrollLeft: number;
  scrollTop: number;
}

// What an unmeasured container looks like — jsdom, and the frame before mount. Every function
// here treats it as "no measurement", never as "a zero-sized workspace".
export const NO_METRICS: ScrollMetrics = {
  clientW: 0,
  clientH: 0,
  scrollW: 0,
  scrollH: 0,
  scrollLeft: 0,
  scrollTop: 0,
};

export function metricsOf(el: HTMLElement | null | undefined): ScrollMetrics {
  if (!el) return NO_METRICS;
  return {
    clientW: el.clientWidth,
    clientH: el.clientHeight,
    scrollW: el.scrollWidth,
    scrollH: el.scrollHeight,
    scrollLeft: el.scrollLeft,
    scrollTop: el.scrollTop,
  };
}

// `clientWidth` is a rounded integer and `scrollWidth` is a ceiling, so a workspace that
// exactly fits its container routinely reports 1084 against 1085. Without this slack the map
// would draw a viewport rectangle around the entire map on a workspace with nothing off
// screen at all — an emphatic way of saying nothing.
const FITS_EPS = 0.005;

// Where the SCREEN is within the workspace, as fractions of the whole — the map's one piece of
// information that no other part of the UI states. null when there is nothing to say: either
// the container has never been measured, or the whole workspace is on screen and a rectangle
// around all of it is noise.
//
// Measured rather than derived from `ext`: `ext` is the model's extent, while the map's job
// here is to report what the user can actually see, dividers, scrollbars, rounding and all. The
// two agree to within a divider's width, and where they disagree the measurement is right.
export function viewportRect(m: ScrollMetrics): Box | null {
  if (!(m.scrollW > 0) || !(m.scrollH > 0)) return null;
  const w = Math.min(1, m.clientW / m.scrollW);
  const h = Math.min(1, m.clientH / m.scrollH);
  if (w >= 1 - FITS_EPS && h >= 1 - FITS_EPS) return null;
  // Clamped into the box on both ends: overscroll (rubber-banding on a trackpad) reports a
  // negative scrollLeft, and a rectangle hanging off the edge of the map reads as a bug in
  // the map rather than as a gesture in the workspace.
  const x = Math.min(Math.max(m.scrollLeft / m.scrollW, 0), 1 - w);
  const y = Math.min(Math.max(m.scrollTop / m.scrollH, 0), 1 - h);
  return { x, y, w, h };
}

// The map's size budget, in rem. The width is what the chooser panel can spare (it is 22rem
// wide with `--space-2` of padding each side); the height is a judgement — tall enough that a
// tile is a shape rather than a line, short enough that the map does not push the list it
// annotates off the bottom of the panel.
export const MAP_MAX_W_REM = 20;
export const MAP_MAX_H_REM = 8;

// How big to draw the map, preserving the workspace's true proportions.
//
// Both budgets are respected by shrinking, never by stretching: a 6-way horizontal split is
// nearly 6:1, and a map drawn 6:1 at full width would be a 3rem-tall strip — which is exactly
// what this returns, and exactly what that workspace looks like. The alternative, clamping the
// aspect into a comfortable band, would draw a workspace shaped unlike the one it maps, and
// the shape is a good half of what the map is for.
export function mapBox(m: ScrollMetrics, ext: Ext): { w: number; h: number } {
  const a = aspectOf(m, ext);
  const w = Math.min(MAP_MAX_W_REM, MAP_MAX_H_REM * a);
  return { w, h: w / a };
}

// The workspace's width-to-height ratio. Measured when there is a measurement — that is the
// true shape of the canvas — and the extent's own ratio when there is not, which is jsdom and
// the frame before mount. The fallback is deliberately NOT "assume a 16:9 screen": inventing a
// number would make the map look right in a test that never measured anything, which is the
// one situation where it should look obviously provisional.
function aspectOf(m: ScrollMetrics, ext: Ext): number {
  const a = m.scrollW > 0 && m.scrollH > 0 ? m.scrollW / m.scrollH : ext.w / ext.h;
  return Number.isFinite(a) && a > 0 ? a : 1;
}

// ---- the numbers inside a cell ---------------------------------------------------------
//
// A tile is a STACK, so it can hold several panes sharing one place on screen, and each of
// them has a number. The cell shows them all — the numbers are what tie a rectangle to a row
// and to a tab, and a cell that showed one of four would be naming the tile rather than
// describing it. When they do not all fit, the ones that are dropped are replaced by an
// ellipsis, which is the honest thing to draw: "there are more of these, and no room to say
// which".
//
// Everything below is in rem, and it is arithmetic rather than measurement for the reason the
// rest of this module is: a cell's width is `rect.w * mapBox().w`, known before anything is
// rendered. That is only true because the bullet is sized in rem too — `.pane-chooser-map`
// sets its own font in rem so that `.pane-number`'s `1.6em` is exactly BULLET_REM whatever the
// root font size is. If that rule is changed to a px font, these numbers stop meaning
// anything and a cell will quietly overflow instead of eliding.

// `.pane-number` is a 1.6em SQUARE, and the map's font is 0.75rem — so one constant serves
// both axes.
export const BULLET_REM = 1.2;
// The gap between two of them (`.pane-chooser-map-tile { gap }`), across and down alike.
export const BULLET_GAP_REM = 0.15;
// The cell's own 1px borders plus a hair, so a bullet is never flush against an edge.
export const CELL_PAD_REM = 0.2;

// How many bullets fit ACROSS a cell and DOWN it. The numbers wrap, so a tall cell holds
// several rows of them — the vertical space is there, and a cell that elided while leaving it
// empty would be throwing away the room it has to say something with.
export function bulletGrid(w: number, h: number): { cols: number; rows: number } {
  // The `+ gap` is the trailing gap the last bullet in a line does not need; flooring keeps
  // the rest. Zero on either axis when not even one fits, which is how a cell too short for a
  // single row ends up with no room at all rather than one clipped line.
  const fit = (extent: number) =>
    Math.max(0, Math.floor((extent - CELL_PAD_REM + BULLET_GAP_REM) / (BULLET_REM + BULLET_GAP_REM)));
  return { cols: fit(w), rows: fit(h) };
}

// The whole grid, which is what `bulletWindow` spends. Zero when the cell cannot hold even one
// bullet — a single clipped glyph is worse than an empty rectangle, and a clipped ellipsis is
// worse still.
export function bulletRoom(w: number, h: number): number {
  const { cols, rows } = bulletGrid(w, h);
  return cols * rows;
}

// Which of a stack's numbers a cell can show, and where the ellipses go. `from`/`to` index the
// stack's panes; `head`/`tail` are the ellipses standing in for what is cut off either side.
export interface BulletWindow {
  from: number;
  to: number;
  head: boolean;
  tail: boolean;
}

// The window SLIDES to keep `keep` — the pane the cell stands for, which is the visible tab, or
// whatever Tab has walked to on the previewed tile — inside it. Anchored at the start when it
// can be, at the end when the kept pane is near it, and centred otherwise. Without the slide
// the map would stop reporting exactly when it is being read: a Tab walk through five tabs in a
// cell that fits two would move the highlight straight out of view.
//
// An ellipsis costs a slot, which is why the anchored cases get one more bullet than the
// centred case: there is nothing cut off at the end they are anchored to.
export function bulletWindow(count: number, room: number, keep: number): BulletWindow {
  const none = { from: 0, to: 0, head: false, tail: false };
  if (count < 1 || room < 1) return none;
  if (room >= count) return { from: 0, to: count, head: false, tail: false };
  // One slot, more than one pane: the ellipsis is all there is room to say.
  if (room < 2) return { from: 0, to: 0, head: false, tail: true };
  const i = Math.min(Math.max(keep, 0), count - 1);
  const size = room - 1;
  if (i < size) return { from: 0, to: size, head: false, tail: true };
  if (i >= count - size) return { from: count - size, to: count, head: true, tail: false };
  const mid = room - 2;
  if (mid < 1) return { from: 0, to: 0, head: false, tail: true };
  // Centred on the kept pane, then held off both ends — a window touching either end would
  // contradict the ellipsis drawn there.
  const from = Math.min(Math.max(i - ((mid - 1) >> 1), 1), count - mid - 1);
  return { from, to: from + mid, head: true, tail: true };
}
