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
// Its shell can also impersonate any agent cornus knows how to classify (`agents`
// lists them, `agent <name>` plays one), replaying real screens from
// agent-screens.json so the activity badges, the agent column and the "needs you"
// affordances have something to report with no backend at all.
//
// startLogStream drives the logs WebSocket (WorkloadDetail.tsx's LogStream): it
// replays a backlog then keeps emitting plausible log lines on a timer.

import { randomUUID } from "node:crypto";
import type { Conn } from "./ws.ts";
// Imported rather than read from disk: this module is loaded both by Node (the dev
// server) and by vitest, and under vitest `import.meta.url` is not a file: URL, so
// resolving a sibling path at runtime works in exactly one of the two.
import screensDoc from "./agent-screens.json" with { type: "json" };
import {
  MOCK_HOME,
  MOCK_WORKDIR,
  containerHasTree,
  containerIsDir,
  containerList,
  containerRead,
} from "../src/mock/fs.ts";

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

// ShellSession is where the fake shell believes it is standing. `workload` is the exact
// fixture name (not the shortened hostname), because it is the key into the mock
// container filesystem — `cd`, `ls` and `cat` are answered from the SAME trees the file
// explorer browses, so the two halves of the mock cannot describe a container
// differently. `prev` backs `cd -`.
export interface ShellSession {
  workload: string;
  host: string;
  user: string;
  cwd: string;
  prev: string;
}
type CommandFn = (argv: string[], s: ShellSession) => string;

// newShell is the starting state of every fake shell. `dir` is what the create request
// asked for (the folder the user opened the terminal from); it is honoured when the
// container actually has it, and falls back to the working directory otherwise — a shell
// claiming to stand somewhere that does not exist would report a cwd the explorer cannot
// follow and files cannot be dropped into.
export function newShell(workload: string, dir?: string): ShellSession {
  const start =
    dir && (!containerHasTree(workload) || containerIsDir(workload, dir)) ? dir : MOCK_WORKDIR;
  return { workload, host: hostOf(workload), user: "root", cwd: start, prev: start };
}

// A believable hostname derived from the workload name (first DNS label).
function hostOf(workload: string): string {
  const base = String(workload || "app").replace(/[^a-zA-Z0-9-]/g, "-");
  return base.split("-")[0] || "app";
}

// resolveDir turns a `cd` argument into an absolute, normalised container path. Relative
// to the session's cwd, with "." and ".." collapsed the way a shell collapses them.
function resolveDir(s: ShellSession, arg: string): string {
  const parts = (arg.startsWith("/") ? arg : `${s.cwd}/${arg}`).split("/");
  const out: string[] = [];
  for (const part of parts) {
    if (part === "" || part === ".") continue;
    if (part === "..") out.pop();
    else out.push(part);
  }
  return `/${out.join("/")}`;
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
  // cd is the reason the rest of this file reads the container trees. Changing directory
  // is what makes a session's REPORTED cwd move (the prompt re-emits OSC 7 after every
  // command), and the workspace hangs several features off that: the follow pairing, a
  // split inheriting where you are, and a file dropped on the terminal landing in the
  // directory you are actually in. A `cd` that only moved a prompt string would leave all
  // of them demonstrable in the mock but never actually exercised.
  cd: (argv, s) => {
    const arg = argv[1] ?? "";
    const target =
      arg === "" || arg === "~" ? MOCK_HOME : arg === "-" ? s.prev : resolveDir(s, arg);
    // A workload with no fixture filesystem cannot contradict anything, so its shell
    // simply goes where it is told; one with a tree is held to it.
    if (containerHasTree(s.workload) && !containerIsDir(s.workload, target)) {
      return `sh: cd: can't cd to ${arg}`;
    }
    s.prev = s.cwd;
    s.cwd = target;
    return "";
  },
  ls: (argv, s) => {
    const arg = argv.slice(1).find((a) => !a.startsWith("-"));
    const entries = containerList(s.workload, arg ? resolveDir(s, arg) : s.cwd);
    // No tree for this workload: the canned listing keeps the standalone demo (and the
    // autoplay script, which runs before any workload is known) looking like a shell.
    if (!entries) {
      if (containerHasTree(s.workload)) return `ls: ${arg}: No such file or directory`;
      return argv.some((a) => /^-[la]+$/.test(a)) ? LS_LONG : LS_SHORT;
    }
    if (!argv.some((a) => /^-[la]+$/.test(a))) {
      return entries.map((e) => e.name).join("  ");
    }
    const rows = entries.map(
      (e) =>
        `${e.kind === "dir" ? "drwxr-xr-x" : "-rw-r--r--"}    1 root     root      ${String(
          e.size,
        ).padStart(8)} Jul 12 09:12 ${e.name}`,
    );
    return [`total ${entries.length * 4}`, ...rows].join("\n");
  },
  cat: (argv, s) => {
    const f = argv[1] || "";
    if (!f) return "cat: missing file operand";
    const content = containerRead(s.workload, resolveDir(s, f));
    if (content !== undefined) return content.replace(/\n$/, "");
    if (containerIsDir(s.workload, resolveDir(s, f))) return `cat: read error: Is a directory`;
    // The canned files stay as a fallback for a workload with no tree, and for the two
    // paths every image has that the fixture does not model.
    if (f === "/etc/os-release") return OS_RELEASE;
    if (!containerHasTree(s.workload)) {
      if (f === "package.json") return PACKAGE_JSON;
      if (f === "app.js" || f === "server.js") return SERVER_JS;
    }
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
  cd  ls  cat  pwd  ps  top  free  df  env  curl  whoami  id  uname  uptime
  date  echo  hostname  node  clear  help  exit  agent  agents
cd, ls and cat read the same mock filesystem the file browser shows, and the
session reports where it stands — so cd somewhere and drop files on this pane.
\`agents\` lists the agents this mock can impersonate; \`agent <name>\` (or just
the name) plays one, so the activity badges have something real to report.
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

// titleSeq is the OSC window-title sequence, the one the BFF sniffs to name a
// session (see cmd/cornus/internal/webbff/osctitle.go). The mock shell emits it
// where a real one would — from its prompt hook, and on entering a command — so
// the demo tabs rename themselves exactly as they do against a live backend.
function titleSeq(text: string): string {
  return `\x1b]0;${text}\x07`;
}

// cwdSeq is OSC 7, the sequence a prompt hook uses to report where the shell is.
// The mock emits it because a stock container image does NOT — which is exactly
// why the demo needs to: without it the follow and split-inherit paths have
// nothing to show.
function cwdSeq(dir: string): string {
  return `\x1b]7;file://${encodeURI(dir)}\x1b\\`;
}

// shellTitle is what a distro prompt hook sets between commands: who and where,
// not what. It is deliberately NOT a program name — the UI has to stay readable
// when the title is this, which is most of the time.
function shellTitle(s: ShellSession): string {
  return `${s.user}@${s.host}: ${s.cwd}`;
}

interface ExecOpts {
  workload?: string;
  cmd?: string[];
  dir?: string;
}

// startExecSession bridges one legacy exec WebSocket to the bogus shell (autoplay
// demo, then interactive on takeover).
export function startExecSession(
  ws: Conn,
  { workload = "app", cmd = [], dir }: ExecOpts = {},
): void {
  const session = newShell(workload, dir);
  const host = session.host;
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
  // cwd mirrors termInfo.Cwd: where the session last said it was, via OSC 7.
  cwd: string;
  // title mirrors termInfo.Title: the window title this session's output last
  // set. Empty until something sets one, which is what the UI's fallback to cmd
  // exists for.
  title: string;
  // agent mirrors termInfo.Agent: which agent the session's FOREGROUND program is,
  // as the manifest set names it — not what the session was launched with. Empty
  // whenever that program is not an agent, which for a shell at a prompt is always.
  agent: string;
}

// ---- the agent demo ---------------------------------------------------------

// AgentScreen is one moment of one agent's terminal, read from agent-screens.json.
// The file is shared with the Go side rather than inlined here, and that sharing is
// the whole design: `state` is what the agent's bundled herdr manifest classifies
// this screen into, checked two ways from Go (see the file's own README), so the mock
// can REPORT a state without owning a classifier and without inventing one.
export interface AgentScreen {
  agent: string;
  state: SessionState;
  rule: string;
  title: string;
  screen: string[];
}

// Screens grouped by agent, in file order — which is the order the demo plays them:
// working, then blocked awaiting an answer, then idle where the manifest has one.
const AGENT_SCREENS: Map<string, AgentScreen[]> = (() => {
  const byAgent = new Map<string, AgentScreen[]>();
  for (const s of screensDoc.screens as AgentScreen[]) {
    byAgent.set(s.agent, [...(byAgent.get(s.agent) ?? []), s]);
  }
  return byAgent;
})();

// knownAgents is every agent the demo can play, which is every agent the bundled
// manifests can classify — a Go test fails if the two ever differ.
export function knownAgents(): string[] {
  return [...AGENT_SCREENS.keys()].sort();
}

// How long a non-blocking screen stays up before the run moves on. A blocked screen
// has no timer at all: it waits for the viewer, because being stuck there until a
// human answers is the state the whole feature exists to surface.
const DEMO_STEP_MS = 2500;

// screenBytes is what the agent would send for one screen. It is deliberately
// identical to emitScreen in cmd/cornus/internal/webbff/agentscreens_test.go — that
// Go test speaks for this demo only while the two agree byte for byte.
//
// The clear-and-home leads because a TUI repaints rather than scrolls. Without it the
// previous step's lines stay on the screen the real detector renders and keep matching
// their own rules, so a blocked prompt would arrive underneath a working footer that
// is still perfectly true.
function screenBytes(s: AgentScreen): string {
  const title = s.title ? titleSeq(s.title) : "";
  return `${CSI}2J${CSI}H${title}${s.screen.map((line) => `${line}\r\n`).join("")}`;
}

function agentsHelp(): string {
  const ids = knownAgents();
  return [
    `${ids.length} agents can be demonstrated — one screen per state the agent's own`,
    `manifest can classify. Start one with \`agent <name>\`, or just type its name:`,
    ``,
    `  ${ids.join("  ")}`,
    ``,
    `A blocked screen waits for you: press Enter to answer it, or Ctrl-C to quit.`,
  ].join("\n");
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
  private title = "";
  private cwd = "";
  private sub?: Conn;
  private line = "";
  private escSwallow = 0;
  // demo is the scripted agent run, or undefined when this session is just a shell.
  // `step` indexes that agent's screens in agent-screens.json order.
  private demo?: { agent: string; steps: AgentScreen[]; step: number };
  private demoTimer?: ReturnType<typeof setTimeout>;
  private shell: ShellSession;
  private onEnd: (id: string) => void;

  constructor(
    id: string,
    workload: string,
    cmd: string[],
    dir: string | undefined,
    onEnd: (id: string) => void,
  ) {
    this.id = id;
    this.workload = workload;
    this.cmd = cmd;
    this.onEnd = onEnd;
    // The requested directory is honoured here and NOWHERE else: a session reports where
    // it stands, and a shell that ignored `dir` would make every terminal in the mock
    // claim the same working directory however it was opened.
    this.shell = newShell(workload, dir);
    this.emit(
      `${DIM}Connected to ${this.shell.host} (mock persistent session). Type commands; reload the page to reattach.${RESET}\r\n`,
    );
    this.emitPrompt();
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
      title: this.title,
      cwd: this.cwd,
      // A shell at a prompt is not an agent, and the real BFF reports nothing for
      // one — the agent is the FOREGROUND program, which here is the scripted run.
      agent: this.demo?.agent ?? "",
    };
  }

  // computeState REPORTS the state of the screen currently on show; it does not
  // decide it. The state comes from agent-screens.json, where it is the verdict the
  // agent's own manifest reaches on that screen, checked from Go. The mock has no
  // classifier, and a state invented here is precisely how a demo starts showing
  // badges the real backend would never produce — which is what the previous
  // hand-written flow did: its "Apply 3 changes? [y/n]" screen matched no rule in
  // claude's manifest at all, so the real detector would have called it idle.
  private computeState(): SessionState {
    return this.demo ? this.demo.steps[this.demo.step].state : "idle";
  }

  // startAgentDemo plays one agent's screens: a working screen, the prompt it stops
  // on, and (for the agents whose manifest can say so) the idle screen it returns to.
  private startAgentDemo(id: string): void {
    const steps = AGENT_SCREENS.get(id);
    if (!steps) {
      this.emit(`${CSI}31msh: ${id}: not an agent this mock can play${RESET}\r\n`);
      this.emit(nl(agentsHelp()) + "\r\n");
      this.emitPrompt();
      return;
    }
    this.demo = { agent: id, steps, step: -1 };
    this.advanceDemo();
  }

  // advanceDemo shows the next screen. A blocked one is left up with no timer: it is
  // waiting for a human, which is the entire point of reporting it.
  private advanceDemo(): void {
    if (!this.demo) return;
    const step = this.demo.step + 1;
    if (step >= this.demo.steps.length) {
      this.endDemo();
      return;
    }
    this.demo.step = step;
    const screen = this.demo.steps[step];
    this.emit(screenBytes(screen));
    clearTimeout(this.demoTimer);
    this.demoTimer = undefined;
    if (screen.state === "blocked") return;
    this.demoTimer = setTimeout(() => {
      if (this.alive) this.advanceDemo();
    }, DEMO_STEP_MS);
  }

  // endDemo returns the terminal to the shell — including the title, which is how a
  // tab stops being named after a program that has exited.
  private endDemo(): void {
    clearTimeout(this.demoTimer);
    this.demoTimer = undefined;
    const id = this.demo?.agent;
    this.demo = undefined;
    this.emit(`${CSI}2J${CSI}H`);
    if (id) this.emit(`${DIM}${id} exited.${RESET}\r\n`);
    this.emitPrompt();
  }

  private emit(s: string): void {
    // Sniff the title off the outgoing stream rather than setting it at the call
    // sites, so the mock learns what it is running the same way the BFF does:
    // from the bytes. Last one wins within a chunk, as in the Go scanner. The
    // mock builds well-formed sequences that never straddle a chunk, so a regex
    // is enough here; the real scanner is a state machine for that reason.
    const titles = [...s.matchAll(/\x1b\][02];([^\x07\x1b]*)(?:\x07|\x1b\\)/g)];
    if (titles.length) this.title = titles[titles.length - 1][1];
    const dirs = [...s.matchAll(/\x1b\]7;file:\/\/[^/\x07\x1b]*([^\x07\x1b]*)(?:\x07|\x1b\\)/g)];
    if (dirs.length) this.cwd = decodeURI(dirs[dirs.length - 1][1]);
    this.ring += s;
    if (this.ring.length > RING_CAP) this.ring = this.ring.slice(this.ring.length - RING_CAP);
    if (this.sub) this.sub.send(Buffer.from(s, "utf8"), true);
  }

  // emitPrompt writes the prompt AND the title that goes with it — the pair a
  // real shell's prompt hook emits together, which is what returns a tab's name
  // to the shell once the program running in it exits.
  private emitPrompt(): void {
    this.emit(titleSeq(shellTitle(this.shell)));
    this.emit(cwdSeq(this.shell.cwd));
    this.emit(promptFor(this.shell));
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
    clearTimeout(this.demoTimer);
    this.demoTimer = undefined;
    this.sub?.close(WS_CLOSE_ENDED, "ended");
    this.onEnd(this.id);
  }

  private execLine(): void {
    this.emit("\r\n");
    const trimmed = this.line.trim();
    this.line = "";
    const argv = trimmed.split(/\s+/);
    const word = argv[0];
    // Keystrokes during a scripted run go to the agent, not to the shell. Answering
    // the prompt it stopped on advances it, which is what a real answer does: the
    // detector clears blocked on stdin and then re-reads the screen.
    if (this.demo) {
      if (this.demo.steps[this.demo.step].state === "blocked") this.advanceDemo();
      else this.endDemo();
      return;
    }
    if (word === "exit" || word === "logout") {
      this.emit(`${DIM}logout${RESET}\r\n`);
      this.end();
      return;
    }
    if (word === "clear") {
      this.emit(`${CSI}2J${CSI}H`);
      this.emitPrompt();
      return;
    }
    if (word === "agents") {
      this.emit(nl(agentsHelp()) + "\r\n");
      this.emitPrompt();
      return;
    }
    // `agent <name>` starts a run, and so does the agent's own name — typing
    // `claude` is what someone reaching for it would do anyway.
    if (word === "agent" || AGENT_SCREENS.has(word)) {
      this.startAgentDemo(word === "agent" ? (argv[1] ?? "claude") : word);
      return;
    }
    // A prompt hook sets the title to the command as it starts and back to the
    // prompt title when it returns, so the tab tracks what is running.
    if (trimmed) this.emit(titleSeq(trimmed));
    this.emit(runCommand(trimmed, this.shell));
    this.emitPrompt();
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
        // Ctrl-C quits the agent rather than the shell it was started from, which is
        // what it does against a real one.
        if (this.demo) {
          this.endDemo();
          continue;
        }
        this.emit("^C\r\n");
        this.line = "";
        this.emitPrompt();
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

  create(workload: string, cmd: string[], dir?: string): TermInfo {
    const id = randomUUID();
    const s = new MockSession(id, workload, cmd, dir, (dead) => this.sessions.delete(dead));
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
