import { For } from "solid-js";
import {
  settings,
  setNewPaneDisposition,
  setPassBrowserShortcuts,
  setPrefixEnabled,
  setPrefix,
  setPaneNumbersInTabs,
  setPaneMiniMap,
  setPaneChooserPinned,
  setPaneChooserSide,
  setPaneZoom,
  setShellCandidates,
  setTerminalFont,
  setTerminalFontSize,
  setTerminalLineHeight,
  setWorkspaceGrowth,
  type ChooserSide,
  type PaneDisposition,
  type WorkspaceGrowth,
} from "../settings";
import {
  TERM_FONT_SIZES,
  TERM_LINE_HEIGHTS,
  TERMINAL_FONTS,
  termFont,
  termFontSize,
  termLineHeight,
  type TerminalFontId,
} from "../components/termFont";
import { PREFIX_PRESETS } from "./terminal/prefix";
import { DEFAULT_SHELL_CANDIDATES } from "./terminal/shells";

// Settings is the global preferences screen. Options here are persisted and read
// by the relevant screens (e.g. the Terminal workspace reads the terminal ones).
//
// The screen is a stack of plain `.setting-group` sections, not a `.cards` grid. A
// card grid is for independent readouts meant to be scanned side by side, and its
// `minmax(260px, 1fr)` columns make every panel narrower than the page — but a
// setting is a sentence of prose with a control on the end of it, read top to
// bottom, so it wants the page's full measure.
//
// A group is a heading and its rows, with NO rule above it — deliberately not the
// `.section` band the Overview and the workload detail page use. Those separate
// unlike things (a project from a project, Spec from Logs); here every group holds
// the same kind of thing and the heading already says where one ends, so a divider
// would be scaffolding around two items.
//
// Every setting is one `.setting-row` with the same shape whatever kind of
// control it carries: its name, what it does, then the control. A checkbox leads
// the row because the box IS the control's affordance and reads as a checklist; a
// select follows the description, left-aligned at its own width. So a new group is
// a copy of an existing one.
export default function Settings() {
  return (
    <>
      <h1>Settings</h1>

      {/* Workspace, not Terminal: the tiled chrome is the same on the Files screen, so a
          setting about tabs belongs to neither one of them alone. This group came back for
          it — the previous one went out with the placement prompt its only row configured. */}
      <section class="setting-group">
        <h2>Workspace</h2>

        {/* A select, not checkboxes and not a radio group: they are one answer to one
            question, and the row shape for "pick one of a short closed list" is already the
            prefix combination's. The options are phrased as the ANSWER each gives, in the
            order Ask / side / tab / by device, so reading down the list says what the
            workspace will do rather than what the setting is called. The device-dependent one
            comes last because it is the two before it put together, and reads as such. */}
        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">New pane placement</span>
            <span class="muted">
              Where a pane goes when a command makes one — <strong>New pane</strong>,{" "}
              <strong>Open</strong>, <strong>Open in a terminal</strong>. Asking lights every
              tile with placement targets and waits: an arrow (or <kbd>hjkl</kbd>) splits
              beside the tile you are on, <kbd>Space</kbd> puts it there as a tab,{" "}
              <kbd>Esc</kbd> changes your mind. The other two answer that question once, in
              advance, and skip the prompt. The last answers it per device — a phone or tablet
              has neither the width for two panes nor an easy way to resize them, and the desk
              it syncs to has both. Splitting is unaffected whichever you pick —{" "}
              <strong>Split pane</strong> already says which disposition it makes, and only
              asks which edge.
            </span>
            <select
              value={settings().newPaneDisposition}
              onChange={(e) => setNewPaneDisposition(e.currentTarget.value as PaneDisposition)}
            >
              <option value="ask">Ask where it goes</option>
              <option value="split">Always side by side</option>
              <option value="tab">Always as a tab</option>
              <option value="auto">As a tab on touch devices, side by side on others</option>
            </select>
          </span>
        </label>

        {/* Placement's neighbour, and directly under it, because the two together are the whole
            of "what happens when I make a pane": that one answers WHERE it goes, this one answers
            what it COSTS. Kept apart rather than folded into one list because they compose —
            every placement is available under either growth — and a single control offering the
            product of them would have six rows for two questions. */}
        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">New pane sizing</span>
            <span class="muted">
              Whether a new pane makes the workspace bigger or takes its space from the pane it
              came from. <strong>Extend the workspace</strong> keeps the pane you split at its
              current size, adds the new one alongside at two thirds of it, and lets the workspace
              grow past the screen — which then scrolls, following the focus. Panes along the edge
              it grew at stretch to meet it; everything else stays exactly where it was.{" "}
              <strong>Divide the pane</strong> is the tmux behaviour: the tree always fits the
              screen, so each new pane halves the one you were on.
            </span>
            <select
              value={settings().workspaceGrowth}
              onChange={(e) => setWorkspaceGrowth(e.currentTarget.value as WorkspaceGrowth)}
            >
              <option value="extend">Extend the workspace</option>
              <option value="divide">Divide the pane</option>
            </select>
          </span>
        </label>

        <label class="setting-row">
          <input
            type="checkbox"
            checked={settings().paneNumbersInTabs}
            onChange={(e) => setPaneNumbersInTabs(e.currentTarget.checked)}
          />
          <span class="setting-text">
            <span class="setting-title">Pane numbers on tabs</span>
            <span class="muted">
              Every tab shows its pane's number, so <kbd>prefix</kbd> <kbd>s</kbd> then that
              digit goes straight to it. Turning this off leaves the numbers in the pane
              chooser's own list and the large one it draws on each tile — those are how a row
              and a pane are matched up; only the standing copy in the tab bar goes.
            </span>
          </span>
        </label>

        <label class="setting-row">
          <input
            type="checkbox"
            checked={settings().paneMiniMap}
            onChange={(e) => setPaneMiniMap(e.currentTarget.checked)}
          />
          <span class="setting-text">
            <span class="setting-title">Mini map in the pane chooser</span>
            <span class="muted">
              <kbd>prefix</kbd> <kbd>s</kbd> draws the whole workspace above its list, one
              rectangle per tile, with a frame around the part of it that is on screen. Worth
              turning on once the workspace is wider than the display and the list can no
              longer say where a pane is; on a workspace that fits, the tiles are already in
              front of you.
            </span>
          </span>
        </label>

        {/* The chooser's other switch, and the one that changes what it IS rather than what it
            draws — hence its own row rather than a third option on the map's. */}
        <label class="setting-row">
          <input
            type="checkbox"
            checked={settings().paneChooserPinned}
            onChange={(e) => setPaneChooserPinned(e.currentTarget.checked)}
          />
          <span class="setting-text">
            <span class="setting-title">Pin the pane chooser</span>
            <span class="muted">
              Stand the chooser permanently in a gutter at the side of the screen instead of
              floating it over the tiles for the length of a keystroke. Pinned, it lists every
              pane whether or not you asked for one, marks the pane you are in, and goes there
              when you click a row — and it covers nothing, because the workspace gives up the
              gutter's width rather than passing underneath it. <kbd>prefix</kbd> <kbd>s</kbd>
              still walks the tiles; the walk reports into this panel. The pin in the panel's own
              corner does the same thing as this box. Desktop only: it needs a mouse and a window
              wide enough to spare a column, so on a phone or a tablet the chooser floats however
              this is set.
            </span>
          </span>
        </label>

        {/* Subordinate to the toggle above — tucked up against it, and dead while it is off,
            exactly as the prefix combination is under the prefix. */}
        <label class="setting-row setting-sub">
          <span class="setting-text">
            <span class="setting-title">Gutter side</span>
            <span class="muted">
              Which side the pinned chooser stands on. <strong>Automatic</strong> puts it at the
              start of the reading direction — the left in a left-to-right language, the right in
              a right-to-left one — which is the corner the chooser floats in when it is not
              pinned, so pinning moves it outward rather than across the screen.
            </span>
            <select
              value={settings().paneChooserSide}
              disabled={!settings().paneChooserPinned}
              onChange={(e) => setPaneChooserSide(e.currentTarget.value as ChooserSide)}
            >
              <option value="auto">Automatic</option>
              <option value="left">Left</option>
              <option value="right">Right</option>
            </select>
          </span>
        </label>

        {/* Last in the group, and the only row here that gives something up to be turned on —
            hence the description says so plainly rather than selling the feature. */}
        <label class="setting-row">
          <input
            type="checkbox"
            checked={settings().paneZoom}
            onChange={(e) => setPaneZoom(e.currentTarget.checked)}
          />
          <span class="setting-text">
            <span class="setting-title">Zoom pane contents</span>
            <span class="muted">
              Pinch a terminal, an editor or an image preview to change how big its contents
              are drawn — on a trackpad, <kbd>Ctrl</kbd> and scroll. It is a text size, not a
              magnifying glass: a terminal re-flows to the new size and tells the shell its new
              width, so nothing is cut off. Each pane keeps its own level for the session, and{" "}
              <strong>Zoom in</strong>, <strong>Zoom out</strong> and{" "}
              <strong>Reset zoom</strong> join the command palette and every tile's{" "}
              <kbd>⋮</kbd> menu while this is on. The cost: over those three kinds of pane, a
              pinch no longer zooms the page the way the browser normally would.
            </span>
          </span>
        </label>
      </section>

      <section class="setting-group">
        <h2>Terminal</h2>

        {/* First in the group: it is what people open this screen for, and unlike the rows
            below it changes nothing about what the keyboard does. The two type rows sit
            together because they are one decision made twice — a face and the air around
            it — and because the preview under them answers both at once. */}
        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">Font</span>
            <span class="muted">
              The typeface every terminal pane is painted in.{" "}
              <strong>Browser-builtin Monospace</strong> is the one your browser is set to use
              for monospaced text, and is the default — the other five ship with Cornus, so
              they render the same on every machine you open this on and need no network. Each
              falls back to your browser's monospace for anything it has no glyph for, which
              for most of them includes the box-drawing characters a full-screen program
              paints its frames with.
            </span>
            {/* Resolved through termFont rather than bound to the raw setting: an id this
                build does not know matches no option, and a select whose value matches
                nothing shows its first entry — which would quietly disagree with the font
                the terminal is actually using. */}
            <select
              value={termFont(settings().terminalFont).id}
              onChange={(e) => setTerminalFont(e.currentTarget.value as TerminalFontId)}
            >
              <For each={TERMINAL_FONTS}>{(f) => <option value={f.id}>{f.label}</option>}</For>
            </select>
          </span>
        </label>

        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">Font size</span>
            <span class="muted">
              How big terminal text is drawn, in pixels. This is the size every terminal
              STARTS at — where pane zoom is switched on, zooming a pane still moves it up and
              down from here, and each pane keeps its own level for the session. Browser page
              zoom is unaffected either way: a terminal paints its glyphs itself, so it is the
              one part of this UI that page zoom does not reach.
            </span>
            <select
              value={String(termFontSize(settings().terminalFontSize))}
              onChange={(e) => setTerminalFontSize(Number(e.currentTarget.value))}
            >
              <For each={TERM_FONT_SIZES}>
                {(s) => <option value={String(s)}>{s} px</option>}
              </For>
            </select>
          </span>
        </label>

        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">Line height</span>
            <span class="muted">
              How much room each row gets, as a multiple of the font size.{" "}
              <strong>1.0</strong> is the packed grid a terminal normally has; the steps above
              it trade rows on screen for legibility. Panes re-flow as soon as you pick one,
              and the shell is told the new size, so nothing is cut off.
            </span>
            <select
              value={String(termLineHeight(settings().terminalLineHeight))}
              onChange={(e) => setTerminalLineHeight(Number(e.currentTarget.value))}
            >
              <For each={TERM_LINE_HEIGHTS}>
                {(h) => <option value={String(h)}>{h.toFixed(2).replace(/0$/, "")}</option>}
              </For>
            </select>
          </span>
        </label>

        {/* A live sample rather than a swatch in each dropdown row. Styling the options
            themselves would mean the page fetches all five faces to draw a list most people
            scroll past once; this fetches the one that is actually selected. The content is
            terminal-shaped on purpose — a prompt, the ligature pairs the coding faces differ
            most visibly on, and the 0/O and 1/l/I pairs that are the whole reason to change
            a terminal font. It answers all three controls above it, which is also why it is
            one sample rather than a swatch on each.

            Its own class rather than a `.setting-row`, because it is not a setting: a row
            is a `<label>` wrapping a control, and a label with nothing to focus is a click
            target that does nothing. `.setting-preview` borrows only the row's grid, so the
            sample still lines up with the titles above it. */}
        <div class="setting-preview">
          <span class="setting-text">
            <span class="setting-title">Preview</span>
            <pre
              class="term-font-preview"
              style={{
                "font-family": termFont(settings().terminalFont).stack,
                // px, matching the terminal exactly rather than a token that approximates
                // it: the whole point of showing a size is that it is the size you will get.
                "font-size": `${termFontSize(settings().terminalFontSize)}px`,
                "line-height": String(termLineHeight(settings().terminalLineHeight)),
              }}
            >
              {"$ git commit -m 'fix: 0O 1lI |= => != <= ~/.config'\n"}
              {"  ├─ 3 files changed, 128 insertions(+), 7 deletions(-)\n"}
              {"  └─ HEAD -> main [a1b2c3d] \"Il1 O0 {}[]()<> &&||\""}
            </pre>
          </span>
        </div>

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

        {/* Subordinate to the toggle above — tucked up against it, and dead while
            it is off. */}
        <label class="setting-row setting-sub">
          <span class="setting-text">
            <span class="setting-title">Prefix combination</span>
            <select
              value={settings().prefix}
              disabled={!settings().prefixEnabled}
              onChange={(e) => setPrefix(e.currentTarget.value)}
            >
              <For each={PREFIX_PRESETS}>{(p) => <option value={p}>{p}</option>}</For>
            </select>
          </span>
        </label>

        {/* A textarea rather than a select: this is a list of paths, and the images
            people run are not enumerable. Same row shape as the select above. */}
        <label class="setting-row">
          <span class="setting-text">
            <span class="setting-title">Shell candidates</span>
            <span class="muted">
              One per line, most preferred first. Opening a terminal probes these inside the
              workload and connects to the first one the image actually has — so an image with
              bash gives you bash, and one with only busybox still gives you a shell. A
              workload's own <code>x-cornus-shells:</code>, the shell its entrypoint names, and
              your connection profile's list are all tried ahead of these. When nothing is
              found, the pane asks you for a command instead.
            </span>
            <textarea
              class="setting-textarea"
              rows="14"
              spellcheck={false}
              value={settings().shellCandidates}
              onInput={(e) => setShellCandidates(e.currentTarget.value)}
            />
            <span>
              <button
                disabled={settings().shellCandidates === DEFAULT_SHELL_CANDIDATES}
                onClick={() => setShellCandidates(DEFAULT_SHELL_CANDIDATES)}
              >
                Restore defaults
              </button>
            </span>
          </span>
        </label>
      </section>

    </>
  );
}
