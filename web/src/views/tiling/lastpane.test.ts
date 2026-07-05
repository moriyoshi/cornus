import { describe, it, expect } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { createLastPane } from "./lastpane";
import { newPane, stack, type Node, type Pane } from "./layout";

// The last-pane memory, driven directly. What it does through the workspace — the key, the
// alternation, the disabled row — is asserted in views.test.tsx with real keys; what is
// tested here is the part no key can reach: the tree changing under a remembered id.

type P = { label: string };
const pane = (label: string) => newPane<P>({ label });

describe("createLastPane", () => {
  // Three panes side by side, so "the pane you came from" is never the same answer as "the
  // next pane" — with two, every implementation looks right.
  function harness() {
    const [a, b, c] = [pane("a"), pane("b"), pane("c")];
    const tree = (panes: Pane<P>[]): Node<P> => ({
      type: "split",
      id: "outer",
      dir: "h",
      ratio: 0.5,
      a: stack([panes[0]], 0),
      b: { type: "split", id: "inner", dir: "h", ratio: 0.5, a: stack([panes[1]], 0), b: stack([panes[2]], 0) },
    });
    return createRoot((dispose) => {
      const [live, setLive] = createSignal<Node<P>>(tree([a, b, c]));
      const [focused, setFocused] = createSignal(a.id);
      const last = createLastPane(live, focused);
      return { a, b, c, last, setLive, setFocused, dispose };
    });
  }

  it("remembers where the focus came from, and alternates between two panes", () => {
    const h = harness();
    // Nothing has been visited yet, so there is nowhere to go back to. A memory that
    // defaulted to the focused pane would make the key a no-op instead of an offer.
    expect(h.last()).toBeUndefined();

    h.setFocused(h.b.id);
    expect(h.last()).toBe(h.a.id);
    // The third pane is what makes this an assertion: "the pane before this one" and "the
    // next pane along" are now different answers, and only one of them is c.
    h.setFocused(h.c.id);
    expect(h.last()).toBe(h.b.id);

    // Going back is itself a focus change, so the memory swaps — which is what makes the
    // key alternate rather than walk backwards through a history.
    h.setFocused(h.b.id);
    expect(h.last()).toBe(h.c.id);
    h.setFocused(h.c.id);
    expect(h.last()).toBe(h.b.id);
    h.dispose();
  });

  it("forgets a pane that has closed, even though the focus never moved", () => {
    const h = harness();
    h.setFocused(h.b.id);
    expect(h.last()).toBe(h.a.id);

    // Closing a BACKGROUND pane leaves the focus exactly where it was, so nothing else in
    // the workspace changes and nothing prompts a re-check: the liveness test has to live
    // in the read. Answering `a` here would activate a pane that is not in the tree, which
    // leaves `focused` naming nothing and every contextual command reading an empty pane.
    h.setLive(stack([h.b, h.c], 0));
    expect(h.last()).toBeUndefined();

    // And it recovers on the next real move rather than staying dead.
    h.setFocused(h.c.id);
    expect(h.last()).toBe(h.b.id);
    h.dispose();
  });
});
