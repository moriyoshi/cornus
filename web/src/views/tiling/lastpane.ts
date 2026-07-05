// "The pane I came from" — tmux's last-pane, bound here to `prefix ;` as it is there.
//
// ONE pane, not a history stack. Pressing the key twice returns you to where you started,
// which is the whole of what the key is for: an editor in one pane and its output in
// another, alternated without looking. A stack would make the second press mean something
// different from the first, and a key whose meaning depends on how many times you have
// already pressed it is a key you have to keep count for.
//
// It is DERIVED from the focus rather than recorded at each place that moves the focus. The
// two hosts move it from about a dozen call sites between them — a tab click, a split, a
// close, a drag drop, the chooser's commit, `prefix o`, a pane retargeting itself — and a
// memory that had to be updated at each would be wrong the first time someone added a
// thirteenth. Watching the one value they all end up writing cannot miss any of them, and
// costs one effect.
//
// The state lives with the HOST, like the drag protocol and the chooser next door: a module
// signal would outlive a route change, offering the other workspace a pane id from a tree it
// is not in, and vitest's shared module registry would carry one test's memory into the next.

import { createEffect, createSignal } from "solid-js";
import type { Accessor } from "solid-js";
import { findPane, type Node } from "./layout";

// createLastPane returns the pane the focus was on BEFORE the pane it is on now — or
// undefined when there is no such pane to go back to, which covers both "nothing has been
// visited yet" and "the pane you came from has since closed". Answering undefined for a dead
// id rather than the id itself is what lets the caller state one honest reason and skip the
// liveness check it would otherwise have to remember: a pane can close without the focus
// moving (closing a background tab), so a remembered id can go stale with nothing else
// changing.
export function createLastPane<P>(
  tree: Accessor<Node<P>>,
  focused: Accessor<string>,
): Accessor<string | undefined> {
  const [last, setLast] = createSignal<string | undefined>();
  // The effect's own return value is the previous focus, which is the whole mechanism: it is
  // read on the next run and never stored anywhere a stale copy could be read from. The
  // inequality guard is belt and braces — both a signal and a store bail out of notifying on
  // a write of the value they already hold, so this effect does not run for a focus "change"
  // that changes nothing.
  createEffect<string | undefined>((prev) => {
    const cur = focused();
    if (prev !== undefined && prev !== cur) setLast(prev);
    return cur;
  });
  return () => {
    const id = last();
    return id && findPane(tree(), id) ? id : undefined;
  };
}
