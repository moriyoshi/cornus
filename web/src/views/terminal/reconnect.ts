// How a terminal pane reacts when its attach WebSocket closes. The BFF
// (cmd/cornus/internal/webbff/term.go) sends a distinct close code per teardown
// cause so the pane can tell a genuinely ended session — where the only recovery is
// a fresh shell — from a transient drop it should silently reattach through, or a
// takeover by another browser tab.

// Close codes from handleTermAttach's closeFrame(). Keep in sync with term.go.
export const WS_CLOSE_ENDED = 4000; // the session's process exited or it was killed
export const WS_CLOSE_SUPERSEDED = 4001; // a newer attach (another tab) took over

// MAX_REATTACHES bounds silent reconnect attempts for a still-alive session that
// keeps flapping, so a wedged socket eventually surfaces the "ended" prompt instead
// of looping forever. Reset once a reconnect holds (see panes.tsx).
export const MAX_REATTACHES = 5;

export interface SocketExit {
  code: number; // WebSocket CloseEvent.code
  opened: boolean; // did the socket ever open before it closed?
}

// close: the user exited the shell — close the pane, no prompt. elsewhere: another
// tab owns the session — offer a manual reattach here. lost: an unexpected drop we
// couldn't recover (session gone, or too many flaps) — offer Reconnect but keep the
// pane. reattach: a transient drop — reconnect silently.
export type PaneExitAction = "close" | "elsewhere" | "lost" | "reattach";

// paneExitAction decides what a pane does when its socket closes. A supersede is a
// takeover. A clean "ended" close code is the session's process exiting — an explicit
// end, so the pane just closes. A (re)attach that never connected (opened=false, so
// the session is gone) or too many flaps is an unexpected loss. Everything else is a
// transient drop we reattach through without bothering the user.
export function paneExitAction(exit: SocketExit, failures: number): PaneExitAction {
  if (exit.code === WS_CLOSE_SUPERSEDED) return "elsewhere";
  if (exit.code === WS_CLOSE_ENDED) return "close";
  if (!exit.opened || failures >= MAX_REATTACHES) return "lost";
  return "reattach";
}
