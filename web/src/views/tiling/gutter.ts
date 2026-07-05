// Where the PINNED pane chooser lives: whether this browser gets a gutter at all, and which
// side of the viewport it opens on.
//
// The chooser is normally a mode — it arrives on `prefix s`, floats over the tiles, and leaves
// on Enter or Escape. PINNED, it stops being a mode and becomes furniture: a standing list of
// the workspace's panes in a strip the tiles no longer reach into. That is a trade, not an
// improvement, which is why it is opt-in and why it is offered only where the trade is
// affordable — a gutter costs 22rem of workspace for as long as it is up, and the machines
// where that is a reasonable price are the ones with a mouse and a wide window.
//
// Split out of PaneChooser for the same reason ./minimap is: every decision here is a pure
// function of two or three values, so it can be checked without a layout. What is measured is
// the viewport's width and the document's direction, each read at one named place.

import { createRoot, createSignal } from "solid-js";
import type { Accessor } from "solid-js";
import { coarsePointer } from "../../pointer";
import { settings, type ChooserSide } from "../../settings";

// Which side the gutter is on. Deliberately PHYSICAL rather than logical (`start`/`end`): the
// setting offers "Left" and "Right" because that is what the user is pointing at, and the one
// place reading order comes into it is the "auto" answer below.
export type Gutter = "left" | "right";

// The narrowest viewport that gets the pin at all. The gutter is 22rem, so this leaves a
// touch over 38rem of workspace at the default root font — enough for two useful tiles, which
// is the least a workspace can be and still be worth mapping.
//
// A px number rather than rem because `innerWidth` is px and this is a THRESHOLD, not
// arithmetic anything is laid out from: a user who has scaled their root font up has made
// everything bigger including the gutter, and the crossover moves by a few tens of pixels
// either way. Nothing rounds on it.
export const PIN_MIN_VIEWPORT_PX = 960;

// Whether the gutter can be offered. "Desktop browser only" as two questions the device can
// actually answer: a fine pointer (see ../../pointer for why the PRIMARY pointer is the one
// asked about) and a window with room to give up a strip of itself.
//
// Both are needed. A phone fails the width; a tablet in landscape passes it and fails the
// pointer, and it is the one that matters there — a permanent panel on a device with no
// hover, no cursor and a scarce screen is a pane's worth of space spent on a list.
export function canPin(coarse: boolean, viewportPx: number): boolean {
  return !coarse && viewportPx >= PIN_MIN_VIEWPORT_PX;
}

// The languages written right to left, by their base subtag. A hand-kept list because the
// platform's own answer — `Intl.Locale`'s text info — is spelled `textInfo` in the browsers
// that shipped it first and `getTextInfo()` in the ones that followed the final spec, and is
// absent in older ones still; a feature test for both spellings plus a fallback is more code
// than the fallback alone, and this list is the fallback. Every language with a living RTL
// script is here.
const RTL_LANGUAGES = new Set([
  "ar", // Arabic
  "arc", // Aramaic
  "ckb", // Central Kurdish
  "dv", // Divehi
  "fa", // Persian
  "he", // Hebrew
  "ku", // Kurdish
  "ps", // Pashto
  "sd", // Sindhi
  "ug", // Uyghur
  "ur", // Urdu
  "yi", // Yiddish
]);

// The reading direction stated by a `dir` attribute, or failing that inferred from a language
// tag. `dir` wins because it is a STATEMENT and the language is an inference: `dir="rtl"` on a
// page is someone saying which way this document runs, and no guess from a locale should be
// able to contradict it. `dir="auto"` is neither — it defers to the content — so it falls
// through to the language like an absent attribute does.
export function directionOf(
  dir: string | null | undefined,
  lang: string | null | undefined,
): "ltr" | "rtl" {
  const d = (dir ?? "").trim().toLowerCase();
  if (d === "rtl" || d === "ltr") return d;
  const base = (lang ?? "").trim().toLowerCase().split(/[-_]/)[0];
  return RTL_LANGUAGES.has(base) ? "rtl" : "ltr";
}

// The document's direction, from the two places that can state it.
//
// The BROWSER's language is asked before the document's `lang`, and that ordering is the whole
// of what "auto" promises. index.html serves `lang="en"` unconditionally — the UI copy is
// English — so reading the document first would make "auto" a permanent synonym for "right"
// and the setting's third option a control that can never do anything. `navigator.language` is
// the setting the user actually chose. A build that localises the UI will set `dir` on the
// document, and that statement outranks both.
export function documentDirection(): "ltr" | "rtl" {
  if (typeof document === "undefined") return "ltr";
  const html = document.documentElement;
  const lang = globalThis.navigator?.language || html.getAttribute("lang");
  return directionOf(html.getAttribute("dir"), lang);
}

// The side the chooser takes when nothing has been asked for: the START of the reading
// direction — left in a left-to-right UI, right in a right-to-left one.
//
// The start rather than the end, because of what this panel is a list OF. The numbers are
// positions in layout order, and layout order begins at the workspace's origin, which is the
// start corner: pane 1 is the tile the eye lands on first. A list running 1, 2, 3 down the far
// side of the screen from the panes it numbers reads as a separate thing that happens to have
// numbers in it; on the same side it reads as a margin against the layout. It is also where a
// reader's eye already is, which matters for a readout that is glanced at rather than worked.
export function startSide(dir: "ltr" | "rtl"): Gutter {
  return dir === "rtl" ? "right" : "left";
}

// Which side "auto" means: the same rule, so the setting's default answer and the answer the
// floating panel gives cannot disagree. A named side is an instruction and overrides it.
export function resolveGutter(pref: ChooserSide, dir: "ltr" | "rtl"): Gutter {
  if (pref === "left" || pref === "right") return pref;
  return startSide(dir);
}

// Where the FLOATING panel is anchored. Not governed by `paneChooserSide`, and deliberately:
// that setting is a choice about a permanent piece of furniture — which strip of the screen
// you are willing to give up — and a card that appears for the length of a keystroke is not
// furniture. It follows the language and nothing else.
export function chooserAnchor(): Gutter {
  return startSide(documentDirection());
}

// Whether this browser can offer the pin, as a reactive read.
//
// Module-level and created in a root of its own, like the settings store next door: the answer
// is a property of the WINDOW rather than of any screen, so one listener serves the app for its
// whole life and nothing can leak between screens — every value it produces is re-derived from
// `innerWidth`, so there is no state to go stale.
//
// Sampled on resize alone. The pointer half is re-read on every call (matchMedia is a lookup;
// see ../../pointer), and a device that gains or loses a pointing device — a tablet with a
// keyboard case — invariably changes its window size in the same gesture. A second media
// listener for the case where it does not would cost more than it could ever buy.
export const pinnable: Accessor<boolean> = createRoot(() => {
  const width = () => globalThis.innerWidth ?? 0;
  const [size, setSize] = createSignal(width());
  globalThis.addEventListener?.("resize", () => setSize(width()));
  return () => canPin(coarsePointer(), size());
});

// Which gutter the chooser is pinned to, or null when it is not pinned — the one thing the
// rest of the app asks. Null covers both "the user has not pinned it" and "this device is not
// offered the pin at all", because for every reader they are the same fact: the chooser floats
// and the workspace has the viewport to itself.
export function chooserGutter(): Gutter | null {
  if (!settings().paneChooserPinned || !pinnable()) return null;
  return resolveGutter(settings().paneChooserSide, documentDirection());
}
