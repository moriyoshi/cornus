// Keeping the view still while the workspace changes size in front of it.
//
// The extending workspace lays out from a fixed origin, so a tile inserted BEFORE the one you are
// looking at does not push the workspace's left edge leftwards — it pushes everything after it to
// the right. With `scrollLeft` untouched that is a sideways lurch of the entire screen, and it
// happens at the exact moment the user is watching the pane that was supposed to stay put. The
// same is true of a `before` split on the vertical axis, of a drag onto a left or top edge, and
// of `Grow pane left`.
//
// So: measure a tile that must not move, run the mutation, measure it again, and put the
// difference back into the scroll offset.
//
// WHY MEASURE rather than compute `scrollLeft += delta * clientWidth`. The arithmetic version has
// to be right about three separate things — which side of the scroll origin the insertion landed
// on, the conversion from viewport extents to pixels, and sub-pixel rounding — and it is silently
// wrong in exactly the nested cases that are hardest to reason about, where "wrong" means the
// screen jumps and no test can see it. Measuring an element that must not move is right by
// construction and has no cases.
//
// WHICH tile: the one nearest the container's top-left corner, i.e. whatever the user currently
// has under the origin of their view. Not the focused tile — by the time a split commits, focus
// has already moved to the new pane, which has no element yet — and not a tile threaded in by
// each caller, which would be four chances to pass the wrong one. "Keep what is on screen on
// screen" is the property wanted, and the tile at the origin is the one that states it.
//
// This runs SYNCHRONOUSLY around the commit. Solid is fine-grained and applies DOM updates during
// the store write rather than queueing them, so the second measurement sees the new layout and
// the correction lands before the browser paints. A requestAnimationFrame here would paint one
// frame of the jump first, which is the entire artefact being removed.

// The corner-most visible tile, and where it sits relative to the scroll container.
interface Anchor {
  id: string;
  dx: number;
  dy: number;
}

function anchorOf(scroller: HTMLElement): Anchor | null {
  const host = scroller.getBoundingClientRect();
  let best: Anchor | null = null;
  let bestScore = Infinity;
  for (const el of scroller.querySelectorAll<HTMLElement>(".stack[data-stack-id]")) {
    const id = el.dataset.stackId;
    if (!id) continue;
    const r = el.getBoundingClientRect();
    // Scrolled entirely past: it is not on screen, so holding it still would hold the wrong
    // thing still.
    if (r.right <= host.left || r.bottom <= host.top) continue;
    const dx = r.left - host.left;
    const dy = r.top - host.top;
    const score = dx * dx + dy * dy;
    if (score < bestScore) {
      bestScore = score;
      best = { id, dx, dy };
    }
  }
  return best;
}

// withScrollAnchor runs `mutate` and then corrects the scroll offset so the anchor tile is where
// it was. Everything about it is best-effort: no container, no tiles, or an anchor that the
// mutation removed (it was the tile that just closed) all fall through to running the mutation
// and leaving the scroll position alone — which is the behaviour without this module, not a
// broken one.
export function withScrollAnchor(scroller: HTMLElement | null | undefined, mutate: () => void): void {
  if (!scroller) {
    mutate();
    return;
  }
  const before = anchorOf(scroller);
  mutate();
  if (!before) return;
  // Re-queried rather than held: a re-tile rebuilds the tile's element under the same id, so the
  // node measured first is often detached by now and would report a stale rect of zeros.
  const el = scroller.querySelector<HTMLElement>(`.stack[data-stack-id="${CSS.escape(before.id)}"]`);
  if (!el) return;
  const host = scroller.getBoundingClientRect();
  const r = el.getBoundingClientRect();
  scroller.scrollLeft += r.left - host.left - before.dx;
  scroller.scrollTop += r.top - host.top - before.dy;
}
