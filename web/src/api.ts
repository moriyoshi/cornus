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
  backend?: string;
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
  // best-effort program identity (basename of cmd[0]).
  state?: SessionState;
  agent?: string;
}

export interface Config {
  endpoint: string;
  configPath?: string;
  context?: string;
  contexts?: string[];
  server?: { registry_host?: string; registry_scheme?: string };
  serverError?: string;
  project?: string;
  composeFiles?: string[];
  agentSocket: string;
  agentLive: boolean;
  version: string;
}

async function req<T>(method: string, path: string, body?: BodyInit): Promise<T> {
  const resp = await fetch(BASE + path, { method, body });
  if (!resp.ok) {
    throw new Error(`${method} ${path}: ${resp.status} ${await resp.text()}`);
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

// Persistent terminal sessions backing the tiled workspace.
export const getTerminals = () => req<TermSession[]>("GET", "/terminals");
export const createTerminal = (workload: string, cmd: string[]) =>
  req<TermSession>("POST", "/terminals", JSON.stringify({ workload, cmd }));
export const killTerminal = (id: string) =>
  req<{ result: string }>("DELETE", `/terminals/${encodeURIComponent(id)}`);

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

// applyProject streams `compose up -d` output; onChunk receives it as it comes.
export async function applyProject(project: string, onChunk: (text: string) => void): Promise<void> {
  const resp = await fetch(`${BASE}/projects/${encodeURIComponent(project)}/apply`, { method: "POST" });
  if (!resp.ok) throw new Error(`apply: ${resp.status} ${await resp.text()}`);
  const reader = resp.body!.getReader();
  const dec = new TextDecoder();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    onChunk(dec.decode(value, { stream: true }));
  }
}

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
}

export interface FsListing {
  source: FsSource;
  root?: string;
  path: string;
  entries: FsEntry[];
  truncated?: boolean;
}

export interface FsRoot {
  id: string;
  label: string;
  path: string;
}

export interface FsWorkloadRef {
  name: string;
  running: boolean;
}

export interface FsRoots {
  roots: FsRoot[];
  workloads: FsWorkloadRef[];
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
// copyPath copies the file at loc to the virtual path `to` (which may live under a
// different mount — a different local root or workload).
export const copyPath = (loc: FsLocation, to: string) =>
  req<{ result: string }>("POST", `/fs/copy?${fsParams(loc)}`, JSON.stringify({ to }));
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
