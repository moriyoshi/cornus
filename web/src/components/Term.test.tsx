import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@solidjs/testing-library";
import Term from "./Term";
import { setTerminalFont, setTerminalFontSize, setTerminalLineHeight } from "../settings";

// What the Terminal font setting is FOR: reaching xterm. Everything else about it is
// covered purely — the catalogue and the two resolvers in termFont.test.ts, the controls
// and the preview in views.test.tsx — and all of that would pass with the terminal itself
// left wired to the default, which is the one failure that would make the whole feature a
// no-op. So this file renders a live terminal and reads back what it is actually painted
// in.
//
// It reads xterm's own injected stylesheet, because that is the only place the answer
// exists. The Terminal instance is deliberately unreachable from outside Term's onMount
// (nothing may hold a reference to a terminal that is mid-teardown — see Term.tsx), so
// `term.options.fontFamily` cannot be asserted; the DOM renderer publishes the same value
// into a `<style>` scoped to the terminal's owner class, and that is a fair read of "what
// is on screen" rather than "what we passed in".
//
// The LEADING is not asserted here, and cannot be: xterm derives the row box from a
// measured character cell, and jsdom measures every glyph as 0×0, so a terminal at leading
// 1 and one at leading 2 both come out as `height: 0px`. Its resolution is covered in
// termFont.test.ts and its wiring to the control in views.test.tsx; the step between them
// is the same assignment on the same options object as the family, one line away.
function fontFamilyOf(container: HTMLElement): string | undefined {
  const owner = Array.from(container.querySelector(".xterm")?.classList ?? []).find((c) =>
    c.startsWith("xterm-dom-renderer-owner-"),
  );
  if (!owner) return undefined;
  // Scoped to THIS terminal's owner class: the renderer injects into document.head, which
  // outlives a test, so an unscoped read would happily pass on a stylesheet left behind by
  // the previous case.
  const css = Array.from(document.querySelectorAll("style"))
    .map((s) => s.textContent ?? "")
    .filter((t) => t.includes(`.${owner} .xterm-rows {`))
    .join("\n");
  return /font-family:\s*([^;]+);/.exec(css)?.[1];
}

// The size xterm published alongside the family, from the same injected rule. Unlike the
// leading, this one IS observable in jsdom: it is the number xterm was handed, not
// something it derived from a measured glyph.
function fontSizeOf(container: HTMLElement): string | undefined {
  const owner = Array.from(container.querySelector(".xterm")?.classList ?? []).find((c) =>
    c.startsWith("xterm-dom-renderer-owner-"),
  );
  if (!owner) return undefined;
  const css = Array.from(document.querySelectorAll("style"))
    .map((s) => s.textContent ?? "")
    .filter((t) => t.includes(`.${owner} .xterm-rows {`))
    .join("\n");
  return /font-size:\s*([^;]+);/.exec(css)?.[1];
}

describe("Term type", () => {
  afterEach(() => {
    cleanup();
    setTerminalFont("system");
    setTerminalFontSize(13);
    setTerminalLineHeight(1);
  });

  it("paints a terminal in the browser's monospace at 13px by default", async () => {
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontFamilyOf(container)).toBe("monospace");
    // The size the terminal had before any of this was configurable.
    expect(fontSizeOf(container)).toBe("13px");
  });

  it("paints a terminal at the configured size, before and after it opens", async () => {
    setTerminalFontSize(20);
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontSizeOf(container)).toBe("20px");

    // Live, on a terminal already open — the same requirement as the family, and the one an
    // apply-at-mount-only implementation would fail.
    setTerminalFontSize(11);
    await Promise.resolve();
    expect(fontSizeOf(container)).toBe("11px");
  });

  it("falls back to the default size for a stored value that is not a number", async () => {
    // parseSettings spreads stored JSON without validating it, so a hand-edited blob can put
    // a string here. Unresolved, it would reach xterm as `fontSize: "14"` and be multiplied
    // by the zoom scale into a NaN — which the fit addon divides by, giving a pane with no
    // rows at all.
    setTerminalFontSize("14" as unknown as number);
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontSizeOf(container)).toBe("13px");
  });

  it("paints a terminal in the font the setting names, with the fallback tail", async () => {
    // Set BEFORE the mount, which is the case that matters most and the one the equality
    // guard in applyType would hide: a terminal built at the default and corrected on the
    // next tick spends that tick having told the shell a column count measured from the
    // wrong font.
    setTerminalFont("jetbrains-mono");
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontFamilyOf(container)).toBe('"JetBrains Mono", monospace');
  });

  it("re-paints an OPEN terminal when the setting changes", async () => {
    // The live path, and the reason the type is read inside an effect rather than once at
    // mount. Someone changing the font has terminals already open in other panes; a setting
    // that only took effect on the next pane would read as broken.
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontFamilyOf(container)).toBe("monospace");

    setTerminalFont("cascadia-code");
    await Promise.resolve();
    expect(fontFamilyOf(container)).toBe('"Cascadia Code", monospace');
  });

  it("falls back to the default for a font id it does not know", async () => {
    // A blob synced from a build that offers a family this one does not. The failure being
    // prevented is a font-family string led by a name nothing can resolve — which would
    // still render, in whatever the document's default font is, and that is proportional.
    setTerminalFont("iosevka-not-a-bundled-font" as never);
    const { container } = render(() => <Term workload="w" />);
    await Promise.resolve();
    expect(fontFamilyOf(container)).toBe("monospace");
  });
});
