import { For, Show } from "solid-js";
import { A } from "@solidjs/router";
import type { Mount } from "../api";

// MountTable renders a project's (or the ungrouped bucket's) mounts. Status
// semantics (derived server-side; see webMount in cmd/cornus/web.go): live = held
// by the client agent's deploy-attach session; running = workload up, mount
// realized by the backend; inactive = workload down / not created.
//
// scope says what the enclosing heading already names, and it is REQUIRED so a new
// section has to answer the question rather than inherit an answer. Under a project
// heading ("project") the rows span many workloads, so Service leads and Workload
// follows. Under one workload's heading ("workload") both are constant down the
// whole table — the caller has filtered to a single deployment, which realizes a
// single compose service — and the section header states the pairing once. Two
// columns of the same string are not identification, they are noise in the two
// most-read positions, and they push Source and Target (the reason to open a mount
// table at all) toward the horizontal scroll.
export default function MountTable(props: { mounts: Mount[]; scope: "project" | "workload" }) {
  const identifies = () => props.scope === "project";
  return (
    <Show when={props.mounts.length} fallback={<p class="muted">No mounts.</p>}>
      <div class="table-scroll">
        <table class="grid">
          <thead>
            <tr>
              <Show when={identifies()}>
                <th>Service</th>
                <th>Workload</th>
              </Show>
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
                  <Show when={identifies()}>
                    <td>{m.service}</td>
                    <td>
                      <A href={`/workloads/${encodeURIComponent(m.workload)}`}>{m.workload}</A>
                    </td>
                  </Show>
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
      </div>
    </Show>
  );
}
