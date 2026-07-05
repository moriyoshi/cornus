import { A, useNavigate } from "@solidjs/router";
import { For, Show, onCleanup, onMount, type ParentProps } from "solid-js";
import {
  allCommands,
  armed,
  handlePrefixKey,
  paletteOpen,
  registerCommands,
  setPaletteOpen,
  type Command,
} from "./command-center";
import { settings, setPassBrowserShortcuts, setPrefixEnabled } from "./settings";
import { modalRequest } from "./modal";
import CommandPalette from "./views/terminal/CommandPalette";
import ModalHost from "./views/ModalHost";

// Sidebar links, also the source for the palette's "Go to" commands.
const NAV = [
  { path: "/", label: "Overview" },
  { path: "/files", label: "Files" },
  { path: "/terminal", label: "Terminal" },
  { path: "/settings", label: "Settings" },
];

export default function App(props: ParentProps) {
  const navigate = useNavigate();

  // App-wide prefix key: one capture-phase listener drives the state machine for
  // every keydown that is NOT inside a terminal (xterm advances the same machine
  // itself via Term's key handler) and NOT while the palette is open (it owns its
  // keys). Only "swallow" is actionable here — drop the prefix / ">" so nothing
  // else sees it; "browser" just lets the event through.
  const onDocKeydown = (e: KeyboardEvent) => {
    if (paletteOpen() || modalRequest()) return;
    const t = e.target as Element | null;
    if (t && typeof t.closest === "function" && t.closest(".xterm")) return;
    if (handlePrefixKey(e) === "swallow") {
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
      <nav class="sidebar">
        <div class="brand">
          <img class="brand-mark" src="/cornus-logo.svg" alt="" width="24" height="24" />
          <span class="brand-name">Cornus</span>
        </div>
        <For each={NAV}>
          {(n) => (
            <A href={n.path} end={n.path === "/"} activeClass="active">
              {n.label}
            </A>
          )}
        </For>
      </nav>
      <main>{props.children}</main>
      <Show when={armed()}>
        <div class="prefix-badge" role="status" aria-live="polite">
          prefix armed — press <kbd>&gt;</kbd> for commands, or a browser shortcut
        </div>
      </Show>
      <Show when={paletteOpen()}>
        <CommandPalette commands={allCommands()} onClose={() => setPaletteOpen(false)} />
      </Show>
      <Show when={modalRequest()} keyed>
        {(req) => <ModalHost req={req} />}
      </Show>
    </>
  );
}
