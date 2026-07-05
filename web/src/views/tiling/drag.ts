// The tiling workspace's drag/rearrange protocol, and the one implementation of it.
//
// Rearranging is expressed as a handful of calls — begin / over / leave / drop / end — that
// say WHAT the user is doing, never HOW they are doing it. That separation is the point: the
// same calls are driven by a pointer drag of a tab (any device, via the emulated transport
// in dnd.ts) and by the tap-to-pick mode, so the gesture and the keyboard/menu route reach
// every operation alike. `panes.tsx` renders both front-ends; this module owns the state
// behind them.
//
// It lives outside panes.tsx because the state belongs to the HOST (Files / Terminal): it
// is reset by the same code that commits the move, and it must die with the workspace.
// A module-level signal would do neither — it outlives a route change, so navigating
// Files -> Terminal mid-pick would mount the Terminal workspace already picking, with a
// source id from a tree it is not in; and vitest shares the module registry across a
// file's tests, where `cleanup()` unmounts the DOM but cannot clear a module signal, so
// one test leaving pick mode armed would poison the next.
//
// Both hosts were carrying character-identical copies of all of this. They now share one.

import { createSignal } from "solid-js";
import type { Accessor } from "solid-js";
import { addTab, movePane, moveStack, splitPane, stackPane, stackStack } from "./layout";
import type { Dir, Node, Pane } from "./layout";
import { settings } from "../../settings";
import { coarsePointer } from "../../pointer";

// Where a drop lands on a target tile: its center STACKS the dragged pane in as a tab,
// an edge moves it to that side.
export type DropZone = "stack" | "left" | "right" | "top" | "bottom";

// A drag carries a "pane" (one tab) or a "stack" (the whole tile / all its tabs).
export type DragKind = "pane" | "stack";

// A PICK is the tap-driven form of the gesture: instead of dragging something onto a
// target, the user arms a mode and the tiles offer their zones as buttons. Three intents
// share every pixel of that UI and differ only in what the chosen zone does:
//
//   "move"  — relocate the pane named by source().
//   "place" — no source: the zone is where a NEW, EMPTY pane is created. This is how
//             "New pane…" asks its question, instead of a modal naming dispositions
//             ("stack" / "split") that the workspace can simply point at.
//   "split" — divide the tile pointed at, the new pane CONTINUING what that tile shows
//             (same workload and command, same folder). Its centre is withheld: stacking a
//             tab is not a split, whatever else it is.
//
// "place" and "split" are two commands and not one with a flag because they answer
// different questions. "New pane…" is "give me somewhere to start"; "Split pane…" is "show
// me two of this". The empty-vs-inherited data follows from that, and so does the missing
// centre — not the other way round.
export type PickIntent = "move" | "place" | "split";

// Generic only because of beginPlace's optional factory: a placement can be armed with the
// data the new pane must carry, which is host-shaped. Everything else here is ids and zones.
export interface TileDrag<P> {
  // source is the dragged pane id or stack id, per kind — null while placing, where
  // there is nothing yet to move.
  source: Accessor<string | null>;
  // Whether source names one pane or a whole tile. Read by the tap-to-pick overlay, which
  // must not offer a stack to itself as a target.
  kind: Accessor<DragKind>;
  target: Accessor<string | null>;
  zone: Accessor<DropZone | null>;
  // picking names the intent of the pick in flight, or null when none is: while it is set
  // every candidate tile offers its drop zones as buttons. Set and cleared with source in
  // one call, so the overlay can never be up with nothing behind it, and cleared by drop()
  // and end() — one owner for one lifetime.
  picking: Accessor<PickIntent | null>;
  begin: (id: string, kind: DragKind) => void;
  // beginPick arms the same gesture without a pointer drag behind it.
  beginPick: (id: string, kind: DragKind) => void;
  // beginPlace arms it with nothing to move: the zone the user taps is where a new pane is
  // CREATED — its centre as a tab on that tile, an edge as a split beside it. With no
  // argument the pane is the host's empty one ("New pane…"); `make` supplies its data
  // instead, for a placement that already knows what it is placing — Files opening a file
  // you pressed Enter on, which is a pane whose content the user has just named.
  beginPlace: (make?: () => P) => void;
  // beginSplit is the same gesture for "Split pane…": edges only, and the pane it makes
  // continues the tile it divided.
  beginSplit: () => void;
  over: (id: string, zone: DropZone) => void;
  // The pointer left the tile it was over without leaving the drag. `over` has no null
  // form because the mouse's dragover simply stopped arriving; a pointer-driven drag knows
  // when it is over nothing, and a preview left standing under a finger that has moved off
  // is a promise the release will not keep.
  leave: () => void;
  drop: (id: string, zone: DropZone) => void;
  end: () => void;
}

// The host's three primitives: read the tree, write the tree, move the focus. Everything
// else a rearrange needs is in layout.ts.
export interface TileDragOps<P> {
  snapshot: () => Node<P>;
  commit: (tree: Node<P>) => void;
  setFocused: (id: string) => void;
  // The pane the workspace is on. Read only when the placement question is NOT being
  // asked — the `newPaneDisposition` setting answers it in advance, and the tile the
  // answer applies to is the one the user is on, which is the tile the wireframe would
  // have lit first.
  focused: () => string;
  // fresh is the host's EMPTY pane — the data a placement pick creates: a terminal with no
  // session (so its picker asks for a workload), a file pane at the virtual root. Deliberately
  // not "a copy of the pane you were on": placing is the global "New pane…", which is aimed
  // at a tile rather than started from one, so there is no source pane to inherit from.
  fresh: () => P;
  // inherit is the same pane again — what a SPLIT makes: the same workload and command, the
  // same folder. Never the session or the unsaved draft, which belong to the one pane that
  // holds them; the host decides where that line falls. It is given the pane being divided,
  // which under a split pick is the tile the user pointed at rather than the focused one.
  inherit: (from: Pane<P>) => P;
}

// dirBefore maps an edge zone to the split axis and which side the moved tile lands on.
// "stack" never reaches here — it is not an edge.
const dirBefore = (z: DropZone): { dir: Dir; before: boolean } => ({
  dir: z === "left" || z === "right" ? "h" : "v",
  before: z === "left" || z === "top",
});

export function createTileDrag<P>(ops: TileDragOps<P>): TileDrag<P> {
  const [source, setSource] = createSignal<string | null>(null);
  const [kind, setKind] = createSignal<DragKind>("pane");
  const [target, setTarget] = createSignal<string | null>(null);
  const [zone, setZone] = createSignal<DropZone | null>(null);
  const [picking, setPicking] = createSignal<PickIntent | null>(null);
  // The data THIS pick was armed with, if any. Not a signal: nothing renders from it, and a
  // pick's payload is settled the moment it is armed.
  let pending: (() => P) | null = null;

  const clear = () => {
    setSource(null);
    setTarget(null);
    setZone(null);
    setPicking(null);
    pending = null;
  };

  // FOCUS FIRST, THEN COMMIT — every one of these, and the hosts' own splitAt already does
  // it. A rearrange rebuilds the panes it touches, and a pane claims DOM focus on mount
  // only if it can see that it is the focused one (TermPane's picker focuses its workload
  // select that way). Committing first mounts the pane while `focused` still names the pane
  // it replaced, so the guard reads false, nothing takes focus, and it lands on <body> —
  // a keyboard user is dropped out of the app by the very action they just performed.
  const land = (tree: Node<P>, focus: string | undefined) => {
    if (focus) ops.setFocused(focus);
    ops.commit(tree);
  };

  const stackById = (srcId: string, destId: string) => land(stackPane(ops.snapshot(), srcId, destId), srcId);
  const moveById = (srcId: string, destId: string, z: DropZone) => {
    const { dir, before } = dirBefore(z);
    land(movePane(ops.snapshot(), srcId, destId, dir, before), srcId);
  };
  const stackStackById = (srcStackId: string, destId: string) => {
    const { tree, focus } = stackStack(ops.snapshot(), srcStackId, destId);
    land(tree, focus);
  };
  const moveStackById = (srcStackId: string, destId: string, z: DropZone) => {
    const { dir, before } = dirBefore(z);
    const { tree, focus } = moveStack(ops.snapshot(), srcStackId, destId, dir, before);
    land(tree, focus);
  };

  // placeAt creates a pane on the chosen zone — the same two layout calls the pane menu's
  // "New tab here" and "Split ⋯" make, with the tile named by the tap rather than by which
  // tile's menu was open. What the pane starts as is the only thing the two creating
  // intents disagree about, and `splitPane` already takes the factory the target is fed to.
  const placeAt = (destId: string, z: DropZone) => {
    let made: { tree: Node<P>; newPaneId: string };
    if (z === "stack") {
      // Only "place" offers a centre, so the data here is the pick's own or the host's
      // empty pane — never `inherit`, which belongs to the split and would need the target
      // pane to copy from.
      made = addTab(ops.snapshot(), destId, (pending ?? ops.fresh)());
    } else {
      const { dir, before } = dirBefore(z);
      // A pick armed with its own data outranks both host factories: the user has already
      // said what the pane is, so neither an empty one nor a copy of the target is it.
      const makeData = pending ?? (picking() === "split" ? ops.inherit : ops.fresh);
      made = splitPane(ops.snapshot(), destId, dir, makeData, before);
    }
    const { tree, newPaneId } = made;
    // An empty id means the destination was not in the tree; `tree` is then the snapshot
    // unchanged, and committing it would churn the store to say nothing.
    if (!newPaneId) return;
    land(tree, newPaneId);
  };

  const arm = (id: string | null, k: DragKind, intent: PickIntent | null) => {
    pending = null; // every gesture starts with a clean payload; beginPlace sets its own after
    setSource(id);
    setKind(k);
    setTarget(null);
    setZone(null);
    setPicking(intent);
  };

  return {
    source,
    kind,
    target,
    zone,
    picking,
    begin: (id, k) => arm(id, k, null),
    beginPick: (id, k) => arm(id, k, "move"),
    // No source and no kind: neither of these is carrying a pane that already exists.
    //
    // THE CHOKEPOINT FOR `newPaneDisposition`. Every command that creates a pane and has
    // to decide where it goes comes through here, which is why the setting is applied here
    // and not at the three call sites: a fourth creating command added later cannot forget
    // to honour it, and there is one answer to "does this setting cover X?" instead of
    // three. `Split pane…` (beginSplit) is deliberately outside it — see below.
    beginPlace: (make) => {
      arm(null, "pane", "place");
      pending = make ?? null;
      // Not "ask" means the user has already answered, once, for every route: place it and
      // never raise the scrim. The answers are the two the prompt itself offers — the arrow
      // (a tile beside this one) and Space (a tab on it) — so nothing here can reach a
      // layout the question could not. "auto" is not a third one: it is those same two, and
      // the device picks which. Resolving it HERE keeps that true of the rest of the
      // function, and keeps the chokepoint a chokepoint.
      // Matched positively, so an unrecognised value falls through to asking rather than to
      // whichever branch happened to be the `else`. parseSettings spreads stored JSON over
      // the defaults without validating it, so a hand-edited or stale blob can put any
      // string here — and the safe answer to "I don't know where this goes" is the question.
      const stored = settings().newPaneDisposition;
      const d = stored === "auto" ? (coarsePointer() ? "tab" : "split") : stored;
      if (d !== "tab" && d !== "split") return;
      placeAt(ops.focused(), d === "tab" ? "stack" : "right");
      clear();
    },
    // NOT governed by newPaneDisposition. This command's title already names the
    // disposition it makes, so the setting has nothing to decide: under "tab" it would have
    // to contradict the word "Split", and under "split" it would collapse into `prefix %`,
    // which already exists. What it asks for is WHICH EDGE, a finer question than the
    // setting answers, and `Split pane left / right` / `top / bottom` are the standing
    // answers to that one.
    beginSplit: () => arm(null, "pane", "split"),
    over: (id, z) => {
      // Hovering the dragged thing itself is not a target.
      if (id === source()) {
        setTarget(null);
        setZone(null);
        return;
      }
      setTarget(id);
      setZone(z);
    },
    leave: () => {
      setTarget(null);
      setZone(null);
    },
    drop: (id, z) => {
      // A creating pick has no source to guard against dropping on itself: every tile is a
      // legitimate home for a pane that does not exist yet.
      if (picking() === "place" || picking() === "split") {
        placeAt(id, z);
        clear();
        return;
      }
      const s = source();
      if (s && s !== id) {
        if (kind() === "stack") {
          if (z === "stack") stackStackById(s, id);
          else moveStackById(s, id, z);
        } else if (z === "stack") stackById(s, id);
        else moveById(s, id, z);
      }
      clear();
    },
    end: clear,
  };
}
