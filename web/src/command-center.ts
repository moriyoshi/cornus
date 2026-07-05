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
import { decidePrefixKey, parsePrefix, matchesPrefix, type Disposition } from "./views/terminal/prefix";

// bindMatches decides whether a command's `bind` names the combo just pressed. Two
// spellings, and the difference is not cosmetic:
//
//   PLAIN ("%", "c", "C") — compared against e.key exactly, with Ctrl/Alt/Meta required
//   ABSENT and Shift ignored. The shifted character is already baked into e.key, which is
//   precisely what lets `c` and `C` be two different commands; comparing Shift as well
//   would make "%" (Shift+5) unmatchable.
//
//   CHORD ("Ctrl+O") — parsed and matched like the app's own prefix, modifiers and all.
//   This is the only way to express tmux's `prefix C-o`, and it is why the lookup below can
//   no longer bail out on `e.ctrlKey` before it has looked.
//
// A one-character bind is always plain, so binding the literal "+" still works.
//
// A command may name SEVERAL spellings of one key (see `bind` below); this matches one of
// them, and bindsOf is what turns a command's `bind` into the list to try.
function bindMatches(bind: string, e: KeyboardEvent): boolean {
  if (bind.length === 1 || !bind.includes("+")) {
    return !e.ctrlKey && !e.altKey && !e.metaKey && bind === e.key;
  }
  const spec = parsePrefix(bind);
  return !!spec && matchesPrefix(e, spec);
}

// A single invocable action offered in the command palette. `group` names the
// section it renders under; `hint` is optional right-aligned text (e.g. a glyph or
// mnemonic); `keywords` is extra text the filter matches but does not display.
// `bind` is a tmux-style second key: after the prefix, pressing that key (matched
// against KeyboardEvent.key, so "%" / '"' / "c" / "x") runs the command directly,
// without opening the palette. Shown as the palette accelerator when present.
//
// An ARRAY names several keys for one command, and every one of them is shown: an
// accelerator nobody can see is a key nobody presses. Uniqueness is per SPELLING, not per
// command; the lookup below takes the first match, so a spelling claimed twice silently
// disables the later claimant.
//
// `direct` is the OTHER kind of key: one that runs the command with NO prefix at all. It
// exists for keys a whole genre of software already spent decades teaching — Files' F5
// (copy) and F6 (move) are the orthodox file manager's, and putting a prefix in front of
// them would leave a key that is right in every detail except the one that makes it a
// habit. Two rules keep it from being a free-for-all:
//
//   - It must be a key nothing else in the page wants to TYPE. A function key qualifies; a
//     letter does not, and App.tsx's handler stands aside for text entry regardless.
//   - It takes the key from the BROWSER, so a command claims one only where it is
//     genuinely the better meaning. F5 is Reload; on the Files screen, inside a mount,
//     Copy is what the user reaching for it means, and a reload there would silently throw
//     away every unsaved editor draft in the workspace (files/drafts.ts keeps them in
//     memory only). That is also why a DISABLED direct command still swallows its key,
//     exactly as a prefixed bind does: falling through to Reload to "do nothing" is the
//     one outcome worse than doing nothing.
//
// `tags` are named facets, any number of them, cutting ACROSS groups: a group says
// where a command renders (one section, chosen by the screen), a tag says what kind
// of thing it acts on. The palette filter takes `:name` to require one exactly, so
// `:pane` is a menu of the pane operations however their screens chose to group and
// word them — which is what the per-tile ⋮ opens instead of carrying its own copy of
// that list. Tags are also plain search text, like `keywords`; the `:` is what turns
// a substring guess into an exact requirement.
// `disabled` carries the REASON a command cannot run right now, and its presence is what
// disables it. A string rather than a boolean on purpose: if you cannot say why, the command
// should be omitted instead, because a grey row with no explanation only moves the user's
// question from "where is it" to "why is it like that". Prefer disabling to omitting
// wherever the absence would read as "this screen cannot do that" instead of "not just now".
export interface Command {
  id: string;
  title: string;
  group: string;
  hint?: string;
  keywords?: string;
  tags?: string[];
  bind?: string | string[];
  direct?: string | string[];
  disabled?: string;
  run: () => void;
}

const spellings = (b: string | string[] | undefined): string[] =>
  b === undefined ? [] : typeof b === "string" ? [b] : b;

// bindsOf / directsOf normalize the two fields to the list of spellings a command answers
// to. One definition each, used by the key lookups, by the palette's accelerators, and by
// the tests that check no two commands claim the same key — places that would otherwise
// each decide what an array means.
export function bindsOf(c: Command): string[] {
  return spellings(c.bind);
}
export function directsOf(c: Command): string[] {
  return spellings(c.direct);
}

// A provider yields its commands lazily so their labels/availability can be
// reactive (e.g. a settings toggle whose title flips with the current value).
export type CommandProvider = Accessor<Command[]>;

const ARM_TIMEOUT_MS = 2500;

const center = createRoot(() => {
  const [armed, setArmed] = createSignal(false);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  // What the filter starts with when the palette opens. The prefix key opens it empty;
  // a caller that already knows the neighbourhood — the tile ⋮, which means "pane
  // things" — opens it seeded, so the palette is a menu of that neighbourhood while
  // still being the same searchable list you can widen by deleting the seed.
  const [paletteQuery, setPaletteQuery] = createSignal("");
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
    // tmux-style second key: the reducer routes the post-prefix key to "browser" (emit a
    // browser shortcut). Intercept that slot first — if the combo is bound to a command,
    // run it and swallow the key instead of emitting it. A modifier-carrying combo is no
    // longer excluded up front, because a bind may name one (tmux's `prefix C-o`); what
    // keeps `prefix Ctrl+C` reaching the browser is simply that no command claims it.
    if (wasArmed && d.disposition === "browser") {
      const cmd = allCommands().find((c) => bindsOf(c).some((b) => bindMatches(b, e)));
      if (cmd) {
        disarm();
        // A disabled command still OWNS its key: swallowing without running is the quiet
        // no-op the palette's greyed row promises. Skipping it in the lookup instead would
        // let the key fall through as a browser shortcut, which is a surprise rather than
        // a no-op.
        if (!cmd.disabled) cmd.run();
        return "swallow";
      }
    }
    if (d.armed && !wasArmed) arm();
    else if (!d.armed && wasArmed) disarm();
    if (d.openCommands) openPalette();
    return d.disposition;
  };

  // handleDirectKey runs the command claiming this keydown WITHOUT a prefix, and reports
  // whether one did — the caller swallows the key when it says true. Kept here beside
  // handlePrefixKey so both lookups read the same registry and the same bindMatches; the
  // caller (App.tsx) is what decides the contexts it may fire in at all.
  //
  // Disabled commands own their key, as they do on the prefix path. The reasoning is
  // stronger here, not weaker: the key underneath is the browser's, so falling through
  // would answer "this cannot run just now" by reloading the page.
  const handleDirectKey = (e: KeyboardEvent): boolean => {
    const cmd = allCommands().find((c) => directsOf(c).some((b) => bindMatches(b, e)));
    if (!cmd) return false;
    if (!cmd.disabled) cmd.run();
    return true;
  };

  // dispatchAppKey runs one document keydown past both lookups and reports whether a
  // command consumed it. The SEQUENCE is the content here, and each step earns its place:
  //
  //   1. The prefix machine gets the key first, always. It owns arming, ">" and its own
  //      second-key binds.
  //   2. `wasArmed` blocks the direct lookup for the key that FOLLOWED a prefix. After a
  //      prefix, an unclaimed key is deliberately handed to the browser — that is what the
  //      "pass browser shortcuts" setting is for — and a direct bind catching it there
  //      would take back the very shortcut the sequence exists to emit. `armed()` blocks it
  //      for the prefix keystroke itself.
  //   3. Text entry is skipped. F5 is not typeable, but the field is not reserved to
  //      function keys, and this guard has to be older than the first bind that needs it.
  //
  // It lives here, not in App.tsx, because it is the part worth testing: App owns only
  // "may commands answer at all right now" (no palette, no modal, not inside a terminal),
  // which is about which component holds the keyboard.
  const dispatchAppKey = (e: KeyboardEvent): boolean => {
    const wasArmed = armed();
    if (handlePrefixKey(e) === "swallow") return true;
    if (wasArmed || armed()) return false;
    const t = e.target as Element | null;
    if (t && typeof t.closest === "function" && t.closest("input, textarea, select, [contenteditable='true']"))
      return false;
    return handleDirectKey(e);
  };

  // openPalette shows the palette with `prefill` already in the filter. Everything that
  // opens it goes through here, so the seed can never be left over from the last opening.
  const openPalette = (prefill = "") => {
    setPaletteQuery(prefill);
    setPaletteOpen(true);
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
    paletteQuery,
    openPalette,
    handlePrefixKey,
    handleDirectKey,
    dispatchAppKey,
    registerCommands,
    allCommands,
    disarm,
  };
});

export const armed = center.armed;
export const paletteOpen = center.paletteOpen;
export const setPaletteOpen = center.setPaletteOpen;
export const paletteQuery = center.paletteQuery;
export const openPalette = center.openPalette;
export const handlePrefixKey = center.handlePrefixKey;
export const handleDirectKey = center.handleDirectKey;
export const dispatchAppKey = center.dispatchAppKey;
export const registerCommands = center.registerCommands;
export const allCommands = center.allCommands;
export const disarm = center.disarm;
