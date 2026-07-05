// Two fingers pan the workspace.
//
// The extending workspace can be larger than the screen, and on a touch device there was no way
// to move it: ONE finger already belongs to whatever it lands on. A file listing scrolls, a
// terminal scrolls, the tab bar scrolls sideways, a divider resizes, a tab drags. Every pixel of
// the workspace is a pane, so a one-finger pan only ever reached the workspace by accident — when
// the surface underneath happened to have nothing to do with the gesture — and that is not an
// affordance, it is a coincidence the user cannot predict.
//
// So the workspace takes the gesture nobody else uses. Two fingers is what maps, canvases and
// image viewers all use for exactly this reason, and it composes rather than competes: the panes
// keep every one-finger gesture they had, unchanged.
//
// A peer of dnd.ts, pointer.ts and pinch.ts, and the fourth question this app asks about
// pointers — again its own. dnd.ts asks "which pointer started this drag"; pointer.ts asks "what
// kind of device is this"; pinch.ts asks "are two fingers moving APART"; this asks "are two
// fingers moving TOGETHER". The last two are the same two fingers and have to be told apart,
// which is the whole of the state machine below.

import { cancelPendingDrag } from "../../dnd";

// How far the fingers may drift apart, as a ratio of their separation when the second one
// landed, before this is a PINCH and not a pan. Deliberately loose: nobody holds two fingers at
// a constant distance while dragging, and a pan misread as a pinch stops dead under the user's
// hand. A real pinch passes 1.25 almost immediately — pinch.ts emits its first zoom step at 1.18.
export const PAN_PINCH_SLOP = 1.25;

// How far the midpoint must travel before this is a pan at all, in px. Until it does, the
// browser keeps the gesture — which is what preserves the native pinch-zoom for anyone who has
// not switched pane zoom on. Claiming the gesture on the second touchdown would take page zoom
// away from every user of every pane, and that is precisely the trade pinch.ts's setting exists
// to let them decline.
export const PAN_SLOP_PX = 6;

// What the two fingers have turned out to mean. One-way from "waiting": once a gesture has been
// read as a pinch it is never re-read as a pan, because a pinch that pans for a few frames on
// its way past the slop is a workspace that lurches every time someone zooms.
type Reading = "waiting" | "pan" | "pinch";

interface Point {
  x: number;
  y: number;
}

const distance = (a: Point, b: Point) => Math.hypot(a.x - b.x, a.y - b.y);

// twoFingerPan attaches to the SCROLL CONTAINER and returns its disposer.
export function twoFingerPan(el: HTMLElement): () => void {
  // Live touches, by pointer id. Only touches — a mouse has one pointer and a trackpad's
  // two-finger scroll arrives as a wheel event, which the container already handles natively.
  const down = new Map<number, Point>();
  // Which fingers have reported a move since the last time the gesture was judged. See onMove:
  // the separation between two fingers is only meaningful once both of them are where they
  // currently are.
  const moved = new Set<number>();
  let reading: Reading = "waiting";
  // The separation and the midpoint when the second finger landed, and the midpoint the last
  // move was measured from. `origin` is what the slop is measured against; `last` is what the
  // scroll delta is, and they diverge as soon as panning starts.
  let base = 0;
  let origin: Point = { x: 0, y: 0 };
  let last: Point = { x: 0, y: 0 };

  const both = () => [...down.values()] as [Point, Point];
  const spread = () => distance(...both());
  const middle = (): Point => {
    const [a, b] = both();
    return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
  };

  // CAPTURE, on all three. A pane may stop propagation of a pointer event it considers its own —
  // xterm and CodeMirror both handle pointers — and a listener that only saw what bubbled would
  // be deaf over exactly the panes this exists to pan across.
  const onDown = (ev: PointerEvent) => {
    if (ev.pointerType !== "touch") return;
    down.set(ev.pointerId, { x: ev.clientX, y: ev.clientY });
    if (down.size !== 2) return;
    // The second finger settles what this is, and dnd.ts may have a press waiting out its
    // 400ms dwell underneath — a tab, a file row. A drag that lifted under a two-finger pan
    // would carry a pane across the workspace while the user was only moving the view.
    // cancelPendingDrag leaves an already-LIFTED drag alone, which is right: that gesture is
    // the user's, and this one stands down by never reaching two fingers first.
    cancelPendingDrag();
    reading = "waiting";
    moved.clear();
    base = spread();
    origin = middle();
    last = origin;
  };

  const onMove = (ev: PointerEvent) => {
    const at = down.get(ev.pointerId);
    if (!at) return;
    at.x = ev.clientX;
    at.y = ev.clientY;
    if (down.size !== 2 || reading === "pinch") return;

    // WAIT FOR BOTH FINGERS before judging anything. A browser reports one pointer per event,
    // so halfway through a frame finger 1 has moved and finger 2 has not — and the separation
    // between them has apparently changed by the whole step. On a 100px grip a 30px pan reads
    // as a spread of 1.3, past the pinch slop, and a perfectly parallel two-finger drag is
    // rejected as a zoom on its very first move. Pairing the events is what makes the spread a
    // measurement of the FINGERS rather than of the event order.
    //
    // A second move for a finger that has already moved means the other one is simply resting,
    // which would otherwise deadlock this forever: judge on what is there.
    if (!moved.delete(ev.pointerId)) {
      moved.add(ev.pointerId);
      if (moved.size < 2) return;
    }
    moved.clear();

    const now = spread();
    // Fingers separating is a pinch, whatever the midpoint is doing. Checked before the pan is
    // claimed AND after, so a gesture that begins as a pan and turns into a zoom hands over.
    if (base > 0 && (now / base > PAN_PINCH_SLOP || base / now > PAN_PINCH_SLOP)) {
      reading = "pinch";
      return;
    }
    const at2 = middle();
    if (reading === "waiting") {
      if (distance(at2, origin) < PAN_SLOP_PX) return;
      reading = "pan";
      // Deliberately NOT re-based to `at2`: the travel that proved this was a pan is travel the
      // user made, and throwing it away makes the workspace start moving a few px behind the
      // fingers and stay there.
    }
    el.scrollLeft -= at2.x - last.x;
    el.scrollTop -= at2.y - last.y;
    last = at2;
  };

  const release = (ev: PointerEvent) => {
    if (!down.delete(ev.pointerId)) return;
    // Two fingers left of three: re-base and carry on, the same rule pinch.ts uses. Below two,
    // the gesture is over.
    moved.clear();
    if (down.size === 2) {
      base = spread();
      origin = middle();
      last = origin;
      reading = "waiting";
    } else {
      reading = "waiting";
      base = 0;
    }
  };

  // Only once the gesture IS a pan. A non-passive listener is what makes preventDefault mean
  // anything — Chrome makes document-level touchmove passive otherwise and the call is silently
  // ignored (the same note dnd.ts carries). Without it the browser scrolls or zooms underneath
  // the pan and the workspace moves twice as far as the fingers.
  const onTouchMove = (ev: TouchEvent) => {
    if (reading === "pan" && ev.cancelable) ev.preventDefault();
  };
  // Safari fires these alongside the pointer events for any two-finger gesture. Refused only
  // while panning, so a pinch still reaches pinch.ts and the browser's own page zoom still
  // works for anyone who has not claimed it.
  const onGesture = (ev: Event) => {
    if (reading === "pan") ev.preventDefault();
  };

  el.addEventListener("pointerdown", onDown, true);
  window.addEventListener("pointermove", onMove, true);
  window.addEventListener("pointerup", release, true);
  window.addEventListener("pointercancel", release, true);
  document.addEventListener("touchmove", onTouchMove, { passive: false });
  el.addEventListener("gesturestart", onGesture);
  el.addEventListener("gesturechange", onGesture);

  return () => {
    el.removeEventListener("pointerdown", onDown, true);
    window.removeEventListener("pointermove", onMove, true);
    window.removeEventListener("pointerup", release, true);
    window.removeEventListener("pointercancel", release, true);
    document.removeEventListener("touchmove", onTouchMove);
    el.removeEventListener("gesturestart", onGesture);
    el.removeEventListener("gesturechange", onGesture);
    down.clear();
    moved.clear();
  };
}
