// What the metrics dashboard plots, and how it asks for it.
//
// Every panel queries a BARE METRIC NAME — `container_memory_usage`, never
// `rate(container_cpu_time[5m])` or `sum by (service) (…)`. That is a deliberate
// restriction, not an oversight. The store's PromQL is a compatibility PROFILE
// (imbh-lgtm), and a construct outside it is rejected with a 400 rather than
// approximated; the only spelling this repo verifies end-to-end against a live
// store is the bare selector (`e2e/scenarios/observability-metrics.star` queries
// nothing else). A dashboard that assumed more would fail as a wall of 400s on
// the day someone actually enabled `--obs`.
//
// So the two things a bare selector cannot do, this module does itself:
//
//   - Per-second RATES for cumulative counters — see `differentiate` in series.ts.
//   - FILTERING by service — done on the returned label sets in series.ts, which
//     also sidesteps the `cornus.replica` / `cornus_replica` footgun entirely
//     (a matcher with a dot in it silently matches zero series).
//
// Metric names, units, and labels are the ones cornus records for you with
// `--obs`; see docs/cli/observe.md, which is the contract this catalog tracks.

// Scope splits the two populations that share the store: the workloads cornus
// deploys, and the cornus server process itself. They never belong on one chart
// (different subjects, different scales), so they are different panel sets.
export type Scope = "workloads" | "server";

// Unit drives formatting and the y-axis. `bytesPerSecond` and `cores` are what a
// counter panel becomes once differentiated, never what the store returns.
export type Unit = "bytes" | "bytesPerSecond" | "cores" | "count" | "seconds";

// Source is one metric a panel draws.
//
// A panel usually has exactly one. It has more when the SAME quantity is recorded
// under different names by different deploy backends — CPU is cumulative seconds
// on a host backend and an instantaneous core count on kubernetes — and merging
// them is only legitimate because they share a unit and therefore an axis. A
// server running both kinds of workload correctly shows both, each series labelled
// with its own deployment.
export interface Source {
  // metric is the PromQL spelling: OpenTelemetry dots resolve as underscores and
  // there is no unit suffix (`container_cpu_time`, not `..._seconds_total`).
  metric: string;
  // counter marks a CUMULATIVE series that must be differentiated before it means
  // anything. A cumulative counter drawn raw is a monotonically rising line that
  // tells the reader nothing about now.
  counter: boolean;
}

export interface Panel {
  id: string;
  title: string;
  sources: Source[];
  unit: Unit;
  scope: Scope;
  // hint explains what the panel shows and, as importantly, why it might be
  // empty — several of these metrics exist only on one deploy backend.
  hint: string;
}

// metricNames is the panel's sources as the store spells them, for the subtitle
// and for the "nothing has reported this" message.
export const metricNames = (p: Panel): string[] => p.sources.map((s) => s.metric);

export const PANELS: Panel[] = [
  {
    id: "cpu",
    title: "CPU",
    sources: [{ metric: "container_cpu_time", counter: true }],
    unit: "cores",
    scope: "workloads",
    hint: "Cumulative CPU seconds per replica, differentiated into cores in use. The kubernetes backend has no cumulative source and reports the instantaneous figure below instead.",
  },
  {
    id: "cpu-instant",
    title: "CPU (instantaneous)",
    sources: [{ metric: "container_cpu_usage", counter: false }],
    unit: "cores",
    scope: "workloads",
    hint: "Kubernetes only — the backends that expose cumulative CPU time record that instead, and this panel stays empty for them.",
  },
  {
    id: "memory",
    title: "Memory in use",
    sources: [{ metric: "container_memory_usage", counter: false }],
    unit: "bytes",
    scope: "workloads",
    hint: "Memory in use excluding reclaimable page cache — the same figure `docker stats` shows.",
  },
  {
    id: "memory-limit",
    title: "Memory limit",
    sources: [{ metric: "cornus_container_memory_limit", counter: false }],
    unit: "bytes",
    scope: "workloads",
    hint: "The enforced limit, recorded only for replicas that have one.",
  },
  {
    id: "network",
    title: "Network I/O",
    sources: [{ metric: "container_network_io", counter: true }],
    unit: "bytesPerSecond",
    scope: "workloads",
    hint: "Cumulative traffic per direction and interface, differentiated. Not available on the kubernetes backend.",
  },
  {
    id: "disk",
    title: "Disk I/O",
    sources: [{ metric: "container_disk_io", counter: true }],
    unit: "bytesPerSecond",
    scope: "workloads",
    hint: "Cumulative block I/O per direction, differentiated. Not available on the kubernetes backend.",
  },
  {
    id: "pids",
    title: "Processes",
    sources: [{ metric: "cornus_container_pids", counter: false }],
    unit: "count",
    scope: "workloads",
    hint: "Processes and threads inside the container.",
  },

  {
    id: "server-cpu",
    title: "Server CPU",
    sources: [{ metric: "process_cpu_time", counter: true }],
    unit: "cores",
    scope: "server",
    hint: "The cornus server process's own CPU time, differentiated into cores.",
  },
  {
    id: "server-memory",
    title: "Server memory",
    sources: [{ metric: "process_memory_usage", counter: false }],
    unit: "bytes",
    scope: "server",
    hint: "Resident memory of the cornus server process.",
  },
  {
    id: "server-go-memory",
    title: "Go heap in use",
    sources: [{ metric: "go_memory_used", counter: false }],
    unit: "bytes",
    scope: "server",
    hint: "Memory the Go runtime reports as in use, which is a subset of the process's resident set.",
  },
  {
    id: "server-goroutines",
    title: "Goroutines",
    sources: [{ metric: "go_goroutine_count", counter: false }],
    unit: "count",
    scope: "server",
    hint: "Live goroutines. A line that only climbs is the shape of a leak.",
  },
  {
    id: "server-threads",
    title: "OS threads",
    sources: [{ metric: "process_thread_count", counter: false }],
    unit: "count",
    scope: "server",
    hint: "Operating-system threads backing the runtime.",
  },
  {
    id: "server-fds",
    title: "Open file descriptors",
    sources: [{ metric: "process_open_file_descriptor_count", counter: false }],
    unit: "count",
    scope: "server",
    hint: "Descriptors held by the server process.",
  },
  {
    id: "server-network",
    title: "Server network I/O",
    sources: [{ metric: "cornus_server_network_io", counter: true }],
    unit: "bytesPerSecond",
    scope: "server",
    hint: "Namespace-scoped, not per-process: in a container this is the server's traffic, on a host install it is the whole host's. The `cornus_` prefix marks exactly that difference.",
  },
  {
    id: "server-builds",
    title: "Builds",
    sources: [{ metric: "cornus_builds", counter: false }],
    unit: "count",
    scope: "server",
    hint: "Cumulative build count since the server started, drawn as it accumulates. Both build routes — the tar upload and the `cornus build` session — count into this one series.",
  },
  {
    id: "server-deploys",
    title: "Deploys",
    sources: [{ metric: "cornus_deploys", counter: false }],
    unit: "count",
    scope: "server",
    hint: "Cumulative count of deploy operations that CHANGED something (apply, delete, start, stop, restart) since the server started. Read-only list/status requests are not counted, so polling a dashboard does not move this line.",
  },
];

export const panelsFor = (scope: Scope): Panel[] => PANELS.filter((p) => p.scope === scope);

// promName folds a RECORDED metric name into the spelling a query uses. The
// server reports unsupported families as OpenTelemetry spells them
// (`container.network.io`) because that is what the recorder records; the store
// resolves the dots itself at query time, and this catalog has always held the
// resolved form. One `replaceAll` is the whole conversion — and doing it here,
// once, is why nothing else in the UI has to know there are two spellings.
export const promName = (metric: string): string => metric.replaceAll(".", "_");

// supported drops the panels this server's deploy backend can NEVER fill.
//
// A panel survives if ANY of its sources is still collectable, which is what
// makes the compact strip's merged CPU panel behave: on kubernetes
// `container_cpu_time` is unsupported and `container_cpu_usage` is not, so the
// panel stays and draws the one that exists.
//
// The empty case is deliberately permissive. An empty or absent list means the
// server made no claim — an older server, or a backend that declares nothing —
// and the answer to "no claim" is to show everything, because a panel that is
// empty for a reason the reader can look up beats a panel that silently is not
// there. Hiding is only ever driven by a positive statement that the source does
// not exist.
export const supported = (panels: Panel[], unsupported?: string[]): Panel[] => {
  if (!unsupported?.length) return panels;
  const gone = new Set(unsupported.map(promName));
  return panels.filter((p) => p.sources.some((s) => !gone.has(promName(s.metric))));
};

export const panelById = (id: string): Panel | undefined => PANELS.find((p) => p.id === id);

// hiddenTitles names the workload panels `supported` drops, so a screen that
// hides them can say what it hid.
//
// Hiding a chart without saying so trades one confusion for a worse one: the
// reader who wondered why the network panel was empty at least knew it existed,
// while a reader who never sees it cannot tell an unavailable metric from a UI
// that forgot to draw it. One line naming the absences keeps the dashboard short
// AND answerable.
export const hiddenTitles = (unsupported?: string[]): string[] => {
  const all = panelsFor("workloads");
  const shown = new Set(supported(all, unsupported).map((p) => p.id));
  return all.filter((p) => !shown.has(p.id)).map((p) => p.title);
};

// COMPACT_PANELS is the two-panel strip the project and workload sections carry:
// the pair a reader glances at to answer "is this thing busy, and is it about to
// run out of memory?".
//
// Its CPU panel merges the host-backend and kubernetes spellings into one, which
// the full dashboard deliberately does not. On the dashboard, two named panels
// side by side are self-explanatory — one is empty and says why. In a strip of
// two, an always-empty box next to the only other chart is half the strip wasted
// on the backend the reader is not running.
export const COMPACT_PANELS: Panel[] = [
  {
    id: "compact-cpu",
    title: "CPU",
    sources: [
      { metric: "container_cpu_time", counter: true },
      { metric: "container_cpu_usage", counter: false },
    ],
    unit: "cores",
    scope: "workloads",
    hint: "Cores in use. Host backends record cumulative CPU seconds and kubernetes records the instantaneous figure; this shows whichever exists.",
  },
  {
    id: "compact-memory",
    title: "Memory",
    sources: [{ metric: "container_memory_usage", counter: false }],
    unit: "bytes",
    scope: "workloads",
    hint: "Memory in use excluding reclaimable page cache.",
  },
];

// A time range and the resolution to sample it at.
//
// The steps are chosen so every range lands at 60–96 points: enough to see shape,
// few enough that one line stays one line rather than a smear, and — for the
// counter panels — wide enough that a 2s sampler's jitter does not dominate the
// difference between consecutive samples.
export interface Range {
  id: string;
  label: string;
  since: string;
  step: string;
  // refreshMs is how often the whole dashboard refetches at this range. A 24h
  // window does not change meaningfully every 15 seconds.
  refreshMs: number;
}

export const RANGES: Range[] = [
  { id: "15m", label: "Last 15 minutes", since: "15m", step: "15s", refreshMs: 15000 },
  { id: "1h", label: "Last hour", since: "1h", step: "1m", refreshMs: 30000 },
  { id: "6h", label: "Last 6 hours", since: "6h", step: "5m", refreshMs: 60000 },
  { id: "24h", label: "Last 24 hours", since: "24h", step: "15m", refreshMs: 300000 },
];

export const DEFAULT_RANGE = RANGES[1];

export const rangeById = (id: string): Range => RANGES.find((r) => r.id === id) ?? DEFAULT_RANGE;
