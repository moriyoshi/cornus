// The command center is the app-wide home of the tmux-style prefix key and the
// command palette it opens. It is a module singleton (like settings.ts / api.ts)
// so any screen can contribute commands and any key handler — the app-wide
// document listener in App.tsx, or an individual terminal's xterm handler — can
// advance the same prefix state machine.
//
// Two things live here:
//   1. The reactive prefix state (armed / paletteOpen) plus handlePrefixKey, the
//      shared per-keydown step wrapping the pure reducer in views/terminal/prefix.
//   2. A command registry: a base set of global commands plus contextual groups
//      that screens push/pop while mounted (e.g. the Terminal workspace adds
//      split / close-pane). The palette renders allCommands().

import { createSignal, createRoot, type Accessor } from "solid-js";
import { settings } from "./settings";
import { decidePrefixKey, parsePrefix, type Disposition } from "./views/terminal/prefix";

// A single invocable action offered in the command palette. `group` names the
// section it renders under; `hint` is optional right-aligned text (e.g. a glyph or
// mnemonic); `keywords` is extra text the filter matches but does not display.
// `bind` is a tmux-style second key: after the prefix, pressing that key (matched
// against KeyboardEvent.key, so "%" / '"' / "c" / "x") runs the command directly,
// without opening the palette. Shown as the palette accelerator when present.
export interface Command {
  id: string;
  title: string;
  group: string;
  hint?: string;
  keywords?: string;
  bind?: string;
  run: () => void;
}

// A provider yields its commands lazily so their labels/availability can be
// reactive (e.g. a settings toggle whose title flips with the current value).
export type CommandProvider = Accessor<Command[]>;

const ARM_TIMEOUT_MS = 2500;

const center = createRoot(() => {
  const [armed, setArmed] = createSignal(false);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  const [providers, setProviders] = createSignal<CommandProvider[]>([]);

  let armTimer: ReturnType<typeof setTimeout> | undefined;
  const disarm = () => {
    if (armTimer) clearTimeout(armTimer);
    armTimer = undefined;
    setArmed(false);
  };
  const arm = () => {
    if (armTimer) clearTimeout(armTimer);
    setArmed(true);
    // Auto-clear so a forgotten prefix never sticks and eats the next keystroke.
    armTimer = setTimeout(() => setArmed(false), ARM_TIMEOUT_MS);
  };

  // handlePrefixKey advances the prefix state machine for one keydown and returns
  // the terminal disposition ("swallow" / "browser" / "shell"; undefined = no
  // opinion). Callers outside a terminal only act on "swallow" (drop the key).
  const handlePrefixKey = (e: KeyboardEvent): Disposition | undefined => {
    const spec = settings().prefixEnabled ? parsePrefix(settings().prefix) : null;
    const wasArmed = armed();
    const d = decidePrefixKey(wasArmed, e, spec);
    // tmux-style second key: the reducer routes the post-prefix key to "browser"
    // (emit a browser shortcut). Intercept that slot first — if the key is bound to
    // a command (and carries no Ctrl/Alt/Meta, which mark a real browser shortcut),
    // run it and swallow the key instead of emitting it. Shift is allowed since the
    // bound char itself may need it (e.g. "%" is Shift+5).
    if (wasArmed && d.disposition === "browser" && !e.ctrlKey && !e.altKey && !e.metaKey) {
      const cmd = allCommands().find((c) => c.bind !== undefined && c.bind === e.key);
      if (cmd) {
        disarm();
        cmd.run();
        return "swallow";
      }
    }
    if (d.armed && !wasArmed) arm();
    else if (!d.armed && wasArmed) disarm();
    if (d.openCommands) setPaletteOpen(true);
    return d.disposition;
  };

  // registerCommands adds a provider and returns a disposer for onCleanup. By
  // default a provider appends after existing ones; `prepend` puts it first so a
  // screen's contextual commands lead the global ones in the palette.
  const registerCommands = (provider: CommandProvider, prepend = false): (() => void) => {
    setProviders((ps) => (prepend ? [provider, ...ps] : [...ps, provider]));
    return () => setProviders((ps) => ps.filter((p) => p !== provider));
  };

  const allCommands = (): Command[] => providers().flatMap((p) => p());

  return {
    armed,
    paletteOpen,
    setPaletteOpen,
    handlePrefixKey,
    registerCommands,
    allCommands,
    disarm,
  };
});

export const armed = center.armed;
export const paletteOpen = center.paletteOpen;
export const setPaletteOpen = center.setPaletteOpen;
export const handlePrefixKey = center.handlePrefixKey;
export const registerCommands = center.registerCommands;
export const allCommands = center.allCommands;
export const disarm = center.disarm;
