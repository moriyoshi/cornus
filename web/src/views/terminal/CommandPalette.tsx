import { For, Show, createMemo, createSignal, createEffect, onMount } from "solid-js";
import { bindsOf, directsOf } from "../../command-center";
import type { Command } from "../../command-center";

export type { Command } from "../../command-center";

// Sections render (and commands sort) in this order; unknown groups fall to the
// end, keeping their registration order. Contextual groups (e.g. "Workspace") lead
// the always-present global ones so the current screen's actions surface first. A
// screen's group MUST be listed here — omitting it ranks the group last, sinking
// that screen's own commands below "Go to" and "Settings".
const GROUP_ORDER = ["Workspace", "Go to", "Settings"];

function groupRank(group: string): number {
  const i = GROUP_ORDER.indexOf(group);
  return i === -1 ? GROUP_ORDER.length : i;
}

// filterCommands narrows by a space-separated query and orders the survivors by
// group. Pure so it is unit-testable; the ordering is what the palette both renders
// and selects over.
//
// A token beginning with ":" names a TAG and must match one exactly — `:pane` is the
// pane commands, and never a command that merely says "panel" somewhere. Every other
// token is a substring that must appear in the title, group, keywords, or tags. So a
// tag is both searchable prose and, with the sigil, a hard filter; that is the whole
// difference between the two forms. A lone ":" requires nothing, because it is what
// the user has typed halfway through naming a tag.
export function filterCommands(commands: Command[], query: string): Command[] {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean);
  const tags = tokens.filter((t) => t.startsWith(":")).map((t) => t.slice(1)).filter(Boolean);
  const words = tokens.filter((t) => !t.startsWith(":"));
  const hit = (c: Command) => {
    const own = (c.tags ?? []).map((t) => t.toLowerCase());
    if (!tags.every((t) => own.includes(t))) return false;
    if (words.length === 0) return true;
    const hay = `${c.group} ${c.title} ${c.keywords ?? ""} ${own.join(" ")}`.toLowerCase();
    return words.every((t) => hay.includes(t));
  };
  return commands
    .filter(hit)
    .map((c, i) => ({ c, i }))
    // Stable sort by group rank: keep original order within a group.
    .sort((a, b) => groupRank(a.c.group) - groupRank(b.c.group) || a.i - b.i)
    .map(({ c }) => c);
}

// CommandPalette is the searchable menu opened by "prefix then >". It grabs focus
// via its filter input, matches commands as you type, and runs the selected one on
// Enter (or a click). Escape / outside-click cancels and restores prior focus.
export default function CommandPalette(props: {
  commands: Command[];
  // The filter's starting text. Read once, at mount: the palette is remounted per
  // opening (App.tsx's <Show>), and making it reactive would fight the user's typing.
  initialQuery?: string;
  onClose: () => void;
}) {
  const [query, setQuery] = createSignal(props.initialQuery ?? "");
  const [sel, setSel] = createSignal(0);
  let input!: HTMLInputElement;
  let listEl!: HTMLDivElement;
  // Remember who had focus (a terminal, a nav link…) so cancel returns focus there
  // rather than dropping it on <body>. Running a command may move focus itself, so
  // only cancels restore.
  let prevFocus: HTMLElement | null = null;

  const results = createMemo(() => filterCommands(props.commands, query()));

  // Keep the selection in range as the result set shrinks/grows while typing.
  createEffect(() => {
    const n = results().length;
    if (sel() > n - 1) setSel(Math.max(0, n - 1));
  });

  onMount(() => {
    prevFocus = document.activeElement as HTMLElement | null;
    input.focus();
    // A seeded filter is a starting point, not a prefix the user must live behind: the
    // caret goes to the end so typing narrows further, and one Backspace-and-hold widens
    // back to everything.
    input.setSelectionRange?.(input.value.length, input.value.length);
  });

  const cancel = () => {
    prevFocus?.focus?.();
    props.onClose();
  };
  const runAndClose = (c: Command) => {
    // A disabled row stays selectable and stays on screen: it is there to be READ. Closing
    // the palette on a press that does nothing would look like the command ran.
    if (c.disabled) return;
    c.run();
    props.onClose();
  };

  const move = (delta: number) => {
    const n = results().length;
    if (n === 0) return;
    setSel((s) => (s + delta + n) % n);
    // Keep the active option visible within the scrolling list (scrollIntoView is
    // absent under jsdom, hence the method-level guard).
    queueMicrotask(() => {
      const el = listEl?.querySelector<HTMLElement>('[aria-selected="true"]');
      el?.scrollIntoView?.({ block: "nearest" });
    });
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const c = results()[sel()];
      if (c) runAndClose(c);
    }
  };

  // Insert a section header each time the (group-ordered) list crosses into a new
  // group; the flat index is what selection and Enter operate on.
  const rows = createMemo(() => {
    let last: string | null = null;
    return results().map((c, i) => {
      const header = c.group !== last ? c.group : null;
      last = c.group;
      return { c, i, header };
    });
  });

  return (
    <div class="cmd-overlay" onMouseDown={cancel}>
      <div
        class="cmd-palette"
        role="dialog"
        aria-label="Command palette"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          class="cmd-filter"
          ref={input}
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls="cmd-list"
          aria-label="Filter commands"
          placeholder="Type to filter commands…"
          autocomplete="off"
          spellcheck={false}
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
          onKeyDown={onKeyDown}
        />
        <div class="cmd-list" id="cmd-list" role="listbox" ref={listEl}>
          <For each={rows()}>
            {(row) => (
              <>
                <Show when={row.header}>
                  <div class="cmd-group" role="presentation">
                    {row.header}
                  </div>
                </Show>
                {/* aria-disabled, never the `disabled` attribute: a disabled <button>
                    receives no mouse events, so hover-select would stop working on it, and
                    some readers drop it from the accessibility tree entirely — the opposite
                    of the point, which is that the command is visibly THERE and merely not
                    available yet. It stays selectable for the same reason; Enter on it is a
                    no-op with a reason printed beside it. */}
                <button
                  class="cmd-item"
                  classList={{ selected: row.i === sel(), disabled: !!row.c.disabled }}
                  role="option"
                  aria-selected={row.i === sel()}
                  aria-disabled={row.c.disabled ? true : undefined}
                  type="button"
                  onMouseEnter={() => setSel(row.i)}
                  onClick={() => runAndClose(row.c)}
                >
                  <span class="cmd-item-title">{row.c.title}</span>
                  <Show
                    when={row.c.disabled}
                    fallback={
                      <Show
                        when={bindsOf(row.c).length || directsOf(row.c).length}
                        fallback={
                          <Show when={row.c.hint}>
                            <kbd>{row.c.hint}</kbd>
                          </Show>
                        }
                      >
                        {/* Every spelling, each in its own cap: a command that answers to
                            two keys and advertises one has a key nobody presses.

                            The two KINDS have to be told apart, because every cap in this
                            column has meant "after the prefix" until now, and a direct key
                            shown in that column would read as one more of those — a wrong
                            instruction, not merely an incomplete one. `.direct` marks it,
                            and the tooltip says the difference in words for anyone who
                            reads the styling as decoration. */}
                        <For each={bindsOf(row.c)}>{(b) => <kbd>{b}</kbd>}</For>
                        <For each={directsOf(row.c)}>
                          {(b) => (
                            <kbd class="direct" title="press on its own, without the prefix">
                              {b}
                            </kbd>
                          )}
                        </For>
                      </Show>
                    }
                  >
                    {/* The reason takes the accelerator's place rather than sitting beside
                        it: the key is what you would press, and right now pressing it does
                        nothing. */}
                    <span class="cmd-item-why">{row.c.disabled}</span>
                  </Show>
                </button>
              </>
            )}
          </For>
          <Show when={results().length === 0}>
            <div class="cmd-empty">No matching commands</div>
          </Show>
        </div>
        <div class="cmd-hint">↑↓ to move · Enter to run · Esc to cancel</div>
      </div>
    </div>
  );
}
