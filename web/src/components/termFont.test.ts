import { describe, it, expect, afterEach } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import {
  DEFAULT_TERM_FONT_SIZE,
  DEFAULT_TERM_LINE_HEIGHT,
  DEFAULT_TERMINAL_FONT,
  TERM_FONT_FALLBACK,
  TERM_FONT_SIZES,
  TERM_LINE_HEIGHTS,
  TERMINAL_FONTS,
  loadTermFont,
  termFont,
  termFontPx,
  termFontSize,
  termLineHeight,
} from "./termFont";
import { ZOOM_SCALES } from "../views/tiling/zoom";

// The px xterm is actually handed. The Terminal instance is local to Term's onMount and
// stays there on purpose (nothing outside may hold a reference to a terminal that is
// mid-teardown), so what the effect writes into `term.options.fontSize` is not observable —
// this covers the number it writes, and the wiring around it is one line.
describe("termFontPx", () => {
  it("leaves an unzoomed terminal at exactly its configured size", () => {
    expect(termFontPx(1)).toBe(DEFAULT_TERM_FONT_SIZE);
    for (const s of TERM_FONT_SIZES) expect(termFontPx(1, s)).toBe(s);
  });

  // The property that makes the zoom table usable, now asserted at EVERY size the picker
  // offers rather than only at the 13px the terminal used to be fixed at. A step that
  // rounded to the same px as its neighbour would be a press with no effect, and the user
  // would read the whole zoom feature as unreliable rather than that one step as a no-op.
  //
  // This is the check that decides where TERM_FONT_SIZES may start: at 9px and below, the
  // small end of the table collides against the 6px floor. Drop a smaller size into the
  // list and this fails rather than shipping three dead keypresses.
  it("gives every step of the zoom table a distinct size, at every offered base", () => {
    for (const base of TERM_FONT_SIZES) {
      // NOT `ZOOM_SCALES.map(termFontPx)` — map passes the index as the second argument,
      // which termFontPx now reads as the base size.
      const sizes = ZOOM_SCALES.map((s) => termFontPx(s, base));
      expect(new Set(sizes).size, `collision at base ${base}px: ${sizes}`).toBe(
        ZOOM_SCALES.length,
      );
      expect(sizes, `not monotonic at base ${base}px`).toEqual([...sizes].sort((a, b) => a - b));
    }
  });

  it("never hands xterm a size that would break its cell measurement", () => {
    // A zero or negative divides by zero in the fit addon's cols/rows calculation. Neither
    // the scale table nor TERM_FONT_SIZES can produce one; the floor is here because this
    // is the boundary and the failure is a blank pane rather than an error anyone could
    // read.
    expect(termFontPx(0)).toBeGreaterThan(0);
    expect(termFontPx(-3)).toBeGreaterThan(0);
    expect(termFontPx(0.0001)).toBe(6);
    expect(termFontPx(1, 0)).toBe(6);
    expect(termFontPx(1, -20)).toBe(6);
  });
});

describe("termFontSize", () => {
  it("returns every offered size unchanged, and defaults to one of them", () => {
    // Same select-consistency invariant as termLineHeight: an option whose value did not
    // resolve to itself would leave the control showing one size and the terminal using
    // another.
    for (const s of TERM_FONT_SIZES) expect(termFontSize(s)).toBe(s);
    expect(TERM_FONT_SIZES).toContain(DEFAULT_TERM_FONT_SIZE);
    // 13px is what the terminal was before the setting existed, so an upgrade re-flows
    // nobody's panes.
    expect(DEFAULT_TERM_FONT_SIZE).toBe(13);
  });

  it("snaps a size that is off the list, and clamps one off the ends", () => {
    expect(TERM_FONT_SIZES).not.toContain(17);
    expect(termFontSize(17)).toBe(16);
    expect(termFontSize(21)).toBe(20);
    // Below the floor the zoom steps stop being distinct, which is the whole reason the
    // list starts where it does — so a stored 6 must come back as the smallest OFFERED
    // size, not as 6.
    expect(termFontSize(2)).toBe(TERM_FONT_SIZES[0]);
    expect(termFontSize(400)).toBe(TERM_FONT_SIZES[TERM_FONT_SIZES.length - 1]);
  });

  it("treats a non-number as no answer at all rather than clamping it", () => {
    for (const bad of [undefined, null, "14", NaN, Infinity, {}, []]) {
      expect(termFontSize(bad)).toBe(DEFAULT_TERM_FONT_SIZE);
    }
  });
});

const here = dirname(fileURLToPath(import.meta.url));

describe("TERMINAL_FONTS", () => {
  it("offers exactly the six families the picker promises, browser default first", () => {
    expect(TERMINAL_FONTS.map((f) => f.label)).toEqual([
      "Browser-builtin Monospace",
      "JetBrains Mono",
      "Source Code Pro",
      "Fira Code",
      "Cascadia Code",
      "Victor Mono",
    ]);
    expect(TERMINAL_FONTS[0].id).toBe(DEFAULT_TERMINAL_FONT);
  });

  it("ends every stack in the browser's monospace", () => {
    // The tail is not decoration. It is what renders every codepoint the chosen font was
    // not subset for — box drawing in three of the five bundled families, CJK and emoji in
    // all of them. Without it those glyphs come from the document's default font, which is
    // PROPORTIONAL, and a single proportional glyph shears the rest of the line out of the
    // grid. A test that checked only `includes("monospace")` would pass on a stack that
    // named it first, so pin the position.
    for (const f of TERMINAL_FONTS) {
      expect(f.stack.endsWith(TERM_FONT_FALLBACK)).toBe(true);
    }
    // The default is the fallback and nothing else: "browser-builtin monospace" means the
    // font the user chose in their own browser settings, so naming any family ahead of it
    // would be overriding exactly the choice this option exists to respect.
    expect(termFont("system").stack).toBe(TERM_FONT_FALLBACK);
  });

  // The drift check, and the reason this file reaches for the filesystem. A family can be
  // added to the catalogue with no @font-face to back it, and nothing about that fails:
  // the stack's tail is a valid font, so the picker offers "Victor Mono", the terminal
  // renders in the browser's monospace, and the two look identical to every other test
  // here. Only the generated stylesheet knows which families are really bundled.
  it("backs every bundled family with faces that exist on disk", () => {
    const css = readFileSync(resolve(here, "termFontFaces.css"), "utf8");
    const declared = new Set(
      Array.from(css.matchAll(/font-family:\s*"([^"]+)"/g)).map((m) => m[1]),
    );
    const bundled = TERMINAL_FONTS.filter((f) => f.id !== "system");
    expect(bundled).not.toHaveLength(0);
    for (const f of bundled) {
      // The name in the stack must be the name the @font-face declares — a stack quoting
      // "Jetbrains Mono" against a face called "JetBrains Mono" matches nothing.
      expect(declared, `no @font-face for ${f.label}`).toContain(f.label);
      expect(f.stack).toBe(`"${f.label}", ${TERM_FONT_FALLBACK}`);
    }
    // And no face for a family the picker cannot reach — that would be woff2 files
    // embedded into every cornus binary that nothing can ever ask for.
    expect([...declared].sort()).toEqual(bundled.map((f) => f.label).sort());

    // Every src actually resolves. A fontsource bump that re-cuts a subset leaves this
    // file naming files that are gone, and Vite fails the build on it — but the build is
    // not what a person runs after `npm install`, so catch it here where the message says
    // which font.
    const urls = Array.from(css.matchAll(/url\(([^)]+)\)/g)).map((m) => m[1]);
    expect(urls.length).toBeGreaterThanOrEqual(bundled.length * 2);
    for (const u of urls) {
      const path = resolve(here, "../../node_modules", u);
      expect(existsSync(path), `missing font file ${u}`).toBe(true);
    }
  });
});

describe("termFont", () => {
  it("resolves every offered id to its own entry", () => {
    for (const f of TERMINAL_FONTS) expect(termFont(f.id)).toBe(f);
  });

  it("lands anything it does not recognise on the default", () => {
    // parseSettings spreads stored JSON over the defaults without validating it, so all of
    // these can reach here from localStorage: a blob written before this setting existed,
    // a hand-edited one, one synced from a build that offers a family this one does not.
    // The failure this prevents is not cosmetic — an unresolved id would put a garbage name
    // at the head of the font-family string AND leave the Settings select matching no
    // option, so the screen would show one font while the terminal painted another.
    for (const bad of [undefined, null, "", "comic-sans", 42, {}, "System"]) {
      expect(termFont(bad).id).toBe(DEFAULT_TERMINAL_FONT);
    }
  });
});

describe("termLineHeight", () => {
  it("returns every offered leading unchanged", () => {
    // The invariant the Settings select depends on: each option's value must resolve to
    // itself, or the control would show one leading while the terminal used another.
    for (const h of TERM_LINE_HEIGHTS) expect(termLineHeight(h)).toBe(h);
    expect(TERM_LINE_HEIGHTS).toContain(DEFAULT_TERM_LINE_HEIGHT);
  });

  it("snaps a value that is in range but off the list", () => {
    // Not merely clamped: 1.15 is a perfectly legal leading, but no option equals it, and a
    // select whose value matches nothing displays its FIRST option — so the screen would
    // claim 1.0 while the terminal ran at 1.15.
    expect(TERM_LINE_HEIGHTS).not.toContain(1.17);
    expect(termLineHeight(1.17)).toBe(1.2);
    expect(termLineHeight(1.44)).toBe(1.4);
    expect(termLineHeight(1.9)).toBe(2);
    // A value exactly between two steps goes to the LOWER one, because the reduce keeps
    // the incumbent unless a later step is strictly closer. Pinned rather than left to
    // chance: it is arbitrary but it is a user-visible answer, and the tighter row is the
    // better tie-break for a terminal, whose scarce resource is rows on screen.
    expect(termLineHeight(1.25)).toBe(1.2);
  });

  it("never hands xterm a leading that would break its row count", () => {
    // The one that is not cosmetic: xterm divides by the cell height to work out how many
    // rows fit, so a 0 is a division by zero and a pane that renders nothing. Below 1 the
    // rows overlap, which xterm documents as unsupported.
    for (const bad of [0, -1, 0.5, -1000]) {
      expect(termLineHeight(bad)).toBeGreaterThanOrEqual(1);
    }
    expect(termLineHeight(99)).toBe(2);
  });

  it("treats a non-number as no answer at all rather than clamping it", () => {
    for (const bad of [undefined, null, "1.4", NaN, Infinity, {}, []]) {
      expect(termLineHeight(bad)).toBe(DEFAULT_TERM_LINE_HEIGHT);
    }
  });
});

describe("loadTermFont", () => {
  const doc = globalThis.document as unknown as { fonts?: unknown };
  afterEach(() => {
    delete doc.fonts;
  });

  it("resolves where the browser has no FontFaceSet", async () => {
    // jsdom is one such environment, which is why this is the ambient state of every other
    // test here. The caller re-fits the grid in `.then`, so a promise that never settled
    // would leave a terminal permanently measured against the fallback.
    expect(doc.fonts).toBeUndefined();
    await expect(loadTermFont(13, TERM_FONT_FALLBACK)).resolves.toBeUndefined();
  });

  it("asks for regular, bold and italic at the size in use", async () => {
    // All three, because a terminal paints all three from SGR attributes and a face that
    // arrives after the measurement is the same wrong-cell bug at a lower rate.
    const asked: string[] = [];
    doc.fonts = {
      load: (font: string) => {
        asked.push(font);
        return Promise.resolve([]);
      },
    };
    await loadTermFont(17, '"Victor Mono", monospace');
    expect(asked).toEqual([
      '17px "Victor Mono", monospace',
      'bold 17px "Victor Mono", monospace',
      'italic 17px "Victor Mono", monospace',
    ]);
  });

  it("resolves rather than rejecting when a face cannot be loaded", async () => {
    // A failed fetch, or a shorthand the parser rejects, leaves the terminal on the
    // fallback face — which is where it already was, so there is nothing for the caller to
    // handle. An unhandled rejection here would be one per pane per settings change.
    doc.fonts = { load: () => Promise.reject(new Error("network")) };
    await expect(loadTermFont(13, "monospace")).resolves.toBeUndefined();

    doc.fonts = {
      load: () => {
        throw new SyntaxError("bad font shorthand");
      },
    };
    await expect(loadTermFont(13, "monospace")).resolves.toBeUndefined();
  });
});
