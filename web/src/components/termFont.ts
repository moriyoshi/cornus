// Everything about the type a terminal is set in: the family, the size, and the leading
// between the rows. Term.tsx composes the three onto an xterm instance and owns nothing
// about them itself.
//
// A module rather than more of Term.tsx because two other places need this and neither
// should be reaching into a component to get it: the Settings screen builds its pickers
// from the three catalogues here, and every persisted value has to come back out through
// the matching resolver — none of them may be trusted raw (see termFont for why).

// The @font-face declarations for every bundled family. Imported HERE, beside the
// catalogue that names them, so the two cannot drift: a family added to TERMINAL_FONTS
// without a face to back it would be a picker entry that silently renders as the
// fallback. See scripts/gen-term-font-faces.mjs for what the file contains and why it
// is generated rather than imported from @fontsource directly.
//
// Importing the whole sheet costs nothing at load: an @font-face is a declaration, not a
// fetch, and a browser downloads a face only once something on the page is actually set
// in it. So a user who never leaves the default pulls none of the 42 woff2 files, and one
// who picks Victor Mono pulls only the four cut for Victor Mono.
import "./termFontFaces.css";

export type TerminalFontId =
  | "system"
  | "jetbrains-mono"
  | "source-code-pro"
  | "fira-code"
  | "cascadia-code"
  | "victor-mono";

export interface TerminalFont {
  id: TerminalFontId;
  // What the Settings picker calls it.
  label: string;
  // The full font-family list handed to xterm, fallback tail included. Precomputed
  // rather than assembled at the call site: xterm compares this string to decide whether
  // a change is worth re-measuring the grid for, so it has to be stable byte for byte.
  stack: string;
}

// What every stack ends in, and what the first entry consists of entirely.
//
// The bare generic, deliberately — not the `ui-monospace, "SF Mono", Menlo, Consolas`
// chain that --font-mono uses for the rest of the UI. In a terminal this tail is doing a
// different job than it does in a badge: it is what renders every codepoint the chosen
// font was not subset for — box drawing in three of the five families, and CJK, powerline
// glyphs and emoji in all of them. `monospace` resolves to the font the user picked in
// their own browser settings for exactly that purpose, and naming specific families ahead
// of it would override that choice on their behalf.
export const TERM_FONT_FALLBACK = "monospace";

// The offered families, in the order the picker lists them: the browser's own first,
// because it is the default and the one answer that needs nothing downloaded, then the
// four bundled ones.
export const TERMINAL_FONTS: readonly TerminalFont[] = [
  { id: "system", label: "Browser-builtin Monospace", stack: TERM_FONT_FALLBACK },
  { id: "jetbrains-mono", label: "JetBrains Mono", stack: `"JetBrains Mono", ${TERM_FONT_FALLBACK}` },
  {
    id: "source-code-pro",
    label: "Source Code Pro",
    stack: `"Source Code Pro", ${TERM_FONT_FALLBACK}`,
  },
  { id: "fira-code", label: "Fira Code", stack: `"Fira Code", ${TERM_FONT_FALLBACK}` },
  { id: "cascadia-code", label: "Cascadia Code", stack: `"Cascadia Code", ${TERM_FONT_FALLBACK}` },
  { id: "victor-mono", label: "Victor Mono", stack: `"Victor Mono", ${TERM_FONT_FALLBACK}` },
];

export const DEFAULT_TERMINAL_FONT: TerminalFontId = "system";

// termFont resolves a stored id to its entry, and anything it does not recognise to the
// default. Matching positively like this is the same idiom as workspaceExtends() in
// settings.ts and for the same reason: parseSettings spreads stored JSON over the
// defaults without validating it, so the id reaching here can be a stale value, a
// hand-edited one, or a family a later build offers and this one does not.
export function termFont(id: unknown): TerminalFont {
  return (
    TERMINAL_FONTS.find((f) => f.id === id) ??
    TERMINAL_FONTS.find((f) => f.id === DEFAULT_TERMINAL_FONT)!
  );
}

// The offered unzoomed sizes, in px. Not a token and not a rem: xterm measures a character
// cell from this number to lay its grid out, and paints the glyphs onto a canvas that
// inherits no CSS type at all, so the terminal is the one surface in the app whose size has
// to be an absolute number the JS can see.
//
// The floor is 10 and that is not a taste call — it is where the zoom table stops working.
// Every step of ZOOM_SCALES has to round to a distinct px or that step is a keypress with no
// effect, and the user reads the whole zoom feature as unreliable rather than that one step
// as a no-op. At 9px and below the small end of the table collides against termFontPx's 6px
// floor. termFont.test.ts asserts the property across every size offered here, so a size
// added to this list is checked rather than assumed.
export const TERM_FONT_SIZES: readonly number[] = [10, 11, 12, 13, 14, 15, 16, 18, 20, 24];

// 13px: what the terminal has always been, so nobody's panes re-flow because this setting
// arrived.
export const DEFAULT_TERM_FONT_SIZE = 13;

// termFontSize resolves a stored size the same way termLineHeight resolves a leading —
// snapped to the offered set, and with anything non-numeric treated as no answer at all
// rather than as a number to clamp. See termLineHeight for why snapping rather than
// clamping is what keeps the Settings select honest.
export function termFontSize(v: unknown): number {
  const n = typeof v === "number" ? v : NaN;
  if (!Number.isFinite(n)) return DEFAULT_TERM_FONT_SIZE;
  return TERM_FONT_SIZES.reduce((best, s) => (Math.abs(s - n) < Math.abs(best - n) ? s : best));
}

// The px a terminal is actually painted at: its configured size, scaled by the zoom level of
// the pane it is in. Rounded, because a font size is what xterm measures a cell from and a
// fractional one makes a grid that does not divide the pane.
export function termFontPx(scale: number, basePx: number = DEFAULT_TERM_FONT_SIZE): number {
  // A floor rather than a clamp on the scale table: 6px is already past legibility, and the
  // number that must never reach xterm is a zero or a negative, which would divide by zero
  // in the fit addon's cell measurement. TERM_FONT_SIZES is chosen so this never bites at a
  // size the picker can produce — it is here for the base that did not come from the picker.
  return Math.max(6, Math.round(basePx * scale));
}

// The offered leadings. A multiplier of the font size, which is what xterm's lineHeight
// option is — 1 packs the rows the way a terminal always has, and the steps above it buy
// legibility with rows on screen.
export const TERM_LINE_HEIGHTS: readonly number[] = [1, 1.1, 1.2, 1.3, 1.4, 1.5, 1.75, 2];

export const DEFAULT_TERM_LINE_HEIGHT = 1;

// termLineHeight resolves a stored leading to one of the offered ones.
//
// It SNAPS rather than merely clamping, which makes TERM_LINE_HEIGHTS the closed set the
// picker presents it as. Clamping alone would let a value inside the range but off the list
// — 1.15, from a hand-edited blob or a build that offered a finer scale — through to a
// `<select>` that has no option equal to it, and a select whose value matches nothing
// displays its first option while the setting says otherwise. That is a settings screen
// lying about the state of the app, which is worse than the odd leading it would preserve.
//
// The floor is the part that is not cosmetic: xterm divides by the cell height to work out
// how many rows fit, so a 0 from a corrupt blob is a division by zero and a pane that
// renders nothing, and below 1 the rows overlap, which xterm documents as unsupported.
// Anything non-numeric — a string, a null, a NaN — is not a leading at all and lands on the
// default rather than on a snap of garbage.
export function termLineHeight(v: unknown): number {
  const n = typeof v === "number" ? v : NaN;
  if (!Number.isFinite(n)) return DEFAULT_TERM_LINE_HEIGHT;
  return TERM_LINE_HEIGHTS.reduce((best, h) =>
    Math.abs(h - n) < Math.abs(best - n) ? h : best,
  );
}

// loadTermFont waits for the faces a terminal is about to be painted in to actually be
// available, so the caller can re-measure the grid afterwards.
//
// This is the part that is easy to leave out and impossible to miss once it is wrong.
// xterm sizes a character cell by rendering one and measuring it, and `font-display: swap`
// means that until the woff2 arrives the thing it measures is the fallback. Skip the wait
// and the grid is laid out to the wrong cell: the pane keeps the column count it computed
// from Courier metrics while painting JetBrains Mono, and the shell — which was told that
// column count — wraps its lines in the wrong place. Nothing recovers it until the next
// resize.
//
// Normal, bold and italic are each asked for because a terminal paints all three from SGR
// attributes, and a face that arrives after the measurement is the same bug at a lower
// rate. Rejections are swallowed: every one of them (no FontFaceSet at all, a stack the
// shorthand parser rejects, a fetch that failed) leaves the terminal on the fallback face,
// which is exactly where it already is, so there is nothing for a caller to do about it.
export function loadTermFont(px: number, stack: string): Promise<void> {
  const fonts = globalThis.document?.fonts;
  if (!fonts?.load) return Promise.resolve();
  try {
    return Promise.all([
      fonts.load(`${px}px ${stack}`),
      fonts.load(`bold ${px}px ${stack}`),
      fonts.load(`italic ${px}px ${stack}`),
    ]).then(
      () => undefined,
      () => undefined,
    );
  } catch {
    return Promise.resolve();
  }
}
