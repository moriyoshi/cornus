import { onCleanup, onMount } from "solid-js";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../api";
import { isBrowserShortcut, isMacPlatform } from "./termKeys";
import { handlePrefixKey } from "../command-center";
import { settings } from "../settings";

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
}

// Term is an interactive terminal bridged over a BFF WebSocket. Binary frames
// carry raw bytes both ways; resizes go as JSON text frames ({"resize":{h,w}}),
// matching handleExecWS / handleTermAttach. It refits on both window and
// container (pane) resizes so it works inside a draggable tiled layout.
export default function Term(props: TermProps) {
  let host!: HTMLDivElement;

  onMount(() => {
    const term = new Terminal({ cursorBlink: true, fontSize: 13 });
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
    term.focus();

    onCleanup(() => {
      window.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      sock.close();
      term.dispose();
    });
  });

  return <div class="term-wrap" ref={host} />;
}
