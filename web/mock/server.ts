// Standalone mock BFF: serves canned /.cornus/web/* responses so the frontend
// can be developed with zero backend (no cornus server, no Docker). Run the Vite
// dev server against it:
//
//   node mock/server.ts                                   # mock BFF on :5080
//   CORNUS_WEB_PROXY=http://127.0.0.1:5080 npm run dev    # Vite proxies to it
//
// or the convenience script `npm run dev:mock` (runs both). The whole mock is
// TypeScript, run directly by Node's native type-stripping (v22.6+), sharing the
// SAME fixtures the component tests use (src/mock/fixtures.ts) as one source of
// truth. GET endpoints return fixtures; mutating verbs return {result:"ok"}; the
// /apply endpoint streams a short fake log.
//
// The exec, logs, and terminals WebSocket panes are also served here (ws.ts
// implements just enough RFC 6455 to avoid a dependency; faketerm.ts drives the
// fakes): the legacy exec terminal auto-plays a scripted shell demo, the tiled
// workspace's /terminals sessions persist across reattaches, and the logs pane
// streams plausible log lines forever. Only the stats WebSocket (no frontend pane
// yet) is left unhandled.

import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { readFileSync } from "node:fs";
import { acceptWebSocket } from "./ws.ts";
import { startExecSession, startLogStream, mockTerms } from "./faketerm.ts";
import { handleFs, contentAsset, imageMime } from "../src/mock/fs.ts";

let fx: typeof import("../src/mock/fixtures.ts");
try {
  fx = await import("../src/mock/fixtures.ts");
} catch (e) {
  console.error(
    "could not load fixtures.ts (needs Node >=22.6 with native type-stripping):",
    (e as Error).message,
  );
  process.exit(1);
}

const PORT = Number(process.env.CORNUS_WEB_MOCK_PORT || 5080);
const BASE = "/.cornus/web";

function json(res: ServerResponse, body: unknown, status = 200): void {
  res.writeHead(status, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" });
  res.end(JSON.stringify(body));
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => resolve(data));
  });
}

const server = createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://mock");
  const p = url.pathname;
  if (!p.startsWith(BASE)) {
    res.writeHead(404).end("mock BFF: not a /.cornus/web path");
    return;
  }
  const rel = p.slice(BASE.length);

  // Persistent terminal sessions (tiled workspace).
  if (rel === "/terminals" && req.method === "GET") {
    return json(res, mockTerms.list());
  }
  if (rel === "/terminals" && req.method === "POST") {
    readBody(req).then((raw) => {
      let workload = "app";
      let cmd: string[] = ["/bin/sh"];
      try {
        const parsed = JSON.parse(raw || "{}");
        if (parsed.workload) workload = String(parsed.workload);
        if (Array.isArray(parsed.cmd) && parsed.cmd.length) cmd = parsed.cmd.map(String);
      } catch {
        // fall through with defaults
      }
      json(res, mockTerms.create(workload, cmd));
    });
    return;
  }
  const termM = rel.match(/^\/terminals\/([^/]+)$/);
  if (termM && req.method === "DELETE") {
    const ok = mockTerms.kill(decodeURIComponent(termM[1]));
    return json(res, ok ? { result: "killed" } : { error: "no such session" }, ok ? 200 : 404);
  }

  // File explorer surface (both sources): delegate to the shared in-memory fs.
  if (rel.startsWith("/fs")) {
    // Image fixtures that reference a real repo asset are served from disk so the image
    // viewer previews the actual bytes (a binary PNG can't live in the in-memory tree).
    if (req.method === "GET" && rel === "/fs/content") {
      const asset = contentAsset(url.searchParams);
      if (asset) {
        try {
          const buf = readFileSync(new URL(`../../assets/${asset}`, import.meta.url));
          res.writeHead(200, {
            "Content-Type": imageMime(asset) ?? "application/octet-stream",
            "Access-Control-Allow-Origin": "*",
          });
          res.end(buf);
          return;
        } catch {
          // Asset missing: fall through to the in-memory content.
        }
      }
    }
    readBody(req).then((body) => {
      const r = handleFs(req.method ?? "GET", rel, url, body);
      res.writeHead(r.status, {
        "Content-Type": r.contentType ?? "application/json",
        "Access-Control-Allow-Origin": "*",
      });
      res.end(r.body);
    });
    return;
  }

  if (req.method !== "GET") {
    if (rel.endsWith("/tunnel") && req.method === "POST") {
      return json(res, { active: true, url: "https://new.ngrok.app", port: 80 });
    }
    if (rel.endsWith("/apply")) {
      res.writeHead(200, { "Content-Type": "text/plain" });
      return res.end("mock apply: recreated 1 service\n");
    }
    return json(res, { result: "ok" });
  }
  switch (true) {
    case rel === "/config":
      return json(res, fx.config);
    case rel === "/workloads":
      return json(res, fx.workloads);
    case rel === "/projects":
      return json(res, fx.projects);
    case rel === "/mounts":
      return json(res, fx.mounts);
    case rel === "/tunnels":
      return json(res, fx.tunnels);
    case rel === "/files":
      return json(res, fx.files);
    case rel === "/files/content": {
      const content = fx.fileContents[url.searchParams.get("path") ?? ""];
      if (content === undefined) return res.writeHead(404).end("not found");
      res.writeHead(200, { "Content-Type": "text/plain" });
      return res.end(content);
    }
    case /^\/projects\/[^/]+\/graph$/.test(rel):
      return json(res, fx.graph);
    case /^\/workloads\/[^/]+$/.test(rel): {
      const name = decodeURIComponent(rel.split("/")[2]);
      const detail =
        fx.workloadDetails[name] ?? { name, status: fx.workloads.find((w) => w.name === name) };
      return json(res, detail);
    }
    default:
      return res.writeHead(404).end(`no mock for ${p}`);
  }
});

// WebSocket upgrades: legacy exec, logs, and persistent terminal attach.
server.on("upgrade", (req, socket, head) => {
  const url = new URL(req.url ?? "/", "http://mock");
  const p = url.pathname;
  const execM = p.match(new RegExp(`^${BASE}/workloads/([^/]+)/exec$`));
  const logsM = p.match(new RegExp(`^${BASE}/workloads/([^/]+)/logs$`));
  const attachM = p.match(new RegExp(`^${BASE}/terminals/([^/]+)/attach$`));
  if (!execM && !logsM && !attachM) {
    socket.destroy();
    return;
  }
  if (attachM) {
    // The real server 404s an unknown session BEFORE the WebSocket upgrade, so the
    // browser socket never opens — the client reads opened=false as "ended" rather
    // than a transient drop it should reattach through. Reject pre-upgrade to match.
    const sess = mockTerms.get(decodeURIComponent(attachM[1]));
    if (!sess) {
      socket.destroy();
      return;
    }
    const ws = acceptWebSocket(req, socket, head);
    if (!ws) {
      socket.destroy();
      return;
    }
    sess.attach(ws);
    return;
  }
  const ws = acceptWebSocket(req, socket, head);
  if (!ws) {
    socket.destroy();
    return;
  }
  if (execM) {
    startExecSession(ws, {
      workload: decodeURIComponent(execM[1]),
      cmd: url.searchParams.getAll("cmd"),
    });
  } else if (logsM) {
    startLogStream(ws, { workload: decodeURIComponent(logsM[1]) });
  }
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`mock BFF listening on http://127.0.0.1:${PORT}${BASE}/*`);
  console.log(`  exec/logs/terminals WebSocket panes are mocked (terminals persist across reattach)`);
  console.log(`point Vite at it:  CORNUS_WEB_PROXY=http://127.0.0.1:${PORT} npm run dev`);
});
