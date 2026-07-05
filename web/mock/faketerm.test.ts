import { describe, expect, it } from "vitest";
import type { Conn } from "./ws.ts";
import { mockTerms } from "./faketerm.ts";
import { handleFs } from "../src/mock/fs.ts";

// The dev server's fake shell REPORTS WHERE IT STANDS, and several things in the workspace
// are built on that report: the file pane that follows a terminal, a split inheriting the
// live directory, and — since a terminal is a transfer destination — files dropped on a
// terminal pane landing in the folder its shell is in. None of those can be tried by hand
// against `npm run mock` unless the mock's own two halves agree: the shell has to report a
// directory that the mock FILESYSTEM actually contains.
//
// These tests drive the persistent-session manager the way the browser does — create,
// attach, type, read the session list — so what they observe is the reported cwd, not an
// internal field. The report travels the same path it does against a real backend: the
// shell emits OSC 7 with its prompt, and the session sniffs its own output for it.
describe("mock terminal sessions report a working directory", () => {
  // A stand-in for one attached browser socket. MockSession uses four things from a Conn —
  // send, close, and the "message"/"close" handlers — so the double implements those and
  // records what was written, which is how a command's OUTPUT is read below.
  function fakeConn() {
    const handlers = new Map<string, (...args: unknown[]) => void>();
    const written: string[] = [];
    const conn = {
      send: (data: Buffer) => void written.push(data.toString("utf8")),
      close: () => {},
      on: (event: string, cb: (...args: unknown[]) => void) => void handlers.set(event, cb),
    };
    return {
      conn: conn as unknown as Conn,
      // type() is a keystroke burst, as the browser sends it: one binary frame.
      type: (text: string) => handlers.get("message")?.(Buffer.from(text, "utf8"), true),
      output: () => written.join(""),
    };
  }

  // Each test makes its own session and kills it, so the manager's list is only ever this
  // test's session and `list()[0]` needs no filtering.
  function session(dir?: string) {
    const info = mockTerms.create("shop-web", ["/bin/ash"], dir);
    const io = fakeConn();
    mockTerms.get(info.id)!.attach(io.conn);
    return {
      ...io,
      id: info.id,
      created: info,
      cwd: () => mockTerms.list().find((t) => t.id === info.id)?.cwd,
      kill: () => mockTerms.kill(info.id),
    };
  }

  it("starts in the directory the create request asked for", () => {
    const s = session("/etc/nginx");
    expect(s.cwd()).toBe("/etc/nginx");
    s.kill();
  });

  it("falls back to the working directory when the container has no such place", () => {
    // "Open in a terminal" can name a folder that this container does not have (the mock's
    // fixtures are not the same tree twice). Reporting it anyway would hand the UI a
    // directory it cannot browse to or drop into, which is worse than being somewhere else.
    const s = session("/no/such/dir");
    expect(s.cwd()).toBe("/app");
    s.kill();
  });

  it("moves the reported directory when the shell cds", () => {
    const s = session();
    expect(s.cwd()).toBe("/app");

    s.type("cd /usr/share/nginx/html\r");
    expect(s.cwd()).toBe("/usr/share/nginx/html");

    // Relative and `..` too, since that is how anyone actually walks a tree.
    s.type("cd ../..\r");
    expect(s.cwd()).toBe("/usr/share");
    s.type("cd nginx/html\r");
    expect(s.cwd()).toBe("/usr/share/nginx/html");
    s.kill();
  });

  it("refuses a cd the container cannot honour, and stays where it was", () => {
    const s = session();
    s.type("cd /nope\r");
    expect(s.output()).toContain("can't cd to /nope");
    expect(s.cwd()).toBe("/app");
    s.kill();
  });

  // The point of all of it: the directory a session reports is a real, writable folder in
  // the SAME mock filesystem the explorer browses, so a terminal pane is a working transfer
  // destination in the mock. Asserted as the transfer itself — the request the UI sends
  // when files are dropped on a terminal — rather than as a path comparison, which would
  // pass just as well against a folder nothing can be written to.
  //
  // BOTH directories a session can report are checked, because they fail differently: the
  // one it STARTS in is a constant the trees have to contain (removing it from the fixture
  // is invisible to every cd-based assertion, which is how this test was first written and
  // why it proved less than it claimed), and the one it CDs to is only reachable because
  // the shell was held to the tree on the way there.
  // The LISTING is asked for first, and it is the load-bearing half. A copy alone cannot
  // answer "does this directory exist?": the mock's transfer creates missing parents
  // (ensureDir), so it reports "ok" for a destination the explorer would 404 on — which is
  // exactly how a fixture with no /app at all passed an earlier version of this test.
  const listing = (dest: string) =>
    handleFs("GET", "/fs", new URL(`http://x/fs?source=virtual&path=${encodeURIComponent(dest)}`), "");
  const transferInto = (dest: string, file: string) =>
    handleFs(
      "POST",
      "/fs/copy",
      new URL(`http://x/fs/copy?source=virtual&path=${encodeURIComponent(dest)}`),
      JSON.stringify({ to: dest, items: [{ from: file }] }),
    );

  it("reports a starting directory files can actually be transferred into", () => {
    const s = session();
    expect(s.cwd()).toBe("/app");
    expect(listing(`shop-web${s.cwd()}`).status).toBe(200);

    const copy = transferInto(`shop-web${s.cwd()}`, "project/compose.yaml");
    expect(copy.status).toBe(200);
    expect(JSON.parse(copy.body).items[0]).toMatchObject({ status: "ok" });

    // And the shell sees what landed, because `ls` reads that same tree.
    s.type("ls\r");
    expect(s.output()).toContain("compose.yaml");
    s.kill();
  });

  it("keeps that true after a cd", () => {
    const s = session();
    s.type("cd /usr/share/nginx/html\r");
    expect(listing(`shop-web${s.cwd()}`).status).toBe(200);

    const copy = transferInto(`shop-web${s.cwd()}`, "project/README.md");
    expect(copy.status).toBe(200);
    expect(JSON.parse(copy.body).items[0]).toMatchObject({ status: "ok" });

    s.type("ls\r");
    expect(s.output()).toContain("README.md");
    s.kill();
  });
});
