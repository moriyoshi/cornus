import { Switch, Match, For, Show, createSignal, onMount } from "solid-js";
import { submitModal, dismissModal, type ModalRequest } from "../modal";

// ModalHost renders the current modal request — a text prompt, a confirm, or a choice.
// Mounted once in App.tsx via a keyed <Show>, so it remounts per request (fresh input /
// selection). Backdrop click and Esc dismiss; the panel traps keyboard focus. Modeled
// on CommandPalette (backdrop cancel + prevFocus restore).
export default function ModalHost(props: { req: ModalRequest }) {
  let prevFocus: HTMLElement | null = null;
  onMount(() => {
    prevFocus = document.activeElement as HTMLElement | null;
  });

  const cancel = () => {
    dismissModal();
    prevFocus?.focus?.();
  };
  // submitModal may move focus (e.g. into a new pane), so only restore on cancel.

  return (
    <div class="modal-overlay" onMouseDown={cancel}>
      <div
        class="modal-panel"
        role="dialog"
        aria-label={props.req.title}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <p class="modal-title">{props.req.title}</p>
        <Switch>
          <Match when={props.req.kind === "text"}>
            <TextBody req={props.req as Extract<ModalRequest, { kind: "text" }>} onCancel={cancel} />
          </Match>
          <Match when={props.req.kind === "confirm"}>
            <ConfirmBody req={props.req as Extract<ModalRequest, { kind: "confirm" }>} onCancel={cancel} />
          </Match>
          <Match when={props.req.kind === "choice"}>
            <ChoiceBody req={props.req as Extract<ModalRequest, { kind: "choice" }>} onCancel={cancel} />
          </Match>
        </Switch>
      </div>
    </div>
  );
}

function TextBody(props: { req: Extract<ModalRequest, { kind: "text" }>; onCancel: () => void }) {
  const [value, setValue] = createSignal(props.req.initial);
  let input!: HTMLInputElement;
  onMount(() => {
    input.focus();
    input.select();
  });
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      submitModal(value());
    } else if (e.key === "Escape") {
      e.preventDefault();
      props.onCancel();
    }
  };
  return (
    <>
      <Show when={props.req.label}>
        <label class="modal-label" for="modal-text-input">
          {props.req.label}
        </label>
      </Show>
      <input
        id="modal-text-input"
        class="modal-input"
        ref={input}
        value={value()}
        placeholder={props.req.placeholder}
        onInput={(e) => setValue(e.currentTarget.value)}
        onKeyDown={onKeyDown}
      />
      <div class="modal-actions">
        <button onClick={props.onCancel}>Cancel</button>
        <button class="primary" onClick={() => submitModal(value())}>
          {props.req.confirmLabel}
        </button>
      </div>
    </>
  );
}

function ConfirmBody(props: { req: Extract<ModalRequest, { kind: "confirm" }>; onCancel: () => void }) {
  let confirmBtn!: HTMLButtonElement;
  onMount(() => confirmBtn.focus());
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      props.onCancel();
    }
  };
  return (
    <div onKeyDown={onKeyDown}>
      <Show when={props.req.message}>
        <p class="modal-message">{props.req.message}</p>
      </Show>
      <div class="modal-actions">
        <button onClick={props.onCancel}>Cancel</button>
        <button
          ref={confirmBtn}
          classList={{ primary: !props.req.danger, danger: props.req.danger }}
          onClick={() => submitModal(true)}
        >
          {props.req.confirmLabel}
        </button>
      </div>
    </div>
  );
}

function ChoiceBody(props: { req: Extract<ModalRequest, { kind: "choice" }>; onCancel: () => void }) {
  const [sel, setSel] = createSignal(0);
  let panel!: HTMLDivElement;
  onMount(() => panel.focus());
  const n = () => props.req.options.length;
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      props.onCancel();
    } else if (e.key === "ArrowLeft") {
      e.preventDefault();
      setSel((s) => (s + n() - 1) % n());
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      setSel((s) => (s + 1) % n());
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      submitModal(props.req.options[sel()].value);
    }
  };
  return (
    <div class="modal-choice" tabindex={-1} ref={panel} onKeyDown={onKeyDown}>
      <div class="modal-options">
        <For each={props.req.options}>
          {(o, i) => (
            <button
              type="button"
              class="modal-option"
              classList={{ focused: sel() === i() }}
              onMouseEnter={() => setSel(i())}
              onClick={() => submitModal(o.value)}
            >
              <Show when={o.glyph}>
                <span class="modal-glyph" aria-hidden="true">{o.glyph}</span>
              </Show>
              <span class="modal-option-label">{o.label}</span>
            </button>
          )}
        </For>
      </div>
      <p class="modal-hint muted">← → to move · Enter to choose · Esc to cancel</p>
    </div>
  );
}
