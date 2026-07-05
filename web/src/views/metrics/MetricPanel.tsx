import { For, Show, createMemo, createResource, createSignal } from "solid-js";
import { ApiError, getMetrics, type MetricSeries } from "../../api";
import TimeSeriesChart from "../../components/TimeSeriesChart";
import { metricNames, type Panel, type Range, type Source } from "./catalog";
import {
  assignSlots,
  buildSeries,
  capSeries,
  formatValue,
  scopedServices,
  summarize,
  type ChartSeries,
} from "./series";

// One chart panel: a metric over the shared window, for a chosen set of services.
//
// This is the only place a metric is turned into a rendered panel. The Metrics
// dashboard, the project sections, and the workload detail view all mount it, so
// a chart means the same thing wherever it appears — same empty states, same
// counter handling, same eight-color cap, same table view.

type View = "chart" | "table";

export interface MetricPanelProps {
  panel: Panel;
  range: Range;
  // tick advances when the OWNER decides it is time to re-read. It is a prop
  // rather than a per-panel timer so every panel on a page refetches together:
  // panels drifting apart would let a reader compare two different moments.
  tick: number;
  // services scopes the panel. Absent means every service the store knows;
  // present (even empty) restricts it — see BuildOptions.services.
  services?: string[];
  // compact is the strip form used inside a project or workload section: a
  // shorter plot and no explanatory hint, because the surrounding section has
  // already said what it is about.
  compact?: boolean;
}

// fetchSource reads one source, translating the store's "metric is not resolved"
// 400 into an ABSENCE rather than an error.
//
// That translation is per-source on purpose: a panel that merges two backend
// spellings of the same quantity has one unresolved source by design on every
// server, and letting that fail the panel would blank the chart that does have
// data. Any other failure still propagates — a broken query or an unreachable
// server is not an absence.
async function fetchSource(
  source: Source,
  range: Range,
): Promise<{ source: Source; rows: MetricSeries[]; unresolved: boolean }> {
  try {
    const rows = await getMetrics(source.metric, range.since, range.step);
    return { source, rows, unresolved: false };
  } catch (e) {
    if (e instanceof ApiError && e.status === 400 && /not resolved/i.test(e.body)) {
      return { source, rows: [], unresolved: true };
    }
    throw e;
  }
}

export default function MetricPanel(props: MetricPanelProps) {
  const [view, setView] = createSignal<View>("chart");

  const [raw] = createResource(
    // Keyed on everything that changes the ANSWER. `services` is not among them:
    // filtering happens on the returned label sets rather than in the query (see
    // catalog.ts), so changing the scope re-shapes what is already here instead
    // of going back to the server.
    () => ({
      id: props.panel.id,
      since: props.range.since,
      step: props.range.step,
      tick: props.tick,
    }),
    () => Promise.all(props.panel.sources.map((s) => fetchSource(s, props.range))),
  );

  // A refresh leaves the resource in "refreshing", where the accessor still
  // returns the PREVIOUS value — that is what holds the frame at reduced opacity
  // instead of flashing a skeleton and jumping the layout. A resource accessor
  // re-throws its error when read, so .error is checked first.
  const answers = () => (raw.error ? [] : (raw() ?? []));

  const capped = createMemo(() => {
    // Whether a label carries its workload's name is decided ONCE, across every
    // source: a panel merging two backend spellings sees one service per source,
    // so a per-source decision would label two different workloads "replica 0".
    const all = answers().flatMap((a) => a.rows);
    const nameService = scopedServices(all, props.services).size > 1;
    return capSeries(
      answers().flatMap((a) =>
        buildSeries(a.rows, {
          counter: a.source.counter,
          services: props.services,
          nameService,
        }),
      ),
    );
  });

  // The palette assignment lives with the panel and outlives each refetch, so a
  // replica that vanishes for one poll comes back the same color. It is a plain
  // closure variable rather than a signal because it is derived from `capped`
  // and never changes on its own.
  let assignment = new Map<string, number>();
  const slots = createMemo(() => {
    assignment = assignSlots(
      assignment,
      capped().shown.map((s) => s.key),
    );
    return assignment;
  });

  // Nothing has ever reported ANY of this panel's sources. Not an error in a
  // dashboard: it is the honest "no data yet" for a metric this deploy backend
  // does not produce.
  const unrecorded = () => {
    const a = answers();
    return a.length > 0 && a.every((r) => r.unresolved);
  };
  const failure = () => (raw.error ? String(raw.error) : "");
  const settled = () => raw.state === "ready" || raw.state === "refreshing" || !!raw.error;

  // The headline: the latest value, summed across the series on the chart when
  // there are several. Summing is only honest for a quantity, which every metric
  // in the catalog is.
  const headline = () => {
    const list = capped().shown;
    if (list.length === 0) return "";
    return formatValue(
      props.panel.unit,
      list.reduce((acc, s) => acc + s.samples[s.samples.length - 1].v, 0),
    );
  };

  return (
    <section class="panel" classList={{ compact: props.compact, stale: raw.loading && raw.state === "refreshing" }}>
      <header class="panel-head">
        <div class="panel-title">
          <h3>{props.panel.title}</h3>
          <p class="panel-metric">{metricNames(props.panel).join(" / ")}</p>
        </div>
        <div class="panel-head-right">
          <Show when={headline()}>
            <span class="panel-value">{headline()}</span>
          </Show>
          <Show when={capped().shown.length > 0}>
            <div class="seg" role="group" aria-label={`${props.panel.title} view`}>
              <button classList={{ active: view() === "chart" }} onClick={() => setView("chart")}>
                Chart
              </button>
              <button classList={{ active: view() === "table" }} onClick={() => setView("table")}>
                Table
              </button>
            </div>
          </Show>
        </div>
      </header>

      <Show when={failure()}>
        <p class="error">{failure()}</p>
      </Show>

      <Show
        when={capped().shown.length > 0}
        fallback={<EmptyPanel panel={props.panel} unrecorded={unrecorded()} settled={settled()} />}
      >
        <Show
          when={view() === "chart"}
          fallback={<PanelTable panel={props.panel} series={capped().shown} />}
        >
          <TimeSeriesChart
            series={capped().shown}
            slots={slots()}
            unit={props.panel.unit}
            height={props.compact ? 128 : undefined}
            label={`${props.panel.title} over the ${props.range.label.toLowerCase()}`}
          />
        </Show>
      </Show>

      {/* Never a silent cap: a chart that quietly drew 8 of 30 series would read
          as complete. */}
      <Show when={capped().hidden > 0}>
        <p class="muted panel-capped">
          {capped().hidden} more series not shown — the palette carries eight distinguishable
          colors. Narrow the scope, or read the table, to reach the rest.
        </p>
      </Show>

      <Show when={!props.compact}>
        <p class="muted panel-hint">{props.panel.hint}</p>
      </Show>
    </section>
  );
}

function EmptyPanel(props: { panel: Panel; unrecorded: boolean; settled: boolean }) {
  return (
    <Show
      when={props.settled}
      fallback={<p class="muted chart-empty chart-loading">Loading…</p>}
    >
      {/* chart-nodata marks a SETTLED absence, which is a different statement
          from "not here yet" — and the hook a test waits on so it never asserts
          against a panel still in flight. */}
      <p class="muted chart-empty chart-nodata">
        {props.unrecorded
          ? `Nothing has reported ${metricNames(props.panel).join(" or ")} yet.`
          : "No samples in this window."}
      </p>
    </Show>
  );
}

// PanelTable is the chart's WCAG-clean twin: the same series, read as numbers.
// It is what makes every value reachable without a pointer — and it is also the
// relief the palette requires, since three of its light-mode hues sit below 3:1
// against the light surface and so may never be the only way to read a figure.
function PanelTable(props: { panel: Panel; series: ChartSeries[] }) {
  return (
    <div class="panel-table">
      <table class="grid">
        <thead>
          <tr>
            <th>Series</th>
            <th>Last</th>
            <th>Min</th>
            <th>Max</th>
            <th>Avg</th>
          </tr>
        </thead>
        <tbody>
          <For each={props.series}>
            {(s) => {
              const sum = summarize(s.samples);
              const cell = (v?: number) => (sum ? formatValue(props.panel.unit, v!) : "—");
              return (
                <tr>
                  {/* Not td.wrap: that breaks mid-word, and "shop-web · replica 0"
                      split as "shop-we / b · replic / a 0" is unreadable. The
                      label stays on one line and .panel-table scrolls instead. */}
                  <td>{s.label}</td>
                  <td>{cell(sum?.last)}</td>
                  <td>{cell(sum?.min)}</td>
                  <td>{cell(sum?.max)}</td>
                  <td>{cell(sum?.avg)}</td>
                </tr>
              );
            }}
          </For>
        </tbody>
      </table>
    </div>
  );
}
