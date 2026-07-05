// Bringing the thing that just got marked into view — the tile out on the workspace, and the
// row for it in the chooser's list. Two containers, one policy (see `revealShift` below), kept
// together because they are two halves of one guarantee: a walk that is invisible in EITHER
// place is a walk the user has to take on trust.
//
// Once the workspace can be bigger than the screen, a pane can be focused while entirely
// off it — the pane a split just made, the pane `prefix o` walked to, the pane a chooser
// committed. Focus that lands somewhere the user cannot see is worse than no focus at all: the
// keyboard is now somewhere else and nothing on screen says so.
//
// This runs AFTER the scroll anchor (see ./anchor). The two look like they are fighting over the
// same scroll offset and are in fact a sequence: anchoring keeps the view still through the
// layout change, so the only movement left is this one, which is deliberate and is about where
// the user asked to go. Reversed, the anchor would undo the reveal and the new pane would stay
// off screen.

// "nearest" on both axes: scroll the least that makes the target fully visible, which is niri's
// behaviour and the reason a workspace two screens wide does not jump a whole screen when focus
// moves one tile. Something already fully visible is left exactly where it is — which is what
// makes this safe to call on every step of a walk, and what keeps a short list that fits from
// ever scrolling at all.
//
// The chooser's ROWS only. The tiles used to be scrolled by the same call and are not any more;
// see `revealShift` for the one case where "nearest" is the wrong answer, which a row — never
// taller than the list it is in — cannot be in.
const HOW: ScrollIntoViewOptions = { block: "nearest", inline: "nearest" };

// One end of a box on one axis, in viewport px: `start` is the lower coordinate (left or top)
// and `end` the higher, whatever the reading direction.
export interface Span {
  start: number;
  end: number;
}

// How far to scroll one axis to bring `el` into `host`, as px to ADD to the scroll offset
// (positive moves the view towards higher coordinates — right, or down). Zero means the target
// is already visible and nothing should move.
//
// `startsLow` says which end of the span is the target's START edge: true for the block axis,
// and for the inline axis of a left-to-right container; false in a right-to-left one, where the
// inline axis starts on the right. It is read in exactly one branch, because it is the only one
// that has to CHOOSE an edge rather than compute one.
export function revealShift(el: Span, host: Span, startsLow = true): number {
  // BIGGER THAN THE SCREEN, where the browser's own "nearest" is wrong for this app — measured,
  // not reasoned. A split multiplies the workspace by φ and hands the new tile a third of it, so
  // tiles GROW: by the fourth split on a 1400px window a single pane is wider than the viewport
  // (see .agents/docs/TODO.md, "The golden sizing rule outgrows the screen"). CSSOM's "nearest"
  // aligns whichever edge is closer, so walking LEFTWARDS onto such a tile left it right-edge
  // flush with its left edge 273px off screen: the view panned, and panned too little.
  //
  // Only one edge can be shown, and it is the START edge — not an arbitrary pick between two
  // equally poor answers. Everything that says WHICH tile this is lives there: the first tab of
  // the bar, the number plate the chooser's rows and its mini map are numbered against, and the
  // corner where the focus ring closes. Landing in the middle of an oversized pane shows a
  // rectangle of content that could belong to any of them.
  if (el.end - el.start > host.end - host.start) {
    return startsLow ? el.start - host.start : el.end - host.end;
  }
  // Otherwise "nearest", exactly as HOW describes it: the least scroll that makes the target
  // fully visible, and none at all when it already is.
  if (el.start < host.start) return el.start - host.start;
  if (el.end > host.end) return el.end - host.end;
  return 0;
}

function smooth(): boolean {
  // matchMedia is absent in jsdom unless a test stubs it; the honest default for "can I animate
  // this" when the environment will not say is no.
  const mq = globalThis.matchMedia?.("(prefers-reduced-motion: reduce)");
  return !mq?.matches;
}

// revealTile scrolls the tile holding `stackId` into view within `scroller`.
//
// It takes a stack id rather than an element because the caller is an effect on the focused PANE
// and the element it wants is the tile's, which the pane does not have a reference to — and
// because a re-tile rebuilds that element, so an element captured earlier is the wrong one.
export function revealTile(scroller: HTMLElement | null | undefined, stackId: string): void {
  if (!scroller || !stackId) return;
  // DEFERRED BY A MICROTASK, and this is load-bearing rather than tidy-looking.
  //
  // A pane claims the keyboard when it becomes the focused one (views/tiling/focusclaim.ts),
  // and calling .focus() on an element makes the BROWSER scroll it into view — natively,
  // synchronously, and with no say from us. That happens during the same Solid flush as this
  // effect, so a reveal called inline is immediately overwritten by the browser's own idea of
  // where to scroll. Measured, not assumed: a near-edge split moved the view by 1284px with
  // this module doing nothing at all.
  //
  // The native scroll is not a bad answer, it is answering a different question — it aims at
  // whatever element took focus (an xterm's textarea, one row of a listing), where the thing
  // that should be brought into view is the whole TILE. Deferring puts this last, so the tile
  // wins over the row, and prefers-reduced-motion is honoured rather than bypassed.
  //
  // Not fixable at the source: two of the four claims go through xterm's and CodeMirror's own
  // focus(), which take no `preventScroll`.
  queueMicrotask(() => {
    const el = scroller.querySelector<HTMLElement>(`.stack[data-stack-id="${CSS.escape(stackId)}"]`);
    if (!el) return;
    const host = scroller.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    // The container's COMPUTED direction, deliberately, and not ./gutter's `documentDirection`.
    // That one infers a reading direction from the browser's language in order to place a panel;
    // this is a question about the box the browser actually laid out. They disagree exactly where
    // it would hurt — an Arabic locale on a document with no `dir` renders ltr — and scrolling
    // the wrong way is worse than not scrolling at all. jsdom reports "", which is not "rtl".
    const ltr = globalThis.getComputedStyle?.(scroller).direction !== "rtl";
    const x = { start: box.left, end: box.right };
    const y = { start: box.top, end: box.bottom };
    const left = revealShift(x, { start: host.left, end: host.right }, ltr);
    const top = revealShift(y, { start: host.top, end: host.bottom });
    // SCROLLED HERE, rather than by asking the tile to scroll itself into view. scrollIntoView
    // takes an alignment out of a fixed vocabulary and "nearest" is the wrong word for an
    // oversized tile (see revealShift); it also walks the ancestor chain, which for this element
    // means every `overflow: hidden` split-child on the way up and, above them, the page.
    //
    // Relative, not absolute: `scrollLeft`'s origin is the one thing that differs between the two
    // directions (negative in rtl), and a delta means the same in both. Measured and applied in
    // the same task, so a smooth scroll already in flight is re-aimed from where it has got to
    // rather than from where it started.
    //
    // UNCONDITIONAL, and an early return for `left === top === 0` is a bug rather than a saving.
    // A commit clears the preview and moves the focus as two writes, so this effect runs twice —
    // once for the tile being LEFT, then once for the tile chosen — and the first run has already
    // aimed a smooth scroll at the wrong tile by the time the second measures. Skipping a
    // zero-distance call leaves that animation to run to completion: measured, a committed choice
    // flew 3647px to the pane the user had just walked away from. A zero scrollBy re-aims it at
    // the position it is passing through, which is the abort — and both microtasks run inside the
    // same task, so it happens before a frame is painted and nothing flickers.
    //
    // jsdom implements no layout and therefore no scrollBy; guarded rather than stubbed so the
    // tests that assert on the scroll can spy on it, and the rest are unaffected.
    scroller.scrollBy?.({ left, top, behavior: smooth() ? "smooth" : "auto" });
  });
}

// revealRow scrolls the chooser's marked row into view inside the panel's own list.
//
// The list scrolls (`.pane-chooser-list` is `overflow-y: auto`) because a workspace can hold
// more panes than a panel has room for, and the walk moves the highlight without touching the
// scroll offset — so on a long list the mark walks straight out of the box and the panel goes on
// reporting a position nobody can see. That is the same failure `revealTile` exists to prevent,
// one container in.
//
// NOT deferred, unlike revealTile: there is no browser scroll to lose a race with. A walk moves
// DOM focus nowhere — the panel holds it for the mode's whole length and the rows are not tab
// stops — so nothing else is scrolling this container.
//
// The panel is a sibling of the workspace's scroll container rather than inside it, so
// "nearest" can only move the list here. Were it ever nested in .workspace-body, this would
// start fighting revealTile for the same offset.
export function revealRow(row: HTMLElement | null | undefined): void {
  row?.scrollIntoView?.({ ...HOW, behavior: smooth() ? "smooth" : "auto" });
}
