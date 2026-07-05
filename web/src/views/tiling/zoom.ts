// How big a pane's contents are drawn, per pane.
//
// A module singleton, in the same idiom as command-center.ts and settings.ts, and for a
// reason particular to the tiling: a pane's COMPONENT is not stable. Splitting a tile
// re-parents its sibling, which rebuilds that sibling's TermPane and its xterm from
// scratch, and the same happens to every pane a rearrange moves. A zoom kept in component
// state would therefore be lost by the neighbour of any tile the user split — a pane the
// user did not touch, silently snapping back to 13px. Keyed by pane id outside the tree,
// it survives every remount and is only ever dropped deliberately (see keepZooms).
//
// NOT persisted, unlike settings.ts. It is a reading posture rather than a preference — the
// pane you leaned into for one log line — and the layout blob is reloaded on a fresh page
// where the pane ids are the same but the sessions behind them are not.

import { createRoot, createSignal, createEffect, onCleanup } from "solid-js";
import { pinchZoom } from "../../pinch";
import { settings } from "../../settings";

// The steps, as a closed table rather than a factor applied continuously. THIS is what
// makes a live pinch cheap: xterm is only re-fitted (and the PTY only told a new size) when
// a step boundary is crossed, so a slow spread across the whole range costs ten resizes
// rather than one per animation frame. It also makes every step reachable by the keyboard
// and by the gesture identically, which a free-running factor could not promise.
//
// At the terminal's default 13px base these round to 8, 9, 10, 12, 13, 15, 17, 20, 23, 26,
// 31px — all distinct, so no step is a no-op. That property now has to hold at every base
// the Terminal font size setting offers, not just this one, which is why TERM_FONT_SIZES
// starts at 10: below it the small end of this table collides against termFontPx's 6px
// floor and the first two or three steps become keypresses that do nothing.
// termFont.test.ts asserts it across the whole of TERM_FONT_SIZES.
export const ZOOM_SCALES: readonly number[] = [0.6, 0.7, 0.8, 0.9, 1, 1.15, 1.3, 1.5, 1.75, 2, 2.4];

// The index whose scale is exactly 1 — "not zoomed", the value every pane starts at and
// the one Reset returns to. Asserted against ZOOM_SCALES in the tests rather than left as a
// number two constants have to agree on by hand.
export const ZOOM_HOME = 4;

const clampStep = (step: number) => Math.min(ZOOM_SCALES.length - 1, Math.max(0, Math.round(step)));

export function scaleOf(step: number): number {
  return ZOOM_SCALES[clampStep(step)];
}

const store = createRoot(() => {
  // Only panes that are actually zoomed appear here: resetZoom DELETES rather than writing
  // ZOOM_HOME back. So the record answers "which panes are zoomed" as well as "how much",
  // which is what makes the prune below observable and keeps it from growing a permanent
  // entry for every pane ever opened.
  const [levels, setLevels] = createSignal<Record<string, number>>({});
  return { levels, setLevels };
});

export const zoomLevels = store.levels;

// Reactive: a component reading this re-renders when its own pane is zoomed. Panes not in
// the record read as ZOOM_HOME, so a pane never has to be registered before it can be read.
export function zoomStep(paneId: string): number {
  return zoomLevels()[paneId] ?? ZOOM_HOME;
}

export function zoomScale(paneId: string): number {
  return scaleOf(zoomStep(paneId));
}

// Whether `paneId` is at either end of the table, which is what the Zoom in / Zoom out
// commands disable themselves on. Asked here rather than compared against ZOOM_SCALES.length
// at the call site: the table's ends are this module's business.
export function canZoom(paneId: string, delta: number): boolean {
  const at = zoomStep(paneId);
  return clampStep(at + delta) !== at;
}

export function nudgeZoom(paneId: string, delta: number): void {
  setZoomStep(paneId, zoomStep(paneId) + delta);
}

export function setZoomStep(paneId: string, step: number): void {
  const next = clampStep(step);
  store.setLevels((prev) => {
    if ((prev[paneId] ?? ZOOM_HOME) === next) return prev; // no signal write, no re-render
    const out = { ...prev };
    if (next === ZOOM_HOME) delete out[paneId];
    else out[paneId] = next;
    return out;
  });
}

export function resetZoom(paneId: string): void {
  setZoomStep(paneId, ZOOM_HOME);
}

// Drops every entry whose pane is gone. Called from the one effect in Workspace that already
// deep-tracks the tree, rather than from a close handler: panes also leave through movePane,
// moveStack, and the auto-close a shell's exit performs, and a hook on any one of those is a
// leak waiting for the next route to be added.
export function keepZooms(liveIds: Set<string>): void {
  store.setLevels((prev) => {
    const stale = Object.keys(prev).filter((id) => !liveIds.has(id));
    if (stale.length === 0) return prev;
    const out = { ...prev };
    for (const id of stale) delete out[id];
    return out;
  });
}

// ---------------------------------------------------------------------------------------
// Wiring a pane's content root up to all of this
// ---------------------------------------------------------------------------------------

// zoomable attaches the pinch gesture to a pane's content root. Called from the component's
// `ref`, which is the same idiom the tiling already uses to hang behaviour off an element it
// has just created (panes.tsx does it with dropTarget).
//
// The setting is read INSIDE the effect, not around the call, so switching it off releases
// the listeners and puts the element's touch-action back on the spot — the browser gets its
// native pinch back without a reload, which is the least a preference this invasive owes the
// user who changes their mind.
export function zoomable(el: HTMLElement, paneId: () => string): void {
  createEffect(() => {
    if (!settings().paneZoom) return;
    onCleanup(pinchZoom(el, (delta) => nudgeZoom(paneId(), delta)));
  });
}

// The custom property the CSS reads, or nothing at all when the pane is at rest. Deliberately
// absent rather than "1": every rule that reads it spells the fallback `var(--pane-zoom, 1)`,
// so an unzoomed pane renders through exactly the declarations it always did, and "the
// feature left no trace on a pane nobody zoomed" is a thing a test can see.
export function zoomStyle(paneId: string): Record<string, string> {
  const scale = zoomScale(paneId);
  return scale === 1 ? {} : { "--pane-zoom": String(scale) };
}
