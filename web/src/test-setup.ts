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
