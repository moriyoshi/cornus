import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  bindsOf,
  directsOf,
  dispatchAppKey,
  handleDirectKey,
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

  // A DISABLED command still owns its key. Swallowing without running is the quiet no-op
  // its greyed palette row promises; the alternative — skipping it in the lookup — lets the
  // key fall through as a browser shortcut, so `prefix x` would suddenly close a browser tab
  // because a pane command happened to be unavailable. The disposition is therefore as much
  // the assertion as the missing call.
  it("swallows a disabled command's bind without running it", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "move", group: "Terminal", title: "Move pane…", bind: "m", disabled: "nowhere to move it", run },
    ]);
    try {
      arm();
      expect(handlePrefixKey(k({ key: "m" }))).toBe("swallow");
      expect(run).not.toHaveBeenCalled();
      expect(armed()).toBe(false); // it consumed the prefix window like any other bind
    } finally {
      dispose();
    }
  });

  // A bind may name a CHORD, which is the only way to express tmux's `prefix C-o`. The
  // lookup therefore had to stop rejecting Ctrl before it looked — so the rows below pin
  // both that the chord runs and that the modifier is compared rather than ignored.
  describe("chord binds", () => {
    const withRotate = (fn: (run: ReturnType<typeof vi.fn>) => void) => {
      const run = vi.fn();
      const dispose = registerCommands(() => [
        { id: "rotate", group: "Terminal", title: "Rotate panes", bind: "Ctrl+O", run },
      ]);
      try {
        fn(run);
      } finally {
        dispose();
      }
    };

    it("runs on the exact chord and swallows it", () =>
      withRotate((run) => {
        arm();
        expect(handlePrefixKey(k({ key: "o", ctrlKey: true }))).toBe("swallow");
        expect(run).toHaveBeenCalledTimes(1);
      }));

    it("ignores the same letter without the modifier", () =>
      withRotate((run) => {
        arm();
        // Falls through as an unbound second key, which is what lets a plain `o` stay free
        // for some other command later.
        expect(handlePrefixKey(k({ key: "o" }))).toBe("browser");
        expect(run).not.toHaveBeenCalled();
      }));

    it("ignores a differently-modified chord", () =>
      withRotate((run) => {
        arm();
        expect(handlePrefixKey(k({ key: "O", ctrlKey: true, shiftKey: true }))).toBe("browser");
        expect(run).not.toHaveBeenCalled();
      }));

    // The regression the change most threatened: plain binds are matched on e.key with
    // Shift IGNORED (that is what lets `c` and `C` differ) and Ctrl/Alt/Meta REQUIRED
    // absent. Both halves are asserted here because the chord support is a rewrite of the
    // same matcher.
    it("leaves plain binds alone", () => {
      const shifted = vi.fn();
      const plain = vi.fn();
      const dispose = registerCommands(() => [
        { id: "split-h", group: "Terminal", title: "Split", bind: "%", run: shifted },
        { id: "new", group: "Terminal", title: "New", bind: "c", run: plain },
      ]);
      try {
        arm();
        expect(handlePrefixKey(k({ key: "%", shiftKey: true }))).toBe("swallow");
        expect(shifted).toHaveBeenCalledTimes(1);
        arm();
        expect(handlePrefixKey(k({ key: "c", ctrlKey: true }))).toBe("browser");
        expect(plain).not.toHaveBeenCalled();
      } finally {
        dispose();
      }
    });
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

// A command may name SEVERAL spellings of one key. The pair that forced it is Files'
// cross-pane copy, which answers both to the app's own Ctrl+Shift+C and to F5 — and F5 is
// the other kind of key entirely, so the two facilities are tested apart.
describe("several binds for one command", () => {
  const arm = () => handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }));

  it("answers to every spelling in the array, and to nothing else", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "copy-to", group: "Files", title: "Copy to…", bind: ["Ctrl+Shift+C", "F5"], run },
    ]);
    try {
      arm();
      expect(handlePrefixKey(k({ key: "C", ctrlKey: true, shiftKey: true }))).toBe("swallow");
      expect(run).toHaveBeenCalledTimes(1);
      // The SECOND spelling is the assertion an array-shaped bind exists at all: a lookup
      // that read only the first element would pass every line above.
      arm();
      expect(handlePrefixKey(k({ key: "F5" }))).toBe("swallow");
      expect(run).toHaveBeenCalledTimes(2);
      // And the array did not become a wildcard.
      arm();
      expect(handlePrefixKey(k({ key: "F6" }))).toBe("browser");
      expect(run).toHaveBeenCalledTimes(2);
    } finally {
      dispose();
    }
  });

  it("normalizes both shapes through bindsOf, so one and many read alike", () => {
    expect(bindsOf({ id: "a", group: "g", title: "t", run: () => {} })).toEqual([]);
    expect(bindsOf({ id: "a", group: "g", title: "t", bind: "x", run: () => {} })).toEqual(["x"]);
    expect(bindsOf({ id: "a", group: "g", title: "t", bind: ["x", "F5"], run: () => {} })).toEqual(["x", "F5"]);
    expect(directsOf({ id: "a", group: "g", title: "t", bind: "x", run: () => {} })).toEqual([]);
    expect(directsOf({ id: "a", group: "g", title: "t", direct: "F5", run: () => {} })).toEqual(["F5"]);
  });
});

// A DIRECT key runs its command with no prefix at all — Files' F5 / F6, the orthodox file
// manager's copy and move. It is a separate lookup from the prefix machine because it takes
// the key from the BROWSER rather than from the prefix window, and the caller (App.tsx) is
// what decides where it may fire. What is pinned here is the lookup's own contract.
describe("handleDirectKey (unprefixed command keys)", () => {
  const withCopy = (opts: Partial<Command>, fn: (run: ReturnType<typeof vi.fn>) => void) => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "files:copy", group: "Files", title: "Copy to…", direct: "F5", run, ...opts },
    ]);
    try {
      fn(run);
    } finally {
      dispose();
    }
  };

  it("runs the command and reports the key as claimed", () =>
    withCopy({}, (run) => {
      expect(handleDirectKey(k({ key: "F5" }))).toBe(true);
      expect(run).toHaveBeenCalledTimes(1);
    }));

  it("leaves every unclaimed key to the browser", () =>
    withCopy({}, (run) => {
      // The whole page's keyboard passes through this lookup, so "claims nothing it was not
      // given" is the property that keeps Reload working everywhere else.
      expect(handleDirectKey(k({ key: "F6" }))).toBe(false);
      expect(handleDirectKey(k({ key: "F5", ctrlKey: true }))).toBe(false);
      expect(run).not.toHaveBeenCalled();
    }));

  // A disabled direct command still owns its key, exactly as a disabled prefixed bind does.
  // The reasoning is STRONGER here, not weaker: the key underneath F5 is Reload, so falling
  // through would answer "this cannot run just now" by throwing away every unsaved editor
  // draft in the workspace. Both halves are asserted — claimed, and not run.
  it("swallows a disabled command's direct key without running it", () =>
    withCopy({ disabled: "nothing selected" }, (run) => {
      expect(handleDirectKey(k({ key: "F5" }))).toBe(true);
      expect(run).not.toHaveBeenCalled();
    }));

  // A prefixed bind and a direct key are different fields, and neither may answer for the
  // other. Otherwise F5 would work bare AND after the prefix (taking back the browser
  // shortcut the prefix window exists to emit), or Ctrl+Shift+C would fire while typing.
  it("keeps the two kinds of key apart", () => {
    const run = vi.fn();
    const dispose = registerCommands(() => [
      { id: "copy-to", group: "Files", title: "Copy to…", bind: "Ctrl+Shift+C", direct: "F5", run },
    ]);
    try {
      expect(handleDirectKey(k({ key: "C", ctrlKey: true, shiftKey: true }))).toBe(false);
      handlePrefixKey(k({ key: "X", ctrlKey: true, shiftKey: true }));
      expect(handlePrefixKey(k({ key: "F5" }))).toBe("browser");
      expect(run).not.toHaveBeenCalled();
    } finally {
      dispose();
    }
  });
});

// The sequencing App.tsx's one document listener runs: prefix machine first, direct keys
// second, and the gates between them. Tested here rather than through a mounted App
// because each gate is a rule about the ORDER of two lookups, which no rendering shows.
describe("dispatchAppKey (the app's one keydown path)", () => {
  const withBoth = (fn: (bind: ReturnType<typeof vi.fn>, direct: ReturnType<typeof vi.fn>) => void) => {
    const bind = vi.fn();
    const direct = vi.fn();
    const dispose = registerCommands(() => [
      { id: "split", group: "Terminal", title: "Split", bind: "%", run: bind },
      { id: "copy-to", group: "Files", title: "Copy to…", direct: "F5", run: direct },
    ]);
    try {
      fn(bind, direct);
    } finally {
      dispose();
    }
  };

  it("runs a direct key with no prefix, and a prefixed bind with one", () =>
    withBoth((bind, direct) => {
      expect(dispatchAppKey(k({ key: "F5" }))).toBe(true);
      expect(direct).toHaveBeenCalledTimes(1);
      expect(armed()).toBe(false); // it never touched the prefix window

      expect(dispatchAppKey(k({ key: "X", ctrlKey: true, shiftKey: true }))).toBe(true);
      expect(dispatchAppKey(k({ key: "%", shiftKey: true }))).toBe(true);
      expect(bind).toHaveBeenCalledTimes(1);
    }));

  // The gate with the most to lose. After a prefix, an unclaimed key is handed to the
  // BROWSER on purpose — that is what makes `prefix Ctrl+T` open a tab from inside a
  // terminal. A direct lookup running on that keystroke would take the shortcut back, and
  // silently: the user would see a copy where they asked for a reload.
  it("does not let a direct key answer the keystroke after a prefix", () =>
    withBoth((_bind, direct) => {
      expect(dispatchAppKey(k({ key: "X", ctrlKey: true, shiftKey: true }))).toBe(true);
      expect(dispatchAppKey(k({ key: "F5" }))).toBe(false);
      expect(direct).not.toHaveBeenCalled();
      // Disarmed by that keystroke, so the NEXT bare F5 is a direct key again — the gate is
      // about one keystroke, not a mode the prefix leaves behind.
      expect(armed()).toBe(false);
      expect(dispatchAppKey(k({ key: "F5" }))).toBe(true);
      expect(direct).toHaveBeenCalledTimes(1);
    }));

  it("does not let a direct key answer the prefix keystroke itself", () => {
    const run = vi.fn();
    // A direct bind ON the prefix combo: contrived, but it is the one case where step 1
    // consumes the key and step 2 must not also see it.
    const dispose = registerCommands(() => [
      { id: "x", group: "Files", title: "X", direct: "Ctrl+Shift+X", run },
    ]);
    try {
      expect(dispatchAppKey(k({ key: "X", ctrlKey: true, shiftKey: true }))).toBe(true);
      expect(run).not.toHaveBeenCalled();
      expect(armed()).toBe(true);
    } finally {
      dispose();
    }
  });

  it("stands aside for text entry", () =>
    withBoth((_bind, direct) => {
      const input = document.createElement("input");
      document.body.appendChild(input);
      try {
        expect(dispatchAppKey(k({ key: "F5", target: input }))).toBe(false);
        expect(direct).not.toHaveBeenCalled();
        // And the same key one element over is still live, so the guard is the FIELD and
        // not a blanket off-switch.
        expect(dispatchAppKey(k({ key: "F5", target: document.body }))).toBe(true);
        expect(direct).toHaveBeenCalledTimes(1);
      } finally {
        input.remove();
      }
    }));

  it("leaves an unclaimed key alone so the browser still gets it", () =>
    withBoth((bind, direct) => {
      expect(dispatchAppKey(k({ key: "F7" }))).toBe(false);
      expect(dispatchAppKey(k({ key: "r", ctrlKey: true }))).toBe(false);
      expect(bind).not.toHaveBeenCalled();
      expect(direct).not.toHaveBeenCalled();
    }));
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
