// tmux-style prefix key handling for terminal panes, as a pure state machine so it
// is unit-testable. After the prefix combo is pressed, the NEXT keydown is routed
// specially: ">" opens the command menu, Escape cancels, and anything else is
// handed to the browser (so a browser shortcut can be emitted without the terminal
// eating it). See Term.tsx for how a disposition is applied, and CommandPalette.tsx
// for the command menu.

export interface Keyish {
  key: string;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

export interface PrefixSpec {
  ctrl: boolean;
  alt: boolean;
  shift: boolean;
  meta: boolean;
  key: string; // normalized, e.g. "b", "space", "]"
}

// Disposition tells the terminal what to do with a keydown: send to the shell,
// let it reach the browser, or drop it entirely. undefined means "no opinion".
export type Disposition = "shell" | "browser" | "swallow";

// Presets offered in Settings. The Ctrl+Shift+<letter> chords lead because a
// terminal can't encode them as control bytes, so no terminal app (tmux, screen,
// readline) is bound to them; the single-modifier ones are kept for people who do
// not run a multiplexer. Shifted punctuation is avoided (its e.key is layout- and
// shift-dependent).
export const PREFIX_PRESETS = [
  "Ctrl+Shift+X",
  "Ctrl+Shift+Space",
  "Ctrl+B",
  "Ctrl+A",
] as const;

const MODIFIERS = new Set(["Control", "Shift", "Alt", "Meta"]);

export function isModifierKey(key: string): boolean {
  return MODIFIERS.has(key);
}

export function normalizeKey(key: string): string {
  if (key === " " || key === "Spacebar") return "space";
  return key.toLowerCase();
}

// parsePrefix turns "Ctrl+B" into a spec, or null if it names no non-modifier key.
export function parsePrefix(spec: string): PrefixSpec | null {
  const out: PrefixSpec = { ctrl: false, alt: false, shift: false, meta: false, key: "" };
  for (const raw of spec.split("+")) {
    const p = raw.trim().toLowerCase();
    if (!p) continue;
    if (p === "ctrl" || p === "control") out.ctrl = true;
    else if (p === "alt" || p === "option") out.alt = true;
    else if (p === "shift") out.shift = true;
    else if (p === "meta" || p === "cmd" || p === "command" || p === "win") out.meta = true;
    else out.key = normalizeKey(p);
  }
  return out.key ? out : null;
}

export function matchesPrefix(e: Keyish, spec: PrefixSpec): boolean {
  if (e.ctrlKey !== spec.ctrl) return false;
  if (e.altKey !== spec.alt) return false;
  if (e.metaKey !== spec.meta) return false;
  const key = normalizeKey(e.key);
  // A mismatched Shift means a different combo for single characters (Ctrl+Shift+B
  // is not Ctrl+B); for named keys (space, brackets) Shift is not significant.
  if (spec.shift !== e.shiftKey && (spec.shift || key.length === 1)) return false;
  return key === spec.key;
}

export interface PrefixDecision {
  armed: boolean;
  disposition?: Disposition;
  openCommands?: boolean;
}

// decidePrefixKey is the state transition for one keydown given the current armed
// state and the configured prefix (null = prefix disabled).
export function decidePrefixKey(armed: boolean, e: Keyish, spec: PrefixSpec | null): PrefixDecision {
  if (!spec) return { armed: false };
  // Lone modifier keydowns (Ctrl, Shift…) neither consume the armed state nor the
  // key — they arrive between the prefix and the real second keystroke.
  if (isModifierKey(e.key)) return { armed };
  if (armed) {
    if (e.key === "Escape") return { armed: false, disposition: "swallow" };
    if (e.key === ">") return { armed: false, disposition: "swallow", openCommands: true };
    // The "emit a browser shortcut thereafter" case: let the next combo through.
    return { armed: false, disposition: "browser" };
  }
  if (matchesPrefix(e, spec)) return { armed: true, disposition: "swallow" };
  return { armed: false };
}
