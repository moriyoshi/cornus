// The geometry of a time-series plot: domains, round ticks, SVG paths, and the
// lookup behind the crosshair. Pure arithmetic — no DOM, no framework.

import type { ChartSeries, Sample } from "./series";

export interface Extent {
  t0: number;
  t1: number;
  v0: number;
  v1: number;
}

// extent is the data's bounding box, with two adjustments a raw min/max cannot
// make on its own:
//
//   - The value axis is anchored at ZERO. Every metric here is a quantity, and a
//     line drawn against a floating baseline exaggerates a 2% wobble into a
//     cliff. (A negative minimum still opens the axis downward — that would be a
//     counter reset the differentiator failed to absorb, and hiding it would be
//     the wrong kind of tidy.)
//   - A FLAT series still needs a range. All-equal values give a zero-height box
//     that would divide by zero in the scale, so the top opens to 1 (or to twice
//     the value) instead.
export function extent(series: ChartSeries[]): Extent | null {
  let t0 = Infinity;
  let t1 = -Infinity;
  let v0 = 0;
  let v1 = -Infinity;
  let any = false;
  for (const s of series) {
    for (const p of s.samples) {
      any = true;
      if (p.t < t0) t0 = p.t;
      if (p.t > t1) t1 = p.t;
      if (p.v < v0) v0 = p.v;
      if (p.v > v1) v1 = p.v;
    }
  }
  if (!any) return null;
  if (t1 === t0) t1 = t0 + 1;
  if (v1 <= v0) v1 = v0 === 0 ? 1 : v0 + Math.abs(v0);
  return { t0, t1, v0, v1 };
}

// binaryScale is the 1024-power a byte axis should be stepped in, so ticks land
// on 1 MiB rather than on 1.19 MiB (which is what a decimal step formats as once
// it has been divided by 1024 twice).
export function binaryScale(max: number): number {
  let scale = 1;
  while (Math.abs(max) / scale >= 1024 && scale < 1024 ** 5) scale *= 1024;
  return scale;
}

// niceStep rounds a rough interval up to 1, 2, 2.5 or 5 times a power of ten.
function niceStep(rough: number): number {
  if (!(rough > 0)) return 1;
  const exp = Math.floor(Math.log10(rough));
  const base = 10 ** exp;
  const f = rough / base;
  const nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;
  return nf * base;
}

// niceTicks lays round values across [min, max], stepping in units of `scale`
// (pass binaryScale(max) for a byte axis, 1 otherwise). The result always spans
// the data: the last tick is at or above max, so the axis never stops short of
// the line it is measuring.
export function niceTicks(min: number, max: number, count = 4, scale = 1): number[] {
  if (!Number.isFinite(min) || !Number.isFinite(max) || count < 1) return [];
  const lo = min / scale;
  const hi = max / scale;
  const step = niceStep((hi - lo) / count);
  const first = Math.floor(lo / step) * step;
  const out: number[] = [];
  // Emit-then-test, so the last tick is always at or ABOVE max. A loop that
  // tests first stops one tick short whenever max sits between two steps (a
  // 426 MiB series on a 200 MiB step ends its axis at 400), and the caller
  // stretches the plotted domain to the last tick — which would pin the line to
  // the top edge of the chart with no headroom at all.
  const eps = step * 1e-9;
  for (let v = first; ; v += step) {
    const scaled = v * scale;
    // -0 renders as "-0"; it is the same tick as 0.
    out.push(Object.is(scaled, -0) ? 0 : scaled);
    if (v >= hi - eps || out.length > 64) break;
  }
  return out;
}

// timeTicks places `count` evenly spaced instants across the window. Time is not
// rounded to the minute: the window's own edges are what the reader picked, and a
// tick that disagrees with the range selector is a small lie.
export function timeTicks(t0: number, t1: number, count = 4): number[] {
  if (!(t1 > t0) || count < 2) return [t0];
  const out: number[] = [];
  for (let i = 0; i < count; i++) out.push(t0 + ((t1 - t0) * i) / (count - 1));
  return out;
}

export interface Plot {
  width: number;
  height: number;
  // The plot box, inset from the SVG by the axis gutters.
  left: number;
  top: number;
  right: number;
  bottom: number;
  x: (t: number) => number;
  y: (v: number) => number;
}

export interface PlotBox {
  width: number;
  height: number;
  padLeft: number;
  padRight: number;
  padTop: number;
  padBottom: number;
}

export function makePlot(ext: Extent, box: PlotBox): Plot {
  const left = box.padLeft;
  const right = Math.max(box.padLeft + 1, box.width - box.padRight);
  const top = box.padTop;
  const bottom = Math.max(box.padTop + 1, box.height - box.padBottom);
  const tSpan = ext.t1 - ext.t0 || 1;
  const vSpan = ext.v1 - ext.v0 || 1;
  return {
    width: box.width,
    height: box.height,
    left,
    top,
    right,
    bottom,
    x: (t) => left + ((t - ext.t0) / tSpan) * (right - left),
    y: (v) => bottom - ((v - ext.v0) / vSpan) * (bottom - top),
  };
}

// linePath draws the series. A gap is not interpolated across: when consecutive
// samples sit further apart than `gapMs`, the path lifts the pen, so a period the
// store has no data for reads as missing rather than as a straight line somebody
// might take for a measurement.
export function linePath(samples: Sample[], plot: Plot, gapMs = Infinity): string {
  let d = "";
  let penDown = false;
  for (let i = 0; i < samples.length; i++) {
    const s = samples[i];
    const broke = i > 0 && s.t - samples[i - 1].t > gapMs;
    if (!penDown || broke) {
      d += `${d ? " " : ""}M${round(plot.x(s.t))},${round(plot.y(s.v))}`;
      penDown = true;
      continue;
    }
    d += ` L${round(plot.x(s.t))},${round(plot.y(s.v))}`;
  }
  return d;
}

// areaPath closes a single series' line down to the baseline for the 10% wash.
// It is only ever used for a one-series chart: stacked washes would imply a
// part-to-whole relationship these metrics do not have.
export function areaPath(samples: Sample[], plot: Plot, gapMs = Infinity): string {
  if (samples.length === 0) return "";
  const line = linePath(samples, plot, gapMs);
  if (!line) return "";
  // Each pen-lift starts a new sub-area; close every one on the baseline so a
  // gap does not swallow the fill between two islands of data.
  const base = round(plot.y(0) > plot.bottom ? plot.bottom : Math.max(plot.y(0), plot.top));
  return line
    .split(/(?=M)/)
    .map((seg) => {
      const pts = seg.trim().match(/[ML]-?[\d.]+,-?[\d.]+/g);
      if (!pts || pts.length === 0) return "";
      const first = pts[0].slice(1).split(",")[0];
      const last = pts[pts.length - 1].slice(1).split(",")[0];
      return `${seg.trim()} L${last},${base} L${first},${base} Z`;
    })
    .filter(Boolean)
    .join(" ");
}

const round = (n: number) => Math.round(n * 100) / 100;

// frameTimes is the shared x grid the crosshair snaps to: every instant any
// series has a sample at, in order. Series do not always agree — a differentiated
// counter has no value at the first instant — so the union is what lets one
// crosshair position address all of them.
export function frameTimes(series: ChartSeries[]): number[] {
  const seen = new Set<number>();
  for (const s of series) for (const p of s.samples) seen.add(p.t);
  return [...seen].sort((a, b) => a - b);
}

// nearestBy finds the entry of a SORTED range closest to t, addressing it through
// `at` so the same binary search serves a number[] and a Sample[] without either
// one having to be copied into the other's shape.
function nearestBy(length: number, at: (i: number) => number, t: number): number {
  if (length === 0) return -1;
  let lo = 0;
  let hi = length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (at(mid) < t) lo = mid + 1;
    else hi = mid;
  }
  // lo is the first entry >= t; its predecessor may be the closer one.
  if (lo > 0 && Math.abs(at(lo - 1) - t) <= Math.abs(at(lo) - t)) return lo - 1;
  return lo;
}

// nearestIndex finds the entry of a SORTED array closest to t.
export const nearestIndex = (times: number[], t: number): number =>
  nearestBy(times.length, (i) => times[i], t);

// valueAt reads a series at a crosshair instant, within `tolerance`. Outside it
// the answer is null — the tooltip then omits that series rather than quoting a
// number from a minute away as if it were the one under the pointer.
export function valueAt(samples: Sample[], t: number, tolerance: number): number | null {
  const i = nearestBy(samples.length, (j) => samples[j].t, t);
  if (i < 0) return null;
  return Math.abs(samples[i].t - t) <= tolerance ? samples[i].v : null;
}
