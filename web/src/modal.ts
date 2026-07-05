// A tiny app-wide modal service (a module singleton like command-center.ts /
// settings.ts). It replaces the browser's native prompt()/confirm() with an in-app
// dialog styled to match the rest of the UI, and also backs the pane-placement choice.
// Callers await a promise; the host (views/ModalHost.tsx, mounted once in App.tsx)
// renders the current request and resolves it.

import { createSignal, createRoot } from "solid-js";
import type { Accessor } from "solid-js";

// A choice option: a stable value plus its label and an optional glyph.
export interface ModalOption {
  value: string;
  label: string;
  glyph?: string;
}

interface TextReq {
  kind: "text";
  title: string;
  label?: string;
  initial: string;
  placeholder?: string;
  confirmLabel: string;
  // Where the caret lands. The default selects the initial value, which is right when it
  // is a SUGGESTION — the first keystroke replaces a path you did not want. It is exactly
  // wrong when the initial value is something the user has ALREADY TYPED: the pane
  // chooser's "an arbitrary location" opens on a letter and hands that letter over as the
  // first character, and a select-all would eat it on the second.
  caretAtEnd?: boolean;
  resolve: (v: string | null) => void;
}
interface ConfirmReq {
  kind: "confirm";
  title: string;
  message?: string;
  confirmLabel: string;
  danger: boolean;
  resolve: (v: boolean) => void;
}
interface ChoiceReq {
  kind: "choice";
  title: string;
  options: ModalOption[];
  // How the options are laid out. "row" is the two-or-three-option form (wide tiles side
  // by side, glyph above label) the pane-placement prompt uses. "list" is the menu form:
  // one full-width row per option, glyph beside label, for the counts where a row would
  // give each option ~50px. NOT a wrap of the row form — `.modal-option` is `flex: 1 1 0`,
  // so a wrapped last row stretches its members to full width and an odd count ends in
  // one giant orphan tile.
  layout: "row" | "list";
  resolve: (v: string | null) => void;
}
export type ModalRequest = TextReq | ConfirmReq | ChoiceReq;

const center = createRoot(() => {
  const [request, setRequest] = createSignal<ModalRequest | null>(null);

  // cancelCurrent resolves any open modal with its dismissed value, so opening a new
  // one never leaves a dangling promise.
  const cancelCurrent = () => {
    const r = request();
    if (!r) return;
    setRequest(null);
    if (r.kind === "confirm") r.resolve(false);
    else r.resolve(null);
  };

  const promptText = (o: {
    title: string;
    label?: string;
    initial?: string;
    placeholder?: string;
    confirmLabel?: string;
    caretAtEnd?: boolean;
  }): Promise<string | null> =>
    new Promise((resolve) => {
      cancelCurrent();
      setRequest({
        kind: "text",
        title: o.title,
        label: o.label,
        initial: o.initial ?? "",
        placeholder: o.placeholder,
        confirmLabel: o.confirmLabel ?? "OK",
        caretAtEnd: o.caretAtEnd,
        resolve: (v) => {
          setRequest(null);
          resolve(v);
        },
      });
    });

  const confirmModal = (o: {
    title: string;
    message?: string;
    confirmLabel?: string;
    danger?: boolean;
  }): Promise<boolean> =>
    new Promise((resolve) => {
      cancelCurrent();
      setRequest({
        kind: "confirm",
        title: o.title,
        message: o.message,
        confirmLabel: o.confirmLabel ?? "Confirm",
        danger: o.danger ?? false,
        resolve: (v) => {
          setRequest(null);
          resolve(v);
        },
      });
    });

  const promptChoice = (o: {
    title: string;
    options: ModalOption[];
    layout?: "row" | "list";
  }): Promise<string | null> =>
    new Promise((resolve) => {
      cancelCurrent();
      setRequest({
        kind: "choice",
        title: o.title,
        options: o.options,
        // Defaulted here rather than at the use site so the existing callers keep the
        // exact layout they were written against.
        layout: o.layout ?? "row",
        resolve: (v) => {
          setRequest(null);
          resolve(v);
        },
      });
    });

  // submit/dismiss close the current modal — called by the host (and by tests).
  const submit = (value: string | boolean | null) => {
    const r = request();
    if (!r) return;
    setRequest(null);
    (r.resolve as (v: string | boolean | null) => void)(value);
  };

  return {
    request: request as Accessor<ModalRequest | null>,
    promptText,
    confirmModal,
    promptChoice,
    submit,
    dismiss: cancelCurrent,
  };
});

export const modalRequest = center.request;
export const promptText = center.promptText;
export const confirmModal = center.confirmModal;
export const promptChoice = center.promptChoice;
export const submitModal = center.submit;
export const dismissModal = center.dismiss;
