import { For, Show, createSignal, onCleanup, createEffect } from "solid-js";
import { useParams } from "@solidjs/router";
import {
  getWorkload,
  workloadAction,
  startTunnel,
  stopTunnel,
  wsURL,
} from "../api";
import { pollResource } from "../poll";
import Term from "../components/Term";

type Tab = "instances" | "spec" | "logs" | "exec";

export default function WorkloadDetail() {
  const params = useParams();
  const name = () => decodeURIComponent(params.name ?? "");
  const [detail, { refetch }] = pollResource(() => getWorkload(name()), 4000);
  const [tab, setTab] = createSignal<Tab>("instances");
  const [err, setErr] = createSignal("");
  const [tunnelPort, setTunnelPort] = createSignal("");
  const [tunnelToken, setTunnelToken] = createSignal("");

  const run = async (fn: () => Promise<unknown>) => {
    setErr("");
    try {
      await fn();
      await refetch();
    } catch (e) {
      setErr(String(e));
    }
  };

  return (
    <>
      <h1>{name()}</h1>
      <Show when={err()}>
        <p class="error">{err()}</p>
      </Show>
      <Show when={detail()} fallback={<p class="muted">loading…</p>}>
        {(d) => (
          <>
            <div class="row" style={{ "margin-bottom": "12px" }}>
              <Show when={d().service}>
                <span class="badge">
                  {d().project}/{d().service}
                </span>
              </Show>
              <Show when={d().status}>
                <button onClick={() => run(() => workloadAction(name(), "start"))}>Start</button>
                <button onClick={() => run(() => workloadAction(name(), "stop"))}>Stop</button>
                <button onClick={() => run(() => workloadAction(name(), "restart"))}>Restart</button>
              </Show>
            </div>

            <div class="card" style={{ "margin-bottom": "16px" }}>
              <h3>Tunnel</h3>
              <Show
                when={d().tunnel?.active}
                fallback={
                  <div class="row">
                    <input
                      placeholder="port"
                      size="6"
                      value={tunnelPort()}
                      onInput={(e) => setTunnelPort(e.currentTarget.value)}
                    />
                    <input
                      placeholder="auth token (backend-specific, optional)"
                      size="32"
                      value={tunnelToken()}
                      onInput={(e) => setTunnelToken(e.currentTarget.value)}
                    />
                    <button
                      class="primary"
                      onClick={() =>
                        run(() =>
                          startTunnel(name(), {
                            port: parseInt(tunnelPort(), 10) || undefined,
                            authToken: tunnelToken() || undefined,
                          }),
                        )
                      }
                    >
                      Open tunnel
                    </button>
                  </div>
                }
              >
                <div class="row">
                  <span class="badge ok">active</span>
                  <a href={d().tunnel!.url} target="_blank" rel="noreferrer">
                    {d().tunnel!.url}
                  </a>
                  <span class="muted">→ :{d().tunnel!.port}</span>
                  <button class="danger" onClick={() => run(() => stopTunnel(name()))}>
                    Close
                  </button>
                </div>
              </Show>
            </div>

            <Show when={d().status?.origin}>
              {(o) => (
                <div class="card" style={{ "margin-bottom": "16px" }}>
                  <h3>Lineage</h3>
                  <dl class="kv">
                    <Show when={o().project}>
                      <dt>Project</dt>
                      <dd>{o().project}</dd>
                    </Show>
                    <Show when={o().user || o().host}>
                      <dt>Deployed by</dt>
                      <dd>{[o().user, o().host].filter(Boolean).join("@")}</dd>
                    </Show>
                    <Show when={o().directory}>
                      <dt>Directory</dt>
                      <dd class="wrap">{o().directory}</dd>
                    </Show>
                    <Show when={o().subject}>
                      <dt>Authenticated</dt>
                      <dd>{o().subject}</dd>
                    </Show>
                    <Show when={o().git}>
                      {(g) => (
                        <>
                          <dt>Git</dt>
                          <dd class="wrap">
                            <Show when={g().remote}>{g().remote} </Show>
                            <Show when={g().branch}>
                              <span class="badge">{g().branch}</span>{" "}
                            </Show>
                            <Show when={g().commit}>
                              <code>{g().commit!.slice(0, 7)}</code>
                            </Show>
                            <Show when={g().dirty}>
                              {" "}
                              <span class="badge warn">dirty</span>
                            </Show>
                          </dd>
                        </>
                      )}
                    </Show>
                  </dl>
                </div>
              )}
            </Show>

            <div class="row" style={{ "margin-bottom": "12px" }}>
              <For each={["instances", "spec", "logs", "exec"] as Tab[]}>
                {(t) => (
                  <button classList={{ primary: tab() === t }} onClick={() => setTab(t)}>
                    {t}
                  </button>
                )}
              </For>
            </div>

            <Show when={tab() === "instances"}>
              <Show when={d().status?.instances?.length} fallback={<p class="muted">No instances.</p>}>
                <table class="grid">
                  <thead>
                    <tr>
                      <th>ID</th>
                      <th>State</th>
                      <th>Health</th>
                      <th>Exit code</th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={d().status!.instances}>
                      {(inst) => (
                        <tr>
                          <td class="wrap">{inst.id}</td>
                          <td>
                            <span classList={{ badge: true, ok: !!inst.running, warn: !inst.running }}>
                              {inst.state}
                            </span>
                          </td>
                          <td>{inst.health || <span class="muted">—</span>}</td>
                          <td>{inst.exitCode ?? <span class="muted">—</span>}</td>
                        </tr>
                      )}
                    </For>
                  </tbody>
                </table>
              </Show>
            </Show>

            <Show when={tab() === "spec"}>
              <Show when={d().spec} fallback={<p class="muted">Spec unknown (not part of the loaded project).</p>}>
                <pre class="log">{JSON.stringify(d().spec, null, 2)}</pre>
              </Show>
            </Show>

            <Show when={tab() === "logs"}>
              <LogStream workload={name()} />
            </Show>

            <Show when={tab() === "exec"}>
              <Term workload={name()} />
            </Show>
          </>
        )}
      </Show>
    </>
  );
}

// LogStream follows the workload's logs over the BFF WebSocket into a <pre>.
function LogStream(props: { workload: string }) {
  const [lines, setLines] = createSignal("");
  let pre!: HTMLPreElement;

  createEffect(() => {
    setLines("");
    const sock = new WebSocket(
      wsURL(`/workloads/${encodeURIComponent(props.workload)}/logs?follow=true&tail=500`),
    );
    sock.binaryType = "arraybuffer";
    const dec = new TextDecoder();
    sock.onmessage = (ev) => {
      setLines((prev) => {
        // Cap the buffer so a chatty workload cannot grow the DOM unbounded.
        const next = prev + dec.decode(ev.data as ArrayBuffer, { stream: true });
        return next.length > 512 * 1024 ? next.slice(-384 * 1024) : next;
      });
      queueMicrotask(() => {
        pre.scrollTop = pre.scrollHeight;
      });
    };
    sock.onclose = (ev) => {
      if (ev.reason) setLines((prev) => prev + `\n[stream closed: ${ev.reason}]\n`);
    };
    onCleanup(() => sock.close());
  });

  return (
    <pre class="log" ref={pre}>
      {lines() || "waiting for logs…"}
    </pre>
  );
}
