import { describe, expect, it, vi } from "vitest";
import type { Conn } from "./ws.ts";
import { knownAgents, mockTerms } from "./faketerm.ts";
import screens from "./agent-screens.json" with { type: "json" };

// The mock shell can impersonate any agent cornus can classify, so the workspace's
// activity badges, the Overview agent column and the "this session needs you"
// affordances can be tried by hand against `npm run mock` with no backend.
//
// What this file checks is the MOCK's half of that: every agent is playable, the
// state it reports is the one the corpus declares, and the run advances the way a
// real one does. The other half — that those states are what the real detector would
// reach on those same bytes — is not checkable from here, because the manifests are
// TOML read by Go. It is checked there instead:
//   pkg/agentdetect/screens_test.go                  the declared rule is the winner
//   cmd/cornus/internal/webbff/agentscreens_test.go  the same bytes, a real session
// Splitting it that way is deliberate. A mock that classified its own screens would
// agree with itself and prove nothing.
describe("the mock shell impersonates every known agent", () => {
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
      type: (text: string) => handlers.get("message")?.(Buffer.from(text, "utf8"), true),
      output: () => written.join(""),
    };
  }

  function session() {
    const info = mockTerms.create("shop-web", ["/bin/ash"]);
    const io = fakeConn();
    mockTerms.get(info.id)!.attach(io.conn);
    const live = () => mockTerms.list().find((t) => t.id === info.id)!;
    return { ...io, id: info.id, live, kill: () => mockTerms.kill(info.id) };
  }

  const entries = screens.screens as Array<{
    agent: string;
    state: string;
    title: string;
    screen: string[];
  }>;
  const stepsFor = (agent: string) => entries.filter((e) => e.agent === agent);

  it("offers exactly the agents the corpus has screens for", () => {
    expect(knownAgents()).toEqual([...new Set(entries.map((e) => e.agent))].sort());
  });

  // Every agent, driven the way a viewer would: type its name, then answer the prompt
  // it stops on. The state is read off the SESSION LIST — the same JSON the browser
  // polls — rather than off an internal field, so what is asserted is what the UI gets.
  it.each(knownAgents())("plays %s and reports each screen's state", (agent) => {
    vi.useFakeTimers();
    const s = session();
    try {
      const steps = stepsFor(agent);
      s.type(`${agent}\r`);

      for (const [i, step] of steps.entries()) {
        expect(s.live().state).toBe(step.state);
        // The agent is named for as long as it holds the terminal, which is what the
        // Overview column reads and what a shell must NOT claim.
        expect(s.live().agent).toBe(agent);
        expect(s.live().title).toBe(step.title);
        expect(s.output()).toContain(step.screen[step.screen.length - 1]);

        // A blocked screen is waiting for a HUMAN, so time alone must not move it.
        // A demo that answered its own prompt on a timer would still pass every other
        // assertion here while making the one state worth surfacing unobservable.
        if (step.state === "blocked") {
          vi.advanceTimersByTime(60_000);
          expect(s.live().state).toBe("blocked");
        }

        if (i === steps.length - 1) break;
        if (step.state === "blocked") s.type("\r");
        else vi.advanceTimersByTime(3000);
      }

      // Whatever the run ended on, the terminal goes back to being a shell: no agent,
      // idle, and named after the prompt again.
      if (steps[steps.length - 1].state === "blocked") s.type("\r");
      else vi.advanceTimersByTime(3000);
      expect(s.live().agent).toBe("");
      expect(s.live().state).toBe("idle");
      expect(s.live().title).toContain("root@shop");
    } finally {
      s.kill();
      vi.useRealTimers();
    }
  });

  // Each screen must REPLACE the last, not pile up on it. The real detector renders
  // these bytes onto a screen and matches rules against what is left standing, so a
  // demo that scrolled would leave a working footer visible under a blocked prompt —
  // and the backend would then disagree with the state the mock is reporting.
  it("repaints the screen between steps instead of scrolling", () => {
    vi.useFakeTimers();
    const s = session();
    try {
      s.type("claude\r");
      const [working, blocked] = stepsFor("claude");
      vi.advanceTimersByTime(3000);
      const painted = s.output();
      const lastClear = painted.lastIndexOf("\x1b[2J\x1b[H");
      expect(lastClear).toBeGreaterThanOrEqual(0);
      // After the final clear, only the blocked screen is on the terminal.
      const visible = painted.slice(lastClear);
      expect(visible).toContain(blocked.screen[0]);
      expect(visible).not.toContain(working.screen[0]);
    } finally {
      s.kill();
      vi.useRealTimers();
    }
  });

  it("names an agent it cannot play instead of pretending", () => {
    const s = session();
    try {
      s.type("not-an-agent\r");
      expect(s.output()).toContain("command not found");
      expect(s.live().agent).toBe("");
      s.type("agent not-an-agent\r");
      expect(s.output()).toContain("not an agent this mock can play");
      // And it says what it CAN play, so the answer is one command, not a grep.
      expect(s.output()).toContain("claude");
      expect(s.live().agent).toBe("");
      expect(s.live().state).toBe("idle");
    } finally {
      s.kill();
    }
  });

  it("quits back to the shell on Ctrl-C", () => {
    vi.useFakeTimers();
    const s = session();
    try {
      s.type("claude\r");
      expect(s.live().agent).toBe("claude");
      s.type("\x03");
      expect(s.live().agent).toBe("");
      expect(s.live().state).toBe("idle");
    } finally {
      s.kill();
      vi.useRealTimers();
    }
  });
});
