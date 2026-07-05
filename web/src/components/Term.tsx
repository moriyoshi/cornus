import { createEffect, onCleanup, onMount } from "solid-js";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../api";
import { isBrowserShortcut, isMacPlatform } from "./termKeys";
import { handlePrefixKey } from "../command-center";
import { settings } from "../settings";
import { claimFocus, holdsFocus } from "../views/tiling/focusclaim";
import { zoomable, zoomScale, zoomStyle } from "../views/tiling/zoom";

// The terminal's unzoomed type size, and the one thing in the app that cannot be a CSS
// token: xterm measures a character cell from this number to lay its grid out, and paints
// the glyphs onto a canvas that inherits nothing.
export const TERM_FONT_PX = 13;

// The px xterm is given for a zoom scale. Exported and pure because it is the only part of
// the terminal's zoom a test can reach — the Terminal instance is local to onMount and
// deliberately stays there (see focusTerm below).
export function termFontPx(scale: number): number {
  // A floor rather than a clamp on the scale table: 6px is already past legibility, and the
  // number that must never reach xterm is a zero or a negative, which would divide by zero
  // in the fit addon's cell measurement.
  return Math.max(6, Math.round(TERM_FONT_PX * scale));
}

// TermExit describes why the socket closed. code is the WebSocket close code (see
// reconnect.ts: 4000 ended, 4001 superseded, else transient); opened records whether
// the socket ever connected, which tells a lost session from a mere drop.
export interface TermExit {
  code: number;
  reason: string;
  opened: boolean;
}

export interface TermProps {
  // In persistent mode, sessionId attaches to an existing BFF terminal session
  // (survives reload). Otherwise the legacy path opens an ephemeral exec against
  // workload directly. Exactly one of sessionId / workload drives the socket.
  sessionId?: string;
  workload?: string;
  cmd?: string[];
  // onOpen fires once the socket connects; onExit fires when it closes, carrying the
  // server's close code/reason and whether it had opened.
  onOpen?: () => void;
  onExit?: (exit: TermExit) => void;
  // Whether this terminal should hold the keyboard. It is NOT "am I new" — a tiled layout
  // remounts panes it never touched (a split re-parents its sibling, so that sibling's Term
  // is rebuilt), and an unconditional focus there means the pane the user just asked for
  // loses the keyboard to a neighbour that merely happened to mount last. The caller answers
  // with "is this the focused pane", which is the same guard the workload picker uses.
  //
  // Read REACTIVELY, not once at mount: a pane can become the focused one long after it
  // mounted, and until the pane chooser existed nothing said so — choosing a terminal from
  // the list moved the workspace's focus while the keyboard stayed wherever it was, which
  // is the one thing "go to that pane" must not do.
  autoFocus?: boolean;
  // The id of the pane this terminal fills, when it is in one. It is what the per-pane zoom
  // level is keyed by, and passing the id rather than the level itself is what lets this
  // component own BOTH halves of zooming a terminal — the gesture on its own host element,
  // and the font size only it can apply — instead of having a caller wire the two together
  // and have to keep them pointing at the same pane.
  paneId?: string;
}

// Term is an interactive terminal bridged over a BFF WebSocket. Binary frames
// carry raw bytes both ways; resizes go as JSON text frames ({"resize":{h,w}}),
// matching handleExecWS / handleTermAttach. It refits on both window and
// container (pane) resizes so it works inside a draggable tiled layout.
export default function Term(props: TermProps) {
  let host!: HTMLDivElement;
  // The one thing outside onMount that needs the xterm instance. A closure rather than the
  // instance itself, so nothing else can reach a terminal that is mid-teardown: it is set
  // once the terminal is open and cleared before it is disposed.
  let focusTerm: (() => void) | undefined;
  // The same shape as focusTerm, and for the same reason: the terminal must not be reachable
  // while it is being torn down. Set once it is open, cleared before it is disposed.
  let applyFont: ((px: number) => void) | undefined;

  onMount(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: termFontPx(props.paneId ? zoomScale(props.paneId) : 1),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);

    // Key routing (all read at event time, so changes apply live without remount):
    //  1. the app-wide prefix controller gets first say: it can force a disposition
    //     — "swallow" (drop, e.g. the prefix key), "browser" (let it propagate,
    //     e.g. the combo after the prefix), or "shell". xterm drives the state
    //     machine here because it, not the DOM, owns keys while a pane is focused.
    //  2. otherwise, the opt-in passBrowserShortcuts hands browser chrome shortcuts
    //     (new/close/switch tab, zoom, macOS Cmd combos) to the browser.
    //  3. otherwise the shell gets the key. Terminal control chars stay here — see
    //     termKeys.ts.
    const mac = isMacPlatform();
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      const guarded = handlePrefixKey(e);
      if (guarded === "swallow") {
        e.preventDefault();
        return false;
      }
      if (guarded === "browser") return false;
      if (guarded === "shell") return true;
      if (settings().passBrowserShortcuts && isBrowserShortcut(e, mac)) return false;
      return true;
    });

    term.open(host);
    const refit = () => {
      try {
        fit.fit();
      } catch {
        // The pane can momentarily have zero size (e.g. mid-collapse); ignore.
      }
    };
    refit();
    // Refit AFTER the size change, never before: the fit addon measures a cell at the size
    // xterm currently has, so fitting first would size the grid to the old font and leave
    // the pane a column short (or over) until the next resize happened to correct it. The
    // equality guard is what keeps the effect below from re-fitting on every unrelated
    // re-run — and a refit is not free, it renegotiates the PTY size with the BFF.
    applyFont = (px) => {
      if (term.options.fontSize === px) return;
      term.options.fontSize = px;
      refit();
    };

    const params = new URLSearchParams();
    params.set("h", String(term.rows));
    params.set("w", String(term.cols));
    let path: string;
    if (props.sessionId) {
      path = `/terminals/${encodeURIComponent(props.sessionId)}/attach?${params}`;
    } else {
      for (const c of props.cmd ?? []) params.append("cmd", c);
      path = `/workloads/${encodeURIComponent(props.workload ?? "")}/exec?${params}`;
    }
    const sock = new WebSocket(wsURL(path));
    sock.binaryType = "arraybuffer";

    let opened = false;
    sock.onopen = () => {
      opened = true;
      props.onOpen?.();
    };
    sock.onmessage = (ev) => {
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    sock.onclose = (ev) => {
      term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n");
      props.onExit?.({ code: ev.code, reason: ev.reason, opened });
    };
    const enc = new TextEncoder();
    term.onData((data) => {
      if (sock.readyState === WebSocket.OPEN) sock.send(enc.encode(data));
    });
    term.onResize(({ rows, cols }) => {
      if (sock.readyState === WebSocket.OPEN) {
        sock.send(JSON.stringify({ resize: { h: rows, w: cols } }));
      }
    });

    // Refit on window resize AND on container-size changes: dragging a split
    // divider resizes the pane without firing a window resize.
    const onWindowResize = () => refit();
    window.addEventListener("resize", onWindowResize);
    const ro = new ResizeObserver(() => refit());
    ro.observe(host);
    focusTerm = () => term.focus();

    onCleanup(() => {
      focusTerm = undefined;
      applyFont = undefined;
      window.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      sock.close();
      term.dispose();
    });
  });

  // Declared after onMount so it runs after it: the first pass is the old mount-time
  // `if (props.autoFocus) term.focus()`, and every later one is the pane becoming focused
  // while it was already on screen (the chooser's Enter, a closed neighbour handing the
  // focus over).
  //
  // The shared rule, so a terminal reaching past a modal or the palette is not a thing this
  // one component gets to decide differently from the other three panes: `holdsFocus` also
  // makes the re-runs free rather than merely harmless. Answered about the WRAPPER, not the
  // xterm's textarea — xterm puts its accessibility tree and its own helper elements in
  // there too, and a claim that fired whenever the textarea was not the exact active element
  // would fight them.
  claimFocus(
    () => !!props.autoFocus,
    () => holdsFocus(host),
    () => focusTerm?.(),
  );

  // Declared after onMount for the same reason claimFocus is: the first pass then finds a
  // terminal that exists, and every later one is the pane's zoom actually changing. A pane
  // with no id (a Term outside the tiling) reads scale 1 and this never does anything.
  createEffect(() => {
    applyFont?.(termFontPx(props.paneId ? zoomScale(props.paneId) : 1));
  });

  return (
    <div
      class="term-wrap"
      ref={(el) => {
        host = el;
        if (props.paneId) zoomable(el, () => props.paneId as string);
      }}
      // Set even though nothing in the terminal's own CSS reads it: it is the pane stating
      // its level on its root, the way the editor and the image preview do, and it is the
      // only outward sign that a canvas-painted grid has been zoomed at all. The glyphs
      // themselves cannot come from here — xterm measures its cell from the fontSize option
      // above and paints to a canvas that inherits no CSS type at all.
      style={props.paneId ? zoomStyle(props.paneId) : {}}
    />
  );
}
