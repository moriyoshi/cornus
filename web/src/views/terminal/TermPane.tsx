import { Show, For, createSignal, createEffect, onCleanup, onMount } from "solid-js";
import type { Accessor } from "solid-js";
import Term from "../../components/Term";
import type { Workload } from "../../api";
import { createTerminal, createTerminalFromLine, discoverShells } from "../../api";
import { settings } from "../../settings";
import type { Pane } from "../tiling/layout";
import { parseCandidates } from "./shells";
import { paneExitAction } from "./reconnect";

// TermPane is the body of one terminal tile in the tiled workspace: the per-pane
// session lifecycle (create / attach / reconnect) plus the empty-pane workload picker
// and the connecting/lost/elsewhere status. The surrounding frame — tab bar, drag,
// split edges — is the generic tiling chrome (views/tiling/panes.tsx); this is only the
// content the chrome renders for a pane. Stacked panes stay mounted (the chrome hides
// inactive tabs with display:none), so background sessions keep running.

// TermData is a terminal pane's durable payload, carried through splits/moves/stacking
// and persisted: its target workload, command, and the BFF session id once created.
//
// An EMPTY cmd means "discover one": the pane asks the BFF which shells the image
// actually has and adopts the best. The answer is written back here, so a reload
// reattaches without probing again, and a split inherits the shell that worked
// rather than repeating the search.
//
// `kind` is what tells it apart from a FILE pane's payload in the same tree — see the
// twin comment on FileData in views/files/FilePane.tsx.
//
// `dir` is where the shell starts, absolute inside the container. It exists because a
// terminal in this workspace is usually opened AT somewhere: "Open in a terminal" reads
// the folder the file browser is showing and hands it over. Stored rather than derived
// so a split inherits the same place, and it is read only at creation — a reattach picks
// up a session whose cwd is whatever the user has since cd'd to, which is theirs to
// decide. Empty means "wherever the image puts you", which is also what a kubernetes
// backend gives you regardless, since PodExecOptions cannot express a working directory.
export interface TermData {
  kind: "term";
  workload: string;
  cmd: string[];
  sessionId?: string;
  dir?: string;
}

// TermCtx is the slice of the workspace a pane body needs.
export interface TermCtx {
  focused: Accessor<string>;
  setSession: (id: string, sessionId: string) => void;
  retarget: (id: string, workload: string, cmd: string[]) => void;
  // adopt records a session the pane created ITSELF, target and command and id in
  // one commit. retarget + setSession cannot express this: retarget clears the
  // session id, so the pane would briefly hold a command with no session and open a
  // second one before setSession landed.
  adopt: (id: string, workload: string, cmd: string[], sessionId: string) => void;
  closePane: (id: string) => void;
  running: Accessor<Workload[]>;
}

// A pane's session lifecycle past "live": elsewhere (another tab took it over) or lost
// (an unexpected drop we couldn't silently recover — offer Reconnect).
type PaneConn = "live" | "elsewhere" | "lost";

export default function TermPane(props: { pane: Pane<TermData>; ctx: TermCtx }) {
  const [status, setStatus] = createSignal<"idle" | "probing" | "connecting" | "error">("idle");
  const [error, setError] = createSignal("");
  // conn is the session lifecycle past "live"; reconnectKey is a nonce whose bump
  // forces the keyed <Show> to remount <Term>, i.e. reattach the socket.
  const [conn, setConn] = createSignal<PaneConn>("live");
  const [reconnectKey, setReconnectKey] = createSignal(0);
  // noShell means discovery ran and found nothing: the image has no interactive
  // shell at any path we know. The pane then asks for a command rather than
  // failing, which is the whole point of probing instead of guessing /bin/sh.
  const [noShell, setNoShell] = createSignal(false);
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

  // When a pane has a target but no live session, get one. Guarded so the effect
  // re-running never opens duplicate sessions; a new target resets reconnect state.
  //
  // An empty cmd means the shell has not been decided yet: probe first, write the
  // winner back through retarget (which re-enters this effect and creates the
  // session), and fall through to the picker when the image has none. Discovery
  // lives HERE rather than in the picker so every way into a pane — picking a
  // workload, a /terminal?workload= deep link, a split that inherits a target —
  // resolves identically.
  createEffect(() => {
    const p = props.pane;
    setConn("live");
    setReconnectKey(0);
    setNoShell(false);
    failures = 0;
    clearTimers();
    if (!p.data.workload || p.data.sessionId || creating) return;

    if (p.data.cmd.length === 0) {
      creating = true;
      setStatus("probing");
      discoverShells(p.data.workload, parseCandidates(settings().shellCandidates))
        .then((r) => {
          // Clear the guard BEFORE retargeting, not in a .finally: retarget commits
          // the discovered command, which re-enters this effect to open the session.
          // With the guard still set that re-entry returns early and the pane sits
          // on a shell it found and never connected to.
          creating = false;
          if (r.found.length > 0) {
            setStatus("idle");
            props.ctx.retarget(p.id, p.data.workload, r.found[0]);
            return;
          }
          setNoShell(true);
          setStatus("idle");
        })
        .catch((e) => {
          creating = false;
          setError(e instanceof Error ? e.message : String(e));
          setStatus("error");
        });
      return;
    }

    creating = true;
    setStatus("connecting");
    createTerminal(p.data.workload, p.data.cmd, p.data.dir)
      .then((s) => {
        props.ctx.setSession(p.id, s.id);
        setStatus("idle");
      })
      .catch((e) => {
        setError(e instanceof Error ? e.message : String(e));
        setStatus("error");
      })
      .finally(() => {
        creating = false;
      });
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
    <Show
      when={d().workload && !noShell()}
      fallback={<PanePicker pane={props.pane} ctx={props.ctx} noShell={noShell()} />}
    >
      <Show
        when={liveSession()}
        keyed
        fallback={
          <PaneStatus
            status={status()}
            conn={conn()}
            error={error()}
            onReattach={reattachHere}
            onRetry={() => props.ctx.retarget(props.pane.id, d().workload, d().cmd)}
          />
        }
      >
        {(_key) => (
          <Term
            sessionId={d().sessionId}
            paneId={props.pane.id}
            // Same guard the picker below uses: a pane takes the keyboard on mount only if
            // it is the focused one. Read at mount, which is when it is asked.
            autoFocus={props.ctx.focused() === props.pane.id}
            onOpen={onTermOpen}
            onExit={onTermExit}
          />
        )}
      </Show>
    </Show>
  );
}

function PaneStatus(props: {
  status: "idle" | "probing" | "connecting" | "error";
  conn: PaneConn;
  error: string;
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
      {/* Probing runs one exec per candidate the image does NOT have, so on a
          distroless image it is a visible pause. Say what is happening rather than
          leaving the pane blank. */}
      <Show when={live() && props.status === "probing"}>
        <p class="muted">Looking for a shell…</p>
      </Show>
      <Show when={live() && props.status === "connecting"}>
        <p class="muted">Connecting…</p>
      </Show>
      <Show when={live() && props.status === "error"}>
        <p class="muted">Failed to start session.</p>
        <Show when={props.error}>
          <p class="error">{props.error}</p>
        </Show>
        <button onClick={props.onRetry}>Retry</button>
      </Show>
    </div>
  );
}

// PanePicker asks the two questions an unstarted pane still has. It has two modes,
// which differ only in what the Command field means:
//
//   - a fresh pane: leave Command empty and the shell is DISCOVERED (the BFF probes
//     the image and connects to the best one it has). Typing one overrides that.
//   - noShell: discovery already ran on this workload and came back empty, so there
//     is nothing to fall back to and a command is required.
function PanePicker(props: { pane: Pane<TermData>; ctx: TermCtx; noShell: boolean }) {
  const [workload, setWorkload] = createSignal(props.pane.data.workload);
  const [cmd, setCmd] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [failed, setFailed] = createSignal("");
  let selectRef!: HTMLSelectElement;
  // Focus the first control when this is the freshly-created (focused) pane, so a new
  // empty pane is immediately keyboard-ready.
  onMount(() => {
    if (props.ctx.focused() === props.pane.id) selectRef.focus();
  });

  const ready = () => !!workload() && (!props.noShell || cmd().trim() !== "") && !busy();

  const connect = () => {
    if (!ready()) return;
    const line = cmd().trim();
    if (line === "") {
      // Empty means "discover": hand the pane a target with no command and let the
      // create effect probe.
      props.ctx.retarget(props.pane.id, workload(), []);
      return;
    }
    // A typed command is sent as ONE STRING so the BFF splits it the same way it
    // splits every other candidate — `/bin/busybox sh` has to work here, and it
    // cannot if the browser wraps it into a single argv element. The response
    // carries the split argv back, which is what the pane then remembers.
    setBusy(true);
    setFailed("");
    createTerminalFromLine(workload(), line, props.pane.data.dir)
      .then((s) => props.ctx.adopt(props.pane.id, workload(), s.cmd, s.id))
      .catch((e) => setFailed(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div class="pane-picker">
      <Show when={props.noShell}>
        <p class="warn">
          No shell found in this image. Enter a command to run.
        </p>
      </Show>
      {/* The labels are bound with for/id rather than left adjacent: in the no-shell
          case the Command field is REQUIRED, and a required control whose label is
          only visually near it is one a screen reader announces unlabelled. */}
      <div class="field">
        <label for={`pane-workload-${props.pane.id}`}>Workload</label>
        <select
          id={`pane-workload-${props.pane.id}`}
          ref={selectRef}
          value={workload()}
          onChange={(e) => setWorkload(e.currentTarget.value)}
        >
          <option value="">select workload…</option>
          <For each={props.ctx.running()}>{(w) => <option value={w.name}>{w.name}</option>}</For>
        </select>
      </div>
      <div class="field">
        <label for={`pane-cmd-${props.pane.id}`}>Command</label>
        <input
          id={`pane-cmd-${props.pane.id}`}
          value={cmd()}
          placeholder={props.noShell ? "" : "auto"}
          onInput={(e) => setCmd(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") connect();
          }}
        />
      </div>
      <Show when={failed()}>
        <p class="error">{failed()}</p>
      </Show>
      <button class="primary" disabled={!ready()} onClick={connect}>
        Connect
      </button>
    </div>
  );
}
