import { A, useNavigate } from "@solidjs/router";
import { For, Show, onCleanup, onMount, type ParentProps } from "solid-js";
import {
  allCommands,
  armed,
  dispatchAppKey,
  paletteOpen,
  paletteQuery,
  registerCommands,
  setPaletteOpen,
  type Command,
} from "./command-center";
import { settings, setPassBrowserShortcuts, setPrefixEnabled } from "./settings";
import { modalRequest } from "./modal";
import CommandPalette from "./views/terminal/CommandPalette";
import ModalHost from "./views/ModalHost";
import Toaster from "./views/Toaster";

// Header nav links, also the source for the palette's "Go to" commands.
const NAV = [
  { path: "/", label: "Overview" },
  { path: "/metrics", label: "Metrics" },
  { path: "/workspace", label: "Workspace" },
  { path: "/settings", label: "Settings" },
];

export default function App(props: ParentProps) {
  const navigate = useNavigate();

  // App-wide command keys: one capture-phase listener, for every keydown that is NOT
  // inside a terminal (xterm advances the same prefix machine itself via Term's key
  // handler) and NOT while the palette or a modal is open (they own their keys). Those
  // three exclusions are this file's whole share of the decision — which component holds
  // the keyboard. Everything after it (the prefix machine, then the unprefixed `direct`
  // keys, in that order and with that gating) is dispatchAppKey in command-center, where
  // it can be tested without an App to mount.
  const onDocKeydown = (e: KeyboardEvent) => {
    if (paletteOpen() || modalRequest()) return;
    const t = e.target as Element | null;
    if (t && typeof t.closest === "function" && t.closest(".xterm")) return;
    if (dispatchAppKey(e)) {
      e.preventDefault();
      e.stopPropagation();
    }
  };
  onMount(() => document.addEventListener("keydown", onDocKeydown, true));
  onCleanup(() => document.removeEventListener("keydown", onDocKeydown, true));

  // Always-available commands: navigation and the global toggles. Registered as a
  // provider so the toggle labels track the current setting.
  const globalCommands = (): Command[] => [
    ...NAV.map((n) => ({
      id: `goto:${n.path}`,
      group: "Go to",
      title: n.label,
      run: () => navigate(n.path),
    })),
    {
      id: "settings:pass-browser-shortcuts",
      group: "Settings",
      title: settings().passBrowserShortcuts
        ? "Keep browser shortcuts in the terminal"
        : "Pass browser shortcuts to the browser",
      keywords: "terminal keys chrome tab zoom",
      run: () => setPassBrowserShortcuts(!settings().passBrowserShortcuts),
    },
    {
      id: "settings:prefix-enabled",
      group: "Settings",
      title: settings().prefixEnabled ? "Disable the prefix key" : "Enable the prefix key",
      keywords: "tmux command menu",
      run: () => setPrefixEnabled(!settings().prefixEnabled),
    },
  ];
  onMount(() => onCleanup(registerCommands(globalCommands)));

  return (
    <>
      <header class="appbar">
        <div class="brand">
          <img class="brand-mark" src="/cornus-logo.svg" alt="" width="24" height="24" />
          <span class="brand-name">Cornus</span>
        </div>
        <nav class="appbar-nav">
          <For each={NAV}>
            {(n) => (
              <A href={n.path} end={n.path === "/"} activeClass="active">
                {n.label}
              </A>
            )}
          </For>
        </nav>
      </header>
      <main>{props.children}</main>
      <Show when={armed()}>
        <div class="prefix-badge" role="status" aria-live="polite">
          prefix armed — press <kbd>&gt;</kbd> for commands, or a browser shortcut
        </div>
      </Show>
      <Show when={paletteOpen()}>
        <CommandPalette
          commands={allCommands()}
          initialQuery={paletteQuery()}
          onClose={() => setPaletteOpen(false)}
        />
      </Show>
      <Show when={modalRequest()} keyed>
        {(req) => <ModalHost req={req} />}
      </Show>
      <Toaster />
    </>
  );
}
