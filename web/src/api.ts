// Typed client for the cornus web BFF (/.cornus/web/*). The response shapes
// mirror the web* structs in cmd/cornus/web.go.

const BASE = "/.cornus/web";

export interface InstanceStatus {
  id?: string;
  state?: string;
  running?: boolean;
  health?: string;
  exitCode?: number | null;
}

export interface GitOrigin {
  remote?: string;
  branch?: string;
  commit?: string;
  dirty?: boolean;
}

// Origin is a workload's lineage: which project it belongs to and the client
// host/user/directory/git repo it was spawned from, plus the server-verified
// authenticated subject.
export interface Origin {
  project?: string;
  host?: string;
  user?: string;
  directory?: string;
  git?: GitOrigin;
  subject?: string;
}

export interface Workload {
  name: string;
  service?: string;
  project?: string;
  image?: string;
  summary: string;
  created: boolean;
  running: boolean;
  instances?: InstanceStatus[];
  origin?: Origin;
}

export interface WorkloadDetail {
  name: string;
  service?: string;
  project?: string;
  status?: {
    name: string;
    image?: string;
    backend?: string;
    instances?: InstanceStatus[];
    origin?: Origin;
  };
  spec?: Record<string, unknown>;
  tunnel?: TunnelStatus;
}

export interface TunnelStatus {
  active: boolean;
  url?: string;
  port?: number;
}

export interface Tunnel extends TunnelStatus {
  // The compose service this tunnel's deployment realizes, joined server-side by
  // the BFF. Absent for a deployment outside the loaded project, where no plan
  // names one — so a table renders that as "—", never as a guess.
  service?: string;
  workload: string;
}

export interface TunnelsResponse {
  tunnels: Tunnel[];
  forwards?: Record<string, string[]>;
  banners?: string[];
}

export interface Project {
  name: string;
  services?: string[];
  running?: string[];
  loaded: boolean;
}

export interface GraphNode {
  service: string;
  resource: string;
  image?: string;
  summary: string;
  running: boolean;
  created: boolean;
}

export interface GraphEdge {
  from: string;
  to: string;
  condition?: string;
  required: boolean;
}

export interface Graph {
  project: string;
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface Mount {
  project?: string;
  service?: string;
  workload: string;
  kind: "bind" | "volume";
  source?: string;
  target: string;
  readOnly?: boolean;
  status: "live" | "running" | "inactive";
}

export interface WebFile {
  path: string;
  label: string;
  kind: "compose" | "env" | "clientconfig";
}

// SessionState is the detected activity of a session's foreground program:
// working, idle, or blocked waiting for a human (a permission/approval prompt).
export type SessionState = "idle" | "working" | "blocked";

// TermSession mirrors termInfo in cmd/cornus/internal/webbff/term.go: a persistent
// terminal session living in the BFF, attachable by id from any pane.
export interface TermSession {
  id: string;
  workload: string;
  cmd: string[];
  alive: boolean;
  rows: number;
  cols: number;
  created: string;
  // state is the detected activity; empty/absent for a dead session. agent is the
  // program currently in the FOREGROUND of the session, which is what scopes the
  // BFF's detection rules. Absent when that program is a shell (a session sitting
  // at a prompt is idle by definition) or could not be identified.
  state?: SessionState;
  agent?: string;
  // title is the window title the session's output last set, via the OSC sequence
  // every terminal reads. It tracks what is ACTUALLY running — "vim README.md"
  // where cmd says "/bin/bash" — so it is the better label wherever a session has
  // to be named. Absent whenever nothing in the session ever set one (a bare
  // busybox sh never does), so every use must fall back to cmd.
  title?: string;
  // cwd is the working directory the session last reported via OSC 7, absolute
  // inside the container. It is the LIVE answer that a pane's creation-time `dir`
  // can only approximate, so it is what a split inherits and what a following file
  // pane tracks. Absent unless the session's shell runs a hook that emits it,
  // which a stock container image does not — so absent means "unknown", never
  // "the root", and every consumer must fall back rather than navigate to "/".
  cwd?: string;
}

export interface Config {
  endpoint: string;
  configPath?: string;
  context?: string;
  contexts?: string[];
  // A slice of api.ServerInfo. `backend` is the server's ONE deploy backend
  // ("dockerhost", "kubernetes", …): a server builds it once for its lifetime, so
  // this is where that fact lives — it is not a per-workload property and the
  // workload tables carry no column for it.
  //
  // Both `backend` and `ingress` are OPTIONAL in a way that means "not reported",
  // never "none": the server omits `backend` when it predates the field or could
  // not construct the backend to ask it, and omits `ingress` when it has no front
  // door to advertise or a cluster introspection failed. The UI must not turn
  // either absence into a claim.
  server?: {
    registry_host?: string;
    registry_scheme?: string;
    backend?: { name?: string; reportsHealth?: boolean };
    // Backend-determined: kubernetes reports the cluster's own ingress (domain,
    // class, and the controller Service a native passthrough targets); every other
    // backend has no controller to find, so the server itself is the front door
    // and says so with emulated.
    ingress?: {
      domain?: string;
      class?: string;
      emulated?: boolean;
      listen?: string;
      controller?: { namespace?: string; service?: string; http_port?: number; https_port?: number };
    };
  };
  serverError?: string;
  project?: string;
  composeFiles?: string[];
  agentSocket: string;
  agentLive: boolean;
  version: string;
}

// ApiError carries the HTTP status alongside the message, because some statuses
// are a UI state rather than a failure: the observability routes answer 501 when
// the server was started without a store, and 400 when a metric has never been
// recorded. A caller that only has the message string has to grep it.
export class ApiError extends Error {
  readonly status: number;
  readonly body: string;
  constructor(method: string, path: string, status: number, body: string) {
    super(`${method} ${path}: ${status} ${body}`);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

async function req<T>(method: string, path: string, body?: BodyInit): Promise<T> {
  const resp = await fetch(BASE + path, { method, body });
  if (!resp.ok) {
    throw new ApiError(method, path, resp.status, await resp.text());
  }
  return resp.json() as Promise<T>;
}

export const getConfig = () => req<Config>("GET", "/config");
export const getWorkloads = () => req<Workload[]>("GET", "/workloads");
export const getWorkload = (name: string) =>
  req<WorkloadDetail>("GET", `/workloads/${encodeURIComponent(name)}`);
export const getProjects = () => req<Project[]>("GET", "/projects");
export const getGraph = (project: string) =>
  req<Graph>("GET", `/projects/${encodeURIComponent(project)}/graph`);
export const getMounts = () => req<Mount[]>("GET", "/mounts");
export const getTunnels = () => req<TunnelsResponse>("GET", "/tunnels");
export const getFiles = () => req<WebFile[]>("GET", "/files");

// ---- observability store (/.cornus/web/observe/*) ----
//
// These mirror obsstore.Series / obsstore.Status. The store is optional: a server
// started without `--obs` answers 501 on every route here, which the Metrics view
// renders as an explanation rather than an error.

export interface MetricPoint {
  // t is RFC3339 (the Go time.Time zero value marshals as "0001-01-01T00:00:00Z",
  // so parse defensively rather than trusting every sample).
  t: string;
  v: number;
}

export interface MetricSeries {
  labels?: Record<string, string>;
  points?: MetricPoint[];
}

export interface ObsTableStatus {
  name: string;
  rows: number;
  segments: number;
  oldest?: string;
  newest?: string;
}

// ObsMetricsStatus is the zero-touch workload sampler's state. Its counters are
// the answer to "why is this panel empty?" far more often than the store is.
export interface ObsMetricsStatus {
  // interval and retention are Go durations, i.e. NANOSECONDS as a JSON number.
  interval: number;
  replicas: number;
  sampled: number;
  failed: number;
  dropped: number;
}

export interface ObsStatus {
  dir: string;
  tables?: ObsTableStatus[];
  bufferBytes: number;
  walBytes: number;
  dropped: number;
  errors: number;
  retention: number;
  maxBytes: number;
  metrics?: ObsMetricsStatus;
  export?: { endpoint: string; sent: number; dropped: number; failed: number };
}

export const getObsStatus = () => req<ObsStatus>("GET", "/observe/status");

// getMetrics evaluates a PromQL range query. `since`/`until` take the same time
// expressions as `cornus observe` (a Go duration, RFC3339, or Unix seconds) and
// `step` is a Go duration.
export function getMetrics(query: string, since: string, step: string, until = ""): Promise<MetricSeries[]> {
  const q = new URLSearchParams({ query, since, step });
  if (until) q.set("until", until);
  return req<MetricSeries[]>("GET", `/observe/metrics?${q.toString()}`);
}

// Persistent terminal sessions backing the tiled workspace.
export const getTerminals = () => req<TermSession[]>("GET", "/terminals");
// `dir` is where the shell starts, absolute inside the container, and is omitted rather
// than sent empty so the BFF's own "unset" and "the root" stay distinguishable. Not every
// backend can honour it — kubernetes' PodExecOptions has no working-directory field and
// the deploy layer warns and ignores — so a caller must treat it as a preference.
export const createTerminal = (workload: string, cmd: string[], dir?: string) =>
  req<TermSession>("POST", "/terminals", JSON.stringify({ workload, cmd, dir: dir || undefined }));
// createTerminalFromLine sends a command the user TYPED, as one string, so the BFF
// splits it with the same shell-words parser every other candidate goes through.
// Wrapping it in a one-element cmd would ask to run a file whose name has a space
// in it, so `/bin/busybox sh` — a perfectly ordinary answer to the no-shell prompt
// — could not start.
export const createTerminalFromLine = (workload: string, cmdline: string, dir?: string) =>
  req<TermSession>("POST", "/terminals", JSON.stringify({ workload, cmdline, dir: dir || undefined }));
export const killTerminal = (id: string) =>
  req<{ result: string }>("DELETE", `/terminals/${encodeURIComponent(id)}`);

// discoverShells asks the BFF which interactive shells the workload actually has.
// candidates is this browser's own list (see views/terminal/shells.ts); the BFF
// ranks the workload's own declared shells, its entrypoint's shell, and the
// connection profile's list ahead of it, and returns the merged probe order it
// used alongside the subset that was found. Both are argv, already split.
//
// An empty `found` is a successful answer — the image has no shell — not a failure.
export const discoverShells = (workload: string, candidates: string[]) =>
  req<{ candidates: string[][]; found: string[][] }>(
    "POST",
    `/workloads/${encodeURIComponent(workload)}/shells`,
    JSON.stringify({ candidates }),
  );

export const workloadAction = (name: string, action: "start" | "stop" | "restart") =>
  req<{ result: string }>("POST", `/workloads/${encodeURIComponent(name)}/${action}`);
export const deleteWorkload = (name: string) =>
  req<{ result: string }>("DELETE", `/workloads/${encodeURIComponent(name)}`);
export const startTunnel = (name: string, body: { authToken?: string; port?: number; proto?: string }) =>
  req<TunnelStatus>("POST", `/workloads/${encodeURIComponent(name)}/tunnel`, JSON.stringify(body));
export const stopTunnel = (name: string) =>
  req<{ result: string }>("DELETE", `/workloads/${encodeURIComponent(name)}/tunnel`);

export async function readFileContent(path: string): Promise<string> {
  const resp = await fetch(`${BASE}/files/content?path=${encodeURIComponent(path)}`);
  if (!resp.ok) throw new Error(`read ${path}: ${resp.status} ${await resp.text()}`);
  return resp.text();
}

export async function writeFileContent(path: string, content: string): Promise<void> {
  const resp = await fetch(`${BASE}/files/content?path=${encodeURIComponent(path)}`, {
    method: "PUT",
    body: content,
  });
  if (!resp.ok) throw new Error(`write ${path}: ${resp.status} ${await resp.text()}`);
}

// No applyProject: the BFF has no apply endpoint to call. Re-deploying a project
// is `cornus compose up -d` in the terminal this UI accompanies — see the comment
// on ProjectSection in views/Projects.tsx.

// ---- file explorer (/.cornus/web/fs*) ----

// FsSource is the addressing space. The SPA browses the unified "virtual" namespace
// (mounts = local roots + workloads under one path tree); "local"/"container" remain
// the concrete sources the BFF resolves virtual paths onto.
export type FsSource = "local" | "container" | "virtual";

export interface FsEntry {
  name: string;
  kind: "dir" | "file" | "symlink";
  size: number;
  mtime?: string;
  mode?: string;
  linkTarget?: string;
  // running is set only for the workload mounts of the virtual root listing, so the
  // UI can disable stopped workloads. Undefined for files and local-root mounts.
  running?: boolean;
  // readOnly marks a virtual-root mount whose bind is declared `:ro` — the kernel holds
  // the container to it, so the UI says so rather than letting a write reach a 403.
  readOnly?: boolean;
}

export interface FsListing {
  source: FsSource;
  root?: string;
  path: string;
  entries: FsEntry[];
  truncated?: boolean;
  // readOnly marks a listing whose directory cannot be written — it sits under a bind
  // declared `:ro`. It rides the LISTING because an entry only carries the flag at the
  // virtual root, so a pane deep inside a read-only mount would otherwise not know.
  readOnly?: boolean;
  // refused travels with the VIRTUAL ROOT listing only: bind sources the BFF declined to
  // expose. Shown so a missing mount is explained rather than merely absent.
  refused?: FsRefusedRoot[];
}

export interface FsRoot {
  id: string;
  label: string;
  path: string;
  // readOnly mirrors the `:ro` of the bind mount this root came from. Mutations under
  // it are refused with 403 by the BFF.
  readOnly?: boolean;
}

// FsRefusedRoot is a bind-mount source the BFF declined to expose at all — a kernel
// pseudo-filesystem (/proc, sysfs, cgroup, …) or the filesystem root. It travels with
// the roots so the UI can explain an absence rather than leaving the user to wonder
// why a directory in their compose file is not listed.
export interface FsRefusedRoot {
  path: string;
  reason: string;
}

export interface FsWorkloadRef {
  name: string;
  running: boolean;
}

export interface FsRoots {
  roots: FsRoot[];
  workloads: FsWorkloadRef[];
  refused?: FsRefusedRoot[];
}

// FsLocation identifies a place in the two-source filesystem. root applies to the
// local source, workload to the container source.
export interface FsLocation {
  source: FsSource;
  root?: string;
  workload?: string;
  path: string;
}

function fsParams(loc: FsLocation, extra?: Record<string, string>): string {
  const q = new URLSearchParams({ source: loc.source, path: loc.path });
  if (loc.root) q.set("root", loc.root);
  if (loc.workload) q.set("workload", loc.workload);
  for (const [k, v] of Object.entries(extra ?? {})) q.set(k, v);
  return q.toString();
}

export const getFsRoots = () => req<FsRoots>("GET", "/fs/roots");
export const listDir = (loc: FsLocation) => req<FsListing>("GET", `/fs?${fsParams(loc)}`);
export const statPath = (loc: FsLocation) => req<FsEntry>("GET", `/fs/stat?${fsParams(loc)}`);
export const mkdir = (loc: FsLocation) =>
  req<{ result: string }>("POST", `/fs/mkdir?${fsParams(loc)}`);
export const renamePath = (loc: FsLocation, to: string) =>
  req<{ result: string }>("POST", `/fs/rename?${fsParams(loc)}`, JSON.stringify({ to }));
// copyPath copies the file or DIRECTORY at loc to the virtual path `to` (which may live
// under a different mount — a different local root or workload). A directory is walked
// server-side; `skipped` names the symlinks that could not ride through the read/write
// path (a link to a directory, a dangling link), which the BFF steps over rather than
// failing a whole tree for.
export const copyPath = (loc: FsLocation, to: string) =>
  req<{ result: string; skipped?: string[] }>("POST", `/fs/copy?${fsParams(loc)}`, JSON.stringify({ to }));
// movePath moves the file or DIRECTORY at loc to the virtual path `to`, in ONE request.
// The BFF renames when both sides share a filesystem (instant, atomic, any size) and
// otherwise copies then deletes the source — but only after a copy that reported nothing
// skipped, so a partial transfer keeps the source rather than losing what it stepped
// over. A non-empty `skipped` therefore means the move did NOT complete.
export const movePath = (loc: FsLocation, to: string) =>
  req<{ result: string; skipped?: string[] }>("POST", `/fs/move?${fsParams(loc)}`, JSON.stringify({ to }));
// ---- batch transfers and preflight ----

// FsTransferItem is one source, and optionally an explicit destination. Leaving `to`
// unset lands the item under the request's destination FOLDER as its own basename, which
// is what a drag gesture means.
export interface FsTransferItem {
  from: string;
  to?: string;
}

export interface FsItemResult {
  from: string;
  to: string;
  status: "ok" | "failed";
  error?: string;
  // skipped names symlinks the transfer stepped over. On a MOVE a non-empty skipped
  // means the source was kept, so status is "failed" — the item did not complete.
  skipped?: string[];
}

export interface FsBatchResult {
  result: "ok" | "partial" | "failed";
  items: FsItemResult[];
}

// FsPreflightItem is what WOULD happen to one item. `route` says where the work runs:
// "here" is the developer's own filesystem (no daemon round trip at all), "relay" streams
// through the BFF and is the one still subject to a per-file size cap.
export interface FsPreflightItem {
  from: string;
  to: string;
  kind?: "dir" | "file" | "symlink";
  action: "create" | "overwrite" | "merge" | "refused";
  route: "here" | "server" | "relay";
  native?: boolean;
  why?: string;
  files?: number;
  bytes?: number;
  truncated?: boolean;
  warnings?: string[];
  error?: string;
}

export interface FsPreflightResult {
  op: "copy" | "move";
  items: FsPreflightItem[];
  refusals: number;
  warnings: number;
  files: number;
  bytes: number;
}

// copyBatch / moveBatch transfer many items in ONE request, reporting per item. An item
// that fails does not abort the ones after it — a partial transfer whose shape the user
// cannot see is worse than either outcome alone.
export const copyBatch = (loc: FsLocation, to: string, items: FsTransferItem[]) =>
  req<FsBatchResult>("POST", `/fs/copy?${fsParams(loc)}`, JSON.stringify({ to, items }));
export const moveBatch = (loc: FsLocation, to: string, items: FsTransferItem[]) =>
  req<FsBatchResult>("POST", `/fs/move?${fsParams(loc)}`, JSON.stringify({ to, items }));

// preflight reports what a copy or move would do — permissions, overwrites, size, and
// the warnings that are otherwise only discoverable by hitting them — and changes
// nothing. It takes exactly the body the real endpoints take, so what is reported is
// what would run.
export const preflight = (loc: FsLocation, op: "copy" | "move", to: string, items: FsTransferItem[]) =>
  req<FsPreflightResult>("POST", `/fs/preflight?${fsParams(loc, { op })}`, JSON.stringify({ to, items }));

export const deletePath = (loc: FsLocation, recursive: boolean) =>
  req<{ result: string }>("DELETE", `/fs?${fsParams(loc, recursive ? { recursive: "1" } : {})}`);

// fsContentURL is the read/download URL for a file; download=1 flips to attachment.
export function fsContentURL(loc: FsLocation, download = false): string {
  return `${BASE}/fs/content?${fsParams(loc, download ? { download: "1" } : {})}`;
}

export async function readFsContent(loc: FsLocation): Promise<string> {
  const resp = await fetch(fsContentURL(loc));
  if (!resp.ok) throw new Error(`read ${loc.path}: ${resp.status} ${await resp.text()}`);
  return resp.text();
}

export async function writeFsContent(loc: FsLocation, content: string): Promise<void> {
  const resp = await fetch(fsContentURL(loc), { method: "PUT", body: content });
  if (!resp.ok) throw new Error(`write ${loc.path}: ${resp.status} ${await resp.text()}`);
}

// uploadFile writes a picked File into the directory at loc.path.
export async function uploadFile(loc: FsLocation, file: File): Promise<void> {
  const url = `${BASE}/fs/upload?${fsParams(loc, { name: file.name })}`;
  const resp = await fetch(url, { method: "POST", body: file });
  if (!resp.ok) throw new Error(`upload ${file.name}: ${resp.status} ${await resp.text()}`);
}

// wsURL builds the WebSocket URL for a BFF streaming endpoint.
export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${BASE}${path}`;
}
