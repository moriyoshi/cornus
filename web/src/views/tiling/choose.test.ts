import { describe, it, expect } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { createPaneChooser } from "./choose";
import { newPane, stack, type Node } from "./layout";

// The pane chooser's state, driven directly. Almost all of its behaviour is asserted
// through the workspace in views.test.tsx, where it can be walked with real keys — what
// is left here is the one path no key can reach: the tree changing UNDER the mode.

type P = { label: string };
const pane = (label: string) => newPane<P>({ label });

describe("createPaneChooser", () => {
  // a | b, and a chooser that starts on a.
  function harness() {
    const a = pane("a");
    const b = pane("b");
    const tree: Node<P> = {
      type: "split", id: "s", dir: "h", ratio: 0.5,
      a: stack([a], 0),
      b: stack([b], 0),
    };
    return createRoot((dispose) => {
      const [live, setLive] = createSignal<Node<P>>(tree);
      const activated: string[] = [];
      const chooser = createPaneChooser<P>({
        tree: live,
        focused: () => a.id,
        activate: (id) => activated.push(id),
        picking: () => null,
        endPick: () => {},
      });
      return { a, b, chooser, activated, setLive, dispose };
    });
  }

  it("starts the walk on the focused pane and steps from there", () => {
    const h = harness();
    expect(h.chooser.selected()).toBeNull();
    h.chooser.begin();
    expect(h.chooser.selected()).toBe(h.a.id);
    h.chooser.move("right");
    expect(h.chooser.selected()).toBe(h.b.id);
    // Still only a preview: nothing has been activated at any point in the walk.
    expect(h.activated).toEqual([]);
    h.chooser.commit();
    expect(h.activated).toEqual([h.b.id]);
    expect(h.chooser.selected()).toBeNull();
    h.dispose();
  });

  // The one thing that can happen to this mode without the user touching a key: a shell
  // exits and its pane closes itself (TermPane's onExit). Committing the id that named it
  // would leave the workspace's `focused` pointing at a pane that is not in the tree, and
  // every contextual command then reads an empty pane — a state no later action repairs,
  // because nothing will ever set the focus back to something real.
  it("commits nothing when the pane it was previewing has gone", () => {
    const h = harness();
    h.chooser.begin();
    h.chooser.move("right");
    expect(h.chooser.selected()).toBe(h.b.id);

    h.setLive(stack([h.a], 0)); // b's pane closed under the mode
    h.chooser.commit();
    expect(h.activated).toEqual([]);
    // And the mode still ENDS: leaving it armed on a dead id would strand the user in a
    // chooser whose Enter does nothing.
    expect(h.chooser.selected()).toBeNull();
    h.dispose();
  });

  it("does nothing at all before it has begun", () => {
    const h = harness();
    h.chooser.move("right");
    h.chooser.cycleTab(1);
    h.chooser.commit();
    expect(h.chooser.selected()).toBeNull();
    expect(h.activated).toEqual([]);
    h.dispose();
  });

  // A PURPOSE makes the mode answer a question other than "which pane do I want to be in?"
  // — Files asks it "which pane do these files go to?". What the walk does is unchanged;
  // what changes is the title, what Enter hands the answer to, and which rows are answers.
  describe("with a purpose", () => {
    it("hands the chosen pane to the purpose instead of the focus", () => {
      const h = harness();
      const picked: string[] = [];
      h.chooser.begin({ title: "Copy 2 items to…", pick: (id) => picked.push(id) });
      expect(h.chooser.purpose().title).toBe("Copy 2 items to…");
      h.chooser.move("right");
      h.chooser.commit();
      expect(picked).toEqual([h.b.id]);
      // The focus is the assertion: a purpose that ALSO activated would look identical
      // from the picked list, and would drag the user away from the pane they are working
      // in every time they sent a file somewhere.
      expect(h.activated).toEqual([]);
      expect(h.chooser.selected()).toBeNull();

      // And the mode reverts: the next plain begin() is the go-to-a-pane chooser again,
      // not the last question asked. A purpose left behind would send the following
      // `prefix s` into a copy.
      h.chooser.begin();
      expect(h.chooser.purpose().title).toBe("Choose a pane");
      h.chooser.commit();
      expect(h.activated).toEqual([h.a.id]);
      h.dispose();
    });

    it("opens on a pane the purpose would accept, not on a refused one", () => {
      const h = harness();
      const picked: string[] = [];
      // `a` is the focused pane AND the one this purpose refuses — the shape of every
      // transfer, whose source is where you are standing. Landing there would open the
      // mode on a greyed row with nothing to press.
      h.chooser.begin({
        title: "Copy to…",
        pick: (id) => picked.push(id),
        refuse: (id) => (id === h.a.id ? "already here" : undefined),
      });
      expect(h.chooser.selected()).toBe(h.b.id);

      // Walking BACK onto the refused pane is allowed — the walk is spatial, and a row
      // that vanished from between two others would renumber the tiles. Enter there is the
      // quiet no-op a disabled row promises, and it does not end the mode: being told "not
      // that one" should cost no more than a wrong turn.
      h.chooser.move("left");
      expect(h.chooser.selected()).toBe(h.a.id);
      h.chooser.commit();
      expect(picked).toEqual([]);
      expect(h.chooser.selected()).toBe(h.a.id);

      // A click names its own row and is refused by the same rule, not by the walk's.
      h.chooser.commit(h.a.id);
      expect(picked).toEqual([]);
      h.chooser.commit(h.b.id);
      expect(picked).toEqual([h.b.id]);
      h.dispose();
    });
  });
});
