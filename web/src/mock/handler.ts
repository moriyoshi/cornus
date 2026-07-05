// mockFetch is a `fetch` implementation that answers the /.cornus/web/* BFF
// surface from the canned fixtures, so component tests render the real views
// against realistic data with no backend. It is intentionally small: GETs return
// fixtures; mutating verbs (POST/PUT/DELETE) return {result:"ok"}.

import * as fx from "./fixtures";
import { handleFs } from "./fs";

const BASE = "/.cornus/web";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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
    return json({ result: "ok" });
  }

  switch (true) {
    case rel === "/config":
      return json(fx.config);
    case rel === "/workloads":
      return json(fx.workloads);
    case rel === "/projects":
      return json(fx.projects);
    case rel === "/mounts":
      return json(fx.mounts);
    case rel === "/tunnels":
      return json(fx.tunnels);
    case rel === "/files":
      return json(fx.files);
    case rel === "/terminals":
      return json([]);
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
