// One rule, in one place: BECOMING THE FOCUSED PANE MOVES THE KEYBOARD INTO IT.
//
// "Focused" is two different things that have to be kept in step. The workspace has its own
// `focused` — which tile wears the frame, which pane the contextual commands act on — and
// the DOM has the element actually receiving keys. A command that moved the first and not
// the second leaves the user looking at one pane and typing into another, which is the worst
// possible outcome: every keystroke goes somewhere, silently, and a terminal is a place where
// that is not recoverable.
//
// It was implemented four times and was right once. `Term` claimed reactively (correct);
// `FilePane` claimed once per mount behind a `landed` latch, so walking BACK to a listing you
// had already visited left the keyboard where it was; the editor and the image viewer never
// claimed at all. The visible symptoms were "Select next pane" and "Select last active pane"
// leaving the keys in the terminal you just left — but not `Choose pane…`, whose panel takes
// focus for the duration of the walk and so happens to get the keyboard off the terminal on
// its way past. That is a side effect of an unrelated mechanism, which is why two of the
// three routes were broken and the third looked fine.
//
// What to focus INSIDE a pane is the pane's own business — a row for a listing, the document
// for an editor, the xterm for a terminal — so this module owns only the WHEN, and every
// caller supplies the other two answers.

import { createEffect } from "solid-js";

// The modes a pane must not reach past. Each is something the user is deliberately inside,
// drawn OVER the workspace, and a pane that mounted or gained focus behind one would pull the
// keyboard out from under it. Same list App.tsx's key handler stands aside for, plus the pane
// chooser — which is the walk itself, previewing panes it has not committed to.
//
// Not reactive, and it does not need to be: this is a guard on an effect that already re-runs
// whenever anything the pane renders from changes, so a claim skipped under a modal is
// retried on the next pass rather than lost. The alternative — a signal tracking the topmost
// mode — would make every pane in the tree re-run on every modal.
//
// ❗NO TEST REACHES THIS, and it is carried anyway. It came from FilePane's own claim, which
// stated the case in exactly these terms, and it is kept because taking it out would be a
// behaviour change made on the strength of not having found the path. I went looking and
// could not: every claim in the tree depends on data that arrives ASYNCHRONOUSLY (a listing,
// a file's text), so by the time an effect re-runs the mode that was up has closed, and a
// claim skipped under a scrim is indistinguishable from one that was never due. A pane whose
// effect re-ran from purely synchronous state while a modal held the keyboard would reach it
// — nothing does that today. Treat it as unverified, not as covered.
export const modeOwnsKeyboard = (): boolean =>
  !!document.activeElement?.closest(
    ".pane-pick-overlay, .modal-overlay, .cmd-overlay, .pane-chooser",
  );

// claimFocus takes the keyboard for a pane that is the workspace's focused one and does not
// already hold it.
//
//   focused — is this pane the one the workspace considers current?
//   holds   — is the keyboard already somewhere inside it? The claim is SKIPPED when it is,
//             which is what lets the effect re-run freely: a listing that refreshes, an
//             editor that re-renders, a terminal that resizes must not yank the cursor back
//             to wherever `take` puts it. Answer it about the whole pane, not about the one
//             element `take` focuses, or a toolbar button in this pane loses the keys to a
//             re-render.
//   take    — put the keyboard somewhere useful inside this pane.
export function claimFocus(focused: () => boolean, holds: () => boolean, take: () => void): void {
  createEffect(() => {
    if (!focused() || holds() || modeOwnsKeyboard()) return;
    take();
  });
}

// holdsFocus answers `holds` for a pane that has a root element: the keyboard is inside it if
// the active element is. `document.activeElement` is <body> when nothing is focused, and body
// contains everything — hence the explicit check, without which no pane would ever claim.
export const holdsFocus = (root: HTMLElement | undefined): boolean => {
  const el = document.activeElement;
  return !!root && !!el && el !== document.body && root.contains(el);
};
