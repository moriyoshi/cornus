import { describe, it, expect } from "vitest";
import {
  parsePrefix,
  matchesPrefix,
  decidePrefixKey,
  type Keyish,
  type PrefixSpec,
} from "./prefix";

function key(over: Partial<Keyish> & { key: string }): Keyish {
  return { ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...over };
}

describe("parsePrefix", () => {
  it("parses modifiers and the key", () => {
    expect(parsePrefix("Ctrl+B")).toEqual({ ctrl: true, alt: false, shift: false, meta: false, key: "b" });
    expect(parsePrefix("ctrl+space")).toMatchObject({ ctrl: true, key: "space" });
    expect(parsePrefix("Alt+X")).toMatchObject({ alt: true, key: "x" });
    expect(parsePrefix("Ctrl+Shift+A")).toMatchObject({ ctrl: true, shift: true, key: "a" });
  });

  it("returns null when there is no non-modifier key", () => {
    expect(parsePrefix("Ctrl")).toBeNull();
    expect(parsePrefix("")).toBeNull();
  });
});

describe("matchesPrefix", () => {
  const ctrlB = parsePrefix("Ctrl+B") as PrefixSpec;
  const ctrlSpace = parsePrefix("Ctrl+Space") as PrefixSpec;
  const ctrlShiftX = parsePrefix("Ctrl+Shift+X") as PrefixSpec;

  it("matches the exact combo", () => {
    expect(matchesPrefix(key({ key: "b", ctrlKey: true }), ctrlB)).toBe(true);
    expect(matchesPrefix(key({ key: "B", ctrlKey: true }), ctrlB)).toBe(true); // caps
    expect(matchesPrefix(key({ key: " ", ctrlKey: true }), ctrlSpace)).toBe(true);
  });

  it("matches the tmux-safe default Ctrl+Shift+X (Shift makes e.key uppercase)", () => {
    expect(matchesPrefix(key({ key: "X", ctrlKey: true, shiftKey: true }), ctrlShiftX)).toBe(true);
    // Plain Ctrl+X (tmux/readline territory) must NOT match — Shift is required.
    expect(matchesPrefix(key({ key: "x", ctrlKey: true }), ctrlShiftX)).toBe(false);
  });

  it("rejects wrong modifiers or an unexpected shift", () => {
    expect(matchesPrefix(key({ key: "b" }), ctrlB)).toBe(false); // no ctrl
    expect(matchesPrefix(key({ key: "b", ctrlKey: true, shiftKey: true }), ctrlB)).toBe(false);
    expect(matchesPrefix(key({ key: "b", ctrlKey: true, altKey: true }), ctrlB)).toBe(false);
    expect(matchesPrefix(key({ key: "a", ctrlKey: true }), ctrlB)).toBe(false);
  });
});

describe("decidePrefixKey", () => {
  const spec = parsePrefix("Ctrl+B") as PrefixSpec;

  it("does nothing when the prefix is disabled", () => {
    expect(decidePrefixKey(false, key({ key: "b", ctrlKey: true }), null)).toEqual({ armed: false });
  });

  it("arms and swallows the prefix combo", () => {
    expect(decidePrefixKey(false, key({ key: "b", ctrlKey: true }), spec)).toEqual({
      armed: true,
      disposition: "swallow",
    });
  });

  it("has no opinion on other keys when not armed", () => {
    expect(decidePrefixKey(false, key({ key: "a" }), spec)).toEqual({ armed: false });
  });

  it("opens the command menu on '>' after the prefix", () => {
    expect(decidePrefixKey(true, key({ key: ">", shiftKey: true }), spec)).toEqual({
      armed: false,
      disposition: "swallow",
      openCommands: true,
    });
  });

  it("cancels on Escape after the prefix", () => {
    expect(decidePrefixKey(true, key({ key: "Escape" }), spec)).toEqual({
      armed: false,
      disposition: "swallow",
    });
  });

  it("emits the next combo to the browser after the prefix", () => {
    expect(decidePrefixKey(true, key({ key: "t", ctrlKey: true }), spec)).toEqual({
      armed: false,
      disposition: "browser",
    });
  });

  it("keeps the armed state across lone modifier keydowns", () => {
    expect(decidePrefixKey(true, key({ key: "Control", ctrlKey: true }), spec)).toEqual({
      armed: true,
    });
  });
});
