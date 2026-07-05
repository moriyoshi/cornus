import { For, Show, createSignal } from "solid-js";
import { A } from "@solidjs/router";
import {
  workloadAction,
  deleteWorkload,
  type Workload,
  type Mount,
  type Tunnel,
} from "../api";
import MountTable from "./Mounts";
import ForwardsView from "./Tunnels";
import MetricsStrip from "./metrics/MetricsStrip";
import type { Range } from "./metrics/catalog";

// WorkloadActions renders the start/stop/restart/delete controls for a single
// workload, owning the transient busy/error state. After a successful action it
// calls onChanged so the parent can refetch the shared workloads resource. Shared
// by the project-oriented table rows and the workload-oriented section headers.
export function WorkloadActions(props: { workload: Workload; onChanged: () => void }) {
  const [busy, setBusy] = createSignal(false);
  const [err, setErr] = createSignal("");
  const w = () => props.workload;

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setErr("");
    try {
      await fn();
      props.onChanged();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Show when={w().created}>
      <span class="row">
        <button
          disabled={busy()}
          onClick={() => act(() => workloadAction(w().name, w().running ? "stop" : "start"))}
        >
          {w().running ? "Stop" : "Start"}
        </button>
        <button disabled={busy()} onClick={() => act(() => workloadAction(w().name, "restart"))}>
          Restart
        </button>
        <button
          class="danger"
          disabled={busy()}
          onClick={() => {
            if (confirm(`Delete deployment ${w().name}?`)) {
              void act(() => deleteWorkload(w().name));
            }
          }}
        >
          Delete
        </button>
        <Show when={err()}>
          <span class="error">{err()}</span>
        </Show>
      </span>
    </Show>
  );
}

// WorkloadTable renders a set of workloads (already filtered to one project, or
// the ungrouped bucket). Used by the project-oriented view. It is a READING
// surface: the rows carry no controls, so a phone-width row is the workload's
// state and nothing else, and a mis-hit while scrolling a wide table sideways
// cannot stop or delete a deployment. Acting on one is a click away through its
// name — the detail page, and the by-workload grouping's section header, own the
// start/stop/restart/delete controls.
//
// No "Backend" column, and do not add one back: a server constructs its deploy
// backend once for its whole lifetime (`pkg/server/server.go` getBackend memoizes
// a single `s.backend`, built from server config alone), and every backend stamps
// its own `Name()` on every row it returns. The BFF proxies exactly one server, so
// the column repeated one identical string down the whole table on every screen it
// ever rendered. The backend is a property of the SERVER — `/config`'s
// `server.backend.name`, from `api.ServerInfo.Backend` — not of a workload.
//
// Service leads, not Name. Under a project heading the reader is looking for the
// service they wrote in compose.yaml; `<project>-<service>` is a name the tooling
// derived from it, and repeating the project prefix down the first column of a
// table already scoped to that project spends the most-read column on the one
// thing every row shares. Name keeps the link — it is what the detail page,
// logs, and exec are addressed by — it just does not lead.
export default function WorkloadTable(props: { workloads: Workload[] }) {
  return (
    <Show when={props.workloads.length} fallback={<p class="muted">No workloads.</p>}>
      <div class="table-scroll">
        <table class="grid">
          <thead>
            <tr>
              <th>Service</th>
              <th>Name</th>
              <th>Image</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.workloads}>
              {(w) => (
                <tr>
                  <td>{w.service || <span class="muted">—</span>}</td>
                  <td>
                    <A href={`/workloads/${encodeURIComponent(w.name)}`}>{w.name}</A>
                  </td>
                  <td class="wrap">{w.image}</td>
                  <td>
                    <span classList={{ badge: true, ok: w.running, warn: w.created && !w.running }}>
                      {w.summary}
                    </span>
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
    </Show>
  );
}

// WorkloadSection is one workload's slice of the dashboard: its status/actions in
// the header, then its own mounts and port-forwards. The workload-oriented view's
// counterpart to ProjectSection.
export function WorkloadSection(props: {
  workload: Workload;
  mounts: Mount[];
  tunnels: Tunnel[];
  forwards: [string, string[]][];
  onChanged: () => void;
  // metrics is the observability window the page is on, or undefined when the
  // server has no store — see ProjectSection, which takes the same prop.
  metrics?: { range: Range; tick: number };
}) {
  const w = () => props.workload;
  return (
    <section id={`workload-${w().name}`} class="section">
      <div class="row">
        <h2 style={{ margin: 0 }}>
          <A href={`/workloads/${encodeURIComponent(w().name)}`}>{w().name}</A>
        </h2>
        <span classList={{ badge: true, ok: w().running, warn: w().created && !w().running }}>
          {w().summary}
        </span>
        <Show when={w().service || w().project}>
          <span class="muted">
            {w().service || w().project}
            {w().service && w().project ? ` · ${w().project}` : ""}
          </span>
        </Show>
        <Show when={w().origin?.user || w().origin?.host}>
          <span class="muted" title="Deployed by">
            {[w().origin!.user, w().origin!.host].filter(Boolean).join("@")}
          </span>
        </Show>
        <WorkloadActions workload={w()} onChanged={props.onChanged} />
      </div>

      <Show when={props.metrics}>
        {(m) => (
          <MetricsStrip
            services={[w().name]}
            range={m().range}
            tick={m().tick}
            to={`/metrics?workload=${encodeURIComponent(w().name)}`}
          />
        )}
      </Show>

      {/* scope="workload": the header above already pairs this deployment with its
          compose service, and the caller has filtered every list here to that one
          deployment — so neither table repeats the pairing per row. */}
      <h3>Mounts</h3>
      <MountTable mounts={props.mounts} scope="workload" />

      <h3>Port-forwards</h3>
      <ForwardsView tunnels={props.tunnels} forwards={props.forwards} scope="workload" />
    </section>
  );
}
