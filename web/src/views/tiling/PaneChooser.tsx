import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from "solid-js";
import type { Accessor, JSX } from "solid-js";
import { chooserAnchor, chooserGutter, pinnable, type Gutter } from "./gutter";
import { allPanes, allStacks, stackOf, tileRects, type Ext, type Node, type Pane } from "./layout";
import { NO_METRICS, bulletRoom, bulletWindow, mapBox, metricsOf, viewportRect } from "./minimap";
import { CTRL_SIDE, KEY_SIDE, type TileCtx } from "./panes";
import { revealRow } from "./reveal";
import { setPaneChooserPinned, settings } from "../../settings";

// The pane chooser's chrome: the list, and the keyboard that drives the walk. The state it
// reads and writes is ./choose; this is only what that mode looks like.
//
// Rendered once per workspace by the host, inside .workspace. It has two lifetimes, and which
// one it has is the pin:
//
//   FLOATING (the default) — mounted only while the mode is up, anchored over the top corner
//   of the tiles, gone on Enter or Escape. A mode with a readout.
//
//   PINNED — mounted for as long as the setting is on, standing in a gutter the workspace has
//   given up the width for (see ./gutter). Then it is not a mode at all between arms: it lists
//   the panes, marks the one holding the focus, and a click on a row goes there. `prefix s`
//   still arms the walk, and this same panel is what the walk reports into.
//
// ARMED is therefore the distinction almost everything below turns on, and it is NOT the same
// question as "is this component mounted" any more. The mode's keyboard, its scrim, its focus
// grab and its refusals all belong to the armed state alone: a standing panel that swallowed
// Escape, or held a scrim over the workspace, would have taken the screen hostage to show a
// list. The app owns a capture-phase document keydown for the tmux prefix, and that one stands
// aside for anything inside .xterm so a live shell keeps its own keys, arrows and Escape
// included. Taking those keys for the duration of a mode the user explicitly entered is
// intended; taking them the rest of the time is exactly what pinning must not do.

export function PaneChooser<P>(props: {
  ctx: TileCtx<P>;
  tree: Accessor<Node<P>>;
  // The workspace's scroll container and its extent — everything the mini map needs to say
  // where the screen is. Handed down rather than read out of the DOM here: the host owns that
  // element, and it already hands out pixel measurements of it for the same reason (see
  // TileCtx.resizeExtend).
  scroller: () => HTMLElement | null;
  ext: Accessor<Ext>;
}): JSX.Element {
  const gutter = () => chooserGutter();
  const armed = () => props.ctx.choose.selected() !== null;
  // A question a CALLER brought — "Copy 3 items to…" — does not go in the gutter, even though
  // it is the same walk over the same panes. The gutter is furniture: it is the plain question
  // asked permanently, and a user reads it while working. Putting a modal question in it
  // replaces the thing being read with something that has to be answered, and then puts it
  // back, in the one part of the screen that was supposed to stay still. So the gutter keeps
  // standing and the question gets a card of its own.
  //
  // Unpinned there is no such conflict and nothing changes: one panel, every purpose.
  const card = () => armed() && !(gutter() !== null && props.ctx.choose.plain());
  // The card hangs OPPOSITE the gutter when there is one. Sharing a side would put it on top of
  // the panel it was split out of, which is most of the way back to sharing it.
  const cardSide = (): Gutter => {
    const g = gutter();
    return g ? (g === "left" ? "right" : "left") : chooserAnchor();
  };

  return (
    <>
      <Show when={gutter()}>
        {(g) => (
          <ChooserPanel
            ctx={props.ctx}
            tree={props.tree}
            scroller={props.scroller}
            ext={props.ext}
            pinned
            side={g()}
          />
        )}
      </Show>
      <Show when={card()}>
        <ChooserPanel
          ctx={props.ctx}
          tree={props.tree}
          scroller={props.scroller}
          ext={props.ext}
          pinned={false}
          side={cardSide()}
        />
      </Show>
    </>
  );
}

// ONE PANEL, in either of the two roles. The gutter and the card are the same list, the same
// map and the same keys; what differs is whether the walk belongs to this one (`owns` below)
// and where it sits. Written as one component rather than two so that a change to the list can
// only ever be made to both.
function ChooserPanel<P>(props: {
  ctx: TileCtx<P>;
  tree: Accessor<Node<P>>;
  scroller: () => HTMLElement | null;
  ext: Accessor<Ext>;
  // Whether this panel IS the gutter — a standing list the workspace made room for — or a card
  // floating over the tiles for the length of a mode.
  pinned: boolean;
  side: Gutter;
}): JSX.Element {
  let panel!: HTMLDivElement;
  // Where focus was when the mode armed, restored only if it is CANCELLED. A commit moves
  // the focus to the pane that was chosen — that is the whole point of the mode — and
  // pulling it back would undo the one thing it does.
  let prevFocus: HTMLElement | null = null;

  const choose = () => props.ctx.choose;
  const selected = () => props.ctx.choose.selected();
  // Whether a walk is up AND IS THIS PANEL'S. The one question the two roles disagree about:
  // a card exists only while it is showing one, and the gutter shows only the plain question —
  // a caller's goes to a card beside it, which is then the panel that owns it.
  const armed = () => selected() !== null && (!props.pinned || choose().plain());
  const pinned = () => props.pinned;
  const side = () => props.side;
  // A walk that belongs to somebody else. Only the gutter can see one, and when it does it is
  // scenery rather than a control — see `inert` on the panel below.
  const otherWalk = () => selected() !== null && !armed();
  // Whether this panel is asking a question a CALLER brought — "Copy 3 items to…", "Follow this
  // terminal in…" — rather than the mode's own "which pane do I want to be in?".
  //
  // Such a panel is a LIST OF DESTINATIONS and nothing else. It drops the mini map, because the
  // map answers "where is that pane", which is a navigation question: a transfer picks its
  // destination by name, by kind and by which rows are greyed, and half of them usually are.
  // (The refusals themselves stay — they are on the rows, which is where the reasons are.) It
  // drops the mark that says where the FOCUS is, because a transfer does not move the focus —
  // that is why `pick` is a callback and not a flag — so the mark reports a fact no answer to
  // this question can change. And it drops the pin, which makes furniture: a modal question is
  // not the place to be offered a permanent panel.
  //
  // It also strips the rows of their STATE BADGES — "working", "needs you", "following" — by
  // asking the host for a plain label (see TileCtx.tabTitle). Those say what a pane is doing,
  // which is exactly right on a tab, where the pane is standing for itself; here every row is
  // a place something is being sent TO, and what a pane happens to be doing is neither a
  // reason to pick it nor a reason not to. The reasons not to are the refusals, which is why
  // those stay: they are answers to THIS question and the badges are answers to another one.
  // A row that carries both invites the badge to be read as a refusal it is not — an amber
  // "needs you" beside a destination reads as a warning about sending there.
  // A MEMO rather than a derivation, unlike everything else here, because one of its readers
  // is per-row and rebuilds DOM. `armed()` reads `selected()`, which changes on every step of
  // a walk while this boolean stays put, and a plain derivation would hand each of those steps
  // to every label in the list to be torn down and built again.
  const asking = createMemo(() => armed() && !choose().plain());
  // Row ids have to name the PANEL as well as the pane, because both panels can be on screen
  // at once listing the same panes. Without this the gutter and the card mint the same ids,
  // `aria-activedescendant` on the focused card resolves to the first match in document order
  // — a row in the gutter — and a screen reader is walked through the wrong panel. Duplicate
  // ids are also simply invalid, and nothing in the DOM complains.
  const rowId = (paneId: string) => `pane-choice-${props.pinned ? "gutter" : "card"}-${paneId}`;
  // Why a pane is not an answer to THIS question, or undefined when it is one. Read during
  // render, so it tracks whatever the purpose reads (the tree, a pane's path, the source of
  // a transfer) and a row greys out the moment its reason becomes true.
  //
  // Nothing is refused while nothing is armed. `purpose` keeps whatever it was last asked —
  // the mode ends by clearing `selected`, not by forgetting the question — so a pinned panel
  // reading it unconditionally would go on greying rows for a transfer that finished minutes
  // ago, and a click on one would do nothing with no way to find out why.
  const refused = (id: string) => (armed() ? choose().purpose().refuse?.(id) : undefined);
  const elsewhere = () => (armed() ? choose().purpose().elsewhere : undefined);
  const tiles = () => allStacks(props.tree());
  // What the panel calls itself. A purpose's title is a QUESTION ("Copy 3 items to…"), which
  // only makes sense while it is being asked; between arms the panel is a standing list and
  // says what it is a list of.
  const title = () => (armed() ? choose().purpose().title : "Panes");
  // The row wearing the highlight: the WALK while one is up, and otherwise the pane the focus
  // is actually in. Idle, "where am I" is the only thing a list of panes has to say, and the
  // mark that says it is the one the walk borrows the moment the mode arms — so arming moves
  // the highlight off the focused row rather than making a second kind of mark appear.
  const marked = () => selected() ?? props.ctx.focused();
  // What pressing a row means. Armed, it answers the question — which may be refused, and may
  // not be a focus change at all. Idle, there is no question outstanding, so it is what
  // pointing at a pane means everywhere else in the workspace: go there.
  const pick = (id: string) => (armed() ? choose().commit(id) : props.ctx.activate(id));
  const markedTile = () => stackOf(props.tree(), marked());
  // Whether a direction can lead anywhere, and whether Tab can. Both gate their own line of
  // the hint: naming an inert key is worse than naming none, because the reader tries it,
  // nothing happens, and the whole line is then suspect.
  const manyTiles = () => tiles().length > 1;
  // Tab walks the whole list, so what makes it worth naming is a second PANE anywhere —
  // not a second tab on the tile the walk is standing on. Those are different questions on
  // any layout with more tiles than tabs, which is most of them.
  const manyPanes = () => allPanes(props.tree()).length > 1;
  const jumpMax = () => Math.min(9, allPanes(props.tree()).length);

  const cancel = () => {
    choose().cancel();
    if (prevFocus?.isConnected) prevFocus.focus();
  };

  // The panel takes focus so that stray keys — everything this handler does not claim — land
  // on it rather than in the shell the user is looking at. It is the mode's one focusable
  // element and it holds aria-activedescendant, which is what a screen reader follows as the
  // walk moves; the rows themselves are not tab stops, because the arrows do not walk them.
  //
  // On ARMING rather than on mounting, which for the floating panel is the same instant and
  // for the pinned one is not. A panel that grabbed the focus when it appeared would take it
  // out of a live shell every time the setting was switched on, and again on every reload.
  createEffect(
    on(armed, (now, before) => {
      if (!now || before) return;
      prevFocus = document.activeElement as HTMLElement | null;
      panel.focus();
    }),
  );

  // …and the list scrolls itself to wherever the mark went. On `marked` rather than on
  // `selected`, so it covers both of the mark's meanings: a walk in progress, and — for a pinned
  // panel between walks — the row for the pane the focus is in, which should be visible when the
  // panel is glanced at rather than found by scrolling.
  //
  // Undeferred and unguarded on `armed`: `revealRow` is a no-op for a row already fully visible,
  // so this costs nothing on the short lists where the panel holds every pane at once, and there
  // is no browser scroll to sequence against (see ./reveal).
  createEffect(
    on(marked, (id) => {
      if (!id) return;
      revealRow(panel.querySelector<HTMLElement>(`#${CSS.escape(rowId(id))}`));
    }),
  );

  // The mode's keyboard, registered only while a walk is up and removed with it. This is the
  // whole of what keeps a pinned panel from being a keyboard trap: every branch below either
  // preventDefaults a key or hands it back, and a standing panel that ran this would eat
  // Escape, Enter, Tab, the digits and hjkl from a shell that is not even highlighted.
  createEffect(() => {
    if (!armed()) return;
    const onKey = (e: KeyboardEvent) => {
      const stop = () => {
        e.preventDefault();
        e.stopPropagation();
      };
      if (e.key === "Escape") {
        stop();
        cancel();
        return;
      }
      if (e.key === "Enter") {
        stop();
        choose().commit();
        return;
      }
      if (e.key === "Tab") {
        // Trapped whether or not it does anything here: the mode covers the workspace, and
        // Tab without this leaves for the nav behind it. What it does is walk the LIST —
        // every pane in the workspace, in the order the rows are in — which is both the one
        // move no direction can express (stacked tabs share a place on screen, so no arrow
        // tells them apart) and the plain "next thing down" a list on screen invites.
        stop();
        choose().cyclePane(e.shiftKey ? -1 : 1);
        return;
      }
      // A letter only counts bare; the same letter under Ctrl is a different binding, and
      // under Alt/Meta it belongs to the OS or the browser.
      if (e.altKey || e.metaKey) return;
      // A digit goes straight to the pane wearing it — the point of putting numbers on the
      // tiles at all, and tmux's own display-panes gesture. Bare only: Ctrl+1..9 switches
      // browser tabs and is not ours to take. Past nine there is no key left, so those panes
      // keep their number (it is how a row and a tile are matched up) and are reached by the
      // arrows; the hint says which range is live.
      if (!e.ctrlKey && /^[1-9]$/.test(e.key)) {
        stop();
        choose().jump(Number(e.key));
        return;
      }
      const named = e.key.length === 1 ? e.key.toLowerCase() : e.key;
      // When this purpose has somewhere to go that is not a pane, EVERY bare letter takes
      // it, and the letter is passed on as the first character of what it asks for. That
      // costs the mode `hjkl`, which is why the hint below stops advertising them: the
      // arrows still walk, and a destination beginning with h, j, k or l stays typeable.
      // Ctrl is excluded so CTRL_SIDE's emacs-shaped moves survive.
      if (elsewhere() && !e.ctrlKey && /^[a-z]$/.test(named)) {
        stop();
        choose().takeElsewhere(e.key);
        return;
      }
      const side = e.ctrlKey ? CTRL_SIDE[named] : KEY_SIDE[named];
      if (!side) return;
      stop();
      choose().move(side);
    };
    window.addEventListener("keydown", onKey, true);
    onCleanup(() => window.removeEventListener("keydown", onKey, true));
  });

  return (
    <>
      {/* The workspace is inert for the duration OF A WALK. Without this a click could move
          the focus — the one thing the mode promises not to do until Enter — and the promise
          is worth more than the click: the panes are all still visible, and every one of them
          is one keystroke away. Anywhere else means "never mind".

          Gone between walks, which is what makes a pinned panel furniture rather than a modal
          that never closes: the workspace beside it stays live, and the list is something you
          glance at while working rather than something you must dismiss first. */}
      <Show when={armed()}>
        <div class="pane-chooser-scrim" onClick={cancel} />
      </Show>
      <div
        ref={panel}
        class="pane-chooser"
        classList={{
          pinned: pinned(),
          // The side it is on, as a class rather than a logical CSS property. `inset-inline-start`
          // would follow the document's COMPUTED direction, and that is not the same question:
          // the app never sets `dir`, so a browser set to Arabic renders ltr and the panel would
          // sit opposite the gutter the same preference puts on the right. One rule decides both,
          // and it is the one in ./gutter.
          left: side() === "left",
          right: side() === "right",
          // INERT while ANOTHER MODE owns the screen — a placement, or a walk that belongs to
          // the card beside it. Both draw a scrim meaning "anywhere but a target is never
          // mind", and both were written when the panel could not be on screen unless its own
          // mode was up. Pinned it is always there, so it would sit above that scrim offering
          // rows that move the focus while the workspace is waiting to be told something else.
          inert: pinned() && (!!props.ctx.drag.picking() || otherWalk()),
        }}
        tabindex={-1}
        role="listbox"
        // The title IS the accessible name: the mode is reused for questions other than
        // "which pane do I want to be in?", and a listbox permanently labelled with the
        // first of them would announce the wrong one for all the rest.
        aria-label={title()}
        // Only while a walk is up AND IS THIS PANEL'S, because only then does this element
        // hold the focus. aria-activedescendant on an unfocused container points a reader at a
        // row nothing is on; the standing panel's answer to "where am I" is the selected row
        // itself. `armed()` and not `selected()`: with a caller's question up in the card
        // beside it, the gutter would otherwise name a row that is being walked in a different
        // panel — and the id it names is its OWN row, so a reader would be told the focus is
        // somewhere it is not.
        aria-activedescendant={armed() && selected() ? rowId(selected()!) : undefined}
      >
        {/* The title and the pin, on one line. A flex row rather than the pin absolutely
            placed over the corner: the titles are questions of unbounded length ("Copy 3 items
            to…") and a control floating over the end of one would sooner or later sit on top
            of a word. */}
        <div class="pane-chooser-head">
          <p class="pane-chooser-title">{title()}</p>
          {/* THE PIN. Offered only where the gutter is affordable — a wide window with a mouse
              (see ./gutter) — because a control that appears everywhere and works in one place
              is worse than one that is honestly absent.

              Faded until it is on. It is a toggle whose OFF state is the app's normal state,
              so the un-pinned form has to be quiet enough to sit in the corner of a panel
              nobody opened to look at it, and the pinned form has to be unmistakable — it is
              the only thing on screen that says why a strip of the workspace has gone. */}
          <Show when={pinnable() && !asking()}>
            <button
              type="button"
              class="pane-chooser-pin"
              classList={{ pinned: pinned() }}
              aria-pressed={pinned()}
              aria-label={pinned() ? "Unpin the pane chooser" : "Pin the pane chooser"}
              title={pinned() ? "Unpin the pane chooser" : "Pin the pane chooser to a gutter"}
              onClick={() => {
                setPaneChooserPinned(!pinned());
                // Focus stays on the panel while a walk is up: it holds aria-activedescendant,
                // and pinning mid-walk must not end the walk or hand the keys back. Guarded on
                // isConnected because unpinning an IDLE panel unmounts this very element —
                // there is no mode to keep, and the workspace should have its focus back.
                if (armed() && panel.isConnected) panel.focus();
              }}
            >
              <span aria-hidden="true">📌</span>
            </button>
          </Show>
        </div>
        {/* Opt-in (Settings -> Workspace -> Mini map), and then only where there is a map to
            draw: one tile would be a single rectangle filling the box, saying nothing the one
            row below it does not already say. Read here rather than inside MiniMap so the
            component is never mounted when it is off — its scroll listener goes with it. */}
        <Show when={settings().paneMiniMap && manyTiles() && !asking()}>
          <MiniMap
            ctx={props.ctx}
            tree={props.tree}
            scroller={props.scroller}
            ext={props.ext}
            marked={marked}
            pick={pick}
          />
        </Show>
        {/* The rows scroll; the title, the map and the key hint do not. With the map above it
            the list is no longer the whole panel, and a map that scrolled out of the top of
            its own panel would be a picture of the workspace you have to go looking for. */}
        <div class="pane-chooser-list">
          <For each={tiles()}>
            {(tile, i) => (
              <div
                class="pane-chooser-tile"
                classList={{ current: tile.id === markedTile()?.id }}
                role="group"
                aria-label={`Tile ${i() + 1}`}
              >
                <For each={tile.panes}>
                  {(pane: Pane<P>) => (
                    <div
                      id={rowId(pane.id)}
                      class="pane-chooser-item"
                      classList={{ selected: pane.id === marked(), disabled: !!refused(pane.id) }}
                      role="option"
                      aria-disabled={refused(pane.id) ? true : undefined}
                      // The mode's two states, and ARIA happens to have a word for each:
                      // aria-selected is where the WALK is, aria-current is where the focus
                      // still is. Both are on screen at once, so a reader that announced only
                      // one of them would be describing a different mode. Between walks they
                      // name the same row, which is the honest reading of a list that is not
                      // being walked: the selection IS where you are.
                      aria-selected={pane.id === marked()}
                      aria-current={
                        !asking() && pane.id === props.ctx.focused() ? "true" : undefined
                      }
                      // A pointer names its own row, so it needs no walk: one press previews
                      // and commits in the same gesture, which is what a click on a list of
                      // places to go already means everywhere else.
                      onClick={() => pick(pane.id)}
                    >
                      {/* The number is the row's link to a place on screen, so it is read out
                          with the label rather than hidden: "3, project" is how you would say
                          it aloud. Its twins on the tab and the tile are the decorative ones.

                          It also carries WHERE THE FOCUS IS, by filling in. That used to be a
                          separate ● in a column of its own, which cost every row a column of
                          blank space to keep the labels aligned, and put two marks side by side
                          for one fact. Inverting the circle says it in the glyph that is
                          already there — and it says it on the tab and the plate too, where
                          there was no room for a dot at all. */}
                      <span
                        class="pane-number"
                        classList={{ current: !asking() && pane.id === props.ctx.focused() }}
                      >
                        {props.ctx.choose.numberOf(pane.id)}
                      </span>
                      {/* The label, and PLAIN while a caller is asking — see `asking` above
                          for why a destination row carries a name and no state. Read as a
                          reactive call rather than a prop the host bakes in: `asking()` is a
                          signal, so the same row re-renders with and without its badges as a
                          question arrives and goes. */}
                      <span class="pane-chooser-label">
                        {props.ctx.tabTitle(pane, asking())}
                      </span>
                      {/* The refusal rides on the row it is about, not in a footnote: the
                          list is short and every row is a candidate, so "why not that one"
                          is only ever asked about a row you are looking at. */}
                      <Show when={refused(pane.id)}>
                        <span class="pane-chooser-why">{refused(pane.id)}</span>
                      </Show>
                    </div>
                  )}
                </For>
              </div>
            )}
          </For>
          {/* The answer that is not a pane. Deliberately NOT one of the walkable rows: the
              walk is spatial and the numbers are positions in the layout, so a row sitting
              between two tiles with no place on screen and no number would break the one
              thing that ties a row to a tile. It is a footer choice — pressable, and
              separated by a rule so it reads as "or, instead of any of these". */}
          <Show when={elsewhere()}>
            {(other) => (
              <div class="pane-chooser-item pane-chooser-other" onClick={() => choose().takeElsewhere("")}>
                <span class="pane-chooser-label">{other().label}</span>
                <span class="pane-chooser-why">type a letter</span>
              </div>
            )}
          </Show>
        </div>
        {/* The keys, only while they ARE the keys. Every line of this names something that
            happens when you press it, and none of it is true of a panel sitting in a gutter
            with no walk up — a standing "Esc cancels" would be an instruction to press a key
            that belongs to whatever pane the user is actually typing in. */}
        <Show when={armed()}>
          <p class="pane-chooser-keys">
            {/* The range is the LIVE one, not the list's: with twelve panes only nine have a
                key, and "1–9" beside a row numbered 12 is the honest way to say so. */}
            <Show when={jumpMax() > 1}>1–{jumpMax()} jump · </Show>
            {/* hjkl is advertised only while it IS movement. With an escape offered, letters
                are how you leave the list, so naming them as arrows here would be the one
                instruction in this panel that does the opposite of what it says. */}
            <Show when={manyTiles()}>{elsewhere() ? "↑↓←→ move · " : "↑↓←→ or hjkl move · "}</Show>
            <Show when={manyPanes()}>Tab next pane · </Show>
            <Show when={elsewhere()}>a–z types a path · </Show>
            Enter selects · Esc cancels
          </p>
        </Show>
      </div>
    </>
  );
}

// THE MINI MAP — the workspace drawn small, above the list. See ./minimap for why it exists
// and for the arithmetic; this is what it looks like.
//
// The list and the map answer two different halves of the same question. The list has the
// names, the tabs and the reasons a pane cannot be chosen; the map has the one thing a list
// cannot carry — WHERE. Once the workspace can be several screens wide, "the terminal in the
// far bottom-right" is a description the list cannot render and the map states in a glance,
// and the rectangle showing what is currently on screen is the only place in the app that
// says how much of the workspace the user is looking at.
function MiniMap<P>(props: {
  ctx: TileCtx<P>;
  tree: Accessor<Node<P>>;
  scroller: () => HTMLElement | null;
  ext: Accessor<Ext>;
  // The two questions the map does NOT get to answer for itself, handed down from the panel so
  // that the picture and the list can never disagree about them: which pane wears the highlight,
  // and what pressing one means. Both have a different answer between walks than during one (see
  // `marked` and `pick` above), and a map that worked them out again from `choose` would be a
  // second set of rules.
  //
  // Refusals are NOT among them, and cannot be: a map is drawn only for the mode's own question
  // (see `asking`), and the only purpose that refuses nothing is that one. Every refusal the app
  // can produce belongs to a caller's question, and those panels have a list and no picture.
  marked: Accessor<string>;
  pick: (id: string) => void;
}): JSX.Element {
  const choose = () => props.ctx.choose;
  const [metrics, setMetrics] = createSignal(NO_METRICS);
  const read = () => setMetrics(metricsOf(props.scroller()));

  // Measured on mount and again whenever the view moves. The listener's lifetime is this
  // component's, and a scroll listener kept alive for a picture nobody is looking at would be
  // pure cost — which is why the panel mounts the map rather than hiding it. `scroll` fires
  // throughout a smooth scroll, so the viewport rectangle TRAVELS with the reveal that each
  // arrow key triggers instead of jumping to where it ended up.
  //
  // PINNING is the third thing that changes what this measures — the gutter takes its width out
  // of the scroll container — and there is deliberately no listener for it. Pinning moves the
  // panel between two different <Show> branches (the gutter and the card), so the map is a new
  // component and this mount IS the re-measure. An effect watching `chooserGutter()` was written
  // for it and the browser pass refused to fail when it was deleted; if those branches are ever
  // merged into one instance, it has to come back. The read is a layout flush against the DOM as
  // it is, so the class that narrows the body is already applied when it runs.
  onMount(() => {
    const el = props.scroller();
    read();
    el?.addEventListener("scroll", read, { passive: true });
    window.addEventListener("resize", read);
    onCleanup(() => {
      el?.removeEventListener("scroll", read);
      window.removeEventListener("resize", read);
    });
  });

  const box = () => mapBox(metrics(), props.ext());
  const view = () => viewportRect(metrics());
  const markedTile = () => stackOf(props.tree(), props.marked());

  // Every tile, with the rectangle the tree says it occupies. `tileRects` rather than a
  // measurement of the rendered tiles, for the same reason `neighborStack` uses it: it is the
  // layout the CSS will produce, exact, and it is the same numbers the arrows walk — so the
  // map cannot disagree with where a direction key goes.
  const cells = () => {
    const stacks = new Map(allStacks(props.tree()).map((s) => [s.id, s]));
    return tileRects(props.tree()).flatMap((rect) => {
      const tile = stacks.get(rect.id);
      return tile ? [{ rect, tile }] : [];
    });
  };

  // Which pane a tile stands for: the one the walk would land on if it went there. For every
  // tile but the marked one that is its visible tab, which is exactly where `neighborStack`
  // lands; for the marked one it is wherever Tab has walked to, so cycling the tabs moves
  // the number on the map as well as the highlight in the list.
  const paneOf = (tile: Extract<Node<P>, { type: "stack" }>) => {
    if (tile.id === markedTile()?.id) return props.marked();
    return (tile.panes[tile.active] ?? tile.panes[0]).id;
  };

  return (
    // ARIA-HIDDEN, exactly like the number plates out on the tiles, and for the same reason:
    // every rectangle here is a row of the listbox above, which is announced, walkable and
    // labelled with the pane's real name. Announcing the workspace twice — once as a list of
    // names and once as a grid of numbers — would make the mode harder to use with a screen
    // reader, not easier. The click is a pointer affordance over a picture; the keyboard
    // route is the list, and it reaches every pane the map draws.
    <div
      class="pane-chooser-map"
      aria-hidden="true"
      style={{ width: `${box().w}rem`, height: `${box().h}rem` }}
    >
      <For each={cells()}>
        {({ rect, tile }) => {
          const pane = () => paneOf(tile);
          // How many numbers this cell can hold, and which of them it shows. Arithmetic, not
          // measurement: a cell is `rect` of the map, and the map's own size is already known
          // in rem (see ./minimap). The window slides to keep the pane the cell stands for
          // visible, so a Tab walk through more tabs than fit still moves a number the user
          // can see.
          const room = () => bulletRoom(rect.w * box().w, rect.h * box().h);
          const win = () => bulletWindow(tile.panes.length, room(), tile.panes.findIndex((p) => p.id === pane()));
          return (
            <div
              class="pane-chooser-map-tile"
              // The tile this rectangle stands for, by name. Nothing in the app reads it; it
              // is here so that a check can match a rectangle to the tile it maps BY IDENTITY
              // rather than by position in the list — which is the very thing a map can get
              // wrong, and the one thing an index-matched comparison could never catch.
              data-stack-id={tile.id}
              classList={{ current: tile.id === markedTile()?.id }}
              style={{
                left: `${rect.x * 100}%`,
                top: `${rect.y * 100}%`,
                width: `${rect.w * 100}%`,
                height: `${rect.h * 100}%`,
              }}
              // A click on a place is the map's whole point, and it means what a click on the
              // matching row means: preview and commit in one gesture. Anywhere in the cell
              // that is not a number takes the pane the cell stands for; a number takes its
              // own, which is the only way to reach a background tab by pointing at it.
              onClick={() => props.pick(pane())}
            >
              {/* An ellipsis for what was cut off this side. Not a bullet: it stands for
                  numbers rather than being one, and a circled "…" would read as a pane. */}
              <Show when={win().head}>
                <span class="pane-chooser-map-more">…</span>
              </Show>
              <For each={tile.panes.slice(win().from, win().to)}>
                {(p: Pane<P>) => (
                  <span
                    class="pane-number"
                    classList={{
                      // Filled where the focus IS, ringed where the WALK is — the same two
                      // marks the list draws, said in the same glyph. Both can be on the one
                      // cell at once when a stack holds the focused pane and the previewed one.
                      current: p.id === props.ctx.focused(),
                      selected: p.id === props.marked(),
                    }}
                    onClick={(e) => {
                      // Stopped here so the cell underneath does not ALSO commit. Unobservable
                      // as things stand — the two commits name the same pane, and the second is
                      // invisible — and it used to have exactly one case that was observable, a
                      // REFUSED number, where the click no-ops and the cell then commits the
                      // pane it stands for. No map is drawn for a question that refuses
                      // anything any more, so that case is gone with it. Kept because a nested
                      // target firing its ancestor's handler is a coincidence rather than a
                      // design, and the coincidence is that they agree.
                      e.stopPropagation();
                      props.pick(p.id);
                    }}
                  >
                    {choose().numberOf(p.id)}
                  </span>
                )}
              </For>
              <Show when={win().tail}>
                <span class="pane-chooser-map-more">…</span>
              </Show>
            </div>
          );
        }}
      </For>
      {/* WHAT IS ON SCREEN. Drawn over the tiles rather than as one of them, because it is not
          a thing in the workspace — it is where the workspace is being looked at from. Absent
          entirely when everything fits, which is both the honest reading and the one that
          keeps the map quiet on the layouts that never needed it. */}
      <Show when={view()}>
        {(v) => (
          <div
            class="pane-chooser-map-view"
            style={{
              left: `${v().x * 100}%`,
              top: `${v().y * 100}%`,
              width: `${v().w * 100}%`,
              height: `${v().h * 100}%`,
            }}
          />
        )}
      </Show>
    </div>
  );
}
