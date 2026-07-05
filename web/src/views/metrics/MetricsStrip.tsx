import { For, Show } from "solid-js";
import { A } from "@solidjs/router";
import MetricPanel from "./MetricPanel";
import { COMPACT_PANELS, type Range } from "./catalog";

// The two-panel strip a project or workload section carries: CPU and memory for
// everything under that heading.
//
// It is a SUMMARY, not a small dashboard. There is no range control here — the
// window is whatever the page chose — because a per-section filter would let two
// sections on one screen describe two different moments while looking like one
// view. The link is the way to more: it opens the full dashboard, already scoped.
export default function MetricsStrip(props: {
  services: string[];
  range: Range;
  tick: number;
  // to is the "everything about this" destination, when there is one.
  to?: string;
}) {
  return (
    <>
      <div class="row strip-head">
        <h3>Metrics</h3>
        <span class="muted">{props.range.label.toLowerCase()}</span>
        <Show when={props.to}>
          <A href={props.to!}>All metrics →</A>
        </Show>
      </div>
      <Show
        when={props.services.length > 0}
        fallback={<p class="muted">Nothing deployed here to measure.</p>}
      >
        <div class="panels compact">
          <For each={COMPACT_PANELS}>
            {(panel) => (
              <MetricPanel
                panel={panel}
                range={props.range}
                tick={props.tick}
                services={props.services}
                compact
              />
            )}
          </For>
        </div>
      </Show>
    </>
  );
}
