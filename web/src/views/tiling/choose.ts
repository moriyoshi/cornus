// The pane CHOOSER's state: tmux's "choose a session from a list", pointed at the panes of
// one workspace.
//
// The shape of the interaction is the unusual part, and it is deliberate. A list is on
// screen, but the list is not what the keys drive: the arrows walk the WORKSPACE, tile to
// tile, and the list merely reports where the walk has got to. That is the opposite of a
// chooser dialog, where the keys walk the list and the workspace is a picture of the
// consequence — and it is the right way round here, because the panes are already laid out
// spatially in front of the user. Asking them to translate "the terminal below this one"
// into "two rows further down a list" is asking them to look up something they can see.
//
// Nothing moves until Enter. `selected` is a PREVIEW: the workspace's own `focused` pane is
// untouched for the whole mode, both are on screen at once (the focused tile keeps its
// frame, the previewed one wears a brighter ring), and Escape leaves with the focus exactly
// where it started. That is what makes the walk free — a wrong turn costs nothing, so the
// arrows can be pressed at the speed of looking.
//
// The state lives with the HOST (Files / Terminal), like the drag protocol next door and for
// the same two reasons: a module signal would outlive a route change, mounting the other
// workspace mid-choose with a pane id from a tree it is not in, and vitest's shared module
// registry would carry one test's armed mode into the next.

import { createEffect, createSignal } from "solid-js";
import type { Accessor } from "solid-js";
import { allPanes, findPane, neighborStack, nextPaneId, stackOf, type Node, type Side } from "./layout";
import type { PickIntent } from "./drag";

// A ChoosePurpose is what the mode is FOR: the question in the panel's title, what Enter
// does with the answer, and which panes are not answers at all.
//
// The mode exists because the panes are laid out spatially and a list is the wrong way to
// pick one of them — and that is true of "which pane do these files go to?" exactly as it
// is of "which pane do I want to be in?". Reusing the walk rather than growing a second
// picker is not only less code: it is the same keys, the same numbers on the same tiles,
// and the same Escape, for what is visibly the same question asked twice.
export interface ChoosePurpose {
  // What the panel calls itself. A question, because the mode is one: "Choose a pane",
  // "Copy 3 items to…".
  title: string;
  // What a commit does with the pane that was chosen. The DEFAULT purpose moves the focus
  // there; a transfer moves files and leaves the focus alone, which is why this is a
  // callback and not a flag.
  pick: (id: string) => void;
  // Why this pane cannot be the answer, or undefined when it can. Rendered on the row and
  // enforced at commit — the same "a reason, not a boolean" rule the command palette
  // follows, and for the same reason: a row you cannot press and cannot ask about is worse
  // than one that is missing.
  refuse?: (id: string) => string | undefined;
  // An answer that is not one of the panes — for a transfer, a path typed by hand. It is
  // taken by TYPING: any bare letter leaves the list and hands that letter on as the first
  // character of whatever the escape asks for, so "I want somewhere else" and "here is
  // where" are one continuous action instead of a row to find first.
  //
  // The cost is stated where it is paid, in the panel's own key hint: while an escape is
  // offered, `hjkl` are letters like any other and the walk is on the arrow keys alone.
  // Taking `hjkl` out of the escape instead would be worse — a destination beginning with
  // h, j, k or l could then not be typed at all, and that is not a rule anyone could guess.
  elsewhere?: {
    // How the row names itself. A place, not an action: it sits among panes.
    label: string;
    // seed is the letter that opened it, or "" when the row was clicked.
    take: (seed: string) => void;
  };
}

export interface TileChoose {
  // The previewed pane, or null when the mode is not up — one signal for both, so the
  // overlay can never be on screen with nothing highlighted.
  selected: Accessor<string | null>;
  // What this choose is for. Never null while the mode is up; the plain "go to a pane"
  // choose gets the default purpose rather than a special case in every reader.
  purpose: Accessor<ChoosePurpose>;
  // Whether the question is the mode's OWN — "which pane do I want to be in?" — rather than one
  // a caller brought with it. Every reader that treats the two differently asks this rather than
  // comparing titles: the default purpose is one stable object, so identity is the exact test,
  // and a title is a string a caller could coincidentally match.
  //
  // It exists because the chooser has a standing home now. A pinned chooser IS the plain
  // question, permanently — so a transfer arriving into that same panel would replace the
  // furniture with a modal question and put "Copy 3 items to…" where the user's list of panes
  // was. See PaneChooser: a caller's question gets a card of its own.
  plain: Accessor<boolean>;
  begin: (purpose?: ChoosePurpose) => void;
  // move walks one tile in a direction, landing on that tile's ACTIVE pane. A wall is a
  // no-op: there is nothing over there to preview.
  move: (side: Side) => void;
  // cyclePane walks EVERY pane in the workspace, in the order the list shows them and the
  // numbers count them — so one Tab is always "the next row", whether that row is the next
  // tab of this tile or the first tab of the next one.
  //
  // It exists because of the stacked tabs: they occupy one place on screen, so no direction
  // can distinguish them, and without a key that ignores the layout a background tab would
  // be a row in the list the keyboard could never reach. Confining it to the previewed tile
  // was the smallest thing that solved that, and it made Tab mean two different things
  // depending on where the walk happened to be standing — a key that traverses the workspace
  // on a tile of one pane, and a key that does nothing on a tile of one pane. Walking all of
  // them costs nothing (the tabs of the current tile are still consecutive in this order, so
  // the old moves are a prefix of the new ones) and gives the mode a second complete route
  // through the workspace, next to the spatial one on the arrows.
  cyclePane: (delta: number) => void;
  // The pane's 1-based place in layout order — the number the list and the tiles both wear,
  // and what makes a row and a place on screen the same thing when their labels are not
  // (two Files panes at the virtual root are both "All"). 0 for a pane that is not in the
  // tree. Derived, never stored: a number is a POSITION, so closing a pane renumbers what
  // follows it, and a chooser that showed stale numbers would be worse than one showing
  // none.
  numberOf: (paneId: string) => number;
  // jump previews the pane wearing that number, if there is one. A digit is the same kind
  // of answer as a direction — it moves the walk, it does not end it.
  jump: (n: number) => void;
  // takeElsewhere leaves the mode for the purpose's non-pane answer, passing on the letter
  // that asked for it. A no-op when this purpose offers none, so the panel's key handler
  // does not have to know which purposes do.
  takeElsewhere: (seed: string) => void;
  // commit acts at last — on `id` when given (a click names its own row), otherwise on
  // whatever the walk selected. A refused pane is not committed and does NOT end the mode:
  // the walk is free, so being told "not that one" should cost no more than a wrong turn.
  commit: (id?: string) => void;
  cancel: () => void;
}

export interface TileChooseOps<P> {
  // The live tree, read reactively: the list is rendered from it.
  tree: Accessor<Node<P>>;
  focused: Accessor<string>;
  // The host's "make this pane current": raise it to its tile's visible tab AND focus it.
  // Choosing a background tab is choosing to look at it.
  activate: (id: string) => void;
  // The drag protocol's pick state, and the way to end it. The two modes both take over the
  // keyboard, so exactly one of them may be up: the prefix key still works underneath a
  // pick's scrim, which is all it takes to ask for a chooser while a placement is armed.
  picking: Accessor<PickIntent | null>;
  endPick: () => void;
}

export function createPaneChooser<P>(ops: TileChooseOps<P>): TileChoose {
  const [selected, setSelected] = createSignal<string | null>(null);
  // One object, not a fresh literal per call, so a reader can compare purposes and the
  // default's identity is stable across the whole session.
  const goToPane: ChoosePurpose = { title: "Choose a pane", pick: ops.activate };
  const [purpose, setPurpose] = createSignal<ChoosePurpose>(goToPane);

  // The two modes are mutually exclusive, and this is the whole of it. begin() ends a pick
  // that was up; the effect ends a choose when a pick arms. Note the effect does not read
  // `selected`, so clearing it cannot re-trigger the effect.
  const begin = (p: ChoosePurpose = goToPane) => {
    ops.endPick();
    setPurpose(() => p);
    // Start where the focus is — except on a pane this purpose would refuse, in which case
    // start at the first one it would not. ONE rule, and it is vacuous for the plain
    // chooser (which refuses nothing); what it buys is that a transfer never opens on a
    // greyed row. In the two-pane layout an orthodox file manager taught, that lands the
    // walk on "the other panel" with nothing to press first.
    const panes = allPanes(ops.tree());
    const here = ops.focused();
    const start = !p.refuse?.(here) ? here : panes.find((q) => !p.refuse!(q.id))?.id;
    setSelected(start ?? here);
  };
  createEffect(() => {
    if (ops.picking()) setSelected(null);
  });

  const move = (side: Side) => {
    const id = selected();
    if (!id) return;
    const from = stackOf(ops.tree(), id);
    if (!from) return;
    const next = neighborStack(ops.tree(), from.id, side);
    if (!next) return;
    setSelected(next.panes[next.active].id);
  };

  // `nextPaneId` is deliberately the same step `prefix o` takes, rather than a second walk
  // over the same list: Tab in the chooser IS `prefix o` with a preview in front of it, and
  // two implementations of "the next pane" would eventually disagree about a stacked tab or
  // about where the wrap goes. It is layout order, which is the order the rows are in and the
  // order `numberOf` counts, so Tab steps from number n to number n+1.
  //
  // The one thing not shared is what happens from a pane that is no longer in the tree — a
  // shell exiting takes its own pane with it, under the mode and without a key being pressed.
  // `nextPaneId` recovers to the first pane, which is right for a focus that must land
  // somewhere; here it would teleport the walk on a keystroke that means "one step", and
  // `commit` already answers a vanished selection by ending the mode.
  const cyclePane = (delta: number) => {
    const id = selected();
    if (!id || !findPane(ops.tree(), id)) return;
    setSelected(nextPaneId(ops.tree(), id, delta));
  };

  return {
    selected,
    purpose,
    plain: () => purpose() === goToPane,
    begin,
    move,
    cyclePane,
    numberOf: (paneId) => allPanes(ops.tree()).findIndex((p) => p.id === paneId) + 1,
    jump: (n) => {
      if (!selected()) return; // a digit is a move within the mode, not a way into it
      const pane = allPanes(ops.tree())[n - 1];
      if (pane) setSelected(pane.id);
    },
    takeElsewhere: (seed) => {
      if (!selected()) return; // as with a digit: a key inside the mode, not a way into it
      const other = purpose().elsewhere;
      if (!other) return;
      // The mode ends BEFORE the escape runs, because what the escape opens is a modal and
      // the chooser's scrim would sit over it — and because this is a commit like any
      // other: the question has been answered, just not with a pane.
      setSelected(null);
      other.take(seed);
    },
    commit: (id) => {
      const target = id ?? selected();
      // A pane can vanish under the mode without a key being pressed: a shell that exits
      // closes its own pane (TermPane's onExit). Acting on a dead id would, for the default
      // purpose, leave `focused` naming a pane that is not in the tree — which every
      // contextual command then misses. The mode ends: what was chosen no longer exists.
      if (!target || !findPane(ops.tree(), target)) {
        setSelected(null);
        return;
      }
      // A refused pane leaves the mode UP. The row already says why, and ending here would
      // punish the press with a re-entry — where a wrong turn during the walk costs nothing
      // at all. This is the same contract as the palette's disabled row: the key is owned
      // and quietly does nothing.
      if (purpose().refuse?.(target)) return;
      setSelected(null);
      purpose().pick(target);
    },
    cancel: () => setSelected(null),
  };
}
