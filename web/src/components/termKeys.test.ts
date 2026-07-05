import { describe, it, expect } from "vitest";
import { isBrowserShortcut, isMacPlatform, type Keyish } from "./termKeys";

// key builds a Keyish with sensible defaults.
function key(over: Partial<Keyish> & { key: string }): Keyish {
  return { ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...over };
}

describe("isBrowserShortcut", () => {
  describe("Windows/Linux (isMac=false)", () => {
    const shortcut = (k: Keyish) => isBrowserShortcut(k, false);

    it("keeps terminal control chars / readline bindings for the shell", () => {
      for (const k of ["c", "d", "z", "w", "r", "a", "e", "u", "k", "l", "t", "n"]) {
        expect(shortcut(key({ key: k, ctrlKey: true }))).toBe(false);
      }
    });

    it("keeps plain and Alt (Meta) keys for the shell", () => {
      expect(shortcut(key({ key: "a" }))).toBe(false);
      expect(shortcut(key({ key: "d", altKey: true }))).toBe(false); // readline delete-word
    });

    it("releases Ctrl+Shift+<letter> chrome shortcuts", () => {
      expect(shortcut(key({ key: "t", ctrlKey: true, shiftKey: true }))).toBe(true); // reopen tab
      expect(shortcut(key({ key: "n", ctrlKey: true, shiftKey: true }))).toBe(true); // incognito
      expect(shortcut(key({ key: "i", ctrlKey: true, shiftKey: true }))).toBe(true); // devtools
    });

    it("releases tab switching and scrollback nav", () => {
      expect(shortcut(key({ key: "Tab", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "Tab", ctrlKey: true, shiftKey: true }))).toBe(true);
      expect(shortcut(key({ key: "PageUp", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "PageDown", ctrlKey: true }))).toBe(true);
    });

    it("releases Ctrl+digit (jump to tab / reset zoom) and zoom keys", () => {
      expect(shortcut(key({ key: "1", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "0", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "+", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "-", ctrlKey: true }))).toBe(true);
      expect(shortcut(key({ key: "=", ctrlKey: true }))).toBe(true);
    });

    it("does not release Cmd combos (no Cmd on these platforms)", () => {
      expect(shortcut(key({ key: "t", metaKey: true }))).toBe(false);
    });
  });

  describe("macOS (isMac=true)", () => {
    const shortcut = (k: Keyish) => isBrowserShortcut(k, true);

    it("releases every Cmd combo to the browser/OS", () => {
      for (const k of ["t", "w", "n", "l", "r", "c", "v", "1", "+"]) {
        expect(shortcut(key({ key: k, metaKey: true }))).toBe(true);
      }
    });

    it("keeps Ctrl control chars for the shell (terminal uses Ctrl on macOS too)", () => {
      expect(shortcut(key({ key: "c", ctrlKey: true }))).toBe(false);
      expect(shortcut(key({ key: "r", ctrlKey: true }))).toBe(false);
    });

    it("still releases Ctrl+Tab tab-switching", () => {
      expect(shortcut(key({ key: "Tab", ctrlKey: true }))).toBe(true);
    });
  });
});

describe("isMacPlatform", () => {
  it("detects macOS from platform or userAgent", () => {
    expect(isMacPlatform({ platform: "MacIntel" })).toBe(true);
    expect(isMacPlatform({ platform: "", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15)" })).toBe(true);
    expect(isMacPlatform({ platform: "iPhone" })).toBe(true);
  });

  it("is false for Windows/Linux", () => {
    expect(isMacPlatform({ platform: "Win32" })).toBe(false);
    expect(isMacPlatform({ platform: "Linux x86_64", userAgent: "Mozilla/5.0 (X11; Linux x86_64)" })).toBe(false);
  });
});
