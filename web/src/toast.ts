// A tiny app-wide notification service (a module singleton like modal.ts /
// command-center.ts / settings.ts). Transient outcomes — "copied 2 items", "renamed to
// x", a failed transfer — go here instead of into the screen that produced them: a line
// appearing inside a pane reflows the very listing you are looking at, and a drop's
// result belongs to the action, not to the pane that happened to receive it.
//
// The host (views/Toaster.tsx, mounted once in App.tsx) renders whatever is queued.

import { createSignal, createRoot } from "solid-js";
import type { Accessor } from "solid-js";

export interface Toast {
  id: number;
  text: string;
  kind: "info" | "error";
}

// Errors linger: they are usually longer, and are read rather than glanced at.
const LINGER = { info: 4000, error: 9000 };

const center = createRoot(() => {
  const [toasts, setToasts] = createSignal<Toast[]>([]);
  let seq = 0;

  const dismiss = (id: number) => setToasts((list) => list.filter((t) => t.id !== id));

  const push = (text: string, kind: Toast["kind"]): number => {
    const id = ++seq;
    setToasts((list) => [...list, { id, text, kind }]);
    setTimeout(() => dismiss(id), LINGER[kind]);
    return id;
  };

  return {
    toasts: toasts as Accessor<Toast[]>,
    push,
    dismiss,
    clear: () => setToasts([]),
  };
});

export const toasts = center.toasts;
export const toast = (text: string) => center.push(text, "info");
export const toastError = (text: string) => center.push(text, "error");
export const dismissToast = center.dismiss;
// clearToasts drops everything queued. The module outlives any one screen, so tests use
// it to start from silence; nothing in the app needs it.
export const clearToasts = center.clear;
