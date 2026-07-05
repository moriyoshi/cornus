import { Show, createResource, createEffect, createSignal, onCleanup, onMount } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { getWorkloads, getTerminals, killTerminal, type SessionState } from "../api";
import { pollResource } from "../poll";
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
  allPanes,
  leaf,
  newPane,
  type Dir,
  type LayoutState,
  type Node,
  type Pane,
} from "./tiling/layout";
import { TreeNode, type TileCtx, type DropZone } from "./tiling/panes";
import TermPane, { type TermData, type TermCtx } from "./terminal/TermPane";
import { promptPanePlacement, resolveSplitSide } from "../pane-placement";

// Terminal is the tiled, tmux-style workspace: a binary tree of splits whose tiles are
// stacks of terminal panes shown as tabs, each attached to a persistent BFF session
// that survives reloads (and stays live in a background tab). Layout — splits, ratios,
// tab stacks, targets, session ids — persists to localStorage; on load, panes reattach
// to still-live sessions. The tree model and chrome are the view-agnostic tiling module
// (shared with the Files explorer); the per-pane session lifecycle lives in TermPane.
const STORAGE_KEY = "cornus.terminal.layout";

const freshData = (): TermData => ({ workload: "", cmd: ["/bin/sh"] });
// A split "here" inherits the target/command but never the session.
const inheritData = (from: Pane<TermData>): TermData => ({
  workload: from.data.workload,
  cmd: [...from.data.cmd],
});
const isValidData = (d: unknown): boolean => {
  const t = d as { workload?: unknown; cmd?: unknown } | undefined;
  return !!t && typeof t.workload === "string" && Array.isArray(t.cmd);
};

function defaultLayout(): LayoutState<TermData> {
  const p = newPane(freshData());
  return { tree: leaf(p), focused: p.id };
}

export default function Terminal() {
  const [state, setState] = createStore<LayoutState<TermData>>(
    loadLayout(STORAGE_KEY, isValidData, defaultLayout),
  );
  const [workloads] = createResource(getWorkloads);
  const running = () => workloads()?.filter((w) => w.running) ?? [];

  // Poll the session list for detected activity so each tab can badge its state.
  const [sessions] = pollResource(getTerminals, 2000);
  const stateOf = (sessionId: string | undefined): SessionState | undefined => {
    if (!sessionId) return undefined;
    const s = sessions()?.find((t) => t.id === sessionId);
    return s?.alive ? s.state : undefined;
  };

  const snapshot = (): Node<TermData> => JSON.parse(JSON.stringify(state.tree));
  const commit = (tree: Node<TermData>) => setState("tree", reconcile(tree));

  const setFocus = (id: string) => setState("focused", id);
  const activate = (id: string) => {
    commit(activatePane(snapshot(), id));
    setState("focused", id);
  };

  // splitFocused (palette) inherits the target; the edge overlay (splitAt) drops a
  // fresh empty pane so its picker lets you choose a workload.
  const splitFocused = (dir: Dir, inherit = true) => {
    const { tree, newPaneId } = splitPane(snapshot(), state.focused, dir, inherit ? inheritData : freshData);
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };
  const splitAt = (paneId: string, dir: Dir, before: boolean) => {
    const { tree, newPaneId } = splitPane(snapshot(), paneId, dir, freshData, before);
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };

  // newPaneHere asks where the new (empty) pane goes — a tab on the current tile, or a
  // split beside it on the preference-resolved side — then creates it and focuses it.
  const newPaneHere = async () => {
    const choice = await promptPanePlacement();
    if (!choice) return;
    const { tree, newPaneId } =
      choice === "stack"
        ? addTab(snapshot(), state.focused, freshData())
        : splitPane(snapshot(), state.focused, "h", freshData, resolveSplitSide() === "left");
    if (newPaneId) setState("focused", newPaneId);
    commit(tree);
  };

  // Drag: onto a tile's CENTER stacks the dragged pane/tile there (tabs); onto an EDGE
  // re-tiles it. Dragging a tab moves one pane; dragging the tab bar moves the whole
  // stack.
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
    const { tree, removed, focus } = closePane(snapshot(), id, freshData);
    if (removed?.data.sessionId) killTerminal(removed.data.sessionId).catch(() => {});
    commit(tree);
    setState("focused", focus);
  };

  const setSession = (id: string, sessionId: string) => commit(updatePane(snapshot(), id, { sessionId }));
  const retarget = (id: string, workload: string, cmd: string[]) => {
    const pane = findPane(snapshot(), id);
    if (pane?.data.sessionId) killTerminal(pane.data.sessionId).catch(() => {});
    commit(updatePane(snapshot(), id, { workload, cmd, sessionId: undefined }));
    setState("focused", id);
  };

  // Contribute the pane commands to the app-wide palette while mounted. The binds
  // mirror tmux: prefix % splits left/right, prefix " splits top/bottom, prefix c makes
  // a new pane, prefix x closes the focused one.
  const paneCommands = (): Command[] => [
    { id: "term:split-h", group: "Terminal", title: "Split pane left / right", bind: "%", run: () => splitFocused("h") },
    { id: "term:split-v", group: "Terminal", title: "Split pane top / bottom", bind: '"', run: () => splitFocused("v") },
    { id: "term:new", group: "Terminal", title: "New pane…", bind: "c", run: () => void newPaneHere() },
    { id: "term:close", group: "Terminal", title: "Close focused pane", bind: "x", run: () => closePaneById(state.focused) },
  ];
  onMount(() => onCleanup(registerCommands(paneCommands, true)));

  const termCtx: TermCtx = { focused: () => state.focused, setSession, retarget, closePane: closePaneById, running };

  const ctx: TileCtx<TermData> = {
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
    // Tab label: workload + command, with the session activity badge.
    tabTitle: (pane: Pane<TermData>) => {
      const label = () =>
        pane.data.workload ? `${pane.data.workload}  ${pane.data.cmd.join(" ")}` : "empty pane";
      return (
        <>
          <span class="tab-name">{label()}</span>
          <Show when={stateOf(pane.data.sessionId) === "blocked"}>
            <span class="badge warn" title="Detected session activity">needs you</span>
          </Show>
          <Show when={stateOf(pane.data.sessionId) === "working"}>
            <span class="badge" title="Detected session activity">working</span>
          </Show>
        </>
      );
    },
    body: (pane) => <TermPane pane={pane} ctx={termCtx} />,
  };

  // Persist the whole layout whenever anything changes (stringify deep-tracks).
  createEffect(() => {
    saveLayout(STORAGE_KEY, { tree: JSON.parse(JSON.stringify(state.tree)), focused: state.focused });
  });

  // On load, drop session ids the BFF no longer knows about so those panes re-create
  // instead of attaching to a dead id.
  onMount(async () => {
    try {
      const live = await getTerminals();
      const liveIds = new Set(live.map((t) => t.id));
      for (const p of allPanes(snapshot())) {
        if (p.data.sessionId && !liveIds.has(p.data.sessionId)) {
          commit(updatePane(snapshot(), p.id, { sessionId: undefined }));
        }
      }
    } catch {
      // BFF unreachable; each pane surfaces its own attach error.
    }
  });

  return (
    <div class="workspace">
      <div class="workspace-toolbar row">
        <strong class="workspace-brand">Terminal</strong>
        <span class="muted workspace-hint">
          Hover a tile's edge to split · drag a tab onto another to stack them, onto an edge to
          re-tile · drag dividers to resize · ✕ closes a tab · sessions persist across reloads
        </span>
      </div>
      <div class="workspace-body">
        <TreeNode node={state.tree} ctx={ctx} />
      </div>
    </div>
  );
}
