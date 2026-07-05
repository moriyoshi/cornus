import { describe, it, expect, beforeEach, afterEach, vi, onTestFinished } from "vitest";
// The stylesheet as text, for the mobile-table rule asserted at the foot of this
// file. `?raw` needs vitest.config's `css.include` to reach it — see there.
import cssSource from "../styles.css?raw";
import { render, screen, cleanup, fireEvent, waitFor, within, createEvent } from "@solidjs/testing-library";
import { Router, Route } from "@solidjs/router";
import { Show, type Component } from "solid-js";
import { EditorView } from "@codemirror/view";
import {
  installMockFetch,
  liveTerminals,
  seedServerInfo,
  seedTerminals,
  seedTunnelBanners,
  setTerminalCwd,
  setTerminalState,
  setTerminalTitle,
  terminalCreates,
} from "../mock/handler";
import Overview from "./Overview";
import WorkloadDetail from "./WorkloadDetail";
import Workspace, { STORAGE_KEY as WS_LAYOUT_KEY, type PaneData } from "./Workspace";
import Settings from "./Settings";
import {
  setNewPaneDisposition,
  setPassBrowserShortcuts,
  setPrefixEnabled,
  setPrefix,
  setPaneNumbersInTabs,
  setPaneZoom,
} from "../settings";
import { ZOOM_HOME, ZOOM_SCALES, keepZooms, zoomLevels } from "./tiling/zoom";
import { PINCH_STEP_RATIO } from "../pinch";
import {
  allCommands,
  bindsOf,
  directsOf,
  dispatchAppKey,
  handlePrefixKey,
  disarm,
  paletteOpen,
  paletteQuery,
  setPaletteOpen,
} from "../command-center";
import CommandPalette from "./terminal/CommandPalette";
import {
  SPLIT_ARM_DELAY_MS,
  SPLIT_HOLD_MS,
  SPLIT_HOT_MS,
  SPLIT_TAP_MS,
  SPLIT_HEAT_MAX,
  PANE_TAG,
  type DropZone,
} from "./tiling/panes";
import { DRAG_LIFT_MS, DRAG_SLOP_PX } from "../dnd";
import { submitModal, modalRequest, dismissModal, promptChoice } from "../modal";
import ModalHost from "./ModalHost";
import { clearToasts } from "../toast";
import Toaster from "./Toaster";

// runCommand invokes a contextual palette command by id (the Files workspace exposes
// its actions there instead of as on-screen buttons).
// block returns the body of the at-rule whose header starts with `header`, matched brace
// by brace so a nested rule cannot end it early. jsdom's CSS parser hands back an empty
// sheet for styles.css, so the CSSOM is not available to ask this question of. Shared by
// the Stylesheet suite and the touch suite, both of which assert rules as text.
function block(css: string, header: string): string {
  const start = css.indexOf(header);
  expect(start, `no ${header} block`).toBeGreaterThanOrEqual(0);
  const open = css.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  throw new Error(`unterminated ${header} block`);
}

// Resolve a CSS length that may be written as a `var(--space-N)` token, so a test
// can compare two spacings without hard-coding either one's current value.
function px(css: string, value: string): number {
  const token = value.match(/var\((--[\w-]+)\)/);
  const raw = token
    ? new RegExp(`${token[1]}:\\s*(\\d+)px`).exec(css)?.[1]
    : /(\d+)px/.exec(value)?.[1];
  expect(raw, `could not resolve ${value}`).toBeDefined();
  return Number(raw);
}

function runCommand(id: string) {
  const cmd = allCommands().find((c) => c.id === id);
  if (!cmd) throw new Error(`command not registered: ${id}`);
  // Calling run() on a disabled command reaches a path no user can: the palette will not
  // press it and a bind will not fire it. A test that did so would be exercising code the
  // app has already decided is unreachable, and would keep passing after the guard broke.
  if (cmd.disabled) throw new Error(`command is disabled (${cmd.disabled}): ${id}`);
  cmd.run();
}

// keyEvent / pressBind drive the REAL prefix path rather than calling a command
// object, so a test asserts that the key is bound — not merely that some command with
// that id exists. pressBind returns the disposition ("swallow" means a bind consumed
// the key; "browser" means nothing claimed it and it fell through).
function keyEvent(over: Partial<KeyboardEvent> & { key: string }): KeyboardEvent {
  return { ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...over } as KeyboardEvent;
}
// `shift` defaults true because the first binds to need this were "%" and '"', which are
// both shifted characters. It is a real parameter and not a constant because `c` and `C`
// are now two different commands: a browser reports Shift+c as key "C", so pressing "c"
// with shiftKey set is an event no keyboard can produce, and a test built on one proves
// nothing about telling the pair apart.
function pressBind(key: string, shift = true, mods: Partial<KeyboardEvent> = {}) {
  disarm();
  handlePrefixKey(keyEvent({ key: "X", ctrlKey: true, shiftKey: true })); // the default prefix
  return handlePrefixKey(keyEvent({ key, shiftKey: shift, ...mods }));
}
// pressDirect drives an UNPREFIXED command key — Files' F5 / F6 — through the same
// dispatcher App.tsx's one document listener uses, minus the "who holds the keyboard"
// guards that belong to the component tree. NO PREFIX IS SENT, which is the point: a test
// that armed first would pass against a plain post-prefix bind. It returns whether a
// command claimed the key, and that half is as much the assertion as the effect — an
// unclaimed F5 is a page reload.
function pressDirect(key: string, target: Element = document.body, mods: Partial<KeyboardEvent> = {}) {
  disarm();
  return dispatchAppKey(keyEvent({ key, target, ...mods }));
}

// These tests render the real Solid views against the mocked BFF (src/mock),
// proving the frontend turns /.cornus/web/* payloads into the expected DOM
// without any backend. Views use <A> and resources, so each is mounted inside a
// Router; findBy* waits out the mocked fetch's microtask resolution.

let restore: () => void;
beforeEach(() => {
  restore = installMockFetch();
});
afterEach(() => {
  cleanup();
  restore();
  // The modal service is a module singleton, and cleanup() only tears down the DOM: a test
  // that ends with a question on screen (several deliberately do) would otherwise leave the
  // request armed for the next one, which then renders a dialog it never asked for. That is
  // invisible until a test asserts NO dialog — exactly what the placement tests below do.
  dismissModal();
});

// The wrapper mounts Toaster because App.tsx does: transient outcomes ("copied 2 items",
// a refused transfer) render there now, not inside the pane that produced them, so a test
// that leaves it out cannot see them at all. ModalHost is mounted for the same reason and
// with App.tsx's own keyed <Show>: prompts (confirm a delete, name a folder, keep or
// discard a pane's unsaved work) are how several flows continue, and without the host a
// test can only poke the modal singleton — it cannot see the question, let alone answer
// it by pressing the button a user presses.
function renderView(Comp: Component) {
  return render(() => (
    <>
      <Router>
        <Route path="*" component={Comp} />
      </Router>
      <Show when={modalRequest()} keyed>
        {(req) => <ModalHost req={req} />}
      </Show>
      {/* The palette is mounted for the same reason ModalHost is: it is not decoration but
          a way flows CONTINUE. A tile's ⋮ opens it — that is the pane menu now — so without
          it here a test can only observe that a signal flipped, not that the menu it stands
          for lists the right commands or that pressing one does anything. */}
      <Show when={paletteOpen()}>
        <CommandPalette
          commands={allCommands()}
          initialQuery={paletteQuery()}
          onClose={() => setPaletteOpen(false)}
        />
      </Show>
      <Toaster />
    </>
  ));
}

// The Workspace opens as a single FILE-BROWSER pane, so a test about terminals has to say
// so: renderTerm seeds the layout with one empty terminal pane — the state the old
// Terminal screen started in — and then mounts the same screen renderView does. It is a
// separate helper rather than a flag because the seeding must land after the suite's
// localStorage.clear() and before the store reads it, which is exactly here.
function seedPanes(data: PaneData[], focused = 0) {
  const panes = data.map((d) => ({ id: `seed-${Math.random().toString(36).slice(2, 8)}`, data: d }));
  globalThis.localStorage?.setItem(
    WS_LAYOUT_KEY,
    JSON.stringify({
      tree: { type: "stack", id: "seed-stack", panes, active: focused },
      focused: panes[focused].id,
    }),
  );
  return panes;
}

function renderTerm(data: PaneData[] = [{ kind: "term", workload: "", cmd: [] }]) {
  seedPanes(data);
  return renderView(Workspace);
}

// expectAlignedColumns pins the invariant that survives every column change: each
// body row carries exactly as many cells as the header has. MountTable and
// ForwardsView drop their identifying columns from <thead> and <tbody> through
// SEPARATE <Show> blocks, so dropping one and not the other misaligns every row
// under a correct-looking header — a silent shift that reads as real data.
function expectAlignedColumns(tables: Element[]) {
  for (const t of tables) {
    const cols = t.querySelectorAll("thead th").length;
    expect(cols).toBeGreaterThan(0);
    const rows = [...t.querySelectorAll("tbody tr")];
    expect(rows.length).toBeGreaterThan(0);
    for (const r of rows) {
      expect(r.querySelectorAll("td").length).toBe(cols);
    }
  }
}

// The dialog and its buttons: a prompt is answered the way a user answers it.
const dialog = () => screen.findByRole("dialog");
const noDialog = () => expect(screen.queryByRole("dialog")).toBeNull();
async function answer(button: string) {
  fireEvent.click(within(await dialog()).getByRole("button", { name: button }));
  await waitFor(noDialog);
}

// closeTab presses the ✕ on the tab whose label contains `label` — the close gesture the
// gate hangs off. Naming the tab matters: these tests close ONE tab of several, and
// ".tab-close"[0] would silently close whichever happened to be first.
function closeTab(label: string) {
  const tab = Array.from(document.querySelectorAll<HTMLElement>(".tab")).find((t) =>
    t.textContent?.includes(label),
  );
  if (!tab) throw new Error(`no tab labelled ${label}`);
  fireEvent.click(tab.querySelector<HTMLElement>(".tab-close")!);
}

// --- edge-split overlays ----------------------------------------------------------
// A strip is click-through until the pointer has RESTED on it for SPLIT_ARM_DELAY_MS, so a
// test that merely clicks the button exercises a path no user can reach. dwellOnEdge()
// performs the real gesture: it gives the tile a rect (jsdom lays nothing out, and a zero
// rect never arms), parks the pointer on the requested strip, and lets the dwell elapse.
// jsdom has no PointerEvent, and fireEvent then falls back to a plain Event that silently
// drops clientX/clientY — hence the hand-built MouseEvent, which carries coordinates. Fake
// timers cover only the dwell, leaving the surrounding real-timer awaits untouched.
type Side = "top" | "bottom" | "left" | "right";

const TILE_RECT = {
  left: 0,
  top: 0,
  right: 200,
  bottom: 200,
  width: 200,
  height: 200,
  x: 0,
  y: 0,
  toJSON() {},
} as DOMRect;

// 3px inside the named edge, centred along it — on every strip (the top one is only 10px
// thick), and far enough from the other three that only the intended zone claims it.
const EDGE_POINT: Record<Side, { clientX: number; clientY: number }> = {
  left: { clientX: 3, clientY: 100 },
  right: { clientX: 197, clientY: 100 },
  top: { clientX: 100, clientY: 3 },
  bottom: { clientX: 100, clientY: 197 },
};
const CENTRE = { clientX: 100, clientY: 100 };
// Over the tab bar but BELOW the 10px top strip: the tile's chrome, not the overlay.
const ON_THE_TABS = { clientX: 100, clientY: 15 };

// tileOf returns the .stack an overlay belongs to, with a rect it can be measured against;
// movePointer drives the hover-intent handler, which lives on the tile.
function tileOf(el: HTMLElement) {
  const tile = el.closest<HTMLElement>(".stack")!;
  tile.getBoundingClientRect = () => TILE_RECT;
  return tile;
}
// pointerType is as load-bearing here as the coordinates. The hover dwell is the MOUSE's
// half of the edge-split gesture — a finger holds the bar instead (SPLIT_HOLD_MS) — so the
// handler dispatches on it, and jsdom's MouseEvent reports `undefined`, which is not "mouse".
// Without this every dwell below would take the touch branch and arm nothing.
function movePointer(tile: HTMLElement, at: { clientX: number; clientY: number }) {
  const ev = new MouseEvent("pointermove", { ...at, bubbles: true });
  Object.defineProperty(ev, "pointerType", { value: "mouse" });
  tile.dispatchEvent(ev);
}


function dwellOnEdge(edgeButton: HTMLElement, side: Side) {
  const tile = tileOf(edgeButton);
  vi.useFakeTimers();
  try {
    movePointer(tile, EDGE_POINT[side]);
    vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS + 1);
  } finally {
    vi.useRealTimers();
  }
}

// splitViaEdge dwells, then clicks — the whole gesture a user performs. Used wherever a
// test needs a second tile; the gate itself is asserted in the Terminal workspace.
async function splitViaEdge(side: Side) {
  const btn = await screen.findByRole("button", { name: `Split pane, new pane on the ${side}` });
  dwellOnEdge(btn, side);
  fireEvent.click(btn);
  return btn;
}

// --- the pane menu (the tap-reachable route) --------------------------------------
// The edge overlays above need a 450ms HOVER dwell, and hover is the one thing a touch
// device does not have — pointermove only fires while a finger is down, and the lift
// disarms the strip before the click lands. Everything below is the gesture that has to
// work WITHOUT any of that, so no helper here dispatches a pointermove.

// openPaneMenu taps a tile's ⋮. That no longer opens a menu of its own — it opens the
// command palette seeded with the `pane` tag — so what a test waits for is the palette.
// `nth` picks the tile.
async function openPaneMenu(nth = 0) {
  const buttons = await screen.findAllByRole("button", { name: "Pane menu" });
  fireEvent.click(buttons[nth]);
  await screen.findByRole("dialog", { name: "Command palette" });
}

// The commands the palette is currently offering, by visible title.
const menuTitles = () =>
  Array.from(document.querySelectorAll<HTMLElement>(".cmd-item-title")).map((e) => e.textContent);

// chooseMenu runs one entry by its visible title. Matched on the title element rather than
// the row's accessible name, which also carries the tmux bind rendered beside it.
async function chooseMenu(title: string) {
  const el = await screen.findByText(title, { selector: ".cmd-item-title" });
  fireEvent.click(el.closest("button")!);
}

const pickZones = () => Array.from(document.querySelectorAll<HTMLElement>(".pane-pick-zone"));
const tiles = () => Array.from(document.querySelectorAll<HTMLElement>(".stack"));

// --- the tab drag (a POINTER drag, on every device) --------------------------------
// Rearranging is no longer HTML5 drag-and-drop, so nothing here dispatches dragstart /
// dragover / drop: it presses, moves and releases, which is what both a mouse and a finger
// now do (src/dnd.ts). Three stubs are load-bearing, and each one hides a different green
// lie by its absence:
//   - EVERY TILE'S RECT, because jsdom lays nothing out. zoneAt() answers "stack" for a
//     zero rect, so an un-stubbed tile would report the centre for every aim and a test of
//     the right edge would pass while proving nothing about the edge.
//   - document.elementFromPoint, which jsdom does not implement AT ALL. Without it the hit
//     test finds no target, the release lands nowhere, and the layout is unchanged — which
//     is indistinguishable from the assertions two of these tests actually make ("nothing
//     moved"), so those two would certify a drag that never happened.
//   - pointerType, because it is what picks the lift rule: a mouse lifts as soon as it has
//     moved, a finger only after the dwell. An event carrying none is read as a finger, and
//     a mouse drag that forgot it would silently wait for a timer no test advances.
type Zone = "centre" | Side;

// Two tiles, side by side, 200x200 each: every point names exactly one of them, and each
// has all four of its own edges. A third tile would need a third rect; no test drags
// across more than two, so anything past the second reuses the second's.
const tileRectAt = (i: number): DOMRect =>
  ({
    left: i ? 200 : 0,
    right: i ? 400 : 200,
    top: 0,
    bottom: 200,
    width: 200,
    height: 200,
    x: i ? 200 : 0,
    y: 0,
    toJSON() {},
  }) as DOMRect;

// A point inside a tile for the zone it names — 3px in from an edge (well within the 0.3
// band), the exact centre for "centre". Derived from the rect rather than written down, so
// the aim and the geometry it is measured against cannot drift apart.
const zonePoint = (r: DOMRect, zone: Zone) => {
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  if (zone === "centre") return { clientX: cx, clientY: cy };
  if (zone === "left") return { clientX: r.left + 3, clientY: cy };
  if (zone === "right") return { clientX: r.right - 3, clientY: cy };
  if (zone === "top") return { clientX: cx, clientY: r.top + 3 };
  return { clientX: cx, clientY: r.bottom - 3 };
};

// Where a drag is pressed: tile 0's top-left corner, far enough from every zone point that
// a mouse press-and-move always clears the slop.
const PRESS_AT = { clientX: 1, clientY: 1 };

// tabDrag opens a gesture and hands back the steering. Tests that only care where the drag
// LANDED should use dropTabOn below; this one is for the ones that assert on the preview
// mid-flight, or that end the drag some way other than a release.
// `hold` is for the one test that needs the press WITHOUT the dwell behind it: a finger that
// starts moving before the countdown finishes is scrolling, and the press it made must never
// become a drag.
function tabDrag(from: HTMLElement, touch = false, hold = true) {
  const all = tiles();
  all.forEach((el, i) => (el.getBoundingClientRect = () => tileRectAt(Math.min(i, 1))));
  const prevHit = document.elementFromPoint;
  document.elementFromPoint = ((x: number, y: number) =>
    all.find((el) => {
      const r = el.getBoundingClientRect();
      return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
    }) ?? null) as typeof document.elementFromPoint;

  const kind = touch ? "touch" : "mouse";
  if (touch) vi.useFakeTimers();
  from.dispatchEvent(pointerEvent("pointerdown", PRESS_AT, 1, kind));
  // The finger's dwell. A mouse has none — it lifts on the first move past the slop — so
  // this line is the entire difference between the two gestures.
  if (touch && hold) vi.advanceTimersByTime(DRAG_LIFT_MS + 1);

  let last = PRESS_AT;
  const close = () => {
    // A finished drag swallows the click the browser synthesises after it, and un-swallows
    // it on the next task. Under fake timers that task has to be run by hand or the guard
    // outlives the whole gesture and eats the next real click a test makes.
    if (touch) {
      vi.advanceTimersByTime(1);
      vi.useRealTimers();
    }
    document.elementFromPoint = prevHit;
  };
  return {
    over(tile: number, zone: Zone) {
      last = zonePoint(tileRectAt(tile), zone);
      window.dispatchEvent(pointerEvent("pointermove", last, 1, kind));
    },
    // A move that does NOT clear the slop — a finger resting, a hand shaking. Used to show
    // the dwell is what arms the drag, not the press.
    nudge() {
      window.dispatchEvent(
        pointerEvent("pointermove", { clientX: PRESS_AT.clientX + 2, clientY: PRESS_AT.clientY }, 1, kind),
      );
    },
    // A move that clears it, before any dwell could have elapsed: on a finger this is the
    // page being scrolled.
    swipe() {
      window.dispatchEvent(
        pointerEvent(
          "pointermove",
          { clientX: PRESS_AT.clientX, clientY: PRESS_AT.clientY + DRAG_SLOP_PX + 20 },
          1,
          kind,
        ),
      );
    },
    drop() {
      window.dispatchEvent(pointerEvent("pointerup", last, 1, kind));
      close();
    },
    cancel() {
      window.dispatchEvent(pointerEvent("pointercancel", last, 1, kind));
      close();
    },
    escape() {
      fireEvent.keyDown(window, { key: "Escape" });
      window.dispatchEvent(pointerEvent("pointerup", last, 1, kind));
      close();
    },
  };
}

// The whole gesture in one call: press this tab, carry it to that zone of that tile, let go.
function dropTabOn(from: HTMLElement, tile: number, zone: Zone, touch = false) {
  const d = tabDrag(from, touch);
  d.over(tile, zone);
  d.drop();
}

// openAsTab clicks a file's name and answers the placement question with the CENTRE —
// "here, as a tab on this tile". Opening a file always asks now, whichever gesture asked
// it, so a test that only needs the editor on screen would otherwise carry three lines of
// wireframe about a question it is not interrogating. Space is the centre's key (see
// PickBackdrop), and the centre is exactly where a mouse open landed back when it did not
// ask — so every test using this reads as it did before, and the tests that are ABOUT the
// question drive the wireframe by hand.
async function openAsTab(name: string) {
  fireEvent.click(await screen.findByText(name));
  await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
  fireEvent.keyDown(window, { key: " " });
  await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeNull());
}
// Tab labels in document order — the readable identity of each pane, and the only way to
// say WHICH pane ended up where after a move.
const tabLabels = () =>
  Array.from(document.querySelectorAll<HTMLElement>(".tab-label")).map((e) => e.textContent?.trim());
// A terminal tab's NAME only — `.tab-label` also holds the activity badge, which comes and
// goes with the polled session list and would make an equality assertion flaky.
const tabNames = () =>
  Array.from(document.querySelectorAll<HTMLElement>(".tab-name")).map((e) => e.textContent?.trim());
// The tile a pick zone belongs to, by index among all tiles — the only way to say "the
// pane landed to the LEFT of the other one" rather than merely "a split exists".
const tileIndexOf = (el: HTMLElement) => tiles().indexOf(el.closest<HTMLElement>(".stack")!);

// tapPickZone presses one wireframe target. It names the TILE as well as the side, because
// with more than one candidate `pickZones()` holds five buttons per tile and a search by
// class alone silently takes whichever tile came first in the DOM. The existence check is
// not ceremony: fireEvent on undefined throws a stack trace that says nothing about which
// zone was missing.
function tapPickZone(zone: DropZone, tile = 0) {
  const target = pickZones().find((z) => z.classList.contains(`zone-${zone}`) && tileIndexOf(z) === tile);
  expect(target, `no ${zone} zone on tile ${tile}`).toBeTruthy();
  fireEvent.click(target!);
}

// connectPane drives the picker the way a user does: choose a running workload, press
// Connect. The mock BFF then creates a real session and keeps listing it as alive, which
// is what makes the pane ACTIVE — the state the close prompt exists for.
async function connectPane() {
  renderTerm();
  const picker = (await screen.findAllByRole("combobox"))[0];
  // The workload list arrives with its own fetch; setting a <select>'s value before its
  // options exist silently leaves it empty (and Connect disabled), so wait for the row.
  await screen.findByRole("option", { name: "shop-web" });
  fireEvent.change(picker, { target: { value: "shop-web" } });
  fireEvent.click(screen.getByRole("button", { name: "Connect" }));
  // Two things happen here and only the second one makes the pane active: the BFF
  // creates the session, and the pane then RECORDS its id. Waiting on the mock's list
  // alone returns between the two, with the pane still session-less — so wait for the
  // terminal itself, which the pane renders only once it holds a live session id.
  await waitFor(() => expect(liveTerminals()).toHaveLength(1));
  await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
}


// dividerDrag drives a real pointer drag on a split's handle. Two stubs are mandatory and
// both are load-bearing:
//   - the .split container's rect, because jsdom lays nothing out. Without it rect.width
//     is 0, SplitView's `if (!span) return` swallows every move, and a teardown test
//     passes green while asserting nothing at all.
//   - pointerId on every event, because jsdom has no PointerEvent and a MouseEvent's
//     pointerId is `undefined`. The handler filters with `ev.pointerId !== id`, and
//     `undefined !== undefined` is false — so an id-less event sails through and a
//     BROKEN filter looks exactly like a working one.
const SPLIT_RECT = { left: 0, top: 0, right: 400, bottom: 400, width: 400, height: 400, x: 0, y: 0, toJSON() {} } as DOMRect;

function pointerEvent(type: string, at: { clientX: number; clientY: number }, id = 1, pointerType = "mouse") {
  const ev = new MouseEvent(type, { ...at, bubbles: true, button: 0 });
  Object.defineProperty(ev, "pointerId", { value: id });
  Object.defineProperty(ev, "isPrimary", { value: true });
  // Read by the tab drag's lift rule (see tabDrag). The divider does not consult it, so its
  // own tests are unaffected by the default.
  Object.defineProperty(ev, "pointerType", { value: pointerType });
  return ev;
}

// growOf reads a split child's flex-grow — the ratio, as rendered.
const growOf = (i: number) =>
  Number(document.querySelectorAll<HTMLElement>(".split-child")[i].style.flexGrow);

function grabDivider() {
  const split = document.querySelector<HTMLElement>(".split")!;
  split.getBoundingClientRect = () => SPLIT_RECT;
  const divider = document.querySelector<HTMLElement>(".divider")!;
  // A deliberately FAT handle (40px): with a realistic 6px one the grab offset is at most
  // 3px, inside the tolerance any ratio comparison needs, so a dropped offset would still
  // look right. Its centre is 200 — the middle of the 400px container — because that is
  // where a 0.5 ratio actually puts it; a stub whose centre disagreed with the current
  // ratio would make "press without moving" appear to resize even with a correct offset.
  divider.getBoundingClientRect = () =>
    ({ left: 180, right: 220, top: 0, bottom: 400, width: 40, height: 400, x: 180, y: 0, toJSON() {} }) as DOMRect;
  return divider;
}

describe("Overview (project-oriented dashboard)", () => {
  it("groups workloads, mounts, and port-forwards under each project section", async () => {
    renderView(Overview);
    // Summary cards: server + project.
    expect(await screen.findByText("http://localhost:5000")).toBeInTheDocument();
    expect(await screen.findByText("connected")).toBeInTheDocument();
    // The loaded "shop" project renders its own section (anchored by slug), and the
    // project-less legacy-cron falls into the trailing "Other" section.
    // findAll: legacy-cron now names both a workload row and its tunnel row.
    expect(await screen.findAllByText("legacy-cron")).not.toHaveLength(0);
    expect(document.getElementById("project-shop")).toBeTruthy();
    expect(document.getElementById("project-other")).toBeTruthy();
    // Under shop: its workloads, its mounts, its forwards, and the depends_on
    // graph (two service_healthy edges). "shop-web" appears in all three of the
    // project's tables, so it is not a unique match.
    expect(await screen.findAllByText("shop-web")).not.toHaveLength(0);
    expect(await screen.findByText("/usr/share/nginx/html")).toBeInTheDocument();
    expect(await screen.findByText("127.0.0.1:8080 -> :80")).toBeInTheDocument();
    expect(await screen.findByText("https://shop-demo.ngrok.app")).toBeInTheDocument();
    expect(await screen.findAllByText("healthy")).toHaveLength(2);
  });

  // Conduit is a fact about the session, not about the last project on the page,
  // so it reads as one of the summary cards. Asserted by CONTAINMENT rather than
  // by "the text is somewhere on the page" — the old heading-at-the-foot markup
  // passed that, and so would a card sitting anywhere else.
  it("carries the conduit in the summary cards, beside Server and Workloads", async () => {
    renderView(Overview);
    const banner = await screen.findByText("SOCKS5 proxy at 127.0.0.1:1080");
    const cards = document.querySelector(".cards")!;
    const card = banner.closest(".card") as HTMLElement | null;
    expect(card).toBeTruthy();
    expect(cards.contains(card)).toBe(true);
    expect(within(card!).getByRole("heading", { name: "Conduit" })).toBeInTheDocument();
    // The neighbours it was asked to sit with, in the same grid.
    const headings = [...cards.querySelectorAll(":scope > .card > h3")].map((h) => h.textContent);
    expect(headings).toContain("Server");
    expect(headings).toContain("Workloads");
    expect(headings).toContain("Conduit");
    // And nothing left behind: no second copy under the project sections.
    expect(await screen.findAllByText("SOCKS5 proxy at 127.0.0.1:1080")).toHaveLength(1);
  });

  // A conduit-less session must still get the card, or the reader cannot tell
  // "no proxy" from "this dashboard doesn't report that".
  it("still shows the conduit card when nothing is announced", async () => {
    seedTunnelBanners([]);
    renderView(Overview);
    // findBy*, not getBy*: the heading is there from the first frame (the card is
    // unconditional), so only waiting on the ANSWER waits for the fetch.
    const answer = await screen.findByText("No proxy conduit.");
    const card = answer.closest(".card") as HTMLElement;
    expect(within(card).getByRole("heading", { name: "Conduit" })).toBeInTheDocument();
    expect(document.querySelector(".cards")!.contains(card)).toBe(true);
  });

  // The live shells belong with the counts: both answer "what is deployed, and
  // what am I inside of". Containment again — the sessions used to render under
  // their own <h2> at the foot, which any "the row is on the page" assertion
  // would have passed unchanged.
  it("keeps the live terminal sessions inside the Workloads card", async () => {
    seedTerminals([{ id: "t1", workload: "shop-db", cmd: ["/bin/sh"], state: "idle" }]);
    renderView(Overview);
    const row = await screen.findByText("/bin/sh");
    const card = row.closest(".card") as HTMLElement;
    expect(within(card).getByRole("heading", { name: "Workloads" })).toBeInTheDocument();
    // The card's own subject is still in it — the sessions joined the counts
    // rather than displacing them.
    expect(within(card).getByText(/total,.*running/)).toBeInTheDocument();
    expect(within(card).getByRole("heading", { name: "Terminal sessions" })).toBeInTheDocument();
    // A card is the narrowest container in the app, so the five nowrap columns
    // overflow it at every width: without the wrapper the overflow becomes the
    // page's, which is the bug `.table-scroll` exists to prevent.
    expect(row.closest("table")!.parentElement).toHaveClass("table-scroll");
  });

  // Title and Command answer different questions — what the session is running now
  // versus what it was launched with — and the table is the one place that shows
  // both, so an operator can see that the tab reading "vim" is a /bin/sh session.
  // Asserted as a ROW, because two columns that each hold the right text in the
  // wrong order would pass any per-cell lookup.
  it("lists a session's window title beside the command it was launched with", async () => {
    seedTerminals([
      { id: "t1", workload: "shop-db", cmd: ["/bin/sh"], title: "vim schema.sql", state: "idle" },
      { id: "t2", workload: "shop-web", cmd: ["/bin/ash"], state: "idle" },
    ]);
    renderView(Overview);
    const titled = (await screen.findByText("vim schema.sql")).closest("tr")!;
    expect(Array.from(titled.querySelectorAll("td")).map((c) => c.textContent)).toEqual([
      "shop-db",
      "—", // no agent: /bin/sh is a shell, not an agent
      "vim schema.sql",
      "/bin/sh",
      "idle",
    ]);

    // A session that set no title reports none, and the column says so rather than
    // repeating the command — which would make the two columns indistinguishable.
    const untitled = (await screen.findByText("/bin/ash")).closest("tr")!;
    expect(Array.from(untitled.querySelectorAll("td")).map((c) => c.textContent)).toEqual([
      "shop-web",
      "—",
      "—",
      "/bin/ash",
      "idle",
    ]);
  });

  // The empty case is the one a reader sees most, and it has to say so inside the
  // card rather than leaving the card looking truncated.
  it("says so in the card when no session is live", async () => {
    renderView(Overview);
    const none = await screen.findByText("No live terminal sessions.");
    expect(
      within(none.closest(".card") as HTMLElement).getByRole("heading", { name: "Workloads" }),
    ).toBeInTheDocument();
  });

  // Where the backend fact went, now that no workload row carries it: the Server
  // card, at the scope it is actually true at. Asserted through the <dt> it sits
  // under, not by "dockerhost appears in the card" — a stray string anywhere in
  // that card would pass the latter, including the one this pair of tests exists
  // to keep out of the tables.
  it("names the deploy backend in the Server card, with what it can do", async () => {
    renderView(Overview);
    const card = (await screen.findByText("dockerhost")).closest(".card") as HTMLElement;
    expect(within(card).getByRole("heading", { name: "Server" })).toBeInTheDocument();
    const labels = [...card.querySelectorAll("dt")];
    const backend = labels.find((d) => d.textContent === "Backend");
    expect(backend?.nextElementSibling?.textContent).toContain("dockerhost");
    // The capability, not just the name: this is what decides whether a compose
    // `depends_on: condition: service_healthy` can be satisfied at all.
    expect(within(card).getByText("health probes")).toHaveClass("ok");
    // And the detail that differs most between backends.
    expect(labels.map((d) => d.textContent)).toContain("Ingress");
    expect(within(card).getByText("emulated")).toBeInTheDocument();
    expect(within(card).getByText("shop.test")).toBeInTheDocument();
  });

  // "Not reported" is not "none". A server omits the field when it predates it or
  // could not construct the backend to ask — dropping the row would let a reader
  // conclude this server has no backend, the one thing that cannot be true.
  it("keeps the Backend row and says unknown when the server did not report it", async () => {
    seedServerInfo({ registry_host: "localhost:5000" });
    renderView(Overview);
    const card = (await screen.findByText("unknown")).closest(".card") as HTMLElement;
    expect(within(card).getByRole("heading", { name: "Server" })).toBeInTheDocument();
    const labels = [...card.querySelectorAll("dt")].map((d) => d.textContent);
    expect(labels).toContain("Backend");
    // Nothing invented from the other absence either: no ingress was advertised,
    // so no Ingress row claims one way or the other.
    expect(labels).not.toContain("Ingress");
  });

  // containerd and bare run no health probe, so this is a live server state and
  // the chip has to read as a limitation rather than as an unset flag.
  it("marks a backend that runs no health probes", async () => {
    seedServerInfo({ backend: { name: "containerd", reportsHealth: false } });
    renderView(Overview);
    const chip = await screen.findByText("no health probes");
    expect(chip).not.toHaveClass("ok");
    expect(
      within(chip.closest(".card") as HTMLElement).getByText("containerd"),
    ).toBeInTheDocument();
  });

  // The deploy backend is a property of the SERVER, not of a workload: a server
  // builds one backend for its whole lifetime and every backend stamps its own
  // name on every status it returns, so a per-row column repeated one identical
  // string down the table on every screen it ever rendered.
  //
  // Asserted as the table's FULL header list, not as "dockerhost is absent from
  // the page": the fixtures could stop saying "dockerhost" for reasons that have
  // nothing to do with this column, and that assertion would still pass. An
  // exact list also fails if the column is re-added under another name.
  it("gives a project's workload table no backend column", async () => {
    renderView(Overview);
    // shop-redis is the one workload with neither a mount nor an active tunnel,
    // so its name is a link in exactly one of the section's three tables.
    const link = await screen.findByRole("link", { name: "shop-redis" });
    const table = link.closest("table") as HTMLElement;
    expect([...table.querySelectorAll("thead th")].map((h) => h.textContent)).toEqual([
      "Service",
      "Name",
      "Image",
      "Status",
    ]);
  });

  // A project section is read service-first: the reader knows the name they wrote
  // in compose.yaml, and every other identifier in these tables (the
  // `<project>-<service>` resource, the forward's local address, the tunnel's
  // public URL) is something the tooling derived from it. Leading with Service in
  // every table also makes the four of them scannable as one column.
  //
  // Asserted over EVERY table in the section rather than table by table: a
  // per-table check passes unchanged when a fifth table is added below with its
  // own idea of a first column, which is exactly how the tunnel table came to be
  // the odd one out.
  it("leads every table in a project section with the service", async () => {
    renderView(Overview);
    // Wait for all three resources the section's tables are built from.
    await screen.findByRole("link", { name: "shop-redis" });
    await screen.findByText("https://shop-demo.ngrok.app");
    await screen.findByText("/srv/shop/html");

    const tables = [...document.querySelectorAll("#project-shop table.grid")];
    expect(tables.length).toBeGreaterThanOrEqual(4);
    for (const t of tables) {
      expect(t.querySelector("thead th")?.textContent).toBe("Service");
    }
    expectAlignedColumns(tables);
  });

  // The counterpart to the rule above, and the reason MountTable/ForwardsView take
  // a `scope`: under ONE workload's heading the caller has filtered every list to a
  // single deployment, which realizes a single compose service, so Service and
  // Workload are the same two strings on every row of every table. The header states
  // the pairing once; the tables carry only what differs per row.
  //
  // Checked as an absence over every header cell of every table, not by counting
  // columns: a count passes unchanged if Service is dropped and some other column
  // grows back in its place. The pairing is then asserted positively in the header,
  // so "the columns are gone" cannot be satisfied by a section that never rendered.
  it("repeats neither service nor workload in the tables under a workload heading", async () => {
    renderView(Overview);
    await screen.findAllByText("shop-web");
    fireEvent.click(await screen.findByRole("tab", { name: "By workload" }));

    const wl = document.getElementById("workload-shop-web")!;
    // All three tables settled: mounts, tunnels, forwards.
    await within(wl).findByText("/srv/shop/html");
    await within(wl).findByText("https://shop-demo.ngrok.app");
    await within(wl).findByText("127.0.0.1:8080 -> :80");

    const tables = [...wl.querySelectorAll("table.grid")];
    expect(tables.length).toBe(3);
    for (const t of tables) {
      const heads = [...t.querySelectorAll("thead th")].map((h) => h.textContent);
      expect(heads).not.toContain("Service");
      expect(heads).not.toContain("Workload");
      expect(heads.length).toBeGreaterThan(0);
    }
    expectAlignedColumns(tables);

    // Stated once, where it belongs: the section header pairs the deployment with
    // its service. Without this, deleting the whole section would pass the above.
    expect(within(wl).getByRole("heading", { level: 2 }).textContent).toBe("shop-web");
    expect([...wl.querySelectorAll(".row span.muted")].map((s) => s.textContent)).toContain(
      "web · shop",
    );
  });

  // The other half of the tunnel join: a deployment outside the loaded project
  // has no service, and the row says so rather than borrowing the resource name.
  it("shows a dash for a tunnel whose deployment has no compose service", async () => {
    renderView(Overview);
    const url = await screen.findByText("https://legacy.ngrok.app");
    const cells = [...url.closest("tr")!.querySelectorAll("td")];
    expect(cells[0].textContent).toBe("—");
    expect(cells[1].textContent).toBe("legacy-cron");
  });

  it("scopes a project's workloads to that project (legacy-cron is not under shop)", async () => {
    renderView(Overview);
    const shop =
      (await screen.findAllByText("legacy-cron")) && document.getElementById("project-shop")!;
    // legacy-cron has no project, so it must not appear inside the shop section.
    expect(shop.textContent).not.toContain("legacy-cron");
    expect(shop.textContent).toContain("shop-web");
  });

  // The rows are for reading. Controls in a row are the ones a reader hits by
  // accident — scrolling a too-wide table sideways on a phone, or aiming at the
  // link one row over — and Delete does not ask twice on a mis-hit it never
  // registered as a click. Asserted with the counterpart: dropping them from the
  // rows must not drop them from the page.
  it("puts no controls in the tables, and keeps them in the workload's own header", async () => {
    renderView(Overview);
    await screen.findAllByText("shop-web");
    await screen.findByText("https://shop-demo.ngrok.app");

    expect([...document.querySelectorAll("table.grid button")]).toEqual([]);
    // What replaces them: the row still reaches the workload, by its name.
    const row = document.querySelector('#project-shop table.grid a[href="/workloads/shop-web"]');
    expect(row).toBeTruthy();

    fireEvent.click(await screen.findByRole("tab", { name: "By workload" }));
    const wl = document.getElementById("workload-shop-web")!;
    for (const name of ["Stop", "Restart", "Delete"]) {
      expect(within(wl).getByRole("button", { name })).toBeInTheDocument();
    }
    // …in the section header, not back inside a table.
    expect([...wl.querySelectorAll("table.grid button")]).toEqual([]);
  });

  // A loaded project's heading used to carry "Apply (up -d)", which re-execed the
  // binary as `cornus compose up -d`. This UI accompanies the CLI rather than
  // replacing it: re-deploying belongs in the terminal the reader already has open.
  //
  // Asserted over every button in the section, not by querying for the old label —
  // a getByRole("button", {name: "Apply (up -d)"}) that throws proves only that ONE
  // spelling is gone, and would pass again the moment the button came back as
  // "Redeploy". The "loaded" badge is asserted alongside it as the counterpart: it
  // is rendered by the same <Show when={project.loaded}> block, so its presence
  // proves the section reached the branch the button used to live in, rather than
  // the whole heading having failed to render.
  it("puts no apply control on a loaded project's heading", async () => {
    renderView(Overview);
    await screen.findAllByText("shop-web");

    const shop = document.getElementById("project-shop")!;
    expect(within(shop).getByText("loaded")).toBeInTheDocument();
    for (const b of shop.querySelectorAll("button")) {
      expect(b.textContent).not.toMatch(/apply|up -d|deploy/i);
    }
  });

  it("switches to a workload-oriented grouping via the toggle", async () => {
    renderView(Overview);
    // Wait for project-mode content backed by all three resources to settle.
    await screen.findAllByText("legacy-cron");
    await screen.findByText("https://shop-demo.ngrok.app");
    expect(document.getElementById("project-shop")).toBeTruthy();

    fireEvent.click(await screen.findByRole("tab", { name: "By workload" }));

    // Every workload now has its own section; the project sections are gone.
    expect(document.getElementById("workload-shop-web")).toBeTruthy();
    expect(document.getElementById("workload-legacy-cron")).toBeTruthy();
    expect(document.getElementById("project-shop")).toBeNull();
    // shop-web's section carries its own mount and public tunnel.
    const wl = document.getElementById("workload-shop-web")!;
    expect(wl.textContent).toContain("/usr/share/nginx/html");
    expect(wl.textContent).toContain("https://shop-demo.ngrok.app");
  });

  // Under the mobile breakpoint a page table is wider than the viewport, and
  // without a scroller of its own the reader drags the WHOLE page sideways to
  // reach the last column. The two halves of that fix are asserted separately
  // because either one alone silently does nothing: the markup here, the rule
  // that makes the wrapper scroll in the stylesheet test below.
  it("puts every table on the page in its own scroller, in both groupings", async () => {
    renderView(Overview);
    await screen.findAllByText("legacy-cron");
    await screen.findByText("https://shop-demo.ngrok.app");

    const unwrapped = () =>
      [...document.querySelectorAll("table.grid")].filter(
        (t) => !t.parentElement?.classList.contains("table-scroll"),
      );
    // Project mode: workloads, mounts, tunnels, forwards, terminal sessions.
    expect(document.querySelectorAll("table.grid").length).toBeGreaterThan(1);
    expect(unwrapped()).toEqual([]);

    fireEvent.click(await screen.findByRole("tab", { name: "By workload" }));
    await waitFor(() => expect(document.getElementById("workload-shop-web")).toBeTruthy());
    expect(unwrapped()).toEqual([]);
  });
});

describe("Stylesheet", () => {
  // The other half of the mobile-table fix: wrappers in the markup do nothing
  // until this rule makes them scroll, and it must live INSIDE the breakpoint —
  // at desktop widths the tables fit, and a scroller there is a second scrollbar
  // on a table that does not need one.
  it("scrolls .table-scroll horizontally under the mobile breakpoint", () => {
    const mobile = block(cssSource, "@media (max-width: 720px)");
    expect(mobile).toMatch(/\.table-scroll\s*\{[^}]*overflow-x:\s*auto/);
    // Nowhere else in the sheet does the BARE .table-scroll selector get a rule
    // (`.card .table-scroll` is a separate, deliberately unscoped one — below).
    const all = cssSource.match(/^\s*\.table-scroll\s*\{/gm) ?? [];
    const inside = mobile.match(/^\s*\.table-scroll\s*\{/gm) ?? [];
    expect(all).toHaveLength(inside.length);
  });

  // A terminal tab's activity badge disappeared exactly when it mattered: with a long
  // enough label, "working" fitted inside `.tab`'s `max-width: 16rem` and the wider
  // "needs you" did not, so the indicator read as blank at the moment a session started
  // waiting on a human. `.tab-label` is a flex item, so `overflow: hidden` gave it an
  // automatic minimum size of 0 and it clipped whatever came after the name.
  //
  // THIS TEST CANNOT SEE THAT. jsdom does no layout and the CSSOM is empty for styles.css,
  // so all it can do is pin the rules the fix consists of — and the honest half is the
  // NEGATIVE one: the truncation must no longer be on `.tab-label`, because that is the
  // box whose clipping ate the badge. A test asserting only that the flex row exists would
  // pass with the old clipping still in place beside it.
  it("lets a tab's name give way, never its activity badge", () => {
    const label = block(cssSource, ".tab-label");
    expect(label).toMatch(/display:\s*flex/);
    expect(label).toMatch(/min-width:\s*0/);
    // The clipping moved to the name, and is not left behind on the label.
    expect(label).not.toMatch(/text-overflow/);
    const name = block(cssSource, ".tab-name");
    expect(name).toMatch(/text-overflow:\s*ellipsis/);
    expect(name).toMatch(/min-width:\s*0/);
    // And the badge is excused from shrinking at all.
    expect(block(cssSource, ".tab .badge")).toMatch(/flex:\s*0 0 auto/);
  });

  // The chooser lists panes with the same ctx.tabTitle, so the same badge markup appears
  // in both places and must be the same SIZE in both. Pinned as one rule naming both
  // selectors rather than as two rules that agree: two rules agreeing today is exactly the
  // state that drifts the next time either is touched, and the drift is invisible until
  // someone opens the chooser on a session that happens to be badged.
  it("sizes the chooser's activity badge from the tab's own rule", () => {
    const rule = block(cssSource, ".tab .badge,\n.pane-chooser-label .badge");
    expect(rule).toMatch(/padding:\s*0 var\(--space-2\)/);
    expect(rule).toMatch(/line-height:\s*var\(--leading-tight\)/);
    expect(rule).toMatch(/flex:\s*0 0 auto/);
    // And the chooser row has the tab's structure, so its badge is not clipped either.
    const label = block(cssSource, ".pane-chooser-label {");
    expect(label).toMatch(/display:\s*flex/);
    expect(label).not.toMatch(/text-overflow/);
  });

  // The glint, and the reason it is built the way it is. `background-image` has animation
  // type DISCRETE — no browser interpolates two `linear-gradient()` values — so keyframing
  // the gradient does not sweep, it flips. The first version did that, and since both of
  // its frames put the band outside the box (which was what made the loop seamless), the
  // band never appeared at all. It shipped, and the test passed, because the test asserted
  // the off-screen ends as a FEATURE.
  //
  // So the assertions that matter are about what makes the motion real: the travelling
  // value is a REGISTERED custom property (an unregistered one is a token and animates
  // discretely, failing identically), and the keyframes move that property rather than the
  // image. The gradient itself is static, which is the point.
  it("sweeps the needs-you badge by animating a registered stop position", () => {
    const prop = block(cssSource, "@property --attn-stop");
    expect(prop).toMatch(/syntax:\s*"<percentage>"/);
    expect(prop).toMatch(/inherits:\s*false/);
    expect(prop).toMatch(/initial-value:/);

    const rule = block(cssSource, ".badge.attention {");
    expect(rule).toMatch(/animation:\s*badge-attention\s+[\d.]+s\s+linear\s+infinite\s*;/);
    // The gradient is declared ONCE, on the rule, and every stop is placed off the one
    // travelling value — that is what makes them move together as a band.
    const grad = /background-image:\s*linear-gradient\(([\s\S]*?)\n\s*\);/.exec(rule)![1];
    expect([...grad.matchAll(/var\(--attn-stop\)/g)]).toHaveLength(3);
    expect(grad).toContain("var(--warn-strong)");

    // The keyframes move the PROPERTY, not the image, and the band crosses the box.
    const frames = block(cssSource, "@keyframes badge-attention");
    expect(frames).not.toContain("background-image");
    const stops = [...frames.matchAll(/--attn-stop:\s*(-?[\d.]+)%/g)].map((m) => Number(m[1]));
    expect(stops).toHaveLength(2);
    expect(Math.min(...stops)).toBeLessThan(0); // starts off the left edge…
    expect(Math.max(...stops)).toBeGreaterThan(100); // …and ends off the right

    // Both themes define the band's colour, or dark mode sweeps with nothing in it.
    expect(block(cssSource, ":root")).toMatch(/--warn-strong:/);
    expect(block(cssSource, "@media (prefers-color-scheme: dark)")).toMatch(/--warn-strong:/);
    // Reduced motion drops the movement and KEEPS the emphasis — pinning the resting fill
    // would demote this to an ordinary warn badge for exactly the people who opted out.
    const reduced = block(
      cssSource.slice(cssSource.indexOf(".badge.attention {")),
      "@media (prefers-reduced-motion: reduce)",
    );
    expect(reduced).toMatch(/animation:\s*none/);
    expect(reduced).toMatch(/background:\s*var\(--warn-strong\)/);
    expect(reduced).toContain(".badge.attention");
  });

  // The general form of the bug above, which is worth a guard of its own: a keyframe that
  // names a property CSS animates discretely is not an animation, it is a flip — and it
  // looks entirely plausible in the source. The list is short and each entry is here
  // because it reads as animatable and is not.
  it("animates no property that CSS can only flip", () => {
    const discrete = ["background-image", "display", "content", "font-family"];
    const offenders: string[] = [];
    for (const m of cssSource.matchAll(/@keyframes\s+([\w-]+)/g)) {
      const body = block(cssSource, `@keyframes ${m[1]}`);
      for (const prop of discrete) {
        if (new RegExp(`(^|[;{\\s])${prop}\\s*:`).test(body)) offenders.push(`${m[1]} -> ${prop}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  // The number says the same thing in its own vocabulary, and in its own SHAPE. Two claims
  // here are the ones worth breaking: that the rule does not exclude `.current` (the
  // obvious way to write it wrongly is to leave the focused pane alone, and "this pane
  // needs you" has to beat "this is the pane you are on"), and that the disc breathes
  // rather than sweeps (a travelling band across 1.6em is a smear, not a movement).
  it("inverts a blocked pane's number and breathes it, active or not", () => {
    // Named in the chooser too, where a list of panes is precisely what a number is for.
    // Asserted FIRST: dropping it also changes the rule's header, and the block lookup
    // below would then fail with "no such block" — true, but it would report a missing
    // selector list rather than the missing half of the behaviour.
    expect(cssSource).toContain(".pane-chooser-item:has(.badge.attention) .pane-number");
    // Unqualified, and asserted before the block lookup for the same reason: narrowing this
    // to `:not(.current)` also changes the header, and the lookup would then report a
    // missing block rather than the exemption that is the actual mistake.
    expect(cssSource).toMatch(
      /\.tab:has\(\.badge\.attention\) \.pane-number,\s*\n\.pane-chooser-item:has\(\.badge\.attention\) \.pane-number \{[^}]*animation:\s*pane-number-attention/,
    );
    const inTab = block(cssSource, "\n.tab:has(.badge.attention) .pane-number,");
    // Inverted: a solid warn disc with contrasting text, the warn twin of .pane-number
    // .current's accent fill — NOT the badge's wash.
    expect(inTab).toMatch(/background:\s*var\(--warn\)/);
    expect(inTab).toMatch(/color:\s*var\(--warn-fg\)/);
    expect(inTab).not.toMatch(/var\(--warn-subtle\)/);
    // Not scoped away from the focused pane — the whole point of "active or not".
    expect(inTab).not.toContain(":not(.current)");

    // Its own animation, and a different one: value, not position.
    expect(inTab).toMatch(/animation:\s*pane-number-attention\s+[\d.]+s\s+[\w-]+\s+infinite\s*;/);
    const frames = block(cssSource, "@keyframes pane-number-attention");
    // Out and back WITHIN one period, rather than `alternate` at half the duration. Both
    // are a 2.4s round trip, but only this one can be compared with the badge's number
    // without doubling it in your head first — see the period assertion below.
    expect(frames).toMatch(/0%,\s*\n?\s*100%\s*\{\s*background-color:\s*var\(--warn\);\s*\}/);
    expect(frames).toMatch(/50%\s*\{\s*background-color:\s*var\(--warn-deep\);\s*\}/);
    expect(frames).not.toContain("linear-gradient");
    // Only the fill's value moves: no geometry, no opacity, nothing that could reflow a bar
    // whose height the tile's body offset depends on.
    expect(frames).not.toMatch(/padding|transform|opacity|font-size|width|height|border/);

    // The SAME period as the badge, and the same number in the source. Asserted as equality
    // rather than as a literal, so retiming one and not the other is what fails.
    const secondsOf = (rule: string) => Number(/animation:[^;]*?([\d.]+)s/.exec(rule)![1]);
    expect(secondsOf(inTab)).toBe(secondsOf(block(cssSource, ".badge.attention {")));

    // Both themes define what the inverted form needs, or one of them inverts to nothing.
    for (const scope of [":root", "@media (prefers-color-scheme: dark)"]) {
      expect(block(cssSource, scope)).toMatch(/--warn-fg:/);
      expect(block(cssSource, scope)).toMatch(/--warn-deep:/);
    }
    // Reduced motion stops the number with the badge. It needs no fill pinned: unlike the
    // badge, its emphasis was never the animation.
    const reduced = block(
      cssSource.slice(cssSource.indexOf(".badge.attention {")),
      "@media (prefers-reduced-motion: reduce)",
    );
    expect(reduced).toContain(".tab:has(.badge.attention) .pane-number");
  });

  // The exception to that scoping, and the reason it is not a contradiction: a
  // card is the narrowest container in the app — a cell of a minmax(260px, 1fr)
  // grid is narrower at 1200px than the whole page is on a phone — so a table in
  // one overflows at EVERY width. Measured without this rule: 345px of table in a
  // 237px card, spilling past the card edge with scrollLeft pinned at 0, i.e. the
  // State column unreachable on desktop.
  it("scrolls a card's table at every width, not only under the breakpoint", () => {
    const rule = /\.card\s+\.table-scroll\s*\{[^}]*overflow-x:\s*auto/;
    expect(cssSource).toMatch(rule);
    // The point of the rule is where it is NOT: inside the breakpoint it would
    // leave the desktop card exactly as broken as before.
    expect(block(cssSource, "@media (max-width: 720px)")).not.toMatch(rule);
  });

  // The focus ring is the only thing telling a keyboard user where they are, and it used
  // to be a 3px --accent-subtle halo — a 10% wash measuring ~1.2:1 against the page, well
  // under WCAG 2.2's 3:1 for a focus indicator. Worse, --accent-subtle is also the FILL of
  // the active nav pill and of a highlighted palette row, so on the two controls a keyboard
  // user lands on most the ring was the same colour as the thing it was drawn on.
  it("draws the focus ring in a colour that is not the fill it lands on", () => {
    expect(block(cssSource, ":root {")).toMatch(/--focus-ring:\s*0 0 0 \d+px var\(--accent\);/);
    // Every ring in the sheet comes from the token, so there is one place to change and no
    // rule can quietly keep the old wash. (input:focus is deliberately not in this list —
    // a field's indicator is its --accent BORDER, and a solid ring outside that is a
    // third edge on a control that already reads as active.)
    const rings = [":focus-visible {", "button:focus-visible {", ".chart-plot:focus-visible {"];
    for (const sel of rings)
      expect(block(cssSource, sel), sel).toMatch(/box-shadow:\s*var\(--focus-ring\)/);
    // The premise that makes the colour a requirement rather than a preference: these are
    // the surfaces the ring has to survive, and they are painted in what it used to be.
    const wash = /background:\s*var\(--accent-subtle\)/;
    expect(block(cssSource, ".appbar-nav a.active {")).toMatch(wash);
    expect(block(cssSource, ".cmd-item.selected,")).toMatch(wash);
  });

  // The ring is drawn OUTSIDE the border box, so a scroll container clips it to its padding
  // box — and a control that fills its scrollport loses the whole indicator, not a corner of
  // it. That is what made the appbar's nav links and a pane's crumbs the two worst cases in
  // the app: barely-visible colour AND nothing left to see. Each lends the ring room with
  // padding and hands the room straight back with a negative margin, so nothing moves.
  it("leaves the ring somewhere to land in the containers that clip it", () => {
    const ring = Number(/--focus-ring:\s*0 0 0 (\d+)px/.exec(cssSource)![1]);
    // Horizontal-only overflow still clips vertically: overflow-y computes from visible to
    // auto the moment its partner is not visible. The nav is exactly as tall as its pills.
    const nav = block(cssSource, ".appbar-nav {");
    expect(nav).toMatch(/overflow-x:\s*auto/);
    const navPad = /padding:\s*([^;]+);/.exec(nav)![1];
    expect(px(cssSource, navPad)).toBeGreaterThanOrEqual(ring);
    const navMargin = /margin:\s*0\s+calc\(-1 \* (var\([^)]+\))\)/.exec(nav)![1];
    expect(px(cssSource, navMargin)).toBe(px(cssSource, navPad));

    // The crumbs' overflow:hidden is load-bearing — it is what the left fade and the
    // scrollLeft anchoring are built on — so the clip cannot simply be dropped.
    const crumbs = block(cssSource, ".stack-subheader .crumbs {");
    expect(crumbs).toMatch(/overflow:\s*hidden/);
    const [, padY, padX] = /padding:\s*(\S+)\s+([^;]+);/.exec(crumbs)!;
    const [, marY, marX] = /margin:\s*-(\S+)\s+calc\(-1 \* (var\([^)]+\))\)/.exec(crumbs)!;
    for (const [pad, mar] of [
      [padY, marY],
      [padX, marX],
    ]) {
      expect(px(cssSource, pad)).toBeGreaterThanOrEqual(ring);
      expect(px(cssSource, mar)).toBe(px(cssSource, pad));
    }
  });

  // The converse case. A file listing suppresses the app ring: focus there sits on the
  // name LINK, so the ring boxes a word inside a row instead of marking the row, on top of
  // two cues already saying the same thing. Comments can argue that; what a test has to
  // hold is that dropping an indicator left the replacements standing — a suppression whose
  // justification quietly disappears is just an invisible focus.
  it("suppresses the ring in a file listing, but not the cues standing in for it", () => {
    expect(block(cssSource, ".fs-list:focus-visible,")).toMatch(/box-shadow:\s*none/);
    expect(block(cssSource, ".fs-list:focus-within {")).toMatch(/border-color:\s*var\(--accent\)/);
    expect(block(cssSource, ".fs-list table.grid tbody tr.fs-selected {")).toMatch(
      /background:\s*var\(--accent-subtle\)/,
    );
    // The third cue, and the only one that survives a multi-row selection: shift+Arrow
    // fills every row in the range identically, so the cursor's own row needs a mark of
    // its own or the next Arrow's origin is unreadable. Inset, so the list's overflow
    // cannot clip it, and at the ring's weight so the two read as one idea in two shapes.
    const ring = Number(/--focus-ring:\s*0 0 0 (\d+)px/.exec(cssSource)![1]);
    const bar = block(cssSource, ".fs-list table.grid tbody tr:focus-within > td:first-child {");
    expect(bar).toMatch(new RegExp(`box-shadow:\\s*inset ${ring}px 0 0 var\\(--accent\\)`));
  });

  // A kv value that wraps (the Server card's backend name plus its capability
  // chip, in a ~150px value column) has to keep the wrapped line closer to its
  // own label than the next entry is. That is a RELATION between two gaps, not a
  // constant, so the test resolves both — asserting `row-gap: 2px` would still
  // pass if `.kv`'s own gap were later tightened past it, which is the exact
  // regression that makes the chip read as the next label's value.
  it("wraps a kv value tighter than the gap between kv entries", () => {
    // `.kv`'s shorthand is `gap: <row> <column>`; the row gap is the first.
    const kvGap = /gap:\s*(\S+)\s+\S+;/.exec(block(cssSource, ".kv {"))![1];
    const ddGap = /row-gap:\s*(\S+);/.exec(block(cssSource, ".kv dd .row"))![1];
    expect(px(cssSource, ddGap)).toBeLessThan(px(cssSource, kvGap));
  });

  // The Settings screen drops the `.section` band's 2px rule, so spacing is the
  // ONLY thing left binding a heading to its rows. That is a RELATION, not a
  // constant: the gap ABOVE a group's heading has to beat the gap below it, or the
  // last row of one group reads as the first row of the next. Asserting
  // `margin-top: 32px` would still pass after someone raised the heading's own
  // bottom margin past it — which is exactly the regression that loses the grouping.
  it("groups settings by spacing alone, the gap between beating the gap within", () => {
    const between = /margin-top:\s*(\S+);/.exec(
      block(cssSource, ".setting-group + .setting-group"),
    )![1];
    const within = /margin:\s*0\s+0\s+(\S+);/.exec(block(cssSource, ".setting-group > h2"))![1];
    expect(px(cssSource, between)).toBeGreaterThan(px(cssSource, within));
    // And nothing draws the rule back: no border anywhere on a settings group.
    expect(cssSource).not.toMatch(/\.setting-group[^{]*\{[^}]*border/);
  });

  // Every setting's title starts on the same line whether or not its row leads
  // with a checkbox. The grid alone does not buy that — what does is `.setting-text`
  // being PINNED to column 2: left to auto-placement, a checkbox-less row's text
  // falls into column 1 and hangs left of its neighbours, which is the raggedness
  // the grid was introduced to remove. So assert the pin, not just the tracks.
  it("reserves the checkbox column so every setting title lines up", () => {
    expect(block(cssSource, ".setting-row {")).toMatch(
      /grid-template-columns:\s*var\(--space-4\)\s+1fr/,
    );
    expect(block(cssSource, ".setting-text {")).toMatch(/grid-column:\s*2/);
  });
});

describe("Workload detail", () => {
  // The Terminal workspace persists its layout, and the Exec CTA lands there — a
  // pane left over from another test would answer for the one the CTA makes.
  beforeEach(() => globalThis.localStorage?.clear());

  // Both routes, because the CTA's whole claim is about where it arrives.
  function renderDetail(name: string) {
    history.replaceState(null, "", `/workloads/${encodeURIComponent(name)}`);
    return render(() => (
      <Router>
        <Route path="/workloads/:name" component={WorkloadDetail} />
        <Route path="/workspace" component={Workspace} />
      </Router>
    ));
  }

  it("shows instances, spec, metrics, and logs at once, with nothing to click first", async () => {
    renderDetail("shop-web");
    // Each section is present WITH its content — a heading alone would pass against
    // four empty boxes.
    const instances = (await screen.findByRole("heading", { name: "Instances" })).closest(
      "section",
    )!;
    await waitFor(() => expect(instances.querySelector("table.grid tbody tr")).toBeTruthy());
    const spec = screen.getByRole("heading", { name: "Spec" }).closest("section")!;
    expect(spec.querySelector("pre.log")?.textContent).toContain("image");
    const logs = screen.getByRole("heading", { name: "Logs" }).closest("section")!;
    expect(logs.querySelector("pre.log")).toBeTruthy();
    const metrics = screen.getByRole("heading", { name: "Metrics" }).closest("section")!;
    await waitFor(() => expect(metrics.querySelector("svg.chart-plot")).toBeTruthy());

    // No tab bar left to reveal any of it.
    for (const gone of ["instances", "spec", "metrics", "logs", "exec"]) {
      expect(screen.queryByRole("button", { name: gone })).toBeNull();
    }
    // …and opening the page did not open a shell in the container.
    expect(document.querySelector(".term-wrap")).toBeNull();
  });

  // The CTA's whole claim is about where it arrives, and the merge moved both halves of
  // that: the route is the Workspace's, and the pane it lands in is a NEW TAB rather than
  // the focused pane retargeted. The old screen opened as an empty terminal, so the target
  // had somewhere to go; this one opens as a file browser, which is not a slot to reuse.
  it("takes Exec to the Workspace, as a terminal tab beside the file browser", async () => {
    renderDetail("shop-web");
    fireEvent.click(await screen.findByRole("button", { name: "Exec" }));

    await waitFor(() => expect(document.querySelector(".workspace")).toBeTruthy());
    await waitFor(() => expect(location.pathname).toBe("/workspace"));
    // Two tabs — the file browser it opened as, and the terminal the CTA asked for — and
    // the terminal is the one raised. Asserting the ACTIVE tab's label rather than merely
    // finding the workload's name somewhere matters here: the file browser's own root
    // listing has a `shop-web` row in it, so a plain text search would pass with no
    // terminal on screen at all.
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(document.querySelector(".tab.active")?.textContent).toContain("shop-web");
    // The pane took the target: it connects rather than showing the empty pane's picker.
    expect(screen.queryAllByRole("combobox")).toHaveLength(0);
    // The param is consumed on arrival — reloading this URL must not open a second
    // session on top of the one it just made.
    await waitFor(() => expect(location.search).toBe(""));
  });
});

describe("Workspace (file browser)", () => {
  // The tiled layout (splits + each pane's path) persists to localStorage; clear it
  // so each test starts from the default single pane at the virtual root. The toast queue
  // is module state that outlives a render, so it is cleared too — a message raised by
  // one test would otherwise still be on screen during the next.
  beforeEach(() => {
    globalThis.localStorage?.clear();
    clearToasts();
  });

  it("opens at a virtual root listing local roots and workloads as mounts", async () => {
    renderView(Workspace);
    // Local roots (by id) and workloads are the top-level mounts; a stopped
    // workload is flagged and not enterable.
    expect(await screen.findByText("project")).toBeInTheDocument();
    expect(await screen.findByText("assets")).toBeInTheDocument();
    expect(await screen.findByText("shop-web")).toBeInTheDocument();
    // Stopped workloads (shop-worker, legacy-cron) are flagged and not enterable.
    expect((await screen.findAllByText("(stopped)")).length).toBeGreaterThan(0);
  });

  it("browses a local mount and navigates into folders", async () => {
    renderView(Workspace);
    // Enter the project mount, then a folder inside it.
    fireEvent.click(await screen.findByText("project"));
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();
    expect(await screen.findByText("README.md")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("web"));
    expect(await screen.findByText("nginx.conf")).toBeInTheDocument();
  });

  it("descends into a running workload's container filesystem", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("shop-web"));
    expect(await screen.findByText("hello.txt")).toBeInTheDocument();
  });

  it("shows the mount (workload/root) name in the tab label, like the terminal", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("shop-web")); // enter the workload mount
    await screen.findByText("hello.txt");
    expect(document.querySelector(".tab-label")?.textContent).toContain("shop-web");
    // Descend a folder → the tab keeps the mount alongside the current folder.
    fireEvent.click(await screen.findByText("etc"));
    await screen.findByText("nginx");
    expect(document.querySelector(".tab-label")?.textContent).toContain("shop-web");
  });

  it("opens an image file into a tiny image viewer pane", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("assets")); // the assets mount holds the logos
    expect(await screen.findByText("cornus-logo.svg")).toBeInTheDocument();
    await openAsTab("cornus-logo.png"); // open → place it here → image viewer tab
    const img = await screen.findByRole("img");
    expect(img.getAttribute("src")).toContain("cornus-logo.png");
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
    // It is a viewer, not the editor — no Save control.
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  // A MOUSE open asks where the pane goes, exactly as Enter on a row does. It used to stack
  // a tab on the spot, and the two gestures disagreeing was the defect: the click named a
  // FILE, and the tile it happened to land on is the one already spending its width on the
  // listing. What this pins is the ASKING — that the click opens nothing on its own — and
  // then that the centre still reaches the old outcome in one keystroke.
  //
  // A false pass would be a click that opened the editor while some unrelated overlay
  // happened to be up, so the tab count is asserted BEFORE the answer as well as after: one
  // tab with the wireframe showing is the state that says the question was really asked.
  it("asks where a mouse-opened file should land, the same as a keyboard one", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("README.md"));

    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    expect(modalRequest()).toBeNull(); // the tile is the question; there is no dialog
    expect(document.querySelectorAll(".tab")).toHaveLength(1); // and nothing opened yet
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();

    fireEvent.keyDown(window, { key: " " }); // the centre: as a tab, here
    expect(await screen.findByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  // The other half of the same contract: a DOUBLE-CLICK on the row is the third spelling of
  // open (the name link and Enter are the other two), and it goes through the identical
  // path. Worth its own test because it is a different handler — `onDblClick` on the <tr>,
  // not `onClick` on the <a> — and the two were free to drift while the flag existed.
  it("asks on a double-clicked row too, and places the editor where the arrow points", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.dblClick((await screen.findByText("README.md")).closest("tr")!);

    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowRight" }); // …and the answer can be a SPLIT
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(within(tiles()[1]).getByRole("button", { name: "Save" })).toBeTruthy();
  });

  // Unsaved work has two on-screen statements, and they cover different cases: the
  // "unsaved" badge on the editor bar, which is only visible while that pane is the
  // ACTIVE tab, and the "*" on the tab label, which is visible from any tab. The edit is
  // made through CodeMirror's own dispatch (the view owns the document; there is no
  // textarea to type into), so the change travels the real updateListener → onChange path
  // that sets the dirty flag.
  it("marks an editor's tab label with * while it holds unsaved changes", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await openAsTab("compose.yaml");
    await screen.findByRole("button", { name: "Save" });

    const labels = () => Array.from(document.querySelectorAll(".tab-label")).map((n) => n.textContent);
    await waitFor(() => expect(labels()).toContain("project  compose.yaml"));
    expect(document.querySelector(".file-pane-editor-bar .badge")).toBeNull();

    const view = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    // Wait for the read to SEED the editor before typing into it. An edit dispatched first
    // is overwritten when the text lands, and the pane goes clean again — the * appears and
    // then vanishes, so the waitFor below passes on a state that is gone by the next line.
    // Same wait openEditor makes further down, and for the same reason.
    await waitFor(() => expect(view.state.doc.length).toBeGreaterThan(0));
    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    await waitFor(() => expect(labels()).toContain("project  compose.yaml *"));
    expect(document.querySelector(".file-pane-editor-bar .badge")?.textContent).toBe("unsaved");

    // Saving clears both marks (the write lands in the mock fs, so this is the real path).
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(labels()).toContain("project  compose.yaml"));
    expect(document.querySelector(".file-pane-editor-bar .badge")).toBeNull();
  });

  // theEditor / openEditor. The wait is not ceremony: a pane renders its Save button (and
  // an empty document) before the read lands, and the arriving text SEEDS the editor — so
  // an edit dispatched too early is overwritten a tick later and the pane goes clean
  // again, which is precisely the state these tests interrogate. Waiting for the file's
  // own text to appear is what makes the edit that follows a real unsaved change.
  const theEditor = () =>
    EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
  async function openEditor(name: string) {
    await openAsTab(name);
    await screen.findByRole("button", { name: "Save" });
    await waitFor(() => expect(theEditor().state.doc.length).toBeGreaterThan(0));
    return theEditor();
  }
  const tabLabels = () => Array.from(document.querySelectorAll(".tab-label")).map((n) => n.textContent);

  // Closing an editor pane is the one close that DESTROYS work: the draft is dropped with
  // the pane (files/drafts.ts forgets it) and the file on disk still holds the old text,
  // so there is nothing to reopen. The ✕ therefore asks first — and the pane must survive
  // the question, both while it is on screen and after a Cancel.
  it("asks before closing an editor tab that holds unsaved changes", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    const view = await openEditor("compose.yaml"); // mouse open → editor tab
    const labels = tabLabels;

    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    await waitFor(() => expect(labels()).toContain("project  compose.yaml *"));

    // The ✕ opens the question and closes nothing yet: both tabs are still there.
    closeTab("compose.yaml");
    expect(await dialog()).toHaveTextContent("Discard unsaved changes to compose.yaml?");
    expect(document.querySelectorAll(".tab")).toHaveLength(2);

    // Cancel keeps the pane AND its edit — a gate that answered by discarding anyway, or
    // by reverting the buffer, would pass a test that only counted tabs.
    await answer("Cancel");
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
    expect(labels()).toContain("project  compose.yaml *");
    expect(theEditor().state.doc.toString()).toContain("# edited");

    // Only confirming closes it, and then the tab is gone for good.
    closeTab("compose.yaml");
    await answer("Discard & close");
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(1));
    expect(labels()).not.toContain("project  compose.yaml *");
  });

  // The counterpart: the prompt is keyed on UNSAVED WORK, not on "this is an editor". A
  // pane with nothing to lose must close on the click that asked for it — otherwise the
  // gate is just a dialog in front of every ✕.
  it("closes a saved editor tab, and a browse tab, without asking", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await openEditor("README.md");
    expect(document.querySelectorAll(".tab")).toHaveLength(2);

    // Never edited: closes on the click that asked for it, with no question in between.
    closeTab("README.md");
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    noDialog();

    // An editor whose edit has been SAVED is clean again, so it too closes silently —
    // the gate keys on unsaved work, not on the pane being an editor.
    const view = await openEditor("README.md");
    view.dispatch({ changes: { from: 0, insert: "# saved edit\n" } });
    await waitFor(() => expect(tabLabels()).toContain("project  README.md *"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(tabLabels()).toContain("project  README.md"));

    closeTab("README.md");
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    noDialog();

    // And the browse pane the workspace started with — nothing to lose, nothing to ask.
    // (It is the last tab: closePane replaces it with a fresh empty pane rather than
    // leaving no tile, so the count stays 1 and the path goes back to the root.)
    closeTab("project");
    noDialog();
    expect(await screen.findByText("shop-web")).toBeInTheDocument();
  });

  // Re-tiling a pane rebuilds it at its new place in the tree, so any state the pane
  // component held privately is destroyed by the move. For an editor that state is the
  // user's unsaved text, and losing it is silent: the rebuilt pane re-reads the file and
  // looks like a clean editor of the same name. The draft therefore cannot live in the
  // component; this pins that it survives the gesture.
  it("keeps an editor's unsaved text when its tab is dragged out to a new tile", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await openAsTab("compose.yaml"); // editor opens as a 2nd tab
    await screen.findByRole("button", { name: "Save" });

    const view = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    const labels = () => Array.from(document.querySelectorAll(".tab-label")).map((n) => n.textContent);
    await waitFor(() => expect(labels()).toContain("project  compose.yaml *"));

    // Pull the editor's own tab to its tile's right edge → movePane, into a new tile.
    const editorTab = Array.from(document.querySelectorAll<HTMLElement>(".tab")).find((t) =>
      t.textContent?.includes("compose.yaml"),
    )!;
    dropTabOn(editorTab, 0, "right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    // Still dirty, and still holding the edit — not silently reverted to the file on disk.
    await waitFor(() => expect(labels()).toContain("project  compose.yaml *"));
    expect(document.querySelector(".file-pane-editor-bar .badge")?.textContent).toBe("unsaved");
    const moved = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    expect(moved.state.doc.toString()).toContain("# edited");
  });

  // The same mechanism covers leaving the workspace entirely: the layout is restored from
  // localStorage with the SAME pane ids, so the draft (keyed by pane id) is still the one
  // that pane's editor asks for. Navigating to another view and back is a rebuild too,
  // just a wholesale one.
  it("keeps an editor's unsaved text across leaving the Files view and coming back", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await openAsTab("compose.yaml");
    await screen.findByRole("button", { name: "Save" });
    const view = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    await waitFor(() =>
      expect(Array.from(document.querySelectorAll(".tab-label")).map((n) => n.textContent)).toContain(
        "project  compose.yaml *",
      ),
    );

    cleanup(); // leave the view
    renderView(Workspace); // …and come back to the persisted layout

    await screen.findByRole("button", { name: "Save" });
    expect(document.querySelector(".file-pane-editor-bar .badge")?.textContent).toBe("unsaved");
    const back = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    expect(back.state.doc.toString()).toContain("# edited");
  });

  // Enter on a row asks WHERE with the wireframe, like everything else in this workspace;
  // it used to open the "Stack as tab / Split → right" modal, which is gone.
  it("opens a text file via keyboard (Enter) by pointing at a target", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("README.md");
    // Select the row without opening it, then Enter on the list.
    const row = (await screen.findByText("README.md")).closest("tr")!;
    fireEvent.click(row);
    fireEvent.keyDown(document.querySelector(".fs-list")!, { key: "Enter" });

    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    expect(modalRequest()).toBeNull(); // no dialog: the tile is the question
    expect(document.querySelectorAll(".tab")).toHaveLength(1); // and nothing opened yet

    fireEvent.keyDown(window, { key: " " }); // the centre: open it as a tab here
    expect(await screen.findByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  // A pick's payload must not outlive it. Arming a second pick without cancelling the first
  // is reachable — the prefix key still works under the scrim — and the only thing stopping
  // the abandoned file from riding along into the next gesture is that arming clears it.
  // Found by neutralizing that line and watching nothing fail: `clear()` also nulls the
  // payload, so every pick that ENDS normally hides the leak, and only a pick superseded
  // mid-flight exposes it.
  it("does not carry one pick's payload into the next", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click((await screen.findByText("README.md")).closest("tr")!);
    fireEvent.keyDown(document.querySelector(".fs-list")!, { key: "Enter" });
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    expect(pressBind("C")).toBe("swallow"); // Split pane…, over the top of the placement
    await waitFor(() => expect(pickZones()).toHaveLength(4)); // four targets: it is a split
    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    // A split CONTINUES the tile it divides: a second listing of the same folder, not the
    // editor the abandoned placement was carrying.
    expect(tabLabels()).toEqual(["project", "project"]);
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  // The placement carries the FILE. Splitting instead of stacking is where a pick that fell
  // back to the host's empty-pane factory would show itself: two tiles either way, but the
  // new one would be a directory listing at the root rather than the editor.
  it("opens the file into whichever side the arrow key names", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click((await screen.findByText("README.md")).closest("tr")!);
    fireEvent.keyDown(document.querySelector(".fs-list")!, { key: "Enter" });
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(document.querySelectorAll(".split.h")).toHaveLength(1);
    // The editor is in the tile the arrow named, and the browser stayed where it was.
    expect(within(tiles()[1]).getByRole("button", { name: "Save" })).toBeTruthy();
    expect(tabLabels()).toEqual(["project", "project  README.md"]);
  });

  it("navigates rows with the arrow keys by moving the browser's focus", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    const list = document.querySelector(".fs-list")!;
    // Arrow down lands focus on the first row's link, and selection follows focus.
    fireEvent.keyDown(list, { key: "ArrowDown" });
    const first = document.activeElement as HTMLElement;
    expect(first.tagName).toBe("A");
    expect(first.closest(".fs-list")).toBeTruthy();
    expect(first.closest("tr")?.classList.contains("fs-selected")).toBe(true);
    // Arrow down again moves focus (and the single selection) to the next row.
    fireEvent.keyDown(list, { key: "ArrowDown" });
    const second = document.activeElement as HTMLElement;
    expect(second).not.toBe(first);
    expect(second.closest("tr")?.classList.contains("fs-selected")).toBe(true);
    expect(document.querySelectorAll(".fs-selected")).toHaveLength(1);
  });

  // --- Multi-selection ---------------------------------------------------------------
  // The project mount lists six rows in a stable order (directories first, then files,
  // each case-insensitively sorted): many-files, reports, web, .env, compose.yaml,
  // README.md. rowOf targets a row through its visible name, and selectedNames reads the
  // highlight back OUT of the DOM — so these assertions describe what the user sees
  // rather than restating the component's internal selection state.
  const rowOf = async (name: string) => (await screen.findByText(name)).closest("tr")!;
  const selectedNames = () =>
    Array.from(document.querySelectorAll(".fs-selected .fs-name")).map((n) => n.textContent);

  it("selects a range with shift+click, from the anchor to the clicked row", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    fireEvent.click(await rowOf("reports")); // a plain click sets the anchor
    expect(selectedNames()).toEqual(["reports"]);
    fireEvent.click(await rowOf("compose.yaml"), { shiftKey: true });
    expect(selectedNames()).toEqual(["reports", "web", ".env", "backup.tar.gz", "compose.yaml"]);
    // Shift-clicking back toward the anchor SHRINKS the range; an implementation that
    // merely adds the clicked row (or the new range) to the set would keep all five.
    fireEvent.click(await rowOf("web"), { shiftKey: true });
    expect(selectedNames()).toEqual(["reports", "web"]);
  });

  it("shift-clicking a file NAME extends the selection instead of opening the file", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await rowOf("compose.yaml"));
    // The name link is the open-this-file affordance; held shift it must select, or
    // ranging across a folder would open an editor tab per file dragged over.
    fireEvent.click(await screen.findByText("README.md"), { shiftKey: true });
    expect(selectedNames()).toEqual(["compose.yaml", "README.md"]);
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("extends and shrinks the selection with shift+ArrowDown / shift+ArrowUp", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    const list = document.querySelector(".fs-list")!;

    fireEvent.keyDown(list, { key: "ArrowDown" }); // cursor and selection onto row 1
    expect(selectedNames()).toEqual(["many-files"]);
    fireEvent.keyDown(list, { key: "ArrowDown", shiftKey: true });
    fireEvent.keyDown(list, { key: "ArrowDown", shiftKey: true });
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);
    // The cursor travelled with the range: DOM focus sits on the far end, not the anchor.
    expect((document.activeElement as HTMLElement).textContent).toContain("web");
    fireEvent.keyDown(list, { key: "ArrowUp", shiftKey: true });
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    // A bare arrow abandons the range and collapses onto the row it lands on.
    fireEvent.keyDown(list, { key: "ArrowDown" });
    expect(selectedNames()).toEqual(["web"]);
  });

  it("selects the run you drag across after pressing a row", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    // Press row 1, drag through rows 2 and 3, release: the crossed run is selected.
    fireEvent.mouseDown(await rowOf("many-files"), { button: 0 });
    fireEvent.mouseEnter(await rowOf("reports"));
    fireEvent.mouseEnter(await rowOf("web"));
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);
    // Dragging back up the way you came gives the rows back.
    fireEvent.mouseEnter(await rowOf("reports"));
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    fireEvent.mouseUp(window);

    // The release ends the drag: moving over further rows no longer selects them.
    fireEvent.mouseEnter(await rowOf("README.md"));
    expect(selectedNames()).toEqual(["many-files", "reports"]);
  });

  // The band draws nothing, so the only thing to observe is the selection — and its hit
  // test is pure geometry (offsetTop / offsetHeight), which jsdom reports as 0 for every
  // element. stubRowGeometry hands the rows a synthetic 20px stack: the code under test
  // is the hit test, not jsdom's absent layout, and a real browser probe checks the same
  // sweep against real boxes. Row 0 of the tbody is the ".." updir row, so the first
  // listing entry sits at y=20.
  const ROW_H = 20;
  const stubRowGeometry = () => {
    document.querySelectorAll<HTMLElement>(".fs-list tbody tr").forEach((tr, i) => {
      Object.defineProperty(tr, "offsetTop", { value: i * ROW_H, configurable: true });
      Object.defineProperty(tr, "offsetHeight", { value: ROW_H, configurable: true });
    });
  };

  it("sweeps a band from the listing's blank space, selecting the rows it crosses", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    stubRowGeometry();
    const list = document.querySelector(".fs-list")!;

    fireEvent.click(await rowOf("compose.yaml"));
    expect(selectedNames()).toEqual(["compose.yaml"]);

    // Press the blank space, then sweep across the first three entries (y 20..80).
    fireEvent.mouseDown(list, { button: 0, clientY: 25 });
    expect(selectedNames()).toEqual([]); // a bare press on blank space deselects
    fireEvent.mouseMove(window, { clientY: 75 });
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);
    // Sweeping back gives rows up again — membership is re-derived, never accumulated.
    fireEvent.mouseMove(window, { clientY: 45 });
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    fireEvent.mouseUp(window);

    // The release ends the sweep: later moves no longer select.
    fireEvent.mouseMove(window, { clientY: 200 });
    expect(selectedNames()).toEqual(["many-files", "reports"]);
  });

  it("starts a band from the blank part of the breadcrumb lane, but not from its controls", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    stubRowGeometry();

    // The lane is drawn by the tiling chrome, outside the pane component — a press on its
    // blank stretch still bands this pane's listing (it starts above the list, at a
    // negative y, and sweeps down into range).
    fireEvent.mouseDown(document.querySelector(".stack-subheader")!, { button: 0, clientY: -10 });
    fireEvent.mouseMove(window, { clientY: 45 });
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    fireEvent.mouseUp(window);

    // The lane's refresh button is a control: pressing it must not band.
    fireEvent.click(await rowOf("compose.yaml"));
    fireEvent.mouseDown(document.querySelector(".pane-refresh")!, { button: 0, clientY: -10 });
    fireEvent.mouseMove(window, { clientY: 45 });
    expect(selectedNames()).toEqual(["compose.yaml"]);
    fireEvent.mouseUp(window);

    // Nor does a press on a ROW — that gesture is the row drag-select.
    fireEvent.mouseDown(await rowOf("web"), { button: 0, clientY: 60 });
    fireEvent.mouseMove(window, { clientY: 200 });
    expect(selectedNames()).toEqual(["web"]); // the row press selected it; no band ran
    fireEvent.mouseUp(window);

    // The COLUMN header does band: nothing sorts on click, so it is blank space that
    // happens to hold labels. (Only listing rows are excluded, not every `tr`.)
    fireEvent.mouseDown(document.querySelector(".fs-list thead th")!, { button: 0, clientY: 5 });
    fireEvent.mouseMove(window, { clientY: 45 });
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    fireEvent.mouseUp(window);
  });

  it("does not fire a row's call to action on a held-modifier double click", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    fireEvent.click(await rowOf("many-files"));

    // Extending a range by shift-clicking twice in the same place also lands a dblclick.
    // That must not descend into the folder (nor, on a file row, open an editor tab).
    fireEvent.dblClick(await rowOf("web"), { shiftKey: true });
    fireEvent.dblClick(await rowOf("README.md"), { ctrlKey: true });
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument(); // still the same listing
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    // An unmodified double click still activates — the guard is about the modifier.
    fireEvent.dblClick(await rowOf("web"));
    expect(await screen.findByText("nginx.conf")).toBeInTheDocument();
  });

  it("answers the arrow keys when the pane is focused but nothing in it holds DOM focus", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    // The state you are in after clicking the tile's tab, pressing blank space, or
    // sweeping a band from the lane: the workspace's focused pane is this one, but DOM
    // focus is on <body>, so the row-level key handler never sees anything.
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);

    fireEvent.keyDown(document.body, { key: "ArrowDown" });
    expect(selectedNames()).toEqual(["many-files"]);
    // The first press also restores real focus, so the row handler owns the next one.
    expect(document.activeElement?.closest(".fs-list")).toBeTruthy();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(selectedNames()).toEqual(["reports"]);

    // With two tiles open, only the focused one answers — the fallback is document-wide,
    // so without the focus gate both listings would move at once.
    await splitViaEdge("right");
    // Both tiles list the same folder; wait for the new one's fetch, or its listing is
    // empty and the arrow has nothing to land on for reasons unrelated to focus.
    await waitFor(async () => expect(await screen.findAllByText("compose.yaml")).toHaveLength(2));
    (document.activeElement as HTMLElement | null)?.blur();
    fireEvent.keyDown(document.body, { key: "ArrowDown" });
    expect(document.querySelectorAll(".fs-selected")).toHaveLength(1);
  });

  it("makes tile focus follow DOM focus between tiles", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    await splitViaEdge("right");
    await waitFor(async () => expect(await screen.findAllByText("compose.yaml")).toHaveLength(2));

    const stacks = () => Array.from(document.querySelectorAll(".stack"));
    const focusedIdx = () => stacks().findIndex((s) => s.classList.contains("focused"));
    const was = focusedIdx();
    expect(was).toBeGreaterThanOrEqual(0);

    // Move DOM focus into the OTHER tile without a pointer press — Tab, or anything that
    // focuses itself. The tile focus (and with it the palette's target) must follow.
    const other = was === 0 ? 1 : 0;
    stacks()[other].querySelector<HTMLElement>(".fs-list tbody tr a")!.focus();
    expect(focusedIdx()).toBe(other);
  });

  it("makes the parent-directory row a control: one click up, and Tab can reach it", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("web")); // two levels down
    await screen.findByText("nginx.conf");

    const up = document.querySelector<HTMLAnchorElement>(".fs-updir a")!;
    expect(up).toBeTruthy();
    up.focus();
    expect(document.activeElement).toBe(up); // reachable by keyboard, not just a dblclick
    fireEvent.click(up); // a SINGLE click goes up
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();

    // Enter belongs to the link while it holds focus; the row cursor must not also fire
    // and open whatever it is sitting on. A keyboard open would ARM the placement
    // wireframe, so its absence is what turns "nothing happened" into something
    // observable — a tab count alone cannot tell "no open" from "an open still asking".
    fireEvent.click(await rowOf("README.md"));
    fireEvent.keyDown(document.querySelector(".fs-updir a")!, { key: "Enter" });
    await new Promise((r) => setTimeout(r, 0));
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(document.querySelectorAll(".tab")).toHaveLength(1); // nothing was opened
  });

  // ---- one gesture, one activation ---------------------------------------------------
  //
  // A click on a NAME opens straight away, so a double-click on a name is not one gesture
  // reported twice — it is a click that ACTS, and then a second click and a dblclick that
  // arrive after the listing has already been replaced under the pointer. All three used to
  // act. Chromium's own sequence for a double-click on `project` at the virtual root:
  //
  //   click    detail=1  "project"      at ""                    -> enters project
  //   click    detail=2  "many-files"   at "project"             -> enters project/many-files
  //   dblclick detail=2  "many-files"   at "project/many-files"  -> project/many-files/many-files
  //
  // The third one is the damaging one: its row came from the listing two navigations ago,
  // and childPath joins that name onto the path the pane has since moved to, so it lands on
  // a directory that does not exist. The 404 then took the whole table down with it, `..`
  // included — which is why this was reported as "the explorer stopped showing new folders
  // and `..` stopped working" rather than as "it went one folder too far".
  //
  // `detail` is spelled out on every event here rather than left to fireEvent's default,
  // because the count IS the mechanism: 1 opens a press sequence, anything above continues
  // the one before it. A test that fired bare clicks would pass on code that reads nothing.
  const crumbText = () => document.querySelector(".pane-crumbs")?.textContent;

  it("acts once per double-click on a folder name, wherever the second click lands", async () => {
    renderView(Workspace);
    const start = await rowOf("project");
    fireEvent.mouseDown(start, { detail: 1 });
    fireEvent.click(within(start).getByText("project"), { detail: 1 });
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument(); // inside `project`

    // The rest of the SAME gesture, landing on whichever row the navigation has just put
    // under the pointer — and on its CELL rather than its link, which is what happens
    // whenever the new row's name is shorter than the one that was clicked. That is the
    // case a "did the dblclick land on a name?" test misses.
    const landed = await rowOf("many-files");
    const cell = landed.querySelector("td")!;
    fireEvent.mouseDown(landed, { detail: 2 });
    fireEvent.click(cell, { detail: 2 });
    fireEvent.dblClick(cell, { detail: 2 });
    await new Promise((r) => setTimeout(r, 0));

    expect(crumbText()).toBe("All/project"); // one gesture, one folder
    expect(screen.queryByText("subdir-00")).toBeNull(); // never entered many-files
    expect(document.querySelector(".file-pane .error")).toBeNull();
    expect(document.querySelector(".fs-updir")).toBeTruthy(); // and the way out is still up
  });

  it("climbs one folder per double-click on '..', not one per click", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("web")); // two levels down
    await screen.findByText("nginx.conf");

    const up = document.querySelector<HTMLElement>(".fs-updir")!;
    fireEvent.mouseDown(up, { detail: 1 });
    fireEvent.click(up.querySelector("a")!, { detail: 1 });
    fireEvent.mouseDown(up, { detail: 2 });
    fireEvent.dblClick(up, { detail: 2 });
    await new Promise((r) => setTimeout(r, 0));

    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();
    expect(crumbText()).toBe("All/project"); // not "All", two folders up
  });

  // The other half of the report. Whatever puts a pane on a directory that will not list —
  // the stale-row bug above, a folder deleted under it, a mount that went away — the pane
  // has to keep the control that leaves the folder. The error used to replace the whole
  // list, so `..` went with it and the only way out was the breadcrumb; it also left the
  // pane's `listEl` ref pointing at nothing, which the band-select's document handler
  // measures against on every press.
  it("keeps '..' usable when the listing itself fails", async () => {
    seedPanes([{ kind: "files", path: "project/no-such-folder" }]);
    renderView(Workspace);

    await waitFor(() => expect(document.querySelector(".file-pane .error")).toBeTruthy());
    expect(document.querySelector(".fs-list")).toBeTruthy(); // the table frame survives
    expect(screen.queryByText("Empty folder.")).toBeNull(); // a failure is not an emptiness

    fireEvent.click(document.querySelector<HTMLAnchorElement>(".fs-updir a")!);
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();
    expect(document.querySelector(".file-pane .error")).toBeNull();
  });

  // --- Drag and drop -----------------------------------------------------------------
  // jsdom implements neither DataTransfer nor a real drag, so the transfer object is a
  // stand-in with the two behaviours the code actually uses: types reflects what has been
  // set, and get/setData round-trip. That makes the payload and the drop handling
  // observable; the gesture itself (press, move, release) is the browser's.
  const DND = "application/x-cornus-fs";
  function dataTransfer(seed: Record<string, string> = {}, files: File[] = []) {
    const data: Record<string, string> = { ...seed };
    return {
      files,
      dropEffect: "none",
      effectAllowed: "none",
      dragImage: null as HTMLElement | null,
      setDragImage(el: HTMLElement) {
        this.dragImage = el;
      },
      get types() {
        return [...Object.keys(data), ...(files.length ? ["Files"] : [])];
      },
      setData(type: string, value: string) {
        data[type] = value;
      },
      getData(type: string) {
        return data[type] ?? "";
      },
    };
  }
  // The payload carries each item's kind, so a drop can refuse what the BFF cannot do.
  const payload = (...paths: string[]) =>
    dataTransfer({ [DND]: JSON.stringify({ items: paths.map((path) => ({ path, dir: false })) }) });
  const dirPayload = (...paths: string[]) =>
    dataTransfer({ [DND]: JSON.stringify({ items: paths.map((path) => ({ path, dir: true })) }) });
  const paneEl = () => document.querySelector(".file-pane")!;

  // jsdom implements no DragEvent, so fireEvent falls back to a plain Event: dataTransfer
  // survives the init, the MODIFIER KEYS do not. Copy-vs-move hangs on shiftKey, so a
  // `fireEvent.drop(el, { shiftKey: true })` would silently test a copy while claiming to
  // test a move. Building the event and defining the property is what makes the two
  // distinguishable at all.
  function dropOn(el: Element, dt: unknown, shiftKey = false) {
    const ev = createEvent.drop(el, { dataTransfer: dt });
    Object.defineProperty(ev, "shiftKey", { value: shiftKey });
    fireEvent(el, ev);
  }

  it("refuses a drop into a read-only mount, at the cursor and at the release", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("seed")); // the `:ro` bind
    await screen.findByText("seed.sql");

    // The pane says why it will refuse, using the same fact the drag handling reads.
    const badge = within(paneEl() as HTMLElement).getByTitle(/read-only bind mount/);
    expect(badge).toBeInTheDocument();

    // DRAGOVER: the cursor must say no. preventDefault is what marks a drop target, so
    // an unprevented dragover IS the refusal — the browser shows "no drop" and never
    // fires a drop event.
    const over = createEvent.dragOver(paneEl(), { dataTransfer: payload("project/README.md") });
    fireEvent(paneEl(), over);
    expect(over.defaultPrevented).toBe(false);
    expect(document.querySelector(".fs-drop-here")).toBeNull();

    // RELEASE: and if one arrives anyway (a file row is not its own drop target, so its
    // event falls through to the pane), it is still refused — with a reason, not silence.
    dropOn(paneEl(), payload("project/README.md"));
    expect(await screen.findByText(/read-only/)).toBeInTheDocument();
    expect(screen.queryByText("README.md")).toBeNull();
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
  });

  it("accepts a drop into a writable mount, so the refusal is not blanket", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("assets"));
    await screen.findByText("cornus-logo.svg");
    expect(within(paneEl() as HTMLElement).queryByTitle(/read-only bind mount/)).toBeNull();

    const over = createEvent.dragOver(paneEl(), { dataTransfer: payload("project/README.md") });
    fireEvent(paneEl(), over);
    expect(over.defaultPrevented).toBe(true);
  });

  it("carries the whole selection in a drag, and a lone file's download URL", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    fireEvent.click(await rowOf("compose.yaml"));
    fireEvent.click(await rowOf("README.md"), { shiftKey: true });
    const dt = dataTransfer();
    fireEvent.dragStart(await screen.findByText("README.md"), { dataTransfer: dt });
    expect(JSON.parse(dt.getData(DND))).toEqual({
      items: [
        { path: "project/compose.yaml", dir: false },
        { path: "project/README.md", dir: false },
      ],
    });
    // Two files selected: no DownloadURL, which carries exactly one.
    expect(dt.getData("DownloadURL")).toBe("");

    fireEvent.click(await rowOf("README.md"));
    const one = dataTransfer();
    fireEvent.dragStart(await screen.findByText("README.md"), { dataTransfer: one });
    expect(one.getData("DownloadURL")).toContain("README.md");
    expect(one.getData("DownloadURL")).toContain("/fs/content?");
    // The pointer carries our own label, not a snapshot of the row (which would bring
    // the row's fill with it). The transparency itself is CSS, so what is checked here is
    // that a ghost of ours is handed over, named for what is being dragged, and that it
    // does not stay in the document.
    fireEvent.click(await rowOf("compose.yaml"));
    fireEvent.click(await rowOf("README.md"), { shiftKey: true });
    const ghosted = dataTransfer();
    fireEvent.dragStart(await screen.findByText("README.md"), { dataTransfer: ghosted });
    expect(ghosted.dragImage?.className).toBe("fs-drag-ghost");
    // Two items are few enough to name outright — no "2 items" consolidation.
    expect([...(ghosted.dragImage?.children ?? [])].map((c) => c.textContent)).toEqual([
      "📄 compose.yaml",
      "📄 README.md",
    ]);
    expect(ghosted.dragImage?.querySelector(".fs-drag-ghost-more")).toBeNull();
    expect(document.querySelector(".fs-drag-ghost")).toBeTruthy(); // snapshotted in place
    await new Promise((r) => setTimeout(r, 0));
    expect(document.querySelector(".fs-drag-ghost")).toBeNull(); // and cleaned up after

    // A folder has nothing to download.
    fireEvent.click(await rowOf("web"));
    const dir = dataTransfer();
    fireEvent.dragStart(await screen.findByText("web"), { dataTransfer: dir });
    expect(dir.getData("DownloadURL")).toBe("");
  });

  // These three mutate the filesystem, and the mock's trees are module state shared by
  // every test in this file — so they work inside project/many-files, the big scratch
  // listing nothing else asserts on. A drop test that landed in `project` would quietly
  // break the tests that count its rows.
  const openScratch = async () => {
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("many-files"));
    await screen.findByText("subdir-00");
  };

  // --- the same transfer, with a finger ------------------------------------------------
  // Moving a file used to be mouse-only: the row's drag was HTML5 drag-and-drop, and a
  // phone cannot perform one. It is a pointer drag now on touch (src/dnd.ts), reaching the
  // same `receiveInto` as the mouse and the cross-pane commands.
  //
  // The rects and the hit-test stub are what make it a real gesture: jsdom lays nothing out
  // and implements no elementFromPoint, so without them the finger is over nothing, the
  // release lands nowhere, and "the file did not move" would be true for the wrong reason.
  function fingerDragRow(from: HTMLElement, onto: HTMLElement) {
    const rects = new Map<Element, DOMRect>([
      [from, { left: 0, right: 300, top: 0, bottom: 20, width: 300, height: 20, x: 0, y: 0, toJSON() {} } as DOMRect],
      [onto, { left: 0, right: 300, top: 20, bottom: 40, width: 300, height: 20, x: 0, y: 20, toJSON() {} } as DOMRect],
    ]);
    for (const [el, r] of rects) (el as HTMLElement).getBoundingClientRect = () => r;
    const prevHit = document.elementFromPoint;
    document.elementFromPoint = ((x: number, y: number) =>
      [...rects.keys()].find((el) => {
        const r = rects.get(el)!;
        return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
      }) ?? null) as typeof document.elementFromPoint;
    vi.useFakeTimers();
    try {
      from.dispatchEvent(pointerEvent("pointerdown", { clientX: 10, clientY: 10 }, 1, "touch"));
      vi.advanceTimersByTime(DRAG_LIFT_MS + 1); // the dwell: this is what lifts it
      window.dispatchEvent(pointerEvent("pointermove", { clientX: 10, clientY: 30 }, 1, "touch"));
      window.dispatchEvent(pointerEvent("pointerup", { clientX: 10, clientY: 30 }, 1, "touch"));
      vi.advanceTimersByTime(1);
    } finally {
      vi.useRealTimers();
      document.elementFromPoint = prevHit;
    }
  }

  // A file and a folder of their own, used by nothing else: the mock filesystem is a module
  // singleton that no beforeEach rebuilds, so a test that moves a row moves it for every
  // test after it in this file. Sharing `file-050.go` with the shift-drop test below made
  // that one pass on an empty folder — green, and checking nothing.
  it("moves a file with a finger, once the drop has asked which it is", async () => {
    renderView(Workspace);
    await openScratch();

    const target = await rowOf("subdir-11");
    fingerDragRow(await rowOf("file-090.txt"), target);
    // The folder is lit while the finger is over it, and the drop asks the question Shift
    // answers for a mouse — a finger has no modifier to hold it down with.
    const req = modalRequest();
    expect(req?.kind).toBe("choice");
    expect(req && "options" in req && req.options.map((o) => o.value)).toEqual(["copy", "move"]);

    submitModal("move");
    await waitFor(() => expect(screen.queryByText("file-090.txt")).toBeNull()); // gone from here
    fireEvent.click(await screen.findByText("subdir-11"));
    expect(await screen.findByText("file-090.txt")).toBeInTheDocument(); // and arrived there
  });

  it("leaves everything alone when that question is dismissed", async () => {
    renderView(Workspace);
    await openScratch();

    fingerDragRow(await rowOf("file-091.css"), await rowOf("subdir-10"));
    expect(modalRequest()?.kind).toBe("choice");
    dismissModal();
    // Neither copied nor moved: a dismissed question is a drop that never happened.
    await new Promise((r) => setTimeout(r, 0));
    expect(await screen.findByText("file-091.css")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("subdir-10"));
    await screen.findByText("keep.txt"); // the folder's only row, as the fixture left it
    expect(screen.queryByText("file-091.css")).toBeNull();
  });

  it("copies on a plain drop and moves on a shift drop", async () => {
    renderView(Workspace);
    await openScratch();

    // Plain drop = copy: it arrives here and the source keeps it.
    dropOn(paneEl(), payload("assets/cornus-logo.svg"));
    expect(await screen.findByText("cornus-logo.svg")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("All")); // breadcrumb back to the root
    fireEvent.click(await screen.findByText("assets"));
    expect(await screen.findByText("cornus-logo.svg")).toBeInTheDocument(); // still there

    // Shift drop = move, and onto a FOLDER row it lands inside that folder.
    fireEvent.click(await screen.findByText("All"));
    await openScratch();
    dropOn(await rowOf("subdir-00"), payload("project/many-files/file-050.go"), true);
    await waitFor(() => expect(screen.queryByText("file-050.go")).toBeNull()); // gone from here
    fireEvent.click(await screen.findByText("subdir-00"));
    expect(await screen.findByText("file-050.go")).toBeInTheDocument(); // and arrived there
  });

  // The refusal names the OPERATION, not the gesture. It used to say "cannot drop…", which
  // was the right word while a drop was the only way to reach it; the cross-pane commands
  // reach the same check with no drag anywhere, and "drop" would then be describing
  // something that did not happen. Both spellings are asserted, because a message that
  // hard-coded either verb would still pass a test that only ever moved.
  it("refuses a folder put into itself, in the words of the operation", async () => {
    renderView(Workspace);
    await openScratch();
    dropOn(await rowOf("subdir-01"), dirPayload("project/many-files/subdir-01"), true);
    expect(await screen.findByText(/cannot move a folder into itself/)).toBeInTheDocument();
    expect(await screen.findByText("subdir-01")).toBeInTheDocument(); // and nothing moved

    dropOn(await rowOf("subdir-01"), dirPayload("project/many-files/subdir-01"));
    expect(await screen.findByText(/cannot copy a folder into itself/)).toBeInTheDocument();
  });

  it("uploads files dropped from outside the browser", async () => {
    renderView(Workspace);
    await openScratch();
    const file = new File(["dropped\n"], "from-desktop.txt", { type: "text/plain" });
    dropOn(paneEl(), dataTransfer({}, [file]));
    expect(await screen.findByText("from-desktop.txt")).toBeInTheDocument();
  });

  it("names the first five dragged items and counts the rest", async () => {
    renderView(Workspace);
    await openScratch(); // many-files has enough rows to overflow the ghost

    fireEvent.click(await rowOf("subdir-00"));
    fireEvent.click(await rowOf("subdir-06"), { shiftKey: true }); // seven items
    const dt = dataTransfer();
    fireEvent.dragStart(await screen.findByText("subdir-03"), { dataTransfer: dt });

    const rows = [...(dt.dragImage?.children ?? [])].map((c) => c.textContent);
    // The first five keep their identities; only the tail is summarised.
    expect(rows).toEqual([
      "📁 subdir-00",
      "📁 subdir-01",
      "📁 subdir-02",
      "📁 subdir-03",
      "📁 subdir-04",
      "+2 more",
    ]);
    expect(dt.dragImage?.textContent).not.toContain("subdir-05");
    await new Promise((r) => setTimeout(r, 0));
  });

  it("starts a drag from any part of a selected row, and sweeps from an unselected one", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    // An UNSELECTED row: pressing a plain cell sweeps, so the row gives up its
    // draggability for the press (the two gestures cannot share one — a started drag
    // stops the mousemove the sweep needs).
    const many = await rowOf("many-files");
    fireEvent.mouseDown(many, { button: 0 });
    expect((many as HTMLTableRowElement).draggable).toBe(false);
    fireEvent.mouseEnter(await rowOf("reports"));
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    fireEvent.mouseUp(window);
    expect((many as HTMLTableRowElement).draggable).toBe(true); // and takes it back

    // A SELECTED row: pressing the same plain cell drags instead — no sweep, and the
    // selection it carries is left intact.
    fireEvent.mouseDown(many, { button: 0 });
    expect((many as HTMLTableRowElement).draggable).toBe(true);
    fireEvent.mouseEnter(await rowOf("README.md"));
    expect(selectedNames()).toEqual(["many-files", "reports"]);
    // The row is the drag source now, so the payload comes from a dragstart on the ROW.
    const dt = dataTransfer();
    fireEvent.dragStart(many, { dataTransfer: dt });
    expect(JSON.parse(dt.getData(DND)).items.map((i: { path: string }) => i.path)).toEqual([
      "project/many-files",
      "project/reports",
    ]);
    fireEvent.mouseUp(window);
    await new Promise((r) => setTimeout(r, 0));
  });

  it("keeps a multi-selection when the drag starts on one of its rows", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    fireEvent.click(await rowOf("many-files"));
    fireEvent.click(await rowOf("web"), { shiftKey: true });
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);

    // Pressing the NAME of an already-selected row must not collapse the selection — the
    // drag has to carry all three — and must not start a sweep either: the pointer's trip
    // to the drop target would otherwise grow the very payload it is carrying. Chromium
    // showed this as a two-tile drag reporting "moved 1/2" for an item nobody dragged.
    fireEvent.mouseDown(await screen.findByText("web"), { button: 0 });
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);
    fireEvent.mouseEnter(await rowOf("README.md"));
    expect(selectedNames()).toEqual(["many-files", "reports", "web"]);
    fireEvent.mouseUp(window);
  });

  it("copies a folder across mounts, tree and all", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("assets"));
    await screen.findByText("cornus-logo.svg");

    // Rename cannot cross mounts, so this rides the copy path — which walks the tree
    // server-side (Server.FsCopy). It used to be refused outright.
    dropOn(paneEl(), dirPayload("project/many-files/subdir-04"));
    expect(await screen.findByText("copied 1 item")).toBeInTheDocument();
    // Wait for the ghost row to give way to the real one before browsing into it.
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
    expect(await screen.findByText("subdir-04")).toBeInTheDocument();
    // The folder arrived with its contents, not as an empty shell.
    fireEvent.click(await screen.findByText("subdir-04"));
    expect(await screen.findByText("keep.txt")).toBeInTheDocument();
  });

  it("offers no drop back into the folder the drag came from", async () => {
    renderView(Workspace);
    await openScratch();
    const dt = payload("project/many-files/file-000.ts");

    // Start a drag from this pane, then hover its own listing: dropping there would put
    // the items where they already are, so it is not offered at all.
    fireEvent.dragStart(await rowOf("file-000.ts"), { dataTransfer: dt });
    fireEvent.dragOver(paneEl(), { dataTransfer: dt });
    expect(paneEl().classList.contains("fs-drop-here")).toBe(false);
    dropOn(paneEl(), dt);
    await new Promise((r) => setTimeout(r, 0));
    expect(document.querySelector(".toast")).toBeNull(); // nothing happened, nothing said

    // A subfolder of the SAME pane is a different directory, so that drop still stands.
    const sub = await rowOf("subdir-00");
    fireEvent.dragOver(sub, { dataTransfer: dt });
    expect(sub.classList.contains("fs-drop-here")).toBe(true);

    // And once the drag is over, this folder accepts drops again.
    fireEvent.dragEnd(await rowOf("file-000.ts"));
    fireEvent.dragOver(paneEl(), { dataTransfer: dt });
    expect(paneEl().classList.contains("fs-drop-here")).toBe(true);
  });

  it("reports a transfer in the global toaster, not inside the receiving pane", async () => {
    renderView(Workspace);
    await openScratch();
    dropOn(paneEl(), payload("assets/cornus-logo.png"));

    // The outcome lands in the toaster — one line, dismissable, outside every pane, so a
    // message never reflows the listing that produced it.
    const toast = await screen.findByText("copied 1 item");
    expect(toast.closest(".toaster")).toBeTruthy();
    expect(toast.closest(".file-pane")).toBeNull();
    expect(document.querySelector(".file-pane-status")).toBeNull(); // the old lane is gone

    fireEvent.click(toast);
    expect(screen.queryByText("copied 1 item")).toBeNull(); // a click dismisses it
  });

  it("moves a folder across mounts, leaving nothing behind", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("assets"));
    await screen.findByText("cornus-logo.svg");

    // A move is ONE request now: the BFF renames when both sides share a filesystem and
    // copies-then-deletes when they do not, so the browser no longer composes it. The
    // recorder below is what keeps that true — a regression to copy+delete still passes
    // every assertion about the resulting tree.
    const calls: string[] = [];
    const inner = globalThis.fetch;
    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      calls.push(`${init?.method ?? "GET"} ${path.split("?")[0]}`);
      return inner(input, init);
    };
    onTestFinished(() => {
      globalThis.fetch = inner;
    });

    dropOn(paneEl(), dirPayload("project/many-files/subdir-05"), true);
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
    expect(await screen.findByText("subdir-05")).toBeInTheDocument();
    fireEvent.click(await screen.findByText("All"));
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("many-files"));
    await screen.findByText("subdir-00");
    expect(screen.queryByText("subdir-05")).toBeNull(); // gone from the source

    // One move request per dragged item — NOT "exactly one request", since a multi-item
    // drop issues one per item and always will. And no DELETE at all: the source going
    // away is the move's own doing, not a second call from here.
    const moves = calls.filter((c) => c === "POST /.cornus/web/fs/move");
    expect(moves).toHaveLength(1);
    expect(calls.filter((c) => c.startsWith("DELETE"))).toHaveLength(0);
    expect(calls.filter((c) => c === "POST /.cornus/web/fs/copy")).toHaveLength(0);
  });

  it("shows a ghost row while a transfer is in flight, then the real row", async () => {
    renderView(Workspace);
    await openScratch();

    // A name the listing does not already hold — a ghost for a row that is already there
    // (an overwrite) has nothing to announce, so it is dropped as soon as it is made.
    dropOn(paneEl(), payload("project/compose.yaml"));
    // Synchronously after the drop — the transfer has not been awaited yet. The pane
    // admits it immediately instead of sitting unchanged for a round trip.
    const ghost = document.querySelector(".fs-ghost")!;
    expect(ghost).toBeTruthy();
    expect(ghost.textContent).toContain("compose.yaml");
    expect(ghost.textContent).toContain("copying…");

    // The listing comes back with the real row and the ghost gives way to it — one row,
    // not two.
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
    expect(await screen.findByText("compose.yaml")).toBeInTheDocument();
  });

  it("wipes the ghost when the transfer fails, leaving no trace of a file", async () => {
    renderView(Workspace);
    await openScratch();

    dropOn(paneEl(), payload("project/nothing-here.txt"));
    expect(document.querySelector(".fs-ghost")?.textContent).toContain("nothing-here.txt");

    // The copy 404s, so the ghost is a promise the transfer did not keep: it goes, and no
    // row takes its place.
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
    expect(screen.queryByText("nothing-here.txt")).toBeNull();
    expect(await screen.findByText(/copied 0\/1/)).toBeInTheDocument();
  });

  it("leaves its ghosts behind when the pane navigates away", async () => {
    renderView(Workspace);
    await openScratch();
    dropOn(paneEl(), payload("project/.env"));
    expect(document.querySelector(".fs-ghost")).toBeTruthy();

    // A ghost stands for something arriving in THIS folder; another folder must not
    // inherit it.
    fireEvent.click(await screen.findByText(".."));
    await screen.findByText("compose.yaml");
    expect(document.querySelector(".fs-ghost")).toBeNull();
  });

  it("asks before a drop overwrites something, and not when it would not", async () => {
    renderView(Workspace);
    await openScratch();
    // A folder made here, rather than a fixture name: the mock filesystem is module state
    // shared by this whole file, so any pre-existing name makes "is this an overwrite?"
    // depend on which tests ran first. An empty folder makes both halves deterministic.
    runCommand("files:new-folder");
    submitModal("preflight-target");
    const target = await rowOf("preflight-target");

    // Into empty space there is nothing to warn about, so there is no dialog at all — a
    // prompt on every drop is a prompt nobody reads.
    dropOn(target, payload("assets/cornus-logo.svg"));
    await screen.findByText(/copied 1 item/);
    noDialog();

    // The same name again would replace it, and the preflight sees that before anything
    // is written.
    dropOn(await rowOf("preflight-target"), payload("assets/cornus-logo.svg"));
    const d = await dialog();
    expect(within(d).getByText(/will be overwritten/)).toBeInTheDocument();

    // Declining does nothing at all, and leaves no ghost behind.
    fireEvent.click(within(d).getByRole("button", { name: "Cancel" }));
    await waitFor(noDialog);
    await waitFor(() => expect(document.querySelector(".fs-ghost")).toBeNull());
  });

  it("marks what a drop just delivered, and stops marking it", async () => {
    renderView(Workspace);
    await openScratch();
    dropOn(paneEl(), payload("assets/cornus-logo.svg"));
    // The mock filesystem is module state shared by this whole file, so by now the name
    // may already be there — in which case the drop is an overwrite and asks first. The
    // prompt arrives only after the preflight round trip, so wait for EITHER outcome
    // before deciding; answering keeps this test about the arrival mark, not the prompt.
    await waitFor(() =>
      expect(document.querySelector(".fs-arrived") ?? screen.queryByRole("dialog")).toBeTruthy(),
    );
    if (screen.queryByRole("dialog")) await answer("Copy");

    // The arrival is marked where it landed, so the eye can find it in a long listing.
    const arrived = await waitFor(() => {
      const row = document.querySelector(".fs-arrived");
      expect(row).toBeTruthy();
      return row!;
    });
    expect(arrived.textContent).toContain("cornus-logo.svg");
    // It is a mark, not a selection: nothing was selected by the drop.
    expect(selectedNames()).toEqual([]);
    // And it clears itself.
    await waitFor(() => expect(document.querySelector(".fs-arrived")).toBeNull(), { timeout: 4000 });
  });

  it("marks the receiving folder when the delivery landed out of sight inside it", async () => {
    renderView(Workspace);
    await openScratch();
    // Dropped onto a folder row, the items are inside it and invisible from here — so the
    // folder that took them is what gets marked.
    dropOn(await rowOf("subdir-03"), payload("assets/cornus-logo.png"));
    const arrived = await waitFor(() => {
      const row = document.querySelector(".fs-arrived");
      expect(row).toBeTruthy();
      return row!;
    });
    expect(arrived.textContent).toContain("subdir-03");
    expect(document.querySelectorAll(".fs-arrived")).toHaveLength(1);
  });

  it("marks the folder row under the pointer as the drop target", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    const dir = await rowOf("web");
    fireEvent.dragOver(dir, { dataTransfer: payload("assets/cornus-logo.svg") });
    expect(dir.classList.contains("fs-drop-here")).toBe(true);
    // A FILE row is not its own target — the drop falls through to the pane's folder.
    const file = await rowOf("compose.yaml");
    fireEvent.dragOver(file, { dataTransfer: payload("assets/cornus-logo.svg") });
    expect(file.classList.contains("fs-drop-here")).toBe(false);
    expect(paneEl().classList.contains("fs-drop-here")).toBe(true);
  });

  it("takes no drop at the virtual root, whose entries are mounts and not paths", async () => {
    renderView(Workspace);
    await screen.findByText("shop-web");
    fireEvent.dragOver(paneEl(), { dataTransfer: payload("project/README.md") });
    expect(paneEl().classList.contains("fs-drop-here")).toBe(false);
    dropOn(paneEl(), payload("project/README.md"));
    // Nothing was created at the root, which still lists exactly its mounts.
    expect(screen.queryByText("README.md")).toBeNull();
  });

  it("toggles individual rows with ctrl/cmd+click, keeping the rest selected", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");

    fireEvent.click(await rowOf("many-files"));
    fireEvent.click(await rowOf("README.md"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["many-files", "README.md"]); // non-adjacent
    fireEvent.click(await rowOf("many-files"), { ctrlKey: true }); // and un-pick again
    expect(selectedNames()).toEqual(["README.md"]);
  });

  it("selects the whole listing with ctrl/cmd+A", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    fireEvent.keyDown(document.querySelector(".fs-list")!, { key: "a", ctrlKey: true });
    expect(selectedNames()).toEqual([
      "many-files",
      "reports",
      "web",
      ".env",
      "backup.tar.gz",
      "compose.yaml",
      "README.md",
    ]);
  });

  it("states the selection size as a badge over the breadcrumb, not as a new row", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    const badge = () => document.querySelector(".stack-subheader .fs-selection-count");

    fireEvent.click(await rowOf("reports"));
    expect(badge()).toBeNull(); // a single row is already named by the palette entries
    fireEvent.click(await rowOf("compose.yaml"), { shiftKey: true });
    // The pane registers its actions AFTER the sub-header first renders, so the badge
    // only tracks the selection if that registration is reactive.
    expect(badge()?.textContent).toBe("5 selected");
    fireEvent.click(await rowOf("web"));
    expect(badge()).toBeNull();
  });

  it("scopes the palette actions to the whole selection, withdrawing single-row rename", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    const titleOf = (id: string) => allCommands().find((c) => c.id === id)?.title;

    fireEvent.click(await rowOf("compose.yaml"));
    expect(titleOf("files:rename")).toBe('Rename "compose.yaml"');
    expect(titleOf("files:delete")).toBe('Delete "compose.yaml"');

    fireEvent.click(await rowOf("README.md"), { shiftKey: true });
    // The palette entry has to state its true target: pressing x behind the prefix runs
    // it with no further confirmation of WHAT it hits.
    expect(titleOf("files:delete")).toBe("Delete 2 items");
    expect(titleOf("files:copy")).toBe("Copy 2 items to another pane…");
    expect(titleOf("files:move")).toBe("Move 2 items to another pane…");
    expect(titleOf("files:download")).toBe("Download 2 items");
    // Rename asks for ONE new name, so it is withdrawn while several rows are selected.
    expect(allCommands().some((c) => c.id === "files:rename")).toBe(false);
  });

  it("deletes every selected row behind a single confirm", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    // many-files is a big listing no other test asserts on — the mock filesystem is
    // module state shared across this file, so a destructive test must not eat a fixture
    // its neighbours rely on.
    fireEvent.click(await screen.findByText("many-files"));
    await screen.findByText("file-000.ts");

    fireEvent.click(await rowOf("file-000.ts"));
    fireEvent.click(await rowOf("file-001.tsx"), { shiftKey: true });
    expect(selectedNames()).toEqual(["file-000.ts", "file-001.tsx"]);

    runCommand("files:delete");
    submitModal(true); // one confirm covers the whole selection
    await waitFor(() => {
      expect(screen.queryByText("file-000.ts")).toBeNull();
      expect(screen.queryByText("file-001.tsx")).toBeNull();
    });
    expect(await screen.findByText("file-002.go")).toBeInTheDocument(); // neighbours survive
  });

  it("downloads every selected file, one request per row", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    fireEvent.click(await rowOf("compose.yaml"));
    fireEvent.click(await rowOf("README.md"), { shiftKey: true });

    // Downloads leave no DOM trace (a synthetic <a> is clicked and removed), so the
    // observation point is the click itself.
    const hrefs: string[] = [];
    const clicked = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        hrefs.push(this.href);
      });
    try {
      runCommand("files:download");
    } finally {
      clicked.mockRestore();
    }
    expect(hrefs).toHaveLength(2);
    expect(hrefs[0]).toContain("compose.yaml");
    expect(hrefs[1]).toContain("README.md");
  });

  it("exposes actions as contextual palette commands, not a toolbar", async () => {
    renderView(Workspace);
    await screen.findByText("shop-web");
    // No on-screen action buttons; refresh moved onto the pane title bar.
    expect(screen.queryByRole("button", { name: "New folder" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Copy" })).toBeNull();
    expect(document.querySelector(".pane-refresh")).toBeTruthy();
    // At the root the mutation commands do not apply; entering a mount reveals them.
    expect(allCommands().some((c) => c.id === "files:new-folder")).toBe(false);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("compose.yaml");
    expect(allCommands().some((c) => c.id === "files:new-folder")).toBe(true);
  });

  it("creates a new folder via a contextual palette command + text modal", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project")); // focus a pane inside a mount
    await screen.findByText("compose.yaml");
    runCommand("files:new-folder"); // opens the text modal
    submitModal("brandnew");
    expect(await screen.findByText("brandnew")).toBeInTheDocument();
  });

  // Copying to a typed path is no longer a command of its own — it is the pane chooser's
  // escape hatch, taken by TYPING. With one pane open there is nowhere else for the files
  // to go, which is exactly why the command must still be live here: gating it on "some
  // pane could receive this" would withdraw it in the only layout the typed route exists
  // for. The old "Copy … to…" prompt reached the same place and is gone.
  it("copies to a typed path through the chooser's escape hatch, and opens a tab there", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    // Select the row without opening it (clicking the name would open a new pane).
    fireEvent.click((await screen.findByText("README.md")).closest("tr")!);
    runCommand("files:copy");
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    // The only pane there is is the source, so the list has nothing to offer and says so.
    expect(document.querySelector(".pane-chooser-item.disabled .pane-chooser-why")?.textContent).toBe(
      "already here",
    );
    expect(screen.getByText("an arbitrary location")).toBeTruthy();

    // A letter leaves the list AND is the first character typed: the prompt opens already
    // holding it. Asserting the field's value is the whole point — a chooser that merely
    // opened an empty prompt would pass every other line here.
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeNull());
    const field = (await screen.findByRole("textbox")) as HTMLInputElement;
    expect(field.value).toBe("a");
    // The caret is AFTER it, not selecting it. The field normally opens with its suggestion
    // selected so the first keystroke replaces it — which here would eat the letter the
    // user already typed, on the very next one. Only the caret says which of the two this
    // is; the value is identical either way.
    expect(field.selectionStart).toBe(1);
    submitModal("assets");

    // The copy landed under the assets mount, and a tab on that folder came up beside the
    // source — the typed destination is the one no pane was showing, so without the tab the
    // files would be somewhere with no view of them.
    await waitFor(() => expect(tabLabels()).toEqual(["project", "assets"]));
    // Scoped to the tab that came up, because the source tab is still mounted behind it and
    // is also showing a README.md — the one that was copied FROM. An unscoped query here
    // would pass without anything having been copied at all.
    const shown = () => within(document.querySelector<HTMLElement>(".stack-pane.active")!);
    expect(await shown().findByText("cornus-logo.svg")).toBeInTheDocument(); // it is assets
    expect(await shown().findByText("README.md")).toBeInTheDocument(); // and the copy is in it
  });

  // ---- opening the file you picked out --------------------------------------------------
  //
  // Open is what this screen does most, and it was reachable only by gesture: no command,
  // so the palette could not name it, could not search it, and could not run it. It now has
  // one — carrying NO key, because Enter on the row already is the key and the palette's
  // job here is to say so to someone who has not found that out yet.

  it("opens the selected file from the palette, by asking where it goes", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    // Select the row without opening it — clicking the NAME is already an open, and this
    // test is about the command, so it must not be the click that did the work.
    fireEvent.click(await rowOf("README.md"));

    runCommand("files:open");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    expect(document.querySelectorAll(".tab")).toHaveLength(1); // and nothing opened yet

    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(within(tiles()[1]).getByRole("button", { name: "Save" })).toBeTruthy();
    expect(tabLabels()).toEqual(["project", "project  README.md"]);
  });

  // ONE command covers "editor / preview": which of the two you get is the FILE's answer,
  // not a second command's. An image is opened here to pin that — the viewer, no Save —
  // from the same entry that opened the editor above.
  it("opens an image through the same command, into the viewer", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("assets"));
    fireEvent.click(await rowOf("cornus-logo.png"));

    runCommand("files:open");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: " " }); // the centre: as a tab here
    expect((await screen.findByRole("img")).getAttribute("src")).toContain("cornus-logo.png");
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  // ONE key, and it is a CHORD. Plain Enter is the listing's own activation and cannot be
  // claimed: the direct lookup runs on a document keydown against anything that is not a
  // text field, so taking it would take it from every button, link and updir row on the
  // screen. Ctrl+Enter is "Enter, but elsewhere", which is the whole command. No prefix
  // `bind` at all — a prefix is for something the focused thing cannot hear, and this
  // listing hears Enter perfectly well.
  it("takes the chord and leaves plain Enter to the listing", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await rowOf("README.md"));
    const cmd = () => allCommands().find((c) => c.id === "files:open")!;
    expect(cmd().disabled).toBeUndefined(); // live, so this is not "absent, therefore unbound"
    expect(bindsOf(cmd())).toEqual([]);
    // jsdom is not a Mac, so this pins the non-Mac spelling AND that only one is registered.
    expect(directsOf(cmd())).toEqual(["Ctrl+Enter"]);

    // Plain Enter reaches nothing at the app level: the row handles it itself.
    expect(pressDirect("Enter")).toBe(false);
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
  });

  // The row names what it will open, and when it cannot, it says which of the three reasons
  // applies. All are asserted through the REGISTERED command rather than through the
  // palette's rendering: the title and the disabled reason are what the run agrees with, and
  // a test that read the DOM would pass on a row whose label had drifted from the answer
  // behind it.
  it("says why Open cannot run, rather than dropping out of the palette", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await screen.findByText("README.md");
    const cmd = () => allCommands().find((c) => c.id === "files:open");
    expect(cmd()?.disabled).toBe("select a file or folder");
    expect(cmd()?.title).toBe("Open…");

    fireEvent.click(await rowOf("README.md"));
    expect(cmd()?.disabled).toBeUndefined();
    expect(cmd()?.title).toBe('Open "README.md"…');

    fireEvent.click(await rowOf("web")); // a folder opens too — it is the same command
    expect(cmd()?.disabled).toBeUndefined();
    expect(cmd()?.title).toBe('Open "web/"…');

    fireEvent.click(await rowOf("backup.tar.gz")); // neither the editor nor the viewer takes it
    expect(cmd()?.disabled).toBe("no editor or preview for this file");
    // A range is not a target: this makes ONE pane showing ONE thing, and picking silently
    // for you which of the two it is would be a guess dressed as an action.
    fireEvent.click(await rowOf("README.md"));
    fireEvent.click(await rowOf("compose.yaml"), { ctrlKey: true });
    expect(cmd()?.disabled).toBe("select just one row");

    // Present throughout, in every one of those states — the palette entry never vanishes,
    // it only greys. Asserted last so the reasons above are known to be reasons on a row
    // that is THERE, rather than readings of an absent command.
    expect(allCommands().some((c) => c.id === "files:open")).toBe(true);
  });

  // ---- the same command, aimed at a folder -----------------------------------------------
  //
  // A FOLDER is the other row this command opens, and it used to be a command of its own
  // ("Open in a new tab"). The two were one sentence with different objects, separated only
  // by the folder one stacking a tab where the file one asked; once the file open asked for
  // the mouse as well as the keyboard, nothing but the payload was left between them.

  it("opens the selected folder through the same command, asking where it goes", async () => {
    renderView(Workspace);
    await openScratch();
    fireEvent.click(await rowOf("subdir-10"));

    // No prefix, and claimed — the same contract as F5.
    expect(pressDirect("Enter", document.body, { ctrlKey: true })).toBe(true);
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    expect(tabLabels()).toEqual(["project  many-files"]); // asked, opened nothing yet

    fireEvent.keyDown(window, { key: " " }); // the centre: as a tab on this tile
    await waitFor(() => expect(tabLabels()).toEqual(["project  many-files", "project  subdir-10"]));
    expect(tiles()).toHaveLength(1); // a tab, because that is the answer given
    const shown = () => within(document.querySelector<HTMLElement>(".stack-pane.active")!);
    expect(await shown().findByText("keep.txt")).toBeInTheDocument();
  });

  // The title is the only thing that says which KIND of row is about to open, now that one
  // command opens both — so a directory says so with a trailing slash. `logs` beside
  // `logs.txt` is not a distinction to make from the extension.
  it("names a folder with a trailing slash and a file without one", async () => {
    renderView(Workspace);
    await openScratch();
    const title = () => allCommands().find((c) => c.id === "files:open")?.title;

    fireEvent.click(await rowOf("subdir-10"));
    expect(title()).toBe('Open "subdir-10/"…');
    fireEvent.click(await rowOf("file-063.json"));
    expect(title()).toBe('Open "file-063.json"…');
  });

  it("keeps Ctrl+Enter to itself when the selection is not one openable row", async () => {
    renderView(Workspace);
    await openScratch();
    const why = () => allCommands().find((c) => c.id === "files:open")?.disabled;
    expect(why()).toBe("select a file or folder"); // nothing selected

    // Two rows: a command that quietly took the first would open something the user did not
    // ask for and leave the other silently ignored. Two FOLDERS, because one of each would
    // also be refused by the kind check and would not reach this one.
    fireEvent.click(await rowOf("subdir-11"));
    fireEvent.click(await rowOf("subdir-10"), { ctrlKey: true });
    expect(why()).toBe("select just one row");

    // Still swallowed rather than handed back — same rule as F5, so the key means one
    // thing throughout a mount instead of falling through to the listing's own Enter,
    // which never looked at the modifier and would open whatever the cursor is on.
    expect(pressDirect("Enter", document.body, { ctrlKey: true })).toBe(true);
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(tabLabels()).toEqual(["project  many-files"]);

    // And PLAIN Enter is not claimed at all — it is the listing's own open, and a chord
    // bind that swallowed the unmodified key would take the primary gesture of the screen
    // away. The direct lookup matches the chord; this is the line that says so.
    expect(pressDirect("Enter")).toBe(false);
  });

  it("puts a new pane on the folder you picked, rather than at the root", async () => {
    renderView(Workspace);
    await openScratch();
    fireEvent.click(await rowOf("subdir-11"));

    // "New pane…" arms the placement pick; the pane it then creates is the assertion. A
    // root pane would list the MOUNTS (shop-web and friends), which is what this did
    // before and what a regression would do again.
    runCommand("ws:new");
    await waitFor(() => expect(pickZones().length).toBeGreaterThan(0));
    fireEvent.click(pickZones().find((z) => z.classList.contains("zone-right"))!);
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(await within(tiles()[1]).findByText("keep.txt")).toBeInTheDocument();
    expect(within(tiles()[1]).queryByText("shop-web")).toBeNull();
  });

  it("opens a folder in a new pane on a shift-click of its NAME, and nowhere else", async () => {
    renderView(Workspace);
    await openScratch();

    // CTRL/CMD-CLICK IS MULTI-SELECT AND STAYS THAT WAY, everywhere — that is what put this
    // gesture on Shift. Asserted first and on the NAME, because the name is where the new
    // gesture lives and so the only place ctrl could have been eaten by it.
    fireEvent.click(await rowOf("subdir-10"));
    fireEvent.click(await screen.findByText("subdir-11"), { ctrlKey: true });
    expect(selectedNames()).toEqual(["subdir-10", "subdir-11"]);
    expect(document.querySelector(".pane-pick-zone")).toBeNull();

    // Shift-click on the ROW is still range-extend, which is the line the new gesture has
    // to stay on the far side of.
    fireEvent.click(await rowOf("subdir-10"));
    fireEvent.click(await rowOf("subdir-11"), { shiftKey: true });
    expect(selectedNames()).toEqual(["subdir-10", "subdir-11"]);
    expect(document.querySelector(".pane-pick-zone")).toBeNull();

    // On the NAME of a folder, Shift opens a new pane instead: the browser's own "modified
    // click on a link opens it elsewhere".
    fireEvent.click(await screen.findByText("subdir-10"), { shiftKey: true });
    await waitFor(() => expect(pickZones().length).toBeGreaterThan(0));
    fireEvent.click(pickZones().find((z) => z.classList.contains("zone-right"))!);
    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(await within(tiles()[1]).findByText("keep.txt")).toBeInTheDocument();

    // A FILE's name keeps the old meaning — extend, not open. Shift-clicking one must not
    // arm a pick and must not open the file either.
    fireEvent.click(within(tiles()[0]).getByText("file-064.yaml"), { shiftKey: true });
    expect(document.querySelector(".pane-pick-zone")).toBeNull();
    expect(tabLabels()).toEqual(["project  many-files", "project  subdir-10"]);
  });

  // ---- cross-pane transfers (F5 / F6) --------------------------------------------------
  //
  // These mutate the shared mock filesystem, so like the drop tests they work inside
  // project/many-files and on subdirectories nothing else asserts on.

  // Two tiles on DIFFERENT folders, which is what a transfer needs: a split INHERITS the
  // source's location, so a second pane left where the split put it is refused as "already
  // here" — the same no-op as copying into your own folder. Ends with the source focused.
  async function twoFolders(dest: string) {
    renderView(Workspace);
    await openScratch();
    runCommand("ws:split-h");
    await waitFor(() => expect(tiles()).toHaveLength(2));
    fireEvent.click(await within(tiles()[1]).findByText(dest));
    await waitFor(() => expect(within(tiles()[1]).queryByText("keep.txt")).toBeTruthy());
    fireEvent.click(tiles()[0].querySelector(".tab-label")!);
    await waitFor(() => expect(tiles()[0].classList.contains("focused")).toBe(true));
  }

  // The PANE rows only. The escape hatch shares their styling class but is not one of them:
  // it has no tile, no number and no place in the walk, so counting it would make every
  // index here off by nothing today and by one the moment a test asserts a length.
  const chooserRows = () =>
    Array.from(document.querySelectorAll<HTMLElement>(".pane-chooser-item:not(.pane-chooser-other)"));

  it("copies the selection into the pane you pick, on a bare F5", async () => {
    await twoFolders("subdir-07");
    fireEvent.click(within(tiles()[0]).getByText("file-060.ts").closest("tr")!);

    // Claimed with NO prefix in front of it — the whole difference between this key and
    // every other bind on the screen, and the thing that would otherwise reload the page.
    expect(pressDirect("F5")).toBe(true);
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    expect(document.querySelector(".pane-chooser-title")?.textContent).toBe('Copy "file-060.ts" to…');

    // It opened on the pane it can actually use. The source is listed, greyed and out of
    // the way — not removed, because the numbers are positions and a hole would renumber
    // the tiles the walk is named after.
    expect(chooserRows()[0].classList.contains("disabled")).toBe(true);
    expect(within(chooserRows()[0]).getByText("already here")).toBeTruthy();
    expect(chooserRows()[1].classList.contains("selected")).toBe(true);

    // With an escape offered, every bare letter leaves the list — so the panel stops
    // advertising hjkl as movement. Naming a key that now does the opposite of what the
    // hint says would be worse than naming none, and this is the only place that trade is
    // visible to the user.
    const hint = document.querySelector(".pane-chooser-keys")!.textContent!;
    expect(hint).toContain("a–z types a path");
    expect(hint).toContain("↑↓←→ move");
    expect(hint).not.toContain("hjkl");

    fireEvent.keyDown(window, { key: "Enter" });
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeNull());
    expect(await screen.findByText("copied 1 item")).toBeInTheDocument();
    // It arrived there AND stayed here: a move would satisfy the first assertion alone.
    expect(await within(tiles()[1]).findByText("file-060.ts")).toBeInTheDocument();
    expect(within(tiles()[0]).getByText("file-060.ts")).toBeInTheDocument();
    // And the user did not travel with the files. Every other commit of this mode moves
    // the focus, so "it stayed" is a decision, not an omission.
    expect(tiles()[0].classList.contains("focused")).toBe(true);
  });

  it("moves on F6, in one request and with nothing left behind", async () => {
    await twoFolders("subdir-08");
    fireEvent.click(within(tiles()[0]).getByText("file-061.tsx").closest("tr")!);

    // The wire is the only place copy and move are truly distinguishable: a regression to
    // copy-then-delete leaves the same tree behind and passes every DOM assertion below.
    const calls: string[] = [];
    const inner = globalThis.fetch;
    globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      calls.push(`${init?.method ?? "GET"} ${path.split("?")[0]}`);
      return inner(input, init);
    };
    onTestFinished(() => {
      globalThis.fetch = inner;
    });

    expect(pressDirect("F6")).toBe(true);
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    expect(document.querySelector(".pane-chooser-title")?.textContent).toBe('Move "file-061.tsx" to…');
    fireEvent.keyDown(window, { key: "Enter" });

    expect(await within(tiles()[1]).findByText("file-061.tsx")).toBeInTheDocument();
    await waitFor(() => expect(within(tiles()[0]).queryByText("file-061.tsx")).toBeNull());
    expect(calls.filter((c) => c === "POST /.cornus/web/fs/move")).toHaveLength(1);
    expect(calls.filter((c) => c === "POST /.cornus/web/fs/copy")).toHaveLength(0);
    expect(calls.filter((c) => c.startsWith("DELETE"))).toHaveLength(0);
  });

  it("owns F5 even when it cannot run, rather than letting it reload the page", async () => {
    renderView(Workspace);
    await openScratch();
    // Nothing selected. The command is still REGISTERED — that is the point — so the key is
    // claimed and quietly does nothing. Falling through instead would make one key mean
    // "copy" or "throw away every unsaved draft in the workspace" depending on a selection
    // the user is not looking at.
    expect(allCommands().find((c) => c.id === "files:copy")?.disabled).toBe("nothing selected");
    expect(pressDirect("F5")).toBe(true);
    expect(document.querySelector(".pane-chooser")).toBeNull();

    // At the virtual root it is not registered at all, and the browser gets its key back:
    // the entries there are mounts, so there is nothing a copy could be about.
    fireEvent.click(await screen.findByText("All"));
    await waitFor(() => expect(allCommands().some((c) => c.id === "files:copy")).toBe(false));
    expect(pressDirect("F5")).toBe(false);
  });

  it("also reaches the transfers after the prefix, and shows both keys for each", async () => {
    await twoFolders("subdir-09");
    fireEvent.click(within(tiles()[0]).getByText("file-062.go").closest("tr")!);

    expect(pressBind("C", true, { ctrlKey: true })).toBe("swallow");
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeNull());
    expect(pressBind("M", true, { ctrlKey: true })).toBe("swallow");
    await waitFor(() =>
      expect(document.querySelector(".pane-chooser-title")?.textContent).toMatch(/^Move /),
    );
    fireEvent.keyDown(window, { key: "Escape" });

    // Both spellings are ADVERTISED, and the bare one is marked as bare. A palette that
    // showed only the chord would leave F5 undiscoverable; one that showed them alike would
    // teach "prefix then F5", which is the one thing that does not work.
    await openPaneMenu();
    fireEvent.input(document.querySelector(".cmd-filter")!, { target: { value: "another pane" } });
    const row = (await screen.findByText(/^Copy .* to another pane…$/, { selector: ".cmd-item-title" }))
      .closest("button")!;
    const caps = Array.from(row.querySelectorAll("kbd"));
    expect(caps.map((c) => c.textContent)).toEqual(["Ctrl+Shift+C", "F5"]);
    expect(caps[0].classList.contains("direct")).toBe(false);
    expect(caps[1].classList.contains("direct")).toBe(true);
    setPaletteOpen(false); // the palette is a module singleton; leaving it up leaks
  });

  it("splits a tile via an edge overlay into two explorer tiles", async () => {
    renderView(Workspace);
    await screen.findByText("shop-web"); // the initial pane's root listing rendered
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    await splitViaEdge("right");
    // A horizontal split with two independent tiles, each browsing the namespace.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();
    expect(await screen.findAllByText("shop-web")).toHaveLength(2);
  });

  it("closes a split tile via its tab, collapsing back to one", async () => {
    renderView(Workspace);
    await screen.findByText("shop-web");
    await splitViaEdge("bottom");
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    fireEvent.click(document.querySelectorAll<HTMLElement>(".tab-close")[0]);
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  // The Terminal workspace's split binds work here too: prefix % splits left/right,
  // prefix " splits top/bottom. Driven through handlePrefixKey so the assertion is
  // about the KEY, not about a command object being present in the registry.
  it("splits a tile with the Terminal's own prefix binds (% and \")", async () => {
    setPrefixEnabled(true);
    setPrefix("Ctrl+Shift+X");
    renderView(Workspace);
    await screen.findByText("shop-web");
    expect(document.querySelectorAll(".stack")).toHaveLength(1);

    expect(pressBind("%")).toBe("swallow");
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();

    expect(pressBind('"')).toBe("swallow");
    expect(document.querySelectorAll(".stack")).toHaveLength(3);
    expect(document.querySelector(".split.v")).toBeTruthy();
  });

  // The split binds are workspace-level, so they must survive the states where the
  // per-pane file commands vanish — at the virtual root nothing is inside a mount and
  // fileCommands' contextual half is empty.
  it("offers the split binds at the virtual root, where no file actions apply", async () => {
    renderView(Workspace);
    await screen.findByText("shop-web");
    expect(allCommands().some((c) => c.id === "files:new-folder")).toBe(false);
    expect(pressBind("%")).toBe("swallow");
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  // The pane binds are the Terminal's, so `c` and `x` are New pane / Close tab here too and
  // Delete moved to vi's `d`. This is the reverse of what this screen did before, and the
  // invariant that survived the reversal is UNIQUENESS: handlePrefixKey dispatches to the
  // FIRST command with a matching bind, so a duplicate does not error — it silently makes
  // the loser unreachable, on a screen whose command set changes with the selection.
  // Checking identities alone would miss that.
  it("binds every key to exactly one command, with the pane keys winning c and x", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    const row = (await screen.findByText("README.md")).closest("tr")!;
    fireEvent.click(row); // select without opening — the richest command set

    // Flattened through bindsOf, which is the only reason uniqueness still means anything:
    // a command may now name SEVERAL spellings, and reading `c.bind` raw would compare a
    // whole array against a string and find every array distinct from everything.
    const binds = allCommands().flatMap(bindsOf);
    expect(new Set(binds).size).toBe(binds.length);
    const bind = (k: string) => allCommands().find((c) => bindsOf(c).includes(k));
    expect(bind("c")?.id).toBe("ws:new");
    expect(bind("x")?.id).toBe("ws:close");
    expect(bind("C")?.id).toBe("ws:split");
    expect(bind("%")?.id).toBe("ws:split-h");
    expect(bind('"')?.id).toBe("ws:split-v");
    // The two untagged pane keys are this screen's too, on the richest command set there is
    // — which is where a future file action would collide with one of them.
    expect(bind("o")?.id).toBe("ws:next");
    expect(bind(";")?.id).toBe("ws:last");
    expect(bind("d")?.id).toBe("files:delete");
    // The transfers, on the chord the app can spell and nothing else can want.
    expect(bind("Ctrl+Shift+C")?.id).toBe("files:copy");
    expect(bind("Ctrl+Shift+M")?.id).toBe("files:move");
    // `y` was Copy until the two transfers absorbed it. Asserted FREE rather than left
    // unmentioned: an unbound letter is a thing a later command may take, and the day one
    // does, this line is what says the old habit is now somebody else's key.
    expect(bind("y")).toBeUndefined();

    // The UNPREFIXED keys are a separate namespace and need their own uniqueness check —
    // F5 does not collide with a prefixed bind, so folding the two lists together would
    // both under- and over-constrain. What matters here is that nothing else on the screen
    // has quietly claimed the same bare key.
    const directs = allCommands().flatMap(directsOf);
    expect(new Set(directs).size).toBe(directs.length);
    const direct = (k: string) => allCommands().find((c) => directsOf(c).includes(k));
    expect(direct("F5")?.id).toBe("files:copy");
    expect(direct("F6")?.id).toBe("files:move");
    // ONE spelling of the new-tab key is registered, the one this platform uses. Both
    // halves matter: the wrong-platform spelling must be ABSENT, or the palette advertises
    // a key that does nothing on the machine reading it. jsdom is not a Mac, so this pins
    // the non-Mac branch; the branch itself is isMacPlatform's, tested in termKeys.test.ts.
    expect(direct("Ctrl+Enter")?.id).toBe("files:open");
    expect(direct("Meta+Enter")).toBeUndefined();

    // The richest BROWSE set is still not every set: `files:save` is registered only while
    // an editor holds unsaved changes, so nothing above could see the clash that cost Save
    // its letter to the pane chooser's `s`. That state is also where a duplicate would bite
    // hardest — `s` would have meant "choose a pane" until the buffer went dirty and "save"
    // thereafter, a bind that changes meaning under you rather than one merely taken.
    const view = await openEditor("README.md");
    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    await waitFor(() => expect(allCommands().some((c) => c.id === "files:save")).toBe(true));
    const editBinds = allCommands()
      .map((c) => c.bind)
      .filter((b): b is string => b !== undefined);
    expect(new Set(editBinds).size).toBe(editBinds.length);
    expect(bind("s")?.id).toBe("ws:choose");
    // Save took NO replacement key, and that is the assertion: it is in the palette and
    // nowhere on the prefix. A prefix bind is for something the focused pane cannot hear,
    // and the editor hears this one — so the palette entry carries no accelerator at all.
    expect(allCommands().find((c) => c.id === "files:save")?.bind).toBeUndefined();

    // Which is only defensible because the editor's own keymap runs it. Driven as a real
    // keystroke on the editor, not through the prefix path: an assertion that Save has no
    // bind, on its own, is equally true of a Save nobody can reach by key.
    expect(document.querySelector(".file-pane-editor-bar .badge")?.textContent).toBe("unsaved");
    fireEvent.keyDown(document.querySelector(".cm-content")!, { key: "s", ctrlKey: true });
    await waitFor(() => expect(document.querySelector(".file-pane-editor-bar .badge")).toBeNull());
  });

  // This replaces a comparison between two SCREENS: Files and Terminal each registered the
  // same ten pane commands, and a test held the two lists letter for letter. One screen
  // means one list, so that comparison is gone — but what it protected is not. The tile's
  // ⋮ is the palette filtered to `:pane`, and it has to be the SAME MENU on every tile,
  // whatever that tile holds. Compared pane-kind against pane-kind for the same reason it
  // used to be compared screen against screen: a list written here would be a third place
  // to update. Asserted concrete as well, so two menus that lost their entries together
  // cannot pass by matching at nothing.
  it("shows the same tile menu whichever kind of pane is focused", async () => {
    // Keyed by ID rather than title, because one entry is SUPPOSED to differ in wording:
    // "Open in a terminal…" names the folder it would land in when there is one. Same
    // commands under the same keys is the invariant the ⋮ needs; that row's wording is a
    // property of the focused pane, asserted where it can be asserted for what it is
    // ("names the tile its ⋮ was pressed on", below).
    const paneMenu = () =>
      Object.fromEntries(
        allCommands()
          .filter((c) => c.tags?.includes(PANE_TAG))
          .map((c) => [c.id, c.bind ?? null]),
      );

    renderTerm();
    await screen.findAllByRole("combobox");
    const onTerminal = paneMenu();
    cleanup(); // unmount so its provider deregisters before the next render registers
    // …and drop the seeded layout, or the second render restores the terminal pane the
    // first one was given and this compares a screen with itself.
    globalThis.localStorage?.clear();

    renderView(Workspace);
    await screen.findByText("project");
    expect(paneMenu()).toEqual(onTerminal);
    expect(onTerminal).toEqual({
      "ws:split": "C",
      "ws:new": "c",
      "ws:open-terminal": "t",
      "ws:move": null, // listed always, bound on neither
      "ws:choose": "s",
      "ws:rotate": "Ctrl+O",
      "ws:close": "x",
    });
    // Their titles are pinned where the user meets them — the ⋮ test asserts the rendered
    // rows word for word — so this one does not hold a second copy of them.
  });
});

describe("Workspace (terminals)", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  it("starts with one empty pane and splits it via an edge overlay", async () => {
    renderTerm();
    // One empty pane, so one target picker (combobox). The screen carries no title or
    // hint of its own — the header nav names it.
    expect(await screen.findAllByRole("combobox")).toHaveLength(1);
    expect(document.querySelector(".workspace-toolbar")).toBeNull();
    // Each pane offers four edge-split overlays; resting on one and clicking it yields a
    // second (empty) pane with its own picker.
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  // REGRESSION: the overlays used to be live from the first frame the pointer grazed an
  // edge — invisible, but hit-testable — so they swallowed clicks aimed at whatever sat
  // under them, and flashed on every pass-by. A zone must stay click-through until the
  // pointer has RESTED on its edge for the full dwell.
  it("ignores an edge overlay until the pointer has dwelled on that edge", async () => {
    renderTerm();
    const rightEdge = await screen.findByRole("button", { name: "Split pane, new pane on the right" });

    // Untouched: click-through (the browser hands the click to the content below), and the
    // handler refuses it too, so nothing splits.
    expect(rightEdge.style.pointerEvents).toBe("none");
    expect(rightEdge.classList.contains("armed")).toBe(false);
    fireEvent.click(rightEdge);
    expect(screen.getAllByRole("combobox")).toHaveLength(1);

    // Passing THROUGH the band is not dwelling: the pointer reaches the edge, then moves on
    // to the middle before the delay elapses. The overlay must never arm.
    const tile = tileOf(rightEdge);
    vi.useFakeTimers();
    try {
      movePointer(tile, EDGE_POINT.right);
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS - 50);
      movePointer(tile, CENTRE);
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS * 2);
    } finally {
      vi.useRealTimers();
    }
    expect(rightEdge.classList.contains("armed")).toBe(false);
    expect(rightEdge.style.pointerEvents).toBe("none");
    fireEvent.click(rightEdge);
    expect(screen.getAllByRole("combobox")).toHaveLength(1);

    // Rest on the edge for the whole dwell and it arms: visible, hit-testable, splits. The
    // .armed class is what reveals the bar — a :hover rule would not, the pointer is still.
    dwellOnEdge(rightEdge, "right");
    expect(rightEdge.classList.contains("armed")).toBe(true);
    expect(rightEdge.style.pointerEvents).toBe("auto");
    fireEvent.click(rightEdge);
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  // Only the edge under the pointer arms. Arming all four at once would put three live
  // strips on edges the pointer never visited — the original bug, on three sides.
  it("arms only the edge the pointer rested on, and re-arms on the next edge", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const zone = (side: Side) => document.querySelector<HTMLElement>(`.pane-split-zone.edge-${side}`)!;
    const armedSides = () =>
      (["top", "bottom", "left", "right"] as Side[]).filter((s) => zone(s).classList.contains("armed"));

    dwellOnEdge(zone("left"), "left");
    expect(armedSides()).toEqual(["left"]);
    expect(zone("right").style.pointerEvents).toBe("none");

    // Crossing to another edge restarts the dwell there: the old zone goes dark at once, and
    // the new one is not live until it has earned its own delay.
    const tile = tileOf(zone("left"));
    vi.useFakeTimers();
    try {
      movePointer(tile, EDGE_POINT.bottom);
      expect(armedSides()).toEqual([]);
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS + 1);
    } finally {
      vi.useRealTimers();
    }
    expect(armedSides()).toEqual(["bottom"]);

    // Back into the interior and everything goes click-through again.
    movePointer(tile, CENTRE);
    expect(armedSides()).toEqual([]);
  });

  // The strips reach past the tile, over the workspace gutter: aiming at a pane border and
  // landing in the gap in front of it was dead space. Nothing is drawn out there and no
  // button covers those pixels — the tile arms from them and completes the click itself.
  // The reach stops at anything owning its own pixels: the divider between two tiles is a
  // 6px drag handle, and a strip live in front of it is a strip in front of the handle.
  it("reaches into the gutter outside the tile, but never over the resize divider", async () => {
    renderTerm();
    const leftEdge = await screen.findByRole("button", { name: "Split pane, new pane on the left" });
    tileOf(leftEdge); // give the tile a rect to be measured against
    const workspace = document.querySelector<HTMLElement>(".workspace")!;
    const inTheGutter = { clientX: -5, clientY: 100 }; // 5px outside the tile's left edge

    vi.useFakeTimers();
    try {
      movePointer(workspace, inTheGutter);
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS + 1);
    } finally {
      vi.useRealTimers();
    }
    expect(leftEdge.classList.contains("armed")).toBe(true);

    // And a click out there splits, though the strip's own box ends at the tile border.
    workspace.dispatchEvent(new MouseEvent("click", { ...inTheGutter, bubbles: true }));
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    // The divider owns the gap between the two tiles: resting on it arms nothing. Both
    // tiles need a rect of their own here — the split replaced the one stubbed above — or
    // the assertion passes because neither tile is measurable, not because of the rule.
    const [a, b] = document.querySelectorAll<HTMLElement>(".stack");
    a.getBoundingClientRect = () => TILE_RECT; // 0..200
    b.getBoundingClientRect = () => ({ ...TILE_RECT, left: 206, right: 406, x: 206 }) as DOMRect;
    const divider = document.querySelector<HTMLElement>(".divider")!;
    // x=203 is inside the 6px gap: within the outward reach of BOTH tiles, and it must
    // still arm neither, because the drag handle is what lives there.
    vi.useFakeTimers();
    try {
      movePointer(divider, { clientX: 203, clientY: 100 });
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS * 2);
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".pane-split-zone.armed")).toHaveLength(0);
  });

  // The top strip deliberately hugs the TILE's top edge, which puts it over the tab bar:
  // the split it previews divides the whole tile. That placement is only safe because the
  // strip is 10px and click-through until armed — so the live region must never grow past
  // the strip onto the tabs. A dwell 15px down is on a tab, not on the overlay.
  it("hugs the tile's top edge without arming over the tab bar", async () => {
    renderTerm();
    const topEdge = await screen.findByRole("button", { name: "Split pane, new pane on the top" });
    // Anchored to the tile, not the pane body — the body starts below the tab bar.
    expect(topEdge.closest(".stack")).toBeTruthy();
    expect(topEdge.closest(".stack-body")).toBeNull();
    expect(topEdge.style.height).toBe("10px");

    const tile = tileOf(topEdge);
    vi.useFakeTimers();
    try {
      movePointer(tile, ON_THE_TABS);
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS * 2);
    } finally {
      vi.useRealTimers();
    }
    expect(topEdge.classList.contains("armed")).toBe(false);
    expect(topEdge.style.pointerEvents).toBe("none");

    // 3px down IS the strip, and resting there arms it.
    dwellOnEdge(topEdge, "top");
    expect(topEdge.classList.contains("armed")).toBe(true);
    fireEvent.click(topEdge);
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  // The hotzone used to run the FULL edge while the purple bar showed only its middle 70%,
  // so a third of what was live was invisible — and the corners, where two strips met, were
  // the worst of it: a press aimed at the content there split the tile instead. The strip is
  // now the bar.
  it("keeps the split hotzone inside the bar it draws", async () => {
    renderTerm();
    const topEdge = await screen.findByRole("button", { name: "Split pane, new pane on the top" });
    const side = await screen.findByRole("button", { name: "Split pane, new pane on the left" });
    // Drawn inset from both ends of its edge — and the numbers are INLINE, from the same
    // constant edgeAt() arms on, which is what stops the two drifting apart.
    expect(topEdge.style.left).toBe("15%");
    expect(topEdge.style.right).toBe("15%");
    expect(side.style.top).toBe("15%");
    expect(side.style.bottom).toBe("15%");

    // Out towards the end of the top edge, where no bar is drawn: a full dwell arms nothing.
    // x=25 on a 200px tile, deliberately — past the left strip's own 20px thickness (nearer
    // the corner it would simply be the LEFT band, and a test that armed nothing there would
    // prove nothing about the top one) and inside the 15% this strip no longer covers.
    const tile = tileOf(topEdge);
    vi.useFakeTimers();
    try {
      movePointer(tile, { clientX: 25, clientY: 3 });
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS * 2);
    } finally {
      vi.useRealTimers();
    }
    expect(topEdge.classList.contains("armed")).toBe(false);
    expect(document.querySelector(".pane-split-zone.armed")).toBeNull(); // nor any other
    // The middle of that same edge, 3px down, is the bar — and it arms.
    dwellOnEdge(topEdge, "top");
    expect(topEdge.classList.contains("armed")).toBe(true);
  });

  // The regression that made the two gestures collide. A tab drag was HTML5 drag-and-drop,
  // which emits no pointer events at all; it is a pointer drag now, so a tab carried near an
  // edge feeds the very dwell that arms the split strips — and the release then lands on an
  // armed bar and splits the tile it has just been dropped into.
  it("stands the edge dwell down while a tab is being dragged", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    const target = tiles()[1];
    const strip = target.querySelector<HTMLElement>(".pane-split-zone.edge-right")!;
    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[0]);
    drag.over(1, "right");
    // Resting on the target tile's own right edge, for as long as the dwell asks. tabDrag
    // has already given the tiles their rects, so this is the same geometry the drag sees.
    vi.useFakeTimers();
    try {
      target.dispatchEvent(pointerEvent("pointermove", { clientX: 397, clientY: 100 }));
      vi.advanceTimersByTime(SPLIT_ARM_DELAY_MS * 2);
    } finally {
      vi.useRealTimers();
    }
    expect(strip.classList.contains("armed")).toBe(false);
    drag.drop();
    // The drop re-tiled, and nothing split: two tiles, not three.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("splits via the left edge too (new pane placed before the original)", async () => {
    renderTerm();
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    await splitViaEdge("left");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
  });

  it("stacks two panes into tabs when a tab is dragged onto another tile's center", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
    // Two separate tiles, one tab each.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);

    // Aim at the real CENTRE of a real rect. Dropping without either would also stack —
    // zoneAt() returns "stack" for a zero rect — but then the test would prove nothing
    // about the centre, and would pass just as green for a drop aimed at an edge.
    dropTabOn(document.querySelectorAll<HTMLElement>(".tab")[0], 1, "centre");

    // Collapsed to a single tile carrying both panes as tabs.
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("moves (re-tiles) a pane when a tab is dropped on another tile's edge", async () => {
    renderTerm();
    await splitViaEdge("right"); // now a horizontal split of two tiles
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();

    // Drop near the target tile's bottom edge → "bottom" → move.
    dropTabOn(document.querySelectorAll<HTMLElement>(".tab")[0], 1, "bottom");

    // Re-tiled: the split is now vertical, still two separate tiles.
    expect(document.querySelector(".split.v")).toBeTruthy();
    expect(document.querySelector(".split.h")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("drags a whole tile by its tab bar onto another, merging all its tabs", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    // Drag the first tile's tab bar (the whole stack) onto the second tile's center — a
    // measured centre, so "merged" answers the aim and not a degenerate rect.
    dropTabOn(document.querySelectorAll<HTMLElement>(".stack-tabs")[0], 1, "centre");
    // Both tiles collapsed into one carrying both panes as tabs.
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("focuses the new pane's workload picker when a pane is created", async () => {
    renderTerm();
    await splitViaEdge("bottom");
    expect(screen.getAllByRole("combobox")).toHaveLength(2);
    // Focus lands on the workload picker of the freshly-created tile (marked .focused).
    const active = document.activeElement as HTMLElement;
    expect(active.tagName).toBe("SELECT");
    expect(active.closest(".stack")?.classList.contains("focused")).toBe(true);
  });

  it("splits, then closes a tab, collapsing back to one tile", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    fireEvent.click(document.querySelectorAll<HTMLElement>(".tab-close")[0]);
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    // An empty pane runs nothing, so it closes on the click itself — no question, and no
    // wait for one. That is the baseline the live-session prompt below is measured from.
    noDialog();
  });

  // Closing a terminal pane KILLS its session (closePaneById sends the DELETE), so the
  // running program is gone and reopening a pane cannot bring it back. The ✕ therefore
  // asks first, and a Cancel must leave both the pane and the session exactly as they were.
  it("asks before closing a pane whose session is still running", async () => {
    await connectPane();
    closeTab("shop-web");
    expect(await dialog()).toHaveTextContent("Close this terminal?");
    // Nothing has happened yet: the tile is still there and the session still alive.
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    expect(liveTerminals()).toHaveLength(1);

    await answer("Cancel");
    expect(liveTerminals()).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(1);

    // Confirming is what ends it — and the session is killed, not merely un-rendered.
    closeTab("shop-web");
    await answer("Close & end session");
    await waitFor(() => expect(liveTerminals()).toHaveLength(0));
  });

  // The gate keys on what the BFF says about the session, not merely on the pane holding
  // an id: a session whose shell has exited stays listed (alive:false) for 30s, and a pane
  // left pointing at one has nothing to lose. Both panes here are restored from a
  // persisted layout, so both hold a session id from the first frame; only the live one
  // may ask. The "needs you" badge is the proof that the polled list has actually landed —
  // without waiting for it the assertion would pass for the wrong reason, since a list
  // that has not answered yet reads as "not known dead" and asks about everything.
  it("asks only about the pane whose session is still alive, not one already ended", async () => {
    seedTerminals([
      { id: "term-live", workload: "shop-web", alive: true, state: "blocked" },
      { id: "term-ended", workload: "shop-db", alive: false },
    ]);
    renderTerm([
      { kind: "term", workload: "shop-web", cmd: ["/bin/sh"], sessionId: "term-live" },
      { kind: "term", workload: "shop-db", cmd: ["/bin/sh"], sessionId: "term-ended" },
    ]);
    await screen.findByText("needs you"); // the session poll has answered

    // The ended one is bookkeeping: it closes on the click, no question asked.
    closeTab("shop-db");
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    noDialog();

    // The live one still asks.
    closeTab("shop-web");
    expect(await dialog()).toHaveTextContent("Close this terminal?");
    await answer("Cancel");
  });

  // The keyboard close (prefix x) is the same door and must have the same lock: it used to
  // reach closePaneById directly, which would have left the bind killing sessions silently.
  it("asks on the prefix-x close bind too, not just the tab ✕", async () => {
    setPrefixEnabled(true);
    setPrefix("Ctrl+Shift+X");
    await connectPane();

    disarm();
    handlePrefixKey(keyEvent({ key: "X", ctrlKey: true, shiftKey: true })); // the prefix
    expect(handlePrefixKey(keyEvent({ key: "x" }))).toBe("swallow");
    expect(await dialog()).toHaveTextContent("Close this terminal?");
    expect(liveTerminals()).toHaveLength(1);

    await answer("Close & end session");
    await waitFor(() => expect(liveTerminals()).toHaveLength(0));
  });

  // "New pane…" asks WHERE by lighting the workspace up — the same wireframe targets a
  // move offers — rather than with a modal naming dispositions. Tapping the centre is the
  // old prompt's "Stack as tab".
  it("New pane asks with the wireframe targets, and stacks where the centre is tapped", async () => {
    renderTerm();
    await screen.findAllByRole("combobox"); // the first pane mounted
    expect(document.querySelectorAll(".stack")).toHaveLength(1);

    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    // Three legs, and each rules out a different way of passing while broken. No dialog:
    // the old modal would still be reachable and this would be a rename, not a change.
    // Still one tab: a command that CREATED the pane and asked afterwards would also show
    // an overlay. Five zones: the question has to be the whole plus, not a stack button.
    expect(modalRequest()).toBeNull();
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    expect(pickZones()).toHaveLength(5);

    tapPickZone("stack");
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(document.querySelectorAll(".stack")).toHaveLength(1); // one tile, two tabs
    expect(document.querySelector(".pane-pick-overlay")).toBeNull(); // and the mode closed
  });

  // Pulling a tab out of its OWN tile is the way to un-stack: the tile has two tabs and
  // one of them should become a tile beside it. The gesture starts with a press, which
  // ACTIVATES the tab — so the tile's active pane is the dragged one, and the drop ids
  // must not collapse to "dropped on itself" (that swallowed the whole gesture).
  async function stackTwoTabs() {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    tapPickZone("stack");
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(document.querySelectorAll(".stack")).toHaveLength(1); // one tile, two tabs
  }
  it("splits a stacked tile by dragging one of its own tabs onto its edge", async () => {
    await stackTwoTabs();
    // The RIGHT edge, deliberately: it is the zone a degenerate rect cannot fake — that
    // fallback is "stack" — so a horizontal split answers the aim rather than an accident.
    dropTabOn(document.querySelectorAll<HTMLElement>(".tab")[1], 0, "right");

    // The tab left the stack and became a tile of its own, beside the original.
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(document.querySelector(".split.h")).toBeTruthy();
    expect(document.querySelectorAll(".tab")).toHaveLength(2); // one per tile
  });

  it("previews the edge drop while a tab is dragged over its own tile", async () => {
    await stackTwoTabs();
    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[1]);

    drag.over(0, "right");
    expect(document.querySelector(".pane-drop-indicator.zone-right")).toBeTruthy();
    // Its own centre is where it already lives: no indicator, nothing promised.
    drag.over(0, "centre");
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    drag.drop();
  });

  it("takes the preview away when the drag leaves every tile", async () => {
    renderTerm();
    await splitViaEdge("right");
    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[0]);
    drag.over(1, "right");
    expect(document.querySelector(".pane-drop-indicator")).toBeTruthy();
    // Out past both tiles — the gutter, or off the workspace entirely. A promise the
    // release will not keep must not be left standing under the pointer.
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 800, clientY: 800 }));
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    drag.drop();
    // And releasing out there rearranges nothing.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("leaves a solo tile alone when its only tab is dropped on its own edge", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    // The drag really runs — the tab is picked up, and the tile lights nothing up, because
    // a lone pane has nowhere to go beside itself.
    const drag = tabDrag(document.querySelector<HTMLElement>(".tab")!);
    drag.over(0, "right");
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    drag.drop();
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelector(".split")).toBeNull();
  });

  // An edge target splits, and the SIDE is the half the old prompt could not name — it
  // read a preference instead. Both cases produce one `.split.h` and two tiles, so the
  // only thing that tells them apart is which child the new (focused) pane became. Two
  // empty terminal panes are otherwise indistinguishable.
  it.each([
    ["left", 0],
    ["right", 1],
  ] as const)("New pane splits to the %s when that edge is tapped", async (zone, newIndex) => {
    renderTerm();
    await screen.findAllByRole("combobox"); // the first pane mounted
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    tapPickZone(zone);
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(document.querySelectorAll(".split.h")).toHaveLength(1);
    expect(document.querySelector(".split.v")).toBeNull();
    expect(tiles()[newIndex].classList.contains("focused")).toBe(true);
    expect(tiles()[1 - newIndex].classList.contains("focused")).toBe(false);
  });

  // Escape is the way out of a mode, and a placement that leaked its armed state would be
  // invisible until the NEXT gesture answered for it — so the test performs one.
  it("creates nothing when the placement is cancelled", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeNull());
    expect(document.querySelector(".pane-pick-scrim")).toBeNull();
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    expect(document.querySelectorAll(".stack")).toHaveLength(1);

    // Still usable afterwards, and still asking: a stale intent would have made this tap
    // land without a question, or not at all.
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    tapPickZone("bottom");
    await waitFor(() => expect(document.querySelectorAll(".split.v")).toHaveLength(1));
  });
});

// Files and Terminal used to be two screens with two layouts; they are one screen with one
// tree now, and everything below is about what only that arrangement can get wrong — a
// pane's KIND. The two suites above still cover each kind on its own.
describe("Workspace (one tree, two kinds of pane)", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    clearToasts();
  });

  const cmdRow = (id: string) => allCommands().find((c) => c.id === id);

  // The merge had to pick what the screen opens as, and it opens as the file browser. Both
  // halves are asserted: the browser IS there (a bare "no terminal" would pass against a
  // screen that rendered nothing at all) and no session was opened behind it — which is the
  // thing the old Terminal screen did on every load, and the reason "which screen am I on"
  // used to cost a container exec.
  it("opens as a single file browser, with no terminal and no session", async () => {
    renderView(Workspace);
    expect(await screen.findByText("project")).toBeInTheDocument();
    expect(document.querySelectorAll(".tab")).toHaveLength(1);
    expect(document.querySelector(".file-pane")).toBeTruthy();
    expect(document.querySelector(".pane-picker")).toBeNull();
    expect(document.querySelector(".xterm")).toBeNull();
    expect(terminalCreates()).toEqual([]);
  });

  // The point of the whole feature: the terminal lands on the workload AND in the directory
  // the browser was showing. The observation point is the CREATE REQUEST, and it has to be:
  // the mock cannot honour a working directory (nothing in jsdom has one) and the session
  // list never echoes it back, so anything read off the pane would be equally true of a
  // terminal that started at the image's default.
  it("opens a terminal on the workload and directory being browsed", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("shop-web"));
    fireEvent.click(await screen.findByText("etc"));
    await screen.findByText("nginx");

    // The row says where it is about to land BEFORE it is pressed — a command whose title
    // named one folder and whose terminal opened in another would be worse than no title.
    await waitFor(() => expect(cmdRow("ws:open-terminal")?.title).toBe('Open "etc" in a terminal…'));
    expect(cmdRow("ws:open-terminal")?.disabled).toBeUndefined();

    runCommand("ws:open-terminal");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowRight" });

    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
    expect(terminalCreates().at(-1)).toMatchObject({ workload: "shop-web", dir: "/etc" });
    // And it did not replace the browser it was opened from: two tiles, one of each.
    expect(tiles()).toHaveLength(2);
    expect(document.querySelector(".file-pane")).toBeTruthy();
  });

  // Three reasons, three states, and the command is LISTED in all of them. Each assertion
  // names the row first and its reason second, because "absent" would satisfy a test that
  // only checked the reason was not undefined — and an absent row reads as "this screen
  // cannot open terminals" rather than "not from here".
  it("says why a terminal cannot be opened, rather than dropping the command", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    await waitFor(() => expect(cmdRow("ws:open-terminal")).toBeTruthy());
    expect(cmdRow("ws:open-terminal")?.title).toBe("Open in a terminal…"); // no folder to name
    expect(cmdRow("ws:open-terminal")?.disabled).toBe("pick a workload first");

    // Inside a LOCAL root there is a folder, and still no container to attach to.
    fireEvent.click(screen.getByText("project"));
    await screen.findByText("compose.yaml");
    await waitFor(() =>
      expect(cmdRow("ws:open-terminal")?.disabled).toBe("a local folder, not a container"),
    );

    // A STOPPED workload is a different answer and names itself. Reached by SEEDING a pane
    // already inside it, because the command aims at the folder being SHOWN and the listing
    // refuses to enter a stopped workload — so the only ways in are a restored layout and a
    // workload that stopped while it was being browsed. Both are real, and both are exactly
    // the moment this reason has to be right.
    cleanup();
    globalThis.localStorage?.clear();
    seedPanes([{ kind: "files", path: "legacy-cron" }]);
    renderView(Workspace);
    await waitFor(() => expect(cmdRow("ws:open-terminal")?.disabled).toBe("legacy-cron is not running"));
  });

  // The command aims at the folder the pane is SHOWING, and a selected row does not move it.
  // Both halves are asserted from ONE state — a listing whose selection is a directory OTHER
  // than the one on screen — because "it used the shown folder" and "it ignored the
  // selection" are the same claim only when the two disagree. The observation is again the
  // create request: a pane that appeared proves nothing about where its shell started.
  it("aims at the folder on screen, not the directory selected in it", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("shop-web"));
    fireEvent.click(await screen.findByText("etc"));
    await screen.findByText("nginx");

    // The selection has to be LIVE for the ignoring to mean anything, and the witness is a
    // command that does read it: `Open …` names the picked-out folder in the same breath
    // that `Open in a terminal…` declines to.
    fireEvent.click((await screen.findByText("nginx")).closest("tr")!);
    await waitFor(() => expect(cmdRow("files:open")?.title).toBe('Open "nginx/"…'));

    expect(cmdRow("ws:open-terminal")?.title).toBe('Open "etc" in a terminal…');
    runCommand("ws:open-terminal");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowRight" });

    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
    expect(terminalCreates().at(-1)).toMatchObject({ workload: "shop-web", dir: "/etc" });
  });

  // A transfer needs somewhere for files to land, and a terminal is not that. It is refused
  // BY KIND and says so: routed through the file pane's "no path" branch it would have read
  // "the mount list", which is a different thing and not true of this pane.
  it("refuses a terminal as a transfer destination, naming what it is", async () => {
    seedPanes([
      { kind: "files", path: "project/many-files" },
      { kind: "term", workload: "shop-web", cmd: ["/bin/sh"] },
    ]);
    renderView(Workspace);
    await screen.findByText("subdir-00");
    fireEvent.click(within(tiles()[0]).getByText("file-062.go").closest("tr")!);

    expect(pressDirect("F5")).toBe(true);
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    const rows = Array.from(
      document.querySelectorAll<HTMLElement>(".pane-chooser-item:not(.pane-chooser-other)"),
    );
    expect(rows).toHaveLength(2);
    expect(rows[1].classList.contains("disabled")).toBe(true);
    expect(within(rows[1]).getByText("a terminal")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
  });

  // One tree holding both kinds has to survive a reload, and the validator is where that is
  // decided: loadLayout rejects a tree if ANY pane fails it, so a predicate that knew only
  // one kind would silently throw the whole layout away and hand back the default. Which is
  // why the assertion is on the TERMINAL specifically — the default layout is a file pane
  // at the root, so "a file pane came back" is exactly what a total loss looks like.
  it("restores a layout holding a file pane and a terminal together", async () => {
    seedPanes([
      { kind: "files", path: "project" },
      { kind: "term", workload: "shop-web", cmd: ["/bin/ash"] },
    ]);
    renderView(Workspace);
    await waitFor(() => expect(tabNames()).toEqual(["project", "shop-web  /bin/ash"]));
    cleanup();

    renderView(Workspace);
    await waitFor(() => expect(tabNames()).toEqual(["project", "shop-web  /bin/ash"]));
  });

  // A tab is asked "what is in you NOW", and the launch argv cannot answer it: it is fixed
  // when the session is created, so a shell that has had vim open for an hour still reads
  // "/bin/ash". The window title the session's output sets does track it (see osctitle.go),
  // so the tab prefers it — and must go on preferring it as the title CHANGES, which is the
  // half a seeded-from-the-start assertion would miss entirely.
  it("names a terminal tab after the window title its session set, tracking it as it changes", async () => {
    seedPanes([{ kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" }]);
    seedTerminals([{ id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true }]);
    renderView(Workspace);

    // No title yet: the argv is what a session that never sets one is left with, and
    // asserting it here is what proves the later change is the title's doing.
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  /bin/ash"]));

    setTerminalTitle("term-live", "vim README.md");
    // Longer than the 2s session poll — the label cannot change before the list is asked
    // again, and the 1s default would fail for that reason rather than for a real one.
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  vim README.md"]), { timeout: 5000 });

    // Leaving the program hands the terminal back to the shell, whose prompt hook sets a
    // title of its own. The tab follows down as well as up; a label that only ever grew
    // more specific would stay stuck on "vim" for the rest of the session.
    setTerminalTitle("term-live", "root@shop-web: /app");
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  root@shop-web: /app"]), {
      timeout: 5000,
    });
  });

  // The title is a nicety, not a contract: plenty of shells (a bare busybox sh with no rc
  // file) never emit one, and the BFF then reports none at all. Clearing it back to empty
  // must return the tab to the argv rather than leave it blank — a nameless tab is worse
  // than a tab named after the wrong thing.
  it("falls back to the launch command when a session reports no title", async () => {
    seedPanes([{ kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" }]);
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, title: "vim" },
    ]);
    renderView(Workspace);
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  vim"]));

    setTerminalTitle("term-live", "");
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  /bin/ash"]), { timeout: 5000 });
  });

  // "Another one of these, here" has always inherited the source's workload, command and
  // directory — but `dir` is where the source shell was TOLD to start, which stops being
  // true the moment the user cd's. The session's reported cwd is the live answer, and a
  // split is the one gesture that can use it. Asserted at the create request, because that
  // is where the inherited dir actually lands.
  it("gives a split the terminal's live directory, not the one it was launched in", async () => {
    seedPanes([
      { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], dir: "/app", sessionId: "term-live" },
    ]);
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, cwd: "/srv/deep", title: "ash" },
    ]);
    renderView(Workspace);
    // Wait for the session poll to have ANSWERED before splitting. The cwd arrives on that
    // poll, and a split fired in the first frame legitimately has no live directory yet —
    // without this the test would assert the fallback and call it a failure of the feature.
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  ash"]), { timeout: 5000 });

    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    // The launch dir "/app" is the wrong answer and is live in the pane, so a split that
    // ignored the session would land there rather than fail to land anywhere.
    await waitFor(() =>
      expect(terminalCreates().some((c) => c.dir === "/srv/deep")).toBe(true),
    );
    expect(terminalCreates().some((c) => c.dir === "/app")).toBe(false);
  });

  // A session that reports nothing must keep the old behaviour exactly: the launch dir is
  // still the best answer available, and "no cwd" must never be read as "the root".
  it("falls back to the launch directory when a split's source reports no cwd", async () => {
    seedPanes([
      { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], dir: "/app", sessionId: "term-live" },
    ]);
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, title: "ash" },
    ]);
    renderView(Workspace);
    await waitFor(() => expect(tabNames()).toEqual(["shop-web  ash"]), { timeout: 5000 });

    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await waitFor(() => expect(terminalCreates().some((c) => c.dir === "/app")).toBe(true));
    expect(terminalCreates().some((c) => c.dir === "/")).toBe(false);
  });

  // The follow pairing end to end: the terminal names the destination, the pane lands on
  // the session's directory, and it KEEPS landing there as the shell moves. The second cd
  // is what separates a follow from a one-shot navigation — a command that only navigated
  // once would pass every assertion up to it.
  it("makes a file pane track the directory of the terminal it follows", async () => {
    // Focus index 1: the command reads the FOCUSED pane, and only a terminal can name the
    // session to follow.
    seedPanes(
      [
        { kind: "files", path: "project" },
        { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" },
      ],
      1,
    );
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, cwd: "/srv/app" },
    ]);
    renderView(Workspace);
    // The terminal must be focused for the command to read it, and the split seed leaves
    // the focus on the last pane — assert it rather than assume it.
    await waitFor(() => expect(allCommands().some((c) => c.id === "ws:follow-terminal")).toBe(true));
    await waitFor(() =>
      expect(allCommands().find((c) => c.id === "ws:follow-terminal")?.disabled).toBeUndefined(),
    );

    runCommand("ws:follow-terminal");
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    expect(document.querySelector(".pane-chooser-title")?.textContent).toBe(
      "Follow this terminal in…",
    );
    // The file pane is row 0; the terminal itself is refused by kind.
    const rows = Array.from(
      document.querySelectorAll<HTMLElement>(".pane-chooser-item:not(.pane-chooser-other)"),
    );
    expect(within(rows[1]).getByText("a terminal")).toBeTruthy();
    fireEvent.click(rows[0]);
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeNull());

    // Landed: the container path became a virtual path under the workload mount.
    await waitFor(() => expect(tabNames()[0]).toBe("shop-web  app"));
    expect(screen.getAllByText("following").length).toBe(1);

    // And it KEEPS tracking — this is the half a one-shot navigation would fail.
    setTerminalCwd("term-live", "/etc/nginx");
    await waitFor(() => expect(tabNames()[0]).toBe("shop-web  nginx"), { timeout: 5000 });
  });

  // Following and steering by hand cannot both be true, and the hand has to win: a pane
  // that yanked the user back on the next poll would be unusable. Navigating is the signal.
  it("stops following as soon as the pane is navigated by hand", async () => {
    // Focus index 1: the command reads the FOCUSED pane, and only a terminal can name the
    // session to follow.
    seedPanes(
      [
        { kind: "files", path: "project" },
        { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" },
      ],
      1,
    );
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, cwd: "/srv/app" },
    ]);
    renderView(Workspace);
    await waitFor(() =>
      expect(allCommands().find((c) => c.id === "ws:follow-terminal")?.disabled).toBeUndefined(),
    );
    runCommand("ws:follow-terminal");
    await waitFor(() => expect(document.querySelector(".pane-chooser")).toBeTruthy());
    fireEvent.click(
      document.querySelectorAll<HTMLElement>(".pane-chooser-item:not(.pane-chooser-other)")[0],
    );
    await waitFor(() => expect(screen.queryAllByText("following").length).toBe(1));

    // Walk the followed pane somewhere itself, via its own breadcrumb. Its tab has to be
    // raised first: only the active pane in a stack renders a body, and therefore crumbs.
    fireEvent.click(document.querySelectorAll<HTMLElement>(".tab")[0]);
    const crumb = await waitFor(() =>
      within(document.querySelector<HTMLElement>(".pane-crumbs")!).getByText("srv"),
    );
    fireEvent.click(crumb);
    await waitFor(() => expect(screen.queryAllByText("following").length).toBe(0));

    // The shell moving again must NOT drag it back — the proof the follow really ended
    // rather than merely losing its badge.
    setTerminalCwd("term-live", "/etc/nginx");
    await new Promise((r) => setTimeout(r, 2500));
    expect(tabNames()[0]).not.toBe("shop-web  nginx");
  });

  // The command's disabled reason is the common case, not an edge one: OSC 7 is absent from
  // stock container images, so most sessions can never be followed and the row has to say
  // why rather than sit there doing nothing.
  it("refuses to follow a terminal whose shell never reports a directory", async () => {
    seedPanes(
      [
        { kind: "files", path: "project" },
        { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" },
      ],
      1,
    );
    seedTerminals([
      { id: "term-live", workload: "shop-web", cmd: ["/bin/ash"], alive: true, title: "ash" },
    ]);
    renderView(Workspace);
    // Wait for the poll, so this asserts "the session answered and reported no directory"
    // rather than "the list has not landed yet", which would give the same string.
    await waitFor(() => expect(tabNames()[1]).toBe("shop-web  ash"), { timeout: 5000 });
    expect(allCommands().find((c) => c.id === "ws:follow-terminal")?.disabled).toBe(
      "this shell does not report its directory",
    );
  });

  // Closing destroys something different in each kind — an editor's unsaved draft, a live
  // shell — so the confirmation is dispatched on the pane, not on the screen. Both are
  // exercised in ONE workspace, which is the only place they can disagree: a host that
  // asked the wrong question would still ask A question, and each suite above would pass.
  it("asks the question that belongs to the pane being closed", async () => {
    seedPanes([
      { kind: "files", path: "project" },
      { kind: "term", workload: "shop-web", cmd: ["/bin/ash"], sessionId: "term-live" },
    ]);
    seedTerminals([{ id: "term-live", workload: "shop-web", alive: true, state: "blocked" }]);
    renderView(Workspace);
    await screen.findByText("needs you"); // the session poll has answered: the shell is live

    closeTab("shop-web");
    expect((await dialog()).textContent).toContain("Closing the pane ends its session");
    await answer("Cancel");

    // The file pane, dirtied, asks the other question entirely.
    await openAsTab("compose.yaml");
    await screen.findByRole("button", { name: "Save" });
    const view = EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement)!;
    await waitFor(() => expect(view.state.doc.length).toBeGreaterThan(0));
    view.dispatch({ changes: { from: 0, insert: "# edited\n" } });
    await waitFor(() => expect(tabLabels().some((l) => l?.endsWith("*"))).toBe(true));

    closeTab("compose.yaml");
    expect((await dialog()).textContent).toContain("edits that were never saved");
    await answer("Cancel");
  });

  // The activity badge is driven by the polled session list, keyed on the pane's OWN
  // session id. Every other test of it SEEDS a layout that already holds an id, which
  // means the tab renders once, with the id in place, and never exercises the ordinary
  // path: the pane creates the session and records the id afterwards, so the tab is first
  // rendered with no id at all and has to pick the badge up when one arrives.
  it("badges a tab whose session became blocked, on a session the pane created itself", async () => {
    await connectPane();
    const id = liveTerminals()[0].id;
    setTerminalState(id, "blocked");
    // Longer than the 2s session poll: the badge cannot appear before the list is asked
    // again, and testing-library's 1s default would fail for that reason alone.
    await screen.findByText("needs you", {}, { timeout: 5000 });

    // The pulse rides on this badge only. `working` is a report and stays still; a class
    // put on `.badge.warn` at large would also set every warn badge in the app moving.
    expect(screen.getByText("needs you").className).toContain("attention");

    setTerminalState(id, "working");
    await screen.findByText("working", {}, { timeout: 5000 });
    expect(screen.queryByText("needs you")).toBeNull();
    expect(screen.getByText("working").className).not.toContain("attention");
  });

  // A split says "another one of these", and what "these" is has to survive the split. The
  // session must NOT: it belongs to the one pane that holds it, and a copied id would give
  // two panes one shell. Read from the create requests rather than the tabs, because two
  // terminals on the same workload and command look identical on screen — which is the
  // point of the split and also what makes a duplicated session invisible there.
  it("splits a terminal into a terminal, carrying the directory and not the session", async () => {
    seedPanes([{ kind: "term", workload: "shop-web", cmd: ["/bin/ash"], dir: "/etc/nginx" }]);
    renderView(Workspace);
    // Waiting for the TERMINAL, not merely for the create request: the pane records its
    // session id a turn after the BFF answers, and a split in between rebuilds a pane that
    // cannot yet see it has one — which opens a second session for the same tile and would
    // make the count below say 3 for a reason that has nothing to do with inheritance.
    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
    expect(terminalCreates()).toHaveLength(1);

    runCommand("ws:split-h");
    await waitFor(() => expect(tiles()).toHaveLength(2));
    // A second CREATE is the proof the new pane did not inherit the first one's session id:
    // a pane that had one would attach to it and never ask for a shell.
    await waitFor(() => expect(terminalCreates()).toHaveLength(2));
    expect(terminalCreates()[1]).toMatchObject({ workload: "shop-web", dir: "/etc/nginx" });
    expect(document.querySelector(".file-pane")).toBeNull(); // a terminal, not the empty pane
    expect(liveTerminals()[0].id).not.toBe(liveTerminals()[1].id);
  });
});

// The placement question, answered once in advance. Every creating command routes through
// `beginPlace`, so the setting is applied there and these tests are about the two things
// that follow from that: the answer is the one the prompt would have given, and NO command
// that goes through the chokepoint escapes it.
//
// Every test here asserts BOTH halves — the pane exists where the setting said, AND nothing
// asked. Neither alone is the claim: "no overlay" is equally true of a command that did
// nothing at all, and "a pane appeared" is equally true of one that asked and was answered.
describe("New pane placement (newPaneDisposition)", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    clearToasts();
  });
  // Restored per test rather than in a shared afterEach: this is module-level state, and a
  // test that left it set would change what 400-odd unrelated tests are exercising.
  const disposition = (v: "ask" | "split" | "tab" | "auto") => {
    onTestFinished(() => setNewPaneDisposition("ask"));
    setNewPaneDisposition(v);
  };

  // The device "auto" reads. Answered through window.matchMedia because that is what the
  // app asks — a helper that flipped some internal flag would test the flag. Restored per
  // test for the same reason the disposition is: it is module-level state that every other
  // test in the file shares (a live terminal asks it about the device pixel ratio).
  const primaryPointer = (kind: "coarse" | "fine", alsoTouch = kind === "coarse") => {
    const real = window.matchMedia;
    onTestFinished(() => {
      window.matchMedia = real;
    });
    window.matchMedia = ((q: string) => {
      const answered = q.includes("any-pointer:") ? alsoTouch : q.includes(`pointer: ${kind}`);
      return { ...(real(q) as MediaQueryList), media: q, matches: answered };
    }) as typeof window.matchMedia;
  };

  it('"Always as a tab" opens on the tile you are on, without asking', async () => {
    disposition("tab");
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(tabNames()).toEqual(["project", "project  compose.yaml"]));
    expect(tiles()).toHaveLength(1); // a tab, not a split
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
  });

  it('"Always side by side" opens beside it, on the side the arrow would have', async () => {
    disposition("split");
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    // WHICH side is the assertion, not merely "a split happened": the setting stands in for
    // one particular answer to the prompt (→), and a new pane landing on the left would be
    // a different answer wearing the same name.
    const tabsIn = (i: number) =>
      Array.from(tiles()[i].querySelectorAll<HTMLElement>(".tab-name")).map((e) =>
        e.textContent?.trim(),
      );
    expect(tabsIn(0)).toEqual(["project"]);
    expect(tabsIn(1)).toEqual(["project  compose.yaml"]);
  });

  // "auto" is the two answers above chosen by the device, so it is tested as ONE claim from
  // both sides: same setting, same gesture, only the pointer differs. Either half alone
  // proves nothing — a resolver stuck on "tab" passes the first, and one that ignored the
  // setting entirely passes the second, since side-by-side is what `ask` would give after an
  // arrow and what a broken read falls back to.
  it('"as a tab on touch devices" tabs on a coarse pointer and splits on a fine one', async () => {
    disposition("auto");
    primaryPointer("coarse");
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(tabNames()).toEqual(["project", "project  compose.yaml"]));
    expect(tiles()).toHaveLength(1);
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();

    cleanup();
    globalThis.localStorage?.clear();
    primaryPointer("fine");
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
  });

  // The deliberate reading of "touch device": the PRIMARY pointer, not "has a touchscreen
  // anywhere". This is the machine the distinction exists for — a laptop with a touchscreen,
  // which matches `any-pointer: coarse` and still has the width for two panes and a mouse to
  // arrange them with. It splits, and the assertion is that `any-pointer` is not what is read.
  it("splits on a laptop that merely has a touchscreen", async () => {
    disposition("auto");
    primaryPointer("fine", true); // fine primary, coarse available
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(tiles()).toHaveLength(2));
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
  });

  // The "every operation" half of the claim. Open is exercised above; these are the other
  // two commands that reach beginPlace, and they are here because the setting would be a
  // lie if it governed only the one route someone happened to test.
  it("covers New pane and Open in a terminal, not just Open", async () => {
    disposition("tab");
    renderView(Workspace);
    await screen.findByText("project");

    fireEvent.click(await screen.findByText("shop-web"));
    await screen.findByText("etc");
    runCommand("ws:open-terminal");
    await waitFor(() => expect(tabNames()).toHaveLength(2));
    expect(tabNames()[1]).toContain("shop-web");
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(tiles()).toHaveLength(1);

    runCommand("ws:new");
    await waitFor(() => expect(tabNames()).toHaveLength(3));
    expect(tabNames()[2]).toBe("All"); // the empty pane: a browser at the virtual root
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(tiles()).toHaveLength(1);
  });

  // A stored blob can hold any string — parseSettings spreads it over the defaults without
  // validating — so the two answers are matched positively and everything else asks. Not a
  // hypothetical: the failure mode of an `else` here is a workspace that silently stopped
  // asking because of a value nobody can see, which is the worst of the three outcomes.
  it("asks when the stored disposition is not one it knows", async () => {
    disposition("sidebyside" as never);
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    fireEvent.click(await screen.findByText("compose.yaml"));

    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "Escape" });
  });

  // The documented boundary, pinned so it is a decision rather than an oversight. `Split
  // pane…` names its own disposition, so the setting has nothing to answer for it — under
  // "tab" it would have to contradict the word Split. What it asks is WHICH EDGE, and it
  // still asks.
  it("leaves Split pane… asking which edge, whatever the setting says", async () => {
    disposition("tab");
    renderView(Workspace);
    await screen.findByText("project");

    runCommand("ws:split");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    // And its centre is still withheld — four edges, no stack — which is the other half of
    // why the setting cannot speak for it.
    expect(pickZones()).toHaveLength(4);
    fireEvent.keyDown(window, { key: "Escape" });
  });
});

// The workspace has two notions of "focused" and they have to stay in step: the pane wearing
// the frame, and the element receiving keys. Every test here asserts the SECOND one, because
// the first was never broken — a command moved it faithfully every time and left the keyboard
// behind, which is the failure mode where the user looks at one pane and types into another.
//
// The assertions are on `document.activeElement` and where it sits, never on a class or a
// signal: "the tile is highlighted" is exactly what was true while the bug was live.
describe("Pane focus follows the keyboard", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    clearToasts();
  });

  const activeIn = (sel: string) => !!document.activeElement?.closest(sel);

  // Gap 1. The editor is what an editor pane is FOR, so opening a file has to leave the
  // caret in the document — not on the Save button above it and not on <body>. Asserted on
  // `.cm-content`, CodeMirror's contenteditable, rather than on `.cm-editor`: the wrapper
  // would also match a stray focus on the gutter.
  it("puts the caret in the document when a file is opened", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await openAsTab("compose.yaml");

    await waitFor(() => expect(document.activeElement?.classList.contains("cm-content")).toBe(true));
  });

  // Gap 2, and the one that reads worst: the keys stay in the SHELL you just left, so the
  // next thing typed is a command in a container. Both accelerators are exercised because
  // they were reported together and reach `activate` by different routes.
  //
  // The starting state is asserted, not assumed — if the terminal never had the keyboard,
  // every later assertion here is vacuously true.
  it("takes the keyboard out of a terminal on Select next pane", async () => {
    seedPanes([
      { kind: "term", workload: "shop-web", cmd: ["/bin/sh"] },
      { kind: "files", path: "project" },
    ]);
    renderView(Workspace);
    await screen.findByText("compose.yaml");
    await waitFor(() => expect(activeIn(".term-wrap")).toBe(true));

    runCommand("ws:next");
    await waitFor(() => expect(activeIn(".file-pane")).toBe(true));
    expect(activeIn(".term-wrap")).toBe(false);
  });

  it("takes the keyboard out of a terminal on Select last active pane", async () => {
    seedPanes([
      { kind: "files", path: "project" },
      { kind: "term", workload: "shop-web", cmd: ["/bin/sh"] },
    ]);
    renderView(Workspace);
    await screen.findByText("compose.yaml");

    // Go to the terminal first — `ws:last` is dead until the focus has actually moved, and
    // this is also what puts the keyboard in the shell for the leg that matters.
    runCommand("ws:next");
    await waitFor(() => expect(activeIn(".term-wrap")).toBe(true));

    runCommand("ws:last");
    await waitFor(() => expect(activeIn(".file-pane")).toBe(true));
    expect(activeIn(".term-wrap")).toBe(false);
  });

  // The specific defect under gap 2: a listing claimed the keyboard ONCE per mount, so the
  // way back was broken while the way in worked. Two panes of the same kind, so nothing here
  // depends on a terminal — this is the latch, not the shell.
  //
  // It also pins WHERE it lands: the roving row, which after walking a listing is the row you
  // were on. Returning someone to the top of a list they had scrolled is a lesser version of
  // the same bug.
  it("returns the keyboard to the row it was on when a listing is re-entered", async () => {
    seedPanes([{ kind: "files", path: "project" }, { kind: "files", path: "" }]);
    renderView(Workspace);
    await screen.findByText("compose.yaml");

    // Two panes stacked as tabs in one tile, so `tiles()` cannot tell them apart — the pane
    // bodies can, and the inactive one stays in the DOM behind `display: none`.
    const panes = () => Array.from(document.querySelectorAll<HTMLElement>(".file-pane"));
    fireEvent.keyDown(panes()[0].querySelector(".fs-list")!, { key: "ArrowDown" });
    fireEvent.keyDown(panes()[0].querySelector(".fs-list")!, { key: "ArrowDown" });
    const parked = document.activeElement as HTMLElement;
    expect(parked.tagName).toBe("A");
    const name = parked.textContent;

    runCommand("ws:next");
    await waitFor(() => expect(panes()[1].contains(document.activeElement)).toBe(true));

    runCommand("ws:last");
    await waitFor(() => expect(panes()[0].contains(document.activeElement)).toBe(true));
    expect((document.activeElement as HTMLElement).textContent).toBe(name);
  });

  // The third pane kind, and the one with nothing to type into — which is why it needs the
  // claim rather than being excused from it. Left out, walking onto a picture leaves the keys
  // in whatever you came from, and the report was about coming from a terminal.
  it("takes the keyboard onto an image viewer, which has nothing to type into", async () => {
    seedPanes([
      { kind: "term", workload: "shop-web", cmd: ["/bin/sh"] },
      { kind: "files", path: "assets", open: "cornus-logo.png" },
    ]);
    renderView(Workspace);
    await waitFor(() => expect(document.querySelector(".image-viewer")).toBeTruthy());
    await waitFor(() => expect(activeIn(".term-wrap")).toBe(true));

    runCommand("ws:next");
    await waitFor(() => expect(document.activeElement).toBe(document.querySelector(".image-viewer")));
  });

  // NOT TESTED HERE: that a pane does not reach past a modal or the palette to claim the
  // keyboard. `claimFocus` guards for it, but the guard is unreachable in the current tree —
  // see the note on `modeOwnsKeyboard`. A test was written for it and deleted: it passed with
  // the guard removed, which makes it a green check for something nobody verified.
});

// Dragging a tab WITH A FINGER. This is the gesture that did not exist: rearranging was
// HTML5 drag-and-drop, which no mobile browser synthesises from touch, so every test above
// this one could pass on a desktop while the whole affordance was missing on a phone. It is
// one pointer drag now (src/dnd.ts) and these are the parts a mouse never exercises — the
// dwell that separates a drag from a scroll, and what happens to the press that is neither.
describe("Tiling chrome: dragging a tab with a finger", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  it("rearranges tiles from a long press, a move and a release", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);
    expect(document.querySelectorAll(".stack")).toHaveLength(2);

    // The whole gesture, with the finger's own lift rule: hold still, then travel.
    dropTabOn(document.querySelectorAll<HTMLElement>(".tab")[0], 1, "centre", true);

    expect(document.querySelectorAll(".stack")).toHaveLength(1);
    expect(document.querySelectorAll(".tab")).toHaveLength(2);
  });

  it("re-tiles to the edge a finger releases on", async () => {
    renderTerm();
    await splitViaEdge("right"); // a horizontal split
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    dropTabOn(document.querySelectorAll<HTMLElement>(".tab")[0], 1, "bottom", true);

    expect(document.querySelector(".split.v")).toBeTruthy();
    expect(document.querySelector(".split.h")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  // THE REASON THE DWELL EXISTS. A finger that presses and moves at once is scrolling — the
  // tab bar scrolls horizontally, the file listing vertically — so that press must not
  // become a drag. Without this the gesture would be "unusable" in the other direction: the
  // tab bar could not be panned without dragging a tab out of it.
  it("reads a press that moves before the dwell as a scroll, not a drag", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    // Pressed, but NOT held: the countdown is still running when the finger sets off.
    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[0], true, false);
    // It travels onto the other tile, where a real drag would be promising a drop. The
    // OTHER tile, deliberately: a release over the tab's own tile rearranges nothing even
    // when the press did become a drag, so a broken guard would look identical there.
    drag.over(1, "centre");
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    // And the abandoned countdown does not come back when the rest of it elapses.
    vi.advanceTimersByTime(DRAG_LIFT_MS * 2); // fake timers are tabDrag's, for a touch press
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    drag.drop();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("abandons the drag when the browser takes the pointer away", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[0], true);
    drag.over(1, "centre");
    expect(document.querySelector(".pane-drop-indicator")).toBeTruthy();
    // pointercancel is what a stolen capture or an incoming call sends. It is terminal, and
    // it is NOT a drop.
    drag.cancel();
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("cancels on Escape, leaving the layout alone", async () => {
    renderTerm();
    await splitViaEdge("right");
    expect(await screen.findAllByRole("combobox")).toHaveLength(2);

    const drag = tabDrag(document.querySelectorAll<HTMLElement>(".tab")[0], true);
    drag.over(1, "centre");
    drag.escape();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });
});

// SPLITTING A TILE WITH A FINGER. The edge strips arm on a 450ms HOVER dwell, which a touch
// device cannot perform at all — pointermove only reports while the finger is down, and
// lifting it disarms the strip before any click can land — so the bars were mouse-only and a
// phone had to reach the palette. The gesture that replaces the hover is two touches: hold
// until the bar has glowed its way up, let go and watch it fade, touch it again to spend it.
//
// Every test here drives it through the WORKSPACE, which is where the handler lives (the
// reach extends past the tile into the gutter, so the tile's own box cannot be the listener).
describe("Tiling chrome: splitting a tile with a finger", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  // The one relationship the two gestures cannot get wrong. The top strip lies over the tab
  // bar, which is the whole-stack drag handle, so a single press counts down towards both a
  // split and a tab drag; the shorter timer claims it. Reversed, the drag would win every
  // press and the top edge would have no touch route at all — which is the state this whole
  // change exists to leave behind.
  it("charges before a tab drag would lift", () => {
    expect(SPLIT_HOLD_MS).toBeLessThan(DRAG_LIFT_MS);
  });

  // The fade is drawn by CSS, so this is the only place its shape can be asserted — and its
  // shape is the whole of what makes the window readable rather than merely long.
  it("fades at one constant rate, by transition", () => {
    const cool = block(cssSource, ".pane-split-zone.cooling {");
    // ONE CONSTANT RATE, which is the whole design: brightness is heat, one to one, so half
    // lit means half the window left at every moment and from every starting point. An
    // explicit `linear` and not merely the absence of a curve — the base rule's shorthand
    // supplies `ease`, so a strip that declared nothing would be eased by inheritance.
    //
    // This is the assertion three separate easings failed to satisfy in three different ways,
    // so it is worth naming them: a gentle ease-in still ran the early fade faster than the
    // late one; an exponential one held the bar at ~99% for four seconds of a ten-second
    // window and piled every visible frame into the drop at the end, which reads as faster,
    // not slower; and ANY easing makes the rate depend on where the fade started, since a
    // partially re-heated bar runs the same curve compressed into a shorter time.
    expect(cool).toMatch(/transition-timing-function:\s*linear/);
    expect(cool).not.toMatch(/cubic-bezier|ease/);
    // A TRANSITION and not an animation, which is what lets a re-hold reverse the fade from
    // the brightness the bar is currently showing. An animation would restart from its own
    // `from` keyframe and jump to full before coming back down.
    expect(cool).not.toMatch(/animation/);
    expect(cssSource).not.toMatch(/@keyframes pane-split/);
    // The ramp stays linear: going up is progress towards a known end, and a curve there
    // would be saying something about urgency that the charge does not mean.
    expect(block(cssSource, ".pane-split-zone.charging {")).toMatch(/transition-timing-function:\s*linear/);
  });

  // A finger on one of the tile's edges, at the same 3px-in point the mouse dwell uses.
  function fingerOnEdge(side: Side) {
    const tile = document.querySelector<HTMLElement>(".stack")!;
    tile.getBoundingClientRect = () => TILE_RECT;
    const at = EDGE_POINT[side];
    const on = (type: string, el: HTMLElement = tile) => el.dispatchEvent(pointerEvent(type, at, 1, "touch"));
    return {
      press: (from?: HTMLElement) => on("pointerdown", from),
      release: () => on("pointerup"),
      strip: () => document.querySelector<HTMLElement>(`.pane-split-zone.edge-${side}`)!,
    };
  }

  it("glows while held, keeps glowing when let go, and splits on the next touch", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      // Charging: lit, but not yet an answer to anything.
      expect(finger.strip().classList.contains("charging")).toBe(true);
      expect(finger.strip().classList.contains("armed")).toBe(false);
      // The ramp the eye sees is the timer the press is racing — one number, carried inline.
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOLD_MS}ms`);

      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      expect(finger.strip().classList.contains("armed")).toBe(true);
      // Still charging, and that is the model: the finger is down and the heat is still
      // climbing — it has only stopped climbing towards anything the eye can see, since the
      // bar cannot get brighter than lit. What it gains from here is banked, not shown.
      expect(finger.strip().classList.contains("charging")).toBe(true);
      expect(document.querySelectorAll(".stack")).toHaveLength(1); // holding is not splitting

      // Letting go leaves the glow standing, now fading on its own.
      finger.release();
      expect(finger.strip().classList.contains("cooling")).toBe(true);
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOT_MS}ms`);
      expect(document.querySelectorAll(".stack")).toHaveLength(1); // and still not splitting

      // The second touch is what spends it — a TAP: press and let go again.
      //
      // A REAL tap, with time passing while the finger is down, and on a bar that is still
      // bright. Both halves are load-bearing. The re-heat this press also starts finishes in
      // 18ms at this heat, so a split that depended on catching it mid-ramp would miss every
      // human tap — and a test that pressed and released on a frozen clock would never
      // notice, which is exactly how that bug survived its first round of tests.
      vi.advanceTimersByTime(SPLIT_HOT_MS * 0.05);
      finger.press();
      const ramp = Math.round(SPLIT_HOT_MS * 0.05 * (SPLIT_HOLD_MS / SPLIT_HOT_MS));
      expect(finger.strip().style.transitionDuration).toBe(`${ramp}ms`);
      const tap = 80; // a deliberate tap: well past that ramp, well inside SPLIT_TAP_MS
      expect(tap).toBeGreaterThan(ramp);
      expect(tap).toBeLessThan(SPLIT_TAP_MS);
      vi.advanceTimersByTime(tap);
      finger.release();
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelector(".split.h")).toBeTruthy();
  });

  it("abandons a charge the finger lets go of too early", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS / 2);
      finger.release();
      expect(finger.strip().classList.contains("charging")).toBe(false);
      // …and the rest of the countdown does not arm it behind the user's back.
      vi.advanceTimersByTime(SPLIT_HOLD_MS);
      expect(finger.strip().classList.contains("armed")).toBe(false);
      finger.press(); // so a later touch on the same edge is just a touch
      finger.release();
      vi.advanceTimersByTime(1);
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  it("lets the glow go out, after which the same touch does nothing", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      finger.release();
      expect(finger.strip().classList.contains("armed")).toBe(true);
      vi.advanceTimersByTime(SPLIT_HOT_MS + 1);
      expect(finger.strip().classList.contains("armed")).toBe(false);
      expect(finger.strip().classList.contains("cooling")).toBe(false);
      finger.press(); // the window has closed: this only starts a new charge
      finger.release();
      vi.advanceTimersByTime(1);
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  // The collision the whole ordering rule exists for: the top strip lies over the tab bar,
  // so this press is counting down towards a split AND towards picking the tab up.
  it("claims a held press on the top edge away from the tab drag under it", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("top");
    const tab = document.querySelector<HTMLElement>(".tab")!;

    vi.useFakeTimers();
    try {
      finger.press(tab); // pressed ON the tab, in the top strip's band
      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      expect(finger.strip().classList.contains("armed")).toBe(true);
      // The drag's own dwell would have elapsed by now. It must find nothing left to lift:
      // the charge retired the press as it completed. Asserted on the TAB — `.dragging` is
      // driven straight off the drag's `source`, so it says whether the tab was picked up
      // regardless of what the pointer then found under it.
      vi.advanceTimersByTime(DRAG_LIFT_MS + 1);
      expect(tab.classList.contains("dragging")).toBe(false);
      finger.release();
      finger.press();
      finger.release();
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelector(".split.v")).toBeTruthy(); // a top split stacks them
  });

  // Holding past the point the bar is spendable is not wasted. The VISUAL peaks there — a lit
  // bar cannot get brighter — so the rest is banked and comes back as time at full strength,
  // which is what makes a deliberate long press mean "and keep it around for a while".
  it("banks a hold that runs past the critical value and spends it at full brightness", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS * 2); // twice the ramp: heat 2, the cap
      finger.release();
      // The visible fade is still exactly ONE window at the one constant rate…
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOT_MS}ms`);
      // …preceded by the overshoot, waited out at full brightness rather than drawn.
      expect(finger.strip().style.transitionDelay).toBe(`${SPLIT_HOT_MS}ms`);

      // So it outlives a bar that was only just charged: still lit a whole window later,
      // where one charged to the threshold would have gone out.
      vi.advanceTimersByTime(SPLIT_HOT_MS + 1);
      expect(finger.strip().classList.contains("armed")).toBe(true);
      vi.advanceTimersByTime(SPLIT_HOT_MS);
      expect(finger.strip().classList.contains("armed")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("caps what a hold can bank, however long the finger stays", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      // Heat climbs ~29x faster than it falls, so without a ceiling a finger left resting
      // would light this bar for the rest of the afternoon.
      vi.advanceTimersByTime(SPLIT_HOLD_MS * 40);
      finger.release();
      expect(finger.strip().style.transitionDelay).toBe(`${(SPLIT_HEAT_MAX - 1) * SPLIT_HOT_MS}ms`);
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOT_MS}ms`);
    } finally {
      vi.useRealTimers();
    }
  });

  // Re-holding a fading bar tops it up FROM WHERE IT IS, rather than restarting the gesture:
  // the user has already said which edge, and being made to say it again from cold is the
  // punishment for having taken a moment to think.
  it("re-heats a cooling bar from the heat it has left, not from cold", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      finger.release();
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOT_MS}ms`);

      // Three quarters of the way through the window, so a quarter of the heat is left.
      vi.advanceTimersByTime(SPLIT_HOT_MS * 0.75);
      finger.press();
      // The ramp asks only for the heat that is MISSING — three quarters of a full hold —
      // rather than the whole of it. A charge from cold would read `SPLIT_HOLD_MS`.
      expect(finger.strip().style.transitionDuration).toBe(`${Math.round(SPLIT_HOLD_MS * 0.75)}ms`);
      expect(finger.strip().classList.contains("charging")).toBe(true);

      // Held to the top, the bar is full again and gets the whole window back.
      vi.advanceTimersByTime(SPLIT_HOLD_MS);
      finger.release();
      expect(finger.strip().style.transitionDuration).toBe(`${SPLIT_HOT_MS}ms`);
      expect(finger.strip().classList.contains("armed")).toBe(true);

      // …and it is still a bar, not a split: re-heating commits nothing.
      expect(document.querySelectorAll(".stack")).toHaveLength(1);
      // Well past the ORIGINAL deadline, which the re-hold moved.
      vi.advanceTimersByTime(SPLIT_HOT_MS * 0.5);
      expect(finger.strip().classList.contains("armed")).toBe(true);
      finger.press();
      finger.release();
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  // A re-hold let go of before it reaches the top keeps what it gained and cools from there,
  // so the window it gets back is the one it actually earned.
  it("cools a partly re-heated bar from where the re-hold got to", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      finger.release();
      vi.advanceTimersByTime(SPLIT_HOT_MS * 0.75); // three quarters spent, a quarter left
      finger.press();
      // A hold that is NEITHER end of the gesture: longer than a tap, so it cannot be
      // spending the bar, and shorter than the ramp back to full, so it cannot be finishing
      // it either. The band is asserted rather than assumed — change the constants so that
      // it closes, and this test says so instead of quietly re-testing one of the ends.
      const ramp = SPLIT_HOLD_MS * 0.75;
      expect(SPLIT_TAP_MS).toBeLessThan(ramp);
      const hold = Math.round((SPLIT_TAP_MS + ramp) / 2);
      vi.advanceTimersByTime(hold);
      finger.release();
      // A quarter, plus what the hold bought: more than it had, less than the whole window.
      // This is also the CONSTANT-RATE invariant in its second instance — the fade always
      // runs `heat * SPLIT_HOT_MS`, so a bar starting at any brightness dims at the same
      // speed as one starting at full, and a partial re-heat buys exactly the time it paid
      // for rather than a whole window or a compressed one.
      const reached = 0.25 + hold / SPLIT_HOLD_MS;
      expect(finger.strip().style.transitionDuration).toBe(`${Math.round(reached * SPLIT_HOT_MS)}ms`);
      expect(finger.strip().classList.contains("cooling")).toBe(true);
      expect(document.querySelectorAll(".stack")).toHaveLength(1); // and not a split
    } finally {
      vi.useRealTimers();
    }
  });

  it("puts a lit bar out when the finger goes somewhere else", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const finger = fingerOnEdge("right");
    const tile = document.querySelector<HTMLElement>(".stack")!;

    vi.useFakeTimers();
    try {
      finger.press();
      vi.advanceTimersByTime(SPLIT_HOLD_MS); // exactly to the critical value
      finger.release();
      expect(finger.strip().classList.contains("armed")).toBe(true);
      // A touch in the middle of the pane: the user has moved on, and a bar still live
      // behind their attention is how an unrelated tap splits a tile out of nowhere.
      tile.dispatchEvent(pointerEvent("pointerdown", CENTRE, 1, "touch"));
      expect(finger.strip().classList.contains("armed")).toBe(false);
      vi.advanceTimersByTime(1);
    } finally {
      vi.useRealTimers();
    }
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });
});

// The tiling chrome's HOVER gestures remain unreachable on a touch device: the edge overlays
// arm only after a 450ms hover dwell, and a finger cannot hover — pointermove only fires
// while it is down, and the lift disarms the strip before the click lands. Everything here
// is the tap-only route, so no test in this block may dispatch a pointermove — that is the
// point of it. (Rearranging a tab is no longer in this category: see the block above.)
describe("Tiling chrome without a pointer (touch)", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  it("puts a pane menu on every tile, ungated by hover", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    expect(await screen.findAllByRole("button", { name: "Pane menu" })).toHaveLength(1);

    // A second tile gets its own, so the affordance follows the tiles rather than the
    // screen. (Made with the MOUSE gesture — this test is about the menu's presence.)
    await splitViaEdge("right");
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Pane menu" })).toHaveLength(2));

    // The DOM containing a button proves nothing about a browser being able to press it:
    // an un-armed .pane-split-zone is in the DOM too, and is click-through. The menu
    // carries no such inline gate, and the stylesheet must never hide it either — the
    // capability may not be conditional on the pointer, only its comfort may.
    const menu = screen.getAllByRole("button", { name: "Pane menu" })[0];
    expect(menu.style.pointerEvents).toBe("");
    expect(cssSource).not.toMatch(/\.pane-menu[^{]*\{[^}]*display:\s*none/);
    expect(cssSource).not.toMatch(/\.pane-menu[^{]*\{[^}]*opacity:\s*0[^.]/);
  });

  // The ⋮ carries no list of its own any more: it opens the command palette seeded with
  // the `pane` tag. One list instead of two that drifted apart, searchable, and each entry
  // shows its tmux bind — which is how anyone learns the binds exist at all.
  it("opens the palette on the pane tag instead of a menu of its own", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    await splitViaEdge("right"); // two panes, so Move is offered
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    await openPaneMenu();
    // The old modal is gone, not merely bypassed.
    expect(modalRequest()).toBeNull();
    expect((screen.getByRole("combobox", { name: "Filter commands" }) as HTMLInputElement).value).toBe(
      ":pane ",
    );

    // Exactly the tagged commands — and the exclusions carry as much weight as the
    // inclusions. The two directional split binds are pane commands that are deliberately
    // NOT tagged (accelerators for a question "Split pane…" already asks), and "Go to
    // Overview" stands for every command the seed must hide.
    const tagged = menuTitles();
    expect(tagged).toEqual([
      "Split pane…",
      "New pane…",
      "Open in a terminal…", // unnamed here: the seeded terminal has no workload yet
      "Move pane…",
      "Choose pane…",
      "Rotate panes",
      "Close focused tab",
    ]);

    // Deleting the seed widens to the whole palette, so the menu is a starting point and
    // not a cage. The untagged pane commands are what it widens TO — the two directional
    // splits and "Select next pane", all three accelerators for a question a tagged command
    // already asks — so their absence above was a filter working rather than a registry that
    // happens to hold only the tagged ones.
    fireEvent.input(screen.getByRole("combobox", { name: "Filter commands" }), {
      target: { value: "" },
    });
    expect(menuTitles()).toEqual(
      expect.arrayContaining([
        ...tagged,
        "Split pane left / right",
        "Split pane top / bottom",
        "Select next pane",
      ]),
    );
    expect(menuTitles().length).toBeGreaterThan(tagged.length);
  });

  // The palette acts on the FOCUSED pane, so the ⋮ has to make its own tile focused before
  // opening. Not left to the click: a <button> press does not move focus in every browser.
  // Asserted through a command that would visibly hit the wrong tile — closing tile 0 from
  // tile 0's ⋮ while tile 1 was the focused one.
  it("acts on the tile whose ⋮ was pressed, not the one that had focus", async () => {
    renderView(Workspace); // Files' panes are tellable apart; two empty terminals are not
    await screen.findByText("project");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await waitFor(() => expect(within(tiles()[1]).getByText("project")).toBeTruthy());
    fireEvent.click(within(tiles()[1]).getByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));
    // The split focused the NEW tile, so the wrong answer is available and distinguishable.
    expect(tiles()[1].classList.contains("focused")).toBe(true);

    await openPaneMenu(0);
    await chooseMenu("Close focused tab");

    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(1));
    // Tile 0 ("All") is the one that went. Had the ⋮ not moved the focus, "project" would
    // have closed instead and this would read ["All"].
    expect(tabLabels()).toEqual(["project"]);
  });

  // Why the terminal command carries the `pane` tag at all: opening a shell from the
  // workspace used to be reachable only by the prefix `t` or by typing in the palette —
  // a keyboard, on the one screen where every other pane operation has been put under a
  // finger. What made it safe to tag is that the row is not generic: it names the tile the
  // ⋮ belongs to, because the ⋮ focuses that tile before it opens. Asserted with the WRONG
  // answer live — the focused tile is the other one, sitting on a mount list with no
  // container behind it — so a menu that skipped the focus step fails here rather than
  // agreeing by coincidence.
  it("offers a terminal on the tile whose ⋮ was pressed, not the focused one", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    await splitViaEdge("right"); // focuses the new tile, which stays at the mount list
    await waitFor(() => expect(tiles()).toHaveLength(2));

    // findByText, not getByText: a freshly split pane re-fetches its listing, so the tile
    // holds an empty "nothing to browse" state for a tick first.
    fireEvent.click(await within(tiles()[0]).findByText("shop-web"));
    fireEvent.click(await within(tiles()[0]).findByText("etc"));
    await within(tiles()[0]).findByText("nginx");

    // Walking into a folder focuses the row, and that focuses its tile — so the focus has
    // to be put BACK on tile 1 for the wrong answer to be available at all. Moved by the
    // command rather than by a synthesised event, because "which tile is focused" is the
    // whole premise of the assertions below and a no-op event would quietly remove it.
    runCommand("ws:next");
    const cmd = () => allCommands().find((c) => c.id === "ws:open-terminal");
    await waitFor(() => expect(cmd()?.disabled).toBe("pick a workload first"));
    expect(cmd()?.title).toBe("Open in a terminal…");

    await openPaneMenu(0);
    expect(menuTitles()).toContain('Open "etc" in a terminal…');

    // And it opens what it says. Followed through to the CREATE REQUEST: a row naming /etc
    // in front of a shell that started at / is the exact failure a pane-dependent entry on
    // a per-tile menu risks, and a pane that merely appeared would prove nothing about it.
    await chooseMenu('Open "etc" in a terminal…');
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowRight" });
    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());
    expect(terminalCreates().at(-1)).toMatchObject({ workload: "shop-web", dir: "/etc" });
  });

  it("splits from the menu with no dwell, in the direction chosen", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");

    await openPaneMenu();
    await chooseMenu("Split pane…");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowDown" });
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    // Direction is the whole contract, and it is what a careless test drops: a split that
    // asserted only "two tiles now" would pass for every direction. The axis and the SIDE
    // are both pinned — "left" and "right" produce identical axis assertions and differ
    // only in child order.
    expect(document.querySelectorAll(".split.v")).toHaveLength(1);
    expect(document.querySelector(".split.h")).toBeNull();
    expect(tiles()[1].classList.contains("focused")).toBe(true);

    // And the gesture demonstrably did not go through the hover path: nothing was ever
    // armed, because nothing ever moved a pointer.
    expect(document.querySelectorAll(".pane-split-zone.armed")).toHaveLength(0);
    for (const z of document.querySelectorAll<HTMLElement>(".pane-split-zone")) {
      expect(z.style.pointerEvents).toBe("none");
    }
  });

  // tmux's `prefix C-o`. Driven through the REAL key path, because the chord is the point:
  // this is the app's first bind carrying a modifier, and until now the prefix handler
  // treated any Ctrl-bearing second key as a browser shortcut and passed it straight
  // through. Files gets the same bind, so the row runs on the tile labels that screen
  // gives its panes — the only way to see WHICH tile ended up where.
  it("rotates the tiles through the layout on prefix Ctrl+O", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await waitFor(() => expect(within(tiles()[1]).getByText("project")).toBeTruthy());
    fireEvent.click(within(tiles()[1]).getByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));

    // prefix, then Ctrl+O. pressBind's second argument is the SHIFT state, so this is
    // Ctrl without Shift — the chord as a keyboard actually reports it.
    expect(pressBind("o", false, { ctrlKey: true })).toBe("swallow");

    await waitFor(() => expect(tabLabels()).toEqual(["project", "All"]));
    // The layout itself did not move: still two tiles side by side, same divider.
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(document.querySelectorAll(".split.h")).toHaveLength(1);
    // And it is a rotation, not a one-way shuffle: going round again restores the start.
    expect(pressBind("o", false, { ctrlKey: true })).toBe("swallow");
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));
  });

  // tmux's `prefix o`, select-pane -t :.+ — and the pairing with the test above is the
  // point: the same letter WITHOUT Ctrl moves the focus and leaves the layout alone, where
  // Ctrl+O moves the layout and leaves the focus alone. Two commands one modifier apart
  // doing opposite halves of the same thing is exactly where a mis-wiring hides.
  it("selects the next pane on prefix o, wrapping round the layout", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await waitFor(() => expect(within(tiles()[1]).getByText("project")).toBeTruthy());
    fireEvent.click(within(tiles()[1]).getByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));

    const focused = () => tiles().findIndex((t) => t.classList.contains("focused"));
    expect(focused()).toBe(1); // the split focused the tile it made

    // Pane 2 is last in layout order, so the step wraps to the first.
    expect(pressBind("o", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(0));
    // Nothing was rearranged: a rotation would ALSO have left the focus looking right (it
    // travels with its tile), and would give itself away here — in the labels.
    expect(tabLabels()).toEqual(["All", "project"]);

    // Pressing it again STEPS. Without this the test passes just as well for "go to the
    // first pane", which is indistinguishable after one press from a two-pane wrap.
    expect(pressBind("o", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(1));
    expect(tabLabels()).toEqual(["All", "project"]);
    expect(document.querySelectorAll(".split.h")).toHaveLength(1);
  });

  // The walk steps through a tile's BACKGROUND TABS as well as between tiles, which is what
  // separates it both from the rotation (slots) and from the chooser's arrows (tiles). It is
  // the only keyboard route to a background tab that does not open the chooser first.
  it("steps into a background tab on prefix o, raising it", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: " " }); // centre of the only tile: stack it as a tab
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(document.querySelectorAll(".stack")).toHaveLength(1); // one tile, two tabs

    const tabs = () => Array.from(document.querySelectorAll<HTMLElement>(".tab"));
    const shownTab = () => tabs().findIndex((t) => t.classList.contains("active"));
    // The filled-in number is where the FOCUS is — the same mark the chooser reads. Asked
    // separately from `active` because this command has to move both: a tab holding the
    // focus while its tile goes on showing the other one is a focus nobody can see.
    const focusedTab = () => tabs().findIndex((t) => t.querySelector(".pane-number.current"));
    expect(shownTab()).toBe(1);
    expect(focusedTab()).toBe(1);

    expect(pressBind("o", false)).toBe("swallow");
    await waitFor(() => expect(shownTab()).toBe(0));
    expect(focusedTab()).toBe(0);
    // And it did not turn the stack back into two tiles on the way.
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  // tmux's `prefix ;` (last-pane). THREE panes, because with two "the pane I came from" and
  // "the next pane" are the same pane and every implementation of either key looks right —
  // the same reason the unit tests in lastpane.test.ts use three.
  it("goes back to the pane the focus came from on prefix ;, and alternates", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:split-h"); // A | B, focus on B
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    runCommand("ws:split-h"); // A | B | C, focus on C
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));

    const focused = () => tiles().findIndex((t) => t.classList.contains("focused"));
    expect(focused()).toBe(2);

    // Come to tile 0 from tile 2, by hand, so the memory is of a move the test made.
    fireEvent.click(tiles()[0].querySelector<HTMLElement>(".tab")!);
    await waitFor(() => expect(focused()).toBe(0));

    // Back to 2 — the pane we came from — and NOT to 1, which is where every "next" walk
    // from here would land. That inequality is the whole assertion.
    expect(pressBind(";", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(2));

    // Pressing it again returns, because going back is itself a move: the two panes
    // alternate rather than the key walking backwards through a history.
    expect(pressBind(";", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(0));
    expect(pressBind(";", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(2));

    // And the other key on the same layout goes somewhere else, which is what proves the
    // assertions above were about `;` and not about any key that moves the focus at all.
    expect(pressBind("o", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(0)); // pane 3 of 3: next wraps to the first
  });

  // The same key on the other workspace, driven the same way. It gets its own test rather
  // than riding on the one above because the pane-bind parity test compares only TAGGED
  // commands, and this one is untagged like `o` — so without this, `files:last` would be a
  // bind nothing presses.
  it("gives Files the same last-pane key", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    runCommand("ws:split-h");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    runCommand("ws:split-h");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));

    const focused = () => tiles().findIndex((t) => t.classList.contains("focused"));
    expect(focused()).toBe(2);
    fireEvent.click(tiles()[0].querySelector<HTMLElement>(".tab")!);
    await waitFor(() => expect(focused()).toBe(0));

    expect(pressBind(";", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(2)); // where we came from, not the next along
    expect(pressBind(";", false)).toBe("swallow");
    await waitFor(() => expect(focused()).toBe(0));
  });

  // The gate is "is there a pane to go back to", which is neither a pane count nor a tile
  // count — the two states below both hold more than one pane and neither can go back.
  it("keeps Select last active pane disabled until the focus has actually moved", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");

    // ASKING MOVES THE FOCUS. A tile's ⋮ calls `setFocus` on that tile before it opens the
    // palette (panes.tsx `openPaneCommands`, so the pane commands act on the menu you
    // opened) — which for this command is the very state being inspected. Every palette here
    // is therefore opened on the tile that ALREADY holds the focus, where that call is a
    // no-op. Opened on the other tile, the question arms the answer, and the first draft of
    // this test passed on exactly that.
    const openPalette = async (tile: number) => {
      await openPaneMenu(tile);
      fireEvent.input(screen.getByRole("combobox", { name: "Filter commands" }), {
        target: { value: "" },
      });
    };
    const closePalette = () =>
      fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter commands" }), { key: "Escape" });
    const row = () =>
      screen.getByText("Select last active pane", { selector: ".cmd-item-title" }).closest("button")!;
    const focused = () => tiles().findIndex((t) => t.classList.contains("focused"));

    await openPalette(0); // one tile, so this cannot move anything
    expect(menuTitles()).toContain("Select last active pane"); // in the palette…
    expect(row().getAttribute("aria-disabled")).toBe("true");
    expect(row().textContent).toContain("no pane to go back to");
    closePalette();
    await openPaneMenu();
    expect(menuTitles()).not.toContain("Select last active pane"); // …and not in the ⋮
    closePalette();

    // A disabled command still owns its key.
    expect(pressBind(";", false)).toBe("swallow");

    runCommand("ws:split-h"); // the split moves the focus, which is what arms it
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(focused()).toBe(1);
    await openPalette(1);
    expect(row().getAttribute("aria-disabled")).toBeNull();
    expect(row().querySelector("kbd")?.textContent).toBe(";");
    closePalette();

    // Reload the workspace: the layout comes back from localStorage with two panes and the
    // focus restored, and there is STILL nowhere to go back to — the memory is of a move
    // made in this session, not of the shape of the tree. A count-based gate would offer the
    // key here and send the user somewhere they have never been.
    cleanup();
    // Mounted WITHOUT reseeding — the point is that the persisted two-pane layout comes
    // back. renderTerm would write its own single pane over it and this would test nothing.
    renderView(Workspace);
    await screen.findAllByRole("combobox");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(focused()).toBe(1); // restored, not moved
    // Asserted by the KEY first, which nothing about asking can perturb.
    expect(pressBind(";", false)).toBe("swallow");
    expect(focused()).toBe(1);
    await openPalette(1);
    expect(row().getAttribute("aria-disabled")).toBe("true");
  });

  // A remembered pane can die without the focus moving, so the liveness check has to happen
  // on every read. Which close does that is not obvious and is worth naming: closing a pane
  // in ANOTHER tile moves the focus (the host sets it to the natural neighbour whether or not
  // the closed pane held it), while closing a background TAB of the tile you are on leaves
  // the focus exactly where it is. Only the second reaches the state under test.
  it("forgets the pane it would go back to when that pane is closed", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:split-h"); // A | B, focus on B
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    runCommand("ws:new"); // and a third pane, stacked as a tab on the focused tile
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: " " });
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(3));
    expect(document.querySelectorAll(".stack")).toHaveLength(2); // A | (B, C), focus on C

    const focused = () => tiles().findIndex((t) => t.classList.contains("focused"));
    const tabsOf = (tile: number) => Array.from(tiles()[tile].querySelectorAll<HTMLElement>(".tab"));
    fireEvent.click(tabsOf(1)[0]); // to B, so the pane we came from is C
    await waitFor(() => expect(tabsOf(1)[0].classList.contains("active")).toBe(true));

    // Close C, a background tab of the tile we are on: the focus does not move, and two panes
    // are left — so a gate counting panes would still say yes, and going back would activate
    // a pane that is not in the tree, leaving `focused` naming nothing.
    fireEvent.click(tabsOf(1)[1].querySelector<HTMLElement>(".tab-close")!);
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    expect(focused()).toBe(1);

    expect(pressBind(";", false)).toBe("swallow"); // owned, and dead
    expect(focused()).toBe(1);
    expect(tabsOf(1)[0].classList.contains("active")).toBe(true);
    await openPaneMenu(1); // the focused tile — see the note about ⋮ moving the focus
    fireEvent.input(screen.getByRole("combobox", { name: "Filter commands" }), {
      target: { value: "" },
    });
    const row = screen
      .getByText("Select last active pane", { selector: ".cmd-item-title" })
      .closest("button")!;
    expect(row.getAttribute("aria-disabled")).toBe("true");
  });

  // Untagged on purpose, so it is in the palette and NOT in the ⋮ — the rule the two
  // directional split binds are left out by. It is a shortcut through a question "Choose
  // pane…" already asks, and the ⋮ is a pointer surface, where the way to reach a pane is
  // to click it. What it needs is to be findable as a KEY, which is the palette's job.
  it("keeps Select next pane out of the ⋮ but in the palette, disabled at one pane", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");

    await openPaneMenu();
    expect(menuTitles()).not.toContain("Select next pane");
    // Widen to the whole palette: absent above was the filter working, not a missing command.
    fireEvent.input(screen.getByRole("combobox", { name: "Filter commands" }), {
      target: { value: "" },
    });
    const row = () => screen.getByText("Select next pane", { selector: ".cmd-item-title" }).closest("button")!;
    expect(row().getAttribute("aria-disabled")).toBe("true");
    expect(row().textContent).toContain("only one pane open");
    // While disabled the reason takes the accelerator's SLOT — the key is what you would
    // press, and right now pressing it does nothing.
    expect(row().querySelector("kbd")).toBeNull();
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter commands" }), { key: "Escape" });

    // A disabled command still OWNS its key: the press is swallowed rather than falling
    // through as a browser shortcut, and nothing happens.
    expect(pressBind("o", false)).toBe("swallow");
    expect(document.querySelectorAll(".tab")).toHaveLength(1);

    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await openPaneMenu();
    fireEvent.input(screen.getByRole("combobox", { name: "Filter commands" }), {
      target: { value: "" },
    });
    expect(row().getAttribute("aria-disabled")).toBeNull();
    // And now the accelerator is in that slot, which is the whole reason a keyboard-only
    // command is listed at all.
    expect(row().querySelector("kbd")?.textContent).toBe("o");
  });

  it("offers Rotate in the pane menu, disabled while there is one tile", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");

    await openPaneMenu();
    const row = () => screen.getByText("Rotate panes", { selector: ".cmd-item-title" }).closest("button")!;
    expect(row().getAttribute("aria-disabled")).toBe("true");
    expect(row().textContent).toContain("nothing to rotate");
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter commands" }), { key: "Escape" });

    // TWO TABS on one tile is still one slot — the gate counts tiles, not panes, and this
    // is the case a pane count would get wrong.
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: " " });
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    await openPaneMenu();
    expect(row().getAttribute("aria-disabled")).toBe("true");
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter commands" }), { key: "Escape" });

    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await openPaneMenu();
    expect(row().getAttribute("aria-disabled")).toBeNull();
    // The accelerator is shown, which is how anyone finds out the chord exists.
    expect(row().textContent).toContain("Ctrl+O");
  });

  // Move is always LISTED and disabled when there is nowhere to go, rather than vanishing:
  // a menu that changes shape as panes come and go teaches nothing, and an absent entry
  // reads as "this screen cannot do that" instead of "not just now".
  it("always offers Move, disabled while there is nowhere to move to", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");

    await openPaneMenu();
    expect(menuTitles()).toContain("Move pane…");
    const row = () => screen.getByText("Move pane…", { selector: ".cmd-item-title" }).closest("button")!;
    expect(row().getAttribute("aria-disabled")).toBe("true");
    // The reason is the whole difference between a disabled row and a riddle.
    expect(row().textContent).toContain("nowhere to move it");

    // Pressing it does nothing AND leaves the palette open — closing on a dead press would
    // look exactly like the command having run.
    fireEvent.click(row());
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter commands" }), { key: "Escape" });

    // A second pane, and the same entry now runs.
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await openPaneMenu();
    expect(row().getAttribute("aria-disabled")).toBeNull();
    fireEvent.click(row());
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
  });

  // Files, not Terminal, because the two panes have to be TELLABLE APART for the zone to
  // mean anything: a Files pane's path is durable tree state and shows in its tab, while
  // a terminal picker's selection is a local signal that a move legitimately discards
  // (panes unmount on every layout change — the reason files/drafts.ts exists). Asserting
  // on the picker would have failed an honest move.
  it("moves a pane by tapping a target zone, with no drag event at all", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    // Send the RIGHT tile into a folder so the two tabs differ. Scoped with within(),
    // since both panes list the same root and a bare query would match either — and
    // awaited, because the new tile fetches its own listing.
    await waitFor(() => expect(within(tiles()[1]).getByText("project")).toBeTruthy());
    fireEvent.click(within(tiles()[1]).getByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));

    await openPaneMenu(0);
    await chooseMenu("Move pane…");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    // Exactly one tile offers targets: the source is a lone pane in its own tile, and a
    // lone pane cannot move beside itself.
    expect(document.querySelectorAll(".pane-pick-overlay")).toHaveLength(1);
    expect(pickZones()).toHaveLength(5);
    expect(tileIndexOf(pickZones()[0])).toBe(1);

    fireEvent.click(pickZones().find((z) => z.classList.contains("zone-bottom"))!);

    await waitFor(() => expect(document.querySelectorAll(".split.v")).toHaveLength(1));
    expect(document.querySelector(".split.h")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    // "bottom" put the moved pane UNDER the target, so the target's label comes first.
    // Order is the whole assertion: every zone yields two tiles, and only this says which
    // one was tapped. It also rules out a rebuild — a recreated pane would not still be
    // the one labelled "All".
    expect(tabLabels()).toEqual(["project", "All"]);
    // And the mode closed itself behind the drop.
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(document.querySelector(".pane-pick-scrim")).toBeNull();
  });

  it("lets a stacked tab be pulled out to its own tile, but not stacked where it is", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    // Two tabs on ONE tile, plus a second tile so Move is offered at all.
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    tapPickZone("stack");
    await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    await openPaneMenu(0);
    await chooseMenu("Move pane…");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    // The source's OWN tile keeps its four edges — that is the only way to take a stack
    // apart without a drag — but loses its centre, where the pane already is. Hiding the
    // source tile altogether would make tap-move strictly weaker than the mouse gesture,
    // and showing all five would put a visible no-op on screen.
    const own = pickZones().filter((z) => tileIndexOf(z) === 0);
    expect(own).toHaveLength(4);
    expect(own.some((z) => z.classList.contains("zone-stack"))).toBe(false);

    fireEvent.click(own.find((z) => z.classList.contains("zone-bottom"))!);
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));
  });

  it("cancels picking on Escape without leaving the protocol armed", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    await openPaneMenu(0);
    await chooseMenu("Move pane…");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeNull());
    expect(document.querySelector(".pane-pick-scrim")).toBeNull();
    expect(document.querySelector(".stack.drop-target")).toBeNull();
    expect(document.querySelectorAll(".stack")).toHaveLength(2);

    // The overlay disappearing is not the same as the mode ENDING: a leaked drag source
    // is invisible until the next gesture, when dropIdFor answers for a stale pane. So
    // the test performs one afterwards and checks it still lands correctly.
    await openPaneMenu(0);
    await chooseMenu("Split pane…");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowDown" });
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));
  });

  // The wireframe mode REPLACED a modal, and a modal is keyboard-operable by construction:
  // it takes focus, keys drive it, Esc leaves. Everything below is that same contract,
  // re-earned on an overlay of buttons — without it the change trades a dialog anyone can
  // drive for a target only a pointer can reach.
  //
  // A direction key here is the ANSWER, not a way to move a cursor towards one: ↑ puts the
  // pane above and the mode is over. So every test below asserts a tree, never a focus
  // position — a test that watched focus would pass for an aim-then-confirm design, which
  // is the thing this deliberately is not.
  describe("by keyboard", () => {
    // Arming from the palette leaves focus wherever the command came from — inside a
    // terminal, which eats Tab. If the mode does not TAKE focus there is no first step, and
    // no tile is "current" for a direction key to act on.
    it("takes focus on the tile the user was on, as the tile and not a target", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");

      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

      const active = document.activeElement as HTMLElement;
      // The OVERLAY, not one of its five buttons: a ring on a single target would promise
      // that a confirm commits that one, when every direction is a single keystroke.
      expect(active.classList.contains("pane-pick-overlay")).toBe(true);
      expect(active.classList.contains("pane-pick-zone")).toBe(false);
      // Focusable on purpose but out of the tab sequence, along with the targets — Tab is
      // reserved for choosing a tile, so nothing else may consume it.
      expect(active.getAttribute("tabindex")).toBe("-1");
      for (const z of pickZones()) expect(z.getAttribute("tabindex")).toBe("-1");
      // Which tile is current has to be VISIBLE, and it is the tile's own focused framing
      // that says so — focusing the overlay drives the tile's focusin.
      expect(active.closest(".stack")!.classList.contains("focused")).toBe(true);
    });

    // One keystroke, one placement. Each row names an axis AND a side, because left/right
    // (and up/down) produce identical `.split.h` assertions and differ only in which child
    // the new pane became — the new pane is the one that takes focus, so `.stack.focused`
    // is where the side is observable. The alternate spellings are covered as their own
    // rows rather than assumed: a table can bind `k` to the wrong direction as easily as
    // it can bind it to none.
    it.each([
      ["ArrowUp", {}, "v", 0],
      ["ArrowDown", {}, "v", 1],
      ["ArrowLeft", {}, "h", 0],
      ["ArrowRight", {}, "h", 1],
      ["k", {}, "v", 0],
      ["j", {}, "v", 1],
      ["h", {}, "h", 0],
      ["l", {}, "h", 1],
      ["p", { ctrlKey: true }, "v", 0],
      ["b", { ctrlKey: true }, "h", 0],
      ["f", { ctrlKey: true }, "h", 1],
    ] as const)("places the pane with a single %s", async (key, mods, axis, newIndex) => {
      renderTerm();
      await screen.findAllByRole("combobox");
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

      fireEvent.keyDown(window, { key, ...mods });

      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
      expect(document.querySelectorAll(`.split.${axis}`)).toHaveLength(1);
      expect(document.querySelector(`.split.${axis === "v" ? "h" : "v"}`)).toBeNull();
      expect(tiles()[newIndex].classList.contains("focused")).toBe(true);
      // Over in one press: no second key, and nothing left armed behind it.
      expect(document.querySelector(".pane-pick-overlay")).toBeNull();
      expect(document.querySelector(".pane-pick-scrim")).toBeNull();
    });

    it.each([[" "], ["Enter"]])("stacks the pane as a tab with %s", async (key) => {
      renderTerm();
      await screen.findAllByRole("combobox");
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

      fireEvent.keyDown(window, { key });

      await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
      // A tab, emphatically not a split — the centre is the one target that is not a
      // direction, and binding it to a direction's key would be invisible in a tile count.
      expect(document.querySelectorAll(".stack")).toHaveLength(1);
      expect(document.querySelector(".split")).toBeNull();
    });

    // Where focus ENDS is half of a keyboard journey. The pick's targets are gone the
    // instant it commits, so unless the pane that just landed claims focus it falls to
    // <body> — the user is dropped out of the app by the action they performed. A pane
    // takes the keyboard when it mounts as the focused one; what makes that fire is
    // `drag.ts` setting the focus BEFORE committing the tree, so the pane can see itself
    // named. Both kinds of pane obey it, by different first controls: a terminal focuses
    // its workload picker, a file browser puts the cursor on its first row.
    it.each([
      [" ", "stacked"],
      ["ArrowRight", "split"],
    ])("puts focus in the new pane after %s", async (key) => {
      renderTerm();
      await screen.findAllByRole("combobox");
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

      fireEvent.keyDown(window, { key });
      // New pane… makes the workspace's EMPTY pane, and that is a file browser at the
      // mount list — so what must take the keyboard is a listing row, beside a terminal
      // whose picker already had it.
      await waitFor(() => expect(document.querySelector(".file-pane .fs-list a")).toBeTruthy());

      const active = document.activeElement as HTMLElement;
      expect(active.tagName).toBe("A");
      // The NEW pane, not the terminal that was already on screen — which pane holds the
      // focus is the whole assertion.
      const landed = document.querySelectorAll(".stack-pane")[1];
      expect(landed.contains(active)).toBe(true);
      expect(active.closest(".stack")!.classList.contains("focused")).toBe(true);
    });

    // Landing focus is not enough — the new pane has to KEEP it. A split re-parents the
    // tile it divides, so that tile's terminal is rebuilt, and an xterm focuses itself on
    // mount: the neighbour that merely happened to mount last took the keyboard back from
    // the pane the user had just asked for. Measured in Chromium first, where focus ended
    // in the OLD terminal's textarea every time.
    it("keeps focus in the new pane when a live terminal is rebuilt beside it", async () => {
      await connectPane();
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: "ArrowRight" });
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
      // The listing arrives on its own fetch, and there is no row to land on before it
      // does. Waiting on the tile count alone reads the focus one turn too early — which
      // is a PASS for the bug, since the answer then is <body> either way.
      await waitFor(() => expect(document.querySelector(".file-pane .fs-list a")).toBeTruthy());

      const active = document.activeElement as HTMLElement;
      expect(active.tagName).toBe("A"); // the new file pane's cursor row
      expect(document.querySelectorAll(".stack-pane")[1].contains(active)).toBe(true);
      expect(tiles()[1].classList.contains("focused")).toBe(true);
      // The sibling is genuinely live, so the steal was genuinely possible: without a live
      // xterm next door this test would pass on an unguarded build too.
      expect(document.querySelector(".xterm")).toBeTruthy();
    });

    // New pane… lights one tile too. Same rule as the split above, asserted here because
    // the two arrive by different commands and only this one offers a centre — five
    // targets on one tile, not five on every tile. That a MOVE still lights every candidate
    // is what the Tab tests below rest on, so the rule is intent-specific rather than
    // "there is only ever one overlay".
    it("lights only the focused tile for New pane…, with all five targets", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");
      await splitViaEdge("right");
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      expect(document.querySelectorAll(".pane-pick-overlay")).toHaveLength(1);
      expect(pickZones()).toHaveLength(5);
      // The split just focused the new tile, so tile 1 is where the cross belongs.
      expect(tileIndexOf(pickZones()[0])).toBe(1);
      expect(tiles()[1].classList.contains("focused")).toBe(true);
    });

    // Tab is the only navigation left, and it navigates the one thing a direction cannot
    // say: which tile. Only a MOVE has more than one candidate to navigate between — a
    // creating pick lights just the focused tile — so the setup is a move, out of a tile
    // that keeps its own targets because it holds two tabs.
    //
    // Both rows assert WHERE the pane landed rather than where focus went: focus moving is
    // not the claim, the claim is that the key acts on the tile focus names. And they are
    // not redundant — the no-Tab row is the only one that fails when the keys ignore the
    // current tile and always take the FIRST, since a single Tab from tile 0 lands on tile
    // 1 but a bug hardcoding index 0 answers the no-Tab row wrong. Found by neutralizing
    // exactly that: with only the Tab row, the broken build was green.
    async function armMoveFromStackedTile() {
      renderTerm();
      await screen.findAllByRole("combobox");
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: " " }); // two tabs on one tile...
      await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
      await splitViaEdge("right"); // ...and a second tile
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

      // The ⋮ focuses tile 0, so the move's source is a tab of the stacked tile and that
      // tile stays a candidate (four edges, no centre) alongside the other one.
      await openPaneMenu(0);
      await chooseMenu("Move pane…");
      await waitFor(() => expect(document.querySelectorAll(".pane-pick-overlay")).toHaveLength(2));
      expect(tileIndexOf(document.activeElement as HTMLElement)).toBe(0);
    }

    it.each([
      [0, 0],
      [1, 1],
    ])("acts on the current tile after %i Tab(s)", async (tabs, expected) => {
      await armMoveFromStackedTile();

      for (let i = 0; i < tabs; i++) fireEvent.keyDown(window, { key: "Tab" });
      expect((document.activeElement as HTMLElement).classList.contains("pane-pick-overlay")).toBe(true);
      expect(tileIndexOf(document.activeElement as HTMLElement)).toBe(expected);

      // Moving the tab out below a tile nests a `.split.v` inside that half of the existing
      // `.split.h`. Three tiles either way, so the nesting is what says which tile the key
      // reached; a count would pass for both.
      fireEvent.keyDown(window, { key: "ArrowDown" });
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));
      const halves = Array.from(document.querySelector(".split.h")!.children).filter((c) =>
        c.classList.contains("split-child"),
      );
      expect(halves[expected].querySelector(".split.v")).toBeTruthy();
      expect(halves[1 - expected].querySelector(".split.v")).toBeNull();
    });

    // Tab must also not escape the mode: everything behind the scrim is covered by it, so
    // handing focus to the nav would be a dead end the user cannot see they are in.
    it("keeps Tab inside the mode, wrapping across the candidates", async () => {
      await armMoveFromStackedTile();

      const first = document.activeElement;
      const seen: Element[] = [];
      for (let i = 0; i < 2; i++) {
        fireEvent.keyDown(window, { key: "Tab" });
        seen.push(document.activeElement!);
      }
      expect(seen.every((el) => el.classList.contains("pane-pick-overlay"))).toBe(true);
      expect(seen[1]).toBe(first); // wrapped, rather than leaving for the page behind
      fireEvent.keyDown(window, { key: "Tab", shiftKey: true });
      expect(document.activeElement).toBe(seen[0]);
      // Two candidates, so the hint says Tab is worth pressing.
      expect(document.querySelector(".pane-pick-keys")!.textContent).toMatch(/Tab next tile/);
    });

    // The source's own tile is the one shape with no centre — stacking a pane where it
    // already is does nothing — so Space there must do nothing too, and must NOT fall
    // through to some other target. Nothing else covers this: a placement lights every
    // tile with all five.
    it("does nothing on Space where there is no centre to stack into", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: " " }); // two tabs on one tile...
      await waitFor(() => expect(document.querySelectorAll(".tab")).toHaveLength(2));
      await splitViaEdge("right"); // ...and a second tile, so Move is offered at all
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

      await openPaneMenu(0);
      await chooseMenu("Move pane…");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      // Aim at the source's own tile, and assert the hole before relying on it.
      document.querySelectorAll<HTMLElement>(".pane-pick-overlay")[0].focus();
      expect(pickZones().filter((z) => tileIndexOf(z) === 0)).toHaveLength(4);

      const before = { tiles: tiles().length, tabs: document.querySelectorAll(".tab").length };
      fireEvent.keyDown(window, { key: " " });
      expect(tiles()).toHaveLength(before.tiles);
      expect(document.querySelectorAll(".tab")).toHaveLength(before.tabs);
      // Still armed — a no-op key is not a cancel, and the user has not answered yet.
      expect(document.querySelector(".pane-pick-overlay")).toBeTruthy();
      // And a direction on the same tile still works, so Space was inert rather than the
      // whole tile being.
      fireEvent.keyDown(window, { key: "ArrowDown" });
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));
    });

    // Cancelling puts focus back where it was. Without it the keyboard user is left on a
    // detached button — focus falls to <body> and the next Tab restarts from the top of
    // the page, which is how a mode becomes a trap in the bad sense.
    it("gives focus back to the opener when cancelled", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");
      const opener = document.querySelectorAll<HTMLElement>(".pane-menu")[0];
      opener.focus();

      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      expect(document.activeElement).not.toBe(opener);

      fireEvent.keyDown(window, { key: "Escape" });
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeNull());
      expect(document.activeElement).toBe(opener);
    });
  });

  it("closes through the host's guarded close, not a raw one", async () => {
    renderTerm();
    const picker = (await screen.findAllByRole("combobox"))[0];
    await screen.findByRole("option", { name: "shop-web" });
    fireEvent.change(picker, { target: { value: "shop-web" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(liveTerminals()).toHaveLength(1));
    await waitFor(() => expect(document.querySelector(".xterm")).toBeTruthy());

    await openPaneMenu();
    await chooseMenu("Close focused tab");

    // Terminal's close asks before killing a live session. A menu wired straight to the
    // model would also make the pane vanish, so "the pane went away" cannot be the
    // assertion — the QUESTION being asked, and the pane surviving until it is answered,
    // is what distinguishes the two.
    // The exact title, not a loose pattern: the dialog's MESSAGE also says "running", so
    // a regex over both matched twice and the query threw on the ambiguity.
    expect(await screen.findByText("Close this terminal?")).toBeTruthy();
    expect(document.querySelectorAll(".stack")).toHaveLength(1);
  });

  // The modal service's "list" choice layout. It was the pane menu's, and the pane menu is
  // now the command palette — so nothing in the app asks for it today (see TODO.md). The
  // coverage stays with the capability rather than following its last caller out: an
  // untested option in a shared service is one that breaks silently the day it is used
  // again. Driven through promptChoice directly, which is all that is left of it.
  it("lays a list-layout choice out as a column, not a row of slivers", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    const answer = promptChoice({
      title: "Pick one",
      layout: "list",
      options: [
        { value: "a", label: "First" },
        { value: "b", label: "Second" },
      ],
    });
    await screen.findByText("Pick one");

    // Three legs, because any two of them pass while the feature is broken: the request
    // can carry the layout while ChoiceBody ignores it (jsdom applies no CSS, so nothing
    // visual fails), and the CSS can exist while nothing ever gets the class.
    const req = modalRequest();
    expect(req?.kind === "choice" && req.layout).toBe("list");
    expect(document.querySelector(".modal-options.list")).toBeTruthy();
    expect(block(cssSource, ".modal-options.list {")).toMatch(/flex-direction:\s*column/);

    // A column is walked with the up/down keys, which is what the hint promises and what
    // the row form does NOT do. ArrowDown then Enter must therefore answer "b", not "a" —
    // the value is the whole proof that the key moved anything.
    const panel = document.querySelector(".modal-choice")!;
    fireEvent.keyDown(panel, { key: "ArrowDown" });
    fireEvent.keyDown(panel, { key: "Enter" });
    expect(await answer).toBe("b");
  });

  it("keeps the tab bar's height off the pane menu", () => {
    // Measured in a browser, asserted here as the rule that produced it: the global
    // `button { height: var(--control-h) }` is 32px against a 27.59px bar, so without
    // this reset every tile's header would grow and each screen would lose a row.
    const rule = block(cssSource, ".pane-menu {");
    expect(rule).toMatch(/height:\s*auto/);
    expect(rule).toMatch(/min-height:\s*0/);
  });

  // "Split pane…" is the general form of the two directional binds, and it asks with the
  // same wireframe. It is a separate command and not a flag on "New pane…" because it
  // answers a different question — "show me two of this" rather than "give me somewhere to
  // start" — and every assertion below is about a way those two differ.
  describe("Split pane… from the palette", () => {
    it("asks with the wireframe instead of splitting where the focus happens to be", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");

      runCommand("ws:split");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      // Nothing split yet and no dialog: the question is on the tiles. Both legs matter —
      // a command that split first and asked after would also show an overlay.
      expect(document.querySelectorAll(".stack")).toHaveLength(1);
      expect(modalRequest()).toBeNull();
      // And it is a DISTINCT entry: the two directional binds still split on the spot.
      const ids = allCommands().map((c) => c.id);
      expect(ids).toEqual(expect.arrayContaining(["ws:split", "ws:split-h", "ws:split-v"]));
    });

    // Shift-c, one shift away from the `c` that opens the other wireframe. The two are the
    // same gesture asking different questions, so the pair has to be exactly that: the
    // SHIFTED key must reach the split and the unshifted one must still reach the new pane.
    // A test that only checked "prefix C armed something" would pass with both keys wired
    // to either command, so each row asserts which question was asked — five targets for a
    // placement, four for a split.
    it.each([
      ["C", true, 4],
      ["c", false, 5],
    ])("arms the right wireframe from prefix %s", async (key, shift, zones) => {
      renderTerm();
      await screen.findAllByRole("combobox");

      // The REAL key path, not the command object: this asserts the bind is registered,
      // not merely that a command with that id exists.
      expect(pressBind(key, shift)).toBe("swallow");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      expect(pickZones()).toHaveLength(zones);
    });

    // The defining difference from "New pane…": four targets, not five. Stacking a tab is
    // not a split, and a centre here would make the command's own name a lie.
    it("offers no centre, because a tab is not a split", async () => {
      renderTerm();
      await screen.findAllByRole("combobox");

      runCommand("ws:split");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      expect(pickZones()).toHaveLength(4);
      expect(pickZones().some((z) => z.classList.contains("zone-stack"))).toBe(false);
      // So the key that means "stack" is inert, rather than falling through to a direction.
      fireEvent.keyDown(window, { key: " " });
      expect(document.querySelectorAll(".stack")).toHaveLength(1);
      expect(document.querySelector(".pane-pick-overlay")).toBeTruthy(); // still asking

      // The hint must not advertise it either — a named key that does nothing is worse
      // than an unnamed one.
      expect(document.querySelector(".pane-pick-keys")!.textContent).not.toMatch(/Space/);
      expect(document.querySelector(".pane-pick-hint")!.textContent).toMatch(/split/i);
    });

    // The other half of the difference: the new pane CONTINUES the tile it divided, where
    // "New pane…" starts empty. Asserted through the tab labels, which are the only place a
    // terminal pane's target is visible — and the empty case is asserted alongside, since
    // "two tabs now" would pass for both.
    it("makes a pane that continues the tile it split, unlike New pane…", async () => {
      await connectPane();
      expect(tabNames()).toEqual(["shop-web  /bin/ash"]);

      runCommand("ws:split");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: "ArrowRight" });
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
      expect(tabNames()).toEqual(["shop-web  /bin/ash", "shop-web  /bin/ash"]);

      // The contrast, on the same workspace: New pane… leaves the picker asking.
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: " " });
      await waitFor(() => expect(tabNames()).toHaveLength(3));
      expect(tabNames()).toContain("All"); // the empty pane is a file browser at the root
    });

    // A creating pick lights ONE tile — the focused one — where a move lights every
    // candidate. Four crosses on screen at once turned "where does this go" into "which of
    // these twenty buttons", and creating is a thing you do where you are working: the
    // wireframe names a direction, the focus already names the tile.
    //
    // Which tile that is has to be observable, so the two are deliberately unalike and the
    // split's pane continues the one it lit. A test that only counted overlays would pass
    // with the wrong tile lit.
    it("lights only the focused tile, and continues that one", async () => {
      await connectPane();
      runCommand("ws:new");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      fireEvent.keyDown(window, { key: "ArrowRight" });
      await waitFor(() => expect(tabNames()).toEqual(["shop-web  /bin/ash", "All"]));
      expect(tiles()[1].classList.contains("focused")).toBe(true);

      // The ⋮ moves the focus to its own tile, so this aims at the CONNECTED one while the
      // empty one held focus a moment ago.
      await openPaneMenu(0);
      await chooseMenu("Split pane…");
      await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
      expect(document.querySelectorAll(".pane-pick-overlay")).toHaveLength(1);
      expect(tileIndexOf(pickZones()[0])).toBe(0);
      // With one candidate there is nowhere for Tab to go, so the hint must not offer it.
      expect(document.querySelector(".pane-pick-keys")!.textContent).not.toMatch(/Tab/);

      fireEvent.keyDown(window, { key: "ArrowDown" });
      await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(3));
      expect(tabNames()).toEqual(["shop-web  /bin/ash", "shop-web  /bin/ash", "All"]);
    });
  });

  // Two things centre themselves while a pick is up — the hint at the top of the workspace
  // and the drop preview in the middle of a tile — and both were drawn INTO the wireframe
  // rather than clear of it.
  it("keeps the hint and the preview clear of the wireframe", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());

    // The hint must be a SIBLING of the scrim. The scrim is deliberately below the tiles'
    // overlays so a tap reaches a target, and a child cannot outrank its parent's stacking
    // context — so inside it, the hint is tinted over by the tile it lies on. The z-order
    // alone is not enough to state that, which is why the DOM relationship is asserted too.
    expect(document.querySelector(".pane-pick-hint")).toBeTruthy();
    expect(document.querySelector(".pane-pick-scrim .pane-pick-hint")).toBeNull();
    const z = (sel: string) => Number(/z-index:\s*(\d+)/.exec(block(cssSource, sel))![1]);
    expect(z(".pane-pick-hint {")).toBeGreaterThan(z(".pane-pick-overlay {"));
    // The trailing comma is the selector, not a typo: the pane chooser's scrim shares this
    // rule (same surface, same job, one mode along), so the header ends in a comma and
    // block() matches from the first name in the group.
    expect(z(".pane-pick-scrim,")).toBeLessThan(z(".pane-pick-overlay {"));
    expect(block(cssSource, ".pane-pick-hint {")).toMatch(/pointer-events:\s*none/);

    // Nothing previews until a POINTER asks: a key is the answer itself, so there is no
    // pending choice to show. Hovering a target still fills the region it promises — that
    // is the whole point of the preview — but drops its centred label, which otherwise
    // renders straight through the centre button.
    expect(document.querySelector(".pane-drop-indicator")).toBeNull();
    fireEvent.pointerEnter(pickZones().find((z) => z.classList.contains("zone-right"))!);
    expect(document.querySelector(".pane-drop-indicator.zone-right")).toBeTruthy();
    expect(document.querySelector(".pane-drop-label")).toBeNull();
  });

  it("marks the current tile without the ring that would vanish on it", () => {
    // The suppression here has to be EXPLICIT, and this test is what says so. It used to
    // be free: the app ring was an --accent-subtle halo and this overlay's backdrop is that
    // same colour, so the ring vanished on its own. The ring is now a solid --accent stroke
    // and would draw a second frame round a tile that already has one, saying "this control
    // is focused" where the answer the user needs is which cross is lit. Both premises are
    // asserted, so this fails the day the backdrop or the ring changes again.
    expect(block(cssSource, ".pane-pick-overlay {")).toMatch(/background:\s*var\(--accent-subtle\)/);
    expect(block(cssSource, ":focus-visible {")).toMatch(/box-shadow:\s*var\(--focus-ring\)/);
    expect(block(cssSource, ".pane-pick-overlay:focus {")).toMatch(/box-shadow:\s*none/);
    // What marks it instead: the whole cross comes up to full strength on the focused tile.
    expect(block(cssSource, ".pane-pick-overlay:focus .pane-pick-zone {")).toMatch(
      /border-color:\s*var\(--accent\)/,
    );
  });

  it("grows the touch targets without touching the header or the split geometry", () => {
    const coarse = block(cssSource, "@media (pointer: coarse)");
    // The handle grows by moving the tiles apart. A ::before overlay would instead make
    // ownerOf() report "divider" for pixels inside the neighbours' edge strips, so those
    // tiles would silently stop arming — the mouse's only split gesture, broken by a
    // touch fix.
    expect(coarse).toMatch(/\.divider\s*\{[^}]*flex-basis/);
    expect(block(cssSource, ".divider {")).not.toMatch(/::before/);
    // The header must not grow on ANY pointer — the ⋮ is what buys reachability, not a
    // taller bar. This is the assertion that fails if someone "fixes" touch by padding.
    expect(coarse).not.toMatch(/\.tab\s*\{/);
    expect(coarse).not.toMatch(/\.stack-tabs\s*\{/);
    // An invisible-but-tappable ✕ is a trap wherever there is no hover.
    expect(block(cssSource, "@media (pointer: coarse), (hover: none)")).toMatch(
      /\.tab-close\s*\{[^}]*opacity:\s*1/,
    );
  });

  it("keeps the platform's own long-press gestures off the drag surfaces", () => {
    const coarse = block(cssSource, "@media (pointer: coarse)");
    // A LONG PRESS IS THE DRAG on touch (src/dnd.ts), and the platforms answer the same
    // press with something of their own: iOS raises the selection callout, and the share
    // sheet over a link — and a file name IS a link, so its row has to say this too.
    expect(coarse).toMatch(/-webkit-touch-callout:\s*none/);
    for (const sel of [".stack-tabs", ".tab", ".fs-list tbody tr", ".fs-list tbody tr a"]) {
      expect(coarse).toContain(sel);
    }
    // NOT `touch-action: none`, which is the obvious way to stop a finger scrolling
    // mid-drag and would take the scroll away for good: this bar pans horizontally and the
    // listing vertically. The veto arrives with the LIFT instead (a non-passive touchmove,
    // in dnd.ts), which is the whole reason the drag waits for a dwell.
    expect(block(cssSource, ".stack-tabs {")).not.toMatch(/^\s*touch-action:/m);
    expect(block(cssSource, ".fs-list {")).not.toMatch(/^\s*touch-action:/m);
    // A mouse drag across the bar must not paint a text selection on its way out; the tab
    // labels are control text, and the pointer drag suppresses nothing by itself.
    expect(block(cssSource, ".stack-tabs {")).toMatch(/user-select:\s*none/);
    // And a drag in flight owns the cursor over everything it crosses, not just its source.
    expect(block(cssSource, "html.dnd-dragging,")).toMatch(/cursor:\s*grabbing/);
  });

  it("stops resizing when the browser takes the pointer away", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelector(".split")).toBeTruthy());

    const divider = grabDivider();
    // Pressed at the handle's centre, so the grab offset is 0 and the arithmetic below is
    // about the teardown only. The offset itself is covered by its own test.
    divider.dispatchEvent(pointerEvent("pointerdown", { clientX: 200, clientY: 200 }));
    // FIRST prove the harness is live. Without this, a rect stub that failed to take
    // would make `if (!span) return` swallow every move, and the teardown assertion
    // below would pass against a drag that never worked.
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 300, clientY: 200 }));
    await waitFor(() => expect(growOf(0)).toBeCloseTo(0.75, 2));

    // pointercancel is what a touch promoted to a scroll sends, and it never arrives with
    // a pointerup. Before this was handled the window listeners leaked and the pane went
    // on resizing under every later pointer move on the page.
    window.dispatchEvent(pointerEvent("pointercancel", { clientX: 300, clientY: 200 }));
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 100, clientY: 200 }));
    // 0.25 is well inside the model's 0.08/0.92 clamp, so a still-live drag WOULD move it.
    expect(growOf(0)).toBeCloseTo(0.75, 2);
  });

  it("ignores a second finger during a divider drag", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelector(".split")).toBeTruthy());

    const divider = grabDivider();
    divider.dispatchEvent(pointerEvent("pointerdown", { clientX: 200, clientY: 200 }, 1));
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 300, clientY: 200 }, 1));
    await waitFor(() => expect(growOf(0)).toBeCloseTo(0.75, 2));

    // A different pointerId must not drive this divider. Note both events carry an
    // explicit id: with jsdom's `undefined`, the filter would compare undefined against
    // undefined and pass vacuously, so this test would hold with no filter at all.
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 100, clientY: 200 }, 2));
    expect(growOf(0)).toBeCloseTo(0.75, 2);
  });

  it("does not jump the divider to the centre of the grab", async () => {
    renderTerm();
    await screen.findAllByRole("combobox");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelector(".split")).toBeTruthy());

    const divider = grabDivider();
    const before = growOf(0);
    expect(before).toBeCloseTo(0.5, 5); // the stub's handle centre is 200 for this reason
    // Press 18px right of the handle's centre (the stub spans 180..220), then move to
    // exactly that same point. Pressing and not moving must not resize — an invariant
    // with no tolerance for a dropped offset to hide inside, unlike comparing two ratios.
    divider.dispatchEvent(pointerEvent("pointerdown", { clientX: 218, clientY: 200 }));
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 218, clientY: 200 }));
    expect(growOf(0)).toBeCloseTo(before, 5);

    // And the offset is CARRIED, not merely ignored on the first move: sliding the finger
    // 40px right moves the handle 40px, putting its centre at 240 of 400.
    window.dispatchEvent(pointerEvent("pointermove", { clientX: 258, clientY: 200 }));
    expect(growOf(0)).toBeCloseTo(0.6, 2);
  });
});

// tmux's "choose from a list", pointed at the panes of one workspace. The list is in the
// corner, but the ARROWS WALK THE WORKSPACE — the list only reports where the walk has got
// to — and nothing moves until Enter.
//
// So the assertion that matters in almost every test here is a NEGATIVE one: after a
// direction key, the previewed tile and the focused tile are two different tiles. A suite
// that only checked "the highlight moved" would pass just as happily for a chooser that
// moved the focus with it, which is the one behaviour this mode exists to avoid.
describe("Pane chooser", () => {
  beforeEach(() => globalThis.localStorage?.clear());

  const chooser = () => document.querySelector<HTMLElement>(".pane-chooser");
  const rows = () => Array.from(document.querySelectorAll<HTMLElement>(".pane-chooser-item"));
  // The row's two halves, read separately: the label is the pane's own name and the number
  // is its place, and a test that lumped them together could not tell a renumbering from a
  // relabelling.
  const rowText = () =>
    rows().map((r) => r.querySelector<HTMLElement>(".pane-chooser-label")?.textContent?.trim());
  const rowNumbers = () =>
    rows().map((r) => r.querySelector<HTMLElement>(".pane-number")?.textContent);
  // The numbers out on the workspace: one per tile, naming the pane that tile is showing.
  const plateNumbers = () =>
    Array.from(document.querySelectorAll<HTMLElement>(".pane-number-plate")).map((p) =>
      p.textContent?.trim(),
    );
  // Which tile wears which mark, by index. Two separate questions on purpose: they are the
  // same tile only at the start and at the end.
  const previewedTile = () => tiles().findIndex((t) => t.classList.contains("previewed"));
  const focusedTile = () => tiles().findIndex((t) => t.classList.contains("focused"));
  const selectedRow = () => rows().findIndex((r) => r.classList.contains("selected"));

  // Two tiles, told apart by their labels: "All" (the virtual root) on the left and
  // "project" on the right, with the RIGHT one focused because that is what a split does.
  async function twoTiles() {
    renderView(Workspace);
    await screen.findByText("project");
    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    await waitFor(() => expect(within(tiles()[1]).getByText("project")).toBeTruthy());
    fireEvent.click(within(tiles()[1]).getByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["All", "project"]));
    expect(focusedTile()).toBe(1);
  }

  it("walks the tiles with the arrows and moves nothing until Enter", async () => {
    await twoTiles();
    // Driven through the real prefix path: `s` is tmux's own choose-session key, and it
    // cost this screen its Save bind, so "the command exists" is not the claim.
    expect(pressBind("s", false)).toBe("swallow");
    await waitFor(() => expect(chooser()).toBeTruthy());

    // The list is every PANE, in layout order, and the walk starts where the focus is.
    expect(rowText()).toEqual(["All", "project"]);
    expect(selectedRow()).toBe(1);
    expect(previewedTile()).toBe(1);

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    // The crux: the preview moved and the FOCUS DID NOT. Both marks are on screen, on
    // different tiles, which is the whole state this mode is for.
    expect(previewedTile()).toBe(0);
    expect(focusedTile()).toBe(1);
    expect(selectedRow()).toBe(0);
    // Nothing was rearranged either — a chooser that activated as it walked would still
    // pass the two assertions above if it also re-rendered the tabs.
    expect(tabLabels()).toEqual(["All", "project"]);

    fireEvent.keyDown(window, { key: "Enter" });
    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(0);
    expect(document.querySelector(".pane-chooser-scrim")).toBeNull();
  });

  // The numbers are what make a row and a place on screen the same thing when the labels
  // cannot: two Files panes at the virtual root are both called "All".
  it("numbers the panes in layout order, in the list and out on the tiles", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    expect(rowNumbers()).toEqual(["1", "2"]);
    // Plain digits, drawn as circles by CSS — NOT the Unicode circled forms (U+2460…),
    // which are missing from many monospace faces and stop at 20.
    expect(rowNumbers().join("")).toMatch(/^\d+$/);
    expect(block(cssSource, "\n.pane-number {")).toMatch(/border-radius:\s*50%/);
    // The plate drops the ring the badge needs: at 38px the FILL is already the circle, and
    // a ring inside the disc's own edge is fussy at exactly the size that needs no help.
    expect(block(cssSource, ".pane-number-big {")).toMatch(/border:\s*none/);

    // Same numbers, same order, on the workspace: tile 1 is "1" because it is first in
    // layout order, which is exactly what the arrows walk.
    expect(plateNumbers()).toEqual(["1", "2"]);
    expect(rowText()).toEqual(["All", "project"]);

    // A digit is a move, not a commit — the mode's one rule, applied to the new key.
    fireEvent.keyDown(window, { key: "1" });
    expect(selectedRow()).toBe(0);
    expect(previewedTile()).toBe(0);
    expect(focusedTile()).toBe(1);
    fireEvent.keyDown(window, { key: "9" }); // no pane 9: nothing moves, nothing breaks
    expect(selectedRow()).toBe(0);
    fireEvent.keyDown(window, { key: "Enter" });
    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(0);
  });

  // Where the focus IS, drawn by filling the number in rather than by a second mark beside
  // it. The two states are distinct and both are on screen: `selected` is where the walk has
  // got to, `current` is where the focus still is.
  it("fills in the number of the pane that holds the focus", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    const filled = () => rows().findIndex((r) => r.querySelector(".pane-number.current"));
    expect(filled()).toBe(1); // the split focused the right-hand tile
    expect(selectedRow()).toBe(1);
    // The ● that used to say this is gone, not merely hidden — it cost every row a column
    // and drew two things for one fact.
    expect(document.querySelector(".pane-chooser-mark")).toBeNull();

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    // The walk moved; the fill did not. A single mark could not have shown both.
    expect(selectedRow()).toBe(0);
    expect(filled()).toBe(1);
    // And the same fact reaches the workspace, where a dot had nowhere to go: the plate on
    // the focused tile is filled, the previewed one is not.
    const plates = () => Array.from(document.querySelectorAll<HTMLElement>(".pane-number-plate"));
    expect(plates().findIndex((p) => p.querySelector(".pane-number.current"))).toBe(1);

    fireEvent.keyDown(window, { key: "Enter" });
    await waitFor(() => expect(chooser()).toBeNull());
  });

  // The tab bar's height is load-bearing: it sets the tile's body offset, so a bar that grows
  // when the chooser opens makes every pane's content jump — which is the one thing a mode
  // for LOOKING at panes must not do. jsdom lays nothing out, so the height itself is checked
  // in a browser; what is pinned here is the rule that keeps it, measured off the stylesheet.
  it("keeps the tab badge smaller than the type around it", () => {
    const badge = block(cssSource, ".pane-number-tab {");
    expect(badge).toMatch(/font-size:\s*0?\.\d+em/);
    // An `em`, not a token: it has to shrink relative to whatever the bar is set in, and the
    // scale's smallest step (--text-xs) is already the bar's own size.
    expect(badge).not.toMatch(/var\(--text-/);
    expect(badge).toMatch(/vertical-align:\s*middle/);
  });

  it("takes the tab numbers off when the setting is off, and leaves the mode's own", async () => {
    onTestFinished(() => setPaneNumbersInTabs(true));
    await twoTiles();
    // Standing, with no mode in sight — that is what the setting governs.
    expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(2);
    setPaneNumbersInTabs(false);
    await waitFor(() => expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(0));

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(0);
    // Only that copy goes. The list and the plates ARE the mode — a list of rows with
    // nothing to match them to the screen would not be a chooser — so the setting cannot
    // reach them, and the walk still works.
    expect(rowNumbers()).toEqual(["1", "2"]);
    expect(plateNumbers()).toEqual(["1", "2"]);
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(previewedTile()).toBe(0);
  });

  // Two lifetimes, deliberately different. The TAB number is standing — it is what you aim
  // the digit key with, so needing the mode open to read it would make the jump useless. The
  // PLATE belongs to the mode: a permanent number the size of a coin over every pane is not
  // a workspace anyone would keep.
  it("keeps the tab numbers standing and the plates only for the mode", async () => {
    await twoTiles();
    expect(plateNumbers()).toEqual([]);
    expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(2);

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(plateNumbers()).toEqual(["1", "2"]);
    expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(2);

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(chooser()).toBeNull());
    expect(plateNumbers()).toEqual([]);
    expect(document.querySelectorAll(".tab .pane-number")).toHaveLength(2);
  });

  it("numbers a tile's tabs in turn, and renumbers when a pane closes", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["project"]));
    await openAsTab("compose.yaml"); // a second tab on this tile
    await waitFor(() => expect(tabLabels()).toHaveLength(2));
    await splitViaEdge("right"); // and a third pane, in its own tile
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    // Layout order runs THROUGH a tile's tabs before moving to the next tile, so the two
    // tabs take 1 and 2 and the second tile takes 3 — the same order allPanes walks, which
    // is the order the list is built in.
    expect(rowNumbers()).toEqual(["1", "2", "3"]);
    expect(plateNumbers()).toEqual(["2", "3"]); // tile 0 is showing its second tab
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(chooser()).toBeNull());

    // A number is a POSITION, not a name: closing the first tab moves everything up. A
    // number stored on the pane would keep the old one and address the wrong row.
    closeTab("project  compose.yaml");
    await waitFor(() => expect(tabLabels()).toHaveLength(2));
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(rowNumbers()).toEqual(["1", "2"]);
  });

  it("answers hjkl and Ctrl-B the same way it answers the arrows", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    fireEvent.keyDown(window, { key: "h" });
    expect(previewedTile()).toBe(0);
    fireEvent.keyDown(window, { key: "l" });
    expect(previewedTile()).toBe(1);
    // readline's Ctrl-B, which is a DIFFERENT map: a bare `b` means nothing here.
    fireEvent.keyDown(window, { key: "b" });
    expect(previewedTile()).toBe(1);
    fireEvent.keyDown(window, { key: "b", ctrlKey: true });
    expect(previewedTile()).toBe(0);
  });

  it("stops at the wall rather than wrapping to the far side", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(previewedTile()).toBe(0);
    // tmux wraps here. This does not — the highlight is being watched travel, and a jump
    // to the opposite edge of the screen reads as a bug rather than a shortcut.
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(previewedTile()).toBe(0);
    // And a direction with nothing on it at all is equally inert, rather than cancelling
    // the mode or landing somewhere diagonal.
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(previewedTile()).toBe(0);
    expect(chooser()).toBeTruthy();
  });

  // Deliberately NOT the two-tile layout, and the reason is worth recording: across tiles,
  // "Escape discarded the walk" and "Escape committed it and then the focus restore undid
  // that" are the same picture — the restored DOM focus lands inside the old tile, whose
  // focusin sets the workspace focus straight back. A previewed background TAB has no such
  // second chance: committing raises it, and nothing puts it back down.
  it("discards the walk on Escape instead of quietly committing it", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["project"]));
    await openAsTab("compose.yaml");
    await waitFor(() => expect(tabLabels()).toEqual(["project", "project  compose.yaml"]));
    const activeTab = () =>
      Array.from(document.querySelectorAll<HTMLElement>(".tab")).findIndex((t) =>
        t.classList.contains("active"),
      );
    expect(activeTab()).toBe(1);

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    fireEvent.keyDown(window, { key: "Tab" });
    expect(selectedRow()).toBe(0);

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(chooser()).toBeNull());
    // The walk is thrown away, which is what makes wandering the workspace free.
    expect(activeTab()).toBe(1);
    expect(document.querySelector(".stack.previewed")).toBeNull();
    expect(document.querySelector(".tab.previewed")).toBeNull();
  });

  it("leaves the focus on the tile it started from when Escape ends the mode", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(previewedTile()).toBe(0);

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(1);
  });

  it("reaches a background tab with Tab, which no direction can name", async () => {
    renderView(Workspace);
    fireEvent.click(await screen.findByText("project"));
    await waitFor(() => expect(tabLabels()).toEqual(["project"]));
    // Opened and placed on the centre: the file becomes a second TAB on the same tile —
    // two panes in one place, which is precisely the pair no arrow key can tell apart.
    await openAsTab("compose.yaml");
    await waitFor(() => expect(tabLabels()).toEqual(["project", "project  compose.yaml"]));
    const activeTab = () =>
      Array.from(document.querySelectorAll<HTMLElement>(".tab")).findIndex((t) =>
        t.classList.contains("active"),
      );
    expect(activeTab()).toBe(1);

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(selectedRow()).toBe(1);

    fireEvent.keyDown(window, { key: "Tab" });
    expect(selectedRow()).toBe(0);
    // The tab bar shows the preview too — the tile ring cannot, since both tabs are the
    // same tile — and the tile is still SHOWING the editor. Nothing has switched yet.
    const previewedTab = () =>
      Array.from(document.querySelectorAll<HTMLElement>(".tab")).findIndex((t) =>
        t.classList.contains("previewed"),
      );
    expect(previewedTab()).toBe(0);
    expect(activeTab()).toBe(1);

    fireEvent.keyDown(window, { key: "Enter" });
    await waitFor(() => expect(chooser()).toBeNull());
    // Only now does the tile change what it shows: choosing a background tab is choosing
    // to look at it, so the commit raises it as well as focusing it.
    expect(activeTab()).toBe(0);
  });

  it("goes straight there when a row is clicked", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    fireEvent.click(rows()[0]);
    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(0);
  });

  it("cancels on a click anywhere else, without moving the focus", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    // The scrim is what makes the promise keepable: a click that reached a pane would
    // focus it through the tile's own focusin, which is the one thing the mode says it
    // will not do before Enter.
    fireEvent.click(document.querySelector(".pane-chooser-scrim")!);
    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(1);
  });

  it("swallows the keys it claims and passes on the ones it does not", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    const press = (init: KeyboardEventInit) => {
      const ev = new KeyboardEvent("keydown", { cancelable: true, bubbles: true, ...init });
      window.dispatchEvent(ev);
      return ev.defaultPrevented;
    };
    expect(press({ key: "ArrowLeft" })).toBe(true);
    // Tab is trapped whether or not it has anywhere to go: the mode covers the workspace,
    // and letting Tab out would walk the nav behind it.
    expect(press({ key: "Tab" })).toBe(true);
    // The prefix must still get through, or the mode could not be left except by its own
    // keys — and the two modes' mutual exclusion below depends on it.
    expect(press({ key: "X", ctrlKey: true, shiftKey: true })).toBe(false);
    // As must an ordinary letter that means nothing here.
    expect(press({ key: "z" })).toBe(false);
  });

  it("gives way to a pick, and takes over from one", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    // Arming a pick from under the chooser (the prefix still works, as above) ends it.
    // Two modes at once would be two window key handlers answering one keystroke.
    expect(pressBind("c", false)).toBe("swallow");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    expect(chooser()).toBeNull();

    // And the other way round, which is the direction that needs its own code: the pick
    // does not know the chooser exists, so the chooser is what has to stand it down.
    expect(pressBind("s", false)).toBe("swallow");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(document.querySelector(".pane-pick-overlay")).toBeNull();
    expect(document.querySelector(".pane-pick-scrim")).toBeNull();
  });

  // The edge-split strips sit at z-index 5 and this mode's scrim at 3, so the scrim does
  // not cover them — and the dwell that arms them reads GEOMETRY rather than the event
  // target (its reach extends into the gutter, where the target belongs to nothing). Both
  // halves therefore have to be stood down explicitly, or a pointer resting near an edge
  // arms an invisible split hotzone and the click meant to cancel takes it instead.
  it("stands the edge-split gesture down while it is up", async () => {
    await twoTiles();
    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());

    const strip = screen.getAllByRole("button", { name: "Split pane, new pane on the right" })[0];
    dwellOnEdge(strip, "right");
    expect(document.querySelectorAll(".pane-split-zone.armed")).toHaveLength(0);
    expect(strip.style.pointerEvents).toBe("none");
    fireEvent.click(strip);
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
    expect(chooser()).toBeTruthy();
  });

  it("disarms a strip that was already armed when it opened", async () => {
    await twoTiles();
    const strip = screen.getAllByRole("button", { name: "Split pane, new pane on the right" })[0];
    dwellOnEdge(strip, "right");
    // The gesture really is armed, which is what makes the rest of this a test: the bar is
    // visible and hit-testable, and standing the DWELL down does nothing about a countdown
    // that already finished.
    expect(strip.classList.contains("armed")).toBe(true);
    expect(strip.style.pointerEvents).toBe("auto");

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    expect(document.querySelectorAll(".pane-split-zone.armed")).toHaveLength(0);
    fireEvent.click(strip);
    expect(document.querySelectorAll(".stack")).toHaveLength(2);
  });

  it("is offered but disabled while there is only one pane", async () => {
    renderView(Workspace);
    await screen.findByText("project");
    const cmd = () => allCommands().find((c) => c.id === "ws:choose")!;
    expect(cmd().disabled).toBe("only one pane open");

    // A disabled command still OWNS its key — swallowed, not passed to the browser as a
    // shortcut — and does nothing.
    expect(pressBind("s", false)).toBe("swallow");
    expect(chooser()).toBeNull();

    await splitViaEdge("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(cmd().disabled).toBeUndefined();
  });

  // The Terminal workspace's own stake in this: the chooser moves the WORKSPACE's focus,
  // and until it existed a terminal only ever took the keyboard on mount. Choosing a
  // running shell and then finding the keys still going somewhere else would make the
  // command useless for the screen it matters most on.
  it("hands the keyboard to the terminal it chose", async () => {
    await connectPane();
    // Queried fresh every time, never held: a split RE-PARENTS the sibling, so the
    // terminal is torn down and rebuilt with a new textarea. Holding the old one would
    // compare against a detached node and fail on an identity that means nothing.
    const xterm = () => document.querySelector<HTMLElement>(".xterm-helper-textarea");
    expect(xterm()).toBeTruthy();

    // A second, empty pane beside it, which the placement focuses — so the shell has
    // demonstrably lost the keyboard before the chooser gives it back. (The rebuild is
    // itself part of the setup: the surviving terminal mounts with autoFocus false, so
    // nothing but a later focus CHANGE can put the keyboard back in it.)
    runCommand("ws:new");
    await waitFor(() => expect(document.querySelector(".pane-pick-overlay")).toBeTruthy());
    tapPickZone("right");
    await waitFor(() => expect(document.querySelectorAll(".stack")).toHaveLength(2));
    expect(document.activeElement).not.toBe(xterm());

    runCommand("ws:choose");
    await waitFor(() => expect(chooser()).toBeTruthy());
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(previewedTile()).toBe(0);
    expect(document.activeElement).not.toBe(xterm()); // still nothing but a preview
    fireEvent.keyDown(window, { key: "Enter" });

    await waitFor(() => expect(chooser()).toBeNull());
    expect(focusedTile()).toBe(0);
    expect(document.activeElement).toBe(xterm());
  });
});

// Zooming a pane's contents (settings.paneZoom).
//
// The feature has two halves that must stand or fall together — a pinch on the pane and
// three commands in the palette — because the gesture's only way back is the commands. So
// most of what is asserted below is asserted TWICE, once with the setting off, and the
// off-half is the more important one: "opt-in" is a claim about what happens to a user who
// never touched the setting, and only a test that pinches with it off can see that.
describe("Zooming a pane's contents", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    clearToasts();
    // The zoom store is a module singleton shared with every other test in this file, like
    // the settings it sits beside. Emptied going IN as well as out, so a test cannot inherit
    // a level from whatever ran before it.
    keepZooms(new Set());
  });

  // Restored per test, for the reason the disposition helper above gives: module-level state
  // that several hundred unrelated tests share.
  const zoomOn = () => {
    onTestFinished(() => {
      setPaneZoom(false);
      keepZooms(new Set());
    });
    setPaneZoom(true);
  };

  const editorPane = async (extra: PaneData[] = []) => {
    seedPanes([{ kind: "files", path: "project", open: "compose.yaml" }, ...extra]);
    renderView(Workspace);
    return await waitFor(() => {
      const el = document.querySelector<HTMLElement>(".file-editor");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
  };

  // Two fingers landing `apart` px apart and moving to `to`. Both go down ON the element,
  // which is the recognizer's rule for whose gesture it is; the release is dispatched too so
  // nothing is left mid-pinch for the next test.
  const pinch = (el: HTMLElement, apart: number, to: number) => {
    const touch = (type: string, x: number, id: number) =>
      el.dispatchEvent(pointerEvent(type, { clientX: x, clientY: 0 }, id, "touch"));
    touch("pointerdown", 0, 1);
    touch("pointerdown", apart, 2);
    touch("pointermove", to, 2);
    touch("pointerup", to, 2);
    touch("pointerup", 0, 1);
  };
  // Comfortably past one threshold in each direction, written from the constant rather than
  // as a magic number so a change to the ratio moves these with it.
  const SPREAD = Math.ceil(100 * PINCH_STEP_RATIO) + 2;
  const SQUEEZE = Math.floor(100 / PINCH_STEP_RATIO) - 2;

  const zoomVar = (el: HTMLElement) => el.style.getPropertyValue("--pane-zoom");
  const zoomIds = () => allCommands().map((c) => c.id).filter((id) => id.startsWith("ws:zoom"));

  it("adds nothing at all to the palette until it is switched on", async () => {
    await editorPane();
    expect(zoomIds()).toEqual([]);
    zoomOn();
    expect(zoomIds()).toEqual(["ws:zoom-in", "ws:zoom-out", "ws:zoom-reset"]);
  });

  it("leaves a pinch entirely alone while it is switched off", async () => {
    // THE opt-in test. Everything else here runs with the feature on, where a broken guard is
    // invisible; this is the only assertion that fails if the gesture is attached regardless
    // of the setting — which would also mean the browser's own pinch had been taken away from
    // a user who never asked for it.
    const el = await editorPane();
    pinch(el, 100, SPREAD * 3);
    expect(zoomVar(el)).toBe("");
    expect(zoomLevels()).toEqual({});
  });

  it("grows and shrinks the editor's text as the fingers move", async () => {
    zoomOn();
    const el = await editorPane();
    pinch(el, 100, SPREAD);
    await waitFor(() => expect(zoomVar(el)).toBe(String(ZOOM_SCALES[ZOOM_HOME + 1])));
    pinch(el, 100, SQUEEZE);
    // Back to 1, and therefore back to saying NOTHING: the rules all spell
    // `var(--pane-zoom, 1)`, so a pane at rest carries no custom property at all.
    await waitFor(() => expect(zoomVar(el)).toBe(""));
    expect(zoomLevels()).toEqual({});
  });

  it("zooms on ctrl+scroll, which is how a trackpad pinch arrives", async () => {
    zoomOn();
    const el = await editorPane();
    el.dispatchEvent(new WheelEvent("wheel", { deltaY: -200, ctrlKey: true, bubbles: true, cancelable: true }));
    await waitFor(() => expect(zoomVar(el)).not.toBe(""));
    // An ordinary scroll is still an ordinary scroll — the pane has to stay scrollable.
    const before = zoomVar(el);
    el.dispatchEvent(new WheelEvent("wheel", { deltaY: -200, bubbles: true, cancelable: true }));
    expect(zoomVar(el)).toBe(before);
  });

  it("offers the way back on the tile's ⋮, which is the only route a finger has", async () => {
    zoomOn();
    const el = await editorPane();
    pinch(el, 100, SPREAD);
    await waitFor(() => expect(zoomVar(el)).not.toBe(""));

    await openPaneMenu(0);
    expect(menuTitles()).toContain("Reset zoom");
    await chooseMenu("Reset zoom");
    await waitFor(() => expect(zoomVar(el)).toBe(""));
  });

  it("disables Reset on a pane nobody has zoomed, and the ends of the range at the ends", async () => {
    zoomOn();
    const el = await editorPane();
    const reason = (id: string) => allCommands().find((c) => c.id === id)?.disabled;
    expect(reason("ws:zoom-reset")).toBe("not zoomed");
    expect(reason("ws:zoom-in")).toBeUndefined();

    for (let i = 0; i < ZOOM_SCALES.length; i++) runCommandIfEnabled("ws:zoom-in");
    await waitFor(() => expect(zoomVar(el)).toBe(String(ZOOM_SCALES[ZOOM_SCALES.length - 1])));
    expect(reason("ws:zoom-in")).toBe("already as large as it goes");
    expect(reason("ws:zoom-out")).toBeUndefined();
    expect(reason("ws:zoom-reset")).toBeUndefined();
  });

  it("refuses a file listing with the reason, rather than dropping the rows", async () => {
    // Listed-always-and-disabled, on the same rule ws:open-terminal follows: a row that came
    // and went with the kind of pane under the cursor would make the ⋮ a different menu
    // depending on which tile it was pressed from, which is the thing the `pane` tag exists
    // to prevent.
    zoomOn();
    seedPanes([{ kind: "files", path: "project" }]);
    renderView(Workspace);
    await screen.findByText("compose.yaml");
    for (const id of ["ws:zoom-in", "ws:zoom-out", "ws:zoom-reset"]) {
      expect(allCommands().find((c) => c.id === id)?.disabled).toBe(
        "a file listing has no contents to zoom",
      );
    }
    await openPaneMenu(0);
    expect(menuTitles()).toContain("Zoom in"); // present, just dead
  });

  it("keeps a pane's level when the layout rebuilds that pane", async () => {
    // The reason the levels live in a module singleton keyed by pane id rather than in the
    // component. Splitting a tile re-parents its sibling, which rebuilds it from scratch — a
    // pane the user did not touch. Held in component state, its zoom would vanish there.
    zoomOn();
    const el = await editorPane();
    pinch(el, 100, SPREAD);
    await waitFor(() => expect(zoomVar(el)).not.toBe(""));
    const was = zoomVar(el);

    runCommand("ws:split-h");
    await waitFor(() => expect(tiles()).toHaveLength(2));
    const now = document.querySelector<HTMLElement>(".file-editor") as HTMLElement;
    // The premise, asserted rather than assumed: if the split did NOT rebuild the pane this
    // test would pass on any implementation at all, including the broken one.
    expect(now).not.toBe(el);
    expect(zoomVar(now)).toBe(was);
  });

  // jsdom hands back an empty sheet for styles.css, so these read it as text (see `block`).
  it("takes its size from one variable, and leaves an unzoomed pane rendering as it always did", () => {
    // The fallback in `var(--pane-zoom, 1)` is the whole compatibility story: nothing sets
    // the property on a pane at rest (asserted above, in the DOM), so every one of these
    // declarations has to compute to what it was before the feature existed. --text-base is
    // what CodeMirror inherited from `body`; 100% is what the image had.
    const cm = block(cssSource, ".file-editor .editor-wrap .cm-editor {");
    expect(cm).toMatch(/font-size:\s*calc\(var\(--text-base\)\s*\*\s*var\(--pane-zoom,\s*1\)\)/);
    const img = block(cssSource, ".image-viewer-img {");
    expect(img).toMatch(/max-width:\s*calc\(100%\s*\*\s*var\(--pane-zoom,\s*1\)\)/);
    expect(img).toMatch(/max-height:\s*calc\(100%\s*\*\s*var\(--pane-zoom,\s*1\)\)/);
  });

  it("centres a zoomed image with auto margins, so every corner of it can be reached", () => {
    const box = block(cssSource, ".stack-pane .image-viewer {");
    expect(box).toMatch(/overflow:\s*auto/);
    // NOT align-items / justify-content, and this is the assertion most likely to be undone
    // by someone tidying up: a flex item centred by its CONTAINER and then overflowed is
    // clipped at its top and left unreachably, because a scroll container cannot scroll to a
    // negative offset. Auto margins on the item centre identically while it fits and collapse
    // once it does not, which is what makes a zoomed image pannable at all.
    expect(box).not.toMatch(/^\s*align-items:/m);
    expect(box).not.toMatch(/^\s*justify-content:/m);
    expect(block(cssSource, ".image-viewer-img {")).toMatch(/^\s*margin:\s*auto;/m);
  });

  it("forgets a pane's level once the pane is gone", async () => {
    zoomOn();
    const el = await editorPane([{ kind: "term", workload: "", cmd: [] }]);
    pinch(el, 100, SPREAD);
    await waitFor(() => expect(Object.keys(zoomLevels())).toHaveLength(1));
    runCommand("ws:close");
    await waitFor(() => expect(zoomLevels()).toEqual({}));
  });
});

// runCommandIfEnabled is runCommand without its disabled guard, for the one thing that guard
// gets in the way of: walking a command to the end of its range, where the last press is
// supposed to be refused. Deliberately not folded into runCommand — every other caller
// wants the guard, because pressing a disabled command is a path no user can reach.
function runCommandIfEnabled(id: string) {
  const cmd = allCommands().find((c) => c.id === id);
  if (cmd && !cmd.disabled) cmd.run();
}

describe("Settings", () => {
  beforeEach(() => {
    globalThis.localStorage?.clear();
    // reset the global singleton between tests
    setPassBrowserShortcuts(false);
    setPrefixEnabled(true);
    setPrefix("Ctrl+Shift+X");
    setNewPaneDisposition("ask");
  });

  // The control has to WRITE the setting, not merely display one — a select whose onChange
  // was wired to the wrong setter renders identically and reads correctly until reloaded,
  // so the assertion is on the persisted blob. The default is asserted first because it is
  // the load-bearing half: everything that existed before this setting keeps asking.
  it("chooses where a new pane goes, and persists it", async () => {
    renderView(Settings);
    // Matched loosely: unlike the prefix combination, this row carries a description, so
    // the label's accessible name is the whole paragraph and an exact match would not find it.
    const sel = (await screen.findByLabelText(/New pane placement/)) as HTMLSelectElement;
    expect(sel.value).toBe("ask");
    expect(Array.from(sel.options).map((o) => o.value)).toEqual(["ask", "split", "tab", "auto"]);
    fireEvent.change(sel, { target: { value: "tab" } });
    const saved = JSON.parse(globalThis.localStorage?.getItem("cornus.settings") || "{}");
    expect(saved.newPaneDisposition).toBe("tab");
  });

  it("toggles the opt-in 'Pass browser shortcuts' setting and persists it globally", async () => {
    renderView(Settings);
    const toggle = (await screen.findByLabelText(/Pass browser shortcuts/)) as HTMLInputElement;
    expect(toggle.checked).toBe(false); // faithful terminal by default
    fireEvent.click(toggle);
    expect(toggle.checked).toBe(true);
    const saved = JSON.parse(globalThis.localStorage?.getItem("cornus.settings") || "{}");
    expect(saved.passBrowserShortcuts).toBe(true);
  });

  it("configures the prefix key and persists it", async () => {
    renderView(Settings);
    const combo = (await screen.findByLabelText("Prefix combination")) as HTMLSelectElement;
    expect(combo.value).toBe("Ctrl+Shift+X"); // tmux-safe default
    expect(combo.disabled).toBe(false); // enabled by default
    fireEvent.change(combo, { target: { value: "Ctrl+Shift+Space" } });
    const saved = JSON.parse(globalThis.localStorage?.getItem("cornus.settings") || "{}");
    expect(saved.prefix).toBe("Ctrl+Shift+Space");
  });

  // Settings groups are plain sections, not `.cards` — and not the `.section` band
  // either, whose 2px rule is a divider this screen does not want. Asserting only
  // the ABSENCE of those classes would pass on an empty render, so pin the positive
  // shape too: every group is a `.setting-group` whose own heading is an `h2`.
  it("renders its groups as ordinary sections, not cards", () => {
    const { container } = renderView(Settings);
    const groups = Array.from(container.querySelectorAll("section.setting-group"));
    // Workspace leads: its settings are about the tiled chrome, which both workspaces
    // share, so they belong to neither the Terminal group nor a Files one.
    expect(groups.map((s) => s.querySelector("h2")?.textContent)).toEqual(["Workspace", "Terminal"]);
    expect(container.querySelectorAll(".card, .cards, .section")).toHaveLength(0);
    expect(container.querySelectorAll("h3")).toHaveLength(0);
    // Every setting is the same row, whatever control it carries: four checkboxes, two
    // selects (new-pane placement, prefix combination), and the shell-candidates textarea.
    // (This screen has been down to one group and back up to two, so the uniform-row rule is
    // what has to survive the churn — hence the shape asserted per row rather than a count
    // alone.)
    const rows = Array.from(container.querySelectorAll(".setting-row"));
    expect(rows).toHaveLength(7);
    for (const row of rows) {
      expect(row.tagName).toBe("LABEL");
      expect(row.querySelector(".setting-text > .setting-title")).not.toBeNull();
    }
  });
});
