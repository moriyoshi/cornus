// The pinch gesture, as a recognizer that knows nothing about panes.
//
// A peer of dnd.ts and pointer.ts, and the third thing in the app to ask a question about
// pointers — deliberately its own question again. dnd.ts asks "which pointer started this
// drag" (ev.pointerType, one pointer, the whole gesture); pointer.ts asks "what kind of
// device is this" (a media query, no gesture at all); this asks "are two fingers moving
// apart", which neither can answer because it is the only one that is about a RELATIONSHIP
// between two pointers rather than about one of them.
//
// It reports STEPS, not a factor. The caller wants a discrete zoom level (see
// views/tiling/zoom.ts) and a continuous factor would have to be quantised somewhere; doing
// it here means the re-baselining lives with the measurement it re-bases, so a slow
// continuous spread emits one step per threshold crossed instead of one per move event.

import { cancelPendingDrag } from "./dnd";

// How far the fingers must spread, as a ratio of their separation when the last step was
// emitted, before the next one is. Roughly one step per 18% — close enough to the gaps in
// ZOOM_SCALES that a pinch feels like it is dragging the size rather than clicking through
// a list, and far enough apart that a hand shake does not step.
export const PINCH_STEP_RATIO = 1.18;

// The wheel's equivalent. A trackpad pinch arrives as a wheel event with ctrlKey set (every
// engine does this; it is how the browser's own pinch-zoom is driven), so the same feature
// works on a laptop with no touchscreen and there is no second concept to explain. The
// deltas are accumulated because a trackpad emits many small ones where a mouse notch emits
// one large one, and a threshold applied per event would make the two wildly different.
export const WHEEL_STEP_DELTA = 40;

const distance = (a: { x: number; y: number }, b: { x: number; y: number }) =>
  Math.hypot(a.x - b.x, a.y - b.y);

// pinchZoom attaches to `el` and calls `onStep(+1 | -1)` each time the gesture crosses a
// step boundary. Returns its disposer.
export function pinchZoom(el: HTMLElement, onStep: (delta: 1 | -1) => void): () => void {
  // Only the touches that STARTED on this element. A pinch whose second finger landed on the
  // tile next door is not this pane's gesture, and tracking by id from our own pointerdown is
  // what says so — geometry could not, since the fingers are by definition far apart.
  const down = new Map<number, { x: number; y: number }>();
  // The separation the last step was measured from, or 0 when fewer than two fingers are
  // down. Re-based on every emit rather than kept as the gesture's start distance: without
  // that, a spread past two thresholds would emit the second step immediately after the
  // first and the gesture would run away.
  let base = 0;
  let wheelAcc = 0;

  const spread = () => {
    const [a, b] = [...down.values()];
    return distance(a, b);
  };

  const onPointerDown = (ev: PointerEvent) => {
    if (ev.pointerType !== "touch") return; // a mouse cannot pinch; two pens are not a pinch
    down.set(ev.pointerId, { x: ev.clientX, y: ev.clientY });
    if (down.size !== 2) return;
    // The second finger is what makes this a pinch, and it is the moment to say so: dnd.ts
    // may have a press waiting out its dwell (a tab bar sits above this element, and the
    // finger that started here could have landed on one), and a drag that lifted under a
    // pinch would carry a tab across the workspace while the user was only resizing text.
    // cancelPendingDrag leaves a LIFTED drag alone by design — that gesture is already the
    // user's, and this one stands down instead by never reaching two fingers first.
    cancelPendingDrag();
    base = spread();
  };

  const onPointerMove = (ev: PointerEvent) => {
    const at = down.get(ev.pointerId);
    if (!at) return;
    at.x = ev.clientX;
    at.y = ev.clientY;
    if (down.size !== 2 || base <= 0) return;
    const now = spread();
    // Loops rather than a single test: one move event can cross more than one boundary (a
    // fast spread, or a coarse synthetic sequence), and emitting only one step per event
    // would leave the zoom lagging behind the fingers with no way to catch up.
    while (now / base >= PINCH_STEP_RATIO) {
      base *= PINCH_STEP_RATIO;
      onStep(1);
    }
    while (base / now >= PINCH_STEP_RATIO) {
      base /= PINCH_STEP_RATIO;
      onStep(-1);
    }
  };

  const release = (ev: PointerEvent) => {
    if (!down.delete(ev.pointerId)) return;
    // A third finger is not a thing this gesture has a meaning for, so the rule is simply
    // that two fingers down re-base from wherever they are now. Lifting one of three
    // therefore resumes the pinch rather than ending it.
    base = down.size === 2 ? spread() : 0;
  };

  const onWheel = (ev: WheelEvent) => {
    if (!ev.ctrlKey) return; // an ordinary wheel still scrolls the pane
    ev.preventDefault();
    wheelAcc += ev.deltaY;
    while (wheelAcc <= -WHEEL_STEP_DELTA) {
      wheelAcc += WHEEL_STEP_DELTA;
      onStep(1); // deltaY is negative when the fingers spread / the wheel rolls up
    }
    while (wheelAcc >= WHEEL_STEP_DELTA) {
      wheelAcc -= WHEEL_STEP_DELTA;
      onStep(-1);
    }
  };

  // Safari's non-standard gesture events, and the ONLY thing done with them. Safari fires
  // them alongside the pointer events above, so recognising the pinch from them as well
  // would count every step twice; refusing gesturestart is purely how the page is stopped
  // from zooming itself underneath a gesture this element has claimed.
  const onGesture = (ev: Event) => ev.preventDefault();

  // touch-action is what tells the browser not to take the pinch for its own page zoom.
  // Inline rather than a rule in styles.css because it must apply only while the feature is
  // switched on: a standing rule would take the native pinch away from every user, which is
  // exactly the trade the setting exists to let them decline. `pan-x pan-y` and not `none` —
  // a zoomed editor and a zoomed image both have to stay scrollable with one finger.
  const restoreTouchAction = el.style.touchAction;
  el.style.touchAction = "pan-x pan-y";

  el.addEventListener("pointerdown", onPointerDown);
  el.addEventListener("wheel", onWheel, { passive: false });
  el.addEventListener("gesturestart", onGesture);
  el.addEventListener("gesturechange", onGesture);
  // Moves and releases on the WINDOW: a pinch that drifts off the pane mid-gesture is still
  // the same two fingers, and a pointerup delivered elsewhere would otherwise leave this
  // element believing they were still down.
  window.addEventListener("pointermove", onPointerMove);
  window.addEventListener("pointerup", release);
  window.addEventListener("pointercancel", release);

  return () => {
    el.style.touchAction = restoreTouchAction;
    el.removeEventListener("pointerdown", onPointerDown);
    el.removeEventListener("wheel", onWheel);
    el.removeEventListener("gesturestart", onGesture);
    el.removeEventListener("gesturechange", onGesture);
    window.removeEventListener("pointermove", onPointerMove);
    window.removeEventListener("pointerup", release);
    window.removeEventListener("pointercancel", release);
    down.clear();
  };
}
