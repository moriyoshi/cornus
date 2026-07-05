// Global, persisted UI settings shared across screens: the Settings screen writes
// them, other screens (e.g. the Terminal workspace) read them. A single module
// singleton keeps it consistent with the app's other module-level state (api.ts)
// rather than introducing a context or store library.

import { createSignal, createEffect, createRoot } from "solid-js";
import { DEFAULT_SHELL_CANDIDATES } from "./views/terminal/shells";

// What happens when a command creates a new pane and has to decide WHERE it goes.
// "ask" arms the placement wireframe — the tiles light up and the answer is a keypress.
// The other two are that same question answered once, in advance, for every route:
// "split" is the arrow answer (a tile beside the one you are on) and "tab" is the Space
// answer (a tab on it). They are not new behaviours, which is the point — each is a
// standing choice of an answer the prompt already offers, so nothing is reachable one way
// and not the other.
//
// "auto" is those same two answers again, chosen by the device: a tab where the primary
// pointer is coarse, side by side everywhere else. It exists because the right answer really
// does differ by machine and the same person uses both — a phone or tablet has neither the
// width for two panes nor a comfortable way to resize them, while the desk it syncs to has
// both. Not a fourth behaviour, and nothing here can reach a layout the prompt could not;
// see coarsePointer in ./pointer for what "touch device" is taken to mean and why the
// question is asked per pane rather than once at load.
export type PaneDisposition = "ask" | "split" | "tab" | "auto";

// What a new pane COSTS. "divide" is the tmux answer this workspace shipped with: the tree
// always fits the screen, so a tile is made by halving the one you were on. "extend" is niri's:
// the workspace is bigger than the display, a new tile is added rather than carved out, and the
// screen scrolls to it.
//
// The difference is only visible from the third or fourth split, which is exactly why it is worth
// a setting rather than a decision — under "divide" that is the point where every tile has become
// too small to read and the only way back is to close something.
export type WorkspaceGrowth = "extend" | "divide";

// Which side of the viewport the PINNED pane chooser's gutter opens on. "auto" is the side
// natural to the reading direction, which is the end of it — the right in a left-to-right UI.
// See views/tiling/gutter.ts, which owns that resolution and the "is this a desktop browser"
// question the pin is gated on.
export type ChooserSide = "auto" | "left" | "right";

export interface Settings {
  // The standing answer to "where does this new pane go?", applied by every command that
  // creates one — New pane, Open, Open in a terminal. "ask" (the default) keeps the
  // prompt. See PaneDisposition; the split commands are deliberately not governed by it,
  // because their own titles already name the disposition they make.
  newPaneDisposition: PaneDisposition;
  // Whether a new pane extends the workspace past the viewport or divides the tile it came
  // from. See WorkspaceGrowth. This governs SIZE; newPaneDisposition governs PLACE, and the two
  // are deliberately separate questions — "as a tab" has no size to argue about, and "side by
  // side" is a different answer under each growth.
  workspaceGrowth: WorkspaceGrowth;
  // When true, browser chrome shortcuts are handed back to the browser instead of
  // the terminal. Off by default so a terminal pane captures every key.
  passBrowserShortcuts: boolean;
  // The tmux-style prefix key (e.g. "Ctrl+B") and whether it is active. When on,
  // pressing the prefix then a browser shortcut emits that shortcut, and prefix
  // then ">" opens the terminal command menu.
  prefixEnabled: boolean;
  prefix: string;
  // Whether every tab shows its pane's number, at all times — not only while the pane
  // chooser is up. That is the point of it: `prefix s` then a digit goes straight to a pane,
  // and a number you can only see once the chooser is open is a number you cannot aim with.
  // The chooser's own list and the plate it draws on each tile are NOT covered by this: they
  // are the mode's readout and its correlate on screen, and a chooser with neither is a list
  // of identical rows. This setting governs only the standing copy, the one that sits in a
  // bar the user reads all day beside labels that are often unambiguous already.
  paneNumbersInTabs: boolean;
  // Whether the pane chooser draws the MINI MAP above its list — the workspace at a glance,
  // one rectangle per tile, with a frame around the part of it that is on screen.
  //
  // OFF by default, because the mode's own subject is the panes and the map is a second thing
  // to look at while looking at them. It earns its place on a workspace that has outgrown the
  // screen, where the list can no longer say WHERE a pane is; on one that fits, the tiles are
  // all in front of you already and a picture of them is a picture of what you can see. Which
  // of those a given user is in is not something the app can tell from the layout — a
  // three-pane workspace is either, depending on the display — so it is asked rather than
  // guessed. See views/tiling/minimap.ts.
  paneMiniMap: boolean;
  // Whether the pane chooser is PINNED: taken out of the mode it normally is and stood
  // permanently in a gutter at the side of the viewport, where it lists the workspace's panes
  // whether or not anyone asked for one. Pinned, it stops covering tiles (the workspace gives
  // up the gutter's width instead), it survives Enter and Escape, and a click on a row goes
  // straight to that pane; `prefix s` still arms the walk, and the panel it reports into is
  // this one rather than a second floating copy.
  //
  // OFF by default, and DESKTOP ONLY however it is set: a standing panel costs 22rem of
  // workspace for as long as it is up, which is a fair trade on a wide window with a mouse and
  // a bad one anywhere else. views/tiling/gutter.ts is where that gate lives, so a blob synced
  // from a desktop to a phone is ignored there rather than obeyed here.
  paneChooserPinned: boolean;
  // Which side that gutter opens on. Only read while the chooser is pinned. See ChooserSide.
  paneChooserSide: ChooserSide;
  // Whether a pane's contents can be zoomed — by pinching them, by ctrl+scrolling them, and
  // through the three Zoom commands the workspace registers only while this is on. One
  // switch for the gesture AND the commands, because they are one feature: a palette that
  // offered "Reset zoom" on a screen where nothing can zoom is a row that cannot mean
  // anything, and a gesture with no listed way back is worse on the touch devices this is
  // for than not having it.
  //
  // OFF by default, and opt-in rather than merely defaulted-off: switching it on takes the
  // browser's own pinch-zoom away from terminals, editors and image previews (a pane that
  // claims the gesture has to tell the browser so — see pinch.ts), and that is a trade to be
  // offered, not made on someone's behalf.
  paneZoom: boolean;
  // Candidate interactive shells for a new terminal, one per line, most preferred
  // first. The BFF probes them inside the workload and connects to the first one
  // the image actually has — after the workload's own `x-cornus-shells:`, the shell
  // its entrypoint names, and the connection context's list, which all rank ahead
  // of this. Free text rather than a picker: it is a list of paths, and the images
  // people run are not enumerable.
  shellCandidates: string;
}

export const SETTINGS_KEY = "cornus.settings";

export function defaultSettings(): Settings {
  // Ctrl+Shift+X by default: a Ctrl+Shift+<letter> chord can't be a terminal
  // control byte, so no terminal app (tmux, screen, vim, readline) is bound to it,
  // and plain Ctrl+B still reaches tmux inside a pane.
  // Tab numbers ON by default: they are what tells two identically-labelled tabs apart, and
  // a setting nobody has found yet should leave the feature working.
  // Asking is the default, and has to be: the two standing answers are each right about
  // half the time (an editor beside its listing, a second listing over the one you are
  // done with), and a guess that is silent is a pane to undo. A blob saved before this
  // setting existed therefore keeps behaving exactly as it did.
  // Pane zoom OFF: it is the one setting here whose ON state takes a browser capability away
  // (the native pinch, over three kinds of pane), so the default has to be the browser's.
  // The chooser's mini map OFF: it answers "where is that pane" on a workspace bigger than the
  // screen, and adds a second thing to look at on one that is not. The app cannot tell which
  // case a user is in from the layout alone, so it asks instead of guessing.
  // The chooser UNPINNED, and its gutter on "auto": pinning takes a strip of the workspace and
  // keeps it, which is not a cost to impose on someone who has not asked for the panel. "auto"
  // is the default for the side because it is the only answer that is right on both kinds of
  // machine without being told which one this is.
  // The workspace EXTENDS by default. The dividing layout is the one this screen shipped with,
  // so making it the default would have been the conservative choice — but it is conservative
  // about the wrong thing: a layout that halves a pane every time you make one is not a
  // preference some people hold, it is the failure the extending workspace exists to fix, and
  // leaving it on by default would mean nobody meets the fix without being told about it.
  // "Divide the pane" stays reachable for anyone who wants the tree to fit the screen.
  return {
    newPaneDisposition: "ask",
    workspaceGrowth: "extend",
    passBrowserShortcuts: false,
    prefixEnabled: true,
    prefix: "Ctrl+Shift+X",
    paneNumbersInTabs: true,
    paneMiniMap: false,
    paneChooserPinned: false,
    paneChooserSide: "auto",
    paneZoom: false,
    shellCandidates: DEFAULT_SHELL_CANDIDATES,
  };
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

// workspaceExtends is the one place the growth setting is READ, and it matches "divide"
// positively so that anything else — a stale blob, a hand-edited value, a future variant this
// build does not know — lands on the default rather than on whichever branch happened to be the
// `else`. parseSettings spreads stored JSON over the defaults without validating it, so that is
// not a hypothetical. Same idiom as newPaneDisposition's resolution in views/tiling/drag.ts.
export function workspaceExtends(): boolean {
  return settings().workspaceGrowth !== "divide";
}

export function setNewPaneDisposition(v: PaneDisposition): void {
  store.setSettings((s) => ({ ...s, newPaneDisposition: v }));
}

export function setWorkspaceGrowth(v: WorkspaceGrowth): void {
  store.setSettings((s) => ({ ...s, workspaceGrowth: v }));
}

export function setPassBrowserShortcuts(v: boolean): void {
  store.setSettings((s) => ({ ...s, passBrowserShortcuts: v }));
}

export function setPrefixEnabled(v: boolean): void {
  store.setSettings((s) => ({ ...s, prefixEnabled: v }));
}

export function setPrefix(v: string): void {
  store.setSettings((s) => ({ ...s, prefix: v }));
}

export function setPaneNumbersInTabs(v: boolean): void {
  store.setSettings((s) => ({ ...s, paneNumbersInTabs: v }));
}

export function setPaneMiniMap(v: boolean): void {
  store.setSettings((s) => ({ ...s, paneMiniMap: v }));
}

export function setPaneChooserPinned(v: boolean): void {
  store.setSettings((s) => ({ ...s, paneChooserPinned: v }));
}

export function setPaneChooserSide(v: ChooserSide): void {
  store.setSettings((s) => ({ ...s, paneChooserSide: v }));
}

export function setPaneZoom(v: boolean): void {
  store.setSettings((s) => ({ ...s, paneZoom: v }));
}

export function setShellCandidates(v: string): void {
  store.setSettings((s) => ({ ...s, shellCandidates: v }));
}

