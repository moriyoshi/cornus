import { createEffect, createSignal, onCleanup, onMount } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { registerCommands, type Command } from "../command-center";
import {
  loadLayout,
  saveLayout,
  splitPane,
  closePane,
  stackPane,
  movePane,
  moveStack,
  stackStack,
  activatePane,
  addTab,
  setRatio,
  updatePane,
  findPane,
  leaf,
  newPane,
  type Dir,
  type LayoutState,
  type Node,
  type Pane,
} from "./tiling/layout";
import { TreeNode, type TileCtx, type DropZone } from "./tiling/panes";
import FilePane, { isImageName, type FileData, type PaneActions } from "./files/FilePane";
import FileEditorPane from "./files/FileEditorPane";
import ImageViewerPane from "./files/ImageViewerPane";
import PaneCrumbs from "./files/PaneCrumbs";
import { promptPanePlacement, resolveSplitSide } from "../pane-placement";

// Files is the tiled file-explorer workspace: a binary tree of splits (like the
// Terminal workspace) whose tiles are stacks of independent file-explorer panes shown
// as tabs, each browsing the unified virtual namespace. Split a tile by hovering an
// edge, drag a tab onto another tile to stack it there (or onto an edge to re-tile),
// drag dividers to resize, ✕ closes a tab. The layout (splits, ratios, tab stacks, and
// each pane's current path) persists to localStorage. The tree model and chrome are the
// view-agnostic tiling module.

const STORAGE_KEY = "cornus.files.layout";

const freshData = (): FileData => ({ path: "" });
const isValidData = (d: unknown): boolean =>
  !!d && typeof d === "object" && typeof (d as { path?: unknown }).path === "string";

function defaultLayout(): LayoutState<FileData> {
  const p = newPane(freshData());
  return { tree: leaf(p), focused: p.id };
}

export default function Files() {
  const [state, setState] = createStore<LayoutState<FileData>>(
    loadLayout(STORAGE_KEY, isValidData, defaultLayout),
  );

  // Reducers run on a detached plain snapshot; reconcile then merges the result into
  // the store in place, so unchanged panes (and their live listings/editors) keep
  // their DOM and only touched fields update.
  const snapshot = (): Node<FileData> => JSON.parse(JSON.stringify(state.tree));
  const commit = (tree: Node<FileData>) => setState("tree", reconcile(tree));

  const setFocus = (id: string) => setState("focused", id);

  // splitAt is the edge-overlay split: it divides a pane and puts a fresh pane on the
  // clicked side, starting at the same location ("open another view here").
  const splitAt = (paneId: string, dir: Dir, before: boolean) => {
    const { tree, newPaneId } = splitPane(snapshot(), paneId, dir, (from) => ({ ...from.data }), before);
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };

  // navigateTo moves a pane to a directory in BROWSE mode (clears any open file), used
  // by both dir entry and the breadcrumb (including from an editor pane back to a dir).
  const navigateTo = (paneId: string, path: string) => {
    commit(updatePane(snapshot(), paneId, { path, open: undefined }));
  };

  // openInNewPane opens `filename` (in the source pane's directory) as a new EDITOR
  // pane. A keyboard open asks whether to stack or split (you can't gesture a direction
  // from the keyboard); a mouse open just stacks it as a tab without interrupting.
  const openInNewPane = async (srcId: string, filename: string, fromKeyboard: boolean) => {
    const src = findPane(snapshot(), srcId);
    if (!src) return;
    const data: FileData = { path: src.data.path, open: filename };
    const choice = fromKeyboard ? await promptPanePlacement() : "stack";
    if (!choice) return;
    const { tree, newPaneId } =
      choice === "stack"
        ? addTab(snapshot(), srcId, data)
        : splitPane(snapshot(), srcId, "h", () => data, resolveSplitSide() === "left");
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };

  const activate = (id: string) => {
    commit(activatePane(snapshot(), id));
    setState("focused", id);
  };

  // Drag-to-rearrange: dragging a tab (or the whole tab bar = the whole stack) onto the
  // CENTER of another tile STACKS it there as tabs; onto an EDGE moves (re-tiles) it
  // beside that tile.
  const [dragSource, setDragSource] = createSignal<string | null>(null);
  const [dragKind, setDragKind] = createSignal<"pane" | "stack">("pane");
  const [dropTarget, setDropTarget] = createSignal<string | null>(null);
  const [dropZone, setDropZone] = createSignal<DropZone | null>(null);
  const dirBefore = (z: DropZone) => ({
    dir: (z === "left" || z === "right" ? "h" : "v") as Dir,
    before: z === "left" || z === "top",
  });
  const stackById = (srcId: string, destId: string) => {
    commit(stackPane(snapshot(), srcId, destId));
    setState("focused", srcId);
  };
  const moveById = (srcId: string, destId: string, z: DropZone) => {
    const { dir, before } = dirBefore(z);
    commit(movePane(snapshot(), srcId, destId, dir, before));
    setState("focused", srcId);
  };
  const stackStackById = (srcStackId: string, destId: string) => {
    const { tree, focus } = stackStack(snapshot(), srcStackId, destId);
    commit(tree);
    if (focus) setState("focused", focus);
  };
  const moveStackById = (srcStackId: string, destId: string, z: DropZone) => {
    const { dir, before } = dirBefore(z);
    const { tree, focus } = moveStack(snapshot(), srcStackId, destId, dir, before);
    commit(tree);
    if (focus) setState("focused", focus);
  };

  const closePaneById = (id: string) => {
    const { tree, focus } = closePane(snapshot(), id, freshData);
    commit(tree);
    setState("focused", focus);
  };

  // Each mounted pane publishes its actions here; the command palette and the
  // title-bar refresh dispatch to the focused pane through this registry.
  const paneActions = new Map<string, PaneActions>();
  const registerActions = (id: string, actions: PaneActions): (() => void) => {
    paneActions.set(id, actions);
    return () => paneActions.delete(id);
  };

  // Contextual "Files" commands for the focused pane: only the actions that apply
  // right now (inside a mount, a row selected, an edit pending) are offered. Evaluated
  // lazily by the palette, so the list tracks the current selection. tmux binds run
  // them directly after the prefix.
  const fileCommands = (): Command[] => {
    const a = paneActions.get(state.focused);
    if (!a) return [];
    const cmds: Command[] = [];
    if (a.kind === "edit") {
      if (a.dirty()) cmds.push({ id: "files:save", group: "Files", title: "Save file", bind: "s", run: a.save });
      return cmds;
    }
    if (!a.atRoot()) {
      cmds.push({ id: "files:new-folder", group: "Files", title: "New folder", bind: "n", run: a.newFolder });
      cmds.push({ id: "files:upload", group: "Files", title: "Upload file(s)", bind: "u", run: a.upload });
      const sel = a.selected();
      if (sel) {
        cmds.push({ id: "files:rename", group: "Files", title: `Rename "${sel}"`, bind: "e", run: a.rename });
        cmds.push({ id: "files:copy", group: "Files", title: `Copy "${sel}" to…`, bind: "c", run: a.copy });
        cmds.push({ id: "files:download", group: "Files", title: `Download "${sel}"`, bind: "w", run: a.download });
        cmds.push({ id: "files:delete", group: "Files", title: `Delete "${sel}"`, bind: "x", run: a.remove });
      }
    }
    return cmds;
  };
  onMount(() => onCleanup(registerCommands(fileCommands, true)));

  const ctx: TileCtx<FileData> = {
    focused: () => state.focused,
    setFocus,
    activate,
    closePane: closePaneById,
    splitAt,
    setRatio: (id, ratio) => commit(setRatio(snapshot(), id, ratio)),
    drag: {
      source: dragSource,
      target: dropTarget,
      zone: dropZone,
      begin: (id, kind) => {
        setDragSource(id);
        setDragKind(kind);
        setDropTarget(null);
        setDropZone(null);
      },
      over: (id, z) => {
        if (id === dragSource()) {
          setDropTarget(null);
          setDropZone(null);
          return;
        }
        setDropTarget(id);
        setDropZone(z);
      },
      drop: (id, z) => {
        const s = dragSource();
        if (s && s !== id) {
          if (dragKind() === "stack") {
            if (z === "stack") stackStackById(s, id);
            else moveStackById(s, id, z);
          } else if (z === "stack") stackById(s, id);
          else moveById(s, id, z);
        }
        setDragSource(null);
        setDropTarget(null);
        setDropZone(null);
      },
      end: () => {
        setDragSource(null);
        setDropTarget(null);
        setDropZone(null);
      },
    },
    // Tab label mirrors the Terminal's "workload  command": the MOUNT (workload or local
    // root — the first path segment) alongside the detail (the open file for an editor
    // pane, else the current folder). "All" at the virtual root; the mount alone at a
    // mount's own root.
    tabTitle: (pane: Pane<FileData>) => {
      const p = pane.data.path;
      if (!p) return "All";
      const slash = p.indexOf("/");
      const mount = slash === -1 ? p : p.slice(0, slash);
      const leaf = pane.data.open ?? (slash === -1 ? p : p.slice(p.lastIndexOf("/") + 1));
      return leaf === mount ? mount : `${mount}  ${leaf}`;
    },
    // Browse panes get a sub-header with the refresh button + breadcrumb. Editor panes
    // have none — the file name is already in the tab and the editor's save bar, and
    // reload lives on that bar.
    subHeader: (pane) =>
      pane.data.open ? null : (
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
          <PaneCrumbs pane={pane} onGo={(path) => paneActions.get(pane.id)?.go(path)} />
        </>
      ),
    body: (pane) =>
      pane.data.open && isImageName(pane.data.open) ? (
        <ImageViewerPane pane={pane} />
      ) : pane.data.open ? (
        <FileEditorPane
          pane={pane}
          navigate={(path) => navigateTo(pane.id, path)}
          register={(actions) => registerActions(pane.id, actions)}
        />
      ) : (
        <FilePane
          pane={pane}
          navigate={(path) => navigateTo(pane.id, path)}
          openFile={(name, fromKeyboard) => void openInNewPane(pane.id, name, fromKeyboard)}
          register={(actions) => registerActions(pane.id, actions)}
        />
      ),
  };

  // Persist the whole layout whenever anything changes (stringify deep-tracks).
  createEffect(() => {
    saveLayout(STORAGE_KEY, {
      tree: JSON.parse(JSON.stringify(state.tree)),
      focused: state.focused,
    });
  });

  return (
    <div class="workspace">
      <div class="workspace-toolbar row">
        <strong class="workspace-brand">Files</strong>
        <span class="muted workspace-hint">
          File actions live in the command palette (prefix key, then a command) · ⟳ refreshes a pane
          · hover a tile's edge to split · drag a tab onto another to stack them, onto an edge to
          re-tile · ✕ closes a tab
        </span>
      </div>
      <div class="workspace-body">
        <TreeNode node={state.tree} ctx={ctx} />
      </div>
    </div>
  );
}
