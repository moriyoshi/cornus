// Turning what the store returns into what a chart can draw.
//
// Everything here is pure and side-effect free so it can be tested without a DOM
// or a backend — the chart component only positions what these functions decide.

import type { MetricSeries } from "../../api";
import type { Unit } from "./catalog";

// Sample carries time as epoch MILLISECONDS rather than the wire's RFC3339
// string: every downstream use (scales, differences, nearest-point lookup) is
// arithmetic, and re-parsing a string per pixel is how a chart gets slow.
export interface Sample {
  t: number;
  v: number;
}

export interface ChartSeries {
  // key identifies the series as an ENTITY — the full label set, canonically
  // spelled. Color is assigned against this key, never against the row's position
  // in the list, so filtering or re-sorting never repaints the survivors.
  key: string;
  label: string;
  samples: Sample[];
}

// MAX_SERIES is the categorical palette's size. Past it there is no honest color
// left to give a series, so the extras are withheld and COUNTED (see capSeries)
// rather than silently dropped or given a recycled hue.
export const MAX_SERIES = 8;

// canonicalKey renders a label set in PromQL's own notation, key-sorted, so the
// same series always produces the same string. It matches the spelling
// `cornus observe metrics` prints, which makes a UI series and a CLI series
// recognizably the same thing.
export function canonicalKey(labels: Record<string, string> | undefined): string {
  const keys = Object.keys(labels ?? {}).sort();
  if (keys.length === 0) return "{}";
  return "{" + keys.map((k) => `${k}=${JSON.stringify(labels![k])}`).join(", ") + "}";
}

// serviceOf reads the deployment name every recorded metric carries.
export function serviceOf(labels: Record<string, string> | undefined): string {
  return labels?.service ?? "";
}

// seriesLabel is the human spelling of a label set: the service (when the chart
// mixes several) plus whatever else distinguishes this series from its siblings.
//
// Values carry their own meaning here — "user", "receive", "eth0" — so only the
// replica ordinal needs its key spelled out to read as anything.
export function seriesLabel(
  labels: Record<string, string> | undefined,
  includeService: boolean,
): string {
  const parts: string[] = [];
  const service = serviceOf(labels);
  if (includeService && service) parts.push(service);
  for (const k of Object.keys(labels ?? {}).sort()) {
    if (k === "service") continue;
    const v = labels![k];
    parts.push(k === "cornus_replica" ? `replica ${v}` : v);
  }
  if (parts.length === 0) return service || "series";
  return parts.join(" · ");
}

// toSamples parses one wire series into sorted, finite samples.
//
// It is defensive on purpose: a store that shed under load, a zero-valued
// timestamp, or a NaN reaching JSON as null would otherwise become a line that
// wanders off the plot instead of a gap.
export function toSamples(series: MetricSeries): Sample[] {
  const out: Sample[] = [];
  for (const p of series.points ?? []) {
    const t = Date.parse(p.t);
    if (!Number.isFinite(t) || t <= 0) continue;
    const v = typeof p.v === "number" ? p.v : Number(p.v);
    if (!Number.isFinite(v)) continue;
    out.push({ t, v });
  }
  out.sort((a, b) => a.t - b.t);
  return out;
}

// differentiate turns a cumulative counter into a per-second rate.
//
// This is the client-side stand-in for PromQL's `rate()`, which the store's
// profile is not verified to accept (see catalog.ts). It is deliberately the
// simple thing — consecutive differences, no extrapolation over the window edges
// — so what a reader sees is exactly the arithmetic on the samples in front of
// them.
//
// A DECREASE is a counter reset (the container was replaced, the server
// restarted), not negative traffic. Prometheus treats the post-reset value as
// the increase since the reset, and so does this: anything else would draw a
// deep negative spike at every restart, which is the one moment the chart most
// needs to stay readable.
export function differentiate(samples: Sample[]): Sample[] {
  const out: Sample[] = [];
  for (let i = 1; i < samples.length; i++) {
    const prev = samples[i - 1];
    const cur = samples[i];
    const dt = (cur.t - prev.t) / 1000;
    if (dt <= 0) continue;
    const delta = cur.v >= prev.v ? cur.v - prev.v : cur.v;
    out.push({ t: cur.t, v: delta / dt });
  }
  return out;
}

export interface BuildOptions {
  // counter differentiates before plotting.
  counter: boolean;
  // services, when non-empty, keeps only the series recorded for those
  // deployments — one for a workload view, a project's whole membership for a
  // project view. The filter runs HERE rather than as a `{service="…"}` matcher
  // in the query because the query is a bare selector by design (catalog.ts),
  // and because a client-side filter cannot be defeated by a label whose name
  // the store's PromQL cannot express.
  //
  // An EMPTY array is not the same as an absent one: absent means "every service",
  // empty means "a set that happens to have no members" — a project with no
  // deployments yet — and drawing the whole server's traffic under that project's
  // heading would be a lie about whose traffic it is.
  services?: string[];
  // nameService forces the service into (or out of) each series' label.
  //
  // Left undefined, buildSeries decides from what it is given: name the service
  // only when more than one is on the chart, so a scoped chart does not repeat
  // the same word down its legend. That decision is WRONG for a caller that
  // merges several sources into one chart — each call sees one service and omits
  // the name, and the finished chart has two different workloads both labelled
  // "replica 0". Such a caller decides once, across everything it will draw, and
  // says so here.
  nameService?: boolean;
}

// buildSeries shapes a store response into drawable series. Empty series (all
// samples dropped, or a counter with a single sample and therefore no rate yet)
// are removed: a legend entry with nothing behind it is worse than one fewer row.
export function buildSeries(raw: MetricSeries[], opts: BuildOptions): ChartSeries[] {
  const keep = opts.services === undefined ? null : new Set(opts.services);
  const wanted = keep ? raw.filter((s) => keep.has(serviceOf(s.labels))) : raw;
  const includeService = opts.nameService ?? scopedServices(raw, opts.services).size > 1;
  const out: ChartSeries[] = [];
  for (const s of wanted) {
    const parsed = toSamples(s);
    const samples = opts.counter ? differentiate(parsed) : parsed;
    if (samples.length === 0) continue;
    out.push({ key: canonicalKey(s.labels), label: seriesLabel(s.labels, includeService), samples });
  }
  return out;
}

// scopedServices is the set of distinct deployments in `raw` that survive the
// scope — what a caller merging several sources needs in order to decide, once,
// whether the labels have to carry the service name.
export function scopedServices(raw: MetricSeries[], services?: string[]): Set<string> {
  const keep = services === undefined ? null : new Set(services);
  const out = new Set<string>();
  for (const s of raw) {
    const name = serviceOf(s.labels);
    if (!name) continue;
    if (!keep || keep.has(name)) out.add(name);
  }
  return out;
}

export interface CappedSeries {
  shown: ChartSeries[];
  // hidden is how many series the palette had no color for. The UI states this
  // number: a chart that quietly drew 8 of 30 series would read as complete.
  hidden: number;
}

// capSeries keeps at most MAX_SERIES, choosing by peak value so the largest
// consumers survive, then orders the survivors by label so the legend is stable
// across refetches.
export function capSeries(list: ChartSeries[], max = MAX_SERIES): CappedSeries {
  if (list.length <= max) {
    return { shown: [...list].sort(byLabel), hidden: 0 };
  }
  const ranked = [...list].sort((a, b) => peak(b) - peak(a) || a.key.localeCompare(b.key));
  return { shown: ranked.slice(0, max).sort(byLabel), hidden: list.length - max };
}

const byLabel = (a: ChartSeries, b: ChartSeries) =>
  a.label.localeCompare(b.label) || a.key.localeCompare(b.key);

const peak = (s: ChartSeries) => s.samples.reduce((m, p) => (p.v > m ? p.v : m), -Infinity);

// assignSlots hands each series key a palette slot and never takes it back while
// the panel lives.
//
// Holding the assignment across refetches is the whole point: a replica that
// disappears for one poll and comes back must come back the same color, and a
// newly appearing replica must not shift everyone else's. Slots are reused only
// once a key has been gone long enough to be evicted by the caller dropping the
// map — within a panel's lifetime, identity is permanent.
export function assignSlots(prev: Map<string, number>, keys: string[]): Map<string, number> {
  const next = new Map(prev);
  const taken = new Set(next.values());
  for (const key of keys) {
    if (next.has(key)) continue;
    let slot = 0;
    while (slot < MAX_SERIES && taken.has(slot)) slot++;
    // Past the palette the assignment wraps rather than inventing a hue; capSeries
    // means the caller should never draw a series that got here, and a wrapped
    // slot is a visible duplicate rather than a crash if one ever does.
    if (slot >= MAX_SERIES) slot = next.size % MAX_SERIES;
    next.set(key, slot);
    taken.add(slot);
  }
  return next;
}

export interface Summary {
  last: number;
  min: number;
  max: number;
  avg: number;
}

// summarize is what the table view and the legend read. Four numbers is what a
// reader can actually use from a line they cannot hover.
export function summarize(samples: Sample[]): Summary | null {
  if (samples.length === 0) return null;
  let min = Infinity;
  let max = -Infinity;
  let sum = 0;
  for (const s of samples) {
    if (s.v < min) min = s.v;
    if (s.v > max) max = s.v;
    sum += s.v;
  }
  return { last: samples[samples.length - 1].v, min, max, avg: sum / samples.length };
}

// ---- formatting ----

const BINARY = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

function formatBytes(v: number): string {
  const sign = v < 0 ? "-" : "";
  let n = Math.abs(v);
  let i = 0;
  while (n >= 1024 && i < BINARY.length - 1) {
    n /= 1024;
    i++;
  }
  const digits = i === 0 ? 0 : n < 10 ? 2 : n < 100 ? 1 : 0;
  return `${sign}${n.toFixed(digits)} ${BINARY[i]}`;
}

function formatCount(v: number): string {
  if (Number.isInteger(v)) return v.toLocaleString("en-US");
  return v.toFixed(Math.abs(v) < 10 ? 2 : 1);
}

// formatValue is the reader-facing spelling, used in the legend, the tooltip, and
// the table view — the three places a value is quoted rather than positioned.
export function formatValue(unit: Unit, v: number): string {
  if (!Number.isFinite(v)) return "—";
  switch (unit) {
    case "bytes":
      return formatBytes(v);
    case "bytesPerSecond":
      return `${formatBytes(v)}/s`;
    case "cores": {
      const n = Math.abs(v) < 1 ? v.toFixed(3) : v.toFixed(2);
      return `${n} ${Math.abs(v) === 1 ? "core" : "cores"}`;
    }
    case "seconds":
      return `${v.toFixed(2)} s`;
    case "count":
      return formatCount(v);
  }
}

// formatTick is the axis spelling: the same magnitude with the words dropped, so
// four ticks do not repeat "cores" down the side of every chart.
export function formatTick(unit: Unit, v: number): string {
  if (!Number.isFinite(v)) return "";
  switch (unit) {
    case "bytes":
      return formatBytes(v);
    case "bytesPerSecond":
      return `${formatBytes(v)}/s`;
    case "cores":
      return Math.abs(v) < 1 ? String(Number(v.toFixed(3))) : v.toFixed(2);
    case "seconds":
      return String(Number(v.toFixed(2)));
    case "count":
      return formatCount(v);
  }
}

// formatTicks renders a whole value axis at once, so its labels share one unit
// and one decimal count. Formatted individually, a byte axis comes out as
// "100 MiB" beside "75.0 MiB" — two precisions for no reason, on the same scale.
//
// `scale` is the 1024-power the ticks were STEPPED in (scales.binaryScale). It is
// passed in rather than re-derived so the label's unit cannot disagree with the
// step's: an axis stepped in MiB always reads in MiB.
export function formatTicks(unit: Unit, values: number[], scale = 1): string[] {
  if (unit !== "bytes" && unit !== "bytesPerSecond") {
    return values.map((v) => formatTick(unit, v));
  }
  const suffix = unit === "bytesPerSecond" ? "/s" : "";
  const i = Math.min(Math.round(Math.log2(scale) / 10), BINARY.length - 1);
  const scaled = values.map((v) => v / scale);
  // The fewest decimals that render every tick EXACTLY: whole steps come out as
  // "25 MiB", half steps as "2.5 MiB", and no tick is ever rounded into a lie
  // about where its gridline sits.
  const digits =
    [0, 1, 2].find((d) => scaled.every((v) => Math.abs(v - Number(v.toFixed(d))) < 1e-9)) ?? 2;
  // Zero keeps its plain spelling: "0 B" is unambiguous at any scale, and
  // "0.00 GiB" spends four characters of gutter saying nothing.
  return values.map((v, k) => (v === 0 ? `0 B${suffix}` : `${scaled[k].toFixed(digits)} ${BINARY[i]}${suffix}`));
}

const pad2 = (n: number) => String(n).padStart(2, "0");

// showSeconds decides whether a window is short enough for the seconds field to
// separate two ticks. Above it, every label would carry the same trailing ":00"
// — noise wearing the shape of precision.
export const showSeconds = (spanMs: number) => spanMs <= 20 * 60 * 1000;

// formatClock renders a sample's time in the reader's own timezone.
export function formatClock(t: number, withSeconds = false): string {
  const d = new Date(t);
  const hm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  return withSeconds ? `${hm}:${pad2(d.getSeconds())}` : hm;
}

// formatStamp is the tooltip's time: the clock, plus the date once the window is
// long enough to span more than one of them.
export function formatStamp(t: number, spanMs: number): string {
  const d = new Date(t);
  const clock = formatClock(t, showSeconds(spanMs));
  if (spanMs < 12 * 60 * 60 * 1000) return clock;
  return `${d.getMonth() + 1}/${d.getDate()} ${clock}`;
}
