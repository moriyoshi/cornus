// A tiny in-memory filesystem backing the /.cornus/web/fs* explorer surface for
// both the component-test fetch stub (src/mock/handler.ts) and the standalone dev
// server (mock/server.ts). It is stateful so the dev server feels live: mkdir,
// upload, rename, and delete mutate these trees. Shapes mirror the Go fsEntry /
// fsListing / fsRoots structs.

import type { FsEntry, FsListing, FsRoots } from "../api";
import { workloads } from "./fixtures.ts";

interface Node {
  name: string;
  kind: "dir" | "file" | "symlink";
  content?: string;
  linkTarget?: string;
  children?: Node[];
  // asset names a real repo asset under /assets that the standalone dev server serves
  // as this file's content (so a binary PNG shows for real). The in-browser test stub,
  // which can't read files, falls back to `content`.
  asset?: string;
}

function dir(name: string, children: Node[]): Node {
  return { name, kind: "dir", children };
}
function file(name: string, content = ""): Node {
  return { name, kind: "file", content };
}
// imageAsset is a file whose bytes are a real repo asset (assets/<asset>), used so the
// image viewer previews the actual Cornus logo in the dev server.
function imageAsset(name: string, asset: string): Node {
  return { name, kind: "file", content: "", asset };
}

// imageMime maps a filename extension to its image MIME type (mirrors the Go BFF), or
// undefined when it is not a recognized image.
export function imageMime(name: string): string | undefined {
  const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
  const map: Record<string, string> = {
    png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif",
    webp: "image/webp", avif: "image/avif", bmp: "image/bmp", ico: "image/x-icon",
    svg: "image/svg+xml",
  };
  return map[ext];
}
// deepChain nests names into a/b/c/… ending in leaf — used to seed a very long path
// so the title-bar breadcrumb's overflow handling can be checked visually.
function deepChain(names: string[], leaf: Node): Node {
  return names.reduceRight((child, name) => dir(name, [child]), leaf);
}

// manyEntries builds a directory with a lot of children (a mix of subdirectories and
// files across a spread of extensions and sizes) for eyeballing a long, scrolling
// listing — the sticky header and the flush pane frame. Deterministic so the fixture
// is stable across reloads.
function manyEntries(): Node {
  const exts = ["ts", "tsx", "go", "json", "yaml", "md", "txt", "css", "sh", "log", "png", "sql"];
  const kids: Node[] = [];
  for (let i = 0; i < 12; i++) {
    kids.push(dir(`subdir-${String(i).padStart(2, "0")}`, [file("keep.txt", "placeholder\n")]));
  }
  for (let i = 0; i < 96; i++) {
    const ext = exts[i % exts.length];
    kids.push(file(`file-${String(i).padStart(3, "0")}.${ext}`, "x".repeat((i * 263) % 5000)));
  }
  return dir("many-files", kids);
}

// Local roots (by id) and container filesystems (by workload). Rebuilt per module
// load; the dev server keeps them for its lifetime.
const local: Record<string, Node> = {
  project: dir("", [
    dir("web", [file("nginx.conf", "server { listen 80; }\n")]),
    manyEntries(),
    // A deliberately deep tree for eyeballing the title-bar breadcrumb overflow.
    dir("reports", [
      deepChain(
        [
          "2026",
          "q3-fiscal-year",
          "engineering-division",
          "platform-infrastructure",
          "kubernetes-clusters",
          "production-us-east-1",
          "namespaces",
          "payment-gateway-service",
          "generated-manifests",
        ],
        file("very-long-breadcrumb-values.yaml", "replicas: 3\n"),
      ),
    ]),
    file("compose.yaml", "services:\n  web:\n    image: shop/web:latest\n"),
    file("README.md", "# Shop\n\nA demo project.\n"),
    file(".env", "PORT=80\n"),
  ]),
  // The second local root mirrors the repo's assets/ dir (served for real by the dev
  // server), so the image viewer previews the actual Cornus logo.
  assets: dir("", [
    imageAsset("cornus-logo.svg", "cornus-logo.svg"),
    imageAsset("cornus-logo.png", "cornus-logo.png"),
  ]),
};
// defaultContainerTree is a small generic root filesystem so any running workload is
// browsable, not just the one with a hand-written tree.
function defaultContainerTree(name: string): Node {
  return dir("", [
    dir("etc", [file("hostname", `${name}\n`)]),
    dir("var", [dir("log", [file("app.log", `[${name}] started\n`)])]),
    file("hello.txt", `container file in ${name}\n`),
  ]);
}

const containers: Record<string, Node> = {
  "shop-web": dir("", [
    dir("etc", [dir("nginx", [file("nginx.conf", "worker_processes auto;\n")])]),
    dir("usr", [dir("share", [dir("nginx", [dir("html", [file("index.html", "<h1>hi</h1>\n")])])])]),
    file("hello.txt", "container file\n"),
  ]),
};
// Every running workload gets a filesystem, so the virtual root's workload mounts are
// all enterable — the container source of truth is the shared workloads fixture.
for (const w of workloads) {
  if (w.running && !(w.name in containers)) containers[w.name] = defaultContainerTree(w.name);
}

export const fsRoots: FsRoots = {
  roots: [
    { id: "project", label: "shop", path: "/srv/shop" },
    { id: "assets", label: "assets", path: "/srv/assets" },
  ],
  // Workloads mirror the shared fixture (name + running), so the file-explorer root
  // shows the same set the rest of the mock app does.
  workloads: workloads.map((w) => ({ name: w.name, running: w.running })),
};

function treeFor(params: URLSearchParams): Node | undefined {
  if (params.get("source") === "container") return containers[params.get("workload") ?? ""];
  return local[params.get("root") || "project"];
}

function split(path: string): string[] {
  return path.split("/").map((s) => s.trim()).filter(Boolean);
}

// resolveVirtual maps a virtual path (/<mount>/<subpath>) onto its mount tree and the
// sub-path within it. An empty path is the virtual root (the mount list). The first
// segment selects a local root (by id) or, failing that, a workload.
function resolveVirtual(path: string): { root?: Node; parts: string[]; atRoot: boolean } {
  const segs = split(path);
  if (segs.length === 0) return { atRoot: true, parts: [] };
  const [mount, ...sub] = segs;
  return { root: mount in local ? local[mount] : containers[mount], parts: sub, atRoot: false };
}

// virtualRootListing lists the mounts: local roots (by id) then workloads (with
// running state), mirroring the Go virtualRootListing.
function virtualRootListing(): FsListing {
  const entries: FsEntry[] = [
    ...fsRoots.roots.map((r) => ({ name: r.id, kind: "dir" as const, size: 0, mode: "0755" })),
    ...fsRoots.workloads.map((w) => ({
      name: w.name,
      kind: "dir" as const,
      size: 0,
      mode: "0755",
      running: w.running,
    })),
  ];
  return { source: "virtual", path: "", entries };
}

function walk(root: Node, parts: string[]): Node | undefined {
  let cur: Node | undefined = root;
  for (const part of parts) {
    if (!cur || cur.kind !== "dir") return undefined;
    cur = cur.children?.find((c) => c.name === part);
  }
  return cur;
}

function toEntry(n: Node): FsEntry {
  return {
    name: n.name,
    kind: n.kind,
    size: n.content ? n.content.length : 0,
    mtime: "2024-01-01T00:00:00Z",
    mode: n.kind === "dir" ? "0755" : "0644",
    linkTarget: n.linkTarget,
  };
}

function sortEntries(entries: FsEntry[]): FsEntry[] {
  return entries.sort((a, b) => {
    if ((a.kind === "dir") !== (b.kind === "dir")) return a.kind === "dir" ? -1 : 1;
    return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
  });
}

export interface FsResult {
  status: number;
  contentType?: string;
  body: string;
}

const ok = (): FsResult => ({ status: 200, body: JSON.stringify({ result: "ok" }) });
const err = (status: number, msg: string): FsResult => ({ status, body: msg });

// handleFs answers one /.cornus/web/fs* request against the in-memory trees. rel is
// the path after /.cornus/web (e.g. "/fs" or "/fs/content"); url carries the query.
export function handleFs(method: string, rel: string, url: URL, body: string): FsResult {
  const params = url.searchParams;
  const path = params.get("path") ?? "";

  if (rel === "/fs/roots") return { status: 200, body: JSON.stringify(fsRoots) };

  // Resolve the addressed mount. The SPA browses the virtual namespace; the concrete
  // sources stay supported for back-compat.
  const virtual = params.get("source") === "virtual";
  const resolved = virtual
    ? resolveVirtual(path)
    : { root: treeFor(params), parts: split(path), atRoot: false };
  const { root, parts, atRoot } = resolved;

  // The virtual root lists the mounts; no per-file operation applies to it.
  if (atRoot) {
    if (rel === "/fs" && method === "GET") {
      return { status: 200, body: JSON.stringify(virtualRootListing()) };
    }
    return err(400, "cannot operate on the virtual root");
  }
  if (!root) return err(404, "no such source");

  if (rel === "/fs/copy" && method === "POST") {
    const to = JSON.parse(body || "{}").to ?? "";
    const src = walk(root, parts);
    if (!src || src.kind === "dir") return err(400, "cannot copy");
    const dst = resolveVirtual(to);
    if (!dst.root) return err(404, "no such destination");
    const existing = walk(dst.root, dst.parts);
    const target = existing?.kind === "dir" ? [...dst.parts, src.name] : dst.parts;
    writeFile(dst.root, target, src.content ?? "");
    return ok();
  }

  if (rel === "/fs/content" && method === "GET") {
    const n = walk(root, parts);
    if (!n || n.kind === "dir") return err(404, "not a file");
    // Mirror the BFF: serve inline image reads with the real image type (SVG needs it).
    return { status: 200, contentType: imageMime(n.name) ?? "text/plain", body: n.content ?? "" };
  }
  if (rel === "/fs/content" && method === "PUT") {
    writeFile(root, parts, body);
    return ok();
  }
  if (rel === "/fs/upload" && method === "POST") {
    const name = params.get("name") ?? "upload";
    writeFile(root, [...parts, name], body);
    return ok();
  }
  if (rel === "/fs/mkdir" && method === "POST") {
    ensureDir(root, parts);
    return ok();
  }
  if (rel === "/fs/rename" && method === "POST") {
    const toRaw = JSON.parse(body || "{}").to ?? "";
    const to = virtual ? resolveVirtual(toRaw).parts : split(toRaw);
    const n = detach(root, parts);
    if (!n) return err(404, "not found");
    n.name = to[to.length - 1];
    attach(root, to.slice(0, -1), n);
    return ok();
  }
  if (rel === "/fs" && method === "DELETE") {
    if (!detach(root, parts)) return err(404, "not found");
    return ok();
  }
  if (rel === "/fs/stat" && method === "GET") {
    const n = walk(root, parts);
    if (!n) return err(404, "not found");
    return { status: 200, body: JSON.stringify(toEntry(n)) };
  }
  if (rel === "/fs" && method === "GET") {
    const n = walk(root, parts);
    if (!n) return err(404, "not found");
    if (n.kind !== "dir") return err(400, "not a directory");
    const listing: FsListing = {
      source: (params.get("source") as FsListing["source"]) || "local",
      root: virtual ? undefined : params.get("root") || undefined,
      path: virtual ? split(path).join("/") : parts.join("/"),
      entries: sortEntries((n.children ?? []).map(toEntry)),
    };
    return { status: 200, body: JSON.stringify(listing) };
  }
  return err(404, `no mock for ${rel}`);
}

// contentAsset resolves a /fs/content request's query to the referenced real repo asset
// (assets/<name>), or undefined. The standalone dev server uses it to serve real image
// bytes; the in-browser test stub ignores it (it can't read files).
export function contentAsset(params: URLSearchParams): string | undefined {
  const path = params.get("path") ?? "";
  const resolved =
    params.get("source") === "virtual"
      ? resolveVirtual(path)
      : { root: treeFor(params), parts: split(path), atRoot: false };
  if (resolved.atRoot || !resolved.root) return undefined;
  return walk(resolved.root, resolved.parts)?.asset;
}

function ensureDir(root: Node, parts: string[]): Node {
  let cur = root;
  for (const part of parts) {
    let next = cur.children?.find((c) => c.name === part);
    if (!next) {
      next = dir(part, []);
      (cur.children ??= []).push(next);
    }
    cur = next;
  }
  return cur;
}

function writeFile(root: Node, parts: string[], content: string): void {
  const parent = ensureDir(root, parts.slice(0, -1));
  const name = parts[parts.length - 1];
  const existing = parent.children?.find((c) => c.name === name);
  if (existing && existing.kind === "file") existing.content = content;
  else (parent.children ??= []).push(file(name, content));
}

function detach(root: Node, parts: string[]): Node | undefined {
  const parent = walk(root, parts.slice(0, -1));
  if (!parent?.children) return undefined;
  const name = parts[parts.length - 1];
  const i = parent.children.findIndex((c) => c.name === name);
  if (i < 0) return undefined;
  return parent.children.splice(i, 1)[0];
}

function attach(root: Node, parts: string[], node: Node): void {
  ensureDir(root, parts).children!.push(node);
}
