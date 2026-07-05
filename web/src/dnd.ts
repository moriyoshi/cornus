// One drag-and-drop vocabulary, two transports.
//
// The browser has exactly one drag gesture and it is the mouse's: HTML5 drag-and-drop is
// `dragstart` / `dragover` / `drop`, and no mobile browser synthesises any of them from a
// finger (iPadOS Safari is the one exception, and only there). Everything in this app that
// could be dragged was therefore mouse-only — a tab onto another tile, a file row into a
// folder — and touch was left with the command routes instead.
//
// The answer is not a second implementation per gesture. It is this layer. A consumer
// declares a DRAG SOURCE and a DROP TARGET in terms of what the drag MEANS, and the module
// decides how it is delivered:
//
//   "auto"     — a mouse press uses the real HTML5 drag, so everything that lives outside
//                the page keeps working (dropping OS files in, dragging a file out to the
//                desktop); a finger gets the emulated one. Files uses this.
//   "emulated" — pointer events on every device, native DnD not registered at all. The
//                tiling chrome uses this: a tab rearrange is entirely in-page, so the one
//                thing native buys is the one thing it does not need, and a single path
//                means the gesture cannot work on one pointer type and not the other.
//
// THE EMULATED PATH MIRRORS NATIVE SEMANTICS rather than inventing its own, so a consumer
// can be written once and read once:
//
//   - Targets NEST, and one that refuses (`over` returning false) hands the point to its
//     ancestor — which is exactly what `dragover` bubbling does when a handler declines to
//     call preventDefault. Files depends on this: a file row is not a drop target, so its
//     point falls through to the pane's own folder.
//   - The payload is typed strings keyed like `dataTransfer`.
//   - A drop with no accepting target is a no-op, not a cancel.
//
// What it cannot mirror is what the browser does not hand to a page: OS files and the
// drag-out download. Those stay native, and `via` on every event is how a consumer says so
// — Files asks a touch drop whether it is a copy or a move, because a finger has no Shift.

import { createSignal } from "solid-js";

// Which transport delivered an event. Consumers should need this for exactly one thing:
// deciding what to do about the modifier keys a finger does not have.
export type DragVia = "native" | "emulated";

export type DropEffect = "copy" | "move";

// The payload, in the shape both transports can honestly answer.
export interface DragPayload {
  types: readonly string[];
  has: (type: string) => boolean;
  // The value for a type. NOT READABLE DURING A HOVER on the native path — the browser
  // puts the dataTransfer in "protected mode" until the drop, so a dragover can ask what
  // is being carried but not what it says. It returns "" there rather than throwing, so
  // one shape covers both phases; a consumer that needs the value must wait for `drop`.
  get: (type: string) => string;
  // OS files, native drops only. Empty everywhere else.
  files: readonly File[];
}

// A point in a live drag, as a drop target sees it.
export interface DragPoint {
  x: number;
  y: number;
  payload: DragPayload;
  // Always false under a finger — which is the whole reason `via` is here.
  shiftKey: boolean;
  via: DragVia;
}

// What a source hands over when the drag actually starts.
export interface DragLift {
  // Keyed like dataTransfer: MIME-ish type -> string.
  data: Record<string, string>;
  effects?: "copy" | "move" | "copyMove";
  // The drag image. The native path snapshots it (setDragImage) and drops it on the next
  // tick; the emulated one appends it and flies it under the finger until the drag ends.
  // Either way the module owns its lifetime — a ghost outliving its drag is a permanent
  // artefact stuck to the page.
  ghost?: HTMLElement;
  // Where the pointer sits within the ghost. The default keeps it just below-right of the
  // finger, where it is not covered by it.
  ghostAt?: { x: number; y: number };
}

export interface DragSourceSpec {
  // Called once, when the drag begins. Returning null refuses it — the press was not
  // carrying anything after all.
  start: (via: DragVia) => DragLift | null;
  // Called when it is over, dropped or not. The counterpart of `dragend`.
  end?: (via: DragVia) => void;
  transport?: "auto" | "emulated";
  // A press landing on one of these never starts a drag. Controls inside a drag handle are
  // the case: the tab bar is draggable and carries a ✕ and a ⋮, and a long press on either
  // must press the button rather than pick up the tile.
  ignore?: string;
}

export interface DropTargetSpec {
  // Whether this target will take the point. FALSE REFUSES IT, and refusing is not the same
  // as ignoring it: the point then goes to the enclosing target, exactly as an un-prevented
  // `dragover` bubbles.
  //
  // MUST BE PURE. It is asked speculatively, of targets the drag then goes on to leave, and
  // it is what orders the hand-over: the module calls the old target's `leave` before the
  // new one's `over`, which it can only do by knowing who the new one is BEFORE anything is
  // drawn. Two sibling tiles writing one shared "where would this land" signal is exactly
  // the case that breaks if the two run the other way round.
  accepts: (p: DragPoint) => boolean;
  // Draw the hover feedback for a point this target has accepted.
  over: (p: DragPoint) => void;
  // The drag left this target — either for another one or off the page. Clear the feedback.
  leave: () => void;
  drop: (p: DragPoint) => void;
  // The cursor the native drag shows. Defaults to the Shift convention Files uses (copy,
  // or move while Shift is held); a target where only one of them is possible says so.
  effect?: (p: DragPoint) => DropEffect;
  transport?: "auto" | "emulated";
}

// How long a finger must hold still before the press becomes a drag. A MOUSE DOES NOT WAIT
// — it lifts as soon as it has moved — because a mouse has nothing else to do with a
// press-and-move, while for a finger that gesture is how the page scrolls. The dwell is the
// only thing that tells the two apart, and it is the reason the tab bar and the file
// listing can still be panned.
export const DRAG_LIFT_MS = 400;
// How far the pointer may stray during the dwell before the press is read as a scroll (and,
// for a mouse, how far it must travel before the press becomes a drag at all).
export const DRAG_SLOP_PX = 8;
// Set on <html> for the duration of an emulated drag: the cursor and the fact that nothing
// on the page should be selecting text while one is in flight.
export const DRAGGING_CLASS = "dnd-dragging";

const DEFAULT_IGNORE = "button, input, select, textarea";

// Every registered target, keyed by its element. A WeakMap and not a list because the hit
// test walks UP from the element under the pointer: it asks each ancestor whether it is a
// target, which is the same walk the browser does when it bubbles a dragover, and it needs
// no ordering of its own to get nesting right.
const targets = new WeakMap<Element, DropTargetSpec>();

const payloadOfData = (data: Record<string, string>): DragPayload => {
  const types = Object.keys(data);
  return {
    types,
    has: (t) => types.includes(t),
    get: (t) => data[t] ?? "",
    files: [],
  };
};

// `atDrop` is what unlocks the values: see DragPayload.get.
const payloadOfTransfer = (dt: DataTransfer | null, atDrop: boolean): DragPayload => {
  const types = dt ? Array.from(dt.types) : [];
  return {
    types,
    has: (t) => types.includes(t),
    get: (t) => (atDrop && dt ? dt.getData(t) : ""),
    files: dt ? Array.from(dt.files ?? []) : [],
  };
};

const nativePoint = (ev: DragEvent, atDrop: boolean): DragPoint => ({
  x: ev.clientX,
  y: ev.clientY,
  payload: payloadOfTransfer(ev.dataTransfer, atDrop),
  shiftKey: ev.shiftKey,
  via: "native",
});

// ---------------------------------------------------------------------------------------
// Drop targets
// ---------------------------------------------------------------------------------------

// dropTarget registers `el` and returns its disposer. Call it from a `ref` and hand the
// disposer to onCleanup — a target left registered after its element is gone would keep
// answering hit tests against a detached rect.
export function dropTarget(el: HTMLElement, spec: DropTargetSpec): () => void {
  targets.set(el, spec);
  if ((spec.transport ?? "auto") === "emulated") return () => targets.delete(el);

  const onOver = (ev: DragEvent) => {
    const p = nativePoint(ev, false);
    if (!spec.accepts(p)) return; // refused: let it bubble to whatever encloses us
    // Both, and in this order: preventDefault is what makes the element a drop target at
    // all, stopPropagation is what stops the ancestor answering the same point twice.
    ev.preventDefault();
    ev.stopPropagation();
    if (ev.dataTransfer) {
      ev.dataTransfer.dropEffect = spec.effect ? spec.effect(p) : ev.shiftKey ? "move" : "copy";
    }
    spec.over(p);
  };
  const onLeave = (ev: DragEvent) => {
    // dragleave also fires when the pointer crosses into a CHILD of this element, and
    // clearing the feedback then would flicker it on every internal boundary.
    if (!el.contains(ev.relatedTarget as globalThis.Node | null)) spec.leave();
  };
  const onDrop = (ev: DragEvent) => {
    const p = nativePoint(ev, true);
    ev.preventDefault();
    ev.stopPropagation();
    spec.drop(p);
  };
  el.addEventListener("dragover", onOver);
  el.addEventListener("dragleave", onLeave);
  el.addEventListener("drop", onDrop);
  return () => {
    targets.delete(el);
    el.removeEventListener("dragover", onOver);
    el.removeEventListener("dragleave", onLeave);
    el.removeEventListener("drop", onDrop);
  };
}

// ---------------------------------------------------------------------------------------
// Drag sources
// ---------------------------------------------------------------------------------------

// dragSource attaches both transports to `el` and returns its disposer. It deliberately
// does NOT set `draggable`: Files toggles that per press to keep the band-select gesture
// alive, and the tiling has no native drag to enable.
export function dragSource(el: HTMLElement, spec: DragSourceSpec): () => void {
  const native = (spec.transport ?? "auto") === "auto";
  const ignore = spec.ignore ?? DEFAULT_IGNORE;

  const onDragStart = (ev: DragEvent) => {
    // iPadOS Safari is the one browser that BOTH honours HTML5 drag-and-drop from a finger
    // and sends the pointer events this module's emulated path is built on. The lift's
    // touchmove veto normally stops the native drag ever starting; if one starts anyway,
    // the emulated drag is the one already carrying the payload, and two drags of the same
    // thing would double every `start` the source performs.
    if (dragging()) {
      ev.preventDefault();
      return;
    }
    const lift = spec.start("native");
    if (!lift || !ev.dataTransfer) {
      ev.preventDefault();
      return;
    }
    for (const [type, value] of Object.entries(lift.data)) ev.dataTransfer.setData(type, value);
    ev.dataTransfer.effectAllowed = lift.effects ?? "move";
    if (lift.ghost) {
      // setDragImage snapshots synchronously, so the node has to be in the document and
      // must never be seen there. Its own stylesheet parks it off-screen; the removal is
      // on the next task, once the snapshot has been taken.
      document.body.appendChild(lift.ghost);
      ev.dataTransfer.setDragImage(lift.ghost, lift.ghostAt?.x ?? 12, lift.ghostAt?.y ?? 12);
      const ghost = lift.ghost;
      setTimeout(() => ghost.remove(), 0);
    }
  };
  const onDragEnd = () => spec.end?.("native");
  const onPointerDown = (ev: PointerEvent) => {
    // The mouse belongs to the native transport wherever there is one.
    if (native && ev.pointerType === "mouse") return;
    if (ev.button > 0) return; // a right- or middle-press is not a drag
    if ((ev.target as Element | null)?.closest(ignore)) return;
    press(el, ev, spec);
  };

  if (native) {
    el.addEventListener("dragstart", onDragStart);
    el.addEventListener("dragend", onDragEnd);
  }
  el.addEventListener("pointerdown", onPointerDown);
  return () => {
    if (native) {
      el.removeEventListener("dragstart", onDragStart);
      el.removeEventListener("dragend", onDragEnd);
    }
    el.removeEventListener("pointerdown", onPointerDown);
    // A source torn down mid-drag takes the drag with it: its `start` closure is over a
    // component that no longer exists, and every later move would be measured against it.
    if (live?.el === el) abort();
  };
}

// ---------------------------------------------------------------------------------------
// The emulated drag itself
// ---------------------------------------------------------------------------------------

// The one press in flight. A module singleton because a drag IS singular — two fingers
// dragging two tabs into each other has no meaning the layout could express — and because
// the nesting guard below depends on there being one: a press on a tab and a press on the
// tab bar behind it are the same gesture arriving at two registered sources, and the first
// (innermost, since pointerdown bubbles) is the one that wins.
interface LivePress {
  el: HTMLElement;
  spec: DragSourceSpec;
  pointerId: number;
  startX: number;
  startY: number;
  // Whether the press has become a drag. Until it has, nothing has been told anything.
  lifted: boolean;
  // Whether the pointer waits for a dwell (a finger) or lifts on movement (a mouse).
  dwell: boolean;
  timer?: ReturnType<typeof setTimeout>;
  payload?: DragPayload;
  ghost?: HTMLElement;
  ghostAt: { x: number; y: number };
  // The target currently accepting the point, so `leave` is called exactly once when it
  // stops being that target.
  hovered?: { el: Element; spec: DropTargetSpec };
  stop: () => void;
}
let live: LivePress | null = null;

// The teardown for a click guard waiting to fire, or null. Module-level so a new press can
// retire one that is still standing — see armClickSwallow.
let unswallow: (() => void) | null = null;

// A completed TOUCH drag must not also be a click: the browser synthesises one from the
// touch sequence, and it would land on whatever the finger was released over — activating a
// tab the user was only dropping onto, or toggling the selection of a row they had just
// moved a file into.
//
// Only for touch. A mouse press-move-release synthesises its click on the nearest common
// ancestor of the press and the release, which for a real drag is the workspace (no handler)
// and for a drag that never left the source is the source itself (whose click is the
// activate that press already performed). Nothing to swallow, and one fewer listener that
// can outlive its gesture.
//
// Three ways out, because there is no guarantee the click ever comes: the click itself, the
// next press, and the task boundary.
function armClickSwallow() {
  unswallow?.();
  const swallow = (e: MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    retire();
  };
  // Retires THIS guard, never whichever one happens to be current: a second drag arms its
  // own, and a stale timer calling `unswallow?.()` would take the new one down with it.
  const retire = () => {
    if (unswallow === retire) unswallow = null;
    clearTimeout(timer);
    window.removeEventListener("click", swallow, true);
  };
  const timer = setTimeout(retire, 0);
  unswallow = retire;
  window.addEventListener("click", swallow, true);
}

// Whether an emulated drag is in flight. REACTIVE, and it has to be: the tiling chrome's
// edge-split strips arm on a hover dwell driven by `pointermove`, and an emulated drag emits
// those all the way across the workspace — where the native drag it replaced emitted none at
// all. Without a signal the tiles cannot stand that dwell down while a tab is travelling, and
// the two gestures fight over the same pixels. It also lets a native path stand itself down
// on iPadOS, the one place both transports could fire.
const [lifted, setLifted] = createSignal(false);
export const dragging: () => boolean = lifted;

// Retires a press that has NOT yet become a drag, for a gesture that has decided the press
// was meant for it instead (the tiles' touch hold on a split edge). A lifted drag is left
// alone: it is the gesture the user is already performing, and nothing outside it gets to
// take that away — the caller checks `dragging()` and stands down.
export function cancelPendingDrag(): void {
  if (live && !live.lifted) abort();
}

// Takes the press EXPLICITLY rather than reading `live`: the drop is delivered after the
// gesture has been retired (finish clears `live` first, so nothing a consumer does from
// inside `drop` can find a half-torn-down drag), and reading the module slot there would
// hand the drop an empty payload — the one event that most needs a full one.
const pointOf = (l: LivePress, x: number, y: number): DragPoint => ({
  x,
  y,
  payload: l.payload ?? payloadOfData({}),
  // A finger has no modifiers. Stated once, here, rather than left as an accident of the
  // event shape.
  shiftKey: false,
  via: "emulated",
});

// hitTest walks up from the element under the pointer asking every target it passes,
// innermost first, and stops at the one that takes the point — the same walk, and the same
// stopping rule, as a bubbling dragover. Returns null when nothing accepts.
function hitTest(p: DragPoint): { el: Element; spec: DropTargetSpec } | null {
  let el: Element | null = document.elementFromPoint?.(p.x, p.y) ?? null;
  while (el) {
    const spec = targets.get(el);
    if (spec?.accepts(p)) return { el, spec };
    el = el.parentElement;
  }
  return null;
}

function track(x: number, y: number) {
  if (!live) return;
  if (live.ghost) live.ghost.style.transform = `translate(${x - live.ghostAt.x}px, ${y - live.ghostAt.y}px)`;
  const p = pointOf(live, x, y);
  const found = hitTest(p);
  // LEAVE THE OLD ONE FIRST, then draw the new one. The two routinely write the same state
  // — two tiles share one "where would this land" signal, a folder row and the pane behind
  // it share one highlight — and in the other order the departing target wipes what the
  // arriving one just set. (The native path has the same hazard, which is why its dragleave
  // is guarded on relatedTarget.)
  if (live.hovered && live.hovered.el !== found?.el) live.hovered.spec.leave();
  live.hovered = found ?? undefined;
  found?.spec.over(p);
}

// finish tears the gesture down exactly once, and is the only path that does: drop, cancel,
// Escape and a source unmounting mid-drag all come through here, so the ghost, the
// listeners, the <html> class and the source's own `end` cannot be left behind by any of
// them.
function finish(dropAt: { x: number; y: number } | null) {
  const l = live;
  if (!l) return;
  live = null;
  l.stop();
  if (l.lifted) {
    if (dropAt && l.hovered) l.hovered.spec.drop(pointOf(l, dropAt.x, dropAt.y));
    // The target is told the drag left it whether or not it took it, so a consumer's
    // `leave` is the ONE place its hover feedback is torn down — a drop path that had to
    // remember to clear its own highlight is a highlight that outlives a failed transfer.
    l.hovered?.spec.leave();
    l.ghost?.remove();
    document.documentElement.classList.remove(DRAGGING_CLASS);
    setLifted(false);
    l.spec.end?.("emulated");
  }
}

const abort = () => finish(null);

function lift(l: LivePress, x: number, y: number) {
  const carried = l.spec.start("emulated");
  if (!carried) {
    abort();
    return;
  }
  l.lifted = true;
  setLifted(true);
  l.payload = payloadOfData(carried.data);
  // The same meaning as setDragImage's two numbers, so a source describes its ghost once
  // and both transports place it the same way: where the pointer sits INSIDE the ghost.
  if (carried.ghostAt) l.ghostAt = carried.ghostAt;
  if (carried.ghost) {
    l.ghost = carried.ghost;
    // Positioned here rather than in a stylesheet: these four are what make it a thing
    // FLYING over the page rather than a thing in it, and the one that matters is
    // pointer-events — a ghost under the finger that answered the hit test would be the
    // only drop target the drag could ever find.
    Object.assign(l.ghost.style, {
      position: "fixed",
      left: "0",
      top: "0",
      pointerEvents: "none",
      zIndex: "90",
    });
    document.body.appendChild(l.ghost);
  }
  document.documentElement.classList.add(DRAGGING_CLASS);
  track(x, y);
}

function press(el: HTMLElement, ev: PointerEvent, spec: DragSourceSpec) {
  if (live) return; // the innermost source already claimed this press
  // A new press means the click the last gesture was braced for is never coming.
  unswallow?.();
  const pointerId = ev.pointerId;
  const l: LivePress = {
    el,
    spec,
    pointerId,
    startX: ev.clientX,
    startY: ev.clientY,
    lifted: false,
    dwell: ev.pointerType !== "mouse",
    ghostAt: { x: 12, y: 12 },
    stop: () => {},
  };

  const strayed = (e: PointerEvent) =>
    Math.abs(e.clientX - l.startX) > DRAG_SLOP_PX || Math.abs(e.clientY - l.startY) > DRAG_SLOP_PX;

  const onMove = (e: PointerEvent) => {
    // A second finger must not steer this drag.
    if (e.pointerId !== l.pointerId) return;
    if (l.lifted) {
      track(e.clientX, e.clientY);
      return;
    }
    if (!strayed(e)) return;
    // Movement means opposite things to the two pointers. For a mouse it IS the drag
    // starting; for a finger, arriving before the dwell has elapsed, it is the page being
    // scrolled — and a scroll that had been quietly counting down towards a drag would
    // lift one mid-fling.
    if (l.dwell) abort();
    else lift(l, e.clientX, e.clientY);
  };
  const onUp = (e: PointerEvent) => {
    if (e.pointerId !== l.pointerId) return;
    finish(l.lifted ? { x: e.clientX, y: e.clientY } : null);
  };
  const onCancel = (e: PointerEvent) => {
    // The browser can take the pointer away without ever sending pointerup: a touch
    // promoted to a scroll, an incoming call, a capture stolen. Terminal, and not a drop.
    if (e.pointerId === l.pointerId) abort();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    abort();
  };
  // Scrolling and the drag are the same finger, and once the drag has it the page must
  // hold still. touch-action cannot say this: the tab bar and the file listing MUST stay
  // pannable, which is the whole reason the dwell exists, so the veto has to arrive with
  // the lift rather than at the stylesheet. A non-passive listener is what makes
  // preventDefault mean anything here — Chrome makes document-level touchmove passive
  // otherwise, and the call is silently ignored. It also suppresses the native drag on
  // iPadOS, the one browser where both transports could otherwise fire at once.
  const onTouchMove = (e: TouchEvent) => {
    if (l.lifted && e.cancelable) e.preventDefault();
  };
  // A long press is also how a touch device asks for the context menu (and, on a link, the
  // share sheet). The gesture is ours from the moment it starts, so the menu is refused for
  // its whole duration — including the dwell, since the menu fires at about the same time
  // the drag lifts.
  const onContextMenu = (e: Event) => e.preventDefault();
  l.stop = () => {
    clearTimeout(l.timer);
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    window.removeEventListener("pointercancel", onCancel);
    window.removeEventListener("keydown", onKey, true);
    document.removeEventListener("touchmove", onTouchMove);
    window.removeEventListener("contextmenu", onContextMenu, true);
    // Asked before released: releasePointerCapture throws when the pointer is no longer
    // active, which is exactly the case after a pointerup or a pointercancel.
    if (el.hasPointerCapture?.(pointerId)) el.releasePointerCapture(pointerId);
    if (l.lifted && l.dwell) armClickSwallow();
  };

  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", onUp);
  window.addEventListener("pointercancel", onCancel);
  window.addEventListener("keydown", onKey, true);
  document.addEventListener("touchmove", onTouchMove, { passive: false });
  window.addEventListener("contextmenu", onContextMenu, true);
  // Optional-call: jsdom implements no pointer capture, and a hard call would throw out of
  // the handler before a single move was tracked.
  el.setPointerCapture?.(pointerId);
  live = l;
  if (l.dwell) l.timer = setTimeout(() => lift(l, l.startX, l.startY), DRAG_LIFT_MS);
}
