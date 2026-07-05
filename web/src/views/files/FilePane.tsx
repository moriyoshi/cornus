import { For, Show, createEffect, createResource, createSignal, onCleanup, onMount } from "solid-js";
import {
  deletePath,
  fsContentURL,
  listDir,
  mkdir,
  renamePath,
  uploadFile,
  type FsEntry,
  type FsLocation,
} from "../../api";
import type { Pane } from "../tiling/layout";
import { claimFocus, holdsFocus } from "../tiling/focusclaim";
import { promptText, confirmModal } from "../../modal";
import { dragSource, dropTarget } from "../../dnd";
import type { DragPoint, DragSourceSpec, DragVia, DropTargetSpec } from "../../dnd";
import { toast, toastError } from "../../toast";
// The transfer itself is pane-independent and is shared with the terminal panes, which
// receive into the directory their shell reports. See views/transfer.ts.
import {
  DND_TYPE,
  askCopyOrMove,
  dragOrigin,
  reportTransfer,
  setDragOrigin,
  transferInto,
} from "../transfer";
import type { DropItem } from "../transfer";

// Re-exported because a file pane is where a transfer is usually picked up, and the
// workspace's cross-pane commands read the type off this module's actions.
export type { DropItem };

// FileData is a file pane's durable payload. A BROWSER pane has just `path` (a
// directory in the virtual namespace). An EDITOR pane also has `open` set (the filename
// within `path` shown in the editor). Kept on the pane node so it travels through
// splits, moves, stacking, and localStorage persistence.
//
// `kind` is what tells it apart from a TERMINAL pane's payload in the same tree: the
// Workspace holds both in one layout (see views/Workspace.tsx), and every seam that has
// to know which is which — the body renderer, the tab label, the close confirmation, the
// split's inherit — reads this one field. It is on the payload rather than on Pane<P>
// because tiling/layout.ts is generic in the payload and deliberately knows nothing
// about it.
export interface FileData {
  kind: "files";
  path: string;
  open?: string;
  // follow is the id of a terminal session whose working directory this pane
  // tracks, set by "Follow this terminal here…". Persisted with the layout, so a
  // reload keeps the pairing — but only until the session goes, since a session id
  // does not survive a BFF restart and a follow pointing at nothing is cleared on
  // sight rather than silently pinning the pane.
  //
  // Any hand navigation clears it (see navigateTo): a pane that both follows and
  // steers would fight whoever is steering.
  follow?: string;
}

// PaneActions is the focused-pane surface the workspace exposes to the command palette
// (and the sub-header refresh/save). It is a discriminated union: a browsing pane offers
// filesystem mutations; an editing pane offers save. The reactive getters let a
// contextual command decide whether it applies right now.
export interface BrowseActions {
  kind: "browse";
  atRoot: () => boolean;
  go: (path: string) => void;
  // selection is every selected row, in listing order — empty when nothing is selected.
  // The palette reads it both to decide which actions apply and to label them, so the
  // multi-row actions (copy, download, delete) and the single-row one (rename) can be
  // offered from the same list.
  selection: () => string[];
  // The same selection as transferable items — virtual paths plus each one's kind, which
  // is what a destination pane needs and cannot work out from a name. Exactly the payload
  // a drag puts on the clipboard, built by the same helper, so the cross-pane copy/move
  // commands and a drag carry the identical thing.
  selectionItems: () => DropItem[];
  // receive puts items into THIS pane's folder — the destination half of a transfer,
  // called by the workspace once a chooser has picked which pane that is. Everything about
  // where files land lives here (ghost rows, arrival flash, read-only refusal), which is
  // why the destination runs it and not the pane that asked.
  receive: (items: DropItem[], move: boolean) => Promise<void>;
  refresh: () => void;
  newFolder: () => void;
  upload: () => void;
  rename: () => void;
  // transferElsewhere is the destination the pane chooser cannot offer: a typed path. The
  // seed is the letter that opened the prompt, "" when it was clicked. Resolves to the
  // folder it sent to, so the workspace can open a tab there, or null if nothing was sent.
  transferElsewhere: (move: boolean, seed: string) => Promise<string | null>;
  remove: () => void;
  download: () => void;
}
export interface EditActions {
  kind: "edit";
  go: (path: string) => void;
  refresh: () => void;
  dirty: () => boolean;
  save: () => void;
}
export type PaneActions = BrowseActions | EditActions;

// FilePane is one independent file-explorer pane: a Finder-style browser over the
// unified virtual namespace whose root lists the mounts (local roots + workloads). Its
// location lives in the pane payload; navigation calls props.navigate. Opening a text
// file calls props.openFile, which the workspace turns into a new editor pane.

const dirOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
const baseOf = (p: string) => (p.includes("/") ? p.slice(p.lastIndexOf("/") + 1) : p);
const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);

function formatSize(e: FsEntry): string {
  if (e.kind === "dir") return "—";
  const u = ["B", "KB", "MB", "GB"];
  let n = e.size;
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${u[i]}`;
}

const icon = (e: FsEntry) => (e.kind === "dir" ? "📁" : e.kind === "symlink" ? "🔗" : "📄");

// isTextName is a best-effort guess for whether a file opens in the editor (vs
// downloads).
export function isTextName(name: string): boolean {
  return /\.(txt|md|ya?ml|json|toml|ini|conf|cfg|env|sh|ts|tsx|js|jsx|css|html?|xml|go|py|rs|sql|log|dockerfile|gitignore)$/i.test(
    name,
  ) || !name.includes(".") || /^\./.test(name);
}

// isImageName reports whether a file opens in the image viewer.
export function isImageName(name: string): boolean {
  return /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i.test(name);
}

export default function FilePane(props: {
  pane: Pane<FileData>;
  navigate: (path: string) => void;
  // openFile asks the workspace to open a text file or an image (in this pane's directory)
  // as a new editor / viewer pane. It takes no "how did you get here" flag: the workspace
  // asks WHERE the pane lands, the same way, whichever gesture arrived — see openInNewPane.
  openFile: (name: string) => void;
  // openDirInNewPane asks the workspace to run "New pane…" aimed at a folder — the
  // Shift-click gesture below. The workspace arms its placement pick; this pane's job ends
  // at naming the folder.
  openDirInNewPane: (path: string) => void;
  // register publishes this pane's actions to the workspace while it is mounted.
  register: (actions: PaneActions) => () => void;
  // refreshAll re-lists every open pane. A drop changes a folder this pane may not be
  // showing (the source of a move is usually another tile), so refreshing only the
  // receiver would leave the moved rows on screen somewhere else.
  refreshAll: () => void;
  // focus makes this pane the workspace's focused one. The pane body already focuses on
  // pointer-down through the tiling chrome, but a band can start in the breadcrumb lane,
  // which is outside it — and a selection the palette does not target is a trap.
  focus: () => void;
  // focused reports whether the workspace considers this pane the focused one, which is
  // not the same as DOM focus being inside it (see the keyboard fallback below).
  focused: () => boolean;
}) {
  const path = () => props.pane.data.path;
  const loc = (): FsLocation => ({ source: "virtual", path: path() });
  const atRoot = () => path() === "";

  // Selection is a SET of names plus two cursors into the listing: `anchor` is where the
  // current range started (a plain click, a bare arrow move, a ctrl-click) and `lead` is
  // the row under the cursor — the one holding DOM focus. A shift-extend recomputes the
  // whole anchor..lead range instead of accumulating rows, so walking a range back in
  // with shift+ArrowUp shrinks it again, the way a file manager does.
  const [sel, setSel] = createSignal<ReadonlySet<string>>(new Set());
  const [anchor, setAnchor] = createSignal<string>();
  const [lead, setLead] = createSignal<string>();

  // Keyed on an object (never falsy) so the virtual root (path "") still fetches.
  const [listing, { refetch }] = createResource(
    () => ({ path: path() }),
    (src) => listDir({ source: "virtual", path: src.path }),
  );

  // listing() RETHROWS a failed fetch — that is how Solid hands the error to a boundary —
  // so every read on the render path goes through `current` instead. The pane draws the
  // error itself, right above a table that still carries `..`, and must not be torn down by
  // the same fact on the way there.
  const current = () => (listing.error ? undefined : listing());
  const entries = () => current()?.entries ?? [];
  // readOnly is the same fact the mount badge shows, and the only one drag handling
  // consults: a pane under a `:ro` bind cannot receive anything, so the drop must be
  // refused at the CURSOR rather than accepted and then 403'd after the release.
  const readOnly = () => current()?.readOnly ?? false;
  // A row is its own drop target only if it is a writable directory. At the virtual root
  // a mount carries its own readOnly; deeper down the pane's applies to every row.
  const rowReadOnly = (e: FsEntry) => e.readOnly === true || readOnly();

  // selection is the selected names in LISTING order, with names the current listing no
  // longer has dropped — a refetch after a delete or a rename can leave the raw set
  // holding rows that are gone. Every action targets this, never the raw set.
  const selection = (): string[] => entries().filter((e) => sel().has(e.name)).map((e) => e.name);
  // The single-row actions (rename) apply only when exactly one row is selected.
  const onlySelected = (): string | undefined => {
    const s = selection();
    return s.length === 1 ? s[0] : undefined;
  };

  const selectOnly = (name: string) => {
    setSel(new Set([name]));
    setAnchor(name);
    setLead(name);
  };
  // toggleAt is the ctrl/cmd-click gesture: flip one row and re-anchor there, so a
  // following shift-extend ranges out from the row you just picked.
  const toggleAt = (name: string) => {
    const next = new Set(sel());
    if (!next.delete(name)) next.add(name);
    setSel(next);
    setAnchor(name);
    setLead(name);
  };
  // extendTo selects the whole run between the anchor and `name`, inclusive.
  const extendTo = (name: string) => {
    const rows = entries();
    const b = rows.findIndex((e) => e.name === name);
    if (b < 0) return;
    const a = rows.findIndex((e) => e.name === anchor());
    const from = a < 0 ? b : a; // no anchor yet (shift-arrow into an empty selection)
    const [lo, hi] = from <= b ? [from, b] : [b, from];
    setSel(new Set(rows.slice(lo, hi + 1).map((e) => e.name)));
    setAnchor(rows[from].name);
    setLead(name);
  };
  const clearSelection = () => {
    setSel(new Set<string>());
    setAnchor(undefined);
    setLead(undefined);
  };

  // Each row's name link registers itself here so keyboard navigation can move the
  // browser's focus onto a sibling row (arrow keys) — real DOM focus, not just a
  // highlight, so the focus ring, Tab order, and command selection all track together.
  const rowRefs = new Map<string, HTMLAnchorElement>();
  // The row elements themselves, for the rubber band's geometry.
  const rowEls = new Map<string, HTMLTableRowElement>();

  // Roving tabindex: exactly one row is in the tab order at a time — the cursor row, or
  // the first when nothing is selected yet — so Tab enters the list on that row and then
  // leaves it (arrows move within), instead of stepping through every file.
  const rovingName = () => {
    const list = entries();
    const cur = lead();
    return cur && list.some((e) => e.name === cur) ? cur : list[0]?.name;
  };

  // Moving DOM focus onto a row normally collapses the selection onto it: a plain click
  // or a Tab into the list means "select just this one". A shift/ctrl gesture has already
  // computed the selection before it moves the cursor, so it passes keep=true to stop
  // onFocus from undoing that. focus() dispatches its focus event synchronously, so the
  // flag is consumed before the reset below.
  let focusKeepsSelection = false;
  const focusRow = (name: string, keep = false) => {
    focusKeepsSelection = keep;
    rowRefs.get(name)?.focus();
    focusKeepsSelection = false;
    // Done HERE as well as in onFocus, because focusing an ALREADY-focused row fires no
    // focus event at all. That used to be unreachable — nothing moved the cursor onto a row
    // without the user asking — but arriving in a pane now parks it on the roving row
    // unselected, so the very next bare ArrowDown resolves back to that same row and would
    // otherwise select nothing and look like a dead key. selectOnly is idempotent, so the
    // ordinary path (event fires, calls it too) is unaffected.
    if (!keep) selectOnly(name);
  };

  // A pane claims DOM focus while it is the FOCUSED one — the shared rule, see
  // tiling/focusclaim.ts. It matters most for a pane the user just asked for by keyboard:
  // the pick's targets vanish the instant it commits, so a pane that does not take the
  // keyboard drops it on <body> and the journey ends outside the app.
  //
  // Where it lands is the ROVING row — the cursor, or the first row when there is none yet —
  // so walking away from a listing and back returns to where you were reading rather than to
  // the top. It waits for the listing, because there is no row to land on before it arrives.
  // `keep` is passed so arriving does not also SELECT that row: the cursor is where the
  // arrows start from, and a pane that selected something nobody picked would arm Delete
  // with it.
  //
  // This used to run ONCE per mount, latched, so that a refresh could not yank the cursor
  // back to the top. The latch also meant a pane could be claimed only on the way in: walking
  // BACK to a listing left the keys wherever they were, which for a terminal is a place where
  // the next keystroke is a command. `holdsFocus` is the honest form of the same guard — the
  // claim is skipped when the keyboard is already somewhere in this pane, which is true of
  // the refresh case and false of the walk-back case.
  let root!: HTMLDivElement;
  claimFocus(
    () => !!entries()[0] && props.focused(),
    () => holdsFocus(root),
    () => {
      const name = rovingName();
      if (name) focusRow(name, true);
    },
  );

  // selectFromClick is the shared mouse gesture for a row and for its name link: shift
  // extends the range from the anchor, ctrl/cmd toggles one row, a bare click selects
  // just that row. It reports whether the click was a MODIFIED one, which is how the
  // name link knows to select instead of opening the file.
  const selectFromClick = (ev: MouseEvent, name: string): boolean => {
    if (ev.shiftKey) {
      extendTo(name);
      focusRow(name, true);
      return true;
    }
    if (ev.ctrlKey || ev.metaKey) {
      toggleAt(name);
      focusRow(name, true);
      return true;
    }
    selectOnly(name);
    focusRow(name);
    return false;
  };

  // Drag-select: press a row and drag through the listing to select the run you cross,
  // like a file manager. The press picks the anchor (a plain press collapses onto it, a
  // shift-press keeps the one you already had), and every row the pointer then enters
  // becomes the lead. A ctrl/cmd press is a toggle, not a range, so it starts no drag.
  //
  // The mouseup that ends a drag is caught on `window`: releasing outside the listing —
  // over the pane chrome, another pane, or off the window entirely — must still end it,
  // or the next stray pointer move would keep painting a selection.
  let dragSelecting = false;
  let pressedRow: HTMLTableRowElement | null = null;
  const beginDragSelect = (ev: MouseEvent, name: string) => {
    if (ev.button !== 0 || ev.ctrlKey || ev.metaKey) return;
    // Which gesture this press is: a DRAG when it lands on the file name, or anywhere on
    // a row that is already selected; a SWEEP otherwise. Decided before the press changes
    // the selection, so "already selected" means as the user found it.
    const onName = !!(ev.target as HTMLElement).closest("a");
    const drag = onName || sel().has(name);

    // Pressing a row that is ALREADY selected leaves the selection alone, so a drag can
    // carry the whole group; a click that turns out not to be a drag still collapses onto
    // the row, because the click handler runs on release and a completed drag fires no
    // click. Pressing an unselected row selects it, so a drag from it carries it.
    if (ev.shiftKey) extendTo(name);
    else if (!sel().has(name)) selectOnly(name);

    // Rows are draggable so a press anywhere on one can start a drag. The two gestures
    // cannot share a press — once a drag begins the browser stops sending mousemove, so
    // the sweep would never see the pointer — so a sweeping press turns THIS row's
    // draggability off for its duration. Chromium reads `draggable` when the drag
    // actually starts rather than at mousedown (verified both ways: a row flipped ON at
    // mousedown does drag, one flipped OFF does not), which is what makes this work at
    // all — and is also why the selection above cannot be what decides it.
    pressedRow = ev.currentTarget as HTMLTableRowElement;
    pressedRow.draggable = drag;
    dragSelecting = !drag;
  };
  // Every press restores the row it touched: a row left undraggable after a sweep would
  // refuse to drag on the NEXT press, when it is selected and should.
  const releasePressedRow = () => {
    if (pressedRow) pressedRow.draggable = true;
    pressedRow = null;
  };
  const dragSelectOver = (name: string) => {
    if (dragSelecting) extendTo(name);
  };
  const endDragSelect = () => {
    if (!dragSelecting) return;
    dragSelecting = false;
    const cur = lead();
    // Park the keyboard cursor where the drag stopped, so shift+Arrow carries on from
    // there. keep=true: the range is already right, and focus must not collapse it.
    if (cur) focusRow(cur, true);
  };
  // Band-select: a drag that starts on any part of this pane that is NOT a control
  // sweeps through the listing and selects the rows it crosses. Nothing is drawn — the
  // row highlight is the feedback — so the band is only a start point and a base set.
  //
  // The hit test is vertical: rows span the full width, so a sweep's horizontal reach
  // never decides membership. Coordinates are the list's CONTENT space (client offset +
  // scroll), which is also what `offsetTop` reports once `.fs-list` is the rows'
  // offsetParent, so the sweep and the rows agree without conversion. A press above the
  // list (the breadcrumb lane) simply lands at a negative y and sweeps down into range.
  let banding = false;
  let bandFromY = 0;
  let bandBase: ReadonlySet<string> = new Set();
  let listEl!: HTMLDivElement;

  const listY = (ev: MouseEvent) => ev.clientY - listEl.getBoundingClientRect().top + listEl.scrollTop;

  const rowsInBand = (top: number, bottom: number): string[] =>
    entries()
      .filter((e) => {
        const el = rowEls.get(e.name);
        if (!el) return false;
        return el.offsetTop + el.offsetHeight > top && el.offsetTop < bottom;
      })
      .map((e) => e.name);

  // applyBand is a whole-set assignment, not a range walk: the band decides membership by
  // geometry on every move, so re-deriving it from scratch is what lets the selection
  // shrink again as you sweep back.
  const applyBand = (names: string[]) => {
    const next = new Set(bandBase);
    for (const n of names) next.add(n);
    setSel(next);
    if (names.length) {
      setAnchor(names[0]);
      setLead(names[names.length - 1]);
    }
  };

  // bandSurface decides whether a press belongs to this pane's empty space. The press is
  // caught on `document` rather than by a JSX handler because the pane's blank areas are
  // not all inside this component: the breadcrumb lane is drawn by the tiling chrome, as
  // a sibling above the pane body, so a press there is matched back to this pane through
  // the shared `.stack` element instead.
  const bandSurface = (ev: MouseEvent): boolean => {
    const t = ev.target as HTMLElement | null;
    const stack = listEl.closest(".stack");
    if (!t || !stack || t.closest(".stack") !== stack) return false; // some other tile
    // Only the tile's visible pane bands; background tabs stay mounted but are hidden.
    if (!listEl.closest(".stack-pane")?.classList.contains("active")) return false;
    // The tab bar is the drag handle for the whole stack, not blank space.
    if (t.closest(".stack-tabs")) return false;
    // Anything that acts on click owns its own press: listing rows (the row drag-select
    // and, on `..`, go-up), links, buttons (refresh, the split-edge overlays), form
    // controls. The COLUMN header is not one of them — nothing sorts on click — so it
    // bands like any other blank stretch. `tbody tr` rather than `tr` is what draws that
    // line; if the columns ever become sortable, they become controls and drop out here.
    return !t.closest("tbody tr, a, button, input, select, textarea, [role='button']");
  };

  const beginBand = (ev: MouseEvent) => {
    if (ev.button !== 0 || !bandSurface(ev)) return;
    banding = true;
    bandFromY = listY(ev);
    // A plain sweep replaces the selection — which also makes a bare click on blank space
    // deselect, since a zero-height band matches no rows. Shift/ctrl adds to what you had.
    bandBase = ev.shiftKey || ev.ctrlKey || ev.metaKey ? new Set(sel()) : new Set<string>();
    if (!bandBase.size) clearSelection();
    props.focus();
  };
  const moveBand = (ev: MouseEvent) => {
    if (!banding) return;
    const y = listY(ev);
    applyBand(rowsInBand(Math.min(bandFromY, y), Math.max(bandFromY, y)));
  };
  const endBand = () => {
    banding = false;
  };

  onMount(() => {
    const up = () => {
      endDragSelect();
      endBand();
      releasePressedRow();
    };
    document.addEventListener("mousedown", beginBand);
    window.addEventListener("mouseup", up);
    window.addEventListener("mousemove", moveBand);
    onCleanup(() => {
      document.removeEventListener("mousedown", beginBand);
      window.removeEventListener("mouseup", up);
      window.removeEventListener("mousemove", moveBand);
    });
  });

  // go moves this pane to a new virtual path (persisted in the tree via navigate).
  const go = (nextPath: string) => {
    clearSelection();
    // A ghost stands for something arriving in THIS folder; leaving the folder leaves it
    // behind. (Its transfer still completes — the toast still reports it.)
    setPending([]);
    rowRefs.clear();
    rowEls.clear();
    props.navigate(nextPath);
  };

  const childPath = (name: string) => joinPath(path(), name);
  const childLoc = (name: string): FsLocation => ({ source: "virtual", path: childPath(name) });

  const enterDir = (name: string) => go(childPath(name));

  const goUp = () => {
    if (!path()) return;
    go(dirOf(path()));
  };

  // isSelectGesture reports a click held with a selection modifier. Those clicks belong
  // to the selection, never to the row's call to action: shift-clicking twice while
  // building a range also lands a DBLCLICK, and without this the second click would
  // descend into the folder (or open the file) out from under the range you were
  // extending. The name link makes the same check through selectFromClick.
  const isSelectGesture = (ev: MouseEvent) => ev.shiftKey || ev.ctrlKey || ev.metaKey;

  // ONE GESTURE, ONE ACTIVATION. A click on a NAME opens straight away, so a double-click
  // on a name is not one gesture the browser reports twice — it is a click that acts, and
  // then a second click plus a dblclick that arrive after the listing has already been
  // replaced under the pointer. Every one of the three used to act:
  //
  //   click    detail=1  "project"      at ""                    -> enters project
  //   click    detail=2  "many-files"   at "project"             -> enters project/many-files
  //   dblclick detail=2  "many-files"   at "project/many-files"  -> project/many-files/many-files
  //
  // The last one is the damaging one: its FsEntry came from the listing two navigations ago,
  // and childPath joins that stale name onto the current path, so the pane lands on a
  // directory that does not exist. The 404 then took the whole table with it, `..` included,
  // which is why this read as "the explorer stopped working" rather than "it went too far".
  //
  // So the rule is one activation per PRESS SEQUENCE, and the sequence is what has to be
  // tracked — not the event kind and not the element under the pointer. `event.detail` is
  // the browser's own count of the clicks in one sequence: 1 opens a new one, anything
  // above continues the one before. Which is why the obvious spellings do not work. A
  // per-event `detail > 1` test leaves the dblclick, which carries detail 2 and would
  // still fire; and asking whether the dblclick landed on the name misses the case that
  // actually happens, where the row has changed under the pointer and the second click
  // lands on the <td> AROUND the new row's link rather than in it.
  //
  // Anything carrying detail 0 or 1 OPENS a sequence: a fresh press, and also every click
  // that never came from a pointer at all — Enter on a focused link, a synthesised click —
  // which report 0 and must never be mistaken for the tail of an earlier gesture. The first
  // thing that would act CLAIMS the sequence, and everything after it stands down.
  //
  // Both events do the opening, and they have to. Mousedown is the earliest and catches a
  // press that lands somewhere with no click handler of its own (a `..` cell); the click
  // catches what mousedown cannot see, since a press on the NAME is stopped from bubbling
  // to the list and a keyboard activation has no press behind it at all.
  let answered = false;
  const beginGesture = (ev: MouseEvent) => {
    if (ev.detail <= 1) answered = false;
  };
  const claimGesture = () => (answered ? false : (answered = true));

  // Activate a row: descend into dirs (blocking stopped workload mounts), open text files
  // and images into a new pane, download the rest. ONE behaviour per row kind, whether the
  // row was double-clicked, its name clicked, or Enter pressed on it — an open that stacked
  // silently for the mouse and asked for the keyboard was two commands wearing one name.
  const activate = (e: FsEntry) => {
    // The row has to still be in the listing on screen. `e` is captured by the row that
    // drew it, and a gesture can outlive that row — the dblclick above is exactly such a
    // case — after which childPath would join its name onto a path it never belonged to.
    // Identity, not name: a folder of the same name in the new listing is a different row.
    if (!entries().includes(e)) return;
    if (e.running === false) {
      toastError(`${e.name} is not running`);
      return;
    }
    if (e.kind === "dir") enterDir(e.name);
    else if (isTextName(e.name) || isImageName(e.name)) props.openFile(e.name);
    else download(e.name);
  };

  const newFolder = async () => {
    const name = (await promptText({ title: "New folder", label: "Folder name", confirmLabel: "Create" }))?.trim();
    if (!name) return;
    try {
      await mkdir(childLoc(name));
      toast(`created ${name}`);
      void refetch();
    } catch (e) {
      toastError(String(e));
    }
  };

  let fileInput!: HTMLInputElement;
  const onUpload = async (files: FileList | null) => {
    if (!files || !files.length) return;
    try {
      for (const f of Array.from(files)) await uploadFile(loc(), f);
      toast(`uploaded ${files.length} file(s)`);
      void refetch();
    } catch (e) {
      toastError(String(e));
    } finally {
      fileInput.value = "";
    }
  };

  // eachSelected runs one request per selected row and keeps going past a failure, so a
  // single undeletable file in a batch does not strand the rest. It returns both sides:
  // the messages of whatever failed (the caller reports them) and the names that landed
  // (the caller can point at them).
  const eachSelected = async (
    names: string[],
    op: (name: string) => Promise<unknown>,
  ): Promise<{ ok: string[]; failed: string[] }> => {
    const ok: string[] = [];
    const failed: string[] = [];
    for (const n of names) {
      try {
        await op(n);
        ok.push(n);
      } catch (e) {
        failed.push(`${n}: ${e}`);
      }
    }
    return { ok, failed };
  };

  // report states the outcome of a batch and always refetches: after a partial failure
  // the listing has changed for the rows that DID succeed. The sentence itself is shared
  // with every other destination (a terminal pane has no listing to refetch), so only the
  // refetch is this pane's.
  const report = (total: number, failed: string[], verb: string) => {
    reportTransfer(total, failed, verb);
    void refetch();
  };

  // Rename is single-row by nature: it asks for ONE new name, so it applies only when
  // exactly one row is selected (the palette hides it otherwise).
  const rename = async () => {
    const name = onlySelected();
    if (!name) return;
    const next = (
      await promptText({ title: "Rename", label: "New name", initial: name, confirmLabel: "Rename" })
    )?.trim();
    if (!next || next === name) return;
    try {
      await renamePath(childLoc(name), joinPath(path(), baseOf(next)));
      selectOnly(next);
      toast(`renamed to ${next}`);
      void refetch();
    } catch (e) {
      toastError(String(e));
    }
  };

  // transferElsewhere is the pane chooser's escape hatch: a destination that is not one of
  // the open panes. It asks for a FOLDER and then runs the same transfer a drop or a
  // cross-pane pick runs, so the typed destination gets the preflight, the overwrite
  // confirmation and the per-item reporting that the old "Copy … to…" prompt never had —
  // that one issued a bare copy per file and overwrote whatever was in the way in silence.
  //
  // `seed` is the letter that opened it: the chooser hands the keystroke through so the
  // dialog opens already holding the first character the user typed (see caretAtEnd in
  // modal.ts). "" means the row was clicked instead, and then this folder is the starting
  // suggestion, as the old prompt's was.
  //
  // ONE ITEM IS NOT SPECIAL ANY MORE. The old prompt asked a single row for a full virtual
  // PATH, prefilled with the source — which doubled as copy-and-rename in one step. A
  // "location" is a folder whether you are sending one file or forty, and a field that
  // silently means two different things depending on the selection size is a field you
  // have to count rows to read. Copy then Rename (`e`) is the two-step form.
  // It RETURNS the folder it used, because the workspace opens a tab there afterwards: a
  // typed destination is the one kind that no pane is showing, so without that tab the
  // files land somewhere the user has no view of and no easy way to reach. null means
  // nothing was sent — the prompt was dismissed, or the answer was this folder.
  const transferElsewhere = async (move: boolean, seed: string): Promise<string | null> => {
    const items = itemsOf(selection());
    if (!items.length) return null;
    const what = items.length === 1 ? baseOf(items[0].path) : `${items.length} items`;
    const dir = (
      await promptText({
        title: `${move ? "Move" : "Copy"} ${what} to…`,
        label: "Destination folder (virtual path)",
        initial: seed || path(),
        caretAtEnd: !!seed,
        confirmLabel: move ? "Move" : "Copy",
      })
    )?.trim();
    if (!dir || dir === path()) return null;
    await receiveInto(dir, items, move);
    return dir;
  };

  const remove = async () => {
    const names = selection();
    if (!names.length) return;
    const kinds = new Map(entries().map((e) => [e.name, e.kind]));
    const ok = await confirmModal({
      title: "Delete",
      message: names.length === 1 ? `Delete "${names[0]}"?` : `Delete ${names.length} items?`,
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    const { failed } = await eachSelected(names, (n) => deletePath(childLoc(n), kinds.get(n) === "dir"));
    clearSelection();
    report(names.length, failed, "deleted");
  };

  const download = (name: string) => {
    const a = document.createElement("a");
    a.href = fsContentURL(childLoc(name), true);
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
  };

  // ---- drag and drop --------------------------------------------------------------
  // Three drops are supported: rows onto another pane or folder (in-app copy/move), OS
  // files onto a listing (upload), and a row out to the desktop (download, Chromium
  // only). DND_TYPE (views/transfer.ts) carries the in-app payload — virtual paths, so the
  // receiving pane never needs to know which pane sent them, and a TERMINAL pane can take
  // the same drop into the directory its shell reports.
  // How many items the drag ghost names one by one before it starts summarising.
  const GHOST_ROWS = 5;
  // How long a just-arrived row stays marked. Long enough to catch the eye after a drop
  // that also had to round-trip the BFF, short enough not to look like a selection.
  const ARRIVED_MS = 2000;
  // How long a ghost may stand if nothing ever replaces or wipes it.
  const PENDING_MS = 30000;
  const [dropInto, setDropInto] = createSignal<string | null>(null); // dir row name, "" = this folder

  // itemsOf turns row names into a transfer payload. It carries each item's KIND, not just
  // its path: the receiving pane cannot stat what it is given, and only the kind tells it
  // whether the BFF can do the job. One builder for both routes out of this pane — the
  // drag's dataTransfer and the commands' selectionItems — because a second one would be
  // free to disagree about the part a destination cannot check.
  const itemsOf = (names: string[]): DropItem[] =>
    names.map((n) => ({
      path: childPath(n),
      dir: entries().find((r) => r.name === n)?.kind === "dir",
    }));

  // The drag image: a bare label on a transparent canvas, in place of the browser's default
  // (a snapshot of the source element, which comes with whatever is painted behind it — for
  // a selected row, its fill). It NAMES what it carries, one line per item with the same
  // glyph the listing uses. Only past GHOST_ROWS does it summarise, and even then it keeps
  // the first GHOST_ROWS lines and puts the count in a last one — "+3 more" tells you the
  // size without replacing the identities of everything you picked.
  //
  // Its lifetime belongs to dnd.ts, which is what makes one builder serve both transports:
  // the native drag snapshots it off-screen and drops it a tick later, the emulated one
  // flies it under the finger until the drop.
  const makeGhost = (names: string[]): HTMLElement => {
    const ghost = document.createElement("div");
    ghost.className = "fs-drag-ghost";
    for (const n of names.slice(0, GHOST_ROWS)) {
      const row = document.createElement("div");
      row.className = "fs-drag-ghost-row";
      const e = entries().find((r) => r.name === n);
      row.textContent = e ? `${icon(e)} ${n}` : n;
      ghost.appendChild(row);
    }
    if (names.length > GHOST_ROWS) {
      const more = document.createElement("div");
      more.className = "fs-drag-ghost-more";
      more.textContent = `+${names.length - GHOST_ROWS} more`;
      ghost.appendChild(more);
    }
    return ghost;
  };

  // Every row is a drag source. On the NATIVE transport the name is the handle and the rest
  // of the row sweeps a selection instead (beginDragSelect turns the row's `draggable` off
  // for the duration of such a press); the emulated one has no sweep to protect — that
  // gesture is mouse-only — so a long press anywhere on a row picks it up.
  const rowDrag = (name: string): DragSourceSpec => ({
    start: (via: DragVia) => {
      // Dragging carries the SELECTION. A press on an unselected row has already made it
      // the selection by the time a native drag can start (mousedown selects); a finger has
      // selected nothing, and then this is the one row it is on.
      const names = selection().includes(name) ? selection() : [name];
      const items = itemsOf(names);
      const data: Record<string, string> = {
        [DND_TYPE]: JSON.stringify({ items }),
        "text/plain": items.map((i) => i.path).join("\n"),
      };
      // Dragging a single FILE out of the browser downloads it. DownloadURL is a Chromium
      // extension — elsewhere it is simply ignored — and it is meaningless off the native
      // transport, whose whole distinction is that it can leave the page at all.
      const e = entries().find((r) => r.name === name);
      if (via === "native" && names.length === 1 && e && e.kind !== "dir") {
        const url = new URL(fsContentURL(childLoc(name), true), location.href).href;
        data.DownloadURL = `application/octet-stream:${name}:${url}`;
      }
      setDragOrigin(path()); // so no pane offers a drop back into this same folder
      return { data, effects: "copyMove", ghost: makeGhost(names) };
    },
    // Whether it ended in a drop or not, the drag is over: forget where it came from, or
    // the next drop into this folder stays refused.
    end: () => setDragOrigin(null),
  });

  // A drop needs a real directory to land in, so the virtual root (whose entries are
  // mounts, not paths) never accepts one. Nor does the folder the drag came FROM: copying
  // items into the directory that already holds them is not a thing anyone means to do,
  // so that drop is not offered at all — no highlight, no drop cursor. Dropping onto a
  // subfolder ROW of the source pane is a different directory and stays available.
  const acceptsDrop = (p: DragPoint, destDir: string, rowRO = false): boolean => {
    if (atRoot()) return false;
    // Nothing lands in a read-only mount — not an in-app transfer, not an upload from
    // the desktop. Refusing here is what makes the cursor say "no" during the drag.
    if (readOnly() || rowRO) return false;
    if (p.payload.has("Files")) return true; // an upload from the desktop always lands
    if (!p.payload.has(DND_TYPE)) return false;
    return dragOrigin() !== destDir;
  };

  // A drop target for this pane's own folder (name === null) or for one of its directory
  // ROWS. A file row is not a target at all: its point falls through to the folder behind
  // it, which is what `dragover` bubbling did before and what the hit test's walk does now.
  //
  // rowRO is a thunk, not a value: a row's read-only flag is read from the listing, and a
  // target registered when the row mounted would go on answering with whatever was true
  // then. The copy/move cursor is left at its default — Shift makes it a move, so the
  // destructive one is the one you ask for.
  const dirTarget = (name: string | null, rowRO: () => boolean = () => false): DropTargetSpec => ({
    accepts: (p) => acceptsDrop(p, name ? childPath(name) : path(), rowRO()),
    over: () => setDropInto(name ?? ""),
    leave: () => setDropInto(null),
    drop: (p) => void onDrop(p, name),
  });

  // A transfer takes a round trip per file, and until it lands the target pane shows no
  // sign it is happening. `pending` is what is ON ITS WAY IN: ghost rows drawn the moment
  // a drop starts, so the pane admits the transfer immediately. A ghost is replaced by
  // the real row when the listing comes back with it (the effect below), and wiped at
  // once if its transfer failed — it was never a file.
  const [pending, setPending] = createSignal<{ name: string; dir: boolean }[]>([]);
  let pendingTimer: ReturnType<typeof setTimeout> | undefined;
  const addPending = (incoming: { name: string; dir: boolean }[]) => {
    setPending((p) => [...p, ...incoming.filter((i) => !p.some((q) => q.name === i.name))]);
    // Backstop: a ghost must not outlive the operation that made it, whatever happens to
    // the listing that was supposed to replace it.
    clearTimeout(pendingTimer);
    pendingTimer = setTimeout(() => setPending([]), PENDING_MS);
  };
  const dropPending = (names: string[]) => {
    if (!names.length) return;
    const gone = new Set(names);
    setPending((p) => p.filter((g) => !gone.has(g.name)));
  };
  onCleanup(() => clearTimeout(pendingTimer));

  // A ghost's job ends the moment the listing has the real row.
  createEffect(() => {
    const have = new Set(entries().map((e) => e.name));
    const still = pending().filter((g) => !have.has(g.name));
    if (still.length !== pending().length) setPending(still);
  });

  // flashArrived marks what a drop just put here, so the eye can find it without reading
  // the whole listing. Rows land in THIS folder only when the drop targeted the pane; a
  // drop onto a folder row put them out of sight inside it, so the folder that received
  // them is what gets marked instead. The mark is temporal — it clears itself, and the
  // timer is replaced (never stacked) when a second drop lands during the first.
  const [arrived, setArrived] = createSignal<ReadonlySet<string>>(new Set());
  let arrivedTimer: ReturnType<typeof setTimeout> | undefined;
  const flashArrived = (bases: string[], destDir: string) => {
    if (!bases.length) return;
    // The folder row only when it IS a row here. `destDir !== path()` used to be enough,
    // because the only other destination was a subfolder of this listing; a typed path can
    // now name a folder anywhere, and marking `baseOf` of it would flash whichever unrelated
    // row happened to share that name.
    const child = dirOf(destDir) === path() ? [baseOf(destDir)] : [];
    setArrived(new Set(destDir === path() ? bases : child));
    clearTimeout(arrivedTimer);
    arrivedTimer = setTimeout(() => setArrived(new Set<string>()), ARRIVED_MS);
  };
  onCleanup(() => clearTimeout(arrivedTimer));

  const onDrop = async (p: DragPoint, name: string | null) => {
    // Defence in depth behind acceptsDrop: a file row is not its own drop target, so its
    // point falls through to the pane, and the pane may be the read-only one.
    if (readOnly()) {
      setDropInto(null);
      toastError("this mount is read-only");
      return;
    }
    const destDir = name ? childPath(name) : path();
    if (!acceptsDrop(p, destDir)) return;
    setDropInto(null);
    setDragOrigin(null);

    const osFiles = Array.from(p.payload.files);
    if (osFiles.length) {
      const names = osFiles.map((f) => f.name);
      if (destDir === path()) addPending(names.map((name) => ({ name, dir: false })));
      const { ok, failed } = await eachSelected(names, (n) =>
        uploadFile({ source: "virtual", path: destDir }, osFiles.find((f) => f.name === n)!),
      );
      dropPending(names.filter((n) => !ok.includes(n))); // the ones that never arrived
      report(osFiles.length, failed, "uploaded");
      flashArrived(ok, destDir);
      props.refreshAll();
      return;
    }

    const raw = p.payload.get(DND_TYPE);
    if (!raw) return;
    let dropped: DropItem[] = [];
    try {
      dropped = (JSON.parse(raw) as { items?: DropItem[] }).items ?? [];
    } catch {
      return;
    }
    // NOT via the question, and not merely to save a promise: the pane admits a transfer
    // the instant it starts (`receiveInto` draws its ghost rows before its first await), and
    // an `await` here — even one that resolves immediately — puts that behind a microtask.
    // The mouse's answer is already in the event, so it never has to wait for it.
    if (p.via === "native") {
      await receiveInto(destDir, dropped, p.shiftKey);
      return;
    }
    const move = await askCopyOrMove(destDir, dropped);
    if (move === null) return; // the question was dismissed: the drop never happened
    await receiveInto(destDir, dropped, move);
  };

  // receiveInto is this pane AS A DESTINATION: the shared transfer (views/transfer.ts)
  // plus the three things only a pane showing the destination can do — ghost rows while it
  // is in flight, the arrival flash after it lands, and the read-only refusal its own
  // listing already knows about. Three callers — a drop (the folder the pointer was over),
  // `receive` below (THIS pane's folder, when a cross-pane command chose this pane), and
  // transferElsewhere (a typed path).
  //
  // The first two are why it belongs to the DESTINATION pane: run from the source pane, a
  // cross-pane copy would draw its ghosts in the wrong listing. The third is the exception
  // that has to be handled rather than assumed away — a typed path may name a folder this
  // pane knows nothing about, so each destination-local behaviour is conditioned on the
  // destination actually being here.
  const receiveInto = async (destDir: string, incoming: DropItem[], move: boolean) => {
    if (!incoming.length) return;
    // This pane's listing describes its own mount, so its read-only flag answers for a
    // destination inside this folder and for nothing else. A typed path somewhere else is
    // the server's to refuse — the preflight reports it as a refusal like any other, which
    // is the same message by a slower route. Refusing here on the SOURCE pane's flag would
    // instead block a perfectly legal copy OUT of a read-only mount.
    if ((destDir === path() || destDir.startsWith(`${path()}/`)) && readOnly()) {
      toastError("this mount is read-only");
      return;
    }

    // Ghosts only make sense for a transfer into the folder this pane is SHOWING; one onto
    // a subfolder row lands out of sight, and flashArrived marks that folder instead.
    const ghosting = destDir === path();
    await transferInto(destDir, incoming, move, {
      admit: (items) => {
        if (ghosting) addPending(items.map((i) => ({ name: baseOf(i.path), dir: i.dir })));
      },
      wipe: (paths) => {
        if (ghosting) dropPending(paths.map(baseOf));
      },
      arrived: flashArrived,
      report,
      refreshAll: props.refreshAll, // the SOURCE pane is usually a different one
    });
  };

  // Publish this pane's actions to the workspace.
  onMount(() =>
    onCleanup(
      props.register({
        kind: "browse",
        atRoot,
        go,
        selection,
        selectionItems: () => itemsOf(selection()),
        receive: (items, move) => receiveInto(path(), items, move),
        refresh: () => void refetch(),
        newFolder: () => void newFolder(),
        upload: () => fileInput.click(),
        rename: () => void rename(),
        transferElsewhere,
        remove: () => void remove(),
        // One synthetic click per file. Browsers ask once for permission to download
        // multiple files from a page and then let the rest through; nothing here can
        // batch them, since the content endpoint serves a single path.
        download: () => selection().forEach(download),
      }),
    ),
  );

  // At the virtual root each entry is a mount; distinguish local roots from workloads
  // (workloads carry a running flag) for the Kind column.
  const kindLabel = (e: FsEntry) =>
    atRoot() ? (e.running === undefined ? "local" : "workload") : e.kind;

  const onListKey = (ev: KeyboardEvent) => {
    if (ev.key === "Backspace") {
      ev.preventDefault();
      goUp();
      return;
    }
    const rows = entries();
    if (!rows.length) return;
    // Ctrl/Cmd+A selects the whole listing. The cursor stays where it is, so a following
    // shift-arrow ranges from there rather than jumping.
    if ((ev.ctrlKey || ev.metaKey) && (ev.key === "a" || ev.key === "A")) {
      ev.preventDefault();
      setSel(new Set(rows.map((e) => e.name)));
      if (!anchor()) setAnchor(rows[0].name);
      if (!lead()) setLead(rows[0].name);
      return;
    }
    const i = rows.findIndex((e) => e.name === lead());
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      ev.preventDefault();
      const next =
        ev.key === "ArrowDown"
          ? rows[Math.min(rows.length - 1, i < 0 ? 0 : i + 1)]
          : rows[Math.max(0, i < 0 ? 0 : i - 1)];
      // Shift drags the range along with the cursor; a bare arrow moves the single
      // selection (onFocus collapses it onto the new row).
      if (ev.shiftKey) {
        extendTo(next.name);
        focusRow(next.name, true);
      } else {
        focusRow(next.name);
      }
    } else if (ev.key === "Enter") {
      // ".." owns its own Enter (the browser turns it into a click on the link). Acting
      // here as well would ALSO open whatever the row cursor happens to sit on.
      if ((ev.target as HTMLElement).closest(".fs-updir")) return;
      ev.preventDefault();
      const e = rows.find((r) => r.name === lead());
      if (e) activate(e);
    }
  };

  // Keyboard fallback. `onListKey` only fires when DOM focus is on a row, but the pane
  // can be the workspace's focused pane with focus nowhere near it: clicking the tile's
  // tab (a plain div, so it takes no focus), pressing blank space, or sweeping a band
  // from the breadcrumb lane all leave focus on <body>. The arrows would then do nothing
  // at exactly the moment the pane looks like it is yours. This catches those keys on
  // `document` and hands them to the same handler, whose first move puts real focus back
  // onto a row — after which the row handler takes over again.
  const navKey = (ev: KeyboardEvent) =>
    ev.key === "ArrowDown" ||
    ev.key === "ArrowUp" ||
    ev.key === "Enter" ||
    ev.key === "Backspace" ||
    ((ev.ctrlKey || ev.metaKey) && (ev.key === "a" || ev.key === "A"));

  const onGlobalKey = (ev: KeyboardEvent) => {
    if (!props.focused() || !navKey(ev)) return;
    const t = ev.target as HTMLElement | null;
    if (!t) return;
    // Any listing already routes its own keys — including another tile's, which must not
    // be driven from here — and anything focusable owns what it does with Enter.
    if (t.closest(".fs-list, a, button, input, textarea, select, [contenteditable='true'], [role='button']"))
      return;
    onListKey(ev);
  };
  onMount(() => {
    document.addEventListener("keydown", onGlobalKey);
    onCleanup(() => document.removeEventListener("keydown", onGlobalKey));
  });

  return (
    <div
      class="file-pane"
      ref={(el) => {
        root = el;
        onCleanup(dropTarget(el, dirTarget(null)));
      }}
      classList={{ "fs-drop-here": dropInto() === "" }}
    >
      {/* Breadcrumb + refresh live in the sub-header; actions in the command palette.
          The upload picker input stays here, driven hidden. */}
      <input
        ref={fileInput}
        type="file"
        multiple
        style={{ display: "none" }}
        onChange={(e) => void onUpload(e.currentTarget.files)}
      />
      <Show when={current()?.truncated}>
        <p class="badge warn">Listing truncated (directory too large)</p>
      </Show>

      {/* A failed listing replaces the ENTRIES and nothing else. It used to take the whole
          list with it — `..` included — which turned any directory that would not list into
          a pane with no way out but the breadcrumb, and left `listEl` pointing at nothing
          for the band-select's document handler to measure against. The reasons a listing
          fails are exactly the ones you most want to leave a folder over. */}
      <Show when={listing.error}>
        <p class="error">{String(listing.error)}</p>
      </Show>
      {/* Rows carry the roving tabindex, so the list itself is only a focus target
          when empty (nothing to land on) — keeping Backspace-to-go-up reachable. */}
      <div
        class="fs-list"
        ref={listEl}
        tabindex={entries().length ? undefined : 0}
        onMouseDown={beginGesture}
        onKeyDown={onListKey}
      >
        <table class="grid">
          <thead>
            <tr>
              <th>
                Name
                {/* The same fact that refuses the drop, said out loud. A cursor that
                    says "no" without a reason is just a broken drag. */}
                <Show when={readOnly()}>
                  <span class="badge" title="read-only bind mount: nothing can be written here">ro</span>
                </Show>
              </th>
              <th>Size</th>
              <th>Kind</th>
              <th>Modified</th>
            </tr>
          </thead>
          <tbody>
            <Show when={!atRoot()}>
              {/* ".." is a control, not a listing row: it carries a real link so a
                  single click goes up and Tab can reach it (its own stop, ahead of the
                  rows' roving one — like the refresh button). It stays out of the
                  selection and out of the arrow-key cursor, which walk entries only;
                  Backspace remains the from-anywhere way up. */}
              <tr
                class="fs-updir"
                onDblClick={(ev) => {
                  // The link below already went up on this gesture's first click; without
                  // the claim a double-click on ".." climbed two folders at once.
                  beginGesture(ev);
                  if (!isSelectGesture(ev) && claimGesture()) goUp();
                }}
              >
                <td colspan="4">
                  <a
                    href="#"
                    draggable={false}
                    title="Go to the parent folder"
                    onClick={(ev) => {
                      ev.preventDefault();
                      beginGesture(ev);
                      if (!isSelectGesture(ev) && claimGesture()) goUp();
                    }}
                  >
                    <span class="fs-icon" aria-hidden="true">
                      📁
                    </span>
                    <span class="fs-name">..</span>
                  </a>
                </td>
              </tr>
            </Show>
            {/* Bind sources the BFF declined to expose (a kernel pseudo-filesystem, or
                the filesystem root). Shown at the virtual root as inert rows: a mount
                that is simply absent reads as a bug in whoever wrote the compose file,
                and the reason is the only thing that makes the absence make sense. */}
            <For each={atRoot() ? current()?.refused : undefined}>
              {(r) => (
                <tr class="fs-refused">
                  <td>
                    <span class="fs-name muted">🚫 {r.path}</span>
                  </td>
                  <td colspan="3">
                    <span class="muted">{r.reason}</span>
                  </td>
                </tr>
              )}
            </For>
            <For
              each={current()?.entries}
              fallback={
                // "Empty folder." is a FACT about the folder, so it is not what a folder
                // that failed to list says — the error above is already saying it.
                <Show when={!listing.error}>
                  <tr>
                    <td colspan="4">
                      <span class="muted">
                        {atRoot() ? "No local project and no workloads to browse." : "Empty folder."}
                      </span>
                    </td>
                  </tr>
                </Show>
              }
            >
              {(e) => (
                <tr
                  ref={(el) => {
                    rowEls.set(e.name, el);
                    onCleanup(dragSource(el, rowDrag(e.name)));
                    // Only a directory row is its own drop target; a file row lets the
                    // drop fall through to the pane's current folder.
                    if (e.kind === "dir") onCleanup(dropTarget(el, dirTarget(e.name, () => rowReadOnly(e))));
                  }}
                  classList={{
                    "fs-selected": sel().has(e.name),
                    "fs-disabled": e.running === false,
                    // Only a directory row is its own drop target; a file row lets the
                    // drop fall through to the pane's current folder.
                    "fs-drop-here": e.kind === "dir" && dropInto() === e.name,
                    "fs-arrived": arrived().has(e.name),
                  }}
                  // The whole row is the drag source, so a press anywhere on it can
                  // start a drag; beginDragSelect turns this off for a press that means
                  // to sweep instead. dragstart bubbles, so a drag begun on the name
                  // lands here too, and every drag carries the same payload and ghost.
                  draggable={true}
                  onMouseDown={(ev) => beginDragSelect(ev, e.name)}
                  onMouseEnter={() => dragSelectOver(e.name)}
                  onClick={(ev) => {
                    beginGesture(ev);
                    selectFromClick(ev, e.name);
                  }}
                  onDblClick={(ev) => {
                    // A press on the NAME has already been answered by the link's own
                    // click; only a press on the rest of the row activates here. A dblclick
                    // carrying detail 0 was synthesised rather than pressed, so it opens its
                    // own sequence like any other pointerless activation.
                    beginGesture(ev);
                    if (!isSelectGesture(ev) && claimGesture()) activate(e);
                  }}
                >
                  <td class="wrap">
                    <a
                      href="#"
                      ref={(el) => rowRefs.set(e.name, el)}
                      tabindex={rovingName() === e.name ? 0 : -1}
                      // A link is natively draggable and would become the drag source
                      // itself, with its own default ghost; the ROW is the source now.
                      draggable={false}
                      onFocus={() => {
                        if (!focusKeepsSelection) selectOnly(e.name);
                      }}
                      onClick={(ev) => {
                        ev.preventDefault();
                        ev.stopPropagation();
                        beginGesture(ev);
                        // Shift-click a FOLDER NAME and it opens in a new pane. That is the
                        // browser's own idiom — a modified click on a link opens it
                        // somewhere else — pointed at the thing this app opens things in.
                        //
                        // NAME ONLY, and folders only, because Shift is also this
                        // listing's RANGE-EXTEND and the two cannot both have the whole
                        // row. The name is the part that is a LINK, which is what makes
                        // the browser idiom apply to it and not to the space beside it:
                        // shift-clicking anywhere else in a folder's row still extends the
                        // range, as does shift-clicking any file, anywhere. What is given
                        // up is ending a range ON a folder's own text — the widest target
                        // in its row, so this is a real cost and not a theoretical one;
                        // the rest of that row, and the row's whitespace, still do it.
                        //
                        // Bare Shift. A shifted click carrying Ctrl or Alt as well is
                        // somebody else's gesture, and guessing at it would make the
                        // listing's own range-extend unreachable in exactly the case where
                        // the user was most deliberate about which modifiers they held.
                        if (e.kind === "dir" && ev.shiftKey && !ev.ctrlKey && !ev.metaKey && !ev.altKey) {
                          if (claimGesture()) props.openDirInNewPane(childPath(e.name));
                          return;
                        }
                        // A modified click on the NAME selects (or ranges) instead of
                        // opening the file — otherwise shift-clicking a range would
                        // open every file you dragged over.
                        if (selectFromClick(ev, e.name)) return;
                        // Claimed, not merely run: the second click of a double-click on a
                        // name arrives here too, aimed at whatever row the first click's
                        // navigation has since put under the pointer.
                        if (claimGesture()) activate(e);
                      }}
                    >
                      <span class="fs-icon" aria-hidden="true">
                        {icon(e)}
                      </span>
                      <span class="fs-name">{e.name}</span>
                      {/* A `:ro` mount says so on the mount row itself. The kernel holds
                          the container to it, so learning it from a 403 after a drag is
                          learning it too late. */}
                      <Show when={e.readOnly}>
                        <span class="badge" title="read-only bind mount">ro</span>
                      </Show>
                    </a>
                    <Show when={e.running === false}>
                      <span class="muted"> (stopped)</span>
                    </Show>
                    <Show when={e.linkTarget}>
                      <span class="muted"> → {e.linkTarget}</span>
                    </Show>
                  </td>
                  <td>{formatSize(e)}</td>
                  <td>{kindLabel(e)}</td>
                  <td class="muted">{e.mtime?.replace("T", " ").replace("Z", "") ?? ""}</td>
                </tr>
              )}
            </For>
            {/* Items still in flight. They sit after the real rows rather than in sort
                order: a ghost is a promise, not a listing entry, and moving the rows
                around it would be a second thing changing under the pointer. Nothing
                here is interactive — it is not a file yet — so they stay out of the
                selection, the arrow cursor, the band geometry and the drag payload,
                all of which read `entries()`. */}
            <For each={pending()}>
              {(g) => (
                <tr class="fs-ghost" aria-hidden="true">
                  <td class="wrap">
                    <span class="fs-icon" aria-hidden="true">
                      {g.dir ? "📁" : "📄"}
                    </span>
                    <span class="fs-name">{g.name}</span>
                  </td>
                  <td>—</td>
                  <td class="muted">copying…</td>
                  <td />
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
    </div>
  );
}
