import { Show, Switch, Match, For, createEffect, createSignal, onCleanup, onMount } from "solid-js";
import type { Accessor, JSX } from "solid-js";
import type { Dir, Node, Pane, Side } from "./layout";
import type { DragKind, DropZone, TileDrag } from "./drag";
import type { TileChoose } from "./choose";
import { cancelPendingDrag, dragSource, dragging, dropTarget } from "../../dnd";
import type { DragPoint } from "../../dnd";
import { openPalette } from "../../command-center";
import { settings } from "../../settings";

// The tag every pane-scoped command carries, and the filter the tile's ⋮ opens the
// palette with. Exported so the hosts tag against this constant rather than a spelling:
// the menu is assembled by matching them, so a typo on either side is a menu entry that
// silently disappears.
export const PANE_TAG = "pane";

// The view-agnostic tiling chrome: it renders the split tree, the divider
// drag-to-resize, the edge-overlay split buttons, and the drag gestures. Each tile is a
// STACK shown as a tab bar over a body; dragging a tab onto a tile's center stacks it
// there (tabs), onto an edge moves it beside (re-tile) — including onto its OWN tile's
// edge, which is how a stack of tabs is pulled back apart. Everything screen-specific (a
// pane's tab label, optional sub-header, and body) is supplied via TileCtx callbacks, so
// the same chrome drives the Files explorer and the Terminal workspace.
//
// Every gesture here is POINTER-driven — the tab drag included, which was HTML5
// drag-and-drop until a finger turned out to have no way to perform it. See dnd.ts for the
// two transports and why this one uses only the emulated half.

// Each edge carries its own strip THICKNESS, in px. It lives here, not in styles.css,
// because the same number has to do two jobs that must never drift apart: it sizes the
// strip, and it is the band the hover-intent dwell watches — "armed" has to mean exactly
// "the pointer is on this strip", or the tile would light up a bar the pointer cannot
// click, or arm a strip the pointer never reached. The top strip is the thin one: it hugs
// the TILE's top edge, which puts it over the tab bar, so its live area is kept to the few
// pixels above the tabs themselves.
const SPLIT_EDGES = [
  { side: "top", dir: "v", before: true, px: 10 },
  { side: "bottom", dir: "v", before: false, px: 20 },
  { side: "left", dir: "h", before: true, px: 20 },
  { side: "right", dir: "h", before: false, px: 20 },
] as const;

const THICKNESS = Object.fromEntries(SPLIT_EDGES.map((e) => [e.side, e.px])) as Record<Side, number>;
const EDGE_BY_SIDE = Object.fromEntries(SPLIT_EDGES.map((e) => [e.side, e])) as Record<
  Side,
  (typeof SPLIT_EDGES)[number]
>;

// HOVER INTENT for the edge-split overlays. The overlays are invisible strips lying on the
// tile's edges — the top one over the tab bar — so without a gate they answer the very
// first mousemove that grazes an edge: they flash while the pointer is only passing
// through and — worse — they are hit-testable the whole time, so a click meant for
// whatever sits under them (a tab, its ✕, a file row) splits the tile instead. A zone
// therefore stays CLICK-THROUGH (pointer-events: none) and invisible until the pointer has
// RESTED on it for SPLIT_ARM_DELAY_MS; moving off it disarms at once (slow to show,
// instant to hide). That dwell is the whole reason the strips may keep hugging the tile
// edge, tab bar included. Exported so the tests dwell for the real delay, not a copy.
export const SPLIT_ARM_DELAY_MS = 450;

// THE SAME AFFORDANCE FOR A FINGER, in two touches. A hover dwell is unreachable on touch —
// pointermove only reports while the finger is DOWN, and lifting it would disarm the strip
// before any click landed — so the strips were mouse-only and splitting was left to the ⋮
// menu. The gesture that replaces the hover is:
//
//   1. hold on the edge; the bar glows its way up over SPLIT_HOLD_MS,
//   2. let go, and the glow REMAINS and fades over SPLIT_HOT_MS — much slower, so there is
//      no hurry about the second half,
//   3. touch the same edge again while it is still lit, and the tile splits.
//
// The two touches are the whole point. One press cannot both arm and commit — it would have
// to guess, at the moment the finger lands, whether the user meant to split, drag a tab, or
// press what is underneath — whereas a lit bar asks the question out loud and the second
// touch answers it. The fade is the deadline, drawn: how bright the bar still is IS how long
// is left, so nothing has to explain the window.
//
// SPLIT_HOLD_MS MUST STAY BELOW dnd.ts's DRAG_LIFT_MS, and a test pins the relationship. The
// top strip lies over the tab bar, which is a drag handle, so one press can be counting down
// towards both gestures; the shorter timer is the one that gets to claim it, and the charge
// retires the drag's pending press as it completes. Reversed, the drag would win every time
// and the top edge could never be split by touch at all.
export const SPLIT_HOLD_MS = 350;
// How long a bar released at FULL heat stays live, and a bar re-heated only part of the way
// gets the matching fraction of it, never the whole window again.
//
// Ten seconds, which is long for a deadline and deliberately so: this is not a race the user
// should ever feel they are in. The second half of the gesture is "look at where the split
// would go, decide, touch it" — and a window that has to be BEATEN turns deciding into
// hurrying. It started at 4s, twice reported as cooling too quickly. Nothing bad happens at
// the far end of it either: the bar is visible the whole time, a touch anywhere else puts it
// out at once, and any other mode disarms it — so the cost of being generous here is a
// purple bar somebody may glance at, and the cost of being mean is a gesture that fails.
export const SPLIT_HOT_MS = 10000;
// The line between the two things a touch on a lit bar can mean. Under this it is a TAP and
// spends the bar; over it the finger is holding, which re-heats instead. It has to be a
// duration and not a movement: both gestures land in the same place and neither travels, so
// how long the finger stays is the only thing that distinguishes them. Comfortably above a
// deliberate tap (~100ms) and far below the ramp, so neither reading is ever a near miss.
export const SPLIT_TAP_MS = 200;
// How far past the critical value the heat may run. Holding does not STOP at the point the
// bar is spendable — the visual peaks there and the rest is banked — so a deliberate long
// press buys a bar that stays at full brightness before it starts to fade at all. The cap is
// what keeps that finite: heat rises ~29x faster than it falls (SPLIT_HOT_MS / SPLIT_HOLD_MS),
// so a finger resting for a few seconds would otherwise light a bar for the rest of the
// afternoon. At 2 the most a hold can buy is one extra window: ten seconds held, then the
// usual ten of fading.
export const SPLIT_HEAT_MAX = 2;

// The arrow each edge zone wears in the tap-to-pick overlay. It points the way the pane
// travels, which is the same way the drop indicator fills.
const PICK_ARROW: Record<"left" | "right" | "top" | "bottom", string> = {
  left: "←",
  right: "→",
  top: "↑",
  bottom: "↓",
};

// The strips also reach OUTSIDE the tile, over the workspace gutter, because aiming at a
// pane border and landing in the gap in front of it is the common miss. The gutter is
// only .workspace's padding (var(--space-2)), so a few px past it costs nothing: the
// pointer is then off the workspace and stops reporting. The reach is NOT part of any
// strip's box — the tile clips its children — so an outside dwell arms the strip and an
// outside click is completed by the tile itself (splitFromGutter).
const OUTSET_PX = 10;

// How much of each edge a strip does NOT cover, per end, as a fraction of that edge's
// length. THE STRIP IS THE BAR: 0.15 is where `.pane-split-bar` starts and ends, so the
// live area and the purple bar the user aims at are the same rectangle. They used to
// disagree — the strip ran the full edge while the bar showed the middle 70% — which made
// the hotzone an invisible band a third longer than anything on screen, and every corner of
// every tile a place where a press meant for the content split the tile instead.
//
// It is also what pulls the strips off the other gestures. A tile's left and right edges ran
// the full height, tab bar included, so they lay across the whole-stack drag handle; at 15%
// they clear it entirely on any tile taller than a few rows. Only the TOP strip still
// overlaps the tabs, which is unavoidable — a split divides the whole tile, so the top edge
// is the tile's own top — and it is 10px of it.
//
// Set INLINE from here, like the thickness and for the same reason: `edgeAt` arms on this
// number and the stylesheet draws with it, and a strip that is live where it is not drawn is
// the bug this whole gate exists to prevent.
const EDGE_INSET = 0.15;
// Whether a point falls within a strip's extent ALONG its edge — `start`/`size` being the
// tile's origin and span on the axis the strip runs down.
const alongEdge = (pos: number, start: number, size: number): boolean =>
  pos >= start + size * EDGE_INSET && pos <= start + size * (1 - EDGE_INSET);

// edgeAt names the strip a point rests on, or null for anywhere else. A strip owns its own
// THICKNESS across the edge and, since EDGE_INSET, only the middle stretch ALONG it — so
// "armed" means exactly "the pointer is on the bar you can see", and the live area and the
// drawn bar can never disagree. Outside the tile the point may be up to `outset` px past one
// edge, and the along-edge test still applies: diagonally off a corner, or level with one,
// belongs to no edge. The corners are now nobody's, which is what stops a press aimed at a
// tile's corner content from splitting it.
function edgeAt(r: DOMRect, x: number, y: number, outset: number): Side | null {
  // A zero-sized rect (nothing laid out) counts as nowhere: arming a strip with no
  // on-screen extent could only steal clicks.
  if (!r.width || !r.height) return null;
  const dl = x - r.left;
  const dr = r.right - x;
  const dt = y - r.top;
  const db = r.bottom - y;
  const inX = dl >= 0 && dr >= 0;
  const inY = dt >= 0 && db >= 0;
  // The side strips run down the tile's height, the top/bottom ones across its width, so
  // each is bounded by the OTHER axis' inset.
  const downSide = alongEdge(y, r.top, r.height);
  const acrossTop = alongEdge(x, r.left, r.width);
  if (inX && inY) {
    if (downSide && (dl < THICKNESS.left || dr < THICKNESS.right)) return dl <= dr ? "left" : "right";
    if (acrossTop && (dt < THICKNESS.top || db < THICKNESS.bottom)) return dt <= db ? "top" : "bottom";
    return null;
  }
  if (downSide && dl < 0 && dl >= -outset) return "left";
  if (downSide && dr < 0 && dr >= -outset) return "right";
  if (acrossTop && dt < 0 && dt >= -outset) return "top";
  if (acrossTop && db < 0 && db >= -outset) return "bottom";
  return null;
}

// The drop-zone vocabulary and the state behind a rearrange live in ./drag; re-exported
// here because every host reaches the tiling chrome through this module.
export type { DropZone, TileDrag } from "./drag";

// zoneAt maps a point within a tile to a zone: the central region (each edge > EDGE_BAND
// away) is "stack", otherwise the nearest edge.
const EDGE_BAND = 0.3;
export function zoneAt(r: DOMRect, x: number, y: number): DropZone {
  if (!r.width || !r.height) return "stack";
  const px = (x - r.left) / r.width;
  const py = (y - r.top) / r.height;
  const dl = px;
  const dr = 1 - px;
  const dt = py;
  const db = 1 - py;
  const m = Math.min(dl, dr, dt, db);
  if (m > EDGE_BAND) return "stack";
  if (m === dl) return "left";
  if (m === dr) return "right";
  if (m === dt) return "top";
  return "bottom";
}

// TileCtx threads the shared handlers and the host's per-pane renderers to every node.
// A drag target/drop id names SOME pane of the dest tile — usually its active one, but
// not necessarily (see dropIdFor); stackPane/movePane resolve the stack from any pane in
// it, so the tile is what is addressed either way.
export interface TileCtx<P> {
  focused: Accessor<string>;
  setFocus: (id: string) => void;
  // activate makes a pane the visible tab of its stack (and focuses it).
  activate: (id: string) => void;
  closePane: (id: string) => void;
  splitAt: (id: string, dir: Dir, before: boolean) => void;
  setRatio: (id: string, ratio: number) => void;
  // Whether the workspace can be bigger than the screen (the `workspaceGrowth` setting). Read
  // by the divider, which offers a second gesture only where there is a second thing to do:
  // under the dividing layout an Alt-drag is an ordinary drag, rather than a modifier that
  // silently does nothing.
  extending: Accessor<boolean>;
  // Resize ONE side of a split and let the workspace take up the difference — the Alt-dragged
  // divider. Given the movement in PIXELS and the axis, because converting to viewport extents
  // needs the scroll container's size, which belongs to the host.
  resizeExtend: (splitId: string, growA: boolean, deltaPx: number, dir: Dir) => void;
  // Rearrange state and the calls that drive it (see ./drag). The tab drag and the
  // tap-to-pick mode speak through this one protocol, whatever the pointer.
  drag: TileDrag<P>;
  // The pane chooser's preview (see ./choose). Every tile reads it to know whether the
  // highlight is on one of its panes; the list itself is PaneChooser's job.
  choose: TileChoose;
  // Host-supplied rendering. tabTitle is the short label on a tab; body draws a pane's
  // content; subHeader is an optional row under the tab bar for the active pane (e.g.
  // the Files breadcrumb) — return null to omit it for a given pane; headerExtra is an
  // optional slot at the tab bar's right (e.g. a refresh button), given the active pane.
  //
  // `plain` asks for the pane's NAME AND NOTHING ELSE — no activity badge, no "following",
  // nothing that reports what the pane is doing. A label is normally a label plus whatever
  // the pane needs to announce about itself, and that is right on a tab, which is the pane's
  // standing representative on screen. It is wrong in a list of DESTINATIONS: see `asking`
  // in ./PaneChooser for why a caller's question strips its rows down to identity. Every
  // implementation must honour it by dropping decoration, never by dropping name.
  tabTitle: (pane: Pane<P>, plain?: boolean) => JSX.Element;
  body: (pane: Pane<P>) => JSX.Element;
  subHeader?: (pane: Pane<P>) => JSX.Element | null;
  headerExtra?: (pane: Pane<P>) => JSX.Element;
}

// TreeNode dispatches on node kind. Reading node.type tracks it in the store, so a
// stack becoming a split (or vice-versa) swaps the branch; a ratio or active-tab change
// leaves both branches — and their live content — mounted.
export function TreeNode<P>(props: { node: Node<P>; ctx: TileCtx<P> }): JSX.Element {
  return (
    <Switch>
      <Match when={props.node.type === "stack"}>
        <StackView node={props.node as Extract<Node<P>, { type: "stack" }>} ctx={props.ctx} />
      </Match>
      <Match when={props.node.type === "split"}>
        <SplitView node={props.node as Extract<Node<P>, { type: "split" }>} ctx={props.ctx} />
      </Match>
    </Switch>
  );
}

// What a key MEANS in the pick mode. A direction is not focus movement, it is the answer:
// press ↑ and the pane goes above, in one keystroke, with nothing to confirm afterwards.
// Aiming-then-committing would be the wrong shape for a question whose entire answer is a
// direction — it puts two steps where the mouse has one, and the pointer never had to
// "select" the top zone before pressing it either.
//
// Three spellings of each direction, because this is a tmux-shaped workspace and its users
// arrive with three sets of fingers: the arrow keys, readline's Ctrl-P/N/B/F, and vi's
// k/j/h/l. Exported because the pane CHOOSER walks the same four directions with the same
// twelve keys — a second copy would be a second place for `k` to stop meaning up.
export const KEY_SIDE: Record<string, Side> = {
  ArrowUp: "top",
  ArrowDown: "bottom",
  ArrowLeft: "left",
  ArrowRight: "right",
  k: "top",
  j: "bottom",
  h: "left",
  l: "right",
};
// Ctrl-N is the one the browser keeps for itself (new window) in most builds, so it is
// bound for completeness rather than relied on; ↓ and j cover the same move.
export const CTRL_SIDE: Record<string, Side> = { p: "top", n: "bottom", b: "left", f: "right" };

// The pick mode's map is the directions plus the centre: Space is "stack here", with Enter
// alongside it as the ordinary confirm, since the centre is what a plain "yes" means here.
const KEY_ZONE: Record<string, DropZone> = { ...KEY_SIDE, " ": "stack", Enter: "stack" };
const CTRL_ZONE: Record<string, DropZone> = CTRL_SIDE;

// PickBackdrop is the chrome around a tap-to-pick: the instruction that says what the
// workspace is now waiting for, the keyboard route through the targets, and the two ways
// out. Rendered once per workspace by the host (inside .workspace), not per tile — there is
// one mode, so there is one way to leave it and one thing steering the keyboard, and its
// onCleanup is what guarantees neither can outlive the screen.
export function PickBackdrop<P>(props: { ctx: TileCtx<P> }): JSX.Element {
  // Where focus was when the mode armed, restored only if it is CANCELLED: a completed pick
  // moves focus to the pane it produced, and pulling it back would undo that. Same split
  // ModalHost draws between dismiss and submit.
  let prevFocus: HTMLElement | null = null;

  // The candidate tiles, read from the DOM rather than through refs. Their overlays belong
  // to the TILES — one per candidate — and this is the only place that needs to see all of
  // them at once, which is precisely what a per-tile ref cannot give.
  const overlays = () => Array.from(document.querySelectorAll<HTMLElement>(".pane-pick-overlay"));
  // A direction key has to act on SOME tile, and the one it acts on is the one holding
  // focus. The overlay itself is the focus holder, not any single target inside it: a ring
  // on one button would promise that the button is what a confirm commits, when the whole
  // point is that every direction is one keystroke away. Which tile that is stays visible
  // anyway — focusing the overlay drives the tile's own focusin, so `.stack.focused`
  // already frames it.
  const current = () =>
    (document.activeElement as HTMLElement | null)?.closest<HTMLElement>(".pane-pick-overlay") ?? null;

  // A key PRESSES the button rather than re-deriving the drop. Same handler, same
  // dropIdFor, same stopPropagation — a second path here would be a second set of rules
  // about which pane a tile reports, and they would drift. A zone the tile does not offer
  // (the centre, on the tile a move came from) has no button, so the key does nothing,
  // which is exactly what "you cannot stack it where it already is" should feel like.
  const press = (overlay: Element, z: DropZone) =>
    overlay.querySelector<HTMLElement>(`.pane-pick-zone.zone-${z}`)?.click();

  // Leaving without choosing. Focus goes back where it came from here and NOT in a cleanup:
  // the drop path unmounts this component too, and restoring there would steal focus back
  // from the pane the pick just made.
  const cancel = () => {
    props.ctx.drag.end();
    if (prevFocus?.isConnected) prevFocus.focus();
  };

  // The mode's keys are registered only while it is up, and removed with it. The app owns a
  // capture-phase document keydown for the tmux prefix (App.tsx), and that one deliberately
  // stands aside for anything inside .xterm so a live shell keeps its own keys — Escape,
  // the arrows, and a bare `j` included. Swallowing them for the duration of a mode the
  // user explicitly entered is intended; swallowing them the rest of the time is not, which
  // is why this listener's whole lifetime is the mode's.
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        cancel();
        return;
      }
      const all = overlays();
      if (!all.length) return;
      // Focus should be on a candidate, but if something took it away the keys still have
      // to work — a mode you cannot answer is a mode you cannot leave except by cancelling.
      // The first candidate stands in.
      const at = Math.max(0, all.indexOf(current()!));
      if (e.key === "Tab") {
        // Tab is the only NAVIGATION left, and it navigates the one thing a direction
        // cannot say: which tile. It also traps — without it Tab leaves for the nav behind
        // the scrim, every control of which the mode has covered.
        e.preventDefault();
        e.stopPropagation();
        all[(at + (e.shiftKey ? -1 : 1) + all.length) % all.length].focus();
        return;
      }
      // A letter only counts bare; the same letter under Ctrl is a different binding, and
      // under Alt/Meta it belongs to the OS or the browser.
      if (e.altKey || e.metaKey) return;
      const named = e.key.length === 1 ? e.key.toLowerCase() : e.key;
      const z = e.ctrlKey ? CTRL_ZONE[named] : KEY_ZONE[named];
      if (!z) return;
      e.preventDefault();
      e.stopPropagation();
      press(all[at], z);
    };
    window.addEventListener("keydown", onKey, true);
    onCleanup(() => window.removeEventListener("keydown", onKey, true));
  });

  // A mode nobody can enter from the keyboard is not keyboard-operable however well its
  // keys work: the command that armed this ("New pane…") was itself reached by key, and
  // focus is still wherever the palette restored it to — often inside a terminal, which
  // swallows Tab. So the mode takes focus, and takes it on the tile the user was already
  // on, `.stack.focused`, which is both the likeliest answer and the one place a direction
  // key means what the user is already looking at.
  onMount(() => {
    prevFocus = document.activeElement as HTMLElement | null;
    const own = document.querySelector<HTMLElement>(".stack.focused .pane-pick-overlay");
    (own ?? overlays()[0])?.focus();
  });

  // Whether more than one tile is a candidate, which is what decides if Tab means anything.
  // Sampled once: the tree cannot change while a pick is up, so the candidate set is fixed
  // for the mode's lifetime and a signal beats re-reading the DOM on every render.
  const [multi, setMulti] = createSignal(false);
  onMount(() => setMulti(overlays().length > 1));

  // The tap that CHOSE "Move pane…" is still travelling: dismissing the modal on click
  // means a trailing synthesized click (touch) can land on a scrim that mounted in the
  // same tick and cancel the mode before the user sees it. The scrim therefore ignores
  // dismissals until after the current task.
  const [live, setLive] = createSignal(false);
  onMount(() => {
    const t = setTimeout(() => setLive(true), 0);
    onCleanup(() => clearTimeout(t));
  });

  return (
    <>
      <div
        class="pane-pick-scrim"
        onClick={(ev) => {
          // …and the same story one step further out. A click on a FILE NAME in the
          // listing arms this mode, so when that click is the first of a double-click the
          // second one lands here, ~100ms later, on a scrim the same gesture just raised —
          // and cancelled the very question it had asked. Double-clicking a file name
          // therefore never opened it. `detail` is the browser's count of the clicks in one
          // press sequence, so anything past the first belongs to the gesture that armed
          // the mode rather than to a new one meaning "never mind"; the guard above cannot
          // see it, being a deadline rather than a reading of the gesture.
          if (live() && ev.detail <= 1) cancel();
        }}
      />
      {/* The hint is the only thing on screen that says WHICH question the wireframes are
          asking — the targets themselves look identical for a move and a placement — and
          the only thing that says the question can be answered without a pointer.

          A SIBLING of the scrim, not a child of it. The scrim has to sit BELOW the tiles'
          pick overlays so a tap on a target reaches the button rather than the cancel, and
          a positioned parent is a stacking context its children cannot climb out of — so a
          hint inside the scrim is washed over by the tint of whichever tile it lies on,
          which is every tile that is a candidate. */}
      <p class="pane-pick-hint">
        {props.ctx.drag.picking() === "split"
          ? "Tap where the split should go"
          : props.ctx.drag.picking() === "place"
            ? "Tap where the new pane should go"
            : "Tap where this pane should go"}
        {/* A key is listed only where it does something. On a split there is no centre to
            stack into; where only one tile is a candidate — which is every creating pick,
            and a move with nowhere much to go — Tab has nothing to move between. Naming an
            inert key is worse than naming none: the reader tries it, nothing happens, and
            now the whole line is suspect. */}
        <span class="pane-pick-keys">
          ↑↓←→ or hjkl place ·{" "}
          <Show when={props.ctx.drag.picking() !== "split"}>Space stacks · </Show>
          <Show when={multi()}>Tab next tile · </Show>
          Esc cancels
        </span>
      </p>
    </>
  );
}

// EdgeDivider is the handle on the workspace's own right or bottom border.
//
// Every other divider is a SplitView's, sitting between two tiles; these two have nothing on
// their far side, so what they drag is the workspace's extent (see resizeEdge in ./grow). The
// pointer bookkeeping is the same as SplitView's and is written out again rather than shared:
// what the two have in common is capture and teardown, and what they differ in is the whole of
// what the numbers mean — one solves a ratio between siblings, the other reports how far an edge
// has to travel. A single function taking a flag would be longer than both.
//
// It works in CORRECTIONS, not accumulated deltas: each move asks where the edge is now and
// reports the difference from where the pointer wants it. That is what keeps the handle under
// the finger when the model clamps — at one viewport, or against a tile at MIN_TILE — instead of
// drifting further away with every pixel of a drag that is no longer having any effect.
export function EdgeDivider(props: {
  dir: Dir;
  // Where the workspace's edge is right now, in client px.
  edgeAt: () => number;
  onResize: (deltaPx: number) => void;
}): JSX.Element {
  let stop: (() => void) | undefined;
  onCleanup(() => stop?.());

  const startDrag = (e: PointerEvent) => {
    if (e.button !== 0 || e.isPrimary === false) return;
    e.preventDefault();
    stop?.();
    const handle = e.currentTarget as HTMLElement;
    const id = e.pointerId;
    handle.setPointerCapture?.(id);
    const axisOf = (ev: { clientX: number; clientY: number }) =>
      props.dir === "h" ? ev.clientX : ev.clientY;
    const startedAt = axisOf(e);
    const startedEdge = props.edgeAt();

    const move = (ev: PointerEvent) => {
      if (ev.pointerId !== id) return;
      props.onResize(startedEdge + (axisOf(ev) - startedAt) - props.edgeAt());
    };
    const end = (ev: PointerEvent) => {
      if (ev.pointerId === id) stop?.();
    };
    const lost = () => stop?.();

    stop = () => {
      stop = undefined;
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", end);
      window.removeEventListener("pointercancel", end);
      handle.removeEventListener("lostpointercapture", lost);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
    window.addEventListener("pointercancel", end);
    handle.addEventListener("lostpointercapture", lost);
  };

  return (
    <div
      class="divider edge"
      classList={{ "edge-right": props.dir === "h", "edge-bottom": props.dir === "v" }}
      role="separator"
      aria-orientation={props.dir === "h" ? "vertical" : "horizontal"}
      aria-label={
        props.dir === "h" ? "Resize the workspace's width" : "Resize the workspace's height"
      }
      onPointerDown={startDrag}
    />
  );
}

function SplitView<P>(props: { node: Extract<Node<P>, { type: "split" }>; ctx: TileCtx<P> }): JSX.Element {
  let container!: HTMLDivElement;
  // The teardown for the drag in flight, or undefined when none is. Held here (not in the
  // closure alone) so a drag can be ended by something other than its own pointerup.
  let stop: (() => void) | undefined;
  // A drag that outlives its component would keep resizing a node that no longer exists,
  // measured off a detached container whose rect is all zeros.
  onCleanup(() => stop?.());

  const startDrag = (e: PointerEvent) => {
    // A right- or middle-press must not resize. Tested against false, not falsy: a
    // synthetic MouseEvent (jsdom implements no PointerEvent) has `isPrimary: undefined`,
    // and `!undefined` would turn every test's pointerdown into a silent no-op — the
    // whole suite would pass while exercising nothing.
    if (e.button !== 0 || e.isPrimary === false) return;
    e.preventDefault();
    stop?.(); // a fresh press supersedes any drag that somehow survived
    const handle = e.currentTarget as HTMLElement;
    const id = e.pointerId;

    // Alt makes this an EXTEND rather than a trade: the side being dragged changes size and the
    // other one keeps its own, so the workspace grows or shrinks to suit.
    //
    // LATCHED at the press, deliberately, rather than read from each move event. A user who
    // lets go of Alt halfway through a drag has not asked for the divider to start meaning
    // something else under their finger — the gesture is whatever it was when it began, which is
    // also the only reading under which the preview matches the result.
    const extend = e.altKey === true && props.ctx.extending();
    // Where the pointer and the low-side child started, for the extend path. It works in
    // DELTAS from these, so it needs no grab offset: the divider cannot teleport if the number
    // being applied is how far the pointer has come.
    const startedAt = props.node.dir === "h" ? e.clientX : e.clientY;
    const lowSide = () => {
      const el = container.firstElementChild as HTMLElement | null;
      if (!el) return 0;
      const r = el.getBoundingClientRect();
      return props.node.dir === "h" ? r.width : r.height;
    };
    const startedWide = lowSide();
    // Optional-call: jsdom implements no setPointerCapture, and a hard call would throw
    // out of the handler before a single move was tracked.
    handle.setPointerCapture?.(id);

    // The GRAB OFFSET. Without it the handle's centre teleports to the contact point on
    // the first move — up to half the handle's width. Invisible under a 1px cursor on a
    // 6px handle; a visible jump under a fingertip on the wider touch handle.
    const hr = handle.getBoundingClientRect();
    const grab =
      props.node.dir === "h" ? e.clientX - (hr.left + hr.width / 2) : e.clientY - (hr.top + hr.height / 2);

    const move = (ev: PointerEvent) => {
      // A second finger must not drive this divider. Note this filter is vacuous unless
      // the event actually carries a pointerId — see the test helper's comment.
      if (ev.pointerId !== id) return;
      if (extend) {
        // Target size for the low-side child, then the correction from where it actually is.
        // Re-measured every move rather than accumulated, so a step the model clamped does not
        // leave the pointer and the divider drifting apart for the rest of the drag.
        const want = startedWide + ((props.node.dir === "h" ? ev.clientX : ev.clientY) - startedAt);
        props.ctx.resizeExtend(props.node.id, true, want - lowSide(), props.node.dir);
        return;
      }
      const rect = container.getBoundingClientRect();
      const span = props.node.dir === "h" ? rect.width : rect.height;
      if (!span) return; // detached or never laid out: the ratio would be NaN
      const pos = props.node.dir === "h" ? ev.clientX - rect.left : ev.clientY - rect.top;
      props.ctx.setRatio(props.node.id, (pos - grab) / span);
    };
    const end = (ev: PointerEvent) => {
      if (ev.pointerId === id) stop?.();
    };
    // The browser can take the pointer away without ever sending pointerup — a touch
    // promoted to a scroll, an incoming call, a capture stolen. Both are terminal, and
    // neither was handled before: the window listeners leaked and the pane went on
    // resizing under every later pointer move.
    const lost = () => stop?.();

    stop = () => {
      stop = undefined;
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", end);
      window.removeEventListener("pointercancel", end);
      handle.removeEventListener("lostpointercapture", lost);
    };
    // No releasePointerCapture: pointerup and pointercancel release implicitly,
    // lostpointercapture means it is already gone, and calling it with a pointerId that
    // is no longer active throws.
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
    window.addEventListener("pointercancel", end);
    handle.addEventListener("lostpointercapture", lost);
  };

  return (
    <div class="split" classList={{ h: props.node.dir === "h", v: props.node.dir === "v" }} ref={container}>
      <div class="split-child" style={{ "flex-grow": String(props.node.ratio) }}>
        <TreeNode node={props.node.a} ctx={props.ctx} />
      </div>
      <div class="divider" onPointerDown={startDrag} />
      <div class="split-child" style={{ "flex-grow": String(1 - props.node.ratio) }}>
        <TreeNode node={props.node.b} ctx={props.ctx} />
      </div>
    </div>
  );
}

function StackView<P>(props: { node: Extract<Node<P>, { type: "stack" }>; ctx: TileCtx<P> }): JSX.Element {
  const activeId = () => props.node.panes[props.node.active]?.id ?? "";
  const activePane = () => props.node.panes[props.node.active];
  const focused = () => props.ctx.focused() === activeId();
  // The pane id this tile reports to the drag handlers. A drop resolves the TILE from any
  // pane in it, so the active pane normally speaks for the tile. The exception is a tab
  // dragged out of THIS tile: pressing a tab activates it, so by the time the drag begins
  // the active pane IS the dragged one, and naming it would make source === dest — which
  // both the over and drop handlers read as "dropped on itself" and discard. Any other tab
  // names the same tile without that collision, and that is what lets a stacked tile be
  // split by pulling one of its own tabs to an edge. A solo tile has no other tab, so it
  // keeps naming itself and the drop stays the no-op it should be (a lone pane cannot move
  // beside itself). Dropping a tab back on its own tile's CENTER is likewise nothing to do
  // — it is already a tab there — so that one deliberately reports the collision.
  const dropIdFor = (z: DropZone) => {
    const src = props.ctx.drag.source();
    if (!src) return activeId();
    const mine = props.node.panes.some((p) => p.id === src);
    if (mine && z === "stack") return src;
    if (src !== activeId()) return activeId();
    return props.node.panes.find((p) => p.id !== src)?.id ?? activeId();
  };
  // The tile is the drop target when the drag names ANY of its panes — dropIdFor may name
  // a background tab, so an identity test against the active one would miss it.
  const isDropTarget = () => {
    const t = props.ctx.drag.target();
    return !!t && t !== props.ctx.drag.source() && props.node.panes.some((p) => p.id === t);
  };
  const zone = () => props.ctx.drag.zone() ?? "stack";

  // THE TILE IS A DROP TARGET AND ITS TABS ARE DRAG SOURCES, both on the `emulated`
  // transport (see dnd.ts) — there is no HTML5 drag-and-drop left anywhere in the tiling
  // chrome. Rearranging is entirely in-page, so the one thing native DnD offers that this
  // does not (a payload that crosses the window boundary) is the one thing a tile move never
  // wants, while its cost is the gesture not existing at all on a touch device. One
  // transport also means the mouse and the finger run the SAME code, instead of a path per
  // pointer type where only one of them is ever exercised.
  const zoneOf = (x: number, y: number) => zoneAt(rootEl.getBoundingClientRect(), x, y);
  const tileTarget = {
    transport: "emulated" as const,
    // A tile takes any point while a rearrange is in flight, EXCEPT when it is itself the
    // thing being dragged: a whole stack cannot land beside itself. A per-PANE self-drop is
    // deliberately not refused here — dropIdFor answers those by naming a pane that makes
    // the drop a no-op, and that is exactly what keeps "pull one of my own tabs onto my own
    // edge" working, which is the only way to take a stack apart.
    //
    // Nothing else can be mistaken for a rearrange: a file-row drag leaves `source` null,
    // so the tiles decline it and the walk carries it back to the file pane it belongs to.
    accepts: () => {
      const src = props.ctx.drag.source();
      return !!src && src !== props.node.id;
    },
    over: (p: DragPoint) => {
      const z = zoneOf(p.x, p.y);
      props.ctx.drag.over(dropIdFor(z), z);
    },
    leave: () => props.ctx.drag.leave(),
    drop: (p: DragPoint) => {
      const z = zoneOf(p.x, p.y);
      props.ctx.drag.drop(dropIdFor(z), z);
    },
  };

  // The source half. `begin` says what is travelling and `end` clears it whether or not a
  // tile took it. The payload is EMPTY on purpose: both ends of this drag are in one
  // component tree and the drag state already names the pane, so putting the id in a
  // transfer too would be a second copy of it, free to disagree with the first.
  //
  // The id is an ACCESSOR, read when the drag lifts and never at registration. A source is
  // registered once, in a ref, and this component outlives the node it was mounted for:
  // TreeNode's Switch re-creates a branch only when a node changes KIND, so a rearrange that
  // puts a different STACK at this position leaves the same StackView in place, now drawing
  // a tile whose id it never saw. A captured id then names the tile that used to be here —
  // which, after two tiles trade places, is the very tile being dropped ON, so source and
  // destination resolve to one stack and the move is correctly refused. Neither tile could
  // cross the other again.
  const tileDragSource = (id: () => string, kind: DragKind) => ({
    transport: "emulated" as const,
    // The bar and the tabs carry controls — ✕ on every tab, ⋮ at the end of the bar — and a
    // press on one of those means the button, not the tile behind it.
    ignore: "button",
    start: () => {
      props.ctx.drag.begin(id(), kind);
      return { data: {} };
    },
    end: () => props.ctx.drag.end(),
  });

  // TAP-TO-PICK. While the drag protocol is armed by something other than a pointer drag
  // — the pane menu's "Move pane…", or the "New pane…" command's placement — each
  // candidate tile offers its drop zones as real buttons. Discrete
  // buttons, not the geometric regions dropZone() reads for a mouse: on a phone a
  // two-way split is ~170px wide, so EDGE_BAND would make "left" and "right" about 51px
  // each — under a fingertip — and a tap, unlike a drag, has no hover preview to correct
  // against before it commits. A labelled button is also the only form that can carry an
  // accessible name and take focus.
  const picking = () => props.ctx.drag.picking() !== null;
  // The two CREATING picks — "New pane…" and "Split pane…" — are the same overlay asking a
  // different question: nothing is being taken from anywhere, so every tile is a candidate,
  // no zone is a self-drop, and the labels promise a new pane rather than a moved one. They
  // part company on one target: a split has no centre, because stacking a tab is not a
  // split.
  const placing = () => props.ctx.drag.picking() === "place";
  const splitting = () => props.ctx.drag.picking() === "split";
  // Whether THIS tile is worth offering as a target, mirroring dropIdFor's rules rather
  // than inventing new ones.
  const holdsSource = () => props.node.panes.some((p) => p.id === props.ctx.drag.source());
  // The pane chooser's preview, and whether it is pointing at this tile. While it is, the
  // tile wears a ring that is deliberately NOT the focus ring: the focused tile keeps its
  // own frame throughout, and seeing the two at once is what says the focus has not moved
  // yet. The mode is over the moment they coincide.
  const previewed = () => props.ctx.choose.selected();
  const choosing = () => previewed() !== null;
  const holdsPreview = () => {
    const id = previewed();
    return !!id && props.node.panes.some((p) => p.id === id);
  };
  // Whether the focused pane is one of this tile's. `focused` names a pane, not a tile, and
  // it is normally the tile's active tab — but a background tab can hold it, so membership
  // is the test rather than identity with activeId().
  const holdsFocus = () => props.node.panes.some((p) => p.id === props.ctx.focused());
  const offersTargets = () => {
    if (!picking()) return false;
    const src = props.ctx.drag.source();
    // A CREATING pick (place / split) has no source, and lights only the FOCUSED tile.
    // Every tile was a candidate at first — a new pane can go anywhere, so why not offer
    // everywhere — but on a workspace with a few tiles that is three or four identical
    // crosses at once, and the question "where does this go" turns into "which of these
    // twenty buttons". Creating is a thing you do where you are working; the wireframe is
    // there to name a DIRECTION, and the tile is already named by the focus.
    //
    // Moving keeps every candidate, and the difference is not an inconsistency: a move's
    // whole subject is a destination somewhere else, so its tiles are the answer rather
    // than scenery.
    if (!src) return holdsFocus();
    // A whole stack cannot be dropped on itself.
    if (props.ctx.drag.kind() === "stack" && src === props.node.id) return false;
    // A lone pane cannot move beside itself: its tile IS the pane.
    if (holdsSource() && props.node.panes.length === 1) return false;
    return true;
  };
  // The source's own tile keeps its four EDGES — pulling a tab out to one of them is how
  // a stack is taken apart, and the only way to do it without a drag — but loses its
  // centre: dropIdFor deliberately reports the self-collision there (the pane is already
  // a tab on this tile), so the button would be a visible no-op.
  // Two ways to lose the centre, for unrelated reasons: a SPLIT has no centre because
  // stacking a tab is not a split, and the source's own tile has none during a move because
  // the pane is already a tab there. Both leave the same four arms.
  const pickZones = (): DropZone[] => {
    const edges: DropZone[] = ["left", "right", "top", "bottom"];
    return splitting() || holdsSource() ? edges : ["stack", ...edges];
  };

  // What a zone PROMISES, which is the only thing that differs between the intents. Spelled
  // out per intent rather than parameterised over a verb: "Move here, to the left" and "New
  // pane, to the left" are not the same sentence with a word swapped, and the accessible
  // name is the whole affordance for anyone not seeing the wireframe.
  //
  // The split wording deliberately avoids the edge strips' "Split pane, new pane on the
  // left": those buttons are in the DOM at the same time, and two controls answering to one
  // accessible name is ambiguous to a screen reader and to `getByRole`.
  const zoneName = (z: DropZone) => {
    if (splitting()) return `Split this tile, new pane on the ${z}`;
    if (placing()) return z === "stack" ? "New tab here" : `New pane, to the ${z}`;
    return z === "stack" ? "Stack here as a tab" : `Move here, to the ${z}`;
  };
  const zoneHint = (z: DropZone) => {
    if (splitting()) return `Split — ${z}`;
    if (placing()) return z === "stack" ? "New tab here" : `New pane — ${z}`;
    return z === "stack" ? "Stack here as a tab" : `Move here — ${z}`;
  };

  // Hover intent for this tile's edge-split overlays (see SPLIT_ARM_DELAY_MS). armed()
  // names the one edge that is currently live; every other strip stays dark and
  // click-through. Visibility is driven by this signal rather than by CSS :hover because
  // the dwell ends while the pointer is STILL: a browser only recomputes hover state on
  // the next pointer move, so a :hover-revealed bar would stay hidden until the user
  // jiggled the mouse — i.e. never, for anyone who did the one thing the gesture asks.
  const [armed, setArmed] = createSignal<Side | null>(null);
  // The touch gesture's two extra states (see SPLIT_HOLD_MS). `charging` is the edge a
  // finger is currently holding down on, glowing its way up; `cooling` is an armed edge the
  // finger has let go of, glowing its way back down. Both are signals because both are drawn
  // — the whole gesture is legible only if the bar shows what stage it is at.
  const [charging, setCharging] = createSignal<Side | null>(null);
  const [cooling, setCooling] = createSignal(false);
  // Which gesture lit the bar. The mouse's armed strip is a real button — that is how a
  // click reaches it — while the touch one must stay click-through from beginning to end,
  // because it sits over the tab bar and the host handles its presses itself.
  const [byTouch, setByTouch] = createSignal(false);
  // How long the ramp or the fade currently running lasts, in ms. Rendered inline, so the
  // motion the eye follows and the timer the state is on are one number rather than two that
  // can drift; it is also what makes a partial re-heat legible, since both are recomputed
  // from the heat the bar is actually at.
  const [glowMs, setGlowMs] = createSignal(0);
  // How long the fade WAITS before it starts, which is where an overshoot is spent: the
  // visual has already peaked, so heat above the critical value buys time at full brightness
  // rather than more light. Zero for a charge and for any bar that never ran past 1.
  const [glowDelayMs, setGlowDelayMs] = createSignal(0);
  let armTimer: ReturnType<typeof setTimeout> | undefined;
  let coolTimer: ReturnType<typeof setTimeout> | undefined;
  let pending: Side | null = null; // the edge a running dwell is counting down for
  let rootEl!: HTMLDivElement;
  // The press feeding a charge: when it landed, and the heat it started from. Together they
  // say how hot the bar has become at any moment before its ramp finishes.
  let pressAt = 0;
  let chargeFrom = 0;
  let coolEndsAt = 0;
  // The edge the press in flight is on, and whether that bar was ALREADY spendable when the
  // finger landed. Recorded at the press and read at the release, because by then the
  // charge state has moved on — which is the whole point: a tap has to be a tap whether or
  // not the re-heat it also started had time to finish.
  let pressSide: Side | null = null;
  let pressLit = false;
  const disarm = () => {
    clearTimeout(armTimer);
    clearTimeout(coolTimer);
    armTimer = undefined;
    coolTimer = undefined;
    pending = null;
    pressSide = null;
    pressLit = false;
    setCharging(null);
    setCooling(false);
    setByTouch(false);
    setGlowMs(0);
    setGlowDelayMs(0);
    setArmed(null);
  };
  onCleanup(disarm);

  // HEAT is the fraction of the hot window still to run — 1 the moment a bar is charged, 0
  // when it goes out — and it is the one quantity the whole touch gesture is expressed in.
  // The bar's brightness is a deliberately NON-LINEAR read-out of it (see the stylesheet):
  // heat is time, and what the eye needs to know is not how much time has passed but whether
  // the moment to act has arrived. The two agree at both ends, which is what matters — a bar
  // is spendable exactly while it is visible.
  const heatNow = (): number => {
    if (charging()) return Math.min(SPLIT_HEAT_MAX, chargeFrom + (Date.now() - pressAt) / SPLIT_HOLD_MS);
    if (cooling()) return Math.max(0, (coolEndsAt - Date.now()) / SPLIT_HOT_MS);
    return armed() ? 1 : 0;
  };

  // Heating from wherever the bar already is, and NOT STOPPING at the point it becomes
  // spendable. The critical value is where the VISUAL peaks — the bar cannot get brighter
  // than lit — so everything a longer hold adds past it is stored rather than shown, and
  // comes back on release as time at full brightness. That is the whole of the model: one
  // accumulator, a rate up, a rate down, and a threshold that only decides what is spendable.
  //
  // A charge from cold takes the full SPLIT_HOLD_MS to reach that threshold, and one from
  // half-cooled takes half of it, so re-holding a bar that has barely faded is nearly instant
  // and rescuing one that is almost out costs what lighting it did.
  const beginCharge = (side: Side, from: number) => {
    clearTimeout(armTimer);
    clearTimeout(coolTimer);
    setCooling(false);
    setByTouch(true);
    setCharging(side);
    chargeFrom = from;
    // Only the climb to the climax is drawn; a bar already at or past it just stays lit.
    const toPeak = Math.max(0, 1 - from) * SPLIT_HOLD_MS;
    setGlowMs(Math.max(1, Math.round(toPeak)));
    setGlowDelayMs(0);
    if (from >= 1) return; // already spendable: there is no threshold left to cross
    armTimer = setTimeout(() => {
      armTimer = undefined;
      // A tab drag lifted out of this same press. That is the gesture the user is performing
      // and it wins — it cannot normally happen (SPLIT_HOLD_MS is the shorter timer) but the
      // two run independently and only one of them may own a press.
      if (dragging()) {
        disarm();
        return;
      }
      // The press is the strip's now, so whatever is still merely PENDING on it must not go
      // on to become a drag under the lit bar. `charging` deliberately stays set: the finger
      // is still down and the heat is still climbing, it is only no longer climbing towards
      // anything the eye can see.
      cancelPendingDrag();
      setArmed(side);
    }, toPeak);
  };

  // …and cooling from wherever it got to. The rate is one window per unit of heat WHATEVER
  // the starting point — that is what makes brightness a readout rather than a readout plus a
  // history — so the fade itself is at most one window long and any overshoot is spent in
  // front of it, at full brightness, as a delay.
  const beginCooling = (from: number) => {
    const shown = Math.min(from, 1);
    const fade = Math.max(1, Math.round(shown * SPLIT_HOT_MS));
    const banked = Math.max(0, Math.round((from - 1) * SPLIT_HOT_MS));
    setCooling(true);
    setGlowMs(fade);
    setGlowDelayMs(banked);
    coolEndsAt = Date.now() + fade + banked;
    clearTimeout(coolTimer);
    coolTimer = setTimeout(disarm, fade + banked);
  };

  const startDwell = (side: Side | null) => {
    if (!side) {
      disarm();
      return;
    }
    if (side === armed()) return; // already live on this edge
    // The dwell survives movement ALONG the edge — restarting the countdown on every move
    // would mean a slowly drifting pointer never arms — so a countdown already running for
    // this edge is left alone. Crossing to a different edge restarts it there.
    if (side === pending) return;
    clearTimeout(armTimer);
    setArmed(null);
    pending = side;
    armTimer = setTimeout(() => {
      armTimer = undefined;
      pending = null;
      setArmed(side);
    }, SPLIT_ARM_DELAY_MS);
  };

  // Everything below is driven from the WORKSPACE, not the tile, because the reach extends
  // past the tile's own box: a pointer resting in the gutter in front of an edge never
  // touches this tile at all. What the gutter belongs to is settled by the event target,
  // not by geometry — another tile or a resize divider owns its own pixels, and a strip
  // that armed over a divider would be a strip in front of the resize handle.
  const ownerOf = (e: Event) => (e.target as Element | null)?.closest(".stack, .divider") ?? null;
  const edgeUnder = (e: MouseEvent) => {
    const owner = ownerOf(e);
    if (owner && owner !== rootEl) return null; // another tile, or the divider between us
    return edgeAt(rootEl.getBoundingClientRect(), e.clientX, e.clientY, OUTSET_PX);
  };
  // Both halves of the edge-split reach stand down while ANY of the gestures below is in
  // flight.
  // Two actors on one tile would otherwise compete: a strip arming UNDER the overlay is a
  // live split hotzone the user cannot see, and the tap that chooses a zone would bubble to
  // the workspace and could complete an armed gutter split on its way out.
  //
  // The chooser was a hole here, and a quiet one. Its scrim (z-index 3) does not cover the
  // strips (5), and `edgeUnder` deliberately measures GEOMETRY rather than trusting the
  // event target — the reach extends past the tile into the gutter, where the target belongs
  // to nothing — so pointer moves over the scrim went on arming strips, and a click meant to
  // cancel the mode could split a tile instead.
  //
  // A DRAG counts too, and that one is new. The tab drag used to be HTML5 drag-and-drop,
  // which emits no pointer events at all, so a tab travelling across the workspace could
  // never feed this dwell. It is a pointer drag now (dnd.ts): every move reports, and a tab
  // carried near an edge and held there for the dwell would arm a strip UNDER the drag — a
  // purple bar promising a split in the middle of a rearrange, and then a release whose
  // click lands on the armed strip and splits the tile it just dropped into.
  const busy = () => picking() || choosing() || dragging();
  // A strip armed BEFORE a mode opened stays armed under it, and that one is not covered by
  // standing the dwell down: the bar is visible, it is above the scrim (5 vs 3), and its own
  // handler asks only whether it is armed — so the next click splits a tile instead of
  // answering the question on screen. Disarming as the mode opens is the fix at the source,
  // and it makes the two guards below belt and braces rather than the whole defence.
  createEffect(() => {
    if (busy()) disarm();
  });
  // A mouse hovers; a finger only reports while it is down, and then it is either charging a
  // strip or drifting off one. One listener, because both are "the pointer moved over this
  // tile" and splitting them would mean two places deciding what an edge is.
  const trackHoverIntent = (e: PointerEvent) => {
    if (e.pointerType === "mouse") {
      startDwell(busy() ? null : edgeUnder(e));
      return;
    }
    if (charging() && edgeUnder(e) !== charging()) disarm();
  };
  // The click half of the reach. Inside the tile the strip is a real button and handles
  // itself; out in the gutter there is nothing to click, so the tile completes the gesture
  // for the edge it has already armed — and only for that edge.
  const splitFromGutter = (e: MouseEvent) => {
    if (busy()) return;
    const side = armed();
    if (!side || ownerOf(e)) return;
    if (edgeAt(rootEl.getBoundingClientRect(), e.clientX, e.clientY, OUTSET_PX) !== side) return;
    e.stopPropagation();
    const edge = EDGE_BY_SIDE[side];
    props.ctx.splitAt(activeId(), edge.dir, edge.before);
    disarm();
  };

  const splitOn = (side: Side) => {
    const edge = EDGE_BY_SIDE[side];
    props.ctx.splitAt(activeId(), edge.dir, edge.before);
    disarm(); // the tile just resized under the pointer
  };

  // The touch gesture's press half (see SPLIT_HOLD_MS). CAPTURE phase, because its second
  // touch has to be claimed before anything under the bar reacts to it — the top strip lies
  // over the tab bar, and a tap that means "split here" must not also activate the tab it
  // happens to be over. The FIRST touch claims nothing: a short tap on the band has to go on
  // meaning whatever it meant before, so the strip only takes the press away once it has
  // held long enough to have said so.
  const onHostPress = (e: PointerEvent) => {
    if (e.pointerType === "mouse") return; // the mouse arms by hovering
    const side = busy() ? null : edgeUnder(e);
    if (!side) {
      // A touch anywhere else spends the glow: leaving a live bar behind the user's
      // attention is how a later, unrelated tap splits a tile out of nowhere.
      disarm();
      return;
    }
    // THE SECOND TOUCH on a lit bar, which can mean either of two things — and both begin
    // identically, with the bar brightening under the finger, so the feedback is honest
    // before the gesture has committed to being one or the other. Which it was is settled on
    // release: a TAP spends the bar, a HOLD feeds it. Deciding at the press instead would
    // make re-heating unreachable, since the split would already have happened.
    //
    // Not a click, either way. The strip is deliberately click-through for the whole touch
    // gesture (unlike the mouse's armed strip, which is a real button), so a bar lit over the
    // tab bar never swallows a press it was not offered.
    if (side === armed()) {
      e.stopPropagation();
      e.preventDefault();
      pressAt = Date.now();
      pressSide = side;
      pressLit = true;
      beginCharge(side, heatNow());
      return;
    }
    disarm(); // clears pressSide, so the two lines after it are the order that matters
    pressAt = Date.now();
    pressSide = side;
    pressLit = false;
    beginCharge(side, 0);
  };

  // Letting go. A first charge abandoned before it finished is simply dropped; a lit bar is
  // spent by a tap and kept alive by anything longer; and a bar whose charge completed under
  // the finger starts cooling the moment it is released, which is what makes the fade read
  // as "your time starts now".
  const onHostRelease = () => {
    const side = pressSide;
    if (!side) return; // a release from a press that was never one of ours
    pressSide = null;
    const held = Date.now() - pressAt;
    const reached = heatNow();
    clearTimeout(armTimer);
    armTimer = undefined;
    setCharging(null);

    // A TAP on a bar that was ALREADY lit spends it. Read off `pressLit` and the duration
    // alone — deliberately NOT off whether the re-heat this press also started has finished,
    // which was the first version of this and was wrong in the commonest case there is: on a
    // bar that is still bright the ramp is a handful of milliseconds (1ms at full heat, 18ms
    // a twentieth of the way down), so every real tap outlived it and the bar re-heated
    // instead of splitting. The gesture would have looked broken exactly when it was aimed
    // most confidently.
    if (pressLit && held < SPLIT_TAP_MS) {
      splitOn(side);
      return;
    }
    // A charge let go of before it ever crossed the threshold never happened at all.
    if (armed() !== side) {
      disarm();
      return;
    }
    // Anything else was a hold: the bar keeps what it gained and starts cooling from there.
    beginCooling(reached);
  };

  // The tile's ⋮ opens the COMMAND PALETTE, seeded with the `pane` tag, rather than a
  // menu of its own. The two used to hold the same list twice — a command and a modal
  // entry per operation, drifting apart at every change — and the palette version is the
  // better one anyway: searchable, keyboard-driven, and showing each command's tmux bind
  // next to it, which is how anyone learns the binds exist. The seed is a starting point,
  // not a cage: deleting it widens to every command on the screen.
  //
  // The palette acts on the FOCUSED pane, so pressing ⋮ must make this tile the focused
  // one. Not left to the click: a <button> press does not move focus in every browser
  // (Safari notably), and "close focused pane" reading the wrong tile is not a defect
  // anyone would forgive.
  const openPaneCommands = () => {
    disarm(); // a menu opened by tap must not leave a dwell counting down behind it
    props.ctx.setFocus(activeId());
    openPalette(`:${PANE_TAG} `);
  };

  onMount(() => {
    const host = rootEl.closest(".workspace") ?? rootEl;
    host.addEventListener("pointermove", trackHoverIntent as EventListener);
    host.addEventListener("pointerleave", disarm);
    host.addEventListener("click", splitFromGutter as EventListener);
    host.addEventListener("pointerdown", onHostPress as EventListener, true);
    host.addEventListener("pointerup", onHostRelease);
    // A stolen pointer never sends pointerup, and a charge left counting down behind it
    // would light a bar for a finger that is no longer there.
    host.addEventListener("pointercancel", onHostRelease);
    onCleanup(() => {
      host.removeEventListener("pointermove", trackHoverIntent as EventListener);
      host.removeEventListener("pointerleave", disarm);
      host.removeEventListener("click", splitFromGutter as EventListener);
      host.removeEventListener("pointerdown", onHostPress as EventListener, true);
      host.removeEventListener("pointerup", onHostRelease);
      host.removeEventListener("pointercancel", onHostRelease);
    });
  });

  return (
    <div
      ref={(el) => {
        rootEl = el;
        onCleanup(dropTarget(el, tileTarget));
      }}
      class="stack"
      // The tile's id, in the DOM. Two things outside the tiling need to find a TILE's element
      // and neither can be given a ref: the scroll anchor (views/tiling/anchor.ts) re-queries
      // after the mutation precisely because a re-tile rebuilt the element, and the reveal
      // (views/tiling/reveal.ts) is driven from an effect on the focused PANE, which does not
      // hold its tile's node. A stack id is stable across a split — splitPane wraps the tile
      // rather than replacing it — which is what makes it usable as an anchor at all.
      data-stack-id={props.node.id}
      classList={{
        focused: focused(),
        "drop-target": isDropTarget(),
        previewed: holdsPreview(),
        dragging: props.ctx.drag.source() === props.node.id,
      }}
      // DOM focus DRIVES tile focus, so the two can never disagree. A pointer press on
      // the body already focused the tile, but focus also moves without one — Tab into a
      // row, a link or button in the chrome, a pane that focuses itself on mount — and a
      // tile that holds the keyboard while another one is "focused" sends every
      // contextual command to the wrong pane. focusin (not focus) because it bubbles,
      // and on the whole tile so the tab bar and sub-header count as this tile too.
      onFocusIn={() => props.ctx.setFocus(activeId())}
    >
      {/* The tab bar is the drag handle for the WHOLE stack: dragging its empty area (not a
          tab) moves/stacks every tab together. A press that lands on a tab is that tab's
          own drag — `ignore` says so, and the press machinery would rule it out anyway,
          since the tab's handler sees the press first and one press is one drag. */}
      <div
        class="stack-tabs"
        classList={{ solo: props.node.panes.length === 1 }}
        role="tablist"
        ref={(el) =>
          onCleanup(
            dragSource(el, { ...tileDragSource(() => props.node.id, "stack"), ignore: "button, .tab" }),
          )
        }
      >
        <For each={props.node.panes}>
          {(pane, i) => (
            <div
              class="tab"
              data-pane-id={pane.id}
              role="tab"
              classList={{
                active: i() === props.node.active,
                // The chooser can preview a BACKGROUND tab, which the tile's ring alone
                // cannot say: the ring would frame a tile while pointing at something the
                // tile is not currently showing.
                previewed: previewed() === pane.id,
                dragging: props.ctx.drag.source() === pane.id,
              }}
              ref={(el) => onCleanup(dragSource(el, tileDragSource(() => pane.id, "pane")))}
              onPointerDown={() => props.ctx.activate(pane.id)}
              onClick={() => props.ctx.activate(pane.id)}
            >
              {/* The pane's number, on its tab, ALWAYS — not only while the chooser is up.
                  It is the tab's standing identity: `prefix s` then that digit goes straight
                  there, so the number has to be readable BEFORE the mode is entered, or the
                  jump is something you can only do once you are already looking at the list.
                  It is also the one place a BACKGROUND tab's number can appear; the plate in
                  the body speaks only for the tab its tile is showing.

                  Switchable, unlike the list and the plate: those two ARE the mode (a list of
                  identical rows with nothing tying them to the screen is no chooser), while
                  this one lives in a bar the user reads all day. Which is also why the height
                  rule in styles.css matters more than it looks — a badge that is always there
                  must cost the bar nothing. */}
              <Show when={settings().paneNumbersInTabs}>
                <span
                  class="pane-number pane-number-tab"
                  classList={{ current: pane.id === props.ctx.focused() }}
                  aria-hidden="true"
                >
                  {props.ctx.choose.numberOf(pane.id)}
                </span>
              </Show>
              <span class="tab-label">{props.ctx.tabTitle(pane)}</span>
              <button
                class="tab-close"
                title="Close tab"
                aria-label="Close tab"
                onClick={(e) => {
                  e.stopPropagation();
                  props.ctx.closePane(pane.id);
                }}
              >
                ✕
              </button>
            </div>
          )}
        </For>
        <span class="stack-tabs-spacer" />
        <Show when={props.ctx.headerExtra && activePane()}>{props.ctx.headerExtra!(activePane())}</Show>
        {/* Last in the bar, after whatever the host contributes, so it sits in the same
            place on every screen. It used to need `draggable` and a dragstart that cancelled
            itself, because a native drag begun on a non-draggable child is attributed to the
            nearest draggable ancestor — the whole-stack handle — and a press-drag from the ⋮
            picked up the entire tile. The pointer transport has no such rule: the bar's
            source simply declines a press that lands on a button (`ignore`). */}
        <button
          type="button"
          class="pane-menu"
          aria-label="Pane menu"
          title="Pane commands"
          onClick={(e) => {
            e.stopPropagation();
            openPaneCommands();
          }}
        >
          ⋮
        </button>
      </div>

      <Show when={props.ctx.subHeader?.(activePane())}>
        {(content) => <div class="stack-subheader">{content()}</div>}
      </Show>

      <div class="stack-body" onPointerDown={() => props.ctx.setFocus(activeId())}>
        <For each={props.node.panes}>
          {(pane, i) => (
            <div
              class="stack-pane"
              classList={{ active: i() === props.node.active }}
              style={{ display: i() === props.node.active ? undefined : "none" }}
            >
              {props.ctx.body(pane)}
            </div>
          )}
        </For>
      </div>

      {/* The split-edge overlays and the drop indicator sit on the TILE edges (relative to
          .stack), so the top zone hugs the tile's top edge rather than the body's — the
          split it previews divides the whole tile, and that is the edge it belongs to. It
          therefore lies over the tab bar, which is safe only because an un-armed strip is
          click-through: the tabs, and each tab's ✕, take every click until the pointer has
          deliberately rested on the strip. */}
      <For each={SPLIT_EDGES}>
        {(edge) => (
          <button
            type="button"
            tabindex={-1}
            class={`pane-split-zone edge-${edge.side}`}
            classList={{
              armed: armed() === edge.side,
              charging: charging() === edge.side,
              cooling: cooling() && armed() === edge.side,
            }}
            // Inline, not in styles.css: the click-through invariant then reads off the
            // same signal that gates the handler (and is observable to a test, which has
            // no stylesheet), and the strip's GEOMETRY is the same pair of numbers edgeAt()
            // arms on, so the live area and the visible bar cannot disagree. The two
            // durations are here for the same reason — the glow's ramp and its fade are what
            // the timers below are counting, and a stylesheet copy of either would be a
            // second deadline drifting away from the real one.
            style={{
              // Clickable only under the MOUSE's armed state. The touch gesture keeps the
              // strip click-through from beginning to end and answers its presses at the
              // host instead (see onHostPress), so a bar lit over the tab bar never swallows
              // one — and the release that ends a charge cannot spend the bar by accident.
              "pointer-events": armed() === edge.side && !byTouch() ? "auto" : "none",
              [edge.dir === "v" ? "height" : "width"]: `${edge.px}px`,
              ...(edge.dir === "v"
                ? { left: `${EDGE_INSET * 100}%`, right: `${EDGE_INSET * 100}%` }
                : { top: `${EDGE_INSET * 100}%`, bottom: `${EDGE_INSET * 100}%` }),
              // One duration for both directions of travel, because both are the same
              // opacity transition: the ramp when charging, the fade when cooling. A
              // transition (not an animation) is also what makes a re-heat pick up from the
              // brightness the bar is CURRENTLY showing — the browser interpolates from the
              // computed value it is passing through, so an interrupted fade reverses in
              // place instead of jumping to full and starting again.
              ...(glowMs() && (charging() === edge.side || (cooling() && armed() === edge.side))
                ? { "transition-duration": `${glowMs()}ms`, "transition-delay": `${glowDelayMs()}ms` }
                : {}),
            }}
            aria-label={`Split pane, new pane on the ${edge.side}`}
            title={`Split — new pane on the ${edge.side}`}
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              // Unreachable in a browser while un-armed (pointer-events: none hands the
              // click to whatever is underneath); the guard keeps the gate real for
              // anything that dispatches straight at the button.
              if (armed() !== edge.side) return;
              e.stopPropagation();
              splitOn(edge.side);
            }}
          >
            <span class="pane-split-bar" />
          </button>
        )}
      </For>
      {/* tmux's display-panes, in the one place it can be read from across the screen. It
          names the pane the tile is SHOWING — a tile can hold several, and the others wear
          their numbers on their tabs — and it is `aria-hidden` because the list already
          reads each number out beside the label it belongs to. Click-through, so the scrim
          underneath still takes the tap that means "never mind". */}
      <Show when={choosing()}>
        <div class="pane-number-plate" aria-hidden="true">
          <span
            class="pane-number pane-number-big"
            classList={{ current: activeId() === props.ctx.focused() }}
          >
            {props.ctx.choose.numberOf(activeId())}
          </span>
        </div>
      </Show>
      {/* The tap-to-pick targets. Rendered after the split strips so they win the same
          pixels, and the strips are stood down while picking anyway. The five are laid
          out as grid cells of one centred plus (styles.css), so they cannot overlap and
          their DOM order carries no meaning — unlike the drop indicator they replace,
          whose "stack" zone is inset:0 and would have covered the other four. */}
      <Show when={offersTargets()}>
        {/* The overlay, not any target inside it, is what the keyboard focuses: a direction
            key acts on the whole tile, so the tile is the unit that can be "current". The
            targets therefore leave the tab order — they are pointer affordances and the
            names of the five directions, not five stops to walk through. */}
        <div
          class="pane-pick-overlay"
          tabindex={-1}
          role="group"
          aria-label={
            splitting()
              ? "Split this tile"
              : placing()
                ? "Place the new pane on this tile"
                : "Move the pane to this tile"
          }
        >
          <For each={pickZones()}>
            {(z) => (
              <button
                type="button"
                tabindex={-1}
                class={`pane-pick-zone zone-${z}`}
                // Hover lights the existing .pane-drop-indicator, which is the preview a
                // drag gets for free. There is no focus counterpart because there is
                // nothing to preview from the keyboard: a direction key is the answer, not
                // a selection waiting to be confirmed.
                onPointerEnter={() => props.ctx.drag.over(dropIdFor(z), z)}
                aria-label={zoneName(z)}
                title={zoneHint(z)}
                onClick={(e) => {
                  e.stopPropagation();
                  props.ctx.drag.drop(dropIdFor(z), z);
                }}
              >
                <span class="pane-pick-glyph" aria-hidden="true">
                  {z === "stack" ? "⊞" : PICK_ARROW[z]}
                </span>
              </button>
            )}
          </For>
        </div>
      </Show>
      <Show when={isDropTarget()}>
        <div class={`pane-drop-indicator zone-${zone()}`}>
          {/* The filled region previews every intent — a move and a placement both light
              the half they would occupy — but the LABEL is for the pointer path only. Both
              it and the pick cluster centre themselves on the region they describe, so
              during a pick they claim the same pixels and the text renders straight
              through the middle button. A label is what names a region you are merely
              hovering; a pick already has a named button under the finger. */}
          <Show when={!picking()}>
            <span class="pane-drop-label">{zone() === "stack" ? "⊞ Stack (tab)" : "Move here"}</span>
          </Show>
        </div>
      </Show>
    </div>
  );
}
