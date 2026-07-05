import { For, Show, onCleanup, onMount } from "solid-js";
import type { Accessor, JSX } from "solid-js";
import { allPanes, allStacks, stackOf, type Node, type Pane } from "./layout";
import { CTRL_SIDE, KEY_SIDE, type TileCtx } from "./panes";

// The pane chooser's chrome: the list in the corner, and the keyboard that drives the walk.
// The state it reads and writes is ./choose; this is only what that mode looks like.
//
// Rendered once per workspace by the host, inside .workspace, and only while the mode is up
// — so its window listener's whole lifetime is the mode's, exactly as PickBackdrop's is. The
// app owns a capture-phase document keydown for the tmux prefix, and that one stands aside
// for anything inside .xterm so a live shell keeps its own keys, arrows and Escape included.
// Taking those keys for the duration of a mode the user explicitly entered is intended;
// taking them the rest of the time is not.

export function PaneChooser<P>(props: { ctx: TileCtx<P>; tree: Accessor<Node<P>> }): JSX.Element {
  let panel!: HTMLDivElement;
  // Where focus was when the mode armed, restored only if it is CANCELLED. A commit moves
  // the focus to the pane that was chosen — that is the whole point of the mode — and
  // pulling it back would undo the one thing it does.
  let prevFocus: HTMLElement | null = null;

  const choose = () => props.ctx.choose;
  const selected = () => props.ctx.choose.selected();
  // Why a pane is not an answer to THIS question, or undefined when it is one. Read during
  // render, so it tracks whatever the purpose reads (the tree, a pane's path, the source of
  // a transfer) and a row greys out the moment its reason becomes true.
  const refused = (id: string) => props.ctx.choose.purpose().refuse?.(id);
  const elsewhere = () => props.ctx.choose.purpose().elsewhere;
  const tiles = () => allStacks(props.tree());
  const selectedTile = () => {
    const id = selected();
    return id ? stackOf(props.tree(), id) : undefined;
  };
  // Whether a direction can lead anywhere, and whether Tab can. Both gate their own line of
  // the hint: naming an inert key is worse than naming none, because the reader tries it,
  // nothing happens, and the whole line is then suspect.
  const manyTiles = () => tiles().length > 1;
  const manyTabs = () => (selectedTile()?.panes.length ?? 0) > 1;
  const jumpMax = () => Math.min(9, allPanes(props.tree()).length);

  const cancel = () => {
    choose().cancel();
    if (prevFocus?.isConnected) prevFocus.focus();
  };

  // The panel takes focus so that stray keys — everything this handler does not claim — land
  // on it rather than in the shell the user is looking at. It is the mode's one focusable
  // element and it holds aria-activedescendant, which is what a screen reader follows as the
  // walk moves; the rows themselves are not tab stops, because the arrows do not walk them.
  onMount(() => {
    prevFocus = document.activeElement as HTMLElement | null;
    panel.focus();
  });

  onMount(() => {
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
        // Tab without this leaves for the nav behind it. When the previewed tile is a stack
        // of tabs it walks them — the one move no direction can express, since stacked tabs
        // share a place on screen.
        stop();
        choose().cycleTab(e.shiftKey ? -1 : 1);
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
      {/* The workspace is inert for the duration. Without this a click could move the focus
          — the one thing the mode promises not to do until Enter — and the promise is worth
          more than the click: the panes are all still visible, and every one of them is one
          keystroke away. Anywhere else means "never mind". */}
      <div class="pane-chooser-scrim" onClick={cancel} />
      <div
        ref={panel}
        class="pane-chooser"
        tabindex={-1}
        role="listbox"
        // The title IS the accessible name: the mode is reused for questions other than
        // "which pane do I want to be in?", and a listbox permanently labelled with the
        // first of them would announce the wrong one for all the rest.
        aria-label={choose().purpose().title}
        aria-activedescendant={selected() ? `pane-choice-${selected()}` : undefined}
      >
        <p class="pane-chooser-title">{choose().purpose().title}</p>
        <For each={tiles()}>
          {(tile, i) => (
            <div
              class="pane-chooser-tile"
              classList={{ current: tile.id === selectedTile()?.id }}
              role="group"
              aria-label={`Tile ${i() + 1}`}
            >
              <For each={tile.panes}>
                {(pane: Pane<P>) => (
                  <div
                    id={`pane-choice-${pane.id}`}
                    class="pane-chooser-item"
                    classList={{ selected: pane.id === selected(), disabled: !!refused(pane.id) }}
                    role="option"
                    aria-disabled={refused(pane.id) ? true : undefined}
                    // The mode's two states, and ARIA happens to have a word for each:
                    // aria-selected is where the WALK is, aria-current is where the focus
                    // still is. Both are on screen at once, so a reader that announced only
                    // one of them would be describing a different mode.
                    aria-selected={pane.id === selected()}
                    aria-current={pane.id === props.ctx.focused() ? "true" : undefined}
                    // A pointer names its own row, so it needs no walk: one press previews
                    // and commits in the same gesture, which is what a click on a list of
                    // places to go already means everywhere else.
                    onClick={() => choose().commit(pane.id)}
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
                      classList={{ current: pane.id === props.ctx.focused() }}
                    >
                      {props.ctx.choose.numberOf(pane.id)}
                    </span>
                    <span class="pane-chooser-label">{props.ctx.tabTitle(pane)}</span>
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
        <p class="pane-chooser-keys">
          {/* The range is the LIVE one, not the list's: with twelve panes only nine have a
              key, and "1–9" beside a row numbered 12 is the honest way to say so. */}
          <Show when={jumpMax() > 1}>1–{jumpMax()} jump · </Show>
          {/* hjkl is advertised only while it IS movement. With an escape offered, letters
              are how you leave the list, so naming them as arrows here would be the one
              instruction in this panel that does the opposite of what it says. */}
          <Show when={manyTiles()}>{elsewhere() ? "↑↓←→ move · " : "↑↓←→ or hjkl move · "}</Show>
          <Show when={manyTabs()}>Tab next tab · </Show>
          <Show when={elsewhere()}>a–z types a path · </Show>
          Enter selects · Esc cancels
        </p>
      </div>
    </>
  );
}
