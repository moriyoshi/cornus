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
// the ungrouped bucket) with per-row actions. Used by the project-oriented view.
export default function WorkloadTable(props: { workloads: Workload[]; onChanged: () => void }) {
  return (
    <Show when={props.workloads.length} fallback={<p class="muted">No workloads.</p>}>
      <table class="grid">
        <thead>
          <tr>
            <th>Name</th>
            <th>Service</th>
            <th>Image</th>
            <th>Backend</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <For each={props.workloads}>
            {(w) => (
              <tr>
                <td>
                  <A href={`/workloads/${encodeURIComponent(w.name)}`}>{w.name}</A>
                </td>
                <td>{w.service || <span class="muted">—</span>}</td>
                <td class="wrap">{w.image}</td>
                <td>{w.backend || <span class="muted">—</span>}</td>
                <td>
                  <span classList={{ badge: true, ok: w.running, warn: w.created && !w.running }}>
                    {w.summary}
                  </span>
                </td>
                <td>
                  <WorkloadActions workload={w} onChanged={props.onChanged} />
                </td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
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
}) {
  const w = () => props.workload;
  return (
    <section id={`workload-${w().name}`} class="project">
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

      <h3>Mounts</h3>
      <MountTable mounts={props.mounts} />

      <h3>Port-forwards</h3>
      <ForwardsView tunnels={props.tunnels} forwards={props.forwards} />
    </section>
  );
}
