import { For, Show, createMemo, createSignal, createEffect, onMount } from "solid-js";
import type { Command } from "../../command-center";

export type { Command } from "../../command-center";

// Sections render (and commands sort) in this order; unknown groups fall to the
// end, keeping their registration order. Contextual groups (e.g. "Terminal") lead
// the always-present global ones so the current screen's actions surface first.
const GROUP_ORDER = ["Terminal", "Go to", "Settings"];

function groupRank(group: string): number {
  const i = GROUP_ORDER.indexOf(group);
  return i === -1 ? GROUP_ORDER.length : i;
}

// filterCommands narrows by a space-separated query (every token must appear in
// the title, group, or keywords) and orders the survivors by group. Pure so it is
// unit-testable; the ordering is what the palette both renders and selects over.
export function filterCommands(commands: Command[], query: string): Command[] {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (c: Command) => {
    if (tokens.length === 0) return true;
    const hay = `${c.group} ${c.title} ${c.keywords ?? ""}`.toLowerCase();
    return tokens.every((t) => hay.includes(t));
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
export default function CommandPalette(props: { commands: Command[]; onClose: () => void }) {
  const [query, setQuery] = createSignal("");
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
  });

  const cancel = () => {
    prevFocus?.focus?.();
    props.onClose();
  };
  const runAndClose = (c: Command) => {
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
                <button
                  class="cmd-item"
                  classList={{ selected: row.i === sel() }}
                  role="option"
                  aria-selected={row.i === sel()}
                  type="button"
                  onMouseEnter={() => setSel(row.i)}
                  onClick={() => runAndClose(row.c)}
                >
                  <span class="cmd-item-title">{row.c.title}</span>
                  <Show when={row.c.bind ?? row.c.hint}>
                    <kbd>{row.c.bind ?? row.c.hint}</kbd>
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
