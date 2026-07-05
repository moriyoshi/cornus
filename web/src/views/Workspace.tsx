import { Show, createEffect, createResource, createSignal, onCleanup, onMount } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { createStore, reconcile } from "solid-js/store";
import { getWorkloads, getTerminals, killTerminal, type SessionState } from "../api";
import { pollResource } from "../poll";
import { registerCommands, type Command } from "../command-center";
import {
  loadLayout,
  saveLayout,
  activatePane,
  addTab,
  setRatio,
  updatePane,
  findPane,
  allPanes,
  allStacks,
  nextPaneId,
  rotateStacks,
  stackOf,
  leaf,
  newPane,
  UNIT_EXT,
  type Dir,
  type Ext,
  type LayoutState,
  type Node,
  type Pane,
} from "./tiling/layout";
import {
  minPaneRem,
  RESIZE_STEP,
  applyClose,
  applySplit,
  evenHorizontal,
  resizeEdge,
  resizeExtending,
  resizePane,
  resizeTargetOf,
} from "./tiling/grow";
import { withScrollAnchor } from "./tiling/anchor";
import { twoFingerPan } from "./tiling/pan";
import { revealTile } from "./tiling/reveal";
import { TreeNode, PickBackdrop, EdgeDivider, PANE_TAG, type TileCtx } from "./tiling/panes";
import { PaneChooser } from "./tiling/PaneChooser";
import { chooserGutter } from "./tiling/gutter";
import { createTileDrag } from "./tiling/drag";
import { createPaneChooser } from "./tiling/choose";
import { createLastPane } from "./tiling/lastpane";
import { ZOOM_HOME, canZoom, keepZooms, nudgeZoom, resetZoom, zoomStep } from "./tiling/zoom";
import { settings, workspaceExtends } from "../settings";
import FilePane, {
  isImageName,
  isTextName,
  type BrowseActions,
  type FileData,
  type PaneActions,
} from "./files/FilePane";
import FileEditorPane from "./files/FileEditorPane";
import ImageViewerPane from "./files/ImageViewerPane";
import PaneCrumbs from "./files/PaneCrumbs";
import { forgetDraft } from "./files/drafts";
import TermPane, { type TermCtx, type TermData } from "./terminal/TermPane";
import { mountPathOf, virtualPathOf } from "./workspace/target";
import { transferInto } from "./transfer";
import { isMacPlatform } from "../components/termKeys";
import { confirmModal } from "../modal";

// Workspace is the tiled, tmux-style screen: a binary tree of splits whose tiles are
// stacks of panes shown as tabs. A pane is either a FILE BROWSER on the unified virtual
// namespace or a TERMINAL attached to a persistent BFF session, and the two live in one
// tree — you can put a shell beside the directory it runs in without leaving the screen.
// Split a tile by hovering an edge, drag a tab onto another tile to stack it there (or
// onto an edge to re-tile), drag dividers to resize, ✕ closes a tab. Those are the MOUSE
// gestures; each tile's ⋮ opens the command palette filtered to the `pane` tag, which is
// how those operations are reached by tap (hover and HTML5 drag-and-drop are the two
// primitives a touch device does not have). The layout — splits, ratios, tab stacks, each
// pane's path or session — persists to localStorage. The tree model and chrome are the
// view-agnostic tiling module.
//
// This was two screens, Files and Terminal, and they were the same screen twice: the same
// store, the same ten pane commands on the same ten binds, and a JSX block that was
// character-identical. What kept them apart was only the pane payload, and `tiling/` was
// already generic in it — so the merge is one host over a tagged union, and nothing under
// `tiling/` changed.
// Exported so a test seeds the layout under the key the screen actually reads, rather
// than under a string literal that would go on "passing" the day the key changes.
export const STORAGE_KEY = "cornus.workspace.layout";

// PaneData is what a tile holds. The tag lives on each payload rather than on Pane<P>
// because tiling/layout.ts is generic in the payload and deliberately knows nothing about
// it; see the twin comments on FileData and TermData.
export type PaneData = FileData | TermData;

// TypeScript cannot narrow Pane<PaneData> from a test on pane.data.kind, so these two do
// it — one runtime check each, behind which the cast is honest. Every kind-dependent seam
// goes through them rather than casting at the call site.
const asFiles = (p: Pane<PaneData>): Pane<FileData> | undefined =>
  p.data.kind === "files" ? (p as Pane<FileData>) : undefined;
const asTerm = (p: Pane<PaneData>): Pane<TermData> | undefined =>
  p.data.kind === "term" ? (p as Pane<TermData>) : undefined;

// A fresh pane is a FILE BROWSER at the virtual root. This is the workspace's default —
// what it opens as, and what `New pane…` and the last-pane-closed replacement make — and
// it is why there is no "which kind?" question anywhere: a pane's kind is settled at the
// moment it is created, by which command created it.
const freshData = (): PaneData => ({ kind: "files", path: "" });
// An EMPTY cmd means "discover the shell": the pane asks the BFF which shells the image
// actually has and adopts the best one, writing it back. Hardcoding /bin/sh was wrong at
// both ends — it ignored an image's bash and it dead-ended on an image with no shell.
const termData = (workload: string, dir?: string): TermData => ({
  kind: "term",
  workload,
  cmd: [],
  dir,
});

// A split inherits, whatever the kind: "another one of these, here". For a file pane that
// is the same folder and the same open file; for a terminal it is the same workload,
// command and directory — but NEVER the session, which belongs to the pane that holds it.
//
// The terminal screen used to give its EDGE split a fresh empty pane instead, so the
// picker could aim it at another workload. That does not survive the merge: a fresh pane
// here is a file browser, and splitting a terminal's edge to get a mount list is not what
// the gesture says. Retargeting is what browsing to the other workload and opening a
// terminal there now does, and it says where in the same motion.
const inheritData = (from: Pane<PaneData>): PaneData =>
  from.data.kind === "term"
    ? { kind: "term", workload: from.data.workload, cmd: [...from.data.cmd], dir: from.data.dir }
    : { ...from.data };

const baseNameOf = (p: string) => p.slice(p.lastIndexOf("/") + 1);

// Open's key is the accelerator's modifier plus Enter: Cmd+Enter on a Mac, Ctrl+Enter
// everywhere else. ONE of them is registered, not both — a palette advertising a key that
// does nothing on the machine reading it is worse than advertising none. Decided once at
// module load; the branch itself is `isMacPlatform`'s, which has its own tests.
//
// It reads as "Enter, but elsewhere", which is what it does. Plain Enter on a row is this
// listing's own activation, and what that means depends on the row: a FOLDER is entered in
// place, a FILE is already opened into a pane of its own. The modifier is what makes the two
// one gesture — "not here, somewhere I will point at" — and it is the only way to say that
// about a folder at all. On a file it lands on the same place plain Enter does, which is not
// a redundancy to remove: the key means one thing for every row, which is the property that
// makes it learnable.
//
// It also ARRIVES, which the obvious spelling would not have — Ctrl+T / Cmd+T is on Chrome's
// and Firefox's reserved list, delivered to the browser chrome and never to the page, where
// even preventDefault has no say.
//
// The one thing it costs: Ctrl+Enter used to fall through to the listing's own Enter
// (`onListKey` never looked at the modifier), so holding Ctrl by accident opened the row
// under the cursor. Now it is claimed here. Plain Enter is untouched — the direct lookup
// matches the chord, not the key.
const OPEN_ELSEWHERE_KEY = isMacPlatform() ? "Meta+Enter" : "Ctrl+Enter";

// One predicate per kind, unioned. A pane that satisfies neither invalidates the whole
// persisted tree (loadLayout checks every pane), which is the behaviour we want: a layout
// half of whose panes are unreadable is not a layout to half-restore.
const isValidData = (d: unknown): boolean => {
  const o = d as { kind?: unknown; path?: unknown; workload?: unknown; cmd?: unknown } | undefined;
  if (!o || typeof o !== "object") return false;
  if (o.kind === "files") return typeof o.path === "string";
  if (o.kind === "term") return typeof o.workload === "string" && Array.isArray(o.cmd);
  return false;
};

function defaultLayout(): LayoutState<PaneData> {
  const p = newPane(freshData());
  return { tree: leaf(p), focused: p.id, ext: { ...UNIT_EXT } };
}

export default function Workspace() {
  // The scroll container. Held as a ref rather than looked up, because two things need it
  // synchronously — the anchor that keeps the view still while the workspace grows in front of
  // it, and the px-to-viewport-extent conversion an Alt-dragged divider does.
  let bodyEl!: HTMLDivElement;
  // The canvas, for the edge dividers: they resize the WORKSPACE, so the thing they have to
  // measure is the workspace's own box rather than any tile's.
  let canvasEl: HTMLDivElement | undefined;
  const [state, setState] = createStore<LayoutState<PaneData>>(
    loadLayout(STORAGE_KEY, isValidData, defaultLayout),
  );

  // Reducers run on a detached plain snapshot; reconcile then merges the result into
  // the store in place, so unchanged panes (and their live listings/editors/terminals)
  // keep their DOM and only touched fields update.
  const snapshot = (): Node<PaneData> => JSON.parse(JSON.stringify(state.tree));
  // commit takes the workspace EXTENT alongside the tree, because under the extending layout
  // every structural change can move it and the two have to land in the same store write — a
  // tree committed against the previous extent is one frame of the wrong geometry, and on a
  // near-edge growth that frame is a visible lurch.
  //
  // Wrapped in the scroll anchor, and HERE rather than at the four call sites: every structural
  // change comes through this one function, so the anchor cannot be forgotten by the next
  // operation someone adds — the same argument that puts `keepZooms` on the persistence effect
  // instead of on a close handler.
  const commit = (tree: Node<PaneData>, ext: Ext = state.ext) => {
    withScrollAnchor(bodyEl, () => {
      setState("tree", reconcile(tree));
      if (ext.w !== state.ext.w || ext.h !== state.ext.h) setState("ext", { ...ext });
    });
  };
  const extending = () => workspaceExtends();

  // The minimum pane extent, converted from rem into the viewport-relative units the tiling
  // model works in. This is the one place a real measurement enters the layout: `grow.ts` is
  // deliberately measurement-free so it can be tested arithmetically, so the SIZE OF THE SCREEN
  // has to be supplied from here.
  //
  // A container that has never been laid out reports 0, and a floor of Infinity is the honest
  // reading of that: with no idea how big the screen is, the safe split is the one that cannot
  // make a pane too small to use, which is the extending one. That is also what keeps this
  // invisible to jsdom, where nothing has a size and every split extends.
  const floorFor = (dir: Dir): number => {
    const px = dir === "h" ? bodyEl?.clientWidth : bodyEl?.clientHeight;
    if (!px) return Infinity;
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    return (minPaneRem(dir) * rem) / px;
  };

  // The workload list serves two jobs: the empty terminal pane's picker offers the running
  // ones, and "Open in a terminal" reads it to tell a workload mount from a local one (a
  // mount that is not a workload is a local root) and a running one from a stopped one.
  const [workloads] = createResource(getWorkloads);
  const running = () => workloads()?.filter((w) => w.running) ?? [];

  // Poll the session list for detected activity so each tab can badge its state,
  // and for the window title its output last set so each tab can NAME itself.
  const [sessions] = pollResource(getTerminals, 2000);
  const stateOf = (sessionId: string | undefined): SessionState | undefined => {
    if (!sessionId) return undefined;
    const s = sessions()?.find((t) => t.id === sessionId);
    return s?.alive ? s.state : undefined;
  };
  // titleOf is the live name of a session: what its foreground program calls
  // itself, which pane data cannot hold because it changes with every program the
  // user runs. Unlike stateOf this is NOT gated on alive — a dead session keeps
  // the last name it went by, so a tab does not rename itself in the linger window
  // at the very moment the user is looking for which one ended.
  const titleOf = (sessionId: string | undefined): string | undefined => {
    if (!sessionId) return undefined;
    return sessions()?.find((t) => t.id === sessionId)?.title || undefined;
  };
  // cwdOf is where a session actually IS, as opposed to the `dir` it was told to
  // start in. Empty far more often than the title: the hook that reports it (OSC 7)
  // is not in a stock container image, so every caller must have somewhere to fall
  // back to. Undefined means "this session has never said", NOT "the root" — a
  // caller that defaulted to "/" would send the user somewhere they never were.
  const cwdOf = (sessionId: string | undefined): string | undefined => {
    if (!sessionId) return undefined;
    return sessions()?.find((t) => t.id === sessionId)?.cwd || undefined;
  };

  const setFocus = (id: string) => setState("focused", id);

  // splitAt is the edge-overlay split: it divides a pane and puts an inherited pane on the
  // clicked side, starting where the source is ("open another view here").
  // inheritLive is inheritData with the terminal's LIVE directory substituted for
  // the one it was launched with. "Another one of these, here" has always meant the
  // same workload and command; what it could not mean until the session reported a
  // cwd is the same PLACE, because `dir` is where the source shell was told to
  // start and stops being true the moment the user cd's. A session that reports
  // nothing falls back to `dir`, which is exactly the old behaviour.
  const inheritLive = (from: Pane<PaneData>): PaneData => {
    const data = inheritData(from);
    const live = cwdOf(asTerm(from)?.data.sessionId);
    if (data.kind === "term" && live) data.dir = live;
    return data;
  };

  // Dragging the workspace's own right or bottom border. Pixels in, viewport extents out — the
  // same conversion the Alt-dragged divider makes, and for the same reason: only this side knows
  // how big a screen is.
  const resizeWorkspaceEdge = (dir: Dir, deltaPx: number) => {
    const unit = dir === "h" ? bodyEl?.clientWidth : bodyEl?.clientHeight;
    if (!unit || !deltaPx) return;
    const { tree, ext } = resizeEdge(snapshot(), state.ext, dir, deltaPx / unit);
    commit(tree, ext);
  };

  const splitAt = (paneId: string, dir: Dir, before: boolean) => {
    const { tree, ext, newPaneId } = applySplit(
      extending(),
      snapshot(),
      state.ext,
      paneId,
      dir,
      inheritLive,
      before,
      floorFor(dir),
    );
    if (newPaneId) setState("focused", newPaneId);
    commit(tree, ext);
  };

  // resizeCommands is the four Grow / Shrink commands, built from one table so the four cannot
  // drift apart. Each is DISABLED WITH A REASON rather than absent when the layout has no
  // divider on its axis — a single-tile workspace has nothing to make wider, and a row that
  // vanished would leave the user wondering whether the command exists at all.
  const resizeCommands = (): Command[] =>
    (
      [
        { id: "wider", title: "Make pane wider", dir: "h", by: RESIZE_STEP, bind: "Alt+ArrowRight" },
        { id: "narrower", title: "Make pane narrower", dir: "h", by: -RESIZE_STEP, bind: "Alt+ArrowLeft" },
        { id: "taller", title: "Make pane taller", dir: "v", by: RESIZE_STEP, bind: "Alt+ArrowDown" },
        { id: "shorter", title: "Make pane shorter", dir: "v", by: -RESIZE_STEP, bind: "Alt+ArrowUp" },
      ] as const
    ).map((r) => ({
      id: `ws:resize-${r.id}`,
      group: "Workspace",
      title: r.title,
      bind: r.bind,
      tags: [PANE_TAG],
      disabled: resizeTargetOf(state.tree, state.focused, r.dir)
        ? undefined
        : r.dir === "h"
          ? "nothing is beside this pane to resize against"
          : "nothing is above or below this pane to resize against",
      run: () => {
        const { tree, ext } = resizePane(extending(), snapshot(), state.ext, state.focused, r.dir, r.by);
        commit(tree, ext);
      },
    }));

  // splitFocused is the keyboard split behind the tmux binds (prefix % / prefix "). The
  // mouse gesture picks a side by which edge you hovered; a keyboard split has no side to
  // read, so it always lands the new pane after the focused one — matching splitAt's own
  // `before = false` default.
  const splitFocused = (dir: Dir) => splitAt(state.focused, dir, false);

  // navigateTo moves a file pane to a directory in BROWSE mode (clears any open file),
  // used by both dir entry and the breadcrumb (including from an editor pane back).
  //
  // Navigating BREAKS a follow, and that is the point rather than an oversight: a
  // pane that is following a terminal and is also steerable by hand would fight the
  // user, yanking them back on the next poll. Every caller here is a human gesture
  // (a folder row, the breadcrumb, the editor's way back), so "I went somewhere
  // myself" is exactly the signal that the follow is over. The follow effect is the
  // one caller that passes keepFollow, because it IS the follow.
  const navigateTo = (paneId: string, path: string, keepFollow = false) => {
    // The pane stops editing, so its draft is gone for good — unlike a MOVE, which
    // rebuilds the same editor elsewhere and must keep it (see files/drafts.ts).
    forgetDraft(paneId);
    const patch: Partial<FileData> = { path, open: undefined };
    if (!keepFollow) patch.follow = undefined;
    commit(updatePane(snapshot(), paneId, patch));
  };

  // A following file pane tracks its terminal's directory. Driven off the same 2s
  // session poll everything else here reads, so it costs no extra request.
  //
  // It converges rather than loops: navigating sets `path` to exactly the string
  // this computed, so the next run finds them equal and does nothing. The guard on
  // `cwd` being present is what keeps a session that never reports one from
  // dragging its pane to the mount root.
  createEffect(() => {
    const list = sessions();
    if (!list) return;
    for (const pane of allPanes(state.tree)) {
      const file = asFiles(pane);
      const followed = file?.data.follow;
      if (!file || !followed) continue;
      const s = list.find((t) => t.id === followed);
      // The session is gone (killed, or reaped after its linger): stop following
      // rather than pinning the pane to wherever it was when the shell died.
      if (!s) {
        commit(updatePane(snapshot(), pane.id, { follow: undefined }));
        continue;
      }
      if (!s.cwd) continue;
      const want = virtualPathOf(s.workload, s.cwd);
      if (want && file.data.path !== want) navigateTo(pane.id, want, true);
    }
  });

  // placeHere arms the placement pick carrying `data` — the wireframe targets, the way every
  // creating command in this workspace asks where a pane goes. ONE function because every
  // "open this row" is the same gesture over a different payload: an editor for a text file,
  // the viewer for an image, a listing for a folder.
  //
  // "Asks" is the DEFAULT and not a guarantee: `newPaneDisposition` can answer the question
  // standingly, in which case beginPlace places without raising the scrim. That is decided
  // inside beginPlace rather than here, so it holds for every caller — see drag.ts.
  //
  // The placement carries its payload, which is why beginPlace takes a factory: the user has
  // already named the content, so neither an empty pane nor a copy of the target tile is what
  // they asked for. Focus moves to the source pane first, because a creating pick lights the
  // FOCUSED tile and the row that was pressed is in this one.
  const placeHere = (srcId: string, data: FileData) => {
    setFocus(srcId);
    drag.beginPlace(() => data);
  };

  // openInNewPane opens `filename` (in the source pane's directory) as a new EDITOR or IMAGE
  // VIEWER pane, taking the same route to "where does it land?" as every other creating
  // command — the question by default, or the standing answer in `newPaneDisposition`.
  //
  // It used to ask only for a KEYBOARD open (Enter on a row) and stack a tab silently for a
  // MOUSE one, on the argument that the click had already said where by being on that pane.
  // That argument does not hold: a click on a file NAME says which file, not which tile, and
  // the tile it landed on is the one LISTING the file — the last place with room to show it.
  // The cost of being wrong was also asymmetric. A silent stack put the editor over the
  // listing it came from, and getting it back beside that listing meant undoing a pane; the
  // question costs one keypress (Space stacks it exactly where the old behaviour would have)
  // and every answer is reachable. So both gestures now do the one thing.
  const openInNewPane = (srcId: string, filename: string) => {
    const src = asFiles(findPane(snapshot(), srcId) ?? ({} as Pane<PaneData>));
    if (!src) return;
    placeHere(srcId, { kind: "files", path: src.data.path, open: filename });
  };

  const activate = (id: string) => {
    commit(activatePane(snapshot(), id));
    setState("focused", id);
  };

  // Drag-to-rearrange: dragging a tab (or the whole tab bar = the whole stack) onto the
  // CENTER of another tile STACKS it there as tabs; onto an EDGE moves (re-tiles) it
  // beside that tile. The same protocol backs the tap-to-pick mode — see
  // views/tiling/drag.ts.
  const drag = createTileDrag<PaneData>({
    snapshot,
    ext: () => state.ext,
    floor: floorFor,
    commit,
    setFocused: (id) => setState("focused", id),
    focused: () => state.focused,
    fresh: freshData,
    inherit: inheritLive,
  });

  // The pane chooser — tmux's "choose from a list", walked over the workspace itself.
  const choose = createPaneChooser<PaneData>({
    tree: () => state.tree,
    focused: () => state.focused,
    activate,
    picking: drag.picking,
    endPick: drag.end,
  });

  // Where the focus was before it was here — tmux's last-pane. See lastpane.ts.
  const lastPane = createLastPane(() => state.tree, () => state.focused);

  // ---- pane bodies talking back ---------------------------------------------------------
  //
  // The two kinds do this differently and keep doing it differently. A terminal pane gets a
  // static TermCtx prop (its needs are fixed: record a session, retarget, close); a file
  // pane REGISTERS its actions here, because which actions exist depends on what the pane
  // is doing right now — browsing, editing, editing something unsaved — and the palette
  // has to see the current answer. Collapsing them into one protocol would mean rewriting
  // every file action for no user-visible gain.

  // Each mounted file pane publishes its actions here; the command palette and the
  // title-bar refresh dispatch to the focused pane through this registry. The Map is
  // plain, so anything RENDERED from it also reads `actionsRev` — that signal is what
  // makes a (de)registration re-run the computation.
  const paneActions = new Map<string, PaneActions>();
  const [actionsRev, bumpActions] = createSignal(0, { equals: false });
  const registerActions = (id: string, actions: PaneActions): (() => void) => {
    paneActions.set(id, actions);
    bumpActions(0);
    return () => {
      // Deregister only OUR OWN entry. A move rebuilds the pane under the same id, so
      // the outgoing instance's cleanup and the incoming one's registration both name
      // that id; today the cleanup lands first (the move test would fail otherwise —
      // the tab marker reads this registry), but an unconditional delete would silently
      // unregister the live pane if that order ever changed.
      if (paneActions.get(id) === actions) paneActions.delete(id);
      bumpActions(0);
    };
  };

  const setSession = (id: string, sessionId: string) => commit(updatePane(snapshot(), id, { sessionId }));
  // adopt records a session the PANE created itself (the no-shell prompt): target,
  // command and id in one commit, so the pane is never briefly holding a command
  // with no session — which the create effect would answer by opening a second one.
  const adopt = (id: string, workload: string, cmd: string[], sessionId: string) => {
    commit(updatePane(snapshot(), id, { workload, cmd, sessionId }));
    setState("focused", id);
  };
  const retarget = (id: string, workload: string, cmd: string[]) => {
    const pane = asTerm(findPane(snapshot(), id) ?? ({} as Pane<PaneData>));
    if (pane?.data.sessionId) killTerminal(pane.data.sessionId).catch(() => {});
    // The directory survives a retarget WITHIN one workload and dies when the workload
    // changes. Shell discovery is the first case — it retargets the same pane with the
    // command it found, and dropping the cwd there would silently undo the whole point of
    // "open a terminal here". The picker is the second: /srv/app named a path in the
    // container the user just walked away from, and carrying it to a different image is
    // how a shell fails to start for a reason nobody can see.
    const dir = pane && pane.data.workload === workload ? pane.data.dir : undefined;
    commit(updatePane(snapshot(), id, { workload, cmd, sessionId: undefined, dir }));
    setState("focused", id);
  };

  // A pane is ACTIVE when it owns a session that closing would end: the shell is running
  // something, and killing it is not undone by opening another pane.
  //
  // The session list is a LAGGING mirror of the BFF — it answers every couple of seconds,
  // so a session created since the last answer is simply not in it yet. Absence therefore
  // cannot mean "dead": right after Connect, the pane most in need of the question is
  // exactly the one missing from the list. Only a session the BFF POSITIVELY reports as
  // dead (it lingers, alive:false, for 30s after its shell exits — see term.go) is one
  // there is nothing left to ask about. The residue of reading it this way is one extra
  // dialog for a session already reaped; the residue of reading it the other way is a
  // running shell killed without a word.
  const isActive = (pane: Pane<TermData> | undefined): boolean => {
    const sid = pane?.data.sessionId;
    if (!sid) return false;
    return !sessions()?.some((t) => t.id === sid && !t.alive);
  };

  // dirtyOf reports whether an editor pane holds unsaved changes, for the tab marker.
  // Reads actionsRev first (the Map is plain), then the pane's own dirty() — so the marker
  // tracks both registration and the edit itself.
  const dirtyOf = (id: string): boolean => {
    actionsRev();
    const a = paneActions.get(id);
    return a?.kind === "edit" && a.dirty();
  };

  // selectionOf is a file pane's current multi-selection, for the sub-header badge.
  const selectionOf = (id: string): string[] => {
    actionsRev();
    const a = paneActions.get(id);
    return a?.kind === "browse" ? a.selection() : [];
  };

  // A drag-and-drop transfer changes two folders at once, and the pane that received the
  // drop is rarely the one showing the source. Re-listing every open file pane is cheap
  // (one GET each) and is the only way the moved rows leave the tile they came from.
  const refreshAllPanes = () => {
    for (const a of paneActions.values()) a.refresh();
  };

  // closePaneById closes unconditionally and, for a terminal, KILLS its session. It is the
  // close TermPane itself performs when a shell exits (there is nothing left to ask about);
  // every close the USER asks for goes through requestClosePane below.
  const closePaneById = (id: string) => {
    forgetDraft(id);
    const { tree, ext, removed, focus } = applyClose(extending(), snapshot(), state.ext, id, freshData);
    if (removed?.data.kind === "term" && removed.data.sessionId) {
      killTerminal(removed.data.sessionId).catch(() => {});
    }
    commit(tree, ext);
    setState("focused", focus);
  };

  // requestClosePane is what the ✕ on a tab and `prefix x` run. Two kinds of pane, two
  // things a close can destroy that reopening does not undo — an editor's unsaved draft
  // (files/drafts.ts forgets it, and the file on disk still has the old text) and a live
  // shell (killing it ends whatever it was running). Each asks its own question, and
  // ONLY in that case: anything else closes on the click that asked for it, with no dialog
  // in the way. The confirm is deliberately not awaited on the clean path — making every
  // close async would put a frame between the click and the pane disappearing.
  const requestClosePane = (id: string) => {
    const pane = findPane(snapshot(), id);
    const term = pane && asTerm(pane);
    if (term) {
      if (!isActive(term)) {
        closePaneById(id);
        return;
      }
      // Name the session the same way its tab does, so the dialog is visibly about
      // the pane the user just clicked close on. Quoted rather than used as the
      // sentence's subject: a title is a NAME, and a shell's prompt hook sets
      // things like "user@web: ~/src" that read as nonsense in "X is running".
      const what = titleOf(term.data.sessionId) ?? term.data.cmd.join(" ");
      void confirmModal({
        title: "Close this terminal?",
        message: `"${what || "the shell"}" on ${term.data.workload} is still running. Closing the pane ends its session.`,
        confirmLabel: "Close & end session",
        danger: true,
      }).then((ok) => {
        if (ok) closePaneById(id);
      });
      return;
    }
    if (!dirtyOf(id)) {
      closePaneById(id);
      return;
    }
    const name = (pane && asFiles(pane)?.data.open) ?? "this file";
    void confirmModal({
      title: `Discard unsaved changes to ${name}?`,
      message: "This pane has edits that were never saved. Closing it throws them away.",
      confirmLabel: "Discard & close",
      danger: true,
    }).then((ok) => {
      if (ok) closePaneById(id);
    });
  };

  // ---- cross-pane transfers -----------------------------------------------------------
  //
  // "Copy / Move selected to another pane" is the two-panel file manager's F5 and F6, and
  // the reason it earns commands of its own is that this workspace has more than two panes:
  // "the other one" is not something you can name here, so the destination has to be
  // CHOSEN. It is chosen with the pane chooser — the same mode, keys and numbers as
  // `prefix s` — because the panes are laid out in front of the user and "which pane" is
  // the same question however it is asked. See tiling/choose.ts for the purpose the mode
  // now carries.
  //
  // The transfer itself is the drag-and-drop one, unchanged: the destination pane's
  // `receive` runs the preflight, the overwrite confirmation, the ghost rows, the batch and
  // the arrival flash. A command that reimplemented any of that would be a second set of
  // answers to questions the drop path has already been taught (FilePane.receiveInto).

  // refuseDestination says why a pane cannot receive a transfer out of `from`, or nothing
  // when it can. Everything here is read from the TREE, not from the registered actions: a
  // pane with `open` set is an editor or an image viewer, `path` is the folder a pane is
  // showing, and both are true of a background tab nobody has looked at. Asking paneActions
  // instead would make the answer depend on mount order.
  const refuseDestination = (id: string, from: string): string | undefined => {
    const pane = findPane(state.tree, id);
    if (!pane) return "gone";
    // A TERMINAL IS A DESTINATION, since the BFF reports where each session is standing:
    // "into the folder this shell is in" is a virtual path like any other. It is the one
    // destination whose path is not in the tree — the tree holds the `dir` the session
    // STARTED in, which stops being true at the first `cd` — so it is resolved from the
    // live session list, and each way it can have no answer says which one it is.
    if (pane.data.kind === "term") {
      if (!pane.data.workload) return "no workload yet";
      if (!pane.data.sessionId) return "no session yet";
      const dest = termDestination(pane.data);
      // The honest reason, and the one a user will hit most: the shell is running fine, it
      // simply never reports where it is. Saying "no directory" would read as a fault.
      if (!dest) return "this shell does not report its directory";
      if (dest === from) return "already here";
      return undefined;
    }
    if (pane.data.open) return "not a folder";
    if (!pane.data.path) return "the mount list";
    // Not "this is the pane you are in": ANOTHER pane showing the same folder is the same
    // no-op, and the transfer would filter every item away and look like a command that
    // silently did nothing.
    if (pane.data.path === from) return "already here";
    return undefined;
  };

  // termDestination is the virtual folder a terminal pane receives into: the workload it is
  // attached to plus the directory its session reports RIGHT NOW. Undefined when the shell
  // has never said where it is — which is normal (OSC 7 is not in a stock image), and the
  // reason a terminal is offered as a destination only sometimes.
  const termDestination = (data: TermData): string | undefined => {
    const cwd = cwdOf(data.sessionId);
    return cwd ? virtualPathOf(data.workload, cwd) || undefined : undefined;
  };

  // The focused pane as a transfer SOURCE, or undefined when it is not one. Reads
  // actionsRev first, like selectionOf and dirtyOf, because paneActions is a plain Map —
  // that signal is what ties this to (de)registration, and the commands below are rebuilt
  // from it every time the palette asks.
  const browseHere = (): { actions: BrowseActions; from: string } | undefined => {
    actionsRev();
    const a = paneActions.get(state.focused);
    if (a?.kind !== "browse") return undefined;
    const pane = findPane(state.tree, state.focused);
    return { actions: a, from: (pane && asFiles(pane)?.data.path) ?? "" };
  };

  // The virtual path of the ONE selected directory, or undefined. This is what "the folder
  // you have picked out" means for the commands that act ON a row — `New pane…` here, and
  // `Open …` through its own resolver: one row, and that row a directory. `Open in a
  // terminal…` deliberately does NOT consult it; see terminalHere.
  // Read from selectionItems because a name alone cannot say which rows are folders, and
  // the whole point is to tell them apart; a file selected is not a smaller version of
  // this, it is a different question.
  const selectedDir = (): string | undefined => {
    const items = browseHere()?.actions.selectionItems() ?? [];
    return items.length === 1 && items[0].dir ? items[0].path : undefined;
  };

  // openTarget is the pane `Open …` would create, or the reason there is none — the same
  // shape as terminalHere and for the same reason: the command's title, its disabled state
  // and what it actually opens must all be one answer, or a row names a thing and then opens
  // something else.
  //
  // ONE COMMAND FOR BOTH KINDS OF ROW. It was two — "Open in a new tab" for a folder and
  // "Open" for a file — and they were the same sentence with different objects. What kept
  // them apart was that the folder one STACKED a tab and the file one asked where to put the
  // pane; once the file open started asking for the mouse as well as the keyboard, the two
  // differed in nothing but their payload. The remaining difference is `open`: a folder pane
  // is a listing of `path`, a file pane is `path` plus the file it is showing.
  //
  // The name carries a trailing slash for a directory. The title is now the only thing that
  // says which kind of row the command is about, and `Open "logs"…` beside `Open "logs.txt"…`
  // is a distinction the reader should not have to make from the extension.
  //
  // WHICH FILES OPEN IS FILEPANE'S QUESTION, asked through the same isTextName / isImageName
  // pair the row's own Enter and double-click consult. Re-deciding it here would be a second
  // answer to "does this open?", and the two would drift into a command that is live for a
  // file the pane then downloads instead.
  //
  // The target is the ONE selected row: a bare arrow or a plain click leaves the row cursor
  // selected on its own, so the selection is what the user is pointing at. A range is
  // deliberately not a target — this makes one pane showing one thing, and picking silently
  // for you which of the five it is would be a guess.
  const openTarget = (): { name: string; data: FileData } | { why: string } => {
    const here = browseHere();
    const items = here?.actions.selectionItems() ?? [];
    if (items.length !== 1) return { why: items.length ? "select just one row" : "select a file or folder" };
    const [it] = items;
    const name = baseNameOf(it.path);
    if (it.dir) return { name: `${name}/`, data: { kind: "files", path: it.path } };
    if (!isTextName(name) && !isImageName(name)) return { why: "no editor or preview for this file" };
    return { name, data: { kind: "files", path: here?.from ?? "", open: name } };
  };

  // Why a transfer cannot be started right now, or nothing when it can. Both commands share
  // it, so the two never disagree about whether F5 is live.
  //
  // A SELECTION IS THE ONLY REQUIREMENT — deliberately not "and somewhere to send it". The
  // chooser's escape hatch is a typed path, which is always available, so a lone pane with
  // one row selected is a perfectly good copy: every pane row will be refused and the only
  // live answer will be "an arbitrary location". Gating on destination panes would take the
  // command away in exactly the layout the typed route exists for.
  const refuseTransfer = (): string | undefined =>
    browseHere()?.actions.selection().length ? undefined : "nothing selected";

  const transferSelected = (move: boolean) => {
    const here = browseHere();
    if (!here) return;
    // Captured NOW, at the press, and not read again when the chooser commits. The panel
    // names a count in its own title, the walk that follows is part of answering it, and a
    // selection that changed under the mode would make that title a lie. `src` is captured
    // for the same reason and matters more: the escape hatch below resolves after a round
    // trip, by which time the focus may be anywhere.
    const src = state.focused;
    const items = here.actions.selectionItems();
    if (!items.length) return;
    const what = items.length === 1 ? `"${baseNameOf(items[0].path)}"` : `${items.length} items`;
    choose.begin({
      title: `${move ? "Move" : "Copy"} ${what} to…`,
      refuse: (id) => refuseDestination(id, here.from),
      pick: (id) => {
        // THE FOCUS DOES NOT MOVE. Every other commit of this mode goes to the pane it
        // chose, because going there is the whole point; this one sends FILES, and taking
        // the user along would abandon the selection they were working through. The toast
        // is what reports the outcome, from wherever they are standing.
        const pane = findPane(state.tree, id);
        const term = pane && asTerm(pane);
        if (term) {
          // Read the cwd AGAIN here rather than reusing the one the chooser row was built
          // with: the walk to a destination takes as long as it takes, and the shell may
          // have moved during it. Sending to where it was would be a stale answer the very
          // first time, before the poll corrected it. Nothing is sent if it has moved
          // somewhere it cannot report — the row's own refusal, applied at the last moment.
          const dest = termDestination(term.data);
          if (dest) void transferInto(dest, items, move, { refreshAll: refreshAllPanes });
          return;
        }
        const dst = paneActions.get(id);
        if (dst?.kind !== "browse") return;
        void dst.receive(items, move);
      },
      // The one destination the list cannot hold: a folder nothing is showing. Reached by
      // typing, so asking for it and saying where are one action — see choose.ts.
      elsewhere: {
        label: "an arbitrary location",
        take: (seed) => {
          void here.actions.transferElsewhere(move, seed).then((dest) => {
            // AND THEN GO THERE — a new tab in the source's own tile, which is the whole
            // difference between this route and the pane one. A pane destination is
            // already on screen; a typed one is not, so without this the files land
            // somewhere with no view of them and the only way back is to re-navigate by
            // hand.
            if (dest) openTabAt(src, dest);
          });
        },
      },
    });
  };

  // openDirInNewPane is the folder half of the same open, which is what a Shift-click on a
  // folder NAME runs (FilePane routes it here) and what the Open command does when the
  // selected row is a directory.
  const openDirInNewPane = (srcId: string, path: string) => placeHere(srcId, { kind: "files", path });

  // openTabAt adds a browse tab for `path` beside `srcId` and makes it current. addTab
  // already raises the new tab within its stack; the focus follows so the keyboard is where
  // the eye is.
  //
  // It is NOT how anything is opened any more — the Open command asks where its pane goes,
  // like every other creating command. This is the one caller that must not ask: the typed
  // transfer destination resolves after a round trip, and the tab is a consequence of a
  // completed copy rather than a thing the user is choosing a place for. Interrupting them
  // to point at a tile, for a pane they did not ask to create, would be a question about
  // somebody else's action.
  const openTabAt = (srcId: string, path: string) => {
    const { tree, newPaneId } = addTab(snapshot(), srcId, { kind: "files", path } as PaneData);
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };

  // followSource is the terminal "Follow this terminal here…" would follow, or the reason
  // there is none. One function for the same reason terminalHere is one: the row's title,
  // its disabled state and what it actually binds all have to agree.
  const followSource = ():
    | { sessionId: string; workload: string; cwd: string }
    | { why: string } => {
    const pane = findPane(state.tree, state.focused);
    const term = pane && asTerm(pane);
    if (!term) return { why: "not a terminal" };
    if (!term.data.workload) return { why: "this pane has no workload yet" };
    if (!term.data.sessionId) return { why: "no session yet" };
    const cwd = cwdOf(term.data.sessionId);
    // The honest reason, and the one a user will hit most: the shell is running fine, it
    // simply never reports where it is. Saying "no directory" would read as a fault.
    if (!cwd) return { why: "this shell does not report its directory" };
    return { sessionId: term.data.sessionId, workload: term.data.workload, cwd };
  };

  // ---- opening a terminal where you are -------------------------------------------------

  // terminalHere is the terminal "Open in a terminal" would make, or the reason there is
  // none. One function, because the command's title, its disabled state and what it
  // actually creates all have to agree: a row that says `Open "app" in a terminal…` and
  // then lands on a different workload is worse than no row.
  //
  // WHERE it aims is the folder the pane is SHOWING, and deliberately not the selected one.
  // `files:open` and `New pane…` read the selection because they act ON a row — the row is
  // their object. A terminal has no object: it is a place to stand, and the place you are
  // standing is the listing in front of you. Following the highlight instead made the
  // command's title flicker between folder names as the arrow keys moved down the listing,
  // and offered to open a shell somewhere the user had pointed at but not gone.
  //
  // An editor pane counts as its folder — the file is in a directory, and that directory is
  // a fine place to stand.
  //
  // A TERMINAL pane is also a source, inheriting its own workload and directory, so a
  // shell can spawn a sibling without going back through the browser.
  //
  // Whether a mount is a workload is asked of the WORKLOAD LIST, not of the local-root
  // table the BFF's resolveVirtual consults. The two rules differ on purpose: the BFF
  // treats an unknown mount as a workload so it surfaces as a normal container not-found,
  // whereas this has to know BEFORE it acts, and only the workload list can tell "that is a
  // local folder" apart from "that container is stopped".
  const terminalHere = (): { data: TermData; label: string } | { why: string } => {
    const pane = findPane(state.tree, state.focused);
    if (!pane) return { why: "no pane" };
    const term = asTerm(pane);
    if (term) {
      if (!term.data.workload) return { why: "this pane has no workload yet" };
      return { data: termData(term.data.workload, term.data.dir), label: term.data.workload };
    }
    const path = asFiles(pane)?.data.path ?? "";
    const { mount, dir } = mountPathOf(path);
    if (!mount) return { why: "pick a workload first" };
    // Undefined means the list has not answered yet — which is not the same as "there is no
    // such workload", and saying the latter would be a row that lies for the first second
    // of every page load.
    const w = workloads()?.find((x) => x.name === mount);
    if (!workloads()) return { why: "still loading workloads" };
    if (!w) return { why: "a local folder, not a container" };
    if (!w.running) return { why: `${mount} is not running` };
    return { data: termData(mount, dir), label: baseNameOf(path) || mount };
  };

  // The pane commands are the workspace's own, not a pane's: they apply to any focused
  // tile whatever it holds, so they are unconditional and keep tmux's letters — % " C c x
  // for the splits and the new/close pair, s o ; C-o for moving around. They were
  // registered twice, once per screen, under `files:` and `term:` ids that had to be kept
  // identical by a test; one screen means one list, and the ids say `ws:` because that is
  // whose commands they are.
  //
  // The ones tagged `pane` are what a tile's ⋮ shows. That menu must be the same on every
  // tile, which is why the tag goes only on commands that are registered unconditionally —
  // an entry that came and went with the kind of pane under the cursor would make the ⋮ a
  // different menu depending on where it was pressed.
  //
  // Same LIST, note, not the same words. A tagged command may still NAME what it is about to
  // act on, because ⋮ focuses its own tile before it opens the palette (see
  // openPaneCommands): a row reading `Open "app" in a terminal…` in a menu raised from that
  // tile is telling the user what this press will do, which is the opposite of the entry
  // whose meaning drifts with the cursor.
  const paneCommands = (): Command[] => [
    // The general form, asking with the wireframe targets instead of assuming the focused
    // tile and an axis. Its bind is SHIFT-c, next to the `c` that opens the other
    // wireframe: the two are the same gesture asking two questions ("somewhere to start" /
    // "two of this"). The two DIRECTIONAL splits below are deliberately untagged — they are
    // accelerators for a question this one asks better, and a menu offering both the
    // general form and two of its four answers reads as three unrelated things.
    { id: "ws:split", group: "Workspace", title: "Split pane…", bind: "C", tags: [PANE_TAG], run: () => drag.beginSplit() },
    { id: "ws:split-h", group: "Workspace", title: "Split pane left / right", bind: "%", run: () => splitFocused("h") },
    { id: "ws:split-v", group: "Workspace", title: "Split pane top / bottom", bind: '"', run: () => splitFocused("v") },
    // "New" means an empty pane — a file browser at the virtual root — EXCEPT when a single
    // directory row is picked out, where the emptiest useful pane is one already showing
    // it. The row is the answer to "new pane on what?", asked and answered before the
    // command ran; making the user place a root pane and then walk back down to the folder
    // they had already selected is a step whose only content is forgetting.
    {
      id: "ws:new",
      group: "Workspace",
      title: "New pane…",
      bind: "c",
      tags: [PANE_TAG],
      run: () => {
        const dir = selectedDir();
        drag.beginPlace(dir ? () => ({ kind: "files", path: dir }) : undefined);
      },
    },
    // The one command that makes a pane of the OTHER kind, and the only way to open a
    // terminal from inside the workspace. It places with the same wireframe every other
    // pane-creating command uses, so "which terminal" and "where" are one gesture.
    //
    // Listed always and disabled with the reason, never absent: the reasons are things the
    // user can act on (browse into a workload, start it), and a row that vanished would
    // read as "this screen cannot open terminals".
    //
    // Tagged `pane`, which it was not at first: it was held out because what it opens is
    // read from the focused pane. That is true, and on the ⋮ it is the point — the menu
    // focuses its tile before opening, so the row names THAT tile's folder. Untagged, the
    // only ways to open a terminal from the workspace were the prefix `t` and typing into
    // the palette, both of which want a keyboard, on the one screen where a touch user has
    // otherwise been given every pane operation under a finger.
    (() => {
      const t = terminalHere();
      return {
        id: "ws:open-terminal",
        group: "Workspace",
        title: "data" in t ? `Open "${t.label}" in a terminal…` : "Open in a terminal…",
        keywords: "terminal shell exec console open here cd workload run",
        bind: "t",
        tags: [PANE_TAG],
        disabled: "why" in t ? t.why : undefined,
        run: () => {
          // Re-read at the press rather than closing over the row's copy: the palette
          // renders a list and the focus can move under it, and the terminal that opens
          // should be the one the title would say now.
          const now = terminalHere();
          if ("data" in now) drag.beginPlace(() => now.data);
        },
      } satisfies Command;
    })(),
    // Listed always, disabled with nowhere to go: arming the mode would light no tile at
    // all (a lone pane cannot move beside itself), leaving Esc as the only way out.
    {
      id: "ws:move",
      group: "Workspace",
      title: "Move pane…",
      tags: [PANE_TAG],
      disabled: allPanes(state.tree).length > 1 ? undefined : "nowhere to move it",
      run: () => drag.beginPick(state.focused, "pane"),
    },
    // The other direction of "Open in a terminal": that takes the browser's folder to a
    // shell, this takes a shell's folder to the browser, and keeps taking it. Asked of the
    // TERMINAL because the terminal is what knows where it is — a file pane cannot offer to
    // follow something it has no way to name.
    //
    // Its disabled reason is the load-bearing part. OSC 7 is absent from a stock container
    // image, so "this shell does not report its directory" is the COMMON case, not an edge
    // one, and a row that simply did nothing would read as a broken feature rather than as
    // a shell that never said where it was.
    (() => {
      const src = followSource();
      return {
        id: "ws:follow-terminal",
        group: "Workspace",
        title: "Follow this terminal here…",
        keywords: "follow track cwd directory sync browse terminal shell",
        // UNTAGGED, unlike ws:open-terminal, which it otherwise mirrors. That row earns its
        // place on the ⋮ because it is listed-always-and-disabled and its reasons are things
        // the user can act on: browse into a workload, start it. This one's dominant reason
        // is "this shell does not report its directory", which nobody can act on — OSC 7 is
        // absent from stock container images — so on the touch surface it would be a
        // permanently dead row. Worth revisiting if that emission rate ever changes.
        disabled: "why" in src ? src.why : undefined,
        run: () => {
          // Re-read at the press, like ws:open-terminal: the palette renders a list and the
          // focus can move under it.
          const now = followSource();
          if ("why" in now) return;
          const sessionId = now.sessionId;
          const workload = now.workload;
          choose.begin({
            title: "Follow this terminal in…",
            refuse: (id) => {
              const pane = findPane(state.tree, id);
              if (!pane) return "gone";
              // Refused by kind, so the row says what it is. Any FILE pane will do,
              // including an editor and the mount list: following navigates, and
              // navigating is what turns either of those into a listing anyway.
              if (pane.data.kind === "term") return "a terminal";
              return undefined;
            },
            pick: (id) => {
              // Read the cwd again here rather than reusing the one the row was built
              // with: the walk to a destination takes as long as it takes, and the shell
              // may have moved during it. Landing where it was would be a stale answer the
              // very first time, before the poll corrected it.
              const dest = virtualPathOf(workload, cwdOf(sessionId) ?? now.cwd);
              if (!dest) return;
              forgetDraft(id);
              commit(
                updatePane(snapshot(), id, { path: dest, open: undefined, follow: sessionId }),
              );
            },
          });
        },
      } satisfies Command;
    })(),
    // The way out. Hand navigation also ends a follow (see navigateTo), but that requires
    // somewhere else to go: a user who followed the wrong terminal and wants THIS folder to
    // stay put has no gesture without this row.
    (() => {
      // Guarded rather than the `?? ({} as Pane)` idiom used elsewhere in this file: that
      // one only survives because its callers cannot miss, and this one can. The command
      // list is rebuilt whenever the palette opens, including moments when `focused` still
      // names a pane that has just closed — asFiles would then read `.kind` off undefined
      // and take the whole palette down with it.
      const focused = findPane(state.tree, state.focused);
      const following = focused ? asFiles(focused)?.data.follow : undefined;
      return {
        id: "ws:unfollow-terminal",
        group: "Workspace",
        title: "Stop following the terminal",
        keywords: "unfollow stop track cwd detach pin",
        // Untagged for the same reason as its twin, and one of its own: it is a toggle with
        // no pointer gesture in it, so its worth is entirely as a palette row.
        disabled: following ? undefined : "this pane is not following a terminal",
        run: () => {
          const pane = findPane(state.tree, state.focused);
          if (pane && asFiles(pane)?.data.follow) {
            commit(updatePane(snapshot(), pane.id, { follow: undefined }));
          }
        },
      } satisfies Command;
    })(),
    // tmux's chooser (`prefix s`, choose-session), pointed at this workspace's panes.
    // Disabled at one PANE, not one tile: a lone tile holding three tabs has two panes the
    // chooser can reach that nothing else on the keyboard can.
    {
      id: "ws:choose",
      group: "Workspace",
      title: "Choose pane…",
      keywords: "choose-tree choose-session display-panes switch select go to",
      bind: "s",
      tags: [PANE_TAG],
      disabled: allPanes(state.tree).length > 1 ? undefined : "only one pane open",
      run: () => choose.begin(),
    },
    // tmux's `prefix o` — the chooser's accelerator, left UNTAGGED on the same rule the two
    // directional splits are: the ⋮ is a pointer surface, where the way to reach a pane is
    // to click it, and this command's worth is entirely as a key.
    {
      id: "ws:next",
      group: "Workspace",
      title: "Select next pane",
      keywords: "next select-pane cycle switch focus",
      bind: "o",
      disabled: allPanes(state.tree).length > 1 ? undefined : "only one pane open",
      run: () => activate(nextPaneId(state.tree, state.focused)),
    },
    // tmux's `prefix ;` (last-pane). Its gate is not a pane COUNT: with three panes open it
    // is still dead until the focus has actually moved, and it goes dead again if the pane
    // it remembers closes, which a count would never notice (see lastpane.ts).
    {
      id: "ws:last",
      group: "Workspace",
      title: "Select last active pane",
      keywords: "last-pane previous back toggle alternate switch",
      bind: ";",
      disabled: lastPane() ? undefined : "no pane to go back to",
      run: () => {
        const id = lastPane();
        if (id) activate(id);
      },
    },
    // tmux's `prefix C-o`. Gated on TILES, not panes: two tabs sharing one tile is one
    // slot, and rotating one slot is a no-op.
    {
      id: "ws:rotate",
      group: "Workspace",
      title: "Rotate panes",
      keywords: "swap rotate-window",
      bind: "Ctrl+O",
      tags: [PANE_TAG],
      disabled: allStacks(state.tree).length > 1 ? undefined : "nothing to rotate",
      run: () => commit(rotateStacks(snapshot())),
    },
    // tmux's `select-layout even-horizontal`: the way out of a layout that has grown lopsided
    // without closing anything — every tile into one row, all the same width.
    //
    // Bound to SHIFT-h rather than to tmux's own Alt+1. The number is a position in a list of
    // five layouts, four of which do not exist here, so it names nothing a user of THIS
    // workspace could reason about; `H` is the letter the command is called after. It also sits
    // with the other Shift-letter on this screen — `C` for "Split pane…" — where the plain
    // letters are taken by the commands you press all day. Lowercase `h` is untouched: it is
    // vi-left inside the pick and chooser modes, and those read it while they are up rather
    // than through the prefix.
    //
    // Under the extending workspace the row is built at the minimum pane width when an even
    // share of the screen would be narrower than that, so "even" does not quietly mean "each of
    // eight panes gets an eighth of the screen and none of them is readable". The workspace gets
    // wider instead, which is the same bargain every other operation here strikes.
    {
      id: "ws:even-h",
      group: "Workspace",
      title: "Even horizontal",
      keywords: "layout even-horizontal columns row arrange spread select-layout",
      bind: "H",
      tags: [PANE_TAG],
      disabled: allStacks(state.tree).length > 1 ? undefined : "there is only one pane",
      run: () => {
        const { tree, ext } = evenHorizontal(
          snapshot(),
          state.ext,
          extending() ? floorFor("h") : 0,
        );
        commit(tree, ext);
      },
    },
    // Resizing, on tmux's own binds (prefix Alt+arrow = resize-pane), and grouped HERE with Move
    // and Rotate rather than up beside the splits: these change an existing layout, where the
    // creating commands make one, and the ⋮ reads top to bottom as make / go to / rearrange.
    //
    // They are the keyboard and touch route to what Alt-dragging a divider does with a mouse,
    // and they exist for the same reason every other gesture here has one: a modifier on a 6px
    // handle is invisible, and a coarse pointer has no Alt at all.
    ...resizeCommands(),
    // It closes one TAB. The model calls that a pane, but a tile with two tabs keeps
    // standing when one of them goes, and "Close tab" is already what the ✕ beside every
    // tab is labelled.
    { id: "ws:close", group: "Workspace", title: "Close focused tab", bind: "x", tags: [PANE_TAG], run: () => requestClosePane(state.focused) },
    // The zoom trio, present only while the setting is on. This is the one place in the list
    // where a command comes and goes, and it does NOT break the rule three comments above
    // that says the ⋮ must be the same menu on every tile: that rule is about the list
    // changing with the pane under the cursor, and these change with a global preference —
    // every tile on the screen has them or none does. A user who has not opted in gets a
    // palette that is unchanged, which is what "opt-in" has to mean for a feature whose other
    // half takes the browser's pinch away.
    ...zoomCommands(),
  ];

  // Zooming acts on the pane whose CONTENTS can be zoomed, so a browsing file pane — a
  // listing, chrome all the way down — is refused with the reason rather than omitted, on the
  // same rule as ws:open-terminal: the user can act on it (open a file, open a terminal), and
  // a row that vanished on some tiles would make the ⋮ the drifting menu the tag exists to
  // prevent.
  const zoomableHere = (): { id: string } | { why: string } => {
    const pane = findPane(state.tree, state.focused);
    if (!pane) return { why: "no pane" };
    if (asFiles(pane) && !asFiles(pane)?.data.open) return { why: "a file listing has no contents to zoom" };
    return { id: pane.id };
  };

  const zoomCommands = (): Command[] => {
    if (!settings().paneZoom) return [];
    const here = zoomableHere();
    // One builder for all three: their titles, their disabled reasons and what they do all
    // have to agree about which pane is being zoomed, and three copies of that agreement is
    // three chances to get one of them wrong.
    const cmd = (id: string, title: string, bind: string | string[], delta: number): Command => ({
      id,
      group: "Workspace",
      title,
      keywords: "zoom font size text bigger smaller larger scale pinch magnify",
      bind,
      tags: [PANE_TAG],
      disabled:
        "why" in here
          ? here.why
          : delta === 0
            ? zoomStep(here.id) === ZOOM_HOME
              ? "not zoomed"
              : undefined
            : canZoom(here.id, delta)
              ? undefined
              : delta > 0
                ? "already as large as it goes"
                : "already as small as it goes",
      run: () => {
        // Re-read at the press, like ws:open-terminal: the palette renders a list and the
        // focus can move under it while it is open.
        const now = zoomableHere();
        if (!("id" in now)) return;
        if (delta === 0) resetZoom(now.id);
        else nudgeZoom(now.id, delta);
      },
    });
    return [
      // Two spellings, both shown: `+` is the key people mean and `=` is the one they can
      // reach without Shift, exactly as every browser's own zoom binds both.
      cmd("ws:zoom-in", "Zoom in", ["+", "="], 1),
      cmd("ws:zoom-out", "Zoom out", "-", -1),
      cmd("ws:zoom-reset", "Reset zoom", "0", 0),
    ];
  };

  // Contextual FILE commands for the focused pane: only the actions that apply right now
  // (inside a mount, a row selected, an edit pending) are offered. Evaluated lazily by the
  // palette, so the list tracks the current selection. A terminal pane has none of these —
  // it contributes nothing here and its own keys go to the shell.
  const workspaceCommands = (): Command[] => {
    const a = paneActions.get(state.focused);
    if (!a) return paneCommands();
    const cmds: Command[] = [];
    if (a.kind === "edit") {
      // NO BIND, and it wants none. Save had `s` until the pane chooser claimed that
      // letter; the first replacement was a `Ctrl+S` chord, which was one key too many
      // twice over: the editor's own keymap already runs this on plain Mod-s
      // (components/Editor.tsx), so the prefixed form asked for three keystrokes to reach
      // what one does, in the only pane where the command exists at all. A prefix bind is
      // for something the focused pane cannot hear; a save is the opposite of that. The
      // palette entry stays, because that is where you look when you have forgotten.
      if (a.dirty()) cmds.push({ id: "files:save", group: "Workspace", title: "Save file", run: a.save });
      return [...cmds, ...paneCommands()];
    }
    if (!a.atRoot()) {
      cmds.push({ id: "files:new-folder", group: "Workspace", title: "New folder", bind: "n", run: a.newFolder });
      cmds.push({ id: "files:upload", group: "Workspace", title: "Upload file(s)", bind: "u", run: a.upload });
      // The batch actions name their target the way the selection reads: one row by name,
      // several by count — so a palette entry states what the key is about to touch before
      // you press it. Computed before the `sel.length` gate because the two transfers are
      // offered whether or not anything is selected; "selected" is what they say when there
      // is nothing to name.
      const sel = a.selection();
      const what = sel.length === 0 ? "selected" : sel.length === 1 ? `"${sel[0]}"` : `${sel.length} items`;
      // OFFERED WHENEVER THIS PANE IS INSIDE A MOUNT, disabled the rest of the time rather
      // than absent — which for these three is not the usual "the absence would read as
      // 'this screen cannot do that'" argument. It is that their keys are UNPREFIXED (see
      // `direct` in command-center): a command that came and went with the selection would
      // be a key that copies files when something is selected and RELOADS THE PAGE when
      // nothing is, throwing away every unsaved editor draft in the workspace. Registered
      // here, F5 means one thing everywhere inside a mount, and the disabled row says which.
      //
      // At the virtual root they are absent along with New folder and Upload, and F5 is the
      // browser's again: the entries there are mounts, not files, so there is nothing this
      // could be about.
      // OPEN, the listing's primary action, as a command. It was reachable only by gesture —
      // Enter on a row, a double-click, a click on the name — which made it the one thing
      // this screen does most often that the palette could not name, could not search, and
      // could not run.
      //
      // NO KEY, AND IT WANTS NONE — the same shape as files:save, and the same reason: a
      // bind is for something the focused thing cannot hear, and this listing hears Enter
      // already. Nor could a `direct` spell it, even to advertise the key it has: the direct
      // lookup runs on a DOCUMENT keydown against anything that is not a text field, so
      // claiming plain Enter would take it from every button, link and the updir row at
      // once — which is exactly why the neighbouring "open in a new tab" is on Ctrl+Enter
      // and why a test pins plain Enter as unclaimed. A function key was the other way out
      // and it was worse: this screen's F5 / F6 are already spoken for, and inventing a
      // second keyboard route to a gesture that has one teaches two habits for one action.
      //
      // It is still registered throughout the mount and disabled with the reason, like its
      // neighbours: the palette is a menu of what this screen does, and an entry that came
      // and went with the selection would answer "why is there no Open?" with silence.
      const open = openTarget();
      cmds.push({
        id: "files:open",
        group: "Workspace",
        title: "name" in open ? `Open "${open.name}"…` : "Open…",
        keywords: "open edit view preview editor image viewer folder directory tab pane enter",
        direct: OPEN_ELSEWHERE_KEY,
        disabled: "why" in open ? open.why : undefined,
        // Re-read at the press, not closed over: the palette builds this list when it opens
        // and the selection can move under it (the arrows work while it is up).
        run: () => {
          const o = openTarget();
          if ("data" in o) placeHere(state.focused, o.data);
        },
      });
      const why = refuseTransfer();
      // THESE TWO ARE THE SCREEN'S ONLY COPY AND MOVE. There used to be a separate
      // "Copy … to…" on `y` that prompted for a path; it is now the chooser's "an arbitrary
      // location", reached by typing, so one command covers both destinations instead of
      // two commands splitting them by how the destination is named. That freed `y`, which
      // is deliberately left unbound.
      cmds.push({
        id: "files:copy",
        group: "Workspace",
        title: `Copy ${what} to another pane…`,
        keywords: "copy f5 transfer destination other pane panel send arbitrary location path",
        bind: "Ctrl+Shift+C",
        direct: "F5",
        disabled: why,
        run: () => transferSelected(false),
      });
      cmds.push({
        id: "files:move",
        group: "Workspace",
        title: `Move ${what} to another pane…`,
        keywords: "move f6 transfer destination other pane panel send relocate arbitrary location path",
        bind: "Ctrl+Shift+M",
        direct: "F6",
        disabled: why,
        run: () => transferSelected(true),
      });
      if (sel.length) {
        // Rename asks for ONE new name, so it is offered only for a single row.
        if (sel.length === 1) {
          cmds.push({ id: "files:rename", group: "Workspace", title: `Rename ${what}`, bind: "e", run: a.rename });
        }
        // DELETE MOVED off `x`, which is the pane bind shared with everything else; vi's
        // `d` was the obvious free letter, and it sits better beside the `hjkl` the pick
        // mode already speaks. Note which way the risk falls: someone reaching for the old
        // `prefix x` now closes a tab instead of deleting files, so the habit misfires into
        // the harmless action, and the destructive one is the one that had to be relearned.
        cmds.push({ id: "files:download", group: "Workspace", title: `Download ${what}`, bind: "w", run: a.download });
        cmds.push({ id: "files:delete", group: "Workspace", title: `Delete ${what}`, bind: "d", run: a.remove });
      }
    }
    return [...cmds, ...paneCommands()];
  };
  onMount(() => onCleanup(registerCommands(workspaceCommands, true)));

  const termCtx: TermCtx = {
    focused: () => state.focused,
    setSession,
    retarget,
    adopt,
    closePane: closePaneById,
    running,
    cwdOf: (sessionId) => cwdOf(sessionId),
    refreshAll: refreshAllPanes,
  };

  const ctx: TileCtx<PaneData> = {
    focused: () => state.focused,
    setFocus,
    activate,
    closePane: requestClosePane,
    splitAt,
    setRatio: (id, ratio) => commit(setRatio(snapshot(), id, ratio)),
    extending,
    // Pixels come in because only this side knows how big a viewport is; the model works in
    // viewport extents so that resizing the browser rescales a layout instead of re-flowing it.
    resizeExtend: (splitId, growA, deltaPx, dir) => {
      const unit = dir === "h" ? bodyEl?.clientWidth : bodyEl?.clientHeight;
      if (!unit) return; // never laid out, or detached mid-drag: the ratio would be Infinity
      const { tree, ext } = resizeExtending(snapshot(), state.ext, splitId, growA, deltaPx / unit);
      commit(tree, ext);
    },
    drag,
    choose,
    // One shape for both kinds: the MOUNT, then the detail. A file pane's mount is the
    // first path segment and its detail is the open file or the current folder; a
    // terminal's are the workload and the command it runs. "All" at the virtual root, the
    // mount alone at a mount's own root, "empty pane" for a terminal with no target yet.
    // An editor holding unsaved changes gets the customary trailing " *", so a dirty file
    // announces itself from a background tab, where the pane's own badge is off screen.
    //
    // `plain` drops the badges and keeps the name — including that " *", which is part of
    // the name a person would say out loud and not a badge. See TileCtx.tabTitle.
    tabTitle: (pane: Pane<PaneData>, plain?: boolean) => {
      const term = asTerm(pane);
      if (term) {
        // The detail is the session's live window title when it has one and the
        // launch argv otherwise. cmd is fixed at creation, so it answers "which
        // shell did we start" — never "what is in this tab now", which is the
        // question a tab bar is actually asked. The title tracks the latter for
        // free, because the programs that care already announce it (see
        // osctitle.go); plenty of shells announce nothing, hence the fallback.
        const label = () => {
          if (!term.data.workload) return "empty pane";
          const detail = titleOf(term.data.sessionId) ?? term.data.cmd.join(" ");
          return `${term.data.workload}  ${detail}`;
        };
        return (
          <>
            <span class="tab-name">{label()}</span>
            {/* `attention` is what makes this one pulse — see styles.css. It is on the
                markup rather than on a `.tab .badge.warn` selector because this same
                fragment is what the pane chooser lists its rows with, and "the session is
                waiting for you" is true of the pane wherever the pane is shown. */}
            <Show when={!plain}>
              <Show when={stateOf(term.data.sessionId) === "blocked"}>
                <span class="badge warn attention" title="Detected session activity">needs you</span>
              </Show>
              <Show when={stateOf(term.data.sessionId) === "working"}>
                <span class="badge" title="Detected session activity">working</span>
              </Show>
            </Show>
          </>
        );
      }
      // Wrapped in the same `.tab-name` the terminal branch uses. It reads as decoration
      // on a tab that has nothing beside it, but the class is what says "this is the tab's
      // NAME, as opposed to the badges next to it" — and a tab bar where half the labels
      // answer to it and half do not is one nothing can address uniformly.
      const file = pane as Pane<FileData>;
      const label = () => {
        const mark = dirtyOf(pane.id) ? " *" : "";
        const p = file.data.path;
        if (!p) return `All${mark}`;
        const slash = p.indexOf("/");
        const mount = slash === -1 ? p : p.slice(0, slash);
        const leafName = file.data.open ?? (slash === -1 ? p : p.slice(p.lastIndexOf("/") + 1));
        return leafName === mount ? `${mount}${mark}` : `${mount}  ${leafName}${mark}`;
      };
      // A following pane moves on its own, which is alarming without a statement of why.
      // The badge is that statement, and it is the only on-screen trace the pairing has —
      // the pane otherwise looks exactly like one someone navigated by hand.
      return (
        <>
          <span class="tab-name">{label()}</span>
          <Show when={!plain && file.data.follow}>
            <span class="badge" title="This pane follows a terminal's working directory">
              following
            </span>
          </Show>
        </>
      );
    },
    // Browse panes get a sub-header with the refresh button + breadcrumb. Editor panes and
    // terminals have none — an editor's file name is already in the tab and its save bar,
    // and a terminal has no location the chrome could show that the shell's own prompt
    // does not show better.
    subHeader: (pane) => {
      const file = asFiles(pane);
      if (!file || file.data.open) return null;
      return (
        <>
          <button
            class="pane-refresh"
            title="Refresh"
            aria-label="Refresh"
            draggable={false}
            onClick={(e) => {
              e.stopPropagation();
              paneActions.get(pane.id)?.refresh();
            }}
          >
            ⟳
          </button>
          <PaneCrumbs pane={file} onGo={(path) => paneActions.get(pane.id)?.go(path)} />
          {/* A multi-row selection has no other on-screen statement of its size — the
              file actions live in the command palette, so this label is what tells you
              how many rows the next Copy / Download / Delete will touch. It is embedded
              in the breadcrumb lane as an overlay, not a flex item, so nothing reflows as
              the selection grows and shrinks. */}
          <Show when={selectionOf(pane.id).length > 1}>
            <span class="fs-selection-count">{selectionOf(pane.id).length} selected</span>
          </Show>
        </>
      );
    },
    // A pane's KIND never changes once created, so this branch is settled the first time it
    // runs; what does change is a file pane's `open`, which swaps browser for editor and is
    // meant to remount.
    body: (pane) => {
      const term = asTerm(pane);
      if (term) return <TermPane pane={term} ctx={termCtx} />;
      const file = pane as Pane<FileData>;
      return file.data.open && isImageName(file.data.open) ? (
        <ImageViewerPane pane={file} focused={() => state.focused === pane.id} />
      ) : file.data.open ? (
        <FileEditorPane
          pane={file}
          navigate={(path) => navigateTo(pane.id, path)}
          register={(actions) => registerActions(pane.id, actions)}
          focused={() => state.focused === pane.id}
        />
      ) : (
        <FilePane
          pane={file}
          navigate={(path) => navigateTo(pane.id, path)}
          openFile={(name) => openInNewPane(pane.id, name)}
          openDirInNewPane={(path) => openDirInNewPane(pane.id, path)}
          register={(actions) => registerActions(pane.id, actions)}
          refreshAll={refreshAllPanes}
          focus={() => setFocus(pane.id)}
          focused={() => state.focused === pane.id}
        />
      );
    },
  };

  // Follow the focus. Once the workspace can exceed the screen, focusing a pane is a request to
  // look at it — and a split, `prefix o`, and the chooser can all put the focus somewhere
  // entirely off screen. Reads the tree as well as `focused` so that a re-tile which MOVES the
  // focused pane re-reveals it at its new place; the scroll anchor has already run inside the
  // commit by then, so this is the only deliberate movement of the view.
  // THE PANE CHOOSER WALKS THE VIEW WITH IT. `prefix s` previews a pane without moving the
  // focus — that is the whole design, nothing commits until Enter — so on a workspace bigger
  // than the screen the walk can highlight a tile that is not on it. A highlight the user
  // cannot see is worse here than anywhere else, because the walk is the interaction: the
  // arrows are being pressed at the speed of looking, and looking is the thing that stops
  // working.
  //
  // Preview first, focus second, in ONE effect rather than two: they both aim the same scroll
  // offset, and two effects would race on whichever ran last. Leaving the mode clears the
  // preview and this re-runs on `focused`, which pans back to where the user started —
  // matching what Escape already does with the keyboard, and making the whole walk free in the
  // way the mode promises.
  createEffect(() => {
    const st = stackOf(state.tree, choose.selected() ?? state.focused);
    if (st) revealTile(bodyEl, st.id);
  });

  // Persist the whole layout whenever anything changes (stringify deep-tracks).
  createEffect(() => {
    saveLayout(STORAGE_KEY, {
      tree: JSON.parse(JSON.stringify(state.tree)),
      focused: state.focused,
      ext: { ...state.ext },
    });
    // Zoom levels are kept outside the tree, keyed by pane id, so something has to tell them
    // which panes still exist. HERE and not in a close handler: panes also leave through
    // movePane, moveStack, and the auto-close a shell's exit performs, and hooking any one of
    // those is a leak waiting for the next route someone adds. This effect already tracks
    // every change to the tree, whatever caused it, so it cannot miss one.
    keepZooms(new Set(allPanes(state.tree).map((p) => p.id)));
  });

  // `/workspace?workload=X` is the Exec CTA on a workload's detail page: arriving with a
  // target opens a terminal on it. An EMPTY TERMINAL pane takes the target (that is what
  // an empty pane is for, and its picker would otherwise ask for something the URL already
  // said); anything else — including the file browser this workspace opens as — gets a new
  // terminal tab beside it, because a pane already holding something is not a slot to be
  // reused.
  //
  // The param is consumed on arrival, so a reload of the resulting URL does not open a
  // second session on top of the one it already made.
  const [searchParams, setSearchParams] = useSearchParams();
  onMount(() => {
    const target = searchParams.workload;
    const workload = Array.isArray(target) ? target[0] : target;
    if (!workload) return;
    setSearchParams({ workload: undefined }, { replace: true });
    const focused = findPane(snapshot(), state.focused);
    const term = focused && asTerm(focused);
    if (term && !term.data.workload && !term.data.sessionId) {
      retarget(term.id, workload, []);
      return;
    }
    const { tree, newPaneId } = addTab(snapshot(), state.focused, termData(workload));
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  });

  // On load, drop session ids the BFF no longer knows about so those panes re-create
  // instead of attaching to a dead id. File panes have no session and are skipped.
  onMount(async () => {
    try {
      const live = await getTerminals();
      const liveIds = new Set(live.map((t) => t.id));
      for (const p of allPanes(snapshot())) {
        if (p.data.kind === "term" && p.data.sessionId && !liveIds.has(p.data.sessionId)) {
          commit(updatePane(snapshot(), p.id, { sessionId: undefined }));
        }
      }
    } catch {
      // BFF unreachable; each pane surfaces its own attach error.
    }
  });

  // Which side the pane chooser is pinned to, or null when it floats — the whole of what the
  // gutter is, from this screen's side. Read once here and once inside the chooser rather than
  // threaded as a prop: it is a global preference gated on a global measurement (see
  // ./tiling/gutter), not something one workspace can hold a different opinion about.
  const gutter = () => chooserGutter();

  return (
    <div
      class="workspace"
      // The gutter is a LINE ITEM, not an overlay: the workspace turns into a row and gives up
      // the strip. That is the difference the pin is for — a floating chooser sits on top of
      // tiles it is describing, and this one cannot, because they no longer reach that far.
      classList={{ "chooser-pinned": !!gutter(), "chooser-left": gutter() === "left" }}
    >
      <div
        class="workspace-body"
        ref={(el) => {
          bodyEl = el;
          onCleanup(twoFingerPan(el));
        }}
      >
        {/* The CANVAS is the whole of the scrolling workspace on screen. It is sized in
            multiples of the body's own box, so a 1×1 extent is `100% × 100%` and renders
            through exactly the rules the fixed-viewport layout always used — the tiling below
            it does not know the difference. Percentages rather than pixels because the tree's
            ratios are relative too: resizing the window rescales the whole workspace instead of
            re-flowing it, which is what keeps a tile the same fraction of the screen it was. */}
        {/* Two fingers pan the workspace; one finger still belongs entirely to the panes. See
            views/tiling/pan.ts for why the workspace had to take a gesture of its own rather
            than share the one-finger drag. Attached from the ref so its lifetime is the
            screen's, like every other behaviour hung off an element the tiling just made. */}
        <div
          class="workspace-canvas"
          ref={canvasEl}
          style={{ width: `${state.ext.w * 100}%`, height: `${state.ext.h * 100}%` }}
        >
          <div class="workspace-rows">
            <TreeNode node={state.tree} ctx={ctx} />
            {/* The workspace's own right and bottom borders, as handles. Every other divider
                sits between two tiles; these have nothing on their far side, so what they drag
                is how big the workspace is. Only under the extending layout — the dividing one
                pins the workspace to exactly one screen, so there would be nothing to drag. */}
            <Show when={extending()}>
              <EdgeDivider
                dir="h"
                edgeAt={() => canvasEl?.getBoundingClientRect().right ?? 0}
                onResize={(px) => resizeWorkspaceEdge("h", px)}
              />
            </Show>
          </div>
          <Show when={extending()}>
            <EdgeDivider
              dir="v"
              edgeAt={() => canvasEl?.getBoundingClientRect().bottom ?? 0}
              onResize={(px) => resizeWorkspaceEdge("v", px)}
            />
          </Show>
        </div>
      </div>
      <Show when={ctx.drag.picking()}>
        <PickBackdrop ctx={ctx} />
      </Show>
      {/* Up while a walk is up, and — pinned — for as long as the setting is on. The panel
          decides what it looks like in each case; all this has to get right is that pinning is
          a second reason for it to exist, rather than a different place to put the first. */}
      <Show when={ctx.choose.selected() !== null || gutter() !== null}>
        <PaneChooser
          ctx={ctx}
          tree={() => state.tree}
          scroller={() => bodyEl}
          ext={() => state.ext}
        />
      </Show>
    </div>
  );
}
