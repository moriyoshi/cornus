// Bogus interactive shell + log stream for the mock BFF, so the exec, logs, and
// tiled-terminal panes have something believable to render in demos and
// screenshots with no real workload behind them.
//
// startExecSession drives the legacy exec WebSocket (Term.tsx, WorkloadDetail's
// exec tab): on connect it auto-plays a scripted shell session — commands are
// "typed" character by character, each followed by canned output — and loops
// forever, which is the "interaction loop" you can point a screen recorder at.
// The moment the viewer presses a key, the autoplay stops and the same canned
// command table backs a real interactive prompt. Bytes go over binary frames both
// ways; text frames carry {"resize":{h,w}} control, matching handleExecWS.
//
// mockTerms is the persistent-session manager backing the tiled workspace: each
// created session keeps a real interactive prompt alive in the mock process with
// a replay ring buffer, so browser sockets can attach/detach and a page reload
// reattaches to the same shell — the same contract as cmd/cornus/webterm.go.
//
// startLogStream drives the logs WebSocket (WorkloadDetail.tsx's LogStream): it
// replays a backlog then keeps emitting plausible log lines on a timer.

import { randomUUID } from "node:crypto";
import type { Conn } from "./ws.ts";

// WebSocket close codes the real BFF's persistent-session attach handler sends (see
// closeFrame in cmd/cornus/internal/webbff/term.go): the browser reads these to tell
// an ended session from a takeover by another tab. The mock must send the same codes
// or the client (paneExitAction) mistakes an ended session for a transient drop and
// flaps through reattaches before giving up.
const WS_CLOSE_ENDED = 4000;
const WS_CLOSE_SUPERSEDED = 4001;

const CSI = "\x1b[";
const RESET = `${CSI}0m`;
const DIM = `${CSI}90m`;
const GREEN = `${CSI}1;32m`;
const BLUE = `${CSI}1;34m`;
const YELLOW = `${CSI}33m`;
const CYAN = `${CSI}36m`;

interface ShellSession {
  host: string;
  user: string;
  cwd: string;
}
type CommandFn = (argv: string[], s: ShellSession) => string;

// A believable hostname derived from the workload name (first DNS label).
function hostOf(workload: string): string {
  const base = String(workload || "app").replace(/[^a-zA-Z0-9-]/g, "-");
  return base.split("-")[0] || "app";
}

// COMMANDS maps a command word to a function of (argv, session) returning the
// canned stdout (with \n line endings; the caller rewrites them to \r\n). Shared
// by the autoplay script and the interactive prompt so both stay consistent.
const COMMANDS: Record<string, CommandFn> = {
  whoami: () => "root",
  id: () => "uid=0(root) gid=0(root) groups=0(root)",
  pwd: (_a, s) => s.cwd,
  hostname: (_a, s) => s.host,
  uname: (argv, s) =>
    argv.includes("-a") ? `Linux ${s.host} 6.1.0-cornus #1 SMP x86_64 GNU/Linux` : "Linux",
  date: () => "Thu Jul 12 09:41:23 UTC 2026",
  echo: (argv) => argv.slice(1).join(" "),
  ls: (argv) =>
    argv.includes("-l") || argv.includes("-la") || argv.includes("-al") ? LS_LONG : LS_SHORT,
  cat: (argv) => {
    const f = argv[1] || "";
    if (f === "/etc/os-release") return OS_RELEASE;
    if (f === "package.json") return PACKAGE_JSON;
    if (f === "app.js" || f === "server.js") return SERVER_JS;
    if (!f) return "cat: missing file operand";
    return `cat: ${f}: No such file or directory`;
  },
  ps: () => PS_AUX,
  top: () => TOP_SNAPSHOT,
  free: () => FREE_OUT,
  df: () => DF_OUT,
  uptime: () => " 09:41:23 up 3 days,  4:12,  0 users,  load average: 0.08, 0.12, 0.09",
  env: () => ENV_OUT,
  printenv: () => ENV_OUT,
  curl: (argv) => {
    const u = argv.find(
      (a) => a.startsWith("http") || a.includes("localhost") || a.includes("127.0.0.1"),
    );
    if (u && u.includes("healthz")) return '{"status":"ok","uptime":"3d4h"}';
    if (u) return "<!doctype html>\n<title>cornus demo</title>\n<h1>It works.</h1>";
    return "curl: try 'curl --help' or 'curl --manual' for more information";
  },
  node: (argv) => (argv.includes("-v") || argv.includes("--version") ? "v22.6.0" : ""),
  help: () => HELP,
};

const LS_SHORT = "app.js  node_modules  package.json  package-lock.json  public  README.md";
const LS_LONG = `total 92
drwxr-xr-x    1 root     root          4096 Jul 12 09:12 .
drwxr-xr-x    1 root     root          4096 Jul 12 09:12 ..
-rw-r--r--    1 root     root          1843 Jul 12 09:12 app.js
drwxr-xr-x  184 root     root          4096 Jul 12 09:12 node_modules
-rw-r--r--    1 root     root           612 Jul 12 09:12 package.json
-rw-r--r--    1 root     root         48213 Jul 12 09:12 package-lock.json
drwxr-xr-x    2 root     root          4096 Jul 12 09:12 public
-rw-r--r--    1 root     root           418 Jul 12 09:12 README.md`;

const OS_RELEASE = `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.20.1
PRETTY_NAME="Alpine Linux v3.20"
HOME_URL="https://alpinelinux.org/"`;

const PACKAGE_JSON = `{
  "name": "demo-web",
  "version": "1.4.2",
  "private": true,
  "scripts": { "start": "node app.js" },
  "dependencies": { "express": "^4.19.2" }
}`;

const SERVER_JS = `const express = require("express");
const app = express();
app.get("/healthz", (_req, res) => res.json({ status: "ok" }));
app.get("/", (_req, res) => res.send("It works."));
app.listen(8080, () => console.log("listening on :8080"));`;

const PS_AUX = `PID   USER     TIME  COMMAND
    1 root      0:00 node app.js
   27 root      0:00 /bin/sh
   34 root      0:00 ps aux`;

const TOP_SNAPSHOT = `Mem: 412536K used, 1623220K free, 2884K shrd, 18432K buff, 214980K cached
CPU:   2% usr   1% sys   0% nic  96% idle   0% io   0% irq   0% sirq
Load average: 0.08 0.12 0.09 1/214 34
  PID  PPID USER     STAT   VSZ %VSZ %CPU COMMAND
    1     0 root     S     612m  30%   1% node app.js
   27     0 root     S     1636   0%   0% /bin/sh`;

const FREE_OUT = `              total        used        free      shared  buff/cache   available
Mem:        2035756      412536     1623220        2884      214980     1489012
Swap:             0           0           0`;

const DF_OUT = `Filesystem                Size      Used Available Use% Mounted on
overlay                  58.4G     12.1G     43.3G  22% /
tmpfs                    64.0M         0     64.0M   0% /dev
/dev/vda1                58.4G     12.1G     43.3G  22% /etc/hosts`;

const ENV_OUT = `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
HOSTNAME=web
NODE_VERSION=22.6.0
NODE_ENV=production
PORT=8080
HOME=/root
TERM=xterm-256color`;

const HELP = `This is a mock shell for cornus demos. Recognised commands:
  ls  cat  ps  top  free  df  env  curl  whoami  id  uname  uptime
  date  echo  hostname  node  clear  help  exit
Anything else prints a 'command not found', just like a real shell.`;

// The autoplay script: each entry is a command line that gets typed out and run
// through COMMANDS. Keep it varied and short so a screenshot lands on something
// interesting no matter when it is taken.
const DEMO_SCRIPT = [
  "whoami",
  "uname -a",
  "cat /etc/os-release",
  "ls -la",
  "ps aux",
  "curl -s localhost:8080/healthz",
  "node -v",
  "df -h",
];

function nl(s: string): string {
  return s.replace(/\n/g, "\r\n");
}

// runCommand resolves a command line against COMMANDS and returns the stdout with
// CRLF endings, or a not-found message. Empty input yields "".
function runCommand(line: string, session: ShellSession): string {
  const trimmed = line.trim();
  if (!trimmed) return "";
  const argv = trimmed.split(/\s+/);
  const fn = COMMANDS[argv[0]];
  if (fn) {
    const out = fn(argv, session);
    return out ? nl(out) + "\r\n" : "";
  }
  return `${CSI}31msh: ${argv[0]}: command not found${RESET}\r\n`;
}

function promptFor(s: ShellSession): string {
  return `${GREEN}${s.user}@${s.host}${RESET}:${BLUE}${s.cwd}${RESET}# `;
}

interface ExecOpts {
  workload?: string;
  cmd?: string[];
}

// startExecSession bridges one legacy exec WebSocket to the bogus shell (autoplay
// demo, then interactive on takeover).
export function startExecSession(ws: Conn, { workload = "app", cmd = [] }: ExecOpts = {}): void {
  const host = hostOf(workload);
  const session: ShellSession = { host, user: "root", cwd: "/app" };
  const prompt = () => promptFor(session);
  const out = (s: string) => ws.send(Buffer.from(s, "utf8"), true);

  // A one-shot command (e.g. `cmd=ps aux`) runs once and the session ends,
  // mirroring a real non-shell exec. A shell gets the demo.
  const joined = cmd.map(String).join(" ").trim();
  const isShell = joined === "" || /(^|\/)(ash|sh|bash|zsh)$/.test(joined) || joined === "/bin/sh";

  let mode: "demo" | "interactive" = "demo";
  let alive = true;
  let line = "";
  let escSwallow = 0; // >0 while consuming an ANSI escape sequence
  // Pending autoplay sleeps, so a takeover or close can wake them immediately.
  const pending = new Set<{ resolve: () => void; timer: ReturnType<typeof setTimeout> }>();
  const wait = (ms: number) =>
    new Promise<void>((resolve) => {
      const entry = {
        resolve,
        timer: setTimeout(() => {
          pending.delete(entry);
          resolve();
        }, ms),
      };
      pending.add(entry);
    });
  const clearTimers = () => {
    for (const entry of pending) {
      clearTimeout(entry.timer);
      entry.resolve();
    }
    pending.clear();
  };

  if (!isShell) {
    out(runCommand(joined, session) || "");
    ws.close(1000, "");
    return;
  }

  const playing = () => alive && mode === "demo";

  async function autoplay() {
    out(`${DIM}Connected to ${host} (mock). Press any key to take over the shell.${RESET}\r\n`);
    await wait(700);
    while (playing()) {
      for (const command of DEMO_SCRIPT) {
        if (!playing()) return;
        out(prompt());
        await wait(400);
        for (const ch of command) {
          if (!playing()) return;
          out(ch);
          await wait(45 + Math.floor(Math.random() * 55));
        }
        await wait(280);
        if (!playing()) return;
        out("\r\n");
        out(runCommand(command, session));
        await wait(1100);
      }
      if (!playing()) return;
      await wait(900);
      if (!playing()) return;
      out(`${CSI}2J${CSI}H`); // clear before looping so it stays screenshot-clean
    }
  }

  function takeOver() {
    mode = "interactive";
    clearTimers();
    out(`\r\n${DIM}[you have the shell]${RESET}\r\n`);
    out(prompt());
  }

  function execLine() {
    out("\r\n");
    const trimmed = line.trim();
    line = "";
    const word = trimmed.split(/\s+/)[0];
    if (word === "exit" || word === "logout") {
      out(`${DIM}logout${RESET}\r\n`);
      ws.close(1000, "");
      return;
    }
    if (word === "clear") {
      out(`${CSI}2J${CSI}H`);
      out(prompt());
      return;
    }
    out(runCommand(trimmed, session));
    out(prompt());
  }

  function onKeys(text: string) {
    for (const ch of text) {
      const code = ch.codePointAt(0)!;
      if (escSwallow > 0) {
        if (ch === "[" || ch === "O" || (code >= 0x30 && code <= 0x3f)) continue;
        escSwallow = 0;
        continue;
      }
      if (code === 0x1b) {
        escSwallow = 1;
        continue;
      }
      if (ch === "\r" || ch === "\n") {
        execLine();
        continue;
      }
      if (code === 0x7f || code === 0x08) {
        if (line.length > 0) {
          line = line.slice(0, -1);
          out("\b \b");
        }
        continue;
      }
      if (code === 0x03) {
        out("^C\r\n");
        line = "";
        out(prompt());
        continue;
      }
      if (code === 0x04) {
        if (line.length === 0) {
          out(`${DIM}logout${RESET}\r\n`);
          ws.close(1000, "");
          return;
        }
        continue;
      }
      if (code < 0x20) continue;
      line += ch;
      out(ch);
    }
  }

  ws.on("message", (data: Buffer, isBinary: boolean) => {
    if (!isBinary) return; // text frames are resize control; nothing to redraw
    if (mode === "demo") {
      takeOver();
    }
    onKeys(data.toString("utf8"));
  });
  const shutdown = () => {
    alive = false;
    clearTimers();
  };
  ws.on("close", shutdown);
  ws.on("error", shutdown);

  autoplay();
}

// ---- persistent terminal sessions (tiled workspace) -------------------------

// SessionState mirrors the Go detector's states (see agentdetect.go): a mock
// session reports one so the tiled-workspace badge and Overview list have
// something to render with no real backend.
export type SessionState = "idle" | "working" | "blocked";

export interface TermInfo {
  id: string;
  workload: string;
  cmd: string[];
  alive: boolean;
  rows: number;
  cols: number;
  created: string;
  state: SessionState;
}

const RING_CAP = 16 * 1024;

// MockSession is one persistent bogus shell: an interactive prompt kept alive in
// the mock process, with a replay ring buffer, that browser sockets attach to and
// detach from. Mirrors termSession in cmd/cornus/webterm.go closely enough to
// exercise the reload/reattach UI.
class MockSession {
  readonly id: string;
  readonly workload: string;
  readonly cmd: string[];
  readonly created = new Date().toISOString();
  alive = true;
  rows = 24;
  cols = 80;

  private ring = "";
  private sub?: Conn;
  private line = "";
  private escSwallow = 0;
  private working = false;
  private blockedPrompt = false;
  private shell: ShellSession;
  private onEnd: (id: string) => void;

  constructor(id: string, workload: string, cmd: string[], onEnd: (id: string) => void) {
    this.id = id;
    this.workload = workload;
    this.cmd = cmd;
    this.onEnd = onEnd;
    this.shell = { host: hostOf(workload), user: "root", cwd: "/app" };
    this.emit(
      `${DIM}Connected to ${this.shell.host} (mock persistent session). Type commands; reload the page to reattach.${RESET}\r\n`,
    );
    this.emit(promptFor(this.shell));
  }

  info(): TermInfo {
    return {
      id: this.id,
      workload: this.workload,
      cmd: this.cmd,
      alive: this.alive,
      rows: this.rows,
      cols: this.cols,
      created: this.created,
      state: this.alive ? this.computeState() : "idle",
    };
  }

  // computeState mirrors the Go detector's output for the UI, driven by the
  // scripted agent flow: a pending approval prompt is blocked, an active churn is
  // working, else idle. (The real backend derives this from the rendered screen.)
  private computeState(): SessionState {
    if (this.blockedPrompt) return "blocked";
    if (this.working) return "working";
    return "idle";
  }

  // startAgentDemo scripts a believable agent run for screenshots: it churns for a
  // couple of seconds (working) then stops on an approval prompt (blocked) until
  // the user answers.
  private startAgentDemo(): void {
    this.working = true;
    this.emit(`${DIM}Analyzing repository…${RESET}\r\n`);
    this.emit(`Working (esc to interrupt)\r\n`);
    setTimeout(() => {
      if (!this.alive) return;
      this.working = false;
      this.blockedPrompt = true;
      this.emit(`\r\nApply 3 changes to this deployment? [y/n] `);
    }, 2500);
  }

  private emit(s: string): void {
    this.ring += s;
    if (this.ring.length > RING_CAP) this.ring = this.ring.slice(this.ring.length - RING_CAP);
    if (this.sub) this.sub.send(Buffer.from(s, "utf8"), true);
  }

  attach(ws: Conn): void {
    if (this.sub) this.sub.close(WS_CLOSE_SUPERSEDED, "superseded");
    this.sub = ws;
    if (this.ring) ws.send(Buffer.from(this.ring, "utf8"), true);
    if (!this.alive) {
      ws.close(WS_CLOSE_ENDED, "ended");
      return;
    }
    ws.on("message", (data: Buffer, isBinary: boolean) => {
      if (!isBinary) return; // resize control frame
      this.onKeys(data.toString("utf8"));
    });
    const drop = () => {
      if (this.sub === ws) this.sub = undefined;
    };
    ws.on("close", drop);
    ws.on("error", drop);
  }

  kill(): void {
    this.end();
  }

  private end(): void {
    if (!this.alive) return;
    this.alive = false;
    this.sub?.close(WS_CLOSE_ENDED, "ended");
    this.onEnd(this.id);
  }

  private execLine(): void {
    this.emit("\r\n");
    const trimmed = this.line.trim();
    this.line = "";
    const word = trimmed.split(/\s+/)[0];
    // Answering the scripted agent's approval prompt resolves it and clears the
    // blocked state (the real detector clears on stdin, then re-evaluates output).
    if (this.blockedPrompt) {
      this.blockedPrompt = false;
      this.emit(`${DIM}Applying changes… done.${RESET}\r\n`);
      this.emit(promptFor(this.shell));
      return;
    }
    if (word === "exit" || word === "logout") {
      this.emit(`${DIM}logout${RESET}\r\n`);
      this.end();
      return;
    }
    if (word === "clear") {
      this.emit(`${CSI}2J${CSI}H`);
      this.emit(promptFor(this.shell));
      return;
    }
    if (word === "agent" || word === "claude") {
      this.startAgentDemo();
      return;
    }
    this.emit(runCommand(trimmed, this.shell));
    this.emit(promptFor(this.shell));
  }

  private onKeys(text: string): void {
    for (const ch of text) {
      const code = ch.codePointAt(0)!;
      if (this.escSwallow > 0) {
        if (ch === "[" || ch === "O" || (code >= 0x30 && code <= 0x3f)) continue;
        this.escSwallow = 0;
        continue;
      }
      if (code === 0x1b) {
        this.escSwallow = 1;
        continue;
      }
      if (ch === "\r" || ch === "\n") {
        this.execLine();
        continue;
      }
      if (code === 0x7f || code === 0x08) {
        if (this.line.length > 0) {
          this.line = this.line.slice(0, -1);
          this.emit("\b \b");
        }
        continue;
      }
      if (code === 0x03) {
        this.emit("^C\r\n");
        this.line = "";
        this.emit(promptFor(this.shell));
        continue;
      }
      if (code === 0x04) {
        if (this.line.length === 0) {
          this.emit(`${DIM}logout${RESET}\r\n`);
          this.end();
          return;
        }
        continue;
      }
      if (code < 0x20) continue;
      this.line += ch;
      this.emit(ch);
    }
  }
}

class MockTermManager {
  private sessions = new Map<string, MockSession>();

  create(workload: string, cmd: string[]): TermInfo {
    const id = randomUUID();
    const s = new MockSession(id, workload, cmd, (dead) => this.sessions.delete(dead));
    this.sessions.set(id, s);
    return s.info();
  }

  get(id: string): MockSession | undefined {
    return this.sessions.get(id);
  }

  list(): TermInfo[] {
    return [...this.sessions.values()].map((s) => s.info());
  }

  kill(id: string): boolean {
    const s = this.sessions.get(id);
    if (!s) return false;
    s.kill();
    this.sessions.delete(id);
    return true;
  }
}

export const mockTerms = new MockTermManager();

// ---- log stream -------------------------------------------------------------

// Plausible log lines the stream cycles through after the backlog.
const LOG_TEMPLATES: Array<(t: string) => string> = [
  (t) => `${t} ${CYAN}INFO${RESET}  request completed method=GET path=/ status=200 dur=3ms`,
  (t) => `${t} ${CYAN}INFO${RESET}  request completed method=GET path=/healthz status=200 dur=1ms`,
  (t) => `${t} ${CYAN}INFO${RESET}  request completed method=POST path=/api/orders status=201 dur=27ms`,
  (t) => `${t} ${YELLOW}WARN${RESET}  slow query detected table=orders dur=812ms`,
  (t) => `${t} ${CYAN}INFO${RESET}  cache hit key=session:8a1f ratio=0.94`,
  (t) => `${t} ${CYAN}INFO${RESET}  gc pause=2.1ms heap=48MB`,
];

function clock(base: number, i: number): string {
  const s = (base + i) % 86400;
  const hh = String(Math.floor(s / 3600)).padStart(2, "0");
  const mm = String(Math.floor((s % 3600) / 60)).padStart(2, "0");
  const ss = String(s % 60).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

interface LogOpts {
  workload?: string;
}

// startLogStream feeds the logs pane: a short backlog, then live lines forever.
export function startLogStream(ws: Conn, { workload = "app" }: LogOpts = {}): void {
  const host = hostOf(workload);
  const out = (s: string) => ws.send(Buffer.from(s + "\r\n", "utf8"), true);
  const base = 9 * 3600 + 41 * 60; // 09:41:00
  let i = 0;

  out(`${DIM}[mock] streaming logs for ${host} — Ctrl-C in the pane to stop${RESET}`);
  out(`${clock(base, 0)} ${CYAN}INFO${RESET}  server listening on :8080`);
  for (let k = 1; k <= 6; k++) {
    out(LOG_TEMPLATES[k % LOG_TEMPLATES.length](clock(base, k)));
  }
  i = 7;

  let timer: ReturnType<typeof setTimeout> | null = null;
  const tick = () => {
    out(LOG_TEMPLATES[i % LOG_TEMPLATES.length](clock(base, i)));
    i += 1;
    timer = setTimeout(tick, 900 + Math.floor(Math.random() * 700));
  };
  timer = setTimeout(tick, 900);

  const stop = () => {
    if (timer) clearTimeout(timer);
    timer = null;
  };
  ws.on("close", stop);
  ws.on("error", stop);
}
