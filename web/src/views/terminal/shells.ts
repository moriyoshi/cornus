// The browser's own candidate interactive shells, and how the free-text setting
// that holds them is read.
//
// This list is the LAST of four sources the BFF probes: a workload's own
// `x-cornus-shells:`, then the shell its entrypoint/command names, then the
// selected connection context's `shells:`, then this. It is the fallback that
// makes an ordinary image work without anyone configuring anything.
//
// Sits beside prefix.ts, which plays the same role for the prefix-key presets.

// DEFAULT_SHELL_CANDIDATES is ordered by preference, not by likelihood: an image
// that has both zsh and sh should give you zsh. The busybox entries are last
// because they are the ones that need an applet name, and a distro that has
// /bin/sh as a busybox symlink is already covered by the entry above them.
export const DEFAULT_SHELL_CANDIDATES = [
  "/bin/zsh",
  "/usr/bin/zsh",
  "/bin/bash",
  "/usr/bin/bash",
  "/bin/dash",
  "/usr/bin/dash",
  "/bin/ash",
  "/usr/bin/ash",
  "/bin/sh",
  "/usr/bin/sh",
  "/busybox/sh",
  "/bin/busybox sh",
  "/usr/bin/busybox sh",
].join("\n");

// parseCandidates reads the setting: one candidate per line, blanks and `#`
// comments dropped.
//
// It deliberately does NOT tokenize. "/bin/busybox sh" stays one entry and is
// split server-side with the same shell-words parser Compose uses for
// command/entrypoint — one splitter for every source, so a quoted path behaves
// identically wherever it was written.
export function parseCandidates(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}
