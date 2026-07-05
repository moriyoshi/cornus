package webbff

// The safeguard that decides whether a host directory may be exposed by the file
// explorer at all.
//
// The explorer reaches the developer's real filesystem from two directions: the
// browsable local roots (buildLocalRoots, from every compose bind-mount source) and —
// once the planner lands — a container path resolved back onto its client-local bind
// source. Both must ask the same question, so the predicate lives here rather than in
// either caller.
//
// Why this is needed at all: the BFF has NO authentication. guardHost is a DNS-rebinding
// defence, not an authorization check, so anything reachable here is reachable by any
// page the developer's browser can be persuaded to load against localhost. A compose
// file that says `- /proc:/host/proc:ro` — the standard monitoring idiom — would
// otherwise put /proc/<pid>/environ, and so every local process's API tokens, one GET
// away.
//
// The test is on the FILESYSTEM, not on the path. A denylist of names ("/proc", "/sys")
// is defeated by exactly the case that motivates the check: the mount's whole purpose is
// to make /proc appear somewhere else. statfs answers what the directory really is.

import (
	"path/filepath"
)

// browsableSource reports whether dir may be exposed as a browsable root or used as a
// redirect target. When it may not, reason says why, in words fit to show a user.
//
// dir must already be absolute and symlink-resolved: the caller resolves it anyway to
// anchor containment, and checking an unresolved path would test the wrong filesystem.
func browsableSource(dir string) (reason string, ok bool) {
	if dir != filepath.Clean(dir) || !filepath.IsAbs(dir) {
		return "path is not absolute and clean", false
	}
	// The filesystem root would make the confinement anchor the whole machine, which
	// is the same as having no anchor. Note this deliberately does NOT refuse every
	// mountpoint: a mounted data disk at /mnt/data is an ordinary bounded subtree and
	// refusing it would break legitimate projects.
	if dir == "/" {
		return "the filesystem root cannot be browsed", false
	}
	if name, pseudo := pseudoFilesystem(dir); pseudo {
		return name + " is a kernel pseudo-filesystem and is never browsable", false
	}
	return "", true
}
