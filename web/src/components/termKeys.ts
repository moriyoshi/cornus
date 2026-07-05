// Deciding which keydowns a terminal should hand back to the browser instead of
// sending to the shell. xterm.js otherwise preventDefault()s every combo it
// handles, so browser chrome shortcuts (new tab, switch tab, zoom, all of macOS'
// Cmd shortcuts) get swallowed. Kept as a pure predicate so it is unit-testable.

export interface Keyish {
  key: string;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

export function isMacPlatform(
  nav: { platform?: string; userAgent?: string } = typeof navigator !== "undefined"
    ? navigator
    : {},
): boolean {
  return /Mac|iPhone|iPad|iPod/i.test(nav.platform || "") || /Mac OS X/i.test(nav.userAgent || "");
}

// isBrowserShortcut reports whether a keydown should be left for the browser (its
// own shortcut) rather than delivered to the shell.
//
// It deliberately does NOT release plain Ctrl+<letter> on Windows/Linux: those are
// terminal control characters and readline bindings the shell needs — Ctrl+C
// (SIGINT), Ctrl+D (EOF), Ctrl+Z, Ctrl+W (delete word), Ctrl+R (reverse search),
// Ctrl+A/E/U/K, Ctrl+L (clear). The browser's overlapping chrome shortcuts on
// those platforms use Ctrl+Shift, or non-letter keys (Tab, digits, +/-), which is
// exactly what this releases. Alt is left to the terminal too (it is Meta —
// Alt+B/F/D are readline word ops), so Alt-based browser shortcuts stay with the
// shell by design.
export function isBrowserShortcut(e: Keyish, isMac: boolean): boolean {
  // macOS: browser/OS shortcuts use Cmd, the terminal uses Ctrl — no overlap, so
  // let every Cmd combo through.
  if (isMac && e.metaKey && !e.ctrlKey) return true;

  // Ctrl-based chrome shortcuts that never map to a terminal control byte.
  if (e.ctrlKey && !e.altKey && !e.metaKey) {
    // Tab switching / scrollback nav: Ctrl+Tab, Ctrl+Shift+Tab, Ctrl+PageUp/Down.
    if (e.key === "Tab" || e.key === "PageUp" || e.key === "PageDown") return true;
    // Jump to tab N / reset zoom: Ctrl+0..9.
    if (e.key.length === 1 && e.key >= "0" && e.key <= "9") return true;
    // Zoom in / out: Ctrl+Plus / Ctrl+Minus / Ctrl+= .
    if (e.key === "+" || e.key === "-" || e.key === "=" || e.key === "_") return true;
    // Ctrl+Shift+<x> chrome shortcuts: reopen tab, incognito, hard reload,
    // devtools, downloads — none are terminal control chars.
    if (e.shiftKey) return true;
  }
  return false;
}
