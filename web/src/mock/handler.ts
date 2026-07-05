// mockFetch is a `fetch` implementation that answers the /.cornus/web/* BFF
// surface from the canned fixtures, so component tests render the real views
// against realistic data with no backend. It is intentionally small: GETs return
// fixtures; mutating verbs (POST/PUT/DELETE) return {result:"ok"}.

import * as fx from "./fixtures";
import { handleFs } from "./fs";
import {
  hasMetric,
  mockMetrics,
  notResolved,
  obsStatusNow,
  setUnservedMetrics,
  setUnsupportedMetrics,
} from "./metrics";
import type { Config, TermSession } from "../api";

const BASE = "/.cornus/web";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// The terminal session list is the one piece of mock state a POST creates and a later
// GET has to agree with: a terminal pane only becomes "active" — the state the close
// prompt asks about — once the BFF both hands back a session id AND keeps listing it as
// alive. Returning {result:"ok"} to the create (the blanket mutation answer) leaves every
// pane session-less, so that state would be unreachable from a test. Reset per
// installMockFetch, i.e. per test.
let terminals: TermSession[] = [];
let termSeq = 0;

// createdTerminals records every create REQUEST in order, before any defaulting, so
// a test can assert which command a pane actually asked for. The session list alone
// cannot answer that: it holds the resolved command, which is equal for "the pane
// discovered /bin/bash" and "the pane was told /bin/bash".
// `dir` is recorded for the same reason the command is, and the reason is sharper here:
// the session list never echoes it back, and the mock cannot honour it — nothing in jsdom
// has a working directory — so the REQUEST is the only place "Open in a terminal aimed at
// the folder you were browsing" is observable at all.
export interface TermCreateRequest {
  workload: string;
  cmd?: string[];
  cmdline?: string;
  dir?: string;
}
let createdTerminals: TermCreateRequest[] = [];
export const terminalCreates = (): TermCreateRequest[] => createdTerminals;

function openTerminal(body: string): TermSession {
  const req = (() => {
    try {
      return JSON.parse(body) as { workload?: string; cmd?: string[]; cmdline?: string; dir?: string };
    } catch {
      return {};
    }
  })();
  createdTerminals.push({ workload: req.workload ?? "", cmd: req.cmd, cmdline: req.cmdline, dir: req.dir });
  // The BFF splits a `cmdline` with a shell-words parser and an explicit `cmd`
  // wins; the mock reproduces the shape a test depends on (multi-word lines become
  // multi-element argv), not the parser's quoting rules.
  const cmd = req.cmd?.length ? req.cmd : (req.cmdline ?? "").trim().split(/\s+/).filter(Boolean);
  const s: TermSession = {
    id: `term-${++termSeq}`,
    workload: req.workload ?? "",
    cmd,
    alive: true,
    rows: 24,
    cols: 80,
    created: "2026-01-01T00:00:00Z",
    state: "idle",
  };
  terminals.push(s);
  return s;
}

// shellsByWorkload is what discovery reports per workload. A workload with no entry
// answers with the default alpine-ish shape; an entry of [] is the distroless case
// the no-shell prompt exists for. Reset per installMockFetch.
let shellsByWorkload: Record<string, string[][]> = {};
export function seedShells(byWorkload: Record<string, string[][]>): void {
  shellsByWorkload = byWorkload;
}

function discoverShells(workload: string, body: string): unknown {
  const req = (() => {
    try {
      return JSON.parse(body) as { candidates?: string[] };
    } catch {
      return {};
    }
  })();
  // The real BFF splits each candidate string into argv; the mock's split is naive
  // on purpose — tests assert on which candidates were SENT, not on quoting.
  const candidates = (req.candidates ?? []).map((c) => c.trim().split(/\s+/).filter(Boolean));
  lastShellCandidates = req.candidates ?? [];
  const found = shellsByWorkload[workload] ?? [["/bin/ash"], ["/bin/sh"]];
  return { candidates, found };
}

// lastShellCandidates is the candidate list the browser last posted — the
// observation point for "the Settings edit reached the wire", which localStorage
// cannot answer (it only proves the setting was stored).
let lastShellCandidates: string[] = [];
export const shellCandidatesSent = (): string[] => lastShellCandidates;

// A killed session leaves the list, exactly as the BFF's own reaper drops it.
function killTerminal(id: string): void {
  terminals = terminals.filter((t) => t.id !== id);
}

// liveTerminals lets a test read what the mock BFF is still running — the observation
// point for "closing the pane ended the session".
export const liveTerminals = (): TermSession[] => terminals;

// setTerminalState changes what the BFF reports a session is doing, for a session the
// PANE created rather than one a test seeded. seedTerminals cannot express that: it
// replaces the list, and the id here was minted by the create the pane itself issued.
// That distinction is the whole point — a pane holding an id from the first frame never
// exercises the path where the id arrives after the tab has already rendered.
export function setTerminalState(id: string, state: TermSession["state"]): void {
  const t = terminals.find((s) => s.id === id);
  if (t) t.state = state;
}

// setTerminalTitle is the same door for the window title the BFF sniffs off the
// session's output (see osctitle.go). A test uses it to say "the program running
// in there just renamed itself", which is the event the tab label has to follow
// and which no amount of seeding can express: the title arrives mid-life, long
// after the pane rendered.
export function setTerminalTitle(id: string, title: string): void {
  const t = terminals.find((s) => s.id === id);
  if (t) t.title = title;
}

// setTerminalCwd is the same door for the working directory the BFF sniffs off OSC
// 7. A test uses it to say "the user just cd'd", which is the event a following
// file pane has to track and a split has to inherit.
export function setTerminalCwd(id: string, cwd: string): void {
  const t = terminals.find((s) => s.id === id);
  if (t) t.cwd = cwd;
}

// seedTerminals plants sessions a test's layout can already point at, including DEAD ones
// (alive:false): the BFF keeps a session listed for 30s after its shell exits, and a pane
// left holding such an id is the case the close prompt must NOT fire for.
export function seedTerminals(seed: Array<Partial<TermSession> & { id: string }>): void {
  terminals = seed.map((s) => ({
    workload: "",
    cmd: ["/bin/sh"],
    alive: true,
    rows: 24,
    cols: 80,
    created: "2026-01-01T00:00:00Z",
    ...s,
  }));
}

// The observability store is OPTIONAL on a real server, and "started without
// --obs" is a state the UI has to render as an explanation rather than an error.
// A test asks for that state with setObsStore(false); installMockFetch resets it,
// so the store is on unless a test says otherwise.
const OBS_OFF = "this server has no observability store: start it with --obs";
let storeOff = false;
export function setObsStore(enabled: boolean): void {
  storeOff = !enabled;
}

// The conduit announces itself only in SOCKS5 mode, so "no banners" is a real
// server state and not just an unpopulated fixture. A test asks for it with
// seedTunnelBanners([]); installMockFetch resets to the fixture's own banner.
// Held as an override rather than by mutating fx.tunnels, which is a shared
// module-level const that would carry the change into every later test.
let bannersOverride: string[] | undefined;
export function seedTunnelBanners(banners: string[]): void {
  bannersOverride = banners;
}

// A server omits its backend facts when it predates the field or could not build
// the backend to ask it, and a backend that runs no health probe is a real target
// (containerd, bare) — both are states the Server card has to render, and neither
// is reachable from a fixture that always reports a healthy dockerhost. The same
// goes for the ingress states: no advertised front door, a native client conduit,
// a client that routes no ingress, no agent to ask at all. A test asks for one with
// seedConfig(); installMockFetch resets to the fixture.
//
// A patch merged over fx.config rather than a mutation of it: fx.config is a shared
// module-level const, and mutating it would carry the change into every later test.
// Merged shallowly, so a patch naming `server` replaces that whole object — which is
// what a caller wants, since the states above are absences WITHIN it.
let configOverride: Partial<Config> | undefined;
export function seedConfig(patch: Partial<Config>): void {
  configOverride = { ...configOverride, ...patch };
}

// seedServerInfo is the server-half shorthand, kept because most callers only ever
// vary that one field.
export function seedServerInfo(server: Config["server"]): void {
  seedConfig({ server });
}

export function resolve(method: string, path: string, body = ""): Response {
  // Strip the origin if a full URL was passed.
  const url = new URL(path, "http://mock");
  const p = url.pathname;
  const rel = p.startsWith(BASE) ? p.slice(BASE.length) : p;

  if (rel.startsWith("/fs")) {
    const r = handleFs(method, rel, url, body);
    return new Response(r.body, {
      status: r.status,
      headers: { "Content-Type": r.contentType ?? "application/json" },
    });
  }

  if (method !== "GET") {
    // Actions, tunnel start/stop, file writes.
    if (rel.endsWith("/tunnel") && method === "POST") {
      return json({ active: true, url: "https://new.ngrok.app", port: 80 });
    }
    if (rel === "/terminals" && method === "POST") return json(openTerminal(body));
    if (method === "POST" && /^\/workloads\/[^/]+\/shells$/.test(rel)) {
      return json(discoverShells(decodeURIComponent(rel.split("/")[2]), body));
    }
    if (rel.startsWith("/terminals/") && method === "DELETE") {
      killTerminal(decodeURIComponent(rel.slice("/terminals/".length)));
      return json({ result: "ok" });
    }
    return json({ result: "ok" });
  }

  switch (true) {
    case rel === "/config":
      return json(configOverride ? { ...fx.config, ...configOverride } : fx.config);
    case rel === "/workloads":
      return json(fx.workloads);
    case rel === "/projects":
      return json(fx.projects);
    case rel === "/mounts":
      return json(fx.mounts);
    case rel === "/tunnels":
      return json(bannersOverride ? { ...fx.tunnels, banners: bannersOverride } : fx.tunnels);
    case rel === "/files":
      return json(fx.files);
    case rel === "/terminals":
      return json(terminals);
    case rel === "/observe/status":
      return storeOff ? json({ error: OBS_OFF }, 501) : json(obsStatusNow());
    case rel === "/observe/metrics": {
      if (storeOff) return new Response(OBS_OFF, { status: 501 });
      const metric = url.searchParams.get("query") ?? "";
      // The store rejects an unrecorded metric by name with a 400 diagnostic
      // rather than answering with an empty result — the UI has to tell the two
      // apart, so the mock must too.
      if (!hasMetric(metric)) return new Response(notResolved(metric), { status: 400 });
      return json(
        mockMetrics(
          metric,
          url.searchParams.get("since") ?? "1h",
          url.searchParams.get("step") ?? "1m",
        ),
      );
    }
    case rel === "/files/content": {
      const fp = url.searchParams.get("path") ?? "";
      const content = fx.fileContents[fp];
      return content === undefined
        ? new Response("not found", { status: 404 })
        : new Response(content, { status: 200, headers: { "Content-Type": "text/plain" } });
    }
    case /^\/projects\/[^/]+\/graph$/.test(rel):
      return json(fx.graph);
    case /^\/workloads\/[^/]+$/.test(rel): {
      const name = decodeURIComponent(rel.split("/")[2]);
      const detail = fx.workloadDetails[name];
      return detail
        ? json(detail)
        : json({ name, status: fx.workloads.find((w) => w.name === name) });
    }
    default:
      return new Response(`no mock for ${p}`, { status: 404 });
  }
}

// installMockFetch replaces globalThis.fetch with the mock and returns a restore
// function. Used by the component tests' beforeEach/afterEach.
export function installMockFetch(): () => void {
  const original = globalThis.fetch;
  terminals = [];
  termSeq = 0;
  createdTerminals = [];
  shellsByWorkload = {};
  lastShellCandidates = [];
  storeOff = false;
  bannersOverride = undefined;
  configOverride = undefined;
  setUnservedMetrics([]);
  setUnsupportedMetrics([]);
  globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const body = typeof init?.body === "string" ? init.body : "";
    return resolve(method, path, body);
  };
  return () => {
    globalThis.fetch = original;
  };
}
