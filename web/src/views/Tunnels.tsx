import { For, Show } from "solid-js";
import { A } from "@solidjs/router";
import type { Tunnel } from "../api";

// ForwardsView renders a project's networking exposure: public ngrok tunnels
// (already filtered to active + this project) and the client agent's local
// port-forwards (entries of the forwards map that belong to this project).
//
// Under a project heading (scope="project") both tables lead with the compose
// service, like every other table in a project section: that is the name the reader
// wrote and the only one the two tables share — a forward is keyed by service and a
// tunnel by deployment resource, so without it the two halves of one heading could
// not be read against each other.
//
// Under one workload's heading (scope="workload") that shared key is the heading
// itself. The caller has filtered to a single deployment and its single service, so
// Service and Workload would repeat one pairing down every row of both tables; the
// section header states it once. What is left is what differs per row: the public
// URL and port, and the local forward. See MountTable, which takes the same prop
// for the same reason — scope is required there and here.
export default function ForwardsView(props: {
  tunnels: Tunnel[];
  forwards: [string, string[]][];
  scope: "project" | "workload";
}) {
  const hasTunnels = () => props.tunnels.length > 0;
  const hasForwards = () => props.forwards.length > 0;
  const identifies = () => props.scope === "project";

  return (
    <Show
      when={hasTunnels() || hasForwards()}
      fallback={<p class="muted">No tunnels or port-forwards.</p>}
    >
      <Show when={hasTunnels()}>
        <div class="table-scroll">
          <table class="grid">
            <thead>
              <tr>
                <Show when={identifies()}>
                  <th>Service</th>
                  <th>Workload</th>
                </Show>
                <th>Public URL</th>
                <th>Port</th>
              </tr>
            </thead>
            <tbody>
              <For each={props.tunnels}>
                {(t) => (
                  <tr>
                    <Show when={identifies()}>
                      <td>{t.service || <span class="muted">—</span>}</td>
                      <td>
                        <A href={`/workloads/${encodeURIComponent(t.workload)}`}>{t.workload}</A>
                      </td>
                    </Show>
                    <td class="wrap">
                      <a href={t.url} target="_blank" rel="noreferrer">
                        {t.url}
                      </a>
                    </td>
                    <td>{t.port}</td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
      <Show when={hasForwards()}>
        <div class="table-scroll">
          <table class="grid">
            <thead>
              <tr>
                <Show when={identifies()}>
                  <th>Service</th>
                </Show>
                <th>Local forward</th>
              </tr>
            </thead>
            <tbody>
              <For each={props.forwards}>
                {([svc, fwds]) => (
                  <For each={fwds}>
                    {(f) => (
                      <tr>
                        <Show when={identifies()}>
                          <td>{svc}</td>
                        </Show>
                        <td class="wrap">{f}</td>
                      </tr>
                    )}
                  </For>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
    </Show>
  );
}
