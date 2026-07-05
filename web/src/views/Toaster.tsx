import { For, Show } from "solid-js";
import { toasts, dismissToast } from "../toast";

// Toaster is the single host for the app's transient messages (see toast.ts). It is
// mounted once, next to ModalHost, and floats over everything: nothing it shows may push
// the page around, which is the whole reason these messages left the panes.
export default function Toaster() {
  return (
    <Show when={toasts().length}>
      <div class="toaster" role="status" aria-live="polite">
        <For each={toasts()}>
          {(t) => (
            <button
              type="button"
              class="toast"
              classList={{ error: t.kind === "error" }}
              title="Dismiss"
              onClick={() => dismissToast(t.id)}
            >
              {t.text}
            </button>
          )}
        </For>
      </div>
    </Show>
  );
}
