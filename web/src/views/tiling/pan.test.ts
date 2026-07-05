import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { PAN_PINCH_SLOP, PAN_SLOP_PX, twoFingerPan } from "./pan";

// The two-finger pan, tested as a RECOGNIZER: given a sequence of touches, does it read them as
// a pan, a pinch, or nothing, and does it move the scroll container by the right amount.
//
// The thing that makes this worth testing at all is the discrimination. Two fingers on a screen
// are also a pinch, and the gesture that must NOT pan is the one that looks most like a pan up
// until the moment it does not — so every test below that asserts panning has a sibling that
// asserts the same shape of gesture does not pan when the fingers separate.

// jsdom implements no PointerEvent, so the suite's own idiom applies (see views.test.tsx): a
// MouseEvent with the pointer fields defined on it. `pointerType` matters here — the recognizer
// refuses anything that is not a finger.
function touchEvent(type: string, id: number, x: number, y: number, pointerType = "touch") {
  const ev = new MouseEvent(type, { clientX: x, clientY: y, bubbles: true, button: 0 });
  Object.defineProperty(ev, "pointerId", { value: id });
  Object.defineProperty(ev, "pointerType", { value: pointerType });
  Object.defineProperty(ev, "isPrimary", { value: id === 1 });
  return ev;
}

describe("two-finger pan", () => {
  let el: HTMLDivElement;
  let dispose: () => void;

  beforeEach(() => {
    el = document.createElement("div");
    document.body.appendChild(el);
    // jsdom lays nothing out, so scrollLeft/scrollTop are inert: the setter is a no-op and the
    // getter always answers 0. Redefined as plain writable properties so the recognizer's
    // arithmetic is observable — what is under test is what it WRITES, and a real container's
    // clamping is the browser's business and is covered by the measurement pass instead.
    let left = 0;
    let top = 0;
    Object.defineProperty(el, "scrollLeft", {
      get: () => left,
      set: (v: number) => {
        left = v;
      },
      configurable: true,
    });
    Object.defineProperty(el, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = v;
      },
      configurable: true,
    });
    dispose = twoFingerPan(el);
  });

  afterEach(() => {
    dispose();
    el.remove();
  });

  // Both fingers travel the same way by the same amount: the midpoint moves with them and their
  // separation does not change.
  const drag = (dx: number, dy: number, opts: { spread?: number; steps?: number } = {}) => {
    const steps = opts.steps ?? 4;
    const gap = 100;
    el.dispatchEvent(touchEvent("pointerdown", 1, 200, 200));
    el.dispatchEvent(touchEvent("pointerdown", 2, 200 + gap, 200));
    for (let i = 1; i <= steps; i++) {
      const fx = (dx * i) / steps;
      const fy = (dy * i) / steps;
      // A spread factor pushes the second finger further out as the gesture runs, which is what
      // turns the same motion into a pinch.
      const extra = opts.spread ? (gap * (opts.spread - 1) * i) / steps : 0;
      window.dispatchEvent(touchEvent("pointermove", 1, 200 + fx, 200 + fy));
      window.dispatchEvent(touchEvent("pointermove", 2, 200 + gap + fx + extra, 200 + fy));
    }
  };

  const end = () => {
    window.dispatchEvent(touchEvent("pointerup", 1, 0, 0));
    window.dispatchEvent(touchEvent("pointerup", 2, 0, 0));
  };

  it("scrolls the container opposite the fingers, one to one", () => {
    drag(-120, -80);
    // Fingers left and up: the content follows them, so the container scrolls right and down.
    expect(el.scrollLeft).toBe(120);
    expect(el.scrollTop).toBe(80);
    end();
  });

  it("does not pan on one finger", () => {
    el.dispatchEvent(touchEvent("pointerdown", 1, 200, 200));
    for (let i = 1; i <= 4; i++) window.dispatchEvent(touchEvent("pointermove", 1, 200 - i * 30, 200));
    expect(el.scrollLeft).toBe(0);
    end();
  });

  // The discrimination, and the reason this module is not four lines. The midpoint travels
  // exactly as far as in the passing test above — 120px, which WOULD pan by 120 — and the only
  // difference is that the fingers double their separation while doing it. Delivered in one
  // step so the spread is judged before any of that travel is spent, which is how a real pinch
  // arrives: fast.
  it("does not pan when the fingers separate — that is a pinch", () => {
    drag(-120, 0, { spread: 2, steps: 1 });
    expect(el.scrollLeft).toBe(0);
    end();
  });

  // The honest account of the gesture in between. Fingers that translate AND slowly separate
  // pan while they are still within the slop and stop the moment they leave it — the travel up
  // to that point was real travel and keeping it is right; carrying on after it is not.
  it("pans up to the point a slow gesture turns into a pinch, and no further", () => {
    drag(-120, 0, { spread: 2, steps: 8 });
    const panned = el.scrollLeft;
    expect(panned).toBeGreaterThan(0);
    expect(panned).toBeLessThan(120);
    end();
  });

  it("still pans when the fingers drift a little, because hands are not calipers", () => {
    drag(-120, 0, { spread: PAN_PINCH_SLOP - 0.1 });
    expect(el.scrollLeft).toBeGreaterThan(0);
    end();
  });

  // Once a pinch, always a pinch: a gesture that separates and then travels must not start
  // panning halfway through, or the workspace lurches every time someone finishes a zoom.
  it("does not start panning after a gesture has been read as a pinch", () => {
    el.dispatchEvent(touchEvent("pointerdown", 1, 200, 200));
    el.dispatchEvent(touchEvent("pointerdown", 2, 300, 200));
    // Separate first...
    window.dispatchEvent(touchEvent("pointermove", 2, 400, 200));
    // ...then move both together, which on its own would be a textbook pan.
    for (let i = 1; i <= 4; i++) {
      window.dispatchEvent(touchEvent("pointermove", 1, 200 - i * 30, 200));
      window.dispatchEvent(touchEvent("pointermove", 2, 400 - i * 30, 200));
    }
    expect(el.scrollLeft).toBe(0);
    end();
  });

  // Below the slop the browser keeps the gesture, which is what leaves the native pinch-zoom
  // alone for anyone who has not switched pane zoom on.
  it("ignores travel under the slop", () => {
    drag(-(PAN_SLOP_PX - 2), 0, { steps: 1 });
    expect(el.scrollLeft).toBe(0);
    end();
  });

  it("keeps the travel that proved it was a pan", () => {
    // One move of exactly 20px: over the slop, and all 20 should land rather than the 14 that
    // re-basing at the moment of recognition would give.
    drag(-20, 0, { steps: 1 });
    expect(el.scrollLeft).toBe(20);
    end();
  });

  it("is a touch gesture: a mouse with two buttons is not two fingers", () => {
    el.dispatchEvent(touchEvent("pointerdown", 1, 200, 200, "mouse"));
    el.dispatchEvent(touchEvent("pointerdown", 2, 300, 200, "mouse"));
    for (let i = 1; i <= 4; i++) {
      window.dispatchEvent(touchEvent("pointermove", 1, 200 - i * 30, 200, "mouse"));
      window.dispatchEvent(touchEvent("pointermove", 2, 300 - i * 30, 200, "mouse"));
    }
    expect(el.scrollLeft).toBe(0);
    end();
  });

  // Lifting one of three fingers resumes the gesture from where the remaining two are, rather
  // than jumping the workspace by the distance between the old midpoint and the new one.
  it("re-bases when a third finger lifts instead of jumping", () => {
    el.dispatchEvent(touchEvent("pointerdown", 1, 200, 200));
    el.dispatchEvent(touchEvent("pointerdown", 2, 300, 200));
    el.dispatchEvent(touchEvent("pointerdown", 3, 600, 200));
    window.dispatchEvent(touchEvent("pointerup", 3, 600, 200));
    const before = el.scrollLeft;
    window.dispatchEvent(touchEvent("pointermove", 1, 180, 200));
    window.dispatchEvent(touchEvent("pointermove", 2, 280, 200));
    expect(el.scrollLeft).toBe(before + 20);
    end();
  });

  it("stops listening when disposed", () => {
    dispose();
    drag(-120, 0);
    expect(el.scrollLeft).toBe(0);
  });
});
