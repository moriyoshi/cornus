import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@solidjs/testing-library";
import CommandPalette, { filterCommands, type Command } from "./CommandPalette";

afterEach(cleanup);

function cmd(over: Partial<Command> & { id: string }): Command {
  return { title: over.id, group: "Workspace", run: () => {}, ...over };
}

function palette(over: Partial<{ commands: Command[]; onClose: () => void }> = {}) {
  const run = vi.fn();
  const onClose = over.onClose ?? vi.fn();
  const commands = over.commands ?? [cmd({ id: "split", title: "Split pane left / right", run })];
  render(() => <CommandPalette commands={commands} onClose={onClose} />);
  return { run, onClose };
}

describe("filterCommands", () => {
  const cmds: Command[] = [
    cmd({ id: "a", title: "Split pane left / right", group: "Workspace" }),
    cmd({ id: "b", title: "Overview", group: "Go to" }),
    cmd({ id: "c", title: "Workloads", group: "Go to" }),
    cmd({ id: "d", title: "Enable the prefix key", group: "Settings", keywords: "tmux" }),
  ];

  it("returns everything (group-ordered) for an empty query", () => {
    const r = filterCommands(cmds, "");
    expect(r.map((c) => c.id)).toEqual(["a", "b", "c", "d"]); // Workspace, Go to, Settings
  });

  it("matches title, is case-insensitive, and all tokens must hit", () => {
    expect(filterCommands(cmds, "split").map((c) => c.id)).toEqual(["a"]);
    expect(filterCommands(cmds, "WORKLOADS").map((c) => c.id)).toEqual(["c"]);
    expect(filterCommands(cmds, "pane right").map((c) => c.id)).toEqual(["a"]);
    expect(filterCommands(cmds, "pane nope")).toHaveLength(0);
  });

  it("matches group and hidden keywords", () => {
    expect(filterCommands(cmds, "go to").map((c) => c.id).sort()).toEqual(["b", "c"]);
    expect(filterCommands(cmds, "tmux").map((c) => c.id)).toEqual(["d"]); // via keywords
  });

  // A screen's contextual group must outrank the always-present global ones, or that
  // screen's own actions render below "Go to" / "Settings". "Files" was missing from
  // GROUP_ORDER and sank to the bottom for exactly that reason; the Files and Terminal
  // screens have since become one "Workspace", and the trap it fell into has not.
  //
  // The unlisted group is the assertion's other half: it stands for a screen added later
  // whose author forgot GROUP_ORDER, and it has to sink BELOW the global ones — that is the
  // failure mode being pinned, not merely the order of the three names above it.
  it("ranks a contextual screen group above the global ones, and an unlisted one below", () => {
    const mixed: Command[] = [
      cmd({ id: "goto", title: "Overview", group: "Go to" }),
      cmd({ id: "set", title: "Enable the prefix key", group: "Settings" }),
      cmd({ id: "later", title: "Split pane left / right", group: "Not In GROUP_ORDER" }),
      cmd({ id: "ws", title: "Split pane left / right", group: "Workspace" }),
    ];
    expect(filterCommands(mixed, "").map((c) => c.id)).toEqual(["ws", "goto", "set", "later"]);
  });

  // Tags are the facet a `:name` token requires exactly. They cut across groups, which is
  // the point: `:pane` is the pane operations of whichever screen is mounted, however that
  // screen chose to group and word them.
  describe("tags", () => {
    const tagged: Command[] = [
      cmd({ id: "split", title: "Split pane…", group: "Workspace", tags: ["pane"] }),
      cmd({ id: "close", title: "Close focused pane", group: "Workspace", tags: ["pane", "danger"] }),
      cmd({ id: "panel", title: "Toggle the side panel", group: "Settings" }),
      cmd({ id: "goto", title: "Overview", group: "Go to" }),
    ];

    it("requires the tag exactly, where a bare word would match prose", () => {
      expect(filterCommands(tagged, ":pane").map((c) => c.id)).toEqual(["split", "close"]);
      // The contrast that gives the sigil its reason: "pane" as a word also hits the
      // command about a side PANEL, and misses nothing tagged. Exactness is the feature.
      expect(filterCommands(tagged, "pane").map((c) => c.id)).toEqual(["split", "close", "panel"]);
      // And a tag is a whole name, not a prefix: `:pan` names no tag at all.
      expect(filterCommands(tagged, ":pan")).toEqual([]);
    });

    it("ands multiple tags, and mixes with ordinary words", () => {
      expect(filterCommands(tagged, ":pane :danger").map((c) => c.id)).toEqual(["close"]);
      expect(filterCommands(tagged, ":pane close").map((c) => c.id)).toEqual(["close"]);
      expect(filterCommands(tagged, ":pane overview")).toEqual([]);
    });

    it("matches a tag as ordinary text too, so plain search still finds it", () => {
      // `danger` appears in no title, group, or keyword — only as a tag.
      expect(filterCommands(tagged, "danger").map((c) => c.id)).toEqual(["close"]);
    });

    it("requires nothing for a lone colon, which is a tag half-typed", () => {
      // Everything, in the usual group order (Workspace, then "Go to", then Settings).
      expect(filterCommands(tagged, ":").map((c) => c.id)).toEqual(["split", "close", "goto", "panel"]);
    });
  });
});

describe("CommandPalette", () => {
  it("filters as you type and runs the match on Enter", () => {
    const run = vi.fn();
    const onClose = vi.fn();
    const commands = [
      cmd({ id: "h", title: "Split pane left / right", run }),
      cmd({ id: "x", title: "Close focused pane", run: vi.fn() }),
    ];
    render(() => <CommandPalette commands={commands} onClose={onClose} />);
    const filter = screen.getByRole("combobox");
    fireEvent.input(filter, { target: { value: "split" } });
    expect(screen.getAllByRole("option")).toHaveLength(1);
    fireEvent.keyDown(filter, { key: "Enter" });
    expect(run).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("runs a command on click", () => {
    const { run, onClose } = palette();
    fireEvent.click(screen.getByRole("option", { name: /Split/ }));
    expect(run).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("moves the selection with the arrow keys and runs it", () => {
    const first = vi.fn();
    const second = vi.fn();
    const commands = [
      cmd({ id: "a", title: "First", run: first }),
      cmd({ id: "b", title: "Second", run: second }),
    ];
    render(() => <CommandPalette commands={commands} onClose={vi.fn()} />);
    const filter = screen.getByRole("combobox");
    fireEvent.keyDown(filter, { key: "ArrowDown" });
    fireEvent.keyDown(filter, { key: "Enter" });
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });

  it("closes on Escape without running anything", () => {
    const { run, onClose } = palette();
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Escape" });
    expect(run).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("shows a command's tmux bind as its accelerator", () => {
    render(() => (
      <CommandPalette
        commands={[cmd({ id: "h", title: "Split pane left / right", bind: "%" })]}
        onClose={vi.fn()}
      />
    ));
    const option = screen.getByRole("option", { name: /Split/ });
    expect(option.querySelector("kbd")?.textContent).toBe("%");
  });

  // A seeded filter is how the tile ⋮ turns the palette into a pane menu. It has to be
  // both applied AND editable: seeded-but-ignored is a menu of everything, and applied-but-
  // frozen is a menu you cannot escape into the rest of the commands.
  it("starts filtered by initialQuery, with the caret past it", () => {
    render(() => (
      <CommandPalette
        commands={[
          cmd({ id: "split", title: "Split pane…", tags: ["pane"] }),
          cmd({ id: "goto", title: "Overview", group: "Go to" }),
        ]}
        initialQuery=":pane "
        onClose={vi.fn()}
      />
    ));
    const input = screen.getByRole("combobox") as HTMLInputElement;
    expect(input.value).toBe(":pane ");
    expect(input.selectionStart).toBe(":pane ".length);
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByText("Split pane…")).toBeTruthy();

    // Clearing it widens back to everything — the seed is a starting point, not a cage.
    fireEvent.input(input, { target: { value: "" } });
    expect(screen.getAllByRole("option")).toHaveLength(2);
  });

  // A disabled command is offered so it can be READ: it keeps its place in the list, says
  // why it cannot run, and does nothing when pressed. Each leg rules out a different way of
  // getting it wrong — filtered out entirely, silently runnable, or "runnable" in the sense
  // that the palette closes and the user believes something happened.
  it("shows a disabled command with its reason, and refuses to run it", () => {
    const run = vi.fn();
    const onClose = vi.fn();
    render(() => (
      <CommandPalette
        commands={[cmd({ id: "move", title: "Move pane…", disabled: "nowhere to move it", run })]}
        onClose={onClose}
      />
    ));

    const row = screen.getByRole("option");
    expect(row.getAttribute("aria-disabled")).toBe("true");
    expect(row.textContent).toContain("nowhere to move it");
    // aria-disabled, not the attribute: the row must still take mouse events (hover-select)
    // and still appear in the accessibility tree.
    expect((row as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(row);
    expect(run).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();

    // Enter goes through the same guard — the selection is allowed to land on it, so this
    // is the press a keyboard user actually makes.
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
    expect(run).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows an empty state when nothing matches", () => {
    palette();
    fireEvent.input(screen.getByRole("combobox"), { target: { value: "zzznope" } });
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText(/No matching commands/)).toBeInTheDocument();
  });
});
