import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The one collaborator, mocked so the interlock can be OBSERVED rather than assumed. A live
// dnd press is not otherwise visible from outside that module — `dragging()` only turns true
// once a drag has lifted, which is precisely the case cancelPendingDrag leaves alone — so
// without this the "a pinch takes the press away from a pending drag" claim would be a
// comment nothing checks.
vi.mock("./dnd", () => ({ cancelPendingDrag: vi.fn() }));

import { pinchZoom, PINCH_STEP_RATIO, WHEEL_STEP_DELTA } from "./pinch";
import { cancelPendingDrag } from "./dnd";

// jsdom implements no PointerEvent, so the pointer fields are defined onto a MouseEvent —
// the same rig views.test.tsx uses. Bubbling matters: the recognizer listens for moves and
// releases on the WINDOW, so an event dispatched at the element has to reach it.
function ptr(type: string, id: number, x: number, kind = "touch"): Event {
  const ev = new MouseEvent(type, { bubbles: true, clientX: x, clientY: 0 });
  Object.defineProperty(ev, "pointerId", { value: id });
  Object.defineProperty(ev, "pointerType", { value: kind });
  return ev;
}

let el: HTMLElement;
let steps: number[];
let stop: () => void;

beforeEach(() => {
  vi.mocked(cancelPendingDrag).mockClear();
  el = document.createElement("div");
  document.body.appendChild(el);
  steps = [];
  stop = pinchZoom(el, (d) => steps.push(d));
});
afterEach(() => {
  stop();
  el.remove();
});

// Two fingers at 0 and `apart`, then the second one moved so they are `to` apart.
const pinchFrom = (apart: number, ...to: number[]) => {
  el.dispatchEvent(ptr("pointerdown", 1, 0));
  el.dispatchEvent(ptr("pointerdown", 2, apart));
  for (const d of to) el.dispatchEvent(ptr("pointermove", 2, d));
};

describe("pinchZoom: two fingers", () => {
  it("emits a step once the fingers have spread past the ratio, and not before", () => {
    pinchFrom(100, 110); // 1.10 — inside the threshold
    expect(steps).toEqual([]);
    el.dispatchEvent(ptr("pointermove", 2, 120)); // 1.20 — past it
    expect(steps).toEqual([1]);
  });

  it("re-bases on each step, so a held spread does not run away", () => {
    // After the step at 120 the baseline is 100 * 1.18 = 118, so the NEXT step is at
    // 118 * 1.18 ≈ 139.2 rather than at another 20px. Without the re-base, 139 would be
    // 1.39 of the original 100 and would fire a second step it has not earned.
    pinchFrom(100, 120, 139);
    expect(steps).toEqual([1]);
    el.dispatchEvent(ptr("pointermove", 2, 140));
    expect(steps).toEqual([1, 1]);
  });

  it("emits one step per threshold crossed, not one per move event", () => {
    // THE test for the re-basing loop's shape. Twenty small moves that add up to one
    // threshold must be one step; a naive "did it grow since last time" would be twenty, and
    // a naive "compare against the start" would keep firing on every move past 118.
    const creep = Array.from({ length: 20 }, (_, i) => 100 + i + 1); // 101 … 120
    pinchFrom(100, ...creep);
    expect(steps).toEqual([1]);
  });

  it("catches up when one event crosses several thresholds at once", () => {
    // A fast spread, or a coarse synthetic sequence. Emitting only one step per event would
    // leave the zoom lagging the fingers with no way to close the gap.
    pinchFrom(100, 150); // 1.50 -> two thresholds (1.18, then 1.39)
    expect(steps).toEqual([1, 1]);
  });

  it("steps down when the fingers converge", () => {
    pinchFrom(100, 84); // 100/84 = 1.19
    expect(steps).toEqual([-1]);
  });

  it("takes a pending drag away from the press as the second finger lands", () => {
    expect(cancelPendingDrag).not.toHaveBeenCalled();
    el.dispatchEvent(ptr("pointerdown", 1, 0));
    expect(cancelPendingDrag).not.toHaveBeenCalled(); // one finger is still just a press
    el.dispatchEvent(ptr("pointerdown", 2, 100));
    expect(cancelPendingDrag).toHaveBeenCalledTimes(1);
  });

  it("stops measuring when a finger lifts, and does not step on the way back", () => {
    pinchFrom(100, 120);
    expect(steps).toEqual([1]);
    el.dispatchEvent(ptr("pointerup", 2, 120));
    // The remaining finger travelling a long way is a drag or a scroll, not a pinch.
    el.dispatchEvent(ptr("pointermove", 1, -400));
    expect(steps).toEqual([1]);
  });

  it("resumes from where the fingers now are when a cancelled pointer comes back", () => {
    pinchFrom(100, 120);
    el.dispatchEvent(ptr("pointercancel", 2, 120));
    el.dispatchEvent(ptr("pointerdown", 2, 300)); // a new finger, far away
    // Re-based at 300, so 300 -> 310 is nothing. If the baseline had survived the cancel,
    // this would fire immediately off a separation the user never made.
    el.dispatchEvent(ptr("pointermove", 2, 310));
    expect(steps).toEqual([1]);
  });
});

describe("pinchZoom: what is not a pinch", () => {
  it("ignores a single finger, however far it travels", () => {
    el.dispatchEvent(ptr("pointerdown", 1, 0));
    el.dispatchEvent(ptr("pointermove", 1, 500));
    expect(steps).toEqual([]);
  });

  it("ignores the mouse — two buttons are not two fingers", () => {
    el.dispatchEvent(ptr("pointerdown", 1, 0, "mouse"));
    el.dispatchEvent(ptr("pointerdown", 2, 100, "mouse"));
    el.dispatchEvent(ptr("pointermove", 2, 200, "mouse"));
    expect(steps).toEqual([]);
    expect(cancelPendingDrag).not.toHaveBeenCalled();
  });

  it("ignores fingers that started somewhere else", () => {
    // A pinch whose second finger landed on the tile next door is not this pane's gesture,
    // and only the id we saw come down here can say so — the two points are far apart by
    // definition, so geometry cannot.
    el.dispatchEvent(ptr("pointerdown", 1, 0));
    document.body.dispatchEvent(ptr("pointerdown", 2, 100));
    el.dispatchEvent(ptr("pointermove", 2, 200));
    expect(steps).toEqual([]);
  });
});

describe("pinchZoom: the trackpad", () => {
  const wheel = (deltaY: number, ctrlKey: boolean) => {
    const ev = new WheelEvent("wheel", { deltaY, ctrlKey, bubbles: true, cancelable: true });
    el.dispatchEvent(ev);
    return ev;
  };

  it("zooms on ctrl+scroll, and stops the browser doing its own", () => {
    const ev = wheel(-WHEEL_STEP_DELTA - 10, true);
    expect(steps).toEqual([1]);
    expect(ev.defaultPrevented).toBe(true);
  });

  it("leaves an ordinary scroll alone entirely", () => {
    const ev = wheel(-500, false);
    expect(steps).toEqual([]);
    expect(ev.defaultPrevented).toBe(false); // the pane still scrolls
  });

  it("accumulates small deltas rather than thresholding each event", () => {
    // A trackpad emits many small deltas where a mouse notch emits one large one. Per-event
    // thresholding would make the two feel like different features.
    const tenth = -WHEEL_STEP_DELTA / 10;
    for (let i = 0; i < 9; i++) wheel(tenth, true);
    expect(steps).toEqual([]);
    wheel(tenth, true);
    expect(steps).toEqual([1]);
  });

  it("zooms out on the other direction", () => {
    wheel(WHEEL_STEP_DELTA, true);
    expect(steps).toEqual([-1]);
  });
});

describe("pinchZoom: attachment", () => {
  it("claims the pinch from the browser while it is attached, and gives it back", () => {
    // touch-action is the whole reason this is opt-in: it is what tells the browser not to
    // take the gesture for its own page zoom. `pan-x pan-y` rather than `none`, because a
    // zoomed editor and a zoomed image both still have to scroll under one finger.
    expect(el.style.touchAction).toBe("pan-x pan-y");
    stop();
    stop = () => {}; // afterEach must not stop it twice

    // Asserted as a ROUND TRIP from a real value, not as "is it empty now": jsdom reports an
    // unset touch-action as undefined where a browser reports "", so an emptiness check would
    // pass here on a disposer that simply cleared the property — and clearing is wrong. An
    // element that already carried one (the coarse-pointer rules in styles.css put
    // `manipulation` on several) must get that one back.
    el.style.touchAction = "manipulation";
    const again = pinchZoom(el, () => {});
    expect(el.style.touchAction).toBe("pan-x pan-y");
    again();
    expect(el.style.touchAction).toBe("manipulation");
  });

  it("emits nothing at all once disposed", () => {
    stop();
    stop = () => {};
    pinchFrom(100, 200);
    expect(steps).toEqual([]);
  });

  it("agrees with the ratio it documents", () => {
    // Pins the constant itself: the tests above are written in distances derived from it, and
    // a change that moved it without moving them would leave them passing for a gesture
    // nobody makes.
    expect(PINCH_STEP_RATIO).toBeGreaterThan(1);
    expect(PINCH_STEP_RATIO).toBeLessThan(1.5);
  });
});
