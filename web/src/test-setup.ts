import "@testing-library/jest-dom/vitest";

// Node ships an experimental global `localStorage` that is disabled unless the
// process is started with --localstorage-file, and it shadows jsdom's. Install a
// simple in-memory Storage so code under test that persists to localStorage (e.g.
// the terminal workspace layout) behaves like a browser.
class MemoryStorage implements Storage {
  private m = new Map<string, string>();
  get length(): number {
    return this.m.size;
  }
  clear(): void {
    this.m.clear();
  }
  getItem(key: string): string | null {
    return this.m.has(key) ? (this.m.get(key) as string) : null;
  }
  setItem(key: string, value: string): void {
    this.m.set(key, String(value));
  }
  removeItem(key: string): void {
    this.m.delete(key);
  }
  key(index: number): string | null {
    return Array.from(this.m.keys())[index] ?? null;
  }
}

Object.defineProperty(globalThis, "localStorage", {
  value: new MemoryStorage(),
  configurable: true,
  writable: true,
});

// jsdom implements no matchMedia, and xterm.js calls it as it opens onto a container
// (it watches the device-pixel-ratio query to redraw on a DPI change). Without a stub the
// call throws from inside the pane's mount, so any test that puts a LIVE terminal on
// screen dies there. The stub answers "no match" and registers no listeners: a headless
// DPR never changes.
if (typeof window !== "undefined" && typeof window.matchMedia !== "function") {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

// xterm refits its grid whenever the pane's box changes, through a ResizeObserver jsdom
// does not implement. Nothing is ever laid out here, so an observer that observes nothing
// is the honest stand-in.
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  } as unknown as typeof ResizeObserver;
}

// A terminal pane attaches to its session over a WebSocket. jsdom ships one, but under
// vitest it resolves `ws` to the browser shim and throws from the constructor — so the
// pane's mount dies and takes the test with it. This stand-in connects to nothing and
// stays that way: it never opens and never closes, which is what a pane attached to a
// live session looks like from the workspace's side. No test drives terminal I/O through
// it; what the pane DOES when a socket closes is decided by paneExitAction, covered
// directly (and purely) in views/terminal/reconnect.test.ts.
class SilentWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly url: string;
  readyState = SilentWebSocket.CONNECTING;
  binaryType = "blob";
  onopen: unknown = null;
  onmessage: unknown = null;
  onclose: unknown = null;
  onerror: unknown = null;
  constructor(url: string) {
    this.url = String(url);
  }
  send(): void {}
  close(): void {
    this.readyState = SilentWebSocket.CLOSED;
  }
  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean {
    return false;
  }
}
globalThis.WebSocket = SilentWebSocket as unknown as typeof WebSocket;

// jsdom has no canvas 2D context; xterm.js probes one at import time to parse
// colors, which logs a noisy "Not implemented" error. A minimal stub keeps
// terminal-importing view tests quiet (real browsers have a real context).
if (typeof HTMLCanvasElement !== "undefined") {
  // Cast through any: we only implement the handful of 2D methods xterm touches,
  // not the full overloaded getContext signature.
  (HTMLCanvasElement.prototype as unknown as { getContext: () => unknown }).getContext = () => ({
    fillRect() {},
    clearRect() {},
    getImageData: (_x: number, _y: number, w: number, h: number) => ({
      data: new Array(w * h * 4).fill(0),
    }),
    putImageData() {},
    createImageData: () => [],
    measureText: () => ({ width: 0 }),
    fillText() {},
    save() {},
    restore() {},
    beginPath() {},
    moveTo() {},
    lineTo() {},
    closePath() {},
    stroke() {},
    fill() {},
    translate() {},
    scale() {},
    rotate() {},
    arc() {},
    rect() {},
    clip() {},
    setTransform() {},
    transform() {},
    drawImage() {},
  });
}
