import { For, Show, createMemo } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { ApiError, getObsStatus, getWorkloads, type ObsStatus } from "../api";
import { pollResource } from "../poll";
import MetricPanel from "./metrics/MetricPanel";
import RangeSelect from "./metrics/RangeSelect";
import { createMetricsClock } from "./metrics/clock";
import { DEFAULT_RANGE, panelsFor, rangeById, type Scope } from "./metrics/catalog";

// The metrics dashboard: what the built-in observability store has recorded about
// the workloads cornus deploys, and about the cornus server itself.
//
// The store is optional. A server started without `--obs` answers 501 on every
// route behind this screen, and that is a configuration fact rather than a
// failure — so it renders as an explanation carrying the command that fixes it,
// not as an error.
//
// The scope switch rides with the page title, and the filter row below it holds
// only what narrows the chosen scope. That split is the drill-down order: scope
// decides WHICH panels exist (and whether a workload filter exists at all), the
// filters decide which slice of them is shown. One clock drives every panel, so
// two panels never describe two different moments. The panels themselves are the
// same component the project sections and the workload detail view mount.

export default function Metrics() {
  // The filter state lives in the URL rather than in signals, so a link can
  // arrive pre-scoped ("All metrics →" from a workload section) and so a reader
  // who found something can send someone else the same view. `rangeById` and the
  // scope check fold an unknown or hand-edited value back to the default instead
  // of rendering an empty screen.
  const [params, setParams] = useSearchParams();
  const scope = (): Scope => (params.scope === "server" ? "server" : "workloads");
  const setScope = (s: Scope) => setParams({ scope: s === "workloads" ? undefined : s });
  const rangeId = () => rangeById(String(params.range ?? "")).id;
  const setRangeId = (id: string) => setParams({ range: id === DEFAULT_RANGE.id ? undefined : id });
  const service = () => (typeof params.workload === "string" ? params.workload : "");
  const setService = (name: string) => setParams({ workload: name || undefined });
  const range = () => rangeById(rangeId());
  const tick = createMetricsClock(range);

  const [status] = pollResource(getObsStatus, 30000);
  const [workloads] = pollResource(getWorkloads, 10000);

  // A resource accessor RE-THROWS its error when read, so every read here is
  // guarded by .error first. storeOff is the one failure that is really a
  // setting; anything else — an unreachable server, a broken profile — stays an
  // error, because it is one.
  const storeOff = () => status.error instanceof ApiError && status.error.status === 501;
  const statusError = () => (status.error && !storeOff() ? String(status.error) : "");
  const storeStatus = (): ObsStatus | undefined => (status.error ? undefined : status());
  // The workload list always CONTAINS the current selection, even when the
  // workload list has not arrived yet or no longer holds it. Two reasons, and the
  // first is not cosmetic: a `<select>` cannot display a value that has no
  // matching `<option>`, so arriving at `?workload=shop-db` before the list loads
  // would leave the control reading "All workloads" while the charts were in fact
  // filtered — the control lying about the page. The second is that a filter
  // naming a deleted deployment should say so by staying selected, rather than
  // silently widening to everything.
  const workloadNames = createMemo(() => {
    const names = new Set(workloads.error ? [] : (workloads() ?? []).map((w) => w.name));
    if (service()) names.add(service());
    return [...names].sort((a, b) => a.localeCompare(b));
  });

  const panels = createMemo(() => panelsFor(scope()));
  // Absent scopes to every service; a chosen workload scopes to just that one.
  const services = () => (scope() === "workloads" && service() ? [service()] : undefined);

  return (
    <>
      {/* The scope switch belongs to the TITLE, not to the filter row. It decides
          which panels exist at all — and, with them, whether there is a workload
          filter to use — so putting it beside that filter made a control and the
          thing it governs peers in one row: three equal-looking filters, one of
          which appeared and vanished for no reason a reader could see from the
          row itself. Drill-down reads outside-in, so the control that governs the
          screen sits above and outside everything it drives.

          Conditional on the store, like the filter row: a server without `--obs`
          has no panels in either scope, and a switch between two empty screens is
          a choice with no consequence. The title itself stays, so the page is
          still identifiable while it explains itself. */}
      <div class={"row page-head" + (storeOff() ? "" : " page-head-containing-switch")}>
        <h1>Metrics</h1>
        <Show when={!storeOff()}>
          <div class="seg" role="group" aria-label="Scope">
            <button
              classList={{ active: scope() === "workloads" }}
              onClick={() => setScope("workloads")}
            >
              Workloads
            </button>
            <button classList={{ active: scope() === "server" }} onClick={() => setScope("server")}>
              Server
            </button>
          </div>
        </Show>
      </div>

      <Show when={storeOff()}>
        <StoreOffCard />
      </Show>

      <Show when={statusError()}>
        <p class="error">{statusError()}</p>
      </Show>

      <Show when={!storeOff()}>
        {/* One filter row, above everything it scopes and below the scope switch
            that decides what there is to scope. Every control in it NARROWS the
            chosen scope; none of them changes what the others are. Range first:
            it is the control every reader reaches for, and the only one present
            in both scopes. */}
        <div class="filters">
          <RangeSelect value={rangeId()} onChange={setRangeId} />

          <Show when={scope() === "workloads"}>
            <label class="field">
              <span>Workload</span>
              <select value={service()} onChange={(e) => setService(e.currentTarget.value)}>
                <option value="">All workloads</option>
                <For each={workloadNames()}>{(s) => <option value={s}>{s}</option>}</For>
              </select>
            </label>
          </Show>
        </div>

        <StoreNote status={storeStatus()} />

        <div class="panels">
          <For each={panels()}>
            {(panel) => (
              <MetricPanel
                panel={panel}
                range={range()}
                tick={tick()}
                services={panel.scope === "workloads" ? services() : undefined}
              />
            )}
          </For>
        </div>
      </Show>
    </>
  );
}

// StoreOffCard is what a server without `--obs` gets: the reason and the remedy.
// Exported because the workload detail view shows the same thing in its Metrics
// tab, and two spellings of one configuration fact would be two things to keep
// true.
export function StoreOffCard(props: { compact?: boolean }) {
  return (
    <div class="card">
      <h3>No observability store on this server</h3>
      <p class="muted">
        The server this UI is connected to was started without the built-in observability store,
        so it records nothing and the routes behind this screen answer <code>501</code>. Restart
        it with <code>--obs</code> to turn recording on:
      </p>
      <pre class="log">cornus serve --obs</pre>
      <Show when={!props.compact}>
        <p class="muted">
          Workload CPU, memory, network, disk, and process counts are then sampled for you — the
          workloads themselves need no OpenTelemetry SDK and no configuration.
        </p>
      </Show>
    </div>
  );
}

// StoreNote surfaces the numbers that decide whether an empty panel means
// "nothing happened" or "the evidence was thrown away". A reader who does not
// know the store shed under load will read a gap as a quiet system — which is why
// `cornus observe status` prints the same counters.
function StoreNote(props: { status?: ObsStatus }) {
  // Retention and the sampler interval are Go durations on the wire: nanoseconds.
  const retentionHours = () => Math.round((props.status?.retention ?? 0) / 3.6e12);
  const sampler = () => props.status?.metrics;
  const dropped = () => (props.status?.dropped ?? 0) + (sampler()?.dropped ?? 0);
  return (
    <Show when={props.status}>
      <p class="store-note muted">
        <Show when={retentionHours() > 0}>Retaining {retentionHours()}h of telemetry. </Show>
        <Show when={sampler()}>
          {(m) => (
            <>
              Sampling {m().replicas} replica{m().replicas === 1 ? "" : "s"} every{" "}
              {Math.round(m().interval / 1e9)}s.{" "}
            </>
          )}
        </Show>
        <Show when={dropped() > 0}>
          <span class="badge warn">
            {dropped()} records dropped — a gap below may be shed data rather than an idle workload
          </span>{" "}
        </Show>
        <Show when={(sampler()?.failed ?? 0) > 0}>
          <span class="badge bad">
            {sampler()!.failed} sampling failures — the deploy backend is refusing readings
          </span>
        </Show>
      </p>
    </Show>
  );
}
