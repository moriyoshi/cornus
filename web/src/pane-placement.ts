// The pane-placement prompt asks the user WHERE a freshly created pane should go —
// stacked as a tab on the current tile, or split into a new tile beside it. It is a
// thin wrapper over the shared modal service (a "choice" dialog); the split side comes
// from the user's preference (RTL-aware for "auto").

import { promptChoice, submitModal, dismissModal } from "./modal";
import { settings } from "./settings";

export type Placement = "stack" | "split";

// resolveSplitSide picks the side a "split" lands on from the user's preference, with
// "auto" following the document's text direction (RTL ⇒ left, else right).
export function resolveSplitSide(): "left" | "right" {
  const pref = settings().newPaneSide;
  if (pref === "left" || pref === "right") return pref;
  if (typeof document !== "undefined") {
    try {
      if (getComputedStyle(document.documentElement).direction === "rtl") return "left";
    } catch {
      // jsdom / unsupported: fall through to the LTR default
    }
  }
  return "right";
}

export function promptPanePlacement(): Promise<Placement | null> {
  const side = resolveSplitSide();
  return promptChoice({
    title: "New pane placement",
    options: [
      { value: "stack", label: "Stack as tab", glyph: "⊞" },
      {
        value: "split",
        label: side === "left" ? "Split ← left" : "Split → right",
        glyph: side === "left" ? "◧" : "◨",
      },
    ],
  }) as Promise<Placement | null>;
}

// choosePlacement / cancelPlacement drive the prompt (used by tests).
export const choosePlacement = (choice: Placement): void => submitModal(choice);
export const cancelPlacement = (): void => dismissModal();
