import { For, Show } from "solid-js";
import { A } from "@solidjs/router";
import type { Tunnel } from "../api";

// ForwardsView renders a project's networking exposure: public ngrok tunnels
// (already filtered to active + this project) and the client agent's local
// port-forwards (entries of the forwards map that belong to this project).
export default function ForwardsView(props: {
  tunnels: Tunnel[];
  forwards: [string, string[]][];
}) {
  const hasTunnels = () => props.tunnels.length > 0;
  const hasForwards = () => props.forwards.length > 0;

  return (
    <Show
      when={hasTunnels() || hasForwards()}
      fallback={<p class="muted">No tunnels or port-forwards.</p>}
    >
      <Show when={hasTunnels()}>
        <table class="grid">
          <thead>
            <tr>
              <th>Workload</th>
              <th>Public URL</th>
              <th>Port</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.tunnels}>
              {(t) => (
                <tr>
                  <td>
                    <A href={`/workloads/${encodeURIComponent(t.workload)}`}>{t.workload}</A>
                  </td>
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
      </Show>
      <Show when={hasForwards()}>
        <table class="grid">
          <thead>
            <tr>
              <th>Service</th>
              <th>Local forward</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.forwards}>
              {([svc, fwds]) => (
                <For each={fwds}>
                  {(f) => (
                    <tr>
                      <td>{svc}</td>
                      <td class="wrap">{f}</td>
                    </tr>
                  )}
                </For>
              )}
            </For>
          </tbody>
        </table>
      </Show>
    </Show>
  );
}
