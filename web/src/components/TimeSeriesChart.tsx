import { For, Show, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { Unit } from "../views/metrics/catalog";
import {
  formatClock,
  formatStamp,
  formatValue,
  formatTicks,
  showSeconds,
  type ChartSeries,
} from "../views/metrics/series";
import {
  areaPath,
  binaryScale,
  extent,
  frameTimes,
  linePath,
  makePlot,
  nearestIndex,
  niceTicks,
  timeTicks,
  valueAt,
} from "../views/metrics/scales";

// A multi-series line chart over time, drawn as plain SVG against the design
// system's tokens.
//
// The conventions it holds to, and why they are not adjustable per call site:
//
//   - ONE value axis, anchored at zero. Two scales on one plot invent a
//     correlation the data does not contain, so a panel that needs a second unit
//     is a second panel.
//   - Color follows the SERIES KEY, never its position, so a series that comes
//     and goes between polls keeps its identity. The caller owns the assignment
//     (assignSlots) because it is what survives across refetches.
//   - Marks stay thin and the chrome stays recessive: 2px lines, hairline solid
//     gridlines one step off the surface, an end-dot with a surface-colored ring
//     so overlapping series stay legible where they cross.
//   - A single series takes the accent-free slot 1 and a 10% wash below its line;
//     it gets no legend box, because the panel title already names what is
//     plotted and a one-swatch legend is a restatement that costs a row.
//
// The hover layer is part of the chart, not an enhancement: a vertical crosshair
// snaps to the nearest sampled instant and reads out EVERY series at it, so the
// pointer never has to find a 2px line. Keyboard does the same thing — the plot
// is focusable and the arrow keys walk the same crosshair — and nothing is
// reachable only by hovering: the legend carries each series' latest value, and
// the panel's table view carries the rest.

const PAD_RIGHT = 16;
const PAD_TOP = 10;
const PAD_BOTTOM = 24;
const FALLBACK_WIDTH = 640;
// The value gutter is sized from the WIDEST tick label rather than fixed: "0.3"
// and "1000 KiB/s" are the same axis in different units, and a constant gutter
// either wastes half the plot or clips the label off the left edge of the SVG.
// 7px per character is the 12px system stack's rough advance width; the clamp
// keeps a pathological label from eating the plot.
const CHAR_PX = 7;
const gutterFor = (labels: string[]) =>
  Math.min(104, Math.max(36, 14 + labels.reduce((m, s) => Math.max(m, s.length), 0) * CHAR_PX));

export interface TimeSeriesChartProps {
  series: ChartSeries[];
  // slots maps a series key to a categorical palette slot (0-7).
  slots: Map<string, number>;
  unit: Unit;
  height?: number;
  // label names the plot for screen readers; the panel's own heading is the
  // visible version of the same thing.
  label: string;
}

export default function TimeSeriesChart(props: TimeSeriesChartProps) {
  const [width, setWidth] = createSignal(FALLBACK_WIDTH);
  const [cursor, setCursor] = createSignal<number | null>(null);
  let wrap: HTMLDivElement | undefined;

  // The SVG is sized in real pixels rather than scaled through a viewBox: a
  // viewBox that stretches would distort every stroke width and every glyph.
  onMount(() => {
    if (!wrap || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0;
      if (w > 0) setWidth(Math.round(w));
    });
    ro.observe(wrap);
    onCleanup(() => ro.disconnect());
  });

  const height = () => props.height ?? 168;
  const ext = createMemo(() => extent(props.series));

  // A byte axis steps in powers of 1024 so its ticks land on whole MiB rather
  // than on the 1.19 MiB a decimal step becomes after two divisions.
  const tickScale = createMemo(() => {
    const e = ext();
    if (!e) return 1;
    return props.unit === "bytes" || props.unit === "bytesPerSecond" ? binaryScale(e.v1) : 1;
  });
  const valueTicks = createMemo(() => {
    const e = ext();
    return e ? niceTicks(e.v0, e.v1, 4, tickScale()) : [];
  });
  const tickLabels = createMemo(() => formatTicks(props.unit, valueTicks(), tickScale()));

  // The plotted domain is stretched to the outermost TICK, not to the outermost
  // sample. Two things follow: the top gridline is inside the plot instead of
  // one pixel above it, and a flat series (a memory limit, an idle counter) sits
  // in the chart rather than pinned along its top edge.
  const domain = createMemo(() => {
    const e = ext();
    if (!e) return null;
    const ticks = valueTicks();
    if (ticks.length === 0) return e;
    return { ...e, v0: Math.min(e.v0, ticks[0]), v1: Math.max(e.v1, ticks[ticks.length - 1]) };
  });

  const plot = createMemo(() => {
    const d = domain();
    if (!d) return null;
    return makePlot(d, {
      width: width(),
      height: height(),
      padLeft: gutterFor(tickLabels()),
      padRight: PAD_RIGHT,
      padTop: PAD_TOP,
      padBottom: PAD_BOTTOM,
    });
  });

  const times = createMemo(() => frameTimes(props.series));
  const spanMs = createMemo(() => {
    const e = ext();
    return e ? e.t1 - e.t0 : 0;
  });
  // The step between samples, used both to break the line across gaps and to
  // decide how far from the crosshair a value may still be quoted. Taken as the
  // median of observed gaps so one long outage does not redefine "adjacent".
  const step = createMemo(() => {
    const ts = times();
    if (ts.length < 2) return 0;
    const gaps: number[] = [];
    for (let i = 1; i < ts.length; i++) gaps.push(ts[i] - ts[i - 1]);
    gaps.sort((a, b) => a - b);
    return gaps[Math.floor(gaps.length / 2)];
  });

  const xTicks = createMemo(() => {
    const e = ext();
    return e ? timeTicks(e.t0, e.t1, 4) : [];
  });

  const slotOf = (key: string) => props.slots.get(key) ?? 0;
  const single = () => props.series.length === 1;
  // A gap wider than 2.5 steps is missing data, not a slow sample.
  const gapMs = () => (step() > 0 ? step() * 2.5 : Infinity);

  const cursorT = () => {
    const c = cursor();
    const ts = times();
    if (c === null || ts.length === 0) return null;
    return ts[Math.min(Math.max(c, 0), ts.length - 1)];
  };

  // readout is what the crosshair says: every series that has a value at this
  // instant, largest first, because at a crosshair the reader is comparing.
  const readout = createMemo(() => {
    const t = cursorT();
    if (t === null) return [];
    const tol = step() > 0 ? step() * 0.75 : 1000;
    return props.series
      .map((s) => ({ series: s, v: valueAt(s.samples, t, tol) }))
      .filter((r): r is { series: ChartSeries; v: number } => r.v !== null)
      .sort((a, b) => b.v - a.v);
  });

  const moveCursor = (e: PointerEvent | MouseEvent) => {
    const p = plot();
    const ts = times();
    if (!p || ts.length === 0) return;
    const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
    const px = e.clientX - rect.left;
    const e0 = ext()!;
    const frac = (px - p.left) / Math.max(1, p.right - p.left);
    setCursor(nearestIndex(ts, e0.t0 + frac * (e0.t1 - e0.t0)));
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const ts = times();
    if (ts.length === 0) return;
    const cur = cursor();
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      const delta = e.key === "ArrowRight" ? 1 : -1;
      const next = cur === null ? (delta > 0 ? 0 : ts.length - 1) : cur + delta;
      setCursor(Math.min(Math.max(next, 0), ts.length - 1));
      e.preventDefault();
    } else if (e.key === "Home") {
      setCursor(0);
      e.preventDefault();
    } else if (e.key === "End") {
      setCursor(ts.length - 1);
      e.preventDefault();
    } else if (e.key === "Escape") {
      setCursor(null);
    }
  };

  // The tooltip is pinned to the side of the plot the crosshair is NOT on, so it
  // never covers the samples the reader is pointing at.
  const tooltipSide = () => {
    const p = plot();
    const t = cursorT();
    if (!p || t === null) return "right";
    return p.x(t) > (p.left + p.right) / 2 ? "left" : "right";
  };

  return (
    <div class="chart" ref={wrap}>
      <Show
        when={plot()}
        fallback={
          <p class="muted chart-empty">No samples in this window.</p>
        }
      >
        {(p) => (
          <>
            <svg
              class="chart-plot"
              width={width()}
              height={height()}
              role="img"
              tabindex="0"
              aria-label={props.label}
              onPointerMove={moveCursor}
              onPointerLeave={() => setCursor(null)}
              onKeyDown={onKeyDown}
              onBlur={() => setCursor(null)}
            >
              <For each={valueTicks()}>
                {(v, i) => (
                  <Show when={p().y(v) >= p().top - 1 && p().y(v) <= p().bottom + 1}>
                    <g>
                      <line
                        class="chart-grid"
                        x1={p().left}
                        x2={p().right}
                        y1={p().y(v)}
                        y2={p().y(v)}
                      />
                      <text class="chart-axis" x={p().left - 8} y={p().y(v)} text-anchor="end" dominant-baseline="middle">
                        {tickLabels()[i()]}
                      </text>
                    </g>
                  </Show>
                )}
              </For>
              <For each={xTicks()}>
                {(t, i) => (
                  <text
                    class="chart-axis"
                    x={p().x(t)}
                    y={p().bottom + 16}
                    text-anchor={i() === 0 ? "start" : i() === xTicks().length - 1 ? "end" : "middle"}
                  >
                    {formatClock(t, showSeconds(spanMs()))}
                  </text>
                )}
              </For>
              <line
                class="chart-axis-rule"
                x1={p().left}
                x2={p().right}
                y1={p().bottom}
                y2={p().bottom}
              />

              <Show when={single()}>
                <path
                  class="chart-area"
                  style={{ fill: `var(--series-${slotOf(props.series[0].key) + 1})` }}
                  d={areaPath(props.series[0].samples, p(), gapMs())}
                />
              </Show>

              <For each={props.series}>
                {(s) => (
                  <path
                    class="chart-line"
                    style={{ stroke: `var(--series-${slotOf(s.key) + 1})` }}
                    d={linePath(s.samples, p(), gapMs())}
                  />
                )}
              </For>

              {/* End-dots mark where each series currently stands; the ring keeps
                  two of them legible where they land on the same value. */}
              <For each={props.series}>
                {(s) => {
                  const last = () => s.samples[s.samples.length - 1];
                  return (
                    <Show when={last()}>
                      <circle
                        class="chart-dot"
                        style={{ fill: `var(--series-${slotOf(s.key) + 1})` }}
                        cx={p().x(last().t)}
                        cy={p().y(last().v)}
                        r="4"
                      />
                    </Show>
                  );
                }}
              </For>

              <Show when={cursorT() !== null}>
                <line
                  class="chart-crosshair"
                  x1={p().x(cursorT()!)}
                  x2={p().x(cursorT()!)}
                  y1={p().top}
                  y2={p().bottom}
                />
                <For each={readout()}>
                  {(r) => (
                    <circle
                      class="chart-dot"
                      style={{ fill: `var(--series-${slotOf(r.series.key) + 1})` }}
                      cx={p().x(cursorT()!)}
                      cy={p().y(r.v)}
                      r="4"
                    />
                  )}
                </For>
              </Show>
            </svg>

            <Show when={cursorT() !== null && readout().length > 0}>
              <div
                class="chart-tip"
                classList={{ left: tooltipSide() === "left" }}
                role="status"
                aria-live="polite"
              >
                <div class="chart-tip-time">{formatStamp(cursorT()!, spanMs())}</div>
                <For each={readout()}>
                  {(r) => (
                    <div class="chart-tip-row">
                      <span
                        class="chart-key"
                        style={{ background: `var(--series-${slotOf(r.series.key) + 1})` }}
                      />
                      <span class="chart-tip-value">{formatValue(props.unit, r.v)}</span>
                      <span class="chart-tip-name">{r.series.label}</span>
                    </div>
                  )}
                </For>
              </div>
            </Show>
          </>
        )}
      </Show>

      {/* The legend doubles as the direct-label channel: it carries each series'
          latest value, so no number on this chart is reachable only by hovering.
          One series needs no legend — the panel heading names it. */}
      <Show when={props.series.length > 1}>
        <ul class="chart-legend">
          <For each={props.series}>
            {(s) => (
              <li>
                <span
                  class="chart-key"
                  style={{ background: `var(--series-${slotOf(s.key) + 1})` }}
                />
                <span class="chart-legend-name">{s.label}</span>
                <span class="chart-legend-value">
                  {formatValue(props.unit, s.samples[s.samples.length - 1].v)}
                </span>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
}
