import { describe, it, expect } from "vitest";
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
} from "./scales";
import type { ChartSeries } from "./series";

const series = (label: string, samples: Array<[number, number]>): ChartSeries => ({
  key: `{k="${label}"}`,
  label,
  samples: samples.map(([t, v]) => ({ t, v })),
});

const box = { width: 200, height: 100, padLeft: 0, padRight: 0, padTop: 0, padBottom: 0 };

describe("extent", () => {
  it("anchors the value axis at zero", () => {
    // 100 and 104 against a floating baseline would look like a doubling.
    const e = extent([series("a", [[0, 100], [1000, 104]])])!;
    expect(e.v0).toBe(0);
    expect(e.v1).toBe(104);
  });

  it("opens a range for a flat series so the scale is not divided by zero", () => {
    const e = extent([series("a", [[0, 5], [1000, 5]])])!;
    expect(e.v1).toBeGreaterThan(e.v0);
    const p = makePlot(e, box);
    expect(Number.isFinite(p.y(5))).toBe(true);
  });

  it("still opens downward for a negative value rather than hiding it", () => {
    const e = extent([series("a", [[0, -3], [1000, 4]])])!;
    expect(e.v0).toBe(-3);
  });

  it("is null when there is nothing to draw", () => {
    expect(extent([])).toBeNull();
    expect(extent([series("a", [])])).toBeNull();
  });
});

describe("niceTicks", () => {
  it("steps in round numbers and always spans the data", () => {
    const ticks = niceTicks(0, 37, 4);
    expect(ticks[0]).toBe(0);
    expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(37);
    for (const t of ticks) expect(Number.isInteger(t / 10)).toBe(true);
  });

  it("steps a byte axis in powers of 1024, so a tick reads as 1.00 GiB", () => {
    const scale = binaryScale(3.5 * 1024 ** 3);
    expect(scale).toBe(1024 ** 3);
    const ticks = niceTicks(0, 3.5 * 1024 ** 3, 4, scale);
    // Every tick is a whole number of GiB rather than a decimal step that would
    // format as 1.19 GiB.
    for (const t of ticks) expect(Number.isInteger(t / 1024 ** 3)).toBe(true);
    expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(3.5 * 1024 ** 3);
  });

  it("keeps a top tick that lands exactly on a round number", () => {
    // Repeated addition leaves floating-point residue; without the epsilon the
    // last tick of a 0..100 axis is emitted twice or not at all.
    const ticks = niceTicks(0, 100, 4);
    expect(ticks).toContain(100);
    expect(ticks.filter((t) => t === 100)).toHaveLength(1);
  });

  it("clears a maximum that falls between two steps", () => {
    // 426 MiB on a 200 MiB step: the axis must reach 600, not stop at 400. The
    // chart stretches its domain to the last tick, so a short axis pins the line
    // to the top edge of the plot with no headroom — which is how this was found.
    const MiB = 1024 ** 2;
    const ticks = niceTicks(0, 426 * MiB, 4, MiB);
    expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(426 * MiB);
    expect(ticks[ticks.length - 1] / MiB).toBe(600);
  });

  it("spans data whose maximum is not a round number, at any scale", () => {
    for (const max of [1, 7, 37, 99, 101, 426, 1023, 4096, 0.37, 0.041]) {
      const ticks = niceTicks(0, max, 4);
      expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(max);
      expect(ticks[0]).toBeLessThanOrEqual(0);
    }
  });
});

describe("timeTicks", () => {
  it("spans the window the reader chose, edge to edge", () => {
    const ticks = timeTicks(1000, 5000, 5);
    expect(ticks[0]).toBe(1000);
    expect(ticks[ticks.length - 1]).toBe(5000);
    expect(ticks).toHaveLength(5);
  });
});

describe("linePath", () => {
  const plot = makePlot({ t0: 0, t1: 100, v0: 0, v1: 100 }, box);

  it("draws one stroke through adjacent samples", () => {
    const d = linePath([{ t: 0, v: 0 }, { t: 50, v: 50 }, { t: 100, v: 100 }], plot);
    expect(d.match(/M/g)).toHaveLength(1);
    expect(d).toBe("M0,100 L100,50 L200,0");
  });

  it("lifts the pen across a gap instead of inventing a measurement", () => {
    // A period the store has no data for must READ as missing. A straight line
    // across it is a number nobody recorded.
    const d = linePath([{ t: 0, v: 10 }, { t: 10, v: 20 }, { t: 90, v: 30 }], plot, 20);
    expect(d.match(/M/g)).toHaveLength(2);
    expect(d.startsWith("M")).toBe(true);
  });
});

describe("areaPath", () => {
  const plot = makePlot({ t0: 0, t1: 100, v0: 0, v1: 100 }, box);

  it("closes the fill on the baseline", () => {
    const d = areaPath([{ t: 0, v: 50 }, { t: 100, v: 60 }], plot);
    expect(d.endsWith("Z")).toBe(true);
    // The baseline is y(0), the bottom of the plot box.
    expect(d).toContain(`,${plot.y(0)}`);
  });

  it("closes each island separately when the data has a gap", () => {
    const d = areaPath([{ t: 0, v: 10 }, { t: 10, v: 20 }, { t: 90, v: 30 }], plot, 20);
    expect(d.match(/Z/g)).toHaveLength(2);
  });

  it("is empty when there is nothing to fill", () => {
    expect(areaPath([], plot)).toBe("");
  });
});

describe("frameTimes", () => {
  it("is the union of every series' instants, in order and deduplicated", () => {
    const got = frameTimes([
      series("a", [[30, 1], [10, 1]]),
      series("b", [[20, 1], [10, 1]]),
    ]);
    expect(got).toEqual([10, 20, 30]);
  });
});

describe("nearestIndex", () => {
  it("finds the closest instant on either side", () => {
    const times = [0, 100, 200, 300];
    expect(nearestIndex(times, -50)).toBe(0);
    expect(nearestIndex(times, 140)).toBe(1);
    expect(nearestIndex(times, 160)).toBe(2);
    expect(nearestIndex(times, 9999)).toBe(3);
    expect(nearestIndex([], 5)).toBe(-1);
  });
});

describe("valueAt", () => {
  const samples = [
    { t: 0, v: 1 },
    { t: 100, v: 2 },
  ];

  it("reads the sample under the crosshair", () => {
    expect(valueAt(samples, 105, 30)).toBe(2);
  });

  it("refuses to quote a value from outside the tolerance", () => {
    // Otherwise a series that stopped reporting would keep answering the
    // crosshair with its last value, as if it were still being measured.
    expect(valueAt(samples, 400, 30)).toBeNull();
    expect(valueAt([], 0, 30)).toBeNull();
  });
});
