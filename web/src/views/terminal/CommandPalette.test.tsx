import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent } from "@solidjs/testing-library";
import CommandPalette, { filterCommands, type Command } from "./CommandPalette";

afterEach(cleanup);

function cmd(over: Partial<Command> & { id: string }): Command {
  return { title: over.id, group: "Terminal", run: () => {}, ...over };
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
    cmd({ id: "a", title: "Split pane left / right", group: "Terminal" }),
    cmd({ id: "b", title: "Overview", group: "Go to" }),
    cmd({ id: "c", title: "Workloads", group: "Go to" }),
    cmd({ id: "d", title: "Enable the prefix key", group: "Settings", keywords: "tmux" }),
  ];

  it("returns everything (group-ordered) for an empty query", () => {
    const r = filterCommands(cmds, "");
    expect(r.map((c) => c.id)).toEqual(["a", "b", "c", "d"]); // Terminal, Go to, Settings
  });

  it("matches title, is case-insensitive, and all tokens must hit", () => {
    expect(filterCommands(cmds, "split").map((c) => c.id)).toEqual(["a"]);
    expect(filterCommands(cmds, "WORK").map((c) => c.id)).toEqual(["c"]);
    expect(filterCommands(cmds, "pane right").map((c) => c.id)).toEqual(["a"]);
    expect(filterCommands(cmds, "pane nope")).toHaveLength(0);
  });

  it("matches group and hidden keywords", () => {
    expect(filterCommands(cmds, "go to").map((c) => c.id).sort()).toEqual(["b", "c"]);
    expect(filterCommands(cmds, "tmux").map((c) => c.id)).toEqual(["d"]); // via keywords
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

  it("shows an empty state when nothing matches", () => {
    palette();
    fireEvent.input(screen.getByRole("combobox"), { target: { value: "zzznope" } });
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText(/No matching commands/)).toBeInTheDocument();
  });
});
