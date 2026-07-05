// Canned observability-store answers for /.cornus/web/observe/*.
//
// Shared by the component tests (src/mock/handler.ts) and the standalone mock
// dev server (mock/server.ts), like the rest of the fixtures.
//
// The series are GENERATED rather than hand-listed because a metrics dashboard's
// whole job is shape over time: a fixture of three points cannot show a line, a
// gap, a counter reset, or a legend that has to fold. Values come from a seeded
// hash of (metric, series, sample index) — never of the wall clock — so the same
// query draws the same picture on every run while the window still ends at "now".
//
// The modelled store carries BOTH deploy backends' metric spellings. NOT because a
// server runs two backends — it constructs exactly one for its whole lifetime, from
// CORNUS_DEPLOY_BACKEND, and stamps that one name on every status it reports. A
// store spans two because it OUTLIVES that choice: the series are on disk under
// `--obs`, so flipping the backend and restarting leaves the old spelling recorded
// beside the new one. That is the state the UI has to render, and it exercises
// several real behaviours:
//
//   - The shop-* workloads are host-backed: cumulative `container_cpu_time`, plus
//     network and disk counters.
//   - `edge-cache` is kubernetes-backed: instantaneous `container_cpu_usage` and
//     memory only, because metrics.k8s.io has no network or disk counters. So a
//     panel that merges the two CPU spellings has data from BOTH sources, and a
//     project-scoped chart has a workload it must exclude.
//   - `cornus_container_memory_limit` is served for ONE workload, because a limit
//     is recorded only where one is enforced.
//
// A test that needs the "nothing has ever reported this" state asks for it with
// setUnservedMetrics(), rather than relying on a gap in the fixture.

import type { MetricSeries, ObsStatus } from "../api";

export const obsStatus: ObsStatus = {
  dir: "/var/lib/cornus/obs",
  tables: [
    { name: "logs", rows: 48211, segments: 12 },
    { name: "spans", rows: 3120, segments: 4 },
    { name: "metrics_gauge", rows: 90400, segments: 21 },
    { name: "metrics_sum", rows: 61200, segments: 15 },
  ],
  bufferBytes: 1_048_576,
  walBytes: 8_388_608,
  dropped: 0,
  errors: 0,
  // Go durations are nanoseconds on the wire: 24h retention, a 2s sampler.
  retention: 24 * 3.6e12,
  maxBytes: 2 * 1024 ** 3,
  metrics: { interval: 2e9, replicas: 4, sampled: 18240, failed: 0, dropped: 0 },
};

// The workloads the mock server is "running", matching fixtures.ts. shop-web has
// two replicas so at least one panel exercises a multi-series legend.
const HOST_REPLICAS: Array<{ service: string; replica: string }> = [
  { service: "shop-web", replica: "0" },
  { service: "shop-web", replica: "1" },
  { service: "shop-db", replica: "0" },
  { service: "shop-worker", replica: "0" },
];

// The kubernetes-backed deployment. It is deliberately NOT in fixtures.ts's
// workload list: the store outlives and outreaches the compose project, and a
// project-scoped chart has to exclude it on the labels rather than on the roster.
const KUBE_REPLICAS: Array<{ service: string; replica: string }> = [
  { service: "edge-cache", replica: "0" },
];

// unserved is what this server has never recorded. Tests set it to reach the
// store's "metric is not resolved" 400 — the state the UI must render as "nothing
// has reported this" rather than as an error. Reset per installMockFetch.
let unserved = new Set<string>();
export function setUnservedMetrics(names: string[]): void {
  unserved = new Set(names);
}

// unsupported is what this server's DEPLOY BACKEND has no source for, which is a
// different claim from `unserved` and gets opposite treatment: an unserved metric
// is drawn as an empty panel that might yet fill, an unsupported one is not drawn
// at all. Names are the recorded (dotted) spelling the server reports, not the
// query spelling, so a test also exercises the fold the UI has to do.
//
// Empty by default: the fixture server is deliberately mixed (a host-backed and a
// kube-backed workload in one store), which is not a state any single backend can
// declare, and defaulting to a declaration would hide panels from every test that
// does not ask about this. Reset per installMockFetch.
let unsupported: string[] = [];
export function setUnsupportedMetrics(names: string[]): void {
  unsupported = names;
}

// obsStatusNow is the status as the mock server would answer it right now — the
// fixture plus whatever a test has declared unsupported.
export function obsStatusNow(): ObsStatus {
  if (!unsupported.length) return obsStatus;
  return { ...obsStatus, metrics: { ...obsStatus.metrics!, unsupported } };
}

// hash is a small deterministic string→[0,1) generator (FNV-1a, scaled). Not
// random, just uncorrelated enough to look like a measurement.
function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 100000) / 100000;
}

// parseDuration reads the Go durations the API speaks ("15m", "90s", "1h30m").
export function parseDuration(v: string): number {
  const units: Record<string, number> = { s: 1000, m: 60000, h: 3600000 };
  let total = 0;
  let matched = false;
  for (const m of v.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)) {
    matched = true;
    total += Number(m[1]) * (m[2] === "ms" ? 1 : units[m[2]]);
  }
  return matched ? total : 0;
}

interface Shape {
  // base and swing describe a wandering gauge; counters integrate `base` per
  // second instead of plotting it.
  base: number;
  swing: number;
  counter: boolean;
  // labelled per-series extras beyond service/replica (io directions, cpu modes).
  variants?: Array<Record<string, string>>;
  // server marks a metric recorded about the cornus process rather than a
  // workload: one series, labelled with the server's own service name.
  server?: boolean;
  // on names which population reports this metric. Default is the host-backed
  // workloads; "kube" is the kubernetes-backed one, "all" is both.
  on?: "host" | "kube" | "all";
  // integer rounds the samples. Goroutines and PIDs are counted things; a
  // fractional one would be the mock admitting it made the number up.
  integer?: boolean;
}

const SHAPES: Record<string, Shape> = {
  container_cpu_time: {
    base: 0.18,
    swing: 0.12,
    counter: true,
    variants: [{ cpu_mode: "user" }, { cpu_mode: "system" }],
  },
  container_cpu_usage: { base: 0.26, swing: 0.18, counter: false, on: "kube" },
  container_memory_usage: { base: 180 * 1024 ** 2, swing: 60 * 1024 ** 2, counter: false, on: "all" },
  cornus_container_memory_limit: { base: 512 * 1024 ** 2, swing: 0, counter: false },
  container_network_io: {
    base: 240 * 1024,
    swing: 200 * 1024,
    counter: true,
    variants: [
      { network_io_direction: "receive", network_interface_name: "eth0" },
      { network_io_direction: "transmit", network_interface_name: "eth0" },
    ],
  },
  container_disk_io: {
    base: 90 * 1024,
    swing: 120 * 1024,
    counter: true,
    variants: [{ disk_io_direction: "read" }, { disk_io_direction: "write" }],
  },
  cornus_container_pids: { base: 22, swing: 9, counter: false, integer: true },

  process_cpu_time: { base: 0.06, swing: 0.05, counter: true, server: true },
  process_memory_usage: { base: 140 * 1024 ** 2, swing: 20 * 1024 ** 2, counter: false, server: true },
  go_memory_used: { base: 96 * 1024 ** 2, swing: 18 * 1024 ** 2, counter: false, server: true },
  go_goroutine_count: { base: 84, swing: 22, counter: false, server: true, integer: true },
  process_thread_count: { base: 19, swing: 4, counter: false, server: true, integer: true },
  process_open_file_descriptor_count: { base: 41, swing: 12, counter: false, server: true, integer: true },
  cornus_server_network_io: {
    base: 60 * 1024,
    swing: 40 * 1024,
    counter: true,
    server: true,
    variants: [
      { network_io_direction: "receive" },
      { network_io_direction: "transmit" },
    ],
  },
  cornus_builds: { base: 0, swing: 0, counter: false, server: true },
  cornus_deploys: { base: 0, swing: 0, counter: false, server: true },
};

// COUNTED are the two cumulative totals drawn raw rather than differentiated:
// they climb in whole steps, which is exactly what "builds so far" looks like.
const COUNTED = new Set(["cornus_builds", "cornus_deploys"]);

// notResolved is the store's own answer for a metric nothing has ever reported —
// a 400 with a diagnostic, not an empty result.
export function notResolved(metric: string): string {
  return `obsstore: promql: metric ${JSON.stringify(metric)} is not resolved`;
}

export function hasMetric(metric: string): boolean {
  return metric in SHAPES && !unserved.has(metric);
}

// mockMetrics answers one range query. `now` is injectable so a test can pin the
// window instead of racing the clock.
export function mockMetrics(
  metric: string,
  since: string,
  step: string,
  now = Date.now(),
): MetricSeries[] {
  const shape = SHAPES[metric];
  if (!shape) return [];
  const stepMs = parseDuration(step) || 60000;
  const spanMs = parseDuration(since) || 3600000;
  const count = Math.max(2, Math.min(400, Math.floor(spanMs / stepMs)));
  const start = now - count * stepMs;

  const population =
    shape.on === "kube" ? KUBE_REPLICAS : shape.on === "all" ? [...HOST_REPLICAS, ...KUBE_REPLICAS] : HOST_REPLICAS;
  const bases = shape.server
    ? [{ service: "cornus" }]
    : population.map((r) => ({ service: r.service, cornus_replica: r.replica }));
  const variants = shape.variants ?? [{}];

  const out: MetricSeries[] = [];
  for (const base of bases) {
    // A memory LIMIT exists only where one is enforced: one workload here.
    if (metric === "cornus_container_memory_limit" && base.service !== "shop-db") continue;
    for (const variant of variants) {
      const labels = { ...base, ...variant };
      const seed = metric + JSON.stringify(labels);
      const scale = 0.6 + hash(seed) * 0.8;
      const points = [];
      let cumulative = hash(seed + "start") * 1000;
      let counted = Math.floor(hash(seed + "n") * 5);
      // Two out-of-phase waves plus a little jitter, rather than independent
      // noise per sample: a real gauge WANDERS, and a chart of white noise is a
      // smear that hides every rendering decision the dashboard makes.
      const phase = hash(seed + "phase") * Math.PI * 2;
      const period = 9 + hash(seed + "period") * 26;
      for (let i = 0; i < count; i++) {
        const t = start + i * stepMs;
        const wobble =
          Math.sin(phase + i / period) * 0.5 +
          Math.sin(phase * 1.7 + i / (period * 0.37)) * 0.2 +
          (hash(`${seed}:${i}`) - 0.5) * 0.15;
        const perSecond = Math.max(0, (shape.base + wobble * shape.swing * 2) * scale);
        let v: number;
        if (COUNTED.has(metric)) {
          // A whole-number total that only ever climbs, in occasional steps.
          if (hash(`${seed}:step:${i}`) > 0.88) counted++;
          v = counted;
        } else if (shape.counter) {
          cumulative += (perSecond * stepMs) / 1000;
          v = cumulative;
        } else {
          v = shape.integer ? Math.round(perSecond) : perSecond;
        }
        points.push({ t: new Date(t).toISOString(), v });
      }
      out.push({ labels, points });
    }
  }
  return out;
}
