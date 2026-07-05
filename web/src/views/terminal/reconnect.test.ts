import { describe, it, expect } from "vitest";
import {
  paneExitAction,
  WS_CLOSE_ENDED,
  WS_CLOSE_SUPERSEDED,
  MAX_REATTACHES,
} from "./reconnect";

// paneExitAction is the whole reconnection policy: given a socket close and how many
// times we've already reattached, decide whether the pane closes, moved to another
// tab, was lost, or should silently reattach. These pin each branch of term.go's
// close codes.
describe("paneExitAction", () => {
  it("treats a superseded close as a takeover by another tab", () => {
    // Even opened=false / high failure count: supersede wins — another tab owns it.
    expect(paneExitAction({ code: WS_CLOSE_SUPERSEDED, opened: true }, 0)).toBe("elsewhere");
    expect(paneExitAction({ code: WS_CLOSE_SUPERSEDED, opened: false }, MAX_REATTACHES)).toBe(
      "elsewhere",
    );
  });

  it("closes the pane on a clean ended close (the user exited the shell)", () => {
    expect(paneExitAction({ code: WS_CLOSE_ENDED, opened: true }, 0)).toBe("close");
  });

  it("reports a loss when a reattach never connected (session is gone)", () => {
    // A transient-looking code but the socket never opened => nothing to attach to.
    expect(paneExitAction({ code: 1006, opened: false }, 0)).toBe("lost");
  });

  it("silently reattaches on a transient drop of a still-open socket", () => {
    expect(paneExitAction({ code: 1006, opened: true }, 0)).toBe("reattach");
    expect(paneExitAction({ code: 1001, opened: true }, MAX_REATTACHES - 1)).toBe("reattach");
  });

  it("reports a loss (offer Reconnect) once the reattach cap is reached", () => {
    expect(paneExitAction({ code: 1006, opened: true }, MAX_REATTACHES)).toBe("lost");
  });
});
