import { For } from "solid-js";
import {
  settings,
  setPassBrowserShortcuts,
  setPrefixEnabled,
  setPrefix,
  setNewPaneSide,
  type NewPaneSide,
} from "../settings";
import { PREFIX_PRESETS } from "./terminal/prefix";

const NEW_PANE_SIDES: { value: NewPaneSide; label: string }[] = [
  { value: "auto", label: "Auto (follows text direction)" },
  { value: "right", label: "Right" },
  { value: "left", label: "Left" },
];

// Settings is the global preferences screen. Options here are persisted and read
// by the relevant screens (e.g. the Terminal workspace reads the terminal ones).
export default function Settings() {
  return (
    <>
      <h1>Settings</h1>
      <div class="cards">
        <div class="card">
          <h3>Terminal</h3>
          <label class="setting-row">
            <input
              type="checkbox"
              checked={settings().passBrowserShortcuts}
              onChange={(e) => setPassBrowserShortcuts(e.currentTarget.checked)}
            />
            <span class="setting-text">
              <span class="setting-title">Pass browser shortcuts</span>
              <span class="muted">
                When on, browser shortcuts (Ctrl/Cmd+T, Ctrl+W, zoom, tab switching…) go to the
                browser instead of the terminal. Off by default, so a terminal pane captures every
                key.
              </span>
            </span>
          </label>

          <label class="setting-row">
            <input
              type="checkbox"
              checked={settings().prefixEnabled}
              onChange={(e) => setPrefixEnabled(e.currentTarget.checked)}
            />
            <span class="setting-text">
              <span class="setting-title">Prefix key</span>
              <span class="muted">
                A tmux-style prefix. Press it, then a tmux second key (<kbd>%</kbd> splits left /
                right, <kbd>"</kbd> top / bottom, <kbd>c</kbd> new pane, <kbd>x</kbd> close), or a
                browser shortcut to send that shortcut to the browser, or <kbd>&gt;</kbd> to open the
                command menu. The default <kbd>Ctrl+Shift+X</kbd> is chosen so it never clashes with
                tmux, screen, or readline inside a pane.
              </span>
            </span>
          </label>
          <div class="setting-row setting-sub">
            <label class="field">
              <span>Prefix combination</span>
              <select
                value={settings().prefix}
                disabled={!settings().prefixEnabled}
                onChange={(e) => setPrefix(e.currentTarget.value)}
              >
                <For each={PREFIX_PRESETS}>{(p) => <option value={p}>{p}</option>}</For>
              </select>
            </label>
          </div>
        </div>

        <div class="card">
          <h3>Workspace</h3>
          <div class="setting-row setting-sub">
            <label class="field">
              <span>New pane placement side</span>
              <select
                value={settings().newPaneSide}
                onChange={(e) => setNewPaneSide(e.currentTarget.value as NewPaneSide)}
              >
                <For each={NEW_PANE_SIDES}>
                  {(o) => <option value={o.value}>{o.label}</option>}
                </For>
              </select>
            </label>
            <span class="muted">
              Where a "Split" places a new pane (Terminal and Files) when you choose Split in the
              placement prompt. Auto puts it on the right, or left in a right-to-left layout.
            </span>
          </div>
        </div>
      </div>
    </>
  );
}
