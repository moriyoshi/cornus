import { For, Show, createSignal, onCleanup, createEffect } from "solid-js";
import { useNavigate, useParams } from "@solidjs/router";
import {
  ApiError,
  getObsStatus,
  getWorkload,
  workloadAction,
  startTunnel,
  stopTunnel,
  wsURL,
} from "../api";
import { pollResource } from "../poll";
import MetricPanel from "./metrics/MetricPanel";
import RangeSelect from "./metrics/RangeSelect";
import { StoreOffCard } from "./Metrics";
import { createMetricsClock } from "./metrics/clock";
import { DEFAULT_RANGE, panelsFor, rangeById } from "./metrics/catalog";

// The detail page is one deployment's whole story, laid out the way the Overview
// lays out a project: every part on the page at once, one section each, in the
// order a reader asks about them — what is running, what it was asked to be, how
// it has been behaving, what it is saying. Tabs used to hide three of those four
// behind the one you were not on, which is the wrong trade on the page you open
// BECAUSE something looks wrong: the instance that just died and the log line
// that says why were never visible together.
//
// Exec is the one thing that stayed a control rather than becoming a section. A
// section renders on arrival, and rendering a terminal means spawning a shell in
// the container — a side effect nobody asked for by opening a page. It is a CTA
// into the Terminal workspace instead, where a session is the point.
export default function WorkloadDetail() {
  const params = useParams();
  const navigate = useNavigate();
  const name = () => decodeURIComponent(params.name ?? "");
  const [detail, { refetch }] = pollResource(() => getWorkload(name()), 4000);
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
              {/* Opening a shell is a session, not a view: it belongs where sessions
                  are kept, split, and reattached after a reload. */}
              <button
                class="primary"
                title="Open a terminal on this workload in the Workspace"
                onClick={() => navigate(`/workspace?workload=${encodeURIComponent(name())}`)}
              >
                Exec
              </button>
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

            <section id="instances" class="section">
              <h2>Instances</h2>
              <Show when={d().status?.instances?.length} fallback={<p class="muted">No instances.</p>}>
                <div class="table-scroll">
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
                              <span
                                classList={{ badge: true, ok: !!inst.running, warn: !inst.running }}
                              >
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
                </div>
              </Show>
            </section>

            <section id="spec" class="section">
              <h2>Spec</h2>
              <Show when={d().spec} fallback={<p class="muted">Spec unknown (not part of the loaded project).</p>}>
                <pre class="log">{JSON.stringify(d().spec, null, 2)}</pre>
              </Show>
            </section>

            <section id="metrics" class="section">
              <h2>Metrics</h2>
              <WorkloadMetrics workload={name()} />
            </section>

            <section id="logs" class="section">
              <h2>Logs</h2>
              <LogStream workload={name()} />
            </section>
          </>
        )}
      </Show>
    </>
  );
}

// WorkloadMetrics is the full workload panel set, scoped to this one deployment.
//
// The detail page is where a reader has already narrowed to one thing, so this is
// the whole set rather than the two-panel strip the Overview sections carry — and
// it gets its own range control, since it is the only thing on the tab and
// therefore scopes all of it.
function WorkloadMetrics(props: { workload: string }) {
  const [rangeId, setRangeId] = createSignal(DEFAULT_RANGE.id);
  const range = () => rangeById(rangeId());
  const tick = createMetricsClock(range);
  const [obs] = pollResource(getObsStatus, 60000);

  const storeOff = () => obs.error instanceof ApiError && obs.error.status === 501;
  const otherError = () => (obs.error && !storeOff() ? String(obs.error) : "");
  // The scope is a one-element set rather than "everything": a workload page must
  // never show another workload's numbers, not even for the seconds between the
  // deployment being created and the sampler first reaching it.
  const services = () => [props.workload];

  return (
    <>
      <Show when={storeOff()}>
        <StoreOffCard compact />
      </Show>
      <Show when={otherError()}>
        <p class="error">{otherError()}</p>
      </Show>
      <Show when={!storeOff()}>
        <div class="filters">
          <RangeSelect value={rangeId()} onChange={setRangeId} />
        </div>
        <div class="panels">
          <For each={panelsFor("workloads")}>
            {(panel) => (
              <MetricPanel panel={panel} range={range()} tick={tick()} services={services()} />
            )}
          </For>
        </div>
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
