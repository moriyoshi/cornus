import { Show, For, createSignal, createEffect, onCleanup, onMount } from "solid-js";
import type { Accessor } from "solid-js";
import Term from "../../components/Term";
import type { Workload } from "../../api";
import { createTerminal } from "../../api";
import type { Pane } from "../tiling/layout";
import { paneExitAction } from "./reconnect";

// TermPane is the body of one terminal tile in the tiled workspace: the per-pane
// session lifecycle (create / attach / reconnect) plus the empty-pane workload picker
// and the connecting/lost/elsewhere status. The surrounding frame — tab bar, drag,
// split edges — is the generic tiling chrome (views/tiling/panes.tsx); this is only the
// content the chrome renders for a pane. Stacked panes stay mounted (the chrome hides
// inactive tabs with display:none), so background sessions keep running.

// TermData is a terminal pane's durable payload, carried through splits/moves/stacking
// and persisted: its target workload, command, and the BFF session id once created.
export interface TermData {
  workload: string;
  cmd: string[];
  sessionId?: string;
}

// TermCtx is the slice of the workspace a pane body needs.
export interface TermCtx {
  focused: Accessor<string>;
  setSession: (id: string, sessionId: string) => void;
  retarget: (id: string, workload: string, cmd: string[]) => void;
  closePane: (id: string) => void;
  running: Accessor<Workload[]>;
}

// A pane's session lifecycle past "live": elsewhere (another tab took it over) or lost
// (an unexpected drop we couldn't silently recover — offer Reconnect).
type PaneConn = "live" | "elsewhere" | "lost";

export default function TermPane(props: { pane: Pane<TermData>; ctx: TermCtx }) {
  const [status, setStatus] = createSignal<"idle" | "connecting" | "error">("idle");
  // conn is the session lifecycle past "live"; reconnectKey is a nonce whose bump
  // forces the keyed <Show> to remount <Term>, i.e. reattach the socket.
  const [conn, setConn] = createSignal<PaneConn>("live");
  const [reconnectKey, setReconnectKey] = createSignal(0);
  let creating = false;
  let failures = 0;
  let stableTimer: ReturnType<typeof setTimeout> | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  const clearTimers = () => {
    clearTimeout(stableTimer);
    clearTimeout(reconnectTimer);
  };
  onCleanup(clearTimers);

  const d = () => props.pane.data;

  // When a pane has a target but no live session, create one. Guarded so the effect
  // re-running never opens duplicate sessions; a new target resets reconnect state.
  createEffect(() => {
    const p = props.pane;
    setConn("live");
    setReconnectKey(0);
    failures = 0;
    clearTimers();
    if (p.data.workload && !p.data.sessionId && !creating) {
      creating = true;
      setStatus("connecting");
      createTerminal(p.data.workload, p.data.cmd)
        .then((s) => {
          props.ctx.setSession(p.id, s.id);
          setStatus("idle");
        })
        .catch(() => setStatus("error"))
        .finally(() => {
          creating = false;
        });
    }
  });

  const onTermOpen = () => {
    clearTimeout(stableTimer);
    stableTimer = setTimeout(() => {
      failures = 0;
    }, 3000);
  };

  const onTermExit = (exit: { code: number; opened: boolean }) => {
    clearTimeout(stableTimer);
    switch (paneExitAction(exit, failures)) {
      case "close":
        props.ctx.closePane(props.pane.id);
        return;
      case "elsewhere":
        setConn("elsewhere");
        return;
      case "lost":
        setConn("lost");
        return;
      case "reattach": {
        failures += 1;
        const delay = Math.min(1000, 100 * 2 ** (failures - 1));
        reconnectTimer = setTimeout(() => setReconnectKey((k) => k + 1), delay);
        return;
      }
    }
  };

  const reattachHere = () => {
    setConn("live");
    failures = 0;
    setReconnectKey((k) => k + 1);
  };

  const liveSession = () =>
    d().sessionId && conn() === "live" ? `${d().sessionId}#${reconnectKey()}` : undefined;

  return (
    <Show when={d().workload} fallback={<PanePicker pane={props.pane} ctx={props.ctx} />}>
      <Show
        when={liveSession()}
        keyed
        fallback={
          <PaneStatus
            status={status()}
            conn={conn()}
            onReattach={reattachHere}
            onRetry={() => props.ctx.retarget(props.pane.id, d().workload, d().cmd)}
          />
        }
      >
        {(_key) => <Term sessionId={d().sessionId} onOpen={onTermOpen} onExit={onTermExit} />}
      </Show>
    </Show>
  );
}

function PaneStatus(props: {
  status: "idle" | "connecting" | "error";
  conn: PaneConn;
  onReattach: () => void;
  onRetry: () => void;
}) {
  const live = () => props.conn === "live";
  return (
    <div class="pane-status">
      <Show when={props.conn === "elsewhere"}>
        <p class="muted">Session opened in another tab.</p>
        <button onClick={props.onReattach}>Reattach here</button>
      </Show>
      <Show when={props.conn === "lost"}>
        <p class="muted">Disconnected.</p>
        <button onClick={props.onRetry}>Reconnect</button>
      </Show>
      <Show when={live() && props.status === "connecting"}>
        <p class="muted">Connecting…</p>
      </Show>
      <Show when={live() && props.status === "error"}>
        <p class="muted">Failed to start session.</p>
        <button onClick={props.onRetry}>Retry</button>
      </Show>
    </div>
  );
}

function PanePicker(props: { pane: Pane<TermData>; ctx: TermCtx }) {
  const [workload, setWorkload] = createSignal("");
  const [cmd, setCmd] = createSignal("/bin/sh");
  let selectRef!: HTMLSelectElement;
  // Focus the first control when this is the freshly-created (focused) pane, so a new
  // empty pane is immediately keyboard-ready.
  onMount(() => {
    if (props.ctx.focused() === props.pane.id) selectRef.focus();
  });
  return (
    <div class="pane-picker">
      <div class="field">
        <label>Workload</label>
        <select ref={selectRef} value={workload()} onChange={(e) => setWorkload(e.currentTarget.value)}>
          <option value="">select workload…</option>
          <For each={props.ctx.running()}>{(w) => <option value={w.name}>{w.name}</option>}</For>
        </select>
      </div>
      <div class="field">
        <label>Command</label>
        <input value={cmd()} onInput={(e) => setCmd(e.currentTarget.value)} />
      </div>
      <button class="primary" disabled={!workload()} onClick={() => props.ctx.retarget(props.pane.id, workload(), [cmd()])}>
        Connect
      </button>
    </div>
  );
}
