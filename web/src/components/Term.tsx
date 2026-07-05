import { createEffect, onCleanup, onMount } from "solid-js";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../api";
import { isBrowserShortcut, isMacPlatform } from "./termKeys";
import { handlePrefixKey } from "../command-center";
import { settings } from "../settings";
import { loadTermFont, termFont, termFontPx, termFontSize, termLineHeight } from "./termFont";
import { claimFocus, holdsFocus } from "../views/tiling/focusclaim";
import { zoomable, zoomScale, zoomStyle } from "../views/tiling/zoom";

// Everything about a terminal's type that changes the size of a character cell — and so
// the number of rows and columns the grid has, and so the size the shell is told the
// window is. The three travel together for that reason: any one of them moving means the
// grid has to be measured again, and there is no path that changes one without the others
// being re-read.
export interface TermType {
  px: number;
  family: string;
  lineHeight: number;
}

// The type a pane should currently be set in: its own zoom level for the size, and the
// global settings for the rest. Reactive — read it inside an effect and the effect re-runs
// when the user changes either.
function termType(paneId: string | undefined): TermType {
  return {
    // The configured size is the BASE, and the pane's zoom scales it. The two are separate
    // on purpose: the setting is what this user reads comfortably and belongs to every
    // terminal they open, while a zoom is a thing done to one pane for as long as it is
    // needed. Changing the setting therefore moves a zoomed pane too, keeping the level it
    // was left at.
    px: termFontPx(paneId ? zoomScale(paneId) : 1, termFontSize(settings().terminalFontSize)),
    // Resolved rather than used raw. termFont maps an id this build does not know — a
    // stale blob, a hand-edited one — onto the default instead of handing xterm a
    // font-family string with a garbage name at the head of it.
    family: termFont(settings().terminalFont).stack,
    lineHeight: termLineHeight(settings().terminalLineHeight),
  };
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
  let applyType: ((t: TermType) => void) | undefined;

  onMount(() => {
    // Constructed with the type it should already be in, not defaulted and then corrected:
    // xterm measures its cell when it opens, and a terminal built at the default and fixed
    // a tick later spends that tick having told the shell a column count taken from the
    // wrong font.
    const type0 = termType(props.paneId);
    const term = new Terminal({
      cursorBlink: true,
      fontSize: type0.px,
      fontFamily: type0.family,
      lineHeight: type0.lineHeight,
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

    // Whether this terminal has been torn down. The font-load continuations below outlive
    // any single turn of the event loop, so each has to ask before touching the terminal
    // it closed over: a pane closed while its face was still in flight would otherwise fit
    // a disposed grid.
    let disposed = false;
    // Re-measure once the requested faces have actually arrived. This is the whole reason
    // loadTermFont exists — see its comment: until the woff2 lands, `font-display: swap`
    // has xterm measuring the FALLBACK, so the grid above was fitted to the wrong cell and
    // the shell was told a column count to match. Cheap when there is nothing to wait for
    // (an already-loaded or built-in family resolves immediately, and the fit addon only
    // resizes when the dimensions actually differ).
    const refitWhenFontReady = (t: TermType) => {
      void loadTermFont(t.px, t.family).then(() => {
        if (!disposed) refit();
      });
    };
    refitWhenFontReady(type0);

    // Refit AFTER the type change, never before: the fit addon measures a cell at the type
    // xterm currently has, so fitting first would size the grid to the old font and leave
    // the pane a column short (or over) until the next resize happened to correct it. The
    // equality guard is what keeps the effect below from re-fitting on every unrelated
    // re-run — the settings signal carries every preference in the app, so it fires on
    // toggles this terminal does not read, and a refit is not free: it renegotiates the PTY
    // size with the BFF.
    applyType = (t) => {
      const o = term.options;
      if (o.fontSize === t.px && o.fontFamily === t.family && o.lineHeight === t.lineHeight) {
        return;
      }
      o.fontSize = t.px;
      o.fontFamily = t.family;
      o.lineHeight = t.lineHeight;
      refit();
      refitWhenFontReady(t);
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
      disposed = true;
      focusTerm = undefined;
      applyType = undefined;
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
  // terminal that exists, and every later one is the pane's zoom or the terminal type
  // settings actually changing. The first pass is a no-op by construction — onMount built
  // the Terminal from this same function — so the mount-time font load is kicked off there
  // rather than being left to fall out of here. A pane with no id (a Term outside the
  // tiling) reads scale 1, so for it this tracks the settings alone.
  createEffect(() => {
    applyType?.(termType(props.paneId));
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
