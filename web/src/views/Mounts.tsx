import { For, Show } from "solid-js";
import { A } from "@solidjs/router";
import type { Mount } from "../api";

// MountTable renders a project's (or the ungrouped bucket's) mounts. Status
// semantics (derived server-side; see webMount in cmd/cornus/web.go): live = held
// by the client agent's deploy-attach session; running = workload up, mount
// realized by the backend; inactive = workload down / not created.
export default function MountTable(props: { mounts: Mount[] }) {
  return (
    <Show when={props.mounts.length} fallback={<p class="muted">No mounts.</p>}>
      <table class="grid">
        <thead>
          <tr>
            <th>Service</th>
            <th>Workload</th>
            <th>Kind</th>
            <th>Source</th>
            <th>Target</th>
            <th>Mode</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          <For each={props.mounts}>
            {(m) => (
              <tr>
                <td>{m.service}</td>
                <td>
                  <A href={`/workloads/${encodeURIComponent(m.workload)}`}>{m.workload}</A>
                </td>
                <td>{m.kind}</td>
                <td class="wrap">{m.source || <span class="muted">(anonymous)</span>}</td>
                <td class="wrap">{m.target}</td>
                <td>{m.readOnly ? "ro" : "rw"}</td>
                <td>
                  <span
                    classList={{
                      badge: true,
                      ok: m.status === "live" || m.status === "running",
                      warn: m.status === "inactive",
                    }}
                  >
                    {m.status}
                  </span>
                </td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </Show>
  );
}
