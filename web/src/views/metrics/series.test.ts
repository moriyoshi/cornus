import { describe, it, expect } from "vitest";
import {
  assignSlots,
  buildSeries,
  canonicalKey,
  capSeries,
  differentiate,
  formatClock,
  formatStamp,
  formatTick,
  formatTicks,
  formatValue,
  seriesLabel,
  summarize,
  toSamples,
  type ChartSeries,
} from "./series";
import type { MetricSeries } from "../../api";

// These cover the arithmetic the dashboard does to the store's answers before
// anything is drawn — the part where a wrong result is a plausible-looking chart
// rather than a crash.

const at = (iso: string, v: number) => ({ t: iso, v });
const ms = (iso: string) => Date.parse(iso);

describe("toSamples", () => {
  it("parses, sorts, and drops what cannot be plotted", () => {
    const got = toSamples({
      points: [
        at("2026-08-02T10:02:00Z", 2),
        at("2026-08-02T10:00:00Z", 1),
        // Go's zero time: a sample the store never really had.
        at("0001-01-01T00:00:00Z", 99),
        at("not a time", 5),
        at("2026-08-02T10:04:00Z", NaN),
      ],
    });
    expect(got).toEqual([
      { t: ms("2026-08-02T10:00:00Z"), v: 1 },
      { t: ms("2026-08-02T10:02:00Z"), v: 2 },
    ]);
  });
});

describe("differentiate", () => {
  it("turns a cumulative counter into a per-second rate at the later instant", () => {
    const got = differentiate([
      { t: 0, v: 100 },
      { t: 10_000, v: 130 },
      { t: 20_000, v: 190 },
    ]);
    // 30 units over 10s, then 60 over 10s. The first sample yields no rate: a
    // difference needs two points, and inventing one at t=0 would draw a spike
    // out of the window's edge.
    expect(got).toEqual([
      { t: 10_000, v: 3 },
      { t: 20_000, v: 6 },
    ]);
  });

  it("reads a decrease as a counter reset, never as negative traffic", () => {
    // The container was replaced: the counter restarted at 0 and has since
    // reached 40. A plain difference would draw -160 here, a deep spike at
    // exactly the moment the chart most needs to stay readable.
    const got = differentiate([
      { t: 0, v: 200 },
      { t: 10_000, v: 40 },
    ]);
    expect(got).toEqual([{ t: 10_000, v: 4 }]);
    expect(got[0].v).toBeGreaterThan(0);
  });

  it("skips samples that do not advance the clock", () => {
    expect(
      differentiate([
        { t: 5_000, v: 1 },
        { t: 5_000, v: 9 },
      ]),
    ).toEqual([]);
  });
});

describe("seriesLabel", () => {
  it("spells the replica ordinal and lets self-describing values speak", () => {
    // Key order, not label order: the parts follow the sorted label keys
    // (cornus_replica before cpu_mode) so the same series always reads the same
    // way, whatever order the store happened to return the labels in.
    const labels = { service: "shop-web", cornus_replica: "1", cpu_mode: "user" };
    expect(seriesLabel(labels, false)).toBe("replica 1 · user");
    expect(seriesLabel(labels, true)).toBe("shop-web · replica 1 · user");
  });

  it("falls back to the service when nothing else distinguishes the series", () => {
    expect(seriesLabel({ service: "cornus" }, false)).toBe("cornus");
    expect(seriesLabel({}, false)).toBe("series");
  });
});

describe("buildSeries", () => {
  const raw: MetricSeries[] = [
    {
      labels: { service: "shop-web", cornus_replica: "0" },
      points: [at("2026-08-02T10:00:00Z", 10), at("2026-08-02T10:00:10Z", 30)],
    },
    {
      labels: { service: "shop-db", cornus_replica: "0" },
      points: [at("2026-08-02T10:00:00Z", 5), at("2026-08-02T10:00:10Z", 5)],
    },
  ];

  it("filters to one deployment on the returned labels", () => {
    const got = buildSeries(raw, { counter: false, services: ["shop-db"] });
    expect(got.map((s) => s.label)).toEqual(["replica 0"]);
    expect(got[0].samples).toHaveLength(2);
  });

  it("distinguishes an EMPTY scope from an absent one", () => {
    // Absent means "every service"; empty means "a set that happens to have no
    // members" — a project with nothing deployed. Collapsing the two would draw
    // the whole server's traffic under that project's heading.
    expect(buildSeries(raw, { counter: false })).toHaveLength(2);
    expect(buildSeries(raw, { counter: false, services: [] })).toEqual([]);
  });

  it("names the service only while more than one is on the chart", () => {
    expect(buildSeries(raw, { counter: false }).map((s) => s.label)).toEqual([
      "shop-web · replica 0",
      "shop-db · replica 0",
    ]);
    expect(buildSeries(raw, { counter: false, services: ["shop-web"] })[0].label).toBe("replica 0");
  });

  it("differentiates counters and drops a series left with no rate", () => {
    const rated = buildSeries(raw, { counter: true });
    expect(rated).toHaveLength(2);
    expect(rated[0].samples).toEqual([{ t: ms("2026-08-02T10:00:10Z"), v: 2 }]);

    const single: MetricSeries[] = [
      { labels: { service: "shop-web" }, points: [at("2026-08-02T10:00:00Z", 1)] },
    ];
    expect(buildSeries(single, { counter: true })).toEqual([]);
  });

  it("keys a series by its whole label set, not by its position", () => {
    const [web] = buildSeries(raw, { counter: false, services: ["shop-web"] });
    expect(web.key).toBe(canonicalKey({ cornus_replica: "0", service: "shop-web" }));
    expect(web.key).toBe('{cornus_replica="0", service="shop-web"}');
  });
});

describe("capSeries", () => {
  const series = (label: string, peak: number): ChartSeries => ({
    key: `{k=${label}}`,
    label,
    samples: [
      { t: 0, v: 0 },
      { t: 1000, v: peak },
    ],
  });

  it("passes a small set through, ordered by label", () => {
    const got = capSeries([series("b", 1), series("a", 2)]);
    expect(got.hidden).toBe(0);
    expect(got.shown.map((s) => s.label)).toEqual(["a", "b"]);
  });

  it("keeps the largest eight and COUNTS the rest", () => {
    const list = Array.from({ length: 11 }, (_, i) => series(`s${i}`, i));
    const got = capSeries(list);
    expect(got.shown).toHaveLength(8);
    expect(got.hidden).toBe(3);
    // The three smallest are the ones withheld — a chart that dropped the
    // biggest consumers would be worse than one that drew nothing.
    expect(got.shown.map((s) => s.label).sort()).not.toContain("s0");
    expect(got.shown.map((s) => s.label)).toContain("s10");
  });
});

describe("assignSlots", () => {
  it("gives each new key the lowest free slot", () => {
    const got = assignSlots(new Map(), ["a", "b", "c"]);
    expect([...got.entries()]).toEqual([
      ["a", 0],
      ["b", 1],
      ["c", 2],
    ]);
  });

  it("never repaints a series when its neighbours come and go", () => {
    // A replica that disappears for one poll and returns must return the same
    // color, and a newly appearing replica must not shift anyone else.
    const first = assignSlots(new Map(), ["a", "b"]);
    const gone = assignSlots(first, ["a"]);
    const back = assignSlots(gone, ["a", "b", "c"]);
    expect(back.get("a")).toBe(first.get("a"));
    expect(back.get("b")).toBe(first.get("b"));
    expect(back.get("c")).toBe(2);
  });

  it("leaves the caller's map untouched", () => {
    const prev = new Map([["a", 0]]);
    assignSlots(prev, ["b"]);
    expect([...prev.keys()]).toEqual(["a"]);
  });
});

describe("summarize", () => {
  it("reports the four numbers the table view quotes", () => {
    expect(
      summarize([
        { t: 0, v: 2 },
        { t: 1, v: 8 },
        { t: 2, v: 5 },
      ]),
    ).toEqual({ last: 5, min: 2, max: 8, avg: 5 });
    expect(summarize([])).toBeNull();
  });
});

describe("formatting", () => {
  it("scales bytes in binary units", () => {
    expect(formatValue("bytes", 512)).toBe("512 B");
    expect(formatValue("bytes", 1024)).toBe("1.00 KiB");
    expect(formatValue("bytes", 180 * 1024 ** 2)).toBe("180 MiB");
    expect(formatValue("bytesPerSecond", 2 * 1024 ** 2)).toBe("2.00 MiB/s");
  });

  it("names cores in the readout and drops the word on the axis", () => {
    expect(formatValue("cores", 0.4213)).toBe("0.421 cores");
    expect(formatValue("cores", 1)).toBe("1.00 core");
    expect(formatTick("cores", 0.5)).toBe("0.5");
  });

  it("groups counts and refuses to invent a number it does not have", () => {
    expect(formatValue("count", 12840)).toBe("12,840");
    expect(formatValue("count", NaN)).toBe("—");
  });

  it("renders a byte axis at one precision, in the unit it was stepped in", () => {
    const MiB = 1024 ** 2;
    // Formatted one at a time these come out as "100 MiB" beside "75.0 MiB":
    // two precisions on one axis. Zero keeps its plain spelling.
    expect(formatTicks("bytes", [0, 25 * MiB, 50 * MiB, 75 * MiB, 100 * MiB], MiB)).toEqual([
      "0 B",
      "25 MiB",
      "50 MiB",
      "75 MiB",
      "100 MiB",
    ]);
    // A half step takes the decimal it needs — and every tick takes it too.
    expect(formatTicks("bytes", [0, 2.5 * MiB, 5 * MiB], MiB)).toEqual([
      "0 B",
      "2.5 MiB",
      "5.0 MiB",
    ]);
    expect(formatTicks("bytesPerSecond", [0, 512 * 1024], 1024)).toEqual(["0 B/s", "512 KiB/s"]);
    // Non-byte units are untouched by the shared-precision rule.
    expect(formatTicks("cores", [0, 0.5, 1])).toEqual(["0", "0.5", "1.00"]);
  });

  it("shows seconds only on a window short enough for them to differ", () => {
    const t = new Date(2026, 7, 2, 14, 5, 30).getTime();
    expect(formatClock(t)).toBe("14:05");
    expect(formatClock(t, true)).toBe("14:05:30");
    expect(formatStamp(t, 15 * 60 * 1000)).toBe("14:05:30");
    expect(formatStamp(t, 6 * 60 * 60 * 1000)).toBe("14:05");
    expect(formatStamp(t, 24 * 60 * 60 * 1000)).toBe("8/2 14:05");
  });
});
