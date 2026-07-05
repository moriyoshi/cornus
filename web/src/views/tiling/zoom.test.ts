import { describe, it, expect, afterEach } from "vitest";
import {
  ZOOM_SCALES,
  ZOOM_HOME,
  scaleOf,
  zoomStep,
  zoomScale,
  zoomLevels,
  canZoom,
  nudgeZoom,
  setZoomStep,
  resetZoom,
  keepZooms,
  zoomStyle,
} from "./zoom";

// The store is a module singleton shared by every test in the process, so each test hands it
// back empty. keepZooms(∅) is the module's own way of saying "nothing is alive".
afterEach(() => keepZooms(new Set()));

describe("zoom steps", () => {
  it("puts an unzoomed pane at exactly 1", () => {
    // The one thing two constants have to agree about. If ZOOM_SCALES ever gains or loses a
    // step below 1 and ZOOM_HOME is not moved with it, every pane in the app opens zoomed.
    expect(ZOOM_SCALES[ZOOM_HOME]).toBe(1);
    expect(scaleOf(ZOOM_HOME)).toBe(1);
    expect(zoomStep("nobody")).toBe(ZOOM_HOME);
    expect(zoomScale("nobody")).toBe(1);
  });

  it("rises and falls monotonically, with no repeated step", () => {
    // A duplicated scale would be a step the user presses and nothing happens.
    for (let i = 1; i < ZOOM_SCALES.length; i++) {
      expect(ZOOM_SCALES[i]).toBeGreaterThan(ZOOM_SCALES[i - 1]);
    }
  });

  it("clamps at both ends of the table instead of running off it", () => {
    expect(scaleOf(-99)).toBe(ZOOM_SCALES[0]);
    expect(scaleOf(999)).toBe(ZOOM_SCALES[ZOOM_SCALES.length - 1]);

    setZoomStep("p", 999);
    expect(zoomStep("p")).toBe(ZOOM_SCALES.length - 1);
    nudgeZoom("p", 1);
    expect(zoomStep("p")).toBe(ZOOM_SCALES.length - 1); // still there, not one past it
    expect(canZoom("p", 1)).toBe(false);
    expect(canZoom("p", -1)).toBe(true);

    setZoomStep("p", -999);
    expect(zoomStep("p")).toBe(0);
    expect(canZoom("p", -1)).toBe(false);
  });

  it("keeps each pane's level to itself", () => {
    nudgeZoom("a", 2);
    nudgeZoom("b", -1);
    expect(zoomStep("a")).toBe(ZOOM_HOME + 2);
    expect(zoomStep("b")).toBe(ZOOM_HOME - 1);
    expect(zoomStep("c")).toBe(ZOOM_HOME);
  });

  it("forgets a pane returned to 1 rather than recording that it is not zoomed", () => {
    // This is what makes zoomLevels() answer "which panes are zoomed", which the prune below
    // and the ⋮'s Reset row both lean on.
    nudgeZoom("a", 3);
    expect(Object.keys(zoomLevels())).toEqual(["a"]);
    resetZoom("a");
    expect(zoomLevels()).toEqual({});
    expect(zoomStep("a")).toBe(ZOOM_HOME);
  });
});

describe("keepZooms", () => {
  it("drops the panes that are gone and leaves the ones that are not", () => {
    nudgeZoom("alive", 1);
    nudgeZoom("dead", 1);
    keepZooms(new Set(["alive"]));
    expect(Object.keys(zoomLevels())).toEqual(["alive"]);
  });

  it("returns the same object when nothing is stale, so it cannot loop the effect it runs in", () => {
    // It is called from the same createEffect that persists the layout. If a no-op prune
    // wrote a new object, that write would re-run every effect reading zoomLevels — and in a
    // tree that reads it, the effect that called it.
    nudgeZoom("alive", 1);
    const before = zoomLevels();
    keepZooms(new Set(["alive"]));
    expect(zoomLevels()).toBe(before);
  });
});

describe("zoomStyle", () => {
  it("says nothing at all about a pane nobody zoomed", () => {
    // Every rule spells `var(--pane-zoom, 1)`, so an untouched pane must render through the
    // declarations it always did — with no custom property set on it anywhere.
    expect(zoomStyle("quiet")).toEqual({});
  });

  it("publishes the scale, as a string the DOM will take", () => {
    setZoomStep("loud", ZOOM_HOME + 1);
    expect(zoomStyle("loud")).toEqual({ "--pane-zoom": String(ZOOM_SCALES[ZOOM_HOME + 1]) });
  });
});
