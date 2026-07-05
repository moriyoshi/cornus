import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  handlePrefixKey,
  armed,
  paletteOpen,
  setPaletteOpen,
  disarm,
  registerCommands,
  allCommands,
  type Command,
} from "./command-center";
import { setPrefix, setPrefixEnabled } from "./settings";

function k(over: Partial<KeyboardEvent> & { key: string }): KeyboardEvent {
  return {
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    metaKey: false,
    ...over,
  } as unknown as KeyboardEvent;
}

beforeEach(() => {
  disarm();
  setPaletteOpen(false);
  setPrefixEnabled(true);
  setPrefix("Ctrl+Shift+X");
});

describe("handlePrefixKey (app-wide prefix state machine)", () => {
  it("arms and swallows the configured prefix", () => {
    expect(armed()).toBe(false);
    expect(handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }))).toBe("swallow");
    expect(armed()).toBe(true);
  });

  it("opens the palette on '>' after the prefix and disarms", () => {
    handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }));
    expect(handlePrefixKey(k({ key: ">", shiftKey: true }))).toBe("swallow");
    expect(paletteOpen()).toBe(true);
    expect(armed()).toBe(false);
  });

  it("hands the next combo to the browser after the prefix", () => {
    handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }));
    expect(handlePrefixKey(k({ key: "t", ctrlKey: true }))).toBe("browser");
    expect(armed()).toBe(false);
  });

  it("does nothing when the prefix is disabled", () => {
    setPrefixEnabled(false);
    expect(handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }))).toBeUndefined();
    expect(armed()).toBe(false);
  });
});

describe("tmux-style second-key binds", () => {
  const arm = () => handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }));

  it("runs a bound command directly and swallows the key", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "split-h", group: "Terminal", title: "Split left / right", bind: "%", run },
    ]);
    try {
      arm();
      expect(handlePrefixKey(k({ key: "%", shiftKey: true }))).toBe("swallow");
      expect(run).toHaveBeenCalledTimes(1);
      expect(armed()).toBe(false); // consumed the prefix window
    } finally {
      dispose();
    }
  });

  it("falls back to the browser for an unbound second key", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "close", group: "Terminal", title: "Close", bind: "x", run },
    ]);
    try {
      arm();
      expect(handlePrefixKey(k({ key: "%", shiftKey: true }))).toBe("browser");
      expect(run).not.toHaveBeenCalled();
    } finally {
      dispose();
    }
  });

  it("ignores a bind when Ctrl/Alt/Meta is held (a real browser shortcut)", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "new", group: "Terminal", title: "New", bind: "c", run },
    ]);
    try {
      arm();
      // prefix then Ctrl+C — meant for the browser, not the "c" bind.
      expect(handlePrefixKey(k({ key: "c", ctrlKey: true }))).toBe("browser");
      expect(run).not.toHaveBeenCalled();
    } finally {
      dispose();
    }
  });

  it("does not fire a bind without the prefix armed", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "split-h", group: "Terminal", title: "Split", bind: "%", run },
    ]);
    try {
      expect(handlePrefixKey(k({ key: "%", shiftKey: true }))).toBeUndefined();
      expect(run).not.toHaveBeenCalled();
    } finally {
      dispose();
    }
  });
});

describe("command registry", () => {
  const go: Command = { id: "goto:/", group: "Go to", title: "Overview", run: () => {} };
  const split: Command = { id: "term:split", group: "Terminal", title: "Split", run: () => {} };

  it("aggregates providers and disposes them; prepend leads", () => {
    const disposeGlobal = registerCommands(() => [go]);
    const disposeCtx = registerCommands(() => [split], true); // contextual leads
    expect(allCommands().map((c) => c.id)).toEqual(["term:split", "goto:/"]);
    disposeCtx();
    expect(allCommands().map((c) => c.id)).toEqual(["goto:/"]);
    disposeGlobal();
    expect(allCommands()).toHaveLength(0);
  });
});
