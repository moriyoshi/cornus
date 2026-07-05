// Global, persisted UI settings shared across screens: the Settings screen writes
// them, other screens (e.g. the Terminal workspace) read them. A single module
// singleton keeps it consistent with the app's other module-level state (api.ts)
// rather than introducing a context or store library.

import { createSignal, createEffect, createRoot } from "solid-js";

// NewPaneSide controls which side a "split" places a new pane on. "auto" follows the
// text direction (left for RTL, right otherwise).
export type NewPaneSide = "auto" | "left" | "right";

export interface Settings {
  // When true, browser chrome shortcuts are handed back to the browser instead of
  // the terminal. Off by default so a terminal pane captures every key.
  passBrowserShortcuts: boolean;
  // The tmux-style prefix key (e.g. "Ctrl+B") and whether it is active. When on,
  // pressing the prefix then a browser shortcut emits that shortcut, and prefix
  // then ">" opens the terminal command menu.
  prefixEnabled: boolean;
  prefix: string;
  // Which side a "split" new pane lands on (the placement prompt's Split option).
  newPaneSide: NewPaneSide;
}

export const SETTINGS_KEY = "cornus.settings";

export function defaultSettings(): Settings {
  // Ctrl+Shift+X by default: a Ctrl+Shift+<letter> chord can't be a terminal
  // control byte, so no terminal app (tmux, screen, vim, readline) is bound to it,
  // and plain Ctrl+B still reaches tmux inside a pane.
  return { passBrowserShortcuts: false, prefixEnabled: true, prefix: "Ctrl+Shift+X", newPaneSide: "auto" };
}

// parseSettings merges stored JSON over the defaults, tolerating missing/unknown
// keys and corrupt storage.
export function parseSettings(raw: string | null | undefined): Settings {
  const d = defaultSettings();
  if (!raw) return d;
  try {
    return { ...d, ...(JSON.parse(raw) as Partial<Settings>) };
  } catch {
    return d;
  }
}

// The global reactive store. createRoot owns the persistence effect for the app's
// lifetime so it does not warn about running outside a root.
const store = createRoot(() => {
  const [settings, setSettings] = createSignal<Settings>(
    parseSettings(globalThis.localStorage?.getItem(SETTINGS_KEY)),
  );
  createEffect(() => {
    try {
      globalThis.localStorage?.setItem(SETTINGS_KEY, JSON.stringify(settings()));
    } catch {
      // storage unavailable; settings still work in-memory this session
    }
  });
  return { settings, setSettings };
});

export const settings = store.settings;

export function setPassBrowserShortcuts(v: boolean): void {
  store.setSettings((s) => ({ ...s, passBrowserShortcuts: v }));
}

export function setPrefixEnabled(v: boolean): void {
  store.setSettings((s) => ({ ...s, prefixEnabled: v }));
}

export function setPrefix(v: string): void {
  store.setSettings((s) => ({ ...s, prefix: v }));
}

export function setNewPaneSide(v: NewPaneSide): void {
  store.setSettings((s) => ({ ...s, newPaneSide: v }));
}
